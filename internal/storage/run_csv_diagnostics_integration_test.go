//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresRunCSVDiagnostics(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	sc := scenario("diagnostics")
	run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut.AddDate(0, 0, 3)})
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]storage.Job, 4)
	for i := range jobs {
		input := sc
		input.CheckIn = input.CheckIn.AddDate(0, 0, i)
		input.CheckOut = input.CheckOut.AddDate(0, 0, i)
		jobs[i], err = store.CreateJob(ctx, run.ID, input)
		if err != nil {
			t.Fatal(err)
		}
	}
	warnings := []storage.Warning{{Code: "plans_metadata_missing", Context: "category-a"}, {Code: "category_availability_unknown", Context: "category-b"}}
	categoryWarnings := []storage.Warning{{Code: "category_availability_unknown", Context: "category-a"}, {Code: "unavailability_dates_mismatch", Context: "2026-12-21/2026-12-22"}}
	// Two offers repeat category diagnostics; the second category has no offer.
	partial := csvAttempt(jobs[0].ID, 1, storage.OutcomePartial, []storage.Observation{
		{CategoryID: "category-a", CategoryName: "A", StockStatus: storage.StockUnknown, HasOffers: true, Warnings: categoryWarnings, Variants: []storage.Variant{{ID: "v", Adults: 1}}, Offers: []storage.Offer{
			{VariantID: "v", RateID: "r1", Visible: true, ReportedQuantity: ptr(0)},
			{VariantID: "v", RateID: "r2", Visible: true},
		}},
		{CategoryID: "category-b", CategoryName: "B", StockStatus: storage.StockUnknown, Warnings: []storage.Warning{}},
	})
	partial.Warnings = warnings
	if _, err := store.SaveAttempt(ctx, partial); err != nil {
		t.Fatal(err)
	}
	none := csvAttempt(jobs[1].ID, 1, storage.OutcomeNoOffers, []storage.Observation{{CategoryID: "category-a", CategoryName: "A", StockStatus: storage.StockKnown, Available: ptr(0), ConfirmedUnavailable: true, Warnings: []storage.Warning{{Code: "plans_metadata_missing", Context: "category-a"}}}})
	none.Warnings = []storage.Warning{{Code: "source_warning", Context: "no-offers-context"}}
	if _, err := store.SaveAttempt(ctx, none); err != nil {
		t.Fatal(err)
	}
	failed := csvAttempt(jobs[2].ID, 1, storage.OutcomeFailed, nil)
	failed.Error = &storage.AttemptError{Code: "timeout"}
	failed.HTTPStatus = 0
	failed.Warnings = []storage.Warning{{Code: "source_warning", Context: "failed-without-observation"}}
	if _, err := store.SaveAttempt(ctx, failed); err != nil {
		t.Fatal(err)
	}
	// A job exhausted by a crashed worker has neither attempt nor observation.
	claimed, err := store.ClaimJobs(ctx, storage.ClaimOptions{Owner: "diagnostic-fixture", RunID: run.ID, MaxClaims: 1})
	if err != nil || len(claimed) != 1 || claimed[0].ID != jobs[3].ID {
		t.Fatal(claimed, err)
	}
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE id=$1`, jobs[3].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimJobs(ctx, storage.ClaimOptions{Owner: "recovery-fixture", RunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	count, err := store.WriteRunCSV(ctx, "g", run.ID, &output)
	if err != nil || count != 6 {
		t.Fatal("diagnostics multiplied/lost rows", count, err)
	}
	rows := readCSVRows(t, output.Bytes())
	var sqlCount int
	if err := db.QueryRow(`SELECT count(*) FROM "`+schema+`".scenario_jobs j LEFT JOIN "`+schema+`".collection_attempts a ON a.job_id=j.id AND a.attempt_no=j.attempts LEFT JOIN "`+schema+`".observations o ON o.attempt_id=a.id LEFT JOIN "`+schema+`".observation_offers f ON f.observation_id=o.id WHERE j.run_id=$1`, run.ID).Scan(&sqlCount); err != nil || len(rows) != sqlCount {
		t.Fatal(sqlCount, len(rows), err)
	}
	assertWarnings := func(cell string, want []storage.Warning) {
		t.Helper()
		var got []storage.Warning
		if err := json.Unmarshal([]byte(cell), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("warnings=%q parsed=%+v want=%+v: %v", cell, got, want, err)
		}
	}
	seen := map[string]int{}
	for _, row := range rows {
		seen[row["job_id"]]++
		switch row["job_id"] {
		case jobs[0].ID:
			if row["job_status"] != "partial" || row["stock_status"] != "unknown" || row["http_status"] != "200" || row["error_code"] != "" || row["confirmed_unavailable"] != "false" || row["available"] != "" {
				t.Fatal(row)
			}
			// Compare to PostgreSQL, not the input order: persistence already canonicalizes warnings.
			var storedAttempt, storedCategory string
			if err := db.QueryRow(`SELECT a.warnings::text,o.warnings::text FROM "`+schema+`".collection_attempts a JOIN "`+schema+`".observations o ON o.attempt_id=a.id WHERE a.job_id=$1 AND o.category_id=$2`, jobs[0].ID, row["category_id"]).Scan(&storedAttempt, &storedCategory); err != nil {
				t.Fatal(err)
			}
			if row["attempt_warning_codes"] != storedAttempt || row["category_warning_codes"] != storedCategory {
				t.Fatal("warnings differ from persisted evidence", row)
			}
			if row["category_id"] == "category-a" {
				var parsed []storage.Warning
				if err := json.Unmarshal([]byte(row["category_warning_codes"]), &parsed); err != nil || len(parsed) != 2 {
					t.Fatal(row, err)
				}
				if row["has_offers"] != "true" || row["price"] != "" {
					t.Fatal(row)
				}
				if row["rate_id"] == "r1" && row["reported_quantity"] != "0" {
					t.Fatal(row)
				}
				if row["rate_id"] == "r2" && row["reported_quantity"] != "" {
					t.Fatal(row)
				}
			} else {
				if row["category_id"] != "category-b" || row["has_offers"] != "false" || row["rate_id"] != "" || row["category_warning_codes"] != "[]" {
					t.Fatal(row)
				}
			}
		case jobs[1].ID:
			if row["job_status"] != "no_offers" || row["stock_status"] != "known" || row["available"] != "0" || row["has_offers"] != "false" || row["confirmed_unavailable"] != "true" || row["rate_id"] != "" {
				t.Fatal(row)
			}
			assertWarnings(row["attempt_warning_codes"], none.Warnings)
			assertWarnings(row["category_warning_codes"], none.Observations[0].Warnings)
		case jobs[2].ID:
			if row["error_code"] != "timeout" || row["http_status"] != "0" || row["job_status"] != "failed" || row["category_id"] != "" || row["stock_status"] != "" || row["has_offers"] != "" || row["confirmed_unavailable"] != "" || row["category_warning_codes"] != "" {
				t.Fatal(row)
			}
			assertWarnings(row["attempt_warning_codes"], failed.Warnings)
		case jobs[3].ID:
			if row["error_code"] != "lease_exhausted" || row["job_status"] != "failed" || row["http_status"] != "" || row["attempt_id"] != "" || row["attempt_warning_codes"] != "" || row["category_warning_codes"] != "" || row["has_offers"] != "" || row["confirmed_unavailable"] != "" {
				t.Fatal(row)
			}
		default:
			t.Fatal(row)
		}
	}
	if seen[jobs[0].ID] != 3 || seen[jobs[1].ID] != 1 || seen[jobs[2].ID] != 1 || seen[jobs[3].ID] != 1 {
		t.Fatal(seen)
	}
}
