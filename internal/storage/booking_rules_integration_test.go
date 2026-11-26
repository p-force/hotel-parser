//go:build integration

package storage_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func storedBookingRulesInput(runID string) storage.RunBookingRulesInput {
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	return storage.RunBookingRulesInput{RunID: runID, RequestedAt: at, ReceivedAt: at.Add(time.Second), StartDate: day, EndDate: day.AddDate(0, 0, 7), RequestedRateIDs: []string{"128299", "128301", "128299"}, Rules: []storage.BookingRuleInput{
		{Date: day, RoomTypeID: ptr("4347"), RateID: "128299", Forbidden: ptr(true), MinLOS: ptr(5), MaxLOS: ptr(1), MinAdvBooking: ptr(3)},
		{Date: day, RoomTypeID: ptr("4347"), RateID: "128299", Forbidden: ptr(true), MinLOS: ptr(5), MaxLOS: ptr(1), MinAdvBooking: ptr(3)},
		{Date: day, RoomTypeID: ptr("4347"), RateID: "128299", Forbidden: ptr(false), MinLOS: ptr(0), MaxLOS: ptr(0), MinAdvBooking: ptr(0)},
		{Date: day, RoomTypeID: nil, RateID: "128301"},
		{Date: day, RoomTypeID: ptr(""), RateID: "unrequested", MinLOS: ptr(0)},
		{Date: day.AddDate(0, 0, 1), RoomTypeID: ptr("4350"), RateID: "128301", MinLOS: ptr(-1), MaxLOS: ptr(-2)},
	}}
}

func assertStoredBookingRules(t *testing.T, got storage.RunBookingRules, want storage.RunBookingRulesInput) {
	t.Helper()
	if got.ID == "" || got.RunID != want.RunID || !got.RequestedAt.Equal(want.RequestedAt) || !got.ReceivedAt.Equal(want.ReceivedAt) || !got.StartDate.Equal(want.StartDate) || !got.EndDate.Equal(want.EndDate) || !reflect.DeepEqual(got.RequestedRateIDs, want.RequestedRateIDs) || len(got.Rules) != len(want.Rules) {
		t.Fatalf("metadata/count changed: %+v", got)
	}
	used := make([]bool, len(got.Rules))
	ids := map[string]bool{}
	for _, rule := range got.Rules {
		if rule.ID == "" || ids[rule.ID] || rule.BookingRulesID != got.ID {
			t.Fatal("missing/duplicate IDs or broken set linkage", rule)
		}
		ids[rule.ID] = true
	}
	for _, expected := range want.Rules {
		found := false
		for i, actual := range got.Rules {
			if used[i] || !actual.Date.Equal(expected.Date) {
				continue
			}
			value := actual.BookingRuleInput
			value.Date = expected.Date
			if reflect.DeepEqual(value, expected) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			t.Fatalf("rule lost or changed: %+v", expected)
		}
	}
}

