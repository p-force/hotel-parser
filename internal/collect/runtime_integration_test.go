//go:build integration

package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func runtimeFixture(t *testing.T, store *storage.Store, count int, handler http.HandlerFunc) ([]PropertyCollection, storage.Run, []storage.Job) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := bnovo.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	properties := propertyInputs(t, client)[:1]
	hotel := &properties[0].Hotel
	hotel.Source.BaseURL = server.URL
	hotel.Inventory.Categories = hotel.Inventory.Categories[:1]
	hotel.Inventory.Categories[0].SourceRoomID = "1"
	hotel.Inventory.TotalRooms = hotel.Inventory.Categories[0].TotalRooms
	if err := hotel.Validate(); err != nil {
		t.Fatal(err)
	}
	request := batchRequests(1)[0]
	run, err := store.CreateRun(context.Background(), storage.RunInput{PropertyID: hotel.ID, PeriodStart: request.CheckIn, PeriodEnd: request.CheckOut.AddDate(0, 0, count)})
	if err != nil {
		t.Fatal(err)
	}
	var jobs []storage.Job
	for i := range count {
		job, err := store.CreateJob(context.Background(), run.ID, storage.Scenario{PropertyID: hotel.ID, Source: "bnovo", SourceOrigin: server.URL, SourcePropertyID: hotel.Source.PropertyID,
			CheckIn: request.CheckIn.AddDate(0, 0, i), CheckOut: request.CheckOut.AddDate(0, 0, i), LOS: 2, Adults: 2, Rooms: 1, Language: "ru"})
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	return properties, run, jobs
}

type runtimeOutcome struct {
	result RuntimeResult
	err    error
}

func launchRuntime(ctx context.Context, store *storage.Store, properties []PropertyCollection, options RuntimeOptions) <-chan runtimeOutcome {
	done := make(chan runtimeOutcome, 1)
	go func() { result, err := RunQueued(ctx, store, properties, options); done <- runtimeOutcome{result, err} }()
	return done
}

func runtimeOptions(runID string, workers int) RuntimeOptions {
	return RuntimeOptions{Batch: BatchOptions{Workers: workers}, Claim: storage.ClaimOptions{Owner: "worker-a", RunID: runID}, ShutdownGracePeriod: time.Second}
}

func expireRuntimeLease(t *testing.T, db *sql.DB, schema, id string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, id); err != nil {
		t.Fatal("expire isolated lease failed")
	}
}

