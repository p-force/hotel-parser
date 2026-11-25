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

func rawQuotas() *storage.RoomTypeQuotaSet {
	return &storage.RoomTypeQuotaSet{Items: []storage.RoomTypeQuota{
		{Ordinal: 0, RPH: "duplicate", Quantity: ptr(10), SourceRoomID: ptr("cat"), MappingStatus: storage.QuotaMapped},
		{Ordinal: 1, RPH: "duplicate", Quantity: ptr(0), SourceRoomID: ptr("cat"), MappingStatus: storage.QuotaMapped},
		{Ordinal: 2, RPH: "duplicate", MappingStatus: storage.QuotaConflict},
		{Ordinal: 3, RPH: "unmatched", Quantity: ptr(1 << 40), MappingStatus: storage.QuotaUnmapped},
		{Ordinal: 4, RPH: "", Quantity: ptr(-1), MappingStatus: storage.QuotaUnmapped},
	}}
}

func TestPostgresRawQuotasReadReplayAndRestart(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		set  *storage.RoomTypeQuotaSet
	}{{"absent", nil}, {"empty", &storage.RoomTypeQuotaSet{Items: []storage.RoomTypeQuota{}}}, {"all", rawQuotas()}, {"failed", rawQuotas()}} {
		t.Run(tc.name, func(t *testing.T) {
			_, job := runJob(t, store, tc.name)
			a := attempt(job.ID)
			a.Observations = nil
			a.Outcome = storage.OutcomePartial
			a.RoomTypeQuotas = tc.set
			if tc.name == "failed" {
				a.Outcome = storage.OutcomeFailed
				a.Error = &storage.AttemptError{Code: "parse_error"}
			}
			id, err := store.SaveAttempt(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			again, err := store.SaveAttempt(ctx, a)
			if err != nil || again != id {
				t.Fatal("idempotency", err)
			}
			restarted := secondPool(t, schema)
			read, err := restarted.GetAttempt(ctx, job.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(read.RoomTypeQuotas, tc.set) || len(read.Observations) != 0 {
				t.Fatalf("read changed raw array: %+v", read.RoomTypeQuotas)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM "`+schema+`".attempt_room_type_quotas WHERE attempt_id=$1::uuid`, id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if tc.set != nil {
				expected = len(tc.set.Items)
			}
			if count != expected {
				t.Fatal("duplicates or lost rows", count, expected)
			}
			if tc.set != nil {
				changed := a
				changed.RoomTypeQuotas = rawQuotas()
				if tc.name == "all" || tc.name == "failed" {
					*changed.RoomTypeQuotas.Items[0].Quantity = 11
				}
				if _, err := store.SaveAttempt(ctx, changed); !errors.Is(err, storage.ErrConflict) {
					t.Fatal("changed raw evidence replay accepted", err)
				}
			}
		})
	}
	for _, table := range []string{"attempt_room_type_quotas", "attempt_room_type_quota_sets"} {
		for _, action := range []string{"UPDATE ", "DELETE FROM ", "TRUNCATE "} {
			suffix := ""
			if action == "UPDATE " {
				suffix = " SET attempt_id=attempt_id"
			}
			if _, err := db.Exec(action + `"` + schema + `".` + table + suffix); err == nil {
				t.Fatal("mutable raw history", action, table)
			}
		}
	}
}

func TestPostgresRawQuotasAtomicAndFenced(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	run, _ := runJob(t, store, "fencing")
	first := claimOne(t, store, run.ID, "first")
	expire(t, db, schema, first)
	second := claimOne(t, store, run.ID, "second")
	a := attempt(first.ID)
	a.Lease = ptr(first.Lease())
	a.RoomTypeQuotas = rawQuotas()
	if _, err := store.SaveAttempt(ctx, a); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatal("stale fencing accepted", err)
	}
	a.Lease = ptr(second.Lease())
	a.RoomTypeQuotas.Items[1].RPH = "invalid\x00text"
	if _, err := store.SaveAttempt(ctx, a); err == nil {
		t.Fatal("expected DB write error")
	}
	for _, table := range []string{"collection_attempts", "observations", "attempt_room_type_quota_sets", "attempt_room_type_quotas"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial transaction leaked", table, count, err)
		}
	}
	a.RoomTypeQuotas = rawQuotas()
	if _, err := store.SaveAttempt(ctx, a); err != nil {
		t.Fatal(err)
	}
}
