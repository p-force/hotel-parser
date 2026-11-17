//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func csvAttempt(job string, number int, outcome storage.Outcome, observations []storage.Observation) storage.AttemptInput {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return storage.AttemptInput{JobID: job, Number: number, StartedAt: now, FinishedAt: now, Outcome: outcome, HTTPStatus: 200, CollectedAt: &now, Observations: observations}
}
func readCSVRows(t *testing.T, body []byte) []map[string]string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Fatal("empty CSV")
	}
	result := []map[string]string{}
	for _, record := range records[1:] {
		row := map[string]string{}
		for i, k := range records[0] {
			row[k] = record[i]
		}
		result = append(result, row)
	}
	return result
}

type brokenCSVWriter struct{}

func (brokenCSVWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type cancelCSVWriter struct{ cancel context.CancelFunc }

func (w cancelCSVWriter) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }

func TestPostgresRunCSVSemantics(t *testing.T) {
	for _, provider := range []string{"bnovo", "travelline"} {
		t.Run(provider, func(t *testing.T) {
			store, db, schema := storagetest.Open(t)
			ctx := context.Background()
			in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
			run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: "hotel", PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 6)})
			if err != nil {
				t.Fatal(err)
			}
			jobs := []storage.Job{}
			for i := 0; i < 5; i++ {
				sc := scenario("hotel")
				sc.Source = provider
				sc.CheckIn = in.AddDate(0, 0, i)
				sc.CheckOut = sc.CheckIn.AddDate(0, 0, 1)
				job, err := store.CreateJob(ctx, run.ID, sc)
				if err != nil {
					t.Fatal(err)
				}
				jobs = append(jobs, job)
			}
			native := func(q int) *int {
				if provider == "travelline" {
					return ptr(q)
				}
				return nil
			}
			observation := func(id string, status storage.StockStatus, available *int, offers []storage.Offer) storage.Observation {
				vars := []storage.Variant{}
				if len(offers) > 0 {
					vars = append(vars, storage.Variant{ID: "v", Adults: 2})
				}
				return storage.Observation{CategoryID: id, CategoryName: "Категория, \"A\"\nстрока", AreaM2: ptr(21.5), StockStatus: status, Available: available, HasOffers: len(offers) > 0, Variants: vars, Offers: offers}
			}
			offer := func(rate string, quantity *int) storage.Offer {
				return storage.Offer{VariantID: "v", RateID: rate, RateName: "Тариф, \"quoted\"", Price: ptr("100.50"), Currency: ptr("RUB"), Visible: true, ReportedQuantity: quantity}
			}
			retry := csvAttempt(jobs[0].ID, 1, storage.OutcomeFailed, nil)
			retry.HTTPStatus = 500
			retry.Error = &storage.AttemptError{Code: "http_error", Retryable: true}
			retry.NextRetryAt = ptr(retry.FinishedAt.Add(time.Millisecond))
			if _, err := store.SaveAttempt(ctx, retry); err != nil {
				t.Fatal(err)
			}
			success := csvAttempt(jobs[0].ID, 2, storage.OutcomeSuccess, []storage.Observation{observation("known", storage.StockKnown, ptr(9), []storage.Offer{offer("r1", native(9)), offer("r2", native(9))})})
			if _, err := store.SaveAttempt(ctx, success); err != nil {
				t.Fatal(err)
			}
			partial := csvAttempt(jobs[1].ID, 1, storage.OutcomePartial, []storage.Observation{
				observation("conflict", storage.StockConflict, nil, []storage.Offer{offer("r1", native(9)), offer("r2", native(10))}),
				observation("unknown", storage.StockUnknown, nil, nil), observation("missing", storage.StockUnknown, nil, []storage.Offer{offer("missing", nil)}),
				observation("zero_signal", storage.StockUnknown, nil, []storage.Offer{offer("zero", native(0))}),
			})
			if _, err := store.SaveAttempt(ctx, partial); err != nil {
				t.Fatal(err)
			}
			none := observation("unavailable", storage.StockKnown, ptr(0), nil)
			none.ConfirmedUnavailable = true
			if _, err := store.SaveAttempt(ctx, csvAttempt(jobs[2].ID, 1, storage.OutcomeNoOffers, []storage.Observation{none})); err != nil {
				t.Fatal(err)
			}
			failed := csvAttempt(jobs[3].ID, 1, storage.OutcomeFailed, nil)
			failed.HTTPStatus = 403
			failed.Error = &storage.AttemptError{Code: "http_error"}
			if _, err := store.SaveAttempt(ctx, failed); err != nil {
				t.Fatal(err)
			}
			claimed, err := store.ClaimJobs(ctx, storage.ClaimOptions{Owner: "exhausted", RunID: run.ID, MaxClaims: 1})
			if err != nil || len(claimed) != 1 {
				t.Fatal(claimed, err)
			}
			if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE id=$1`, claimed[0].ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimJobs(ctx, storage.ClaimOptions{Owner: "sweep", RunID: run.ID}); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if _, err := store.WriteRunCSV(ctx, "group", run.ID, &output); !errors.Is(err, storage.ErrIncomplete) || output.Len() != 0 {
				t.Fatal("unfinished run exported", err)
			}
			if _, err := store.FinishRun(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			// Другая история в той же схеме не должна попасть в CSV.
			_, other := runJob(t, store, "other")
			if _, err := store.SaveAttempt(ctx, attempt(other.ID)); err != nil {
				t.Fatal(err)
			}
			count, err := store.WriteRunCSV(ctx, "group", run.ID, &output)
			if err != nil {
				t.Fatal(err)
			}
			rows := readCSVRows(t, output.Bytes())
			if count != 10 || len(rows) != 10 {
				t.Fatal("missing/duplicate CSV rows", count, len(rows))
			}
			var sqlCount int
			if err := db.QueryRow(`SELECT count(*) FROM "`+schema+`".scenario_jobs j LEFT JOIN "`+schema+`".collection_attempts a ON a.job_id=j.id AND a.attempt_no=j.attempts LEFT JOIN "`+schema+`".observations o ON o.attempt_id=a.id LEFT JOIN "`+schema+`".observation_offers f ON f.observation_id=o.id WHERE j.run_id=$1`, run.ID).Scan(&sqlCount); err != nil || sqlCount != len(rows) {
				t.Fatal(sqlCount, err)
			}
			byKey := map[string]map[string]string{}
			for _, row := range rows {
				if row["run_id"] != run.ID || row["schema"] != schema || row["property_id"] != "hotel" || row["source"] != provider || row["los"] != "1" || row["adults"] != "2" || row["rooms"] != "1" {
					t.Fatal(row)
				}
				for _, k := range []string{"run_started_at", "run_finished_at", "collected_at", "attempt_started_at", "attempt_finished_at"} {
					if row[k] != "" {
						if _, err := time.Parse(time.RFC3339Nano, row[k]); err != nil || !strings.HasSuffix(row[k], "Z") {
							t.Fatal(k, row[k], err)
						}
					}
				}
				if row["category_id"] != "" && (row["category_name"] != "Категория, \"A\"\nстрока" || row["area_m2"] != "21.5") {
					t.Fatal("CSV escaping/number", row)
				}
				if row["job_id"] == jobs[0].ID && row["attempt_no"] != "2" {
					t.Fatal("retry history included", row)
				}
				byKey[row["category_id"]+"/"+row["rate_id"]] = row
			}
			if byKey["unknown/"]["available"] != "" || byKey["unavailable/"]["available"] != "0" || byKey["unavailable/"]["attempt_outcome"] != "no_offers" {
				t.Fatal("NULL/zero/no_offers changed", byKey)
			}
			for _, rid := range []string{"r1", "r2"} {
				row := byKey["conflict/"+rid]
				want := ""
				if provider == "travelline" {
					want = map[string]string{"r1": "9", "r2": "10"}[rid]
				}
				if row["available"] != "" || row["stock_status"] != "conflict" || row["reported_quantity"] != want || row["price"] != "100.50" {
					t.Fatal(row)
				}
			}
			wantZero := ""
			if provider == "travelline" {
				wantZero = "0"
			}
			if byKey["zero_signal/zero"]["reported_quantity"] != wantZero || byKey["missing/missing"]["reported_quantity"] != "" {
				t.Fatal("native NULL/zero changed")
			}
			exhausted, failedRow := false, false
			for _, row := range rows {
				if row["job_id"] == jobs[4].ID {
					exhausted = row["job_status"] == "failed" && row["error_code"] == "lease_exhausted" && row["attempt_id"] == "" && row["attempt_outcome"] == ""
				}
				if row["job_id"] == jobs[3].ID {
					failedRow = row["attempt_outcome"] == "failed" && row["http_status"] == "403" && row["error_code"] == "http_error"
				}
			}
			if !exhausted || !failedRow {
				t.Fatal("failed scenarios lost")
			}
			var repeat bytes.Buffer
			if _, err := store.WriteRunCSV(ctx, "group", run.ID, &repeat); err != nil || !bytes.Equal(output.Bytes(), repeat.Bytes()) {
				t.Fatal("CSV is not deterministic", err)
			}
			if _, err := store.WriteRunCSV(ctx, "group", run.ID, brokenCSVWriter{}); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatal("writer error lost", err)
			}
			canceled, cancel := context.WithCancel(ctx)
			if _, err := store.WriteRunCSV(canceled, "group", run.ID, cancelCSVWriter{cancel}); !errors.Is(err, context.Canceled) {
				t.Fatal("writer cancellation lost", err)
			}
		})
	}
}

type countCSVWriter struct{ bytes int }

func (w *countCSVWriter) Write(p []byte) (int, error) { w.bytes += len(p); return len(p), nil }
func TestPostgresRunCSVStreaming(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	run, job := runJob(t, store, "stream")
	input := attempt(job.ID)
	o := &input.Observations[0]
	o.Variants = o.Variants[:1]
	o.Offers = nil
	for i := 0; i < 2000; i++ {
		o.Offers = append(o.Offers, storage.Offer{VariantID: "v2", RateID: "rate-" + strconv.Itoa(i), Price: ptr("123.45"), Currency: ptr("RUB")})
	}
	if _, err := store.SaveAttempt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	writer := &countCSVWriter{}
	n, err := store.WriteRunCSV(context.Background(), "group", run.ID, writer)
	if err != nil || n != 2000 || writer.bytes < 200000 {
		t.Fatal(n, writer.bytes, err)
	}
	// Writer хранит только счётчик; exporter не собирает [][]records/Attempt history.
}
