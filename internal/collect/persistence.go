package collect

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
)

// StoredBatch сохраняет идентификаторы даже при частичной ошибке записи.
// Миграции вызываются отдельно, до запуска сбора.
type StoredBatch struct {
	Results []PropertyResult
	Summary PropertiesSummary
	Runs    []storage.Run
	JobIDs  []string // В том же порядке, что Results.
}

// CollectPropertiesStored использует прежний executor и сохраняет КАЖДУЮ попытку,
// включая retry. Ошибка storage возвращается отдельно от результатов источника;
// следующие HTTP-попытки этого задания при ошибке записи не запускаются.
func CollectPropertiesStored(ctx context.Context, store *storage.Store, properties []PropertyCollection, options BatchOptions) (StoredBatch, error) {
	var batch StoredBatch
	if store == nil {
		return batch, fmt.Errorf("storage is required")
	}
	if options.Workers < 0 || options.Workers > MaxWorkers || options.StartInterval < 0 {
		return batch, fmt.Errorf("invalid batch options")
	}
	if _, err := options.Retry.normalized(); err != nil {
		return batch, err
	}
	type planned struct{ scenario storage.Scenario }
	plannedJobs := make([]planned, 0)
	ranges := make(map[string]storage.RunInput)
	sourceByProperty := make(map[string]string)
	order := make([]string, 0)
	for _, property := range properties {
		if err := property.Hotel.Validate(); err != nil {
			return batch, err
		}
		requests, err := configuredRequests(property.Hotel, property.CheckIn)
		if err != nil {
			return batch, err
		}
		origin, err := SourceKey(property.Hotel.Source.BaseURL)
		if err != nil {
			return batch, err
		}
		sourceIdentity := property.Hotel.Source.Type + "/" + origin + "/" + property.Hotel.Source.PropertyID
		if previous, ok := sourceByProperty[property.Hotel.ID]; ok && previous != sourceIdentity {
			return batch, fmt.Errorf("conflicting property sources")
		}
		sourceByProperty[property.Hotel.ID] = sourceIdentity
		for _, req := range requests {
			scenario := storage.Scenario{PropertyID: property.Hotel.ID, Source: property.Hotel.Source.Type, SourceOrigin: origin, SourcePropertyID: req.PropertyID, CheckIn: req.CheckIn, CheckOut: req.CheckOut, LOS: int(civilDays(req.CheckIn, req.CheckOut)), Adults: req.Adults, Rooms: property.Hotel.Collection.Rooms, ChildrenAges: append([]int{}, req.ChildrenAges...), Language: req.Language}
			plannedJobs = append(plannedJobs, planned{scenario})
			span, ok := ranges[property.Hotel.ID]
			if !ok {
				order = append(order, property.Hotel.ID)
				span = storage.RunInput{PropertyID: property.Hotel.ID, PeriodStart: req.CheckIn, PeriodEnd: req.CheckOut}
			}
			if req.CheckIn.Before(span.PeriodStart) {
				span.PeriodStart = req.CheckIn
			}
			if req.CheckOut.After(span.PeriodEnd) {
				span.PeriodEnd = req.CheckOut
			}
			ranges[property.Hotel.ID] = span
		}
	}
	runIDs := make(map[string]string)
	for _, propertyID := range order {
		run, err := store.CreateRun(ctx, ranges[propertyID])
		if err != nil {
			return batch, err
		}
		batch.Runs = append(batch.Runs, run)
		runIDs[propertyID] = run.ID
	}
	for _, job := range plannedJobs {
		created, err := store.CreateJob(ctx, runIDs[job.scenario.PropertyID], job.scenario)
		if err != nil {
			return batch, err
		}
		batch.JobIDs = append(batch.JobIDs, created.ID)
	}
	observer := &attemptObserver{
		before: func(ctx context.Context, index, number int, started time.Time) error {
			return store.MarkRunning(ctx, batch.JobIDs[index], number, started)
		},
		after: func(ctx context.Context, done completedAttempt, number int, next *time.Time) error {
			// Завершение фактически выполненного HTTP должно попасть в историю и после
			// отмены collection context. Отдельное ожидание записи ограничено пятью секундами.
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			input := storageAttempt(done.result, batch.JobIDs[done.index], number, done.startedAt, done.finishedAt, done.durationMS, done.queueWaitMS, next)
			_, err := store.SaveAttempt(writeCtx, input)
			return err
		},
	}
	results, summary, err := collectPropertiesObserved(ctx, properties, options, observer)
	batch.Results = results
	batch.Summary = summary
	for i, run := range batch.Runs {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if ctx.Err() != nil {
			code := "canceled"
			if ctx.Err() == context.DeadlineExceeded {
				code = "timeout"
			}
			err = errors.Join(err, store.CancelPending(writeCtx, run.ID, code))
		}
		finished, finishErr := store.FinishRun(writeCtx, run.ID)
		cancel()
		if finishErr != nil {
			err = errors.Join(err, finishErr)
		} else {
			batch.Runs[i] = finished
		}
	}
	return batch, err
}

