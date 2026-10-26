package collect

import (
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// BatchSummary — формат диагностики завершённого пакета для будущего сборщика.
// Не запускает запросы и не предполагает наличия очереди или worker pool.
type BatchSummary struct {
	BatchDurationMS int64 `json:"batch_duration_ms"`
	SuccessCount    int   `json:"success_count"`
	PartialCount    int   `json:"partial_count"`
	NoOffersCount   int   `json:"no_offers_count"`
	FailedCount     int   `json:"failed_count"`
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
