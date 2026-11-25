//go:build integration

package storage_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresDeliveryClaimsRecoveryAndIsolation(t *testing.T) {
	ctx := context.Background()
	s, db, schema := storagetest.Open(t)
	run, job := runJob(t, s, "hotel")
	ensure := func(root string) {
		t.Helper()
		if err := s.EnsureDeliveries(ctx, "group", root); err != nil {
			t.Fatal(err)
		}
	}
	ensure("/root/group")
	if ds, err := s.Deliveries(ctx, "group", 10); err != nil || len(ds) != 0 {
		t.Fatal("unfinished run enqueued", ds, err)
	}
	if _, err := s.SaveAttempt(ctx, attempt(job.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	ensure("/root/group")
	ensure("/other-root")
	ds, err := s.Deliveries(ctx, "group", 10)
	if err != nil || len(ds) != 1 || !strings.HasPrefix(ds[0].RemotePath, "/root/group/") {
		t.Fatal(ds, err)
	}
	var wg sync.WaitGroup
	claims := make(chan storage.Delivery, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.ClaimDelivery(ctx, "group")
			if err == nil {
				claims <- d
			} else {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	if len(claims) != 1 || len(errs) != 1 {
		t.Fatal("concurrent claims", len(claims), len(errs))
	}
	for e := range errs {
		if !errors.Is(e, storage.ErrNotFound) {
			t.Fatal(e)
		}
	}
	stale := <-claims
	if err := s.CompleteDelivery(ctx, stale, "disk_http_429", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(ctx, "group"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("ignored backoff", err)
	}
	table := `"` + schema + `".csv_deliveries`
	if _, err := db.Exec(`UPDATE ` + table + ` SET next_attempt_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	active, err := s.ClaimDelivery(ctx, "group")
	if err != nil || active.Attempts != 2 {
		t.Fatal(active, err)
	}
	if err := s.CompleteDelivery(ctx, stale, "", 0); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatal("stale token", err)
	}
	// Crash без CompleteDelivery: другой процесс восстанавливает expired claim.
	if _, err := db.Exec(`UPDATE ` + table + ` SET leased_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimDelivery(ctx, "group")
	if err != nil || recovered.LeaseToken == active.LeaseToken || recovered.Attempts != 3 {
		t.Fatal(recovered, err)
	}
	if err := s.CompleteDelivery(ctx, active, "", 0); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(ctx, recovered, "", 0); err != nil {
		t.Fatal(err)
	}
	ensure("/root/group")
	if _, err := s.ClaimDelivery(ctx, "group"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("delivered claimed", err)
	}
	if ds, err := s.Deliveries(ctx, "another-group", 10); err != nil || len(ds) != 0 {
		t.Fatal("group leak", ds, err)
	}
	other, _, _ := storagetest.Open(t)
	if ds, err := other.Deliveries(ctx, "group", 10); err != nil || len(ds) != 0 {
		t.Fatal("schema leak", ds, err)
	}
}

func TestPostgresDeliverySlotPaths(t *testing.T) {
	ctx := context.Background()
	a, db, schema := storagetest.Open(t)
	b, _, _ := storagetest.Open(t)
	create := func(s *storage.Store, at time.Time) storage.Run {
		t.Helper()
		sc := scenario("same-hotel")
		run, err := s.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut, StartedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		job, err := s.CreateJob(ctx, run.ID, sc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveAttempt(ctx, attempt(job.ID)); err != nil {
			t.Fatal(err)
		}
		run, err = s.FinishRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	slot := time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC) // 08:00 МСК, независимо от фактического исполнения.
	first := create(a, slot)
	second := create(a, slot.Add(4*time.Hour))
	legacy := create(a, slot.Add(-24*time.Hour))
	create(b, slot)
	old := "/old/v1/" + schema + "/group-a/same-hotel/timestamp/" + legacy.ID + ".csv"
	if _, err := db.Exec(`INSERT INTO "`+schema+`".csv_deliveries(group_id,run_id,remote_path) VALUES($1,$2,$3)`, "group-a", legacy.ID, old); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		s     *storage.Store
		group string
	}{{a, "group-a"}, {b, "group-b"}} {
		if err := tc.s.EnsureDeliveries(ctx, tc.group, "/reports"); err != nil {
			t.Fatal(err)
		}
		ds, err := tc.s.Deliveries(ctx, tc.group, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			want := "/reports/" + tc.group + "/same-hotel/2026-11-02_08-00.v3.csv"
			if d.RunID == second.ID {
				want = "/reports/group-a/same-hotel/2026-11-02_12-00.v3.csv"
			}
			if d.RunID == legacy.ID {
				want = old
			}
			if d.RemotePath != want {
				t.Fatalf("path: got %q want %q", d.RemotePath, want)
			}
		}
	}
	// Повторное enqueue/root change не обновляет ни новый, ни legacy путь.
	before, err := a.Deliveries(ctx, "group-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureDeliveries(ctx, "group-a", "/changed"); err != nil {
		t.Fatal(err)
	}
	after, err := a.Deliveries(ctx, "group-a", 10)
	if err != nil || len(after) != 3 {
		t.Fatal(after, err)
	}
	for i := range before {
		if before[i].RemotePath != after[i].RemotePath {
			t.Fatal("path changed")
		}
	}
	if first.ID == second.ID {
		t.Fatal("distinct slot runs expected")
	}
}
