package collect

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func TestSummarizeBatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []bnovo.SearchResult
		counts  BatchSummary
	}{
		{name: "empty"},
		{name: "mixed", results: []bnovo.SearchResult{
			{Status: bnovo.SearchStatusSuccess}, {Status: bnovo.SearchStatusPartial},
			{Status: bnovo.SearchStatusNoOffers}, {Status: bnovo.SearchStatusFailed}, {Status: bnovo.SearchStatusSuccess},
		}, counts: BatchSummary{SuccessCount: 2, PartialCount: 1, NoOffersCount: 1, FailedCount: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Add сохраняет монотонные часы. Не требуется запускать настоящую очередь.
			started := time.Now().Add(-25 * time.Millisecond)
			before := append([]bnovo.SearchResult(nil), tc.results...)
			summary := SummarizeBatch(started, tc.results)
			if summary.BatchDurationMS < 25 || summary.BatchDurationMS > time.Since(started).Milliseconds() {
				t.Errorf("неверное время пакета: %d", summary.BatchDurationMS)
			}
			counts := summary
			counts.BatchDurationMS = 0
			if counts != tc.counts {
				t.Errorf("неверные счётчики: %+v, ожидались %+v", counts, tc.counts)
			}
			if !reflect.DeepEqual(tc.results, before) {
				t.Error("сводка изменила результаты или порядок")
			}
			data, err := json.Marshal(summary)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 5 {
				t.Errorf("неожиданные поля сводки: %s", data)
			}
			for _, key := range []string{"batch_duration_ms", "success_count", "partial_count", "no_offers_count", "failed_count"} {
				value, ok := fields[key].(float64)
				if !ok || value < 0 {
					t.Errorf("нет неотрицательной метрики %s: %s", key, data)
				}
			}
		})
	}
}
