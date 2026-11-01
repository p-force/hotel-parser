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
