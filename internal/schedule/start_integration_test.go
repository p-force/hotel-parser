//go:build integration

package schedule

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/storage"
)

func TestPostgresStartPublishesAllGroupsBeforeHTTPAndSkipsOldQueue(t *testing.T) {
	var service *Service
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for _, g := range service.groups {
			progress, e := g.Store.RunProgress(r.Context())
			if e != nil || len(progress) != 2 {
				t.Errorf("HTTP before every plan committed: %v %v", progress, e)
				w.WriteHeader(500)
				return
			}
			for _, p := range progress {
				if p.Run.StartedAt.Equal(slot(8)) && p.Remaining+p.Succeeded != 61 {
					t.Errorf("incomplete plan: %+v", p)
				}
			}
		}
		fmt.Fprint(w, successHTML)
	}))
	defer server.Close()
	registry := testGroups(t, server.URL)
	service = testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 2, Batch: collect.BatchOptions{Workers: 2}})
	oldSlot := slot(20).AddDate(0, 0, -1)
	if _, e := service.Execute(context.Background(), Request{Action: "plan", Slot: oldSlot}); e != nil {
		t.Fatal(e)
	}
	for pass := 0; pass < 2; pass++ {
		result, e := service.Execute(context.Background(), Request{Action: "start", Slot: slot(8)})
		if e != nil || len(result.Planned) != 2 || !result.ProgressKnown {
			t.Fatal(result, e)
		}
		for _, g := range service.groups {
			progress, e := g.Store.RunProgress(context.Background())
			if e != nil || len(progress) != 2 {
				t.Fatal(progress, e)
			}
			for _, p := range progress {
				if p.Run.StartedAt.Equal(oldSlot) && (p.Queued != 1 || p.Succeeded != 0) {
					t.Fatal("old HTTP ran before current slot", p)
				}
				if p.Run.StartedAt.Equal(slot(8)) && (p.Succeeded != pass+1 || p.Remaining != 60-pass) {
					t.Fatal("duplicate or skipped current job", p)
				}
			}
		}
	}
	if calls.Load() != 4 {
		t.Fatal(calls.Load())
	}
}

func TestPostgresStartTerminalReplayCSVAndDeliveryLedger(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	registry := testGroups(t, server.URL)
	service := testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 2, Batch: collect.BatchOptions{Workers: 2}})
	for pass := 0; pass < 2; pass++ {
		// One job per group at the smallest supported slot; all source traffic is mock.
		response, e := service.Execute(context.Background(), Request{Action: "start", Slot: slot(20)})
		if e != nil || !response.ProgressKnown || len(response.Exports) != 2 {
			t.Fatal(response, e)
		}
		for _, a := range response.Exports {
			if !a.Ready {
				t.Fatal(a)
			}
			if _, e := os.Stat(a.Path); e != nil {
				t.Fatal(e)
			}
		}
		for _, g := range service.groups {
			progress, e := g.Store.RunProgress(context.Background())
			if e != nil || len(progress) != 1 || progress[0].Succeeded != 1 || progress[0].Remaining != 0 || progress[0].Run.FinishedAt == nil {
				t.Fatal(progress, e)
			}
			deliveries, e := g.Store.Deliveries(context.Background(), g.ID, 100)
			if e != nil || len(deliveries) != 1 || deliveries[0].State != "pending" {
				t.Fatal(deliveries, e)
			}
			jobs, e := g.Store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "replay-audit", RunStartedAt: slot(20)})
			if e != nil || len(jobs) != 0 {
				t.Fatal(jobs, e)
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatal("completed jobs repeated", calls.Load())
	}
	// Context cancellation before a start cannot publish another slot or execute HTTP.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := service.Execute(ctx, Request{Action: "start", Slot: slot(8).AddDate(0, 0, 1)}); e == nil {
		t.Fatal("canceled start succeeded")
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}

func TestPostgresStartPlanFailureDoesNotRunAnyHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	registry := testGroups(t, server.URL)
	service := testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 2})
	plans, e := BuildPlans(registry, slot(8))
	if e != nil {
		t.Fatal(e)
	}
	plans[1].Scenarios[0].Adults = 2
	if _, e = service.groups[1].Store.PlanRun(context.Background(), plans[1].Run, plans[1].Scenarios); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Execute(context.Background(), Request{Action: "start", Slot: slot(8)}); e == nil {
		t.Fatal("changed plan accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("runner started before all plans committed", calls.Load())
	}
}
