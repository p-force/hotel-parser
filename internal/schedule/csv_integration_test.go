//go:build integration

package schedule

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/report"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresAutomaticCSVAcrossResumeAndRestart(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOTEL_PARSER_CSV_DIR", directory)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	registry := testGroups(t, server.URL)
	registry.Groups = registry.Groups[:1]
	options := collect.RuntimeOptions{MaxJobs: 4, Batch: collect.BatchOptions{Workers: 4}}
	s := testService(t, registry, server, options)
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(12)}); err != nil {
		t.Fatal(err)
	}
	first, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(first.Exports) != 0 || first.Groups[0].Runs[0].Remaining != 4 {
		t.Fatal("unfinished run exported", first, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("premature artifact", entries, err)
	}
	second, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(second.Exports) != 1 {
		t.Fatal(second, err)
	}
	artifact := second.Exports[0]
	if !artifact.Ready || !artifact.Generated || artifact.RetryPending || artifact.RunID != second.Groups[0].Runs[0].Run.ID {
		t.Fatal(artifact)
	}
	body, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(records) != 9 {
		t.Fatal(len(records), err)
	}
	if calls.Load() != 8 {
		t.Fatal("collection count changed", calls.Load())
	}
	replay, err := s.Execute(context.Background(), Request{Action: "tick", Slot: slot(12)})
	if err != nil || len(replay.Exports) != 1 || !replay.Exports[0].Ready || replay.Exports[0].Generated || calls.Load() != 8 {
		t.Fatal("duplicate tick/export", replay, err)
	}
	// Новый процесс с тем же диском повторно использует готовый файл.
	restarted := testService(t, registry, server, options)
	replay, err = restarted.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(replay.Exports) != 1 || replay.Exports[0].Path != artifact.Path || replay.Exports[0].Generated {
		t.Fatal(replay, err)
	}
	// Другой ephemeral filesystem: тот же completed run воспроизводится из БД.
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	fresh := testService(t, registry, server, options)
	recovered, err := fresh.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(recovered.Exports) != 1 || !recovered.Exports[0].Generated || calls.Load() != 8 {
		t.Fatal(recovered, err)
	}
	restored, err := os.ReadFile(recovered.Exports[0].Path)
	if err != nil || !bytes.Equal(body, restored) {
		t.Fatal("restart CSV differs", err)
	}
	if err := os.Remove(recovered.Exports[0].Path); err != nil {
		t.Fatal(err)
	}
	again, err := fresh.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || !again.Exports[0].Generated || calls.Load() != 8 {
		t.Fatal(again, err)
	}
}

func TestPostgresCSVWriteFailureDoesNotChangeCollection(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOTEL_PARSER_CSV_DIR", blocker)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	registry := testGroups(t, server.URL)
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 1})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || response.Groups[0].Runs[0].Succeeded != 1 || response.Groups[0].Runs[0].Run.Status != storage.RunSucceeded || len(response.Exports) != 1 {
		t.Fatal("CSV error changed collection", response, err)
	}
	failed := response.Exports[0]
	if failed.Ready || !failed.RetryPending || failed.ErrorCode == "" || failed.Generated {
		t.Fatal(failed)
	}
	if _, err := os.Stat(failed.Path); err == nil {
		t.Fatal("failure published CSV")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	restarted := testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 1})
	recovered, err := restarted.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(recovered.Exports) != 1 || !recovered.Exports[0].Ready || !recovered.Exports[0].Generated || calls.Load() != 1 {
		t.Fatal("missing retry without HTTP", recovered, err)
	}
}

