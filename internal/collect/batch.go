package collect

import (
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// BatchSummary содержит время пакета и счётчики исходов.
// CanceledCount учитывает только начатые и отменённые сценарии;
// NotStartedCount — отдельный счётчик незапущенных сценариев.
type BatchSummary struct {
	RateWaitCount   int   `json:"rate_wait_count,omitempty"`
	RetryCount      int   `json:"retry_count,omitempty"`
	ThrottledCount  int   `json:"throttled_count,omitempty"`
	LimiterWaitMS   int64 `json:"limiter_wait_ms,omitempty"`
	BatchDurationMS int64 `json:"batch_duration_ms"`
	SuccessCount    int   `json:"success_count"`
	PartialCount    int   `json:"partial_count"`
	NoOffersCount   int   `json:"no_offers_count"`
	FailedCount     int   `json:"failed_count"`
	CanceledCount   int   `json:"canceled_count,omitempty"`
	NotStartedCount int   `json:"not_started_count,omitempty"`
}

// SummarizeBatch считает исходы готовых результатов и время пакета.
// started нужно получить через time.Now() перед началом реального пакета,
// чтобы time.Since использовал монотонные часы. Время не суммируется по запросам.
func SummarizeBatch(started time.Time, results []bnovo.SearchResult) BatchSummary {
	summary := BatchSummary{BatchDurationMS: time.Since(started).Milliseconds()}
	for _, result := range results {
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
	}
	return summary
}
