//go:build integration

package vercelapp

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/schedule"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresVercelResumeStatusAndEphemeralCSV(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	localDSN := os.Getenv("HOTEL_PARSER_DATABASE_URL")
	// Application delivery is disabled locally; never use the developer's real token.
	t.Setenv("YANDEX_DISK_TOKEN", "")
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	registry, err := schedule.LoadRegistry("../../configs/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	registry.Groups = registry.Groups[:1]
	registry.Groups[0].Schema = schema
	registry.Groups[0].Properties = registry.Groups[0].Properties[:1]
	plans, err := schedule.BuildPlans(registry, time.Date(2026, 11, 10, 17, 0, 0, 0, time.UTC))
	if err != nil || len(plans) != 1 || len(plans[0].Scenarios) != 1 {
		t.Fatal(plans, err)
	}
	plan := plans[0]
	run, err := store.PlanRun(context.Background(), plan.Run, plan.Scenarios)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "vercel-fixture", RunID: run.ID})
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	now := time.Now().UTC()
	lease := jobs[0].Lease()
	_, err = store.SaveAttempt(context.Background(), storage.AttemptInput{JobID: jobs[0].ID, Number: 1, Lease: &lease, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeFailed, HTTPStatus: 403, Error: &storage.AttemptError{Code: "http_error"}})
	if err != nil {
		t.Fatal(err)
	}
	h := testHandler(testEnv())
	h.registry = func() (schedule.Registry, error) { return registry, nil }
	h.status = func(ctx context.Context, _ string, r schedule.Registry) (any, error) {
		return readStatus(ctx, localDSN, r)
	}
	h.open = func(ctx context.Context, _ string, r schedule.Registry, o collect.RuntimeOptions) (application, error) {
		return schedule.Open(ctx, localDSN, r, o)
	}
	// Merely reading diagnostics must not finish runs, generate CSV, or enqueue delivery.
	if w := call(h, "status", "GET", testTrigger, "", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	progress, err := store.RunProgress(context.Background())
	if err != nil || len(progress) != 1 || progress[0].Run.FinishedAt != nil {
		t.Fatal(progress, err)
	}
	deliveries, err := store.Deliveries(context.Background(), registry.Groups[0].ID, 100)
	if err != nil || len(deliveries) != 0 {
		t.Fatal(deliveries, err)
	}
	var original []byte
	for i := 0; i < 2; i++ {
		// Each request opens a new application service; a different /tmp models another instance.
		t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
		w := call(h, "resume", "POST", testTrigger, "", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		progress, err = store.RunProgress(context.Background())
		if err != nil || progress[0].Run.FinishedAt == nil || progress[0].Run.Status != storage.RunFailed || progress[0].Failed != 1 {
			t.Fatal(progress, err)
		}
		// Reconstruct through the existing PostgreSQL CSV API, independent of local files.
		var csvBody bytes.Buffer
		if _, err = store.WriteRunCSV(context.Background(), registry.Groups[0].ID, run.ID, &csvBody); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(bytes.NewReader(csvBody.Bytes())).ReadAll()
		if err != nil || len(rows) != 2 || len(rows[0]) != 35 {
			t.Fatal(rows, err)
		}
		if i == 0 {
			original = append([]byte(nil), csvBody.Bytes()...)
		} else if !bytes.Equal(original, csvBody.Bytes()) {
			t.Fatal("CSV changed after restart")
		}
	}
	for table, want := range map[string]int{"collection_runs": 1, "scenario_jobs": 1, "collection_attempts": 1, "csv_deliveries": 1} {
		var count int
		if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM "`+schema+`"."`+table+`"`).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, err)
		}
	}
	deliveries, err = store.Deliveries(context.Background(), registry.Groups[0].ID, 100)
	if err != nil || len(deliveries) != 1 || deliveries[0].State != "pending" {
		t.Fatal(deliveries, err)
	}
}
