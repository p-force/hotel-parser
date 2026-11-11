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
