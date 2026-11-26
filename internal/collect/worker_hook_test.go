package collect

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
)

type workerHookClient struct {
	collect func(context.Context, bnovo.SearchRequest) bnovo.SearchResult
}

func (c workerHookClient) SourceOrigin() string { return "https://worker-hook.invalid" }
func (c workerHookClient) SourceType() string   { return "bnovo" }
func (c workerHookClient) Collect(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
	return c.collect(ctx, req)
}

func workerHookFixture() ([]runtimeLease, batchJob) {
	request := batchRequests(1)[0]
	states := make([]runtimeLease, 1)
	states[0].leasedJob = leasedJob{job: storage.Job{
		ID: "job", RunID: "run", Attempts: 2, LeaseOwner: "owner", LeaseToken: 7,
		Scenario: storage.Scenario{Source: "bnovo", SourcePropertyID: request.PropertyID, CheckIn: request.CheckIn, CheckOut: request.CheckOut, Adults: request.Adults, LOS: 2},
	}}
	client := workerHookClient{collect: func(_ context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
		return bnovo.SearchResult{Request: req, Status: bnovo.SearchStatusPartial, HTTPStatus: 200,
			Categories:           []bnovo.RoomCategory{{ID: "1", Name: "original"}},
			CategoryAvailability: []bnovo.CategoryAvailability{{CategoryID: "1"}}}
	}}
	return states, batchJob{client: client, request: request, propertyID: "hotel", source: "bnovo"}
}

func TestWorkerHookOrderAndPreparedAttempt(t *testing.T) {
	states, job := workerHookFixture()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var events []string
	var released bool
	originalClient := job.client
	job.client = workerHookClient{collect: func(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
		events = append(events, "collect")
		return originalClient.Collect(ctx, req)
	}}
	var hookInput *storage.AttemptInput
	hook := func(hookCtx context.Context, _ *storage.Store, _ source.Client, received storage.Job, attempt *storage.AttemptInput) error {
		events = append(events, "hook")
		if released || hookCtx != ctx || received.ID != "job" || received.RunID != "run" ||
			attempt.Number != 3 || attempt.FinishedAt.Before(attempt.StartedAt) || attempt.QueueWaitMS == nil || *attempt.QueueWaitMS != 42 ||
			attempt.Lease == nil || attempt.Lease.Token != 7 || attempt.NextRetryAt != nil {
			t.Fatal("hook lost execution context, permit or attempt metadata", attempt)
		}
		hookInput = attempt
		attempt.Observations[0].CategoryName = "prepared"
		attempt.Warnings = append(attempt.Warnings, storage.Warning{Code: "hook_diagnostic"})
		return nil
	}
	observer := &attemptObserver{worker: runtimeWorkerHook(states, hook, cancel)}
	wait := int64(42)
	done := executeObservedAttempt(ctx, job, admission{release: func() {
		released = true
		events = append(events, "release")
	}}, 0, 1, &wait, observer)
	if done.workerError != nil || done.prepared != hookInput || !reflect.DeepEqual(events, []string{"collect", "hook", "release"}) {
		t.Fatal("incorrect worker pipeline", events, done.workerError)
	}
	// Тот же путь вызывается обработчиком after перед SaveAttempt. Отдельный
	// исходный SearchResult не содержит изменений hook, поэтому пересборка их потеряет.
	next := time.Now().Add(time.Second)
	input, err := states[0].attemptInput(context.Background(), done, 1, &next)
	if err != nil || input.Observations[0].CategoryName != "prepared" ||
		input.Warnings[len(input.Warnings)-1].Code != "hook_diagnostic" || input.NextRetryAt != &next ||
		input.Lease.Token != 7 || input.Number != 3 || input.QueueWaitMS != &wait ||
		!input.FinishedAt.Equal(done.finishedAt) || input.DurationMS != done.durationMS {
		t.Fatal("prepared attempt lost changes or persistence metadata", input, err)
	}
	if done.result.Categories[0].Name != "original" || done.result.Status != bnovo.SearchStatusPartial {
		t.Fatal("hook changed original source result")
	}
}

