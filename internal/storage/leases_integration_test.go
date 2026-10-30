//go:build integration

package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func secondPool(t *testing.T, schema string) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func jobsInRun(t *testing.T, store *storage.Store, count int) (storage.Run, []storage.Job) {
	t.Helper()
	run, first := runJob(t, store, "arena33")
	jobs := []storage.Job{first}
	for i := 1; i < count; i++ {
		sc := scenario("arena33")
		sc.Adults = i + 2
		job, err := store.CreateJob(context.Background(), run.ID, sc)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	return run, jobs
}
func expire(t *testing.T, db *sql.DB, schema string, job storage.Job) {
	t.Helper()
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal("failed to expire isolated test lease")
	}
}
func claimOne(t *testing.T, store *storage.Store, runID, owner string) storage.Job {
	t.Helper()
	jobs, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: owner, RunID: runID})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim: count=%d err=%v", len(jobs), err)
	}
	return jobs[0]
}

func TestPostgresConcurrentClaimsIndependentPools(t *testing.T) {
	first, _, schema := storagetest.Open(t)
	second := secondPool(t, schema)
	run, _ := jobsInRun(t, first, 10)
	start := make(chan struct{})
	var wg sync.WaitGroup
	type result struct {
		jobs []storage.Job
		err  error
	}
	results := make(chan result, 2)
	for i, store := range []*storage.Store{first, second} {
		wg.Add(1)
		go func(i int, s *storage.Store) {
			defer wg.Done()
			<-start
			owner := []string{"worker-a", "worker-b"}[i]
			jobs, err := s.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: owner, BatchSize: 5, RunID: run.ID})
			results <- result{jobs, err}
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	owners := map[string]int{}
	for result := range results {
		if result.err != nil || len(result.jobs) != 5 {
			t.Fatalf("batch claim: %d %v", len(result.jobs), result.err)
		}
		for _, job := range result.jobs {
			if seen[job.ID] || job.LeaseToken != 1 || job.LeasedUntil == nil || job.Status != storage.JobRunning {
				t.Fatalf("duplicate/invalid ownership: %+v", job)
			}
			seen[job.ID] = true
			owners[job.LeaseOwner]++
		}
	}
	if len(seen) != 10 || owners["worker-a"] != 5 || owners["worker-b"] != 5 {
		t.Fatalf("unfair batch distribution: %v", owners)
	}
	if jobs, err := first.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "third", BatchSize: 10, RunID: run.ID}); err != nil || len(jobs) != 0 {
		t.Fatalf("active leases reissued: %d %v", len(jobs), err)
	}
}

func TestPostgresCrashRecoveryAndFencing(t *testing.T) {
	first, db, schema := storagetest.Open(t)
	second := secondPool(t, schema)
	run, _ := jobsInRun(t, first, 1)
	a := claimOne(t, first, run.ID, "worker-a")
	expire(t, db, schema, a)
	if _, err := first.RenewLease(context.Background(), a.Lease(), time.Minute); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("expired renewal: %v", err)
	}
	if _, err := first.CompleteJob(context.Background(), a.Lease(), attempt(a.ID)); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("expired completion: %v", err)
	}
	b := claimOne(t, second, run.ID, "worker-b")
	if b.ID != a.ID || b.LeaseToken != a.LeaseToken+1 || b.Attempts != 0 {
		t.Fatalf("crash recovery: a=%+v b=%+v", a, b)
	}
	if _, err := first.CompleteJob(context.Background(), a.Lease(), attempt(a.ID)); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("stale worker overwrote new owner: %v", err)
	}
	staleFailure := attempt(a.ID)
	staleFailure.Outcome = storage.OutcomeFailed
	staleFailure.Observations = nil
	staleFailure.Error = &storage.AttemptError{Code: "timeout", Retryable: true}
	if _, err := first.FailJob(context.Background(), a.Lease(), staleFailure, storage.FailPolicy{}); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("stale failure changed new owner: %v", err)
	}
	input := attempt(b.ID)
	id, err := second.CompleteJob(context.Background(), b.Lease(), input)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := second.CompleteJob(context.Background(), b.Lease(), input)
	if err != nil || duplicate != id {
		t.Fatalf("completion replay duplicated facts: %s %v", duplicate, err)
	}
	loaded, err := second.GetAttempt(context.Background(), b.ID, 1)
	if err != nil || loaded.Lease == nil || loaded.Lease.Token != b.LeaseToken || loaded.Lease.Owner != "worker-b" {
		t.Fatalf("lease metadata not preserved: %+v %v", loaded.Lease, err)
	}
	if _, err := first.CompleteJob(context.Background(), a.Lease(), input); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("stale replay accepted after completion: %v", err)
	}
	if _, err := first.SaveAttempt(context.Background(), input); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("unfenced SaveAttempt bypass: %v", err)
	}
	if err := first.MarkRunning(context.Background(), b.ID, 2, time.Now()); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("legacy MarkRunning bypass: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".observations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate observations=%d", count)
	}
}