func runtimeSecondPool(t *testing.T, schema string) *storage.Store {
	t.Helper()
	s, err := storage.Open(context.Background(), os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresRuntimeCancellationAndRecovery(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	entered, exited := make(chan struct{}, 1), make(chan struct{}, 1)
	var calls atomic.Int32
	var recoverNow atomic.Bool
	properties, run, jobs := runtimeFixture(t, store, 3, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if recoverNow.Load() {
			fmt.Fprint(w, propertySuccessHTML)
			return
		}
		entered <- struct{}{}
		<-r.Context().Done()
		exited <- struct{}{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	options := runtimeOptions(run.ID, 1)
	defer cancel()
	done := launchRuntime(ctx, store, properties, options)
	receive(t, entered)
	cancel()
	first := receive(t, done)
	receive(t, exited)
	if !errors.Is(first.err, context.Canceled) || !first.result.ProgressKnown || first.result.Progress.Remaining != 3 || first.result.Progress.Completed != 0 || calls.Load() != 1 {
		t.Fatalf("cancellation lost jobs/started waiting worker: %+v calls=%d", first, calls.Load())
	}
	state, err := store.GetJob(context.Background(), jobs[0].ID)
	if err != nil || state.Status != storage.JobRunning || state.Attempts != 0 {
		t.Fatalf("interrupted job finalized: %+v %v", state, err)
	}
	expireRuntimeLease(t, db, schema, state.ID)
	recoverNow.Store(true)
	second := runtimeSecondPool(t, schema)
	options.Claim.Owner = "worker-b"
	result, err := RunQueued(context.Background(), second, properties, options)
	if err != nil || !result.ProgressKnown || result.Progress.Completed != 3 || result.Progress.Remaining != 0 {
		t.Fatalf("recovery: %+v %v", result, err)
	}
	recovered, _ := store.GetJob(context.Background(), state.ID)
	if recovered.LeaseToken != state.LeaseToken+1 {
		t.Fatal("recovery did not fence old worker")
	}
	old := storageAttempt(bnovo.SearchResult{Status: bnovo.SearchStatusSuccess}, state.ID, 1, time.Now(), time.Now(), 0, nil, nil)
	if _, err := store.CompleteJob(context.Background(), state.Lease(), old); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("old token saved result: %v", err)
	}
}

func TestPostgresRuntimeCanceledBeforeClaimAndDuringClaim(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	var calls atomic.Int32
	properties, run, jobs := runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, propertySuccessHTML) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunQueued(ctx, store, properties, runtimeOptions(run.ID, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled: %v", err)
	}
	job, _ := store.GetJob(context.Background(), jobs[0].ID)
	if job.LeaseToken != 0 || calls.Load() != 0 {
		t.Fatal("pre-canceled runtime claimed/sent HTTP")
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`LOCK TABLE "` + schema + `".scenario_jobs IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal("table lock failed")
	}
	options := runtimeOptions(run.ID, 1)
	options.ExecutionTimeout = 30 * time.Millisecond
	options.ShutdownGracePeriod = 30 * time.Millisecond
	result := receive(t, launchRuntime(context.Background(), store, properties, options))
	if result.err == nil || calls.Load() != 0 || result.result.ProgressKnown {
		t.Fatalf("blocked claim ignored deadline: %+v", result)
	}
	tx.Rollback()
	job, _ = store.GetJob(context.Background(), jobs[0].ID)
	if job.LeaseToken != 0 {
		t.Fatal("canceled claim committed")
	}
}

func TestPostgresRuntimeCancelRateAndBackoff(t *testing.T) {
	for _, backoff := range []bool{false, true} {
		t.Run(fmt.Sprint("backoff=", backoff), func(t *testing.T) {
			store, _, _ := storagetest.Open(t)
			var calls atomic.Int32
			properties, run, jobs := runtimeFixture(t, store, 2, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if backoff {
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(429)
				} else {
					fmt.Fprint(w, propertySuccessHTML)
				}
			})
			testLimits(t, Limits{SourceStartInterval: time.Hour})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var scheduled <-chan struct{}
			if backoff {
				scheduled = signalLog(t, "bnovo.batch.retry.scheduled")
			}
			done := launchRuntime(ctx, store, properties, runtimeOptions(run.ID, 2))
			if backoff {
				receive(t, scheduled)
			} else {
				// Ждём реального wait ticket источника, а не произвольного sleep.
				waitForRuntime(t, func() bool {
					globalConcurrency.Lock()
					defer globalConcurrency.Unlock()
					return calls.Load() == 1 && len(globalConcurrency.waiters) > 0
				})
			}
			cancel()
			result := receive(t, done)
			if !errors.Is(result.err, context.Canceled) || calls.Load() != 1 || !result.result.ProgressKnown || result.result.Progress.Remaining < 1 || result.result.Progress.Canceled != 0 {
				t.Fatalf("canceled wait: %+v calls=%d", result, calls.Load())
			}
			if backoff {
				savedRetries := 0
				for _, planned := range jobs {
					job, err := store.GetJob(context.Background(), planned.ID)
					if err != nil {
						t.Fatal(err)
					}
					if job.Attempts == 0 {
						continue
					}
					savedRetries++
					attempt, err := store.GetAttempt(context.Background(), job.ID, 1)
					if err != nil || job.Attempts != 1 || job.Status != storage.JobRetryable || job.NextRetryAt == nil || attempt.HTTPStatus != 429 || attempt.Error == nil || !attempt.Error.Retryable || attempt.NextRetryAt == nil {
						t.Fatalf("retry history lost on cancellation: %+v %+v %v", job, attempt, err)
					}
					if _, err := store.GetAttempt(context.Background(), job.ID, 2); !errors.Is(err, storage.ErrNotFound) {
						t.Fatalf("cancellation invented another attempt: %v", err)
					}
				}
				if savedRetries != 1 {
					t.Fatalf("saved retries=%d, want 1", savedRetries)
				}
			}
			globalConcurrency.Lock()
			active, batches, waiters := globalConcurrency.active, globalConcurrency.batches, len(globalConcurrency.waiters)
			globalConcurrency.Unlock()
			if active != 0 || batches != 0 || waiters != 0 {
				t.Fatalf("leaked permits/waiters: %d %d %d", active, batches, waiters)
			}
		})
	}
}

func waitForRuntime(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !predicate() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("runtime condition not reached")
		}
	}
}

type runtimeCancelHandler struct {
	slog.Handler
	cancel context.CancelFunc
}

func (h runtimeCancelHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "bnovo.collect.completed" {
		h.cancel()
	}
	return h.Handler.Handle(ctx, record)
}
func (h runtimeCancelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}

func TestPostgresRuntimeCompletedFlushAndFlushTimeout(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint("blocked=", blocked), func(t *testing.T) {
			store, db, schema := storagetest.Open(t)
			entered, release := make(chan struct{}, 1), make(chan struct{})
			properties, run, jobs := runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				fmt.Fprint(w, propertySuccessHTML)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			previous := slog.Default()
			slog.SetDefault(slog.New(runtimeCancelHandler{slog.NewTextHandler(io.Discard, nil), cancel}))
			defer slog.SetDefault(previous)
			options := runtimeOptions(run.ID, 1)
			if blocked {
				options.ShutdownGracePeriod = 60 * time.Millisecond
			}
			done := launchRuntime(ctx, store, properties, options)
			receive(t, entered)
			var tx *sql.Tx
			if blocked {
				var err error
				tx, err = db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`SELECT id FROM "`+schema+`".scenario_jobs WHERE id=$1::uuid FOR UPDATE`, jobs[0].ID); err != nil {
					t.Fatal("row lock failed")
				}
			}
			close(release)
			result := receive(t, done)
			if result.err == nil {
				t.Fatal("shutdown falsely reported success")
			}
			if tx != nil {
				tx.Rollback()
			}
			job, err := store.GetJob(context.Background(), jobs[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if blocked {
				if job.Status != storage.JobRunning || job.Attempts != 0 || result.result.Progress.Completed != 0 {
					t.Fatalf("flush timeout reported committed success: %+v %+v", job, result)
				}
				if _, err := store.GetAttempt(context.Background(), job.ID, 1); !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("partial transaction survived: %v", err)
				}
			} else {
				if job.Status != storage.JobSucceeded || job.Attempts != 1 || !result.result.ProgressKnown || result.result.Progress.Completed != 1 {
					t.Fatalf("completed HTTP not flushed: %+v %+v", job, result)
				}
				attempt, err := store.GetAttempt(context.Background(), job.ID, 1)
				if err != nil || len(attempt.Observations) != 1 {
					t.Fatalf("flushed observations missing: %+v %v", attempt, err)
				}
			}
		})
	}
}

func TestPostgresRuntimeRenewsUntilExecutionDeadline(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	entered, exited := make(chan struct{}, 1), make(chan struct{}, 1)
	properties, run, jobs := runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
		exited <- struct{}{}
	})
	options := runtimeOptions(run.ID, 1)
	options.ExecutionTimeout = 500 * time.Millisecond
	options.Claim.LeaseDuration = time.Second
	options.RenewInterval = 20 * time.Millisecond
	done := launchRuntime(context.Background(), store, properties, options)
	receive(t, entered)
	initial, err := store.GetJob(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRuntime(t, func() bool {
		current, err := store.GetJob(context.Background(), jobs[0].ID)
		return err == nil && current.LeasedUntil.After(*initial.LeasedUntil)
	})
	result := receive(t, done)
	receive(t, exited)
	if !errors.Is(result.err, context.DeadlineExceeded) || result.result.Progress.Remaining != 1 {
		t.Fatalf("execution deadline: %+v", result)
	}
}

func TestPostgresRuntimeRejectsFencedHTTPResult(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	second := runtimeSecondPool(t, schema)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	properties, run, jobs := runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, propertySuccessHTML)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := launchRuntime(ctx, store, properties, runtimeOptions(run.ID, 1))
	receive(t, entered)
	expireRuntimeLease(t, db, schema, jobs[0].ID)
	claimed, err := second.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "worker-b", RunID: run.ID})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("takeover: %+v %v", claimed, err)
	}
	close(release)
	result := receive(t, done)
	if !errors.Is(result.err, storage.ErrLeaseLost) {
		t.Fatalf("stale HTTP result accepted: %+v", result)
	}
	job, err := second.GetJob(context.Background(), jobs[0].ID)
	if err != nil || job.LeaseOwner != "worker-b" || job.Attempts != 0 || job.Status != storage.JobRunning {
		t.Fatalf("new owner's state damaged: %+v %v", job, err)
	}
}

func TestPostgresRuntimeCanceledWaitingForGlobalWorker(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	var calls atomic.Int32
	properties, run, _ := runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, propertySuccessHTML) })
	globalLimit(t, 1)
	slots, unregister := registerBatch()
	defer unregister()
	if _, ok := acquireScenario(context.Background(), slots); !ok {
		t.Fatal("reserve worker failed")
	}
	defer releaseScenario(slots)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := launchRuntime(ctx, store, properties, runtimeOptions(run.ID, 1))
	waitForRuntime(t, func() bool {
		globalConcurrency.Lock()
		defer globalConcurrency.Unlock()
		return len(globalConcurrency.waiters) > 0
	})
	cancel()
	result := receive(t, done)
	if !errors.Is(result.err, context.Canceled) || calls.Load() != 0 || result.result.Progress.Remaining != 1 {
		t.Fatalf("waiting worker cancellation: %+v", result)
	}
}

func TestPostgresRuntimeIndependentOutcomesAndDurableAttemptBudget(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	var calls atomic.Int32
	properties, run, jobs := runtimeFixture(t, store, 2, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("dfrom") == "26-10-2026" {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, propertySuccessHTML)
	})
	options := runtimeOptions(run.ID, 2)
	result, err := RunQueued(context.Background(), store, properties, options)
	if err != nil || result.Progress.Completed != 1 || result.Progress.Failed != 1 || calls.Load() != 2 {
		t.Fatalf("independent outcomes: %+v %v calls=%d", result, err, calls.Load())
	}
	// Новый run: одна ранее сохранённая HTTP-попытка, затем crash/reclaim.
	properties, run, jobs = runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	claimed, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "previous", RunID: run.ID, MaxAttempts: 2})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("prepare claim: %v", err)
	}
	now := time.Now()
	next := now.Add(time.Hour)
	input := storageAttempt(bnovo.SearchResult{Status: bnovo.SearchStatusFailed, HTTPStatus: 503, Error: &bnovo.CollectionError{Code: "http_error", Retryable: true}}, jobs[0].ID, 1, now, now, 0, nil, &next)
	input.Lease = ptrLease(claimed[0].Lease())
	if _, err := store.SaveAttempt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	expireRuntimeLease(t, db, schema, jobs[0].ID)
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET next_retry_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, jobs[0].ID); err != nil {
		t.Fatal("advance isolated retry failed")
	}
	result, err = RunQueued(context.Background(), store, properties, runtimeOptions(run.ID, 1))
	job, readErr := store.GetJob(context.Background(), jobs[0].ID)
	if err != nil || readErr != nil || result.Progress.Failed != 1 || job.Attempts != 2 || job.Status != storage.JobFailed {
		t.Fatalf("reclaim exceeded HTTP budget: %+v %+v %v %v", result, job, err, readErr)
	}
}

func TestPostgresRuntimeDeadlineAtHTTPStartGateAndRecovery(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	var calls atomic.Int32
	properties, run, jobs := runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, propertySuccessHTML) })
	origin := properties[0].Hotel.Source.BaseURL
	// httptest может переиспользовать origin предыдущего сервера; limiter хранит
	// историю между ConfigureLimits. Детерминированно воспроизводим эту историю.
	testLimits(t, Limits{Sources: map[string]SourceLimit{origin: {StartInterval: 0}}})
	if result := properties[0].Client.Collect(context.Background(), batchRequests(1)[0]); result.Status != bnovo.SearchStatusSuccess {
		t.Fatalf("seed existing origin history: %+v", result)
	}
	// A direct request consumes the transport interval, but no scheduler admission.
	// The queued job can therefore reach RenewLease/Collect and wait at HTTP gate.
	if result := properties[0].Client.Collect(context.Background(), batchRequests(1)[0]); result.Status != bnovo.SearchStatusSuccess {
		t.Fatalf("warm-up: %+v", result)
	}
	// Сначала warm-up без задержки, затем длинный интервал: существующая
	// история origin блокирует именно queued HTTP, а не подготовку теста.
	testLimits(t, Limits{Sources: map[string]SourceLimit{origin: {StartInterval: time.Hour}}})
	started := signalLog(t, "bnovo.collect.started")
	options := runtimeOptions(run.ID, 1)
	options.ExecutionTimeout = 200 * time.Millisecond
	options.RenewInterval = 20 * time.Millisecond
	outcome := receive(t, launchRuntime(context.Background(), store, properties, options))
	receive(t, started)
	if !errors.Is(outcome.err, context.DeadlineExceeded) || calls.Load() != 2 || outcome.result.Progress.Remaining != 1 || outcome.result.Progress.Canceled != 0 {
		t.Fatalf("transport wait deadline lost job or sent HTTP: %+v calls=%d", outcome, calls.Load())
	}
	job, err := store.GetJob(context.Background(), jobs[0].ID)
	if err != nil || job.Status != storage.JobRunning || job.Attempts != 0 {
		t.Fatalf("unfinished job finalized: %+v %v", job, err)
	}
	globalConcurrency.Lock()
	active, batches, waiters := globalConcurrency.active, globalConcurrency.batches, len(globalConcurrency.waiters)
	globalConcurrency.Unlock()
	if active != 0 || batches != 0 || waiters != 0 {
		t.Fatalf("leaked admission: %d/%d/%d", active, batches, waiters)
	}
	expireRuntimeLease(t, db, schema, job.ID)
	if err := ConfigureLimits(Limits{Sources: map[string]SourceLimit{origin: {StartInterval: 0}}}); err != nil {
		t.Fatal(err)
	}
	options = runtimeOptions(run.ID, 1)
	options.Claim.Owner = "worker-b"
	recovered, err := RunQueued(context.Background(), runtimeSecondPool(t, schema), properties, options)
	if err != nil || recovered.Progress.Completed != 1 || calls.Load() != 3 {
		t.Fatalf("recover: %+v %v calls=%d", recovered, err, calls.Load())
	}
	current, err := store.GetJob(context.Background(), job.ID)
	if err != nil || current.LeaseToken != job.LeaseToken+1 || current.Attempts != 1 {
		t.Fatalf("recovery fencing/attempt: %+v %v", current, err)
	}
	old := storageAttempt(bnovo.SearchResult{Status: bnovo.SearchStatusSuccess}, job.ID, 1, time.Now(), time.Now(), 0, nil, nil)
	if _, err := store.CompleteJob(context.Background(), job.Lease(), old); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("stale token accepted: %v", err)
	}
}