func TestWorkerHookAbsent(t *testing.T) {
	states, job := workerHookFixture()
	state := &states[0]
	if runtimeWorkerHook(states, nil, nil) != nil {
		t.Fatal("nil option installed a hook")
	}
	released := false
	done := executeObservedAttempt(context.Background(), job, admission{release: func() { released = true }}, 0, 1, nil, &attemptObserver{})
	if !released || done.prepared != nil || done.workerError != nil || done.finishedAt.IsZero() {
		t.Fatal("legacy worker path changed")
	}
	input, err := states[0].attemptInput(context.Background(), done, 1, nil)
	want, wantErr := storageAttemptContext(context.Background(), done.result, state.job.ID, 3, done.startedAt, done.finishedAt, done.durationMS, nil, nil)
	want.SelectionPolicy = selectionPolicy(state.property.Hotel)
	want.Lease = ptrLease(state.job.Lease())
	if err != nil || wantErr != nil || !reflect.DeepEqual(input, want) {
		t.Fatal("nil hook changed persistence input", err, wantErr)
	}
}

func TestWorkerHookCancellation(t *testing.T) {
	for _, duringHook := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_hook=%v", duringHook), func(t *testing.T) {
			states, job := workerHookFixture()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			originalClient := job.client
			job.client = workerHookClient{collect: func(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
				result := originalClient.Collect(ctx, req)
				if !duringHook {
					cancel(context.Canceled)
				}
				return result
			}}
			called, released := false, false
			hook := func(hookCtx context.Context, _ *storage.Store, _ source.Client, _ storage.Job, attempt *storage.AttemptInput) error {
				called = true
				attempt.Observations[0].CategoryName = "discarded"
				cancel(context.Canceled)
				if hookCtx.Err() == nil {
					t.Fatal("hook did not receive execution context")
				}
				return hookCtx.Err()
			}
			done := executeObservedAttempt(ctx, job, admission{release: func() { released = true }}, 0, 1, nil,
				&attemptObserver{worker: runtimeWorkerHook(states, hook, cancel)})
			if called != duringHook || !released || done.workerError != nil || done.prepared != nil {
				t.Fatal("cancellation did not release permit/discard hook result")
			}
			// Штатный flush может сохранить основной завершённый результат.
			input, err := states[0].attemptInput(context.WithoutCancel(ctx), done, 1, nil)
			if err != nil || input.Observations[0].CategoryName != "original" || input.Outcome != storage.OutcomePartial {
				t.Fatal("cancellation lost completed source attempt", input, err)
			}
		})
	}
}

func TestWorkerHookErrorStopsWithoutSavingOrRetry(t *testing.T) {
	states, job := workerHookFixture()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	failure := errors.New("hook preparation failed")
	var saved atomic.Int32
	observer := &attemptObserver{
		worker: runtimeWorkerHook(states, func(context.Context, *storage.Store, source.Client, storage.Job, *storage.AttemptInput) error {
			return failure
		}, cancel),
		after: func(context.Context, completedAttempt, int, *time.Time) error {
			saved.Add(1)
			return nil
		},
	}
	run, err := collectJobsObserved(ctx, []batchJob{job}, BatchOptions{Workers: 1}, observer)
	if !errors.Is(err, failure) || !errors.Is(context.Cause(ctx), failure) || saved.Load() != 0 || run.attempts[0] != 1 || run.results[0].Status != bnovo.SearchStatusPartial {
		t.Fatal("hook error saved/retried/overwrote source attempt", err, run.attempts, saved.Load())
	}
}