func TestPostgresLeaseRenewalAndCancellation(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	run, jobs := jobsInRun(t, store, 2)
	a := claimOne(t, store, run.ID, "worker-a")
	until, err := store.RenewLease(context.Background(), a.Lease(), 2*time.Minute)
	if err != nil || !until.After(*a.LeasedUntil) {
		t.Fatalf("renewal failed: %s %v", until, err)
	}
	wrong := a.Lease()
	wrong.Owner = "worker-b"
	if _, err := store.RenewLease(context.Background(), wrong, time.Minute); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("wrong owner renewed: %v", err)
	}
	// Удерживаем только одну строку; SKIP LOCKED должен выдать другое задание.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT id FROM "`+schema+`".scenario_jobs WHERE id=$1::uuid FOR UPDATE`, a.ID); err != nil {
		t.Fatal("row lock failed")
	}
	second := secondPool(t, schema)
	other := claimOne(t, second, run.ID, "worker-b")
	if other.ID == a.ID || len(jobs) != 2 {
		t.Fatal("claim blocked on/returned locked row")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err = second.RenewLease(ctx, a.Lease(), time.Minute); err == nil || ctx.Err() == nil {
		t.Fatalf("context failed to interrupt locked renewal: %v", err)
	}
	tx.Rollback()
	expire(t, db, schema, a)
	if _, err := store.RenewLease(context.Background(), a.Lease(), time.Minute); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("expired lease revived: %v", err)
	}
}

func TestPostgresLeaseBudgetAndRetryScheduling(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	run, _ := jobsInRun(t, store, 2)
	claimed, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "a", BatchSize: 1, RunID: run.ID, MaxClaims: 2, MaxAttempts: 2})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v", err)
	}
	a := claimed[0]
	expire(t, db, schema, a)
	// Бюджет заморожен: другой worker не может увеличить MaxClaims до 100.
	b := claimOne(t, store, run.ID, "b")
	// Если очередь отдала второе queued-задание, запрашиваем до возврата expired a.
	if b.ID != a.ID {
		b = claimOne(t, store, run.ID, "b")
	}
	if b.ID != a.ID || b.MaxClaims != 2 {
		t.Fatalf("claim budget changed: %+v", b)
	}
	expire(t, db, schema, b)
	if _, err = store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "c", BatchSize: 2, RunID: run.ID, MaxClaims: 100}); err != nil {
		t.Fatal(err)
	}
	exhausted, err := store.GetJob(context.Background(), a.ID)
	if err != nil || exhausted.Status != storage.JobFailed || exhausted.LastErrorCode != "lease_exhausted" {
		t.Fatalf("endless crash reclaim: %+v %v", exhausted, err)
	}
	// Отдельный run для ограниченного durable retry; SQL не исполняет HTTP.
	retryRun, _ := jobsInRun(t, store, 1)
	j := claimOne(t, store, retryRun.ID, "retry-a")
	failed := attempt(j.ID)
	failed.Outcome = storage.OutcomeFailed
	failed.Error = &storage.AttemptError{Code: "http_error", Retryable: true}
	failed.HTTPStatus = 503
	failed.Observations = nil
	first, err := store.FailJob(context.Background(), j.Lease(), failed, storage.FailPolicy{InitialBackoff: time.Hour, MaxBackoff: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := store.FailJob(context.Background(), j.Lease(), failed, storage.FailPolicy{InitialBackoff: time.Hour, MaxBackoff: time.Hour}); err != nil || duplicate != first {
		t.Fatalf("FailJob replay is not idempotent: %v", err)
	}
	state, err := store.GetJob(context.Background(), j.ID)
	if err != nil || state.Status != storage.JobRetryable || state.NextRetryAt == nil || state.LeasedUntil != nil {
		t.Fatalf("durable retry state: %+v %v", state, err)
	}
	if ready, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "early", RunID: retryRun.ID}); err != nil || len(ready) != 0 {
		t.Fatalf("retry started before next_retry_at: %v", err)
	}
	if _, err := store.RenewLease(context.Background(), j.Lease(), time.Minute); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("released lease renewed: %v", err)
	}
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET next_retry_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, j.ID); err != nil {
		t.Fatal("advance isolated retry time failed")
	}
	next := claimOne(t, store, retryRun.ID, "retry-b")
	success := attempt(next.ID)
	success.Number = 2
	if _, err := store.CompleteJob(context.Background(), next.Lease(), success); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetAttempt(context.Background(), next.ID, 1)
	if err != nil || old.HTTPStatus != 503 || old.NextRetryAt == nil || old.Lease.Token != j.LeaseToken {
		t.Fatalf("retry history was overwritten: %v", err)
	}
}

