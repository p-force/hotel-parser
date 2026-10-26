package collect

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// PropertyCollection задаёт дату и конфигурацию отеля. Client можно переиспользовать
// между вызовами; nil создаёт один клиент на BaseURL внутри пакета, не на сценарий.
// Переданный Client должен соответствовать Source конфигурации.
type PropertyCollection struct {
	Hotel   config.Config
	CheckIn time.Time
	Client  *bnovo.Client
}

// PropertyResult сохраняет идентичность задания; даты, adults и остальные параметры
// остаются в Result.Request, включая незапущенные задания. QueueWaitMS=nil — не запущено.
type PropertyResult struct {
	PropertyID  string
	Source      config.SourceConfig
	LOS         int
	QueueWaitMS *int64
	Result      bnovo.SearchResult
}

type PropertySummary struct {
	PropertyID      string
	Source          string
	SuccessCount    int
	PartialCount    int
	NoOffersCount   int
	FailedCount     int
	CanceledCount   int
	NotStartedCount int
}

type PropertiesSummary struct {
	BatchSummary
	ConcurrencyLimit    int
	PeakActiveScenarios int // Пик сценариев этого пакета; активность всего процесса есть в DEBUG-событиях.
	Properties          []PropertySummary
}

// CollectProperties разворачивает отели в один упорядоченный список заданий:
// порядок отелей, затем adults, затем LOS из конфигурации. Один bounded worker pool
// и общий процессный допуск ограничивают все отели и одновременно запущенные пакеты.
// Enabled не проверяется: это явный запуск, как CollectConfiguredBatch.
func CollectProperties(ctx context.Context, properties []PropertyCollection, options BatchOptions) ([]PropertyResult, PropertiesSummary, error) {
	if options.Workers == 0 {
		options.Workers = DefaultGlobalConcurrency
	}
	jobs := make([]batchJob, 0)
	results := make([]PropertyResult, 0)
	clients := make(map[string]*bnovo.Client)
	summaries := make([]PropertySummary, 0, len(properties))
	indices := make(map[string]int)
	sources := make(map[string]config.SourceConfig)
	owners := make([]int, 0)
	for _, property := range properties {
		hotel := property.Hotel
		if hotel.ID == "" {
			return nil, PropertiesSummary{}, fmt.Errorf("hotel ID is required")
		}
		if err := hotel.Validate(); err != nil {
			return nil, PropertiesSummary{}, fmt.Errorf("property %s: %w", hotel.ID, err)
		}
		requests, err := configuredRequests(hotel, property.CheckIn)
		if err != nil {
			return nil, PropertiesSummary{}, fmt.Errorf("property %s: %w", hotel.ID, err)
		}
		client := property.Client
		if client == nil {
			client = clients[hotel.Source.BaseURL]
			if client == nil {
				client, err = bnovo.NewClient(hotel.Source.BaseURL, nil)
				if err != nil {
					return nil, PropertiesSummary{}, err
				}
				clients[hotel.Source.BaseURL] = client
			}
		}
		// Одинаковый ID может иметь несколько дат; неоднозначная конфигурация источника запрещена.
		owner, exists := indices[hotel.ID]
		if exists {
			if sources[hotel.ID] != hotel.Source {
				return nil, PropertiesSummary{}, fmt.Errorf("property %s has conflicting sources", hotel.ID)
			}
		} else {
			sources[hotel.ID] = hotel.Source
			owner = len(summaries)
			indices[hotel.ID] = owner
			summaries = append(summaries, PropertySummary{PropertyID: hotel.ID, Source: hotel.Source.Type})
		}
		for i, request := range requests {
			jobs = append(jobs, batchJob{request: request, client: client, propertyID: hotel.ID, source: hotel.Source.Type})
			results = append(results, PropertyResult{PropertyID: hotel.ID, Source: hotel.Source, LOS: hotel.Collection.Nights[i%len(hotel.Collection.Nights)]})
			owners = append(owners, owner)
		}
	}
	run, err := collectJobs(ctx, jobs, options)
	if err != nil {
		return nil, PropertiesSummary{}, err
	}
	for i, result := range run.results {
		results[i].Result = result
		results[i].QueueWaitMS = run.queueWaitMS[i]
		summary := &summaries[owners[i]]
		switch result.Status {
		case bnovo.SearchStatusSuccess:
			summary.SuccessCount++
		case bnovo.SearchStatusPartial:
			summary.PartialCount++
		case bnovo.SearchStatusNoOffers:
			summary.NoOffersCount++
		case bnovo.SearchStatusFailed:
			summary.FailedCount++
		}
		if !run.executed[i] {
			summary.NotStartedCount++
		} else if result.Error != nil && (result.Error.Code == "canceled" || (result.Error.Code == "timeout" && ctx.Err() == context.DeadlineExceeded)) {
			summary.CanceledCount++
		}
	}
	for _, summary := range summaries {
		level := slog.LevelInfo
		if summary.PartialCount > 0 {
			level = slog.LevelWarn
		}
		if summary.FailedCount > 0 {
			level = slog.LevelError
		}
		slog.Log(ctx, level, "bnovo.batch.property.completed", "batch_id", run.batchID, "property_id", summary.PropertyID, "source", summary.Source,
			"success_count", summary.SuccessCount, "partial_count", summary.PartialCount, "no_offers_count", summary.NoOffersCount,
			"failed_count", summary.FailedCount, "canceled_count", summary.CanceledCount, "not_started_count", summary.NotStartedCount)
	}
	return results, PropertiesSummary{BatchSummary: run.summary, ConcurrencyLimit: run.concurrencyLimit, PeakActiveScenarios: run.peakActiveScenarios, Properties: summaries}, nil
}
