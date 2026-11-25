//go:build integration

package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/p-force/hotel-parser/internal/config"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresAvailabilityDiagnosticsRecovery(t *testing.T) {
	for _, source := range []string{"bnovo", "travelline"} {
		t.Run(source, func(t *testing.T) {
			store, db, schema := storagetest.Open(t)
			ctx := context.Background()
			fixture := diagnosticFixture(source)
			sc := fixture.Scenarios[0].Job.Scenario
			run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckIn.AddDate(0, 0, 7)})
			if err != nil {
				t.Fatal(err)
			}
			var saved storage.AttemptInput
			for i, s := range fixture.Scenarios {
				job, err := store.CreateJob(ctx, run.ID, s.Job.Scenario)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					var b bytes.Buffer
					if err := WriteRunDiagnostics(ctx, store, run.ID, &b); !errors.Is(err, storage.ErrIncomplete) || b.Len() != 0 {
						t.Fatal("unfinished run exported", err)
					}
				}
				a := s.Attempt.AttemptInput
				a.JobID = job.ID
				a.Number = 1
				a.StartedAt = time.Now().UTC()
				a.FinishedAt = a.StartedAt.Add(time.Millisecond)
				if i == 0 {
					a.Outcome = storage.OutcomePartial
					a.Warnings = []storage.Warning{{Code: "category_availability_unknown", Context: "cat"}}
					a.Observations[0].Warnings = []storage.Warning{{Code: "stock_evidence_missing", Context: "cat"}}
					saved = a
				}
				id, err := store.SaveAttempt(ctx, a)
				if err != nil {
					t.Fatal(err)
				}
				if again, err := store.SaveAttempt(ctx, a); err != nil || again != id {
					t.Fatal("replay failed", err)
				}
			}
			// A terminal technical failure with no observation/offer retains its scope.
			extra := sc
			extra.CheckIn = extra.CheckIn.AddDate(0, 0, 1)
			extra.CheckOut = extra.CheckIn.AddDate(0, 0, 1)
			job, err := store.CreateJob(ctx, run.ID, extra)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			_, err = store.SaveAttempt(ctx, storage.AttemptInput{JobID: job.ID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeFailed, HTTPStatus: 503, Error: &storage.AttemptError{Code: "http_error"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.FinishRun(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			var first bytes.Buffer
			if err := WriteRunDiagnostics(ctx, store, run.ID, &first); err != nil {
				t.Fatal(err)
			}
			var report RunDiagnostics
			if err := json.Unmarshal(first.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Data.Scenarios) != 4 || diagnosticCount(report, "duration_dependent_availability") != 2 || diagnosticCount(report, "technical_error") != 1 {
				t.Fatalf("incomplete diagnostics: %+v", report.Findings)
			}
			for _, f := range report.Findings {
				if f.Code == "duration_dependent_availability" && (!reflect.DeepEqual(f.Evidence.AttemptWarnings, saved.Warnings) || !reflect.DeepEqual(f.Evidence.CategoryWarnings, saved.Observations[0].Warnings)) {
					t.Fatal("warning scopes lost", f)
				}
			}
			// Restart: read via an independent store, without a client/runner or HTTP.
			restarted, err := storage.Open(ctx, os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{Schema: schema})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			var second bytes.Buffer
			if err := WriteRunDiagnostics(ctx, restarted, run.ID, &second); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Bytes(), second.Bytes()) {
				t.Fatal("restart changes report")
			}
			changed := saved
			changed.SelectionPolicy = &storage.SelectionPolicy{Mode: "allowlist", RateIDs: []string{"different"}}
			if _, err := store.SaveAttempt(ctx, changed); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("policy conflict accepted", err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".collection_attempts`).Scan(&count); err != nil || count != 4 {
				t.Fatal("attempts duplicated", count, err)
			}
			if _, err := db.Exec(`UPDATE "` + schema + `".attempt_selection_policy SET policy=policy`); err == nil {
				t.Fatal("policy mutable")
			}
			if _, err := db.Exec(`DELETE FROM "` + schema + `".attempt_selection_policy`); err == nil {
				t.Fatal("policy deletable")
			}
			if _, err := db.Exec(`TRUNCATE "` + schema + `".attempt_selection_policy`); err == nil {
				t.Fatal("policy truncatable")
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			var canceledOut bytes.Buffer
			if err := WriteRunDiagnostics(canceled, store, run.ID, &canceledOut); err == nil || canceledOut.Len() != 0 {
				t.Fatal("canceled report", err)
			}
		})
	}
}

