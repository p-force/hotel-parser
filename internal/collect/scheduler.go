package collect

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

type scheduledAttempt struct {
	queuedJob
	permit admission
}
type completedAttempt struct {
	index  int
	result bnovo.SearchResult
}
type pendingJob struct {
	queuedAt, readyAt, waitingAt time.Time
	rateThrottled                bool
}

// executeAttempt освобождает допуски также при панике. Паника не перехватывается.
func executeAttempt(ctx context.Context, job batchJob, permit admission) bnovo.SearchResult {
	defer permit.release()
	return job.client.Collect(ctx, job.request)
}

// scheduleJobs расширяет диспетчер прежнего bounded pool: workers исполняют одну
// попытку, диспетчер хранит ожидания и retry. Нет goroutine на задание или таймер.
func scheduleJobs(ctx context.Context, input []batchJob, options BatchOptions, policy RetryPolicy, slots chan struct{}, run *batchRun) {
	ctx, cancel := context.WithCancel(ctx)
	defer withdrawAdmissions(run.batchID)
	workers := options.Workers
	if workers == 0 {
		workers = DefaultWorkers
	}
	workers = min(workers, len(input))
	jobs := make(chan scheduledAttempt)
	completed := make(chan completedAttempt, workers)
	var wg sync.WaitGroup
	var active, peak atomic.Int64
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				index := job.index
				current := active.Add(1)
				for old := peak.Load(); current > old; old = peak.Load() {
					if peak.CompareAndSwap(old, current) {
						break
					}
				}
				release := job.permit.release
				job.permit.release = func() { active.Add(-1); release() }
				result := executeAttempt(ctx, input[index], job.permit)
				completed <- completedAttempt{index, result}
			}
		}()
	}
	defer func() { cancel(); close(jobs); wg.Wait(); run.peakActiveScenarios = int(peak.Load()) }()
	// FIFO внутри отеля, round-robin между отелями; retry помещается в хвост.
	var groups [][]int
	owners := make(map[string]int)
	for i, job := range input {
		owner, ok := owners[job.propertyID]
		if !ok {
			owner = len(groups)
			owners[job.propertyID] = owner
			groups = append(groups, nil)
		}
		groups[owner] = append(groups[owner], i)
	}
	pending := make([]pendingJob, len(input))
	remaining, running, cursor := len(input), 0, 0
	var lastStart time.Time
	handle := func(done completedAttempt) {
		running--
		i := done.index
		run.results[i] = done.result
		if done.result.HTTPStatus == 429 {
			run.throttled[i]++
		}
		if ctx.Err() == nil && run.attempts[i] < policy.MaxAttempts && retryable(done.result) {
			delay := retryDelay(policy, run.attempts[i], done.result.Error.RetryAfter)
			now := time.Now()
			pending[i] = pendingJob{queuedAt: now, readyAt: now.Add(delay)}
			owner := owners[input[i].propertyID]
			groups[owner] = append(groups[owner], i)
			slog.DebugContext(ctx, "bnovo.batch.retry.scheduled", "batch_id", run.batchID, "scenario_index", i, "property_id", input[i].propertyID, "source", input[i].source, "source_origin", input[i].origin, "retry_reason", done.result.Error.Code, "http_status", done.result.HTTPStatus, "backoff_ms", delay.Milliseconds(), "next_attempt", run.attempts[i]+1)
		} else {
			remaining--
		}
	}
	for remaining > 0 {
		if ctx.Err() != nil {
			break
		}
		now := time.Now()
		var next time.Time
		earlier := func(at time.Time) {
			if at.After(now) && (next.IsZero() || at.Before(next)) {
				next = at
			}
		}
		globalConcurrency.Lock()
		changed := globalConcurrency.changed
		globalConcurrency.Unlock()
		dispatched := false
		for offset := 0; offset < len(groups); offset++ {
			owner := (cursor + offset) % len(groups)
			chosen := -1
			for position, index := range groups[owner] {
				if pending[index].readyAt.After(now) {
					earlier(pending[index].readyAt)
					continue
				}
				chosen = position
				break
			}
			if chosen < 0 {
				continue
			}
			i := groups[owner][chosen]
			state := &pending[i]
			if state.queuedAt.IsZero() {
				state.queuedAt = time.Now()
			}
			// Отмечаем ожидание worker до освобождения места; глобальные permits пока не берём.
			if running >= workers {
				break
			}
			if state.waitingAt.IsZero() {
				state.waitingAt = time.Now()
			}
			if at := lastStart.Add(options.StartInterval); !lastStart.IsZero() && options.StartInterval > 0 && now.Before(at) {
				earlier(at)
				continue
			}
			permit, rateAt, _, ok := tryAdmission(ctx, slots, input[i].origin+"/"+strings.ToLower(input[i].request.PropertyID), input[i].origin, fmt.Sprintf("%s/%d/%d", run.batchID, i, run.attempts[i]+1))
			// Используем снимок сигнала ДО проверок: release во время обхода не теряется.
			if !ok {
				if rateAt.After(now) {
					earlier(rateAt)
					if !state.rateThrottled {
						run.rateWait[i]++
						state.rateThrottled = true
					}
				}
				continue
			}
			func() {
				handedOff := false
				defer func() {
					if !handedOff {
						permit.release()
					}
				}()
				if ctx.Err() != nil {
					return
				}
				attemptWait := time.Since(state.waitingAt).Milliseconds()
				run.limiterWaitMS[i] += attemptWait
				if !run.executed[i] {
					wait := time.Since(state.queuedAt).Milliseconds()
					run.queueWaitMS[i] = &wait
					run.executed[i] = true
					slog.DebugContext(ctx, "bnovo.batch.scenario.started", "batch_id", run.batchID, "scenario_index", i, "queue_wait_ms", wait, "property_id", input[i].propertyID, "source", input[i].source, "source_origin", input[i].origin, "active_scenarios", permit.global, "active_property", permit.property, "active_source", permit.source, "concurrency_limit", cap(slots))
				}
				run.attempts[i]++
				slog.DebugContext(ctx, "bnovo.batch.attempt.started", "batch_id", run.batchID, "scenario_index", i, "property_id", input[i].propertyID, "source", input[i].source, "source_origin", input[i].origin, "attempt", run.attempts[i], "limiter_wait_ms", attemptWait, "rate_limited", state.rateThrottled, "active_scenarios", permit.global, "active_property", permit.property, "active_source", permit.source)
				groups[owner] = append(groups[owner][:chosen], groups[owner][chosen+1:]...)
				cursor = (owner + 1) % len(groups)
				withdrawAdmissions(run.batchID)
				lastStart = time.Now()
				running++

				// running<workers до резервирования гарантирует свободного/завершающего worker.
				jobs <- scheduledAttempt{queuedJob{i, state.queuedAt}, permit}
				handedOff = true
				dispatched = true
			}()
			break
		}
		if dispatched {
			continue
		}
		var timer *time.Timer
		var tick <-chan time.Time
		if !next.IsZero() {
			timer = time.NewTimer(time.Until(next))
			tick = timer.C
		}
		select {
		case done := <-completed:
			handle(done)
		case <-ctx.Done():
		case <-changed:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
	// Активные HTTP отменяются исходным context; завершения обязательно дожидаемся.
	for running > 0 {
		handle(<-completed)
	}
	// Уже начатый сценарий в backoff при отмене имеет canceled/timeout, а не старую 429.
	if ctx.Err() != nil {
		code := "canceled"
		if ctx.Err() == context.DeadlineExceeded {
			code = "timeout"
		}
		for i := range input {
			if run.executed[i] && run.attempts[i] < policy.MaxAttempts && retryable(run.results[i]) {
				run.results[i].Status = bnovo.SearchStatusFailed
				run.results[i].Error = &bnovo.CollectionError{Code: code, HTTPStatus: run.results[i].HTTPStatus, Message: "batch retry canceled", Retryable: code == "timeout"}
			}
		}
	}
}
