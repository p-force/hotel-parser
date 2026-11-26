//go:build integration

package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresUnavailabilityReasonPersistenceAndReplay(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		reason *storage.UnavailabilityReason
	}{
		{"booking_forbidden", ptr(storage.UnavailabilityReasonBookingForbidden)},
		{"unspecified", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, job := runJob(t, store, tc.name)
			input := attempt(job.ID)
			input.Outcome = storage.OutcomeNoOffers
			input.Observations = []storage.Observation{
				{CategoryID: "category_a", CategoryName: "A", StockStatus: storage.StockKnown, Available: ptr(0), ConfirmedUnavailable: true, UnavailabilityReason: tc.reason},
				{CategoryID: "category_b", CategoryName: "B", StockStatus: storage.StockKnown, Available: ptr(0), ConfirmedUnavailable: true},
			}
			id, err := store.SaveAttempt(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			var raw sql.NullString
			if err := db.QueryRow(`SELECT unavailability_reason FROM "`+schema+`".observations WHERE attempt_id=$1::uuid AND category_id='category_a'`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if raw.Valid != (tc.reason != nil) || (tc.reason != nil && raw.String != string(*tc.reason)) {
				t.Fatal("wrong SQL NULL/value", raw)
			}
			restarted := secondPool(t, schema)
			loaded, err := restarted.GetAttempt(ctx, job.ID, 1)
			if err != nil || loaded.ID != id || len(loaded.Observations) != 2 {
				t.Fatal("attempt read failed", err)
			}
			for i, observation := range loaded.Observations {
				want := input.Observations[i]
				if observation.CategoryID != want.CategoryID || !observation.ConfirmedUnavailable || observation.HasOffers || observation.StockStatus != storage.StockKnown || observation.Available == nil || *observation.Available != 0 {
					t.Fatal("availability changed", observation)
				}
				if want.UnavailabilityReason == nil {
					if observation.UnavailabilityReason != nil {
						t.Fatal("NULL became a reason", observation)
					}
				} else if observation.UnavailabilityReason == nil || *observation.UnavailabilityReason != *want.UnavailabilityReason {
					t.Fatal("reason lost on read", observation)
				}
			}
			if again, err := restarted.SaveAttempt(ctx, input); err != nil || again != id {
				t.Fatal("identical payload replay failed", again, err)
			}
			changed := input
			changed.Observations = append([]storage.Observation(nil), input.Observations...)
			if tc.reason == nil {
				changed.Observations[0].UnavailabilityReason = ptr(storage.UnavailabilityReasonBookingForbidden)
			} else {
				changed.Observations[0].UnavailabilityReason = nil
			}
			if _, err := restarted.SaveAttempt(ctx, changed); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("different reason must conflict, not rewrite history", err)
			}
			var attempts, observations int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM "`+schema+`".collection_attempts WHERE job_id=$1::uuid),(SELECT count(*) FROM "`+schema+`".observations WHERE attempt_id=$2::uuid)`, job.ID, id).Scan(&attempts, &observations); err != nil || attempts != 1 || observations != 2 {
				t.Fatal("replay duplicated history", attempts, observations, err)
			}
			if _, err := restarted.FinishRun(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			// ReadRunDiagnostics is the other public read path; it delegates to GetAttempt.
			data, err := restarted.ReadRunDiagnostics(ctx, run.ID)
			if err != nil || len(data.Scenarios) != 1 || data.Scenarios[0].Attempt == nil || len(data.Scenarios[0].Attempt.Observations) != 2 {
				t.Fatal("diagnostic read failed", err)
			}
			got := data.Scenarios[0].Attempt.Observations[0].UnavailabilityReason
			if (got == nil) != (tc.reason == nil) || (tc.reason != nil && *got != *tc.reason) {
				t.Fatal("reason lost in diagnostic read", got)
			}
		})
	}
}
