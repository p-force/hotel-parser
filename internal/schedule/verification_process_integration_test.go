//go:build integration

package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

type processResult struct {
	Jobs      []storage.Job
	LeaseLost bool
	AttemptID string
}

func TestPostgresVerificationProcessHelper(t *testing.T) {
	mode := os.Getenv("HOTEL_PARSER_PROCESS_HELPER")
	if mode == "" {
		t.Skip("subprocess helper only")
	}
	schema := os.Getenv("HOTEL_PARSER_PROCESS_SCHEMA")
	if !strings.HasPrefix(schema, "hp_test_") {
		t.Fatal("unsafe helper schema")
	}
	s, err := storage.Open(context.Background(), os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var result processResult
	if mode == "claim" {
		result.Jobs, err = s.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: os.Getenv("HOTEL_PARSER_PROCESS_OWNER"), RunID: os.Getenv("HOTEL_PARSER_PROCESS_RUN")})
	} else {
		data, e := os.ReadFile(os.Getenv("HOTEL_PARSER_PROCESS_LEASE"))
		if e != nil {
			t.Fatal(e)
		}
		var lease storage.Lease
		if e = json.Unmarshal(data, &lease); e != nil {
			t.Fatal(e)
		}
		now := time.Now().UTC()
		result.AttemptID, err = s.CompleteJob(context.Background(), lease, storage.AttemptInput{JobID: lease.JobID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeNoOffers, HTTPStatus: 200})
		result.LeaseLost = errors.Is(err, storage.ErrLeaseLost)
		if result.LeaseLost {
			err = nil
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if err := os.WriteFile(os.Getenv("HOTEL_PARSER_PROCESS_RESULT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresVerificationIndependentProcessFencing(t *testing.T) {
	s, db, schema := storagetest.Open(t)
	r := testRegistry(t)
	r.Groups[0].Schema = schema
	service := testService(t, r, nil, collect.RuntimeOptions{})
	planned, err := service.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	command := func(mode, owner, name string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPostgresVerificationProcessHelper$", "-test.timeout=10s")
		cmd.Env = append(os.Environ(), "HOTEL_PARSER_PROCESS_HELPER="+mode, "HOTEL_PARSER_PROCESS_OWNER="+owner, "HOTEL_PARSER_PROCESS_RUN="+planned.Planned[0].ID, "HOTEL_PARSER_PROCESS_SCHEMA="+schema, "HOTEL_PARSER_PROCESS_RESULT="+filepath.Join(dir, name), "HOTEL_PARSER_PROCESS_LEASE="+filepath.Join(dir, "lease.json"))
		return cmd
	}
	read := func(name string) processResult {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var r processResult
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, b := command("claim", "a", "a.json"), command("claim", "b", "b.json")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		_ = a.Process.Kill()
		_ = a.Wait()
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(); err != nil {
		t.Fatal(err)
	}
	first, second := read("a.json"), read("b.json")
	jobs := append(first.Jobs, second.Jobs...)
	if len(jobs) != 1 {
		t.Fatalf("active lease issued to multiple processes: %+v %+v", first, second)
	}
	old := jobs[0]
	if _, err := db.Exec(`UPDATE "`+schema+`".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, old.ID); err != nil {
		t.Fatal("isolated lease expiration failed")
	}
	if err := command("claim", "new", "new.json").Run(); err != nil {
		t.Fatal(err)
	}
	new := read("new.json").Jobs
	if len(new) != 1 || new[0].ID != old.ID || new[0].LeaseToken != old.LeaseToken+1 {
		t.Fatalf("cross-process recovery failed: %+v", new)
	}
	submit := func(lease storage.Lease, name string) processResult {
		data, _ := json.Marshal(lease)
		if err := os.WriteFile(filepath.Join(dir, "lease.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := command("complete", "", name).Run(); err != nil {
			t.Fatal(err)
		}
		return read(name)
	}
	if !submit(old.Lease(), "stale.json").LeaseLost {
		t.Fatal("old process lease accepted")
	}
	if submit(new[0].Lease(), "ok.json").AttemptID == "" {
		t.Fatal("current process could not commit")
	}
	progress, err := s.Progress(context.Background(), "")
	if err != nil || progress.Completed != 1 {
		t.Fatalf("durable completion: %+v %v", progress, err)
	}
	t.Log("independent processes: one claim winner, token increment, stale write rejected, one committed attempt")
}
