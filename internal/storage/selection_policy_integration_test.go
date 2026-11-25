//go:build integration

package storage_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresSelectionPolicyUsesAttemptFencing(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	run, _ := runJob(t, store, "arena33")
	first := claimOne(t, store, run.ID, "first")
	expire(t, db, schema, first)
	second := claimOne(t, store, run.ID, "second")
	input := attempt(first.ID)
	input.Lease = ptr(first.Lease())
	input.SelectionPolicy = &storage.SelectionPolicy{Mode: "allowlist", RateIDs: []string{"r2", "r1"}}
	if _, err := store.SaveAttempt(ctx, input); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatal("stale token accepted", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".attempt_selection_policy`).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan policy persisted", count, err)
	}
	input.Lease = ptr(second.Lease())
	id, err := store.SaveAttempt(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.SaveAttempt(ctx, input)
	if err != nil || again != id {
		t.Fatal("duplicate replay", err)
	}
	read, err := store.GetAttempt(ctx, input.JobID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if read.SelectionPolicy == nil || !reflect.DeepEqual(read.SelectionPolicy.RateIDs, []string{"r1", "r2"}) {
		t.Fatal("policy lost", read.SelectionPolicy)
	}
}