func TestPostgresDiagnosticsUnknownConflictAndMissingFacts(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	ctx := context.Background()
	fixture := diagnosticFixture("travelline")
	sc := fixture.Scenarios[0].Job.Scenario
	inventory := config.InventoryConfig{Categories: []config.RoomCategory{{ID: "category", Name: "Category", SourceRoomID: "cat"}, {ID: "missing", Name: "Missing", SourceRoomID: "missing"}}}
	run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckIn.AddDate(0, 0, 5)}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		scenario := sc
		scenario.CheckIn = sc.CheckIn.AddDate(0, 0, i)
		scenario.CheckOut = scenario.CheckIn.AddDate(0, 0, 1)
		job, err := store.CreateJob(ctx, run.ID, scenario)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		a := storage.AttemptInput{JobID: job.ID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomePartial, HTTPStatus: 200, Warnings: []storage.Warning{{Code: "minimum_stay", Context: "cat/rate"}}, Observations: []storage.Observation{{CategoryID: "cat", CategoryName: "Category", StockStatus: storage.StockUnknown}}}
		switch i {
		case 0:
			zero := 0
			a.Outcome = storage.OutcomeNoOffers
			a.Observations[0].Available = &zero
			a.Observations[0].StockStatus = storage.StockKnown
			a.Observations[0].ConfirmedUnavailable = true
		case 1:
			a.Observations[0].StockStatus = storage.StockConflict
			a.Observations[0].Warnings = []storage.Warning{{Code: "stock_quantity_conflict", Context: "cat"}}
		case 2:
			a.Observations = nil
		case 3:
			a.Outcome = storage.OutcomeFailed
			a.Error = &storage.AttemptError{Code: "source_error"}
			a.Observations = nil
		}
		if _, err := store.SaveAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteRunDiagnostics(ctx, store, run.ID, &out); err != nil {
		t.Fatal(err)
	}
	var report RunDiagnostics
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if diagnosticCount(report, "explicit_unavailability") != 1 || diagnosticCount(report, "availability_evidence_conflict") != 1 || diagnosticCount(report, "observations_missing") != 1 || diagnosticCount(report, "observation_missing") != 6 || diagnosticCount(report, "technical_error") != 0 {
		t.Fatalf("lost evidence: %+v", report.Findings)
	}
	for _, f := range report.Findings {
		if f.Cause != "unknown" || len(f.Evidence.AttemptWarnings) != 1 || f.Evidence.AttemptWarnings[0].Context != "cat/rate" {
			t.Fatalf("guessed business cause or lost context: %+v", f)
		}
	}
	if report.Data.Inventory == nil || report.Data.Inventory.Categories[0].PhysicalTotalRooms != nil {
		t.Fatal("NULL inventory lost")
	}
}

func TestPostgresRuntimeCapturesDiagnosticPolicy(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	properties, run, jobs := runtimeFixture(t, store, 1, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, propertySuccessHTML) })
	if _, err := RunQueued(context.Background(), store, properties, runtimeOptions(run.ID, 1)); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetAttempt(context.Background(), jobs[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.SelectionPolicy == nil || attempt.SelectionPolicy.Mode != "all_visible" {
		t.Fatal("runtime omitted policy", attempt.SelectionPolicy)
	}
}