func TestPostgresBookingRulesSaveReadReplay(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	run, _ := runJob(t, store, "booking_storage")
	if _, err := store.GetRunBookingRules(ctx, run.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("unsaved set must be missing", err)
	}
	input := storedBookingRulesInput(run.ID)
	saved, err := store.SaveRunBookingRules(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredBookingRules(t, saved, input)
	restarted := secondPool(t, schema)
	read, err := restarted.GetRunBookingRules(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(read, saved) {
		t.Fatal("restart changed set or rule IDs", err)
	}
	for _, reorder := range []bool{false, true} {
		replay := storedBookingRulesInput(run.ID)
		if reorder {
			for i, j := 0, len(replay.Rules)-1; i < j; i, j = i+1, j-1 {
				replay.Rules[i], replay.Rules[j] = replay.Rules[j], replay.Rules[i]
			}
		}
		again, err := restarted.SaveRunBookingRules(ctx, replay)
		if err != nil || !reflect.DeepEqual(again, saved) {
			t.Fatal("identical multiset replay failed", reorder, err)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*storage.RunBookingRulesInput)
	}{
		{"different metadata", func(in *storage.RunBookingRulesInput) { in.ReceivedAt = in.ReceivedAt.Add(time.Second) }},
		{"different request rate order", func(in *storage.RunBookingRulesInput) {
			in.RequestedRateIDs[0], in.RequestedRateIDs[1] = in.RequestedRateIDs[1], in.RequestedRateIDs[0]
		}},
		{"different duplicate count", func(in *storage.RunBookingRulesInput) { in.Rules = append(in.Rules, in.Rules[0]) }},
		{"NULL forbidden vs false", func(in *storage.RunBookingRulesInput) { in.Rules[3].Forbidden = ptr(false) }},
		{"NULL room vs empty", func(in *storage.RunBookingRulesInput) { in.Rules[3].RoomTypeID = ptr("") }},
		{"NULL minimum vs zero", func(in *storage.RunBookingRulesInput) { in.Rules[3].MinLOS = ptr(0) }},
		{"changed maximum", func(in *storage.RunBookingRulesInput) { in.Rules[0].MaxLOS = ptr(7) }},
		{"NULL advance vs zero", func(in *storage.RunBookingRulesInput) { in.Rules[3].MinAdvBooking = ptr(0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := storedBookingRulesInput(run.ID)
			tc.change(&changed)
			result, err := store.SaveRunBookingRules(ctx, changed)
			if !errors.Is(err, storage.ErrConflict) || result.ID != "" {
				t.Fatal("changed set must conflict without returning success", result.ID, err)
			}
		})
	}
	var sets, rules int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM "`+schema+`".run_booking_rules),(SELECT count(*) FROM "`+schema+`".booking_rules)`).Scan(&sets, &rules); err != nil || sets != 1 || rules != len(input.Rules) {
		t.Fatal("replays changed history cardinality", sets, rules, err)
	}
	read, err = store.GetRunBookingRules(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(read, saved) {
		t.Fatal("conflicting replay changed history", err)
	}
}

func TestPostgresBookingRulesEmptyAndMissing(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	ctx := context.Background()
	const missing = "00000000-0000-0000-0000-000000000001"
	if _, err := store.GetRunBookingRules(ctx, missing); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("unknown run should be missing", err)
	}
	if result, err := store.SaveRunBookingRules(ctx, storedBookingRulesInput(missing)); !errors.Is(err, storage.ErrNotFound) || result.ID != "" {
		t.Fatal("set saved for an unknown run", err)
	}
	run, _ := runJob(t, store, "empty_rules")
	input := storedBookingRulesInput(run.ID)
	input.Rules = nil
	saved, err := store.SaveRunBookingRules(ctx, input)
	if err != nil || saved.ID == "" || saved.Rules == nil || len(saved.Rules) != 0 {
		t.Fatal("empty success treated as missing", saved, err)
	}
	read, err := store.GetRunBookingRules(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(saved, read) {
		t.Fatal("empty set read failed", err)
	}
	input.Rules = []storage.BookingRuleInput{}
	again, err := store.SaveRunBookingRules(ctx, input)
	if err != nil || !reflect.DeepEqual(saved, again) {
		t.Fatal("empty set replay duplicated metadata", err)
	}
}

func TestPostgresBookingRulesRollbackOnRuleError(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	run, _ := runJob(t, store, "atomic_rules")
	input := storedBookingRulesInput(run.ID)
	input.Rules = []storage.BookingRuleInput{
		{Date: input.StartDate, RateID: "128299"},
		{Date: input.StartDate.AddDate(0, 0, 1), RateID: "private-invalid\x00rate"},
	}
	result, err := store.SaveRunBookingRules(ctx, input)
	var dbErr *storage.Error
	if result.ID != "" || !errors.As(err, &dbErr) || dbErr.Code != "22021" {
		t.Fatal("expected a database error while inserting second rule", result.ID, err)
	}
	var sets, rules int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM "`+schema+`".run_booking_rules),(SELECT count(*) FROM "`+schema+`".booking_rules)`).Scan(&sets, &rules); err != nil || sets != 0 || rules != 0 {
		t.Fatal("partial transaction leaked", sets, rules, err)
	}
	if _, err := store.GetRunBookingRules(ctx, run.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("rolled back metadata must not look like an empty success", err)
	}
	input.Rules[1].RateID = "128301"
	if _, err := store.SaveRunBookingRules(ctx, input); err != nil {
		t.Fatal("failed transaction occupied the run slot", err)
	}
}

func TestPostgresBookingRulesConcurrentIdenticalSaves(t *testing.T) {
	first, db, schema := storagetest.Open(t)
	second := secondPool(t, schema)
	run, _ := runJob(t, first, "concurrent_rules")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	type outcome struct {
		value storage.RunBookingRules
		err   error
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for _, store := range []*storage.Store{first, second} {
		workers.Add(1)
		go func(s *storage.Store) {
			defer workers.Done()
			<-start
			value, err := s.SaveRunBookingRules(ctx, storedBookingRulesInput(run.ID))
			results <- outcome{value, err}
		}(store)
	}
	close(start)
	workers.Wait()
	close(results)
	var id string
	for result := range results {
		if result.err != nil || result.value.ID == "" || (id != "" && result.value.ID != id) {
			t.Fatal("concurrent save did not reuse the same immutable set", result.err)
		}
		id = result.value.ID
	}
	var sets, rules int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM "`+schema+`".run_booking_rules),(SELECT count(*) FROM "`+schema+`".booking_rules)`).Scan(&sets, &rules); err != nil || sets != 1 || rules != 6 {
		t.Fatal("concurrent save duplicated data", sets, rules, err)
	}
}
