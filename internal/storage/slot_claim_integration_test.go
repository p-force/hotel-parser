//go:build integration

package storage_test

import (
	"context"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
	"testing"
	"time"
)

func TestPostgresSlotClaimPriorityRecoveryAndFIFOCompatibility(t *testing.T) {
	for _, mode := range []string{"fifo", "latest", "slot"} {
		t.Run(mode, func(t *testing.T) {
			store, db, schema := storagetest.Open(t)
			ctx := context.Background()
			sc := scenario("arena33")
			old, e := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut, StartedAt: sc.CheckIn.Add(-48 * time.Hour)})
			if e != nil {
				t.Fatal(e)
			}
			oldJob, e := store.CreateJob(ctx, old.ID, sc)
			if e != nil {
				t.Fatal(e)
			}
			newer, e := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut, StartedAt: sc.CheckIn.Add(-24 * time.Hour)})
			if e != nil {
				t.Fatal(e)
			}
			newJob, e := store.CreateJob(ctx, newer.ID, sc)
			if e != nil {
				t.Fatal(e)
			}
			options := storage.ClaimOptions{Owner: "first", PropertyID: sc.PropertyID}
			want := oldJob
			if mode == "latest" {
				options.NewestRunFirst = true
				want = newJob
			}
			if mode == "slot" {
				options.RunStartedAt = newer.StartedAt
				want = newJob
			}
			jobs, e := store.ClaimJobs(ctx, options)
			if e != nil || len(jobs) != 1 || jobs[0].ID != want.ID {
				t.Fatal(jobs, e)
			}
			if mode == "slot" {
				expire(t, db, schema, jobs[0])
				options.Owner = "recovered"
				recovered, e := store.ClaimJobs(ctx, options)
				if e != nil || len(recovered) != 1 || recovered[0].ID != newJob.ID || recovered[0].LeaseToken != jobs[0].LeaseToken+1 {
					t.Fatal(recovered, e)
				}
				if _, e := store.CompleteJob(ctx, jobs[0].Lease(), attempt(newJob.ID)); e != storage.ErrLeaseLost {
					t.Fatal("stale token accepted", e)
				}
				if _, e := store.CompleteJob(ctx, recovered[0].Lease(), attempt(newJob.ID)); e != nil {
					t.Fatal(e)
				}
				none, e := store.ClaimJobs(ctx, options)
				if e != nil || len(none) != 0 {
					t.Fatal("old job leaked into target slot", none, e)
				}
			}
		})
	}
}

func TestPostgresRunProgressReadyLeaseBackoff(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	sc := scenario("arena33")
	run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut.AddDate(0, 0, 2), StartedAt: sc.CheckIn.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var jobs []storage.Job
	for i := range 3 {
		input := sc
		input.CheckIn = input.CheckIn.AddDate(0, 0, i)
		input.CheckOut = input.CheckOut.AddDate(0, 0, i)
		job, err := store.CreateJob(ctx, run.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET status='running',lease_owner='fixture',lease_token=1,leased_until=clock_timestamp()+interval '1 hour' WHERE id=$1`, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET status='retryable',next_retry_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, jobs[1].ID); err != nil {
		t.Fatal(err)
	}
	progress, err := store.RunProgress(ctx)
	if err != nil || len(progress) != 1 {
		t.Fatal(progress, err)
	}
	p := progress[0]
	if p.Remaining != 3 || p.Ready != 1 || p.WaitingLease != 1 || p.WaitingBackoff != 1 || p.ObservedAt.IsZero() || p.LastActivityAt.IsZero() {
		t.Fatal(p)
	}
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE id=$1`, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET next_retry_at=clock_timestamp()-interval '1 second' WHERE id=$1`, jobs[1].ID); err != nil {
		t.Fatal(err)
	}
	progress, err = store.RunProgress(ctx)
	if err != nil || progress[0].Ready != 3 || progress[0].WaitingLease != 0 || progress[0].WaitingBackoff != 0 {
		t.Fatal(progress, err)
	}
}