func civilDays(in, out time.Time) int {
	a, _ := time.Parse("2006-01-02", in.Format("2006-01-02"))
	b, _ := time.Parse("2006-01-02", out.Format("2006-01-02"))
	return int(b.Sub(a) / (24 * time.Hour))
}

var diagnosticCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
var publicLink = regexp.MustCompile(`(?i)https?://[^\s<>]+`)

func storedWarnings(warnings []bnovo.ParseWarning) []storage.Warning {
	result := make([]storage.Warning, 0, len(warnings))
	for _, warning := range warnings {
		code := warning.Code
		if !diagnosticCode.MatchString(code) {
			code = "source_warning"
		}
		context := warning.Context
		if len(context) > 512 || strings.Contains(context, "://") || strings.ContainsAny(context, "?&#\x00\r\n") {
			context = ""
		}
		result = append(result, storage.Warning{Code: code, Context: context})
	}
	return result
}
func publicLabel(value string) string { return publicLink.ReplaceAllString(value, "[link omitted]") }
func storageAttempt(result bnovo.SearchResult, jobID string, number int, started, finished time.Time, duration int64, queue *int64, next *time.Time) storage.AttemptInput {
	input := storage.AttemptInput{JobID: jobID, Number: number, StartedAt: started, FinishedAt: finished, DurationMS: duration, QueueWaitMS: queue, Outcome: storage.Outcome(result.Status), HTTPStatus: result.HTTPStatus, Warnings: storedWarnings(result.Warnings), NextRetryAt: next, Observations: make([]storage.Observation, 0, len(result.Categories))}
	if !result.CollectedAt.IsZero() {
		at := result.CollectedAt
		input.CollectedAt = &at
	}
	if result.Error != nil {
		code := result.Error.Code
		if !diagnosticCode.MatchString(code) {
			code = "source_error"
		}
		input.Error = &storage.AttemptError{Code: code, Retryable: result.Error.Retryable}
	}
	categories := make(map[string]bnovo.RoomCategory)
	for _, category := range result.Categories {
		categories[category.ID] = category
	}
	availability := make(map[string]bnovo.CategoryAvailability)
	for _, state := range result.CategoryAvailability {
		availability[state.CategoryID] = state
	}
	for _, stock := range NormalizeCategoryStock(result) {
		category := categories[stock.CategoryID]
		state := availability[stock.CategoryID]
		observation := storage.Observation{CategoryID: stock.CategoryID, CategoryName: publicLabel(category.Name), AreaM2: category.AreaM2, Available: stock.Available, StockStatus: storage.StockStatus(stock.Status), HasOffers: state.HasOffers, ConfirmedUnavailable: state.ConfirmedUnavailable, Warnings: storedWarnings(stock.Warnings)}
		for _, variant := range result.Variants {
			if variant.CategoryID == stock.CategoryID {
				observation.Variants = append(observation.Variants, storage.Variant{ID: variant.ID, Adults: variant.Adults})
			}
		}
		for _, offer := range result.Offers {
			if offer.CategoryID != stock.CategoryID {
				continue
			}
			stored := storage.Offer{VariantID: offer.VariantID, RateID: offer.RateID, RateName: publicLabel(offer.RateName), MealPlan: publicLabel(offer.MealPlan), Visible: offer.Visible, DataAvailable: offer.Availability.DataAvailable, RowAvailable: offer.Availability.RowAvailable, SelectDisabled: offer.Availability.SelectDisabled, SelectableQuantities: append([]int{}, offer.Availability.SelectableQuantities...)}
			if offer.Price != "" {
				price := offer.Price
				stored.Price = &price
			}
			if offer.Currency != "" {
				currency := offer.Currency
				stored.Currency = &currency
			}
			if value := offer.Availability.DataAvailableForGuests; value != nil && (*value == "0" || *value == "1") {
				copy := *value
				stored.DataAvailableForGuests = &copy
			}
			observation.Offers = append(observation.Offers, stored)
		}
		input.Observations = append(input.Observations, observation)
	}
	return input
}