func TestPostgresHTTPRetriesShareLeaseAndBudget(t *testing.T) {
	store, _, schema := storagetest.Open(t)
	second := secondPool(t, schema)
	run, _ := jobsInRun(t, store, 2)
	claimed, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "worker", RunID: run.ID, MaxAttempts: 2})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v", err)
	}
	j := claimed[0]
	failed := attempt(j.ID)
	failed.Lease = ptr(j.Lease())
	failed.Outcome = storage.OutcomeFailed
	failed.HTTPStatus = 429
	failed.Error = &storage.AttemptError{Code: "http_error", Retryable: true}
	failed.Observations = nil
	failed.NextRetryAt = ptr(failed.FinishedAt.Add(time.Second))
	if _, err := store.SaveAttempt(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetJob(context.Background(), j.ID)
	if err != nil || state.LeasedUntil == nil || state.LeaseToken != j.LeaseToken || state.Attempts != 1 {
		t.Fatalf("HTTP retry lost lease/budget: %+v %v", state, err)
	}
	// Ошибка этого задания не препятствует выдаче второго.
	other := claimOne(t, second, run.ID, "other")
	if other.ID == j.ID {
		t.Fatal("active HTTP retry was reclaimed")
	}
	if err := second.CancelPending(context.Background(), run.ID, "canceled"); err != nil {
		t.Fatal(err)
	}
	state, _ = store.GetJob(context.Background(), j.ID)
	if state.Status != storage.JobRetryable {
		t.Fatal("legacy cancellation changed leased job")
	}
	failed.Number = 2
	failed.NextRetryAt = nil
	if _, err := store.FailJob(context.Background(), j.Lease(), failed, storage.FailPolicy{}); err != nil {
		t.Fatal(err)
	}
	state, err = store.GetJob(context.Background(), j.ID)
	if err != nil || state.Status != storage.JobFailed || state.Attempts != 2 || state.NextRetryAt != nil {
		t.Fatalf("HTTP retry budget not exhausted: %+v %v", state, err)
	}
	// История обеих HTTP-попыток остаётся доступной.
	for n := 1; n <= 2; n++ {
		got, err := store.GetAttempt(context.Background(), j.ID, n)
		if err != nil || got.HTTPStatus != 429 || got.Lease.Token != j.LeaseToken {
			t.Fatalf("attempt %d: %+v %v", n, got, err)
		}
	}
}

func TestPostgresConcurrentCompletionAndCanceledClaim(t *testing.T) {
	first, db, schema := storagetest.Open(t)
	second := secondPool(t, schema)
	run, _ := jobsInRun(t, first, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := first.ClaimJobs(ctx, storage.ClaimOptions{Owner: "canceled", RunID: run.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("claim ignored cancellation: %v", err)
	}
	j := claimOne(t, first, run.ID, "worker")
	input := attempt(j.ID)
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, store := range []*storage.Store{first, second} {
		go func(s *storage.Store) {
			<-start
			id, err := s.CompleteJob(context.Background(), j.Lease(), input)
			results <- result{id, err}
		}(store)
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.id != b.id {
		t.Fatalf("concurrent completion: %+v %+v", a, b)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".collection_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent ledger writes: %d %v", count, err)
	}
}
