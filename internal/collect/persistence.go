package collect

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
)

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
func storageAttemptContext(ctx context.Context, result bnovo.SearchResult, jobID string, number int, started, finished time.Time, duration int64, queue *int64, next *time.Time) (storage.AttemptInput, error) {
	if err := ctx.Err(); err != nil {
		return storage.AttemptInput{}, err
	}
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
		if err := ctx.Err(); err != nil {
			return storage.AttemptInput{}, err
		}
		category := categories[stock.CategoryID]
		state := availability[stock.CategoryID]
		observation := storage.Observation{CategoryID: stock.CategoryID, CategoryName: publicLabel(category.Name), AreaM2: category.AreaM2, Available: stock.Available, StockStatus: storage.StockStatus(stock.Status), HasOffers: state.HasOffers, ConfirmedUnavailable: state.ConfirmedUnavailable, Warnings: storedWarnings(stock.Warnings)}
		for _, variant := range result.Variants {
			if err := ctx.Err(); err != nil {
				return storage.AttemptInput{}, err
			}
			if variant.CategoryID == stock.CategoryID {
				observation.Variants = append(observation.Variants, storage.Variant{ID: variant.ID, Adults: variant.Adults})
			}
		}
		for _, offer := range result.Offers {
			if err := ctx.Err(); err != nil {
				return storage.AttemptInput{}, err
			}
			if offer.CategoryID != stock.CategoryID {
				continue
			}
			stored := storage.Offer{ReportedQuantity: offer.Availability.ReportedQuantity, VariantID: offer.VariantID, RateID: offer.RateID, RateName: publicLabel(offer.RateName), MealPlan: publicLabel(offer.MealPlan), Visible: offer.Visible, DataAvailable: offer.Availability.DataAvailable, RowAvailable: offer.Availability.RowAvailable, SelectDisabled: offer.Availability.SelectDisabled, SelectableQuantities: append([]int{}, offer.Availability.SelectableQuantities...)}
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
	return input, ctx.Err()
}
