//go:build integration

package schedule

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresScheduleConcurrentStartResumeCancelAndRecovery(t *testing.T) {
	_, db, schema := storagetest.Open(t)
	entered := make(chan struct{}, 1)
	var blocked atomic.Bool
	blocked.Store(true)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if blocked.Load() {
			entered <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, successHTML)
	}))
	defer server.Close()
	registry := testRegistry(t)
	registry.Groups[0].Schema = schema
	p := &registry.Groups[0].Properties[0]
	p.Hotel.Source.BaseURL = server.URL
	p.Hotel.Inventory.Categories = p.Hotel.Inventory.Categories[:1]
	p.Hotel.Inventory.Categories[0].SourceRoomID = "1"
	p.Hotel.Inventory.PublicObservedMax = p.Hotel.Inventory.Categories[0].PublicObservedMax
	competitor := *p
	competitor.Hotel.ID = "competitor"
	registry.Groups[0].Properties = append(registry.Groups[0].Properties, competitor)
	options := collect.RuntimeOptions{MaxJobs: 2, Batch: collect.BatchOptions{Workers: 1}, Claim: storage.ClaimOptions{LeaseDuration: time.Second}, RenewInterval: 100 * time.Millisecond}
	first := testService(t, registry, server, options)
	second := testService(t, registry, server, options)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := first.Execute(ctx, Request{Action: "start", Slot: slot(20)}); done <- e }()
	await(t, entered)
	for _, req := range []Request{{Action: "start", Slot: slot(20)}, {Action: "resume"}} {
		if _, e := second.Execute(context.Background(), req); !errors.Is(e, storage.ErrRunnerBusy) {
			t.Fatal("concurrent invocation bypassed lock", e)
		}
	}
	cancel()
	if e := await(t, done); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	blocked.Store(false)
	// Only the isolated fixture lease is advanced; no production clocks or rows change.
	if _, e := db.Exec(`UPDATE "` + schema + `".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE status='running'`); e != nil {
		t.Fatal(e)
	}
	response, e := second.Execute(context.Background(), Request{Action: "resume"})
	if e != nil || !response.ProgressKnown {
		t.Fatal(response, e)
	}
	beforeRemaining, afterRemaining := 0, 0
	for _, g := range response.Before {
		for _, p := range g.Runs {
			beforeRemaining += p.Remaining
		}
	}
	for _, g := range response.Groups {
		for _, p := range g.Runs {
			afterRemaining += p.Remaining
			if p.Succeeded != 1 || p.Run.FinishedAt == nil {
				t.Fatal(p)
			}
		}
	}
	if beforeRemaining != 2 || afterRemaining != 0 {
		t.Fatal("incorrect durable progress", beforeRemaining, afterRemaining)
	}
	for table, want := range map[string]int{"collection_runs": 2, "scenario_jobs": 2, "collection_attempts": 2, "observations": 2, "observation_offers": 2, "csv_deliveries": 2} {
		var n int
		if e := db.QueryRow(`SELECT count(*) FROM "` + schema + `"."` + table + `"`).Scan(&n); e != nil || n != want {
			t.Fatal(table, n, e)
		}
	}
	// A new service models a new process; completed jobs remain terminal.
	restart := testService(t, registry, server, options)
	count := calls.Load()
	if _, e = restart.Execute(context.Background(), Request{Action: "start", Slot: slot(20)}); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != count {
		t.Fatal("completed source requests repeated")
	}
}
