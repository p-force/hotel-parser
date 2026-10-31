//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresPlanPublicationRollbackAndLedger(t *testing.T) {
	s, db, schema := storagetest.Open(t)
	sc := scenario("arena33")
	input := storage.RunInput{ID: "00000000-0000-0000-0000-000000000027", PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut, StartedAt: sc.CheckIn}
	bad := sc
	bad.CheckOut = bad.CheckOut.AddDate(0, 0, 1)
	bad.LOS = 2
	if _, err := s.PlanRun(context.Background(), input, []storage.Scenario{sc, bad}); err == nil {
		t.Fatal("out-of-range scenario accepted")
	}
	if _, err := s.GetRun(context.Background(), input.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("partial plan published: %v", err)
	}
	for range 2 {
		if _, err := s.PlanRun(context.Background(), input, []storage.Scenario{sc}); err != nil {
			t.Fatal(err)
		}
	}
	progress, err := s.Progress(context.Background(), input.ID)
	if err != nil || progress.Queued != 1 {
		t.Fatalf("duplicate plan: %+v %v", progress, err)
	}
	if _, err := db.Exec(`UPDATE "` + schema + `".collection_plans SET plan_hash=decode(repeat('00',32),'hex')`); err == nil {
		t.Fatal("plan ledger mutable")
	}
	if _, err := db.Exec(`TRUNCATE "` + schema + `".collection_plans`); err == nil {
		t.Fatal("plan ledger truncatable")
	}
}

func TestPostgresRunnerLockAcrossPoolsAndConnectionLoss(t *testing.T) {
	s, _, schema := storagetest.Open(t)
	other := secondPool(t, schema)
	lock, err := s.TryRunnerLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lock.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := other.TryRunnerLock(context.Background()); !errors.Is(err, storage.ErrRunnerBusy) {
		t.Fatalf("two active runner processes allowed: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Check(context.Background()); err == nil {
		t.Fatal("lost connection still owns runner lock")
	}
	next, err := other.TryRunnerLock(context.Background())
	if err != nil {
		t.Fatalf("closed runner blocks recovery: %v", err)
	}
	defer next.Close()
}