func TestWorkerHookPreservesRetryScheduling(t *testing.T) {
	states, job := workerHookFixture()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	originalClient := job.client
	var calls atomic.Int32
	job.client = workerHookClient{collect: func(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
		if calls.Add(1) == 1 {
			return bnovo.SearchResult{Request: req, Status: bnovo.SearchStatusFailed, HTTPStatus: 429,
				Error: &bnovo.CollectionError{Code: "http_error", Retryable: true}}
		}
		return originalClient.Collect(ctx, req)
	}}
	var prepared, saved int
	observer := &attemptObserver{
		worker: runtimeWorkerHook(states, func(_ context.Context, _ *storage.Store, _ source.Client, _ storage.Job, attempt *storage.AttemptInput) error {
			prepared++
			attempt.Warnings = append(attempt.Warnings, storage.Warning{Code: "hook_diagnostic"})
			return nil
		}, cancel),
		after: func(ctx context.Context, done completedAttempt, number int, next *time.Time) error {
			saved++
			input, err := states[0].attemptInput(ctx, done, number, next)
			if err != nil {
				return err
			}
			if input.Number != 2+number || input.NextRetryAt != next || (next != nil) != (number == 1) || input.Lease.Token != 7 ||
				input.Warnings[len(input.Warnings)-1].Code != "hook_diagnostic" || (input.Error != nil) != (number == 1) {
				t.Error("prepared input lost retry/fencing metadata", input)
			}
			return nil
		},
	}
	run, err := collectJobsObserved(ctx, []batchJob{job}, BatchOptions{Workers: 1, Retry: RetryPolicy{
		MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	}}, observer)
	if err != nil || prepared != 2 || saved != 2 || calls.Load() != 2 || run.summary.RetryCount != 1 || run.results[0].Status != bnovo.SearchStatusPartial {
		t.Fatal("hook changed retry scheduling", err, prepared, saved, run.summary)
	}
}

func TestWorkerHookDoesNotBlockOtherCompletions(t *testing.T) {
	globalLimit(t, 2)
	testLimits(t, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release, otherSaved := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	_, job := workerHookFixture()
	observer := &attemptObserver{
		worker: func(ctx context.Context, done completedAttempt, _ int) (*storage.AttemptInput, error) {
			if done.index == 0 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, nil
		},
		after: func(_ context.Context, done completedAttempt, _ int, _ *time.Time) error {
			if done.index == 1 {
				close(otherSaved)
			}
			return nil
		},
	}
	finished := make(chan jobOutcome, 1)
	go func() {
		run, err := collectJobsObserved(ctx, []batchJob{job, job}, BatchOptions{Workers: 2}, observer)
		finished <- jobOutcome{run, err}
	}()
	for _, signal := range []chan struct{}{entered, otherSaved} {
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatal("hook blocked sequential completion handling", ctx.Err())
		}
	}
	unblock()
	if outcome := <-finished; outcome.err != nil {
		t.Fatal(outcome.err)
	}
}

func TestWorkerHookKeepsConcurrencyLimits(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		global, property, source int
		want                     int
	}{
		{"global", 1, 4, 4, 1},
		{"property", 4, 1, 4, 1},
		{"source", 4, 4, 1, 1},
		{"parallel", 4, 2, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			globalLimit(t, tc.global)
			testLimits(t, Limits{PropertyConcurrency: tc.property, SourceConcurrency: tc.source})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered, release := make(chan struct{}, 8), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var active, peak atomic.Int32
			_, job := workerHookFixture()
			originalClient := job.client
			job.client = workerHookClient{collect: func(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
				n := active.Add(1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				return originalClient.Collect(ctx, req)
			}}
			observer := &attemptObserver{worker: func(ctx context.Context, _ completedAttempt, _ int) (*storage.AttemptInput, error) {
				defer active.Add(-1)
				entered <- struct{}{}
				select {
				case <-release:
					return nil, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}}
			jobs := []batchJob{job, job, job, job}
			finished := make(chan jobOutcome, 1)
			go func() {
				run, err := collectJobsObserved(ctx, jobs, BatchOptions{Workers: 4}, observer)
				finished <- jobOutcome{run, err}
			}()
			for range tc.want {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if active.Load() != int32(tc.want) {
				t.Fatal("hook released admission before completion", active.Load())
			}
			unblock()
			outcome := <-finished
			if outcome.err != nil || active.Load() != 0 || peak.Load() != int32(tc.want) || outcome.run.peakActiveScenarios != tc.want {
				t.Fatal("hook violated concurrency limits", outcome.err, active.Load(), peak.Load(), outcome.run.peakActiveScenarios)
			}
		})
	}
}
