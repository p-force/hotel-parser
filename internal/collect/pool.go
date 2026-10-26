package collect

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

const (
	DefaultWorkers = 3
	MaxWorkers     = 32
)

// BatchOptions ограничивает число одновременно выполняемых сценариев и частоту их старта.
type BatchOptions struct {
	Workers       int           // 0 означает DefaultWorkers; допустимы значения 1..MaxWorkers.
	StartInterval time.Duration // Минимальный интервал между допусками к Collect; 0 отключает ограничение.
}

// CollectBatch выполняет независимые сценарии общим клиентом и сохраняет порядок результатов.
// Ошибка возвращается только при неверных настройках. Отмена представляется в SearchResult:
// незапущенные сценарии имеют failed, canceled/timeout, HTTPStatus=0 и нулевой CollectedAt.
func CollectBatch(ctx context.Context, client *bnovo.Client, requests []bnovo.SearchRequest, options BatchOptions) ([]bnovo.SearchResult, BatchSummary, error) {
	workers := options.Workers
	if workers == 0 {
		workers = DefaultWorkers
	}
	if workers < 1 || workers > MaxWorkers {
		return nil, BatchSummary{}, errors.New("workers must be between 1 and 32")
	}
	if options.StartInterval < 0 {
		return nil, BatchSummary{}, errors.New("start interval must not be negative")
	}
	if client == nil {
		return nil, BatchSummary{}, errors.New("Bnovo client is required")
	}

	started := time.Now()
	batchID := rand.Text()
	results := make([]bnovo.SearchResult, len(requests))
	executed := make([]bool, len(requests))
	var canceledCount atomic.Int64
	jobs := make(chan int)
	var wg sync.WaitGroup
	var startMu sync.Mutex
	var lastStart time.Time

	// Общий допуск не накапливает разрешения после простоя и отменяется вместе с context.
	begin := func(index int) bool {
		startMu.Lock()
		defer startMu.Unlock()
		if ctx.Err() != nil {
			return false
		}
		if !lastStart.IsZero() && options.StartInterval > 0 {
			if wait := options.StartInterval - time.Since(lastStart); wait > 0 {
				timer := time.NewTimer(wait)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return false
				case <-timer.C:
				}
			}
		}
		if ctx.Err() != nil {
			return false
		}
		lastStart = time.Now()
		executed[index] = true
		slog.DebugContext(ctx, "bnovo.batch.scenario.started", "batch_id", batchID,
			"scenario_index", index, "queue_wait_ms", time.Since(started).Milliseconds())
		return true
	}

	for range min(workers, len(requests)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if !begin(index) {
					continue
				}
				result := client.Collect(ctx, requests[index])
				results[index] = result
				if result.Error != nil && (result.Error.Code == "canceled" ||
					(result.Error.Code == "timeout" && ctx.Err() == context.DeadlineExceeded)) {
					canceledCount.Add(1)
				}
			}
		}()
	}
dispatch:
	for index := range requests {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()

	notStarted := 0
	for index, ran := range executed {
		if ran {
			continue
		}
		notStarted++
		code := "canceled"
		if ctx.Err() == context.DeadlineExceeded {
			code = "timeout"
		}
		results[index] = bnovo.SearchResult{
			Request: requests[index], Status: bnovo.SearchStatusFailed,
			Error: &bnovo.CollectionError{Code: code, Message: "batch scenario was not started: context ended", Retryable: code == "timeout"},
		}
	}
	summary := SummarizeBatch(started, results)
	summary.CanceledCount = int(canceledCount.Load())
	summary.NotStartedCount = notStarted
	level := slog.LevelInfo
	if summary.PartialCount > 0 {
		level = slog.LevelWarn
	}
	if summary.FailedCount > 0 {
		level = slog.LevelError
	}
	slog.Log(ctx, level, "bnovo.batch.completed", "batch_id", batchID,
		"batch_duration_ms", summary.BatchDurationMS, "success_count", summary.SuccessCount,
		"partial_count", summary.PartialCount, "no_offers_count", summary.NoOffersCount,
		"failed_count", summary.FailedCount, "canceled_count", summary.CanceledCount,
		"not_started_count", summary.NotStartedCount)
	return results, summary, nil
}
