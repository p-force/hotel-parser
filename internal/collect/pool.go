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
	Workers       int           // 0: DefaultWorkers (3), для CollectProperties — 8; допустимы 1..MaxWorkers.
	StartInterval time.Duration // Минимальный интервал между допусками к Collect; 0 отключает ограничение.
}

// CollectBatch выполняет независимые сценарии общим клиентом и сохраняет порядок результатов.
// Ошибка возвращается только при неверных настройках. Отмена представляется в SearchResult:
// незапущенные сценарии имеют failed, canceled/timeout, HTTPStatus=0 и нулевой CollectedAt.
func CollectBatch(ctx context.Context, client *bnovo.Client, requests []bnovo.SearchRequest, options BatchOptions) ([]bnovo.SearchResult, BatchSummary, error) {
	if client == nil {
		return nil, BatchSummary{}, errors.New("Bnovo client is required")
	}
	jobs := make([]batchJob, len(requests))
	for i, req := range requests {
		jobs[i] = batchJob{request: req, client: client, propertyID: req.PropertyID, source: "bnovo"}
	}
	run, err := collectJobs(ctx, jobs, options)
	return run.results, run.summary, err
}

type batchJob struct {
	request    bnovo.SearchRequest
	client     *bnovo.Client
	propertyID string
	source     string
}

type batchRun struct {
	batchID             string
	results             []bnovo.SearchResult
	summary             BatchSummary
	executed            []bool
	queueWaitMS         []*int64
	concurrencyLimit    int
	peakActiveScenarios int
}

// collectJobs — единственный bounded worker pool для всех пакетных API.
func collectJobs(ctx context.Context, input []batchJob, options BatchOptions) (batchRun, error) {
	workers := options.Workers
	if workers == 0 {
		workers = DefaultWorkers
	}
	if workers < 1 || workers > MaxWorkers {
		return batchRun{}, errors.New("workers must be between 1 and 32")
	}
	if options.StartInterval < 0 {
		return batchRun{}, errors.New("start interval must not be negative")
	}
	slots, unregister := registerBatch()
	defer unregister()
	started := time.Now()
	batchID := rand.Text()
	results := make([]bnovo.SearchResult, len(input))
	executed := make([]bool, len(input))
	var canceledCount atomic.Int64
	var active, peak atomic.Int64
	queueWaitMS := make([]*int64, len(input))
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
		globalActive, ok := acquireScenario(ctx, slots)
		if !ok {
			return false
		}
		current := active.Add(1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		lastStart = time.Now()
		executed[index] = true
		wait := time.Since(started).Milliseconds()
		queueWaitMS[index] = &wait
		slog.DebugContext(ctx, "bnovo.batch.scenario.started", "batch_id", batchID,
			"scenario_index", index, "queue_wait_ms", wait,
			"property_id", input[index].propertyID, "source", input[index].source,
			"active_scenarios", globalActive, "concurrency_limit", cap(slots))
		return true
	}

	for range min(workers, len(input)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if !begin(index) {
					continue
				}
				result := input[index].client.Collect(ctx, input[index].request)
				active.Add(-1)
				releaseScenario(slots)
				results[index] = result
				if result.Error != nil && (result.Error.Code == "canceled" ||
					(result.Error.Code == "timeout" && ctx.Err() == context.DeadlineExceeded)) {
					canceledCount.Add(1)
				}
			}
		}()
	}
dispatch:
	for index := range input {
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
			Request: input[index].request, Status: bnovo.SearchStatusFailed,
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
		"not_started_count", summary.NotStartedCount,
		"concurrency_limit", cap(slots), "peak_active_scenarios", peak.Load())
	return batchRun{batchID: batchID, results: results, summary: summary, executed: executed, queueWaitMS: queueWaitMS, concurrencyLimit: cap(slots), peakActiveScenarios: int(peak.Load())}, nil
}
