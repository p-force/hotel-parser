package collect

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
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
	StartInterval time.Duration // Интервал всех попыток этого batch; дополнительно действует source limiter.
	Retry         RetryPolicy
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
	groupID     string
	maxAttempts int // 0: policy; durable runtime ограничивает оставшимся бюджетом.
	request     bnovo.SearchRequest
	client      *bnovo.Client
	propertyID  string
	source      string
	origin      string
}

type queuedJob struct {
	index    int
	queuedAt time.Time
}

type batchRun struct {
	storageError        error
	batchID             string
	results             []bnovo.SearchResult
	summary             BatchSummary
	executed            []bool
	queueWaitMS         []*int64
	concurrencyLimit    int
	peakActiveScenarios int
	attempts            []int
	throttled           []int
	rateWait            []int
	limiterWaitMS       []int64
}

// collectJobs — единственный bounded worker pool для всех пакетных API.
func collectJobs(ctx context.Context, input []batchJob, options BatchOptions) (batchRun, error) {
	return collectJobsObserved(ctx, input, options, nil)
}

func collectJobsObserved(ctx context.Context, input []batchJob, options BatchOptions, observer *attemptObserver) (batchRun, error) {
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
	policy, err := options.Retry.normalized()
	if err != nil {
		return batchRun{}, err
	}
	input = append([]batchJob(nil), input...)
	for i := range input {
		if input[i].client == nil {
			return batchRun{}, errors.New("Bnovo client is required")
		}
		input[i].origin, err = SourceKey(input[i].client.SourceOrigin())
		if err != nil {
			return batchRun{}, err
		}
	}
	run := batchRun{batchID: batchID, results: results, executed: executed, queueWaitMS: make([]*int64, len(input)), attempts: make([]int, len(input)), throttled: make([]int, len(input)), rateWait: make([]int, len(input)), limiterWaitMS: make([]int64, len(input)), concurrencyLimit: cap(slots)}
	scheduleJobs(ctx, input, options, policy, slots, &run, observer)
	canceledCount := 0
	for i, result := range results {
		if executed[i] && result.Error != nil && (result.Error.Code == "canceled" || (result.Error.Code == "timeout" && ctx.Err() == context.DeadlineExceeded)) {
			canceledCount++
		}
	}

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
	summary.CanceledCount = canceledCount
	for i := range input {
		summary.RetryCount += max(0, run.attempts[i]-1)
		summary.ThrottledCount += run.throttled[i]
		summary.RateWaitCount += run.rateWait[i]
		summary.LimiterWaitMS += run.limiterWaitMS[i]
	}
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
		"concurrency_limit", cap(slots), "peak_active_scenarios", run.peakActiveScenarios, "retry_count", summary.RetryCount, "throttled_count", summary.ThrottledCount, "rate_wait_count", summary.RateWaitCount, "limiter_wait_ms", summary.LimiterWaitMS)
	run.summary = summary
	return run, run.storageError

}
