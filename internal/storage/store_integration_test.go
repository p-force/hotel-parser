//go:build integration

package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func ptr[T any](v T) *T { return &v }
func scenario(property string) storage.Scenario {
	in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
	return storage.Scenario{PropertyID: property, Source: "bnovo", SourceOrigin: "https://reservationsteps.ru", SourcePropertyID: "public-id", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), LOS: 1, Adults: 2, Rooms: 1, ChildrenAges: []int{}, Language: "ru"}
}
func runJob(t *testing.T, s *storage.Store, property string) (storage.Run, storage.Job) {
	t.Helper()
	sc := scenario(property)
	run, err := s.CreateRun(context.Background(), storage.RunInput{PropertyID: property, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateJob(context.Background(), run.ID, sc)
	if err != nil {
		t.Fatal(err)
	}
	return run, job
}
func attempt(job string) storage.AttemptInput {
	finish := time.Now().UTC().Truncate(time.Microsecond)
	return storage.AttemptInput{JobID: job, Number: 1, StartedAt: finish.Add(-10 * time.Millisecond), FinishedAt: finish, DurationMS: 10, QueueWaitMS: ptr(int64(2)), Outcome: storage.OutcomeSuccess, HTTPStatus: 200, Warnings: []storage.Warning{}, Observations: []storage.Observation{{CategoryID: "561429", CategoryName: "Stadium", AreaM2: ptr(21.5), Available: ptr(10), StockStatus: storage.StockKnown, HasOffers: true, Warnings: []storage.Warning{}, Variants: []storage.Variant{{ID: "v2", Adults: 2}, {ID: "v3", Adults: 3}}, Offers: []storage.Offer{{VariantID: "v2", RateID: "rate1", RateName: "Основной", Price: ptr("4910.50"), Currency: ptr("RUB"), Visible: true, DataAvailable: ptr(10), RowAvailable: ptr(true), SelectableQuantities: []int{1, 10}}, {VariantID: "v3", RateID: "rate2", Price: ptr("5100.25"), Currency: ptr("RUB"), DataAvailable: ptr(10), SelectableQuantities: []int{1, 10}}}}}}
}

func TestPostgresOutcomesAndReadback(t *testing.T) {
	s, _, _ := storagetest.Open(t)
	for _, tc := range []struct {
		name   string
		modify func(*storage.AttemptInput)
		status storage.JobStatus
	}{
		{"success", func(*storage.AttemptInput) {}, storage.JobSucceeded},
		{"partial", func(a *storage.AttemptInput) {
			a.Outcome = storage.OutcomePartial
			a.Warnings = []storage.Warning{{Code: "offer_price_invalid", Context: "rate1/v2"}}
		}, storage.JobPartial},
		{"failed", func(a *storage.AttemptInput) {
			a.Outcome = storage.OutcomeFailed
			a.HTTPStatus = 403
			a.Error = &storage.AttemptError{Code: "http_error"}
			a.Observations = nil
		}, storage.JobFailed},
		{"retryable", func(a *storage.AttemptInput) {
			a.Outcome = storage.OutcomeFailed
			a.HTTPStatus = 429
			a.Error = &storage.AttemptError{Code: "http_error", Retryable: true}
			a.NextRetryAt = ptr(a.FinishedAt.Add(time.Second))
			a.Observations = nil
		}, storage.JobRetryable},
		{"unknown", func(a *storage.AttemptInput) {
			a.Outcome = storage.OutcomePartial
			a.Observations[0].Available = nil
			a.Observations[0].StockStatus = storage.StockUnknown
			a.Observations[0].HasOffers = false
			a.Observations[0].Warnings = []storage.Warning{{Code: "stock_evidence_missing", Context: "561429"}}
		}, storage.JobPartial},
		{"confirmed_unavailable", func(a *storage.AttemptInput) {
			a.Outcome = storage.OutcomeNoOffers
			o := &a.Observations[0]
			o.Available = ptr(0)
			o.HasOffers = false
			o.ConfirmedUnavailable = true
			o.Offers = nil
			o.Variants = nil
		}, storage.JobNoOffers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, job := runJob(t, s, "arena33")
			input := attempt(job.ID)
			tc.modify(&input)
			id, err := s.SaveAttempt(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := s.GetAttempt(context.Background(), job.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			state, err := s.GetJob(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.ID != id || loaded.Outcome != input.Outcome || loaded.HTTPStatus != input.HTTPStatus || loaded.DurationMS != 10 || loaded.QueueWaitMS == nil || *loaded.QueueWaitMS != 2 || state.Status != tc.status || state.Attempts != 1 {
				t.Fatalf("attempt/state lost: %+v %+v", loaded, state)
			}
			if len(loaded.Warnings) != len(input.Warnings) {
				t.Fatal("partial warnings lost")
			}
			if len(input.Observations) > 0 {
				o := loaded.Observations[0]
				if o.AreaM2 == nil || *o.AreaM2 != 21.5 || o.StockStatus != input.Observations[0].StockStatus || o.ConfirmedUnavailable != input.Observations[0].ConfirmedUnavailable {
					t.Fatalf("observation lost: %+v", o)
				}
				if input.Observations[0].Available == nil {
					if o.Available != nil {
						t.Fatal("unknown converted to zero")
					}
				} else if o.Available == nil || *o.Available != *input.Observations[0].Available {
					t.Fatal("availability lost")
				}
				if len(o.Offers) > 0 && (o.Offers[0].Price == nil || *o.Offers[0].Price != "4910.50" || o.Offers[0].Currency == nil || *o.Offers[0].Currency != "RUB" || o.Offers[0].RateID != "rate1" || len(o.Variants) != 2) {
					t.Fatalf("price/rate/variant lost: %+v", o)
				}
			}
			if tc.status == storage.JobRetryable {
				if state.NextRetryAt == nil || loaded.NextRetryAt == nil || !state.NextRetryAt.Equal(*loaded.NextRetryAt) {
					t.Fatal("retry schedule lost")
				}
				if _, err := s.FinishRun(context.Background(), run.ID); !errors.Is(err, storage.ErrIncomplete) {
					t.Fatalf("unfinished retry run completed: %v", err)
				}
				next := attempt(job.ID)
				next.Number = 2
				if _, err := s.SaveAttempt(context.Background(), next); err != nil {
					t.Fatal(err)
				}
			}
			finished, err := s.FinishRun(context.Background(), run.ID)
			if err != nil || finished.FinishedAt == nil {
				t.Fatalf("finish run: %+v %v", finished, err)
			}
		})
	}
}

func TestPostgresIdempotencyAndHistory(t *testing.T) {
	s, db, schema := storagetest.Open(t)
	first, job := runJob(t, s, "arena33")
	duplicate, err := s.CreateJob(context.Background(), first.ID, scenario("arena33"))
	if err != nil || duplicate.ID != job.ID {
		t.Fatalf("job replay: %v", err)
	}
	data := attempt(job.ID)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); id, err := s.SaveAttempt(context.Background(), data); ids <- id; errs <- err }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	expected := ""
	for id := range ids {
		if expected == "" {
			expected = id
		}
		if id == "" || id != expected {
			t.Fatal("concurrent duplicate inserted another attempt")
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Повторная запись восстановленного SELECT payload также идемпотентна.
	loaded, err := s.GetAttempt(context.Background(), job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.SaveAttempt(context.Background(), loaded.AttemptInput); err != nil || id != expected {
		t.Fatalf("readback replay: %v", err)
	}
	changed := attempt(job.ID)
	changed.Observations[0].Offers[0].Price = ptr("9999.99")
	if _, err = s.SaveAttempt(context.Background(), changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("changed attempt overwrote history: %v", err)
	}
	second, secondJob := runJob(t, s, "arena33")
	if second.ID == first.ID {
		t.Fatal("recollection reused run")
	}
	next := attempt(secondJob.ID)
	next.Observations[0].Available = ptr(4)
	next.Observations[0].Offers[0].Price = ptr("2500.99")
	if _, err = s.SaveAttempt(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	original, err := s.GetAttempt(context.Background(), job.ID, 1)
	if err != nil || *original.Observations[0].Available != 10 || *original.Observations[0].Offers[0].Price != "4910.50" {
		t.Fatalf("old history changed: %v", err)
	}
	var attempts, observations, offers int
	if err = db.QueryRow(`SELECT (SELECT count(*) FROM "`+schema+`".collection_attempts),(SELECT count(*) FROM "`+schema+`".observations),(SELECT count(*) FROM "`+schema+`".observation_offers)`).Scan(&attempts, &observations, &offers); err != nil {
		t.Fatal("count history failed")
	}
	if attempts != 2 || observations != 2 || offers != 4 {
		t.Fatalf("duplicate/summed facts: attempts=%d observations=%d offers=%d", attempts, observations, offers)
	}
}

func TestPostgresRollbackConstraintsAndAppendOnly(t *testing.T) {
	s, db, schema := storagetest.Open(t)
	run, job := runJob(t, s, "arena33")
	data := attempt(job.ID)
	data.Observations[0].Offers[1].VariantID = "missing-variant"
	_, err := s.SaveAttempt(context.Background(), data)
	var failure *storage.Error
	if !errors.As(err, &failure) || failure.Code != "23503" {
		t.Fatalf("expected real FK failure: %v", err)
	}
	state, err := s.GetJob(context.Background(), job.ID)
	if err != nil || state.Status != storage.JobQueued || state.Attempts != 0 {
		t.Fatalf("job state not rolled back: %+v %v", state, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM "` + schema + `".collection_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatal("attempt insert not rolled back")
	}
	if err = db.QueryRow(`SELECT count(*) FROM "` + schema + `".observations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("observation insert not rolled back")
	}
	if _, err = s.SaveAttempt(context.Background(), attempt(job.ID)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"collection_attempts", "observations", "observation_variants", "observation_offers"} {
		for _, query := range []string{`UPDATE "` + schema + `".` + table + ` SET ` + map[string]string{"collection_attempts": "duration_ms=duration_ms", "observations": "category_name=category_name", "observation_variants": "adults=adults", "observation_offers": "rate_name=rate_name"}[table], `DELETE FROM "` + schema + `".` + table, `TRUNCATE "` + schema + `".` + table + ` CASCADE`} {
			if _, err = db.Exec(query); err == nil {
				t.Errorf("append-only protection missing for %s", table)
			}
		}
	}
	if _, err = db.Exec(`UPDATE "`+schema+`".scenario_jobs SET status='queued',finished_at=NULL WHERE id=$1::uuid`, job.ID); err == nil {
		t.Error("terminal job was rewritten")
	}
	if _, err = s.FinishRun(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateJob(context.Background(), run.ID, scenario("arena33")); err != nil {
		t.Fatalf("completed job replay rejected: %v", err)
	}
}

func TestPostgresMigrationsAndIndexes(t *testing.T) {
	s, db, schema := storagetest.Open(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migration replay failed: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".schema_migrations`).Scan(&count); err != nil || count != 7 {
		t.Fatal("migration version missing or duplicated")
	}
	rows, err := db.Query(`SELECT indexname FROM pg_indexes WHERE schemaname=$1`, schema)
	if err != nil {
		t.Fatal("read indexes failed")
	}
	indexes := map[string]bool{}
	for rows.Next() {
		var name string
		rows.Scan(&name)
		indexes[name] = true
	}
	rows.Close()
	for _, name := range []string{"collection_runs_property_period_idx", "scenario_jobs_pending_idx", "scenario_jobs_identity_idx", "collection_attempts_errors_idx", "observations_category_history_idx", "observation_offers_rate_idx"} {
		if !indexes[name] {
			t.Errorf("missing index %s", name)
		}
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_constraint WHERE connamespace=(SELECT oid FROM pg_namespace WHERE nspname=$1) AND contype='f'`, schema).Scan(&count); err != nil || count < 5 {
		t.Fatalf("missing foreign keys: %d", count)
	}
}

func TestPostgresScenarioDimensions(t *testing.T) {
	s, _, _ := storagetest.Open(t)
	base := scenario("arena33")
	run, err := s.CreateRun(context.Background(), storage.RunInput{PropertyID: base.PropertyID, PeriodStart: base.CheckIn, PeriodEnd: base.CheckOut.AddDate(0, 0, 2)})
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, change := range []func(*storage.Scenario){
		func(*storage.Scenario) {}, func(s *storage.Scenario) { s.Adults = 1 },
		func(s *storage.Scenario) { s.LOS = 2; s.CheckOut = s.CheckOut.AddDate(0, 0, 1) },
		func(s *storage.Scenario) { s.ChildrenAges = []int{3, 7} },
		func(s *storage.Scenario) {
			s.Source = "another_source"
			s.SourceOrigin = "https://another.example"
			s.SourcePropertyID = "other-id"
		},
		func(s *storage.Scenario) { s.Rooms = 2 }, func(s *storage.Scenario) { s.Language = "en" },
	} {
		input := base
		change(&input)
		job, err := s.CreateJob(context.Background(), run.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		if ids[job.ID] {
			t.Fatal("different scenario dimensions merged")
		}
		ids[job.ID] = true
		loaded, err := s.GetJob(context.Background(), job.ID)
		if err != nil || loaded.Scenario.Adults != input.Adults || loaded.Scenario.LOS != input.LOS || loaded.Scenario.Source != input.Source || loaded.Scenario.Rooms != input.Rooms || loaded.Scenario.Language != input.Language || len(loaded.Scenario.ChildrenAges) != len(input.ChildrenAges) {
			t.Fatalf("scenario fields lost: %+v %v", loaded, err)
		}
	}
}

func TestPostgresMixedJobFailures(t *testing.T) {
	s, _, _ := storagetest.Open(t)
	run, failedJob := runJob(t, s, "arena33")
	other := scenario("arena33")
	other.Adults = 1
	successfulJob, err := s.CreateJob(context.Background(), run.ID, other)
	if err != nil {
		t.Fatal(err)
	}
	failed := attempt(failedJob.ID)
	failed.Outcome = storage.OutcomeFailed
	failed.HTTPStatus = 403
	failed.Error = &storage.AttemptError{Code: "http_error"}
	failed.Observations = nil
	if _, err := s.SaveAttempt(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAttempt(context.Background(), attempt(successfulJob.ID)); err != nil {
		t.Fatal(err)
	}
	finished, err := s.FinishRun(context.Background(), run.ID)
	if err != nil || finished.Status != storage.RunPartial {
		t.Fatalf("mixed run status: %+v %v", finished, err)
	}
	success, err := s.GetAttempt(context.Background(), successfulJob.ID, 1)
	if err != nil || *success.Observations[0].Available != 10 {
		t.Fatalf("failed scenario removed success: %v", err)
	}
	failure, err := s.GetAttempt(context.Background(), failedJob.ID, 1)
	if err != nil || failure.HTTPStatus != 403 {
		t.Fatalf("failure history lost: %v", err)
	}
}