func TestPostgresCSVDoesNotHoldRunnerLock(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	registry := testGroups(t, "https://reservationsteps.ru")
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, nil, collect.RuntimeOptions{})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	store := s.groups[0].Store
	jobs, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "csv-test", PropertyID: "arena33"})
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	now := time.Now().UTC()
	lease := jobs[0].Lease()
	input := storage.AttemptInput{JobID: jobs[0].ID, Number: 1, Lease: &lease, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeFailed, HTTPStatus: 403, Error: &storage.AttemptError{Code: "http_error"}}
	if _, err := store.SaveAttempt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	_, db, _ := storagetest.Open(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`LOCK TABLE "` + registry.Groups[0].Schema + `".observation_offers IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Execute(context.Background(), Request{Action: "resume"}); done <- err }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	found := false
	for !found {
		select {
		case <-deadline.C:
			t.Fatal("CSV read did not start")
		case <-tick.C:
			filepath.WalkDir(s.csvDirectory, func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && filepath.Ext(path) == ".tmp" {
					found = true
				}
				return nil
			})
		}
	}
	lock, err := store.TryRunnerLock(context.Background())
	if err != nil {
		t.Fatal("export holds collection lock", err)
	}
	lock.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CSV worker stuck")
	}
}

func TestPostgresCSVContextDoesNotPublish(t *testing.T) {
	registry := testGroups(t, "https://reservationsteps.ru")
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, nil, collect.RuntimeOptions{})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	store := s.groups[0].Store
	jobs, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "csv-cancel"})
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	now := time.Now().UTC()
	lease := jobs[0].Lease()
	if _, err := store.SaveAttempt(context.Background(), storage.AttemptInput{JobID: jobs[0].ID, Number: 1, Lease: &lease, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeFailed, Error: &storage.AttemptError{Code: "http_error"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(context.Background(), jobs[0].RunID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	artifact, err := report.Generate(ctx, store, "arena33", t.TempDir(), jobs[0].RunID)
	if !errors.Is(err, context.Canceled) || artifact.Ready || !artifact.RetryPending {
		t.Fatal(artifact, err)
	}
}

func TestPostgresAutomaticCSVSourceAndGroupIsolation(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	_, _, aSchema := storagetest.Open(t)
	_, _, bSchema := storagetest.Open(t)
	a, err := config.Load("../../configs/arena33.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.Load("../../configs/aerostar.json")
	if err != nil {
		t.Fatal(err)
	}
	a.Collection.Enabled = true
	b.Collection.Enabled = true
	a.ID = "same_hotel"
	b.ID = "same_hotel"
	a.Inventory.Categories = a.Inventory.Categories[:1]
	a.Inventory.Categories[0].SourceRoomID = "1"
	a.Inventory.TotalRooms = a.Inventory.Categories[0].TotalRooms
	info, body := travelLineRegressionFixture(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/ApiWebDistribution/BookingForm/hotel_info":
			w.Write(info)
		case "/ApiWebDistribution/BookingForm/hotel_availability":
			w.Write(body)
		default:
			fmt.Fprint(w, successHTML)
		}
	}))
	defer server.Close()
	regressionTransport(t, server)
	a.Source.BaseURL = server.URL
	b.Source.BaseURL = server.URL
	registry := Registry{Groups: []Group{{ID: "bnovo_group", Schema: aSchema, Properties: []Property{{Hotel: a}}}, {ID: "travelline_group", Schema: bSchema, Properties: []Property{{Hotel: b}}}}}
	s := testService(t, registry, nil, collect.RuntimeOptions{MaxJobs: 2, Batch: collect.BatchOptions{Workers: 2}})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: regressionSlot()}); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(response.Exports) != 2 || calls.Load() != 3 {
		t.Fatal(response, err, calls.Load())
	}
	paths := map[string]bool{}
	for _, artifact := range response.Exports {
		if !artifact.Ready || !artifact.Generated || paths[artifact.Path] {
			t.Fatal("artifact isolation", artifact)
		}
		paths[artifact.Path] = true
		content, err := os.ReadFile(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		records, err := csv.NewReader(bytes.NewReader(content)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		columns := map[string]int{}
		for i, c := range records[0] {
			columns[c] = i
		}
		wantSource, wantSchema, wantRows := "bnovo", aSchema, 1
		if artifact.GroupID == "travelline_group" {
			wantSource, wantSchema, wantRows = "travelline", bSchema, 42
		}
		if len(records)-1 != wantRows {
			t.Fatal("offerless categories lost", len(records)-1, wantRows)
		}
		empty, native := 0, 0
		for _, row := range records[1:] {
			for key, want := range map[string]string{"group_id": artifact.GroupID, "schema": wantSchema, "source": wantSource, "run_id": artifact.RunID, "property_id": "same_hotel"} {
				if row[columns[key]] != want {
					t.Fatal("mixed CSV", key, row)
				}
			}
			if row[columns["rate_id"]] == "" {
				empty++
				if row[columns["stock_status"]] != "unknown" || row[columns["available"]] != "" {
					t.Fatal(row)
				}
			}
			if row[columns["reported_quantity"]] != "" {
				native++
			}
		}
		if wantSource == "travelline" && (empty != 2 || native != 40) {
			t.Fatal("native quota/unknown lost", empty, native)
		}
		if wantSource == "bnovo" && native != 0 {
			t.Fatal("Bnovo reinterpreted as native quota")
		}
	}
	replay, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(replay.Exports) != 2 || calls.Load() != 3 {
		t.Fatal(replay, err)
	}
}
