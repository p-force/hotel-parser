//go:build integration

package collect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresRuntimeHistoryRetriesAndMultipleHotels(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	var firstPrice atomic.Int32
	firstPrice.Store(100)
	var mutex sync.Mutex
	calls := make(map[string]int)
	lindsay := map[int]string{1: string(lindsayHTML(t, 1)), 2: string(lindsayHTML(t, 2))}
	arena := strings.ReplaceAll(propertySuccessHTML, `"1"`, `"561429"`)
	arena = strings.ReplaceAll(arena, `data-available="561429"`, `data-available="1"`)
	arena = strings.ReplaceAll(arena, `value="561429"`, `value="1"`)
	arena = strings.ReplaceAll(arena, `"id":1`, `"id":561429`)
	arena = strings.Replace(arena, `"name":"Room"`, `"name":"Stadium","amenities":{"1":{"value":"21.5"}}`, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path + "?" + r.URL.RawQuery
		mutex.Lock()
		calls[key]++
		count := calls[key]
		mutex.Unlock()
		isArena := strings.Contains(r.URL.Path, "7f3f84ae-1f13-419f-af60-ae782d454666")
		if isArena {
			if r.URL.Query().Get("dto") == "20-02-2027" && count == 1 {
				w.WriteHeader(429)
				return
			}
			fmt.Fprint(w, strings.ReplaceAll(arena, `data-tariff-price="100"`, fmt.Sprintf(`data-tariff-price="%d"`, firstPrice.Load())))
			return
		}
		adults := 2
		if r.URL.Query().Get("adults") == "1" {
			adults = 1
		}
		fmt.Fprint(w, lindsay[adults])
	}))
	defer server.Close()
	client, err := bnovo.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	properties := propertyInputs(t, client)
	// Fixture Arena33 содержит одну физическую категорию; ожидаем именно её.
	properties[0].Hotel.Inventory.Categories = properties[0].Hotel.Inventory.Categories[:1]
	properties[0].Hotel.Inventory.PublicObservedMax = properties[0].Hotel.Inventory.Categories[0].PublicObservedMax
	for i := range properties {
		properties[i].CheckIn = lindsayRequest(2).CheckIn
		properties[i].Hotel.Source.BaseURL = server.URL
	}
	options := RuntimeOptions{Claim: storage.ClaimOptions{Owner: "history-worker"}, Batch: BatchOptions{Workers: 4, Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	firstRuns, firstJobs := queueConfiguredTestScenarios(t, store, properties)
	first, err := RunQueued(context.Background(), store, properties, options)
	if err != nil || !first.ProgressKnown || first.Progress.Completed != 9 || first.Progress.Remaining != 0 || len(first.JobIDs) != 9 {
		t.Fatalf("runtime: %+v %v", first, err)
	}
	claimed := make(map[string]bool)
	for _, id := range first.JobIDs {
		if claimed[id] {
			t.Fatal("duplicate claim result")
		}
		claimed[id] = true
	}
	for _, planned := range firstJobs {
		job, err := store.GetJob(context.Background(), planned.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantAttempts := 1
		if planned.ID == firstJobs[0].ID {
			wantAttempts = 2
		}
		if !claimed[job.ID] || job.Scenario.PropertyID != planned.Scenario.PropertyID || job.Scenario.SourcePropertyID != planned.Scenario.SourcePropertyID || job.Scenario.LOS != planned.Scenario.LOS || job.Scenario.Adults != planned.Scenario.Adults || !job.Scenario.CheckIn.Equal(planned.Scenario.CheckIn) || !job.Scenario.CheckOut.Equal(planned.Scenario.CheckOut) || job.Attempts != wantAttempts {
			t.Fatalf("job/scenario identity or retry lost: %+v", job)
		}
		attempt, err := store.GetAttempt(context.Background(), job.ID, job.Attempts)
		if err != nil {
			t.Fatal(err)
		}
		count := 10
		if job.Scenario.PropertyID == "arena33" {
			count = 1
		}
		if len(attempt.Observations) != count || attempt.HTTPStatus != 200 || attempt.DurationMS < 0 || attempt.CollectedAt == nil || attempt.Lease == nil {
			t.Fatalf("observations/diagnostics/lease lost: %+v", attempt)
		}
	}
	retry, err := store.GetAttempt(context.Background(), firstJobs[0].ID, 1)
	if err != nil || retry.HTTPStatus != 429 || retry.Error == nil || !retry.Error.Retryable || retry.NextRetryAt == nil {
		t.Fatalf("retry not saved: %+v %v", retry, err)
	}
	original, err := store.GetAttempt(context.Background(), firstJobs[0].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if *original.Observations[0].Offers[0].Price != "100" || *original.Observations[0].AreaM2 != 21.5 || *original.Observations[0].Available != 1 {
		t.Fatalf("area/price/stock changed: %+v", original.Observations[0])
	}
	firstPrice.Store(200)
	secondRuns, secondJobs := queueConfiguredTestScenarios(t, store, properties)
	second, err := RunQueued(context.Background(), store, properties, options)
	if err != nil || second.Progress.Completed != 18 || second.Progress.Remaining != 0 || len(second.JobIDs) != 9 {
		t.Fatalf("second runtime: %+v %v", second, err)
	}
	if secondRuns[0].ID == firstRuns[0].ID || secondJobs[0].ID == firstJobs[0].ID {
		t.Fatal("new collection overwrote run/job")
	}
	// RunQueued выполняет jobs; финализацию run в production вызывает Service.
	for _, run := range append(firstRuns, secondRuns...) {
		saved, err := store.FinishRun(context.Background(), run.ID)
		if err != nil || saved.Status == storage.RunRunning || saved.FinishedAt == nil {
			t.Fatalf("run not finalized: %+v %v", saved, err)
		}
	}
	updated, err := store.GetAttempt(context.Background(), secondJobs[0].ID, 1)
	if err != nil || *updated.Observations[0].Offers[0].Price != "200" {
		t.Fatalf("new price not stored: %v", err)
	}
	previous, err := store.GetAttempt(context.Background(), firstJobs[0].ID, 2)
	if err != nil || *previous.Observations[0].Offers[0].Price != "100" {
		t.Fatal("previous price overwritten")
	}
}

// Наполняет тестовую PostgreSQL-очередь через существующие repository API.
// Production-планы по-прежнему создаёт schedule.Service; runtime читает готовые jobs.
func queueConfiguredTestScenarios(t *testing.T, store *storage.Store, properties []PropertyCollection) ([]storage.Run, []storage.Job) {
	t.Helper()
	var runs []storage.Run
	var jobs []storage.Job
	for _, property := range properties {
		requests, err := configuredRequests(property.Hotel, property.CheckIn)
		if err != nil || len(requests) == 0 {
			t.Fatalf("test scenarios: %v", err)
		}
		end := requests[0].CheckOut
		for _, request := range requests {
			if request.CheckOut.After(end) {
				end = request.CheckOut
			}
		}
		run, err := store.CreateRun(context.Background(), storage.RunInput{PropertyID: property.Hotel.ID, PeriodStart: requests[0].CheckIn, PeriodEnd: end})
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
		for i, request := range requests {
			job, err := store.CreateJob(context.Background(), run.ID, storage.Scenario{PropertyID: property.Hotel.ID, Source: property.Hotel.Source.Type, SourceOrigin: property.Hotel.Source.BaseURL, SourcePropertyID: request.PropertyID, CheckIn: request.CheckIn, CheckOut: request.CheckOut, LOS: property.Hotel.Collection.Nights[i%len(property.Hotel.Collection.Nights)], Adults: request.Adults, Rooms: property.Hotel.Collection.Rooms, ChildrenAges: request.ChildrenAges, Language: request.Language})
			if err != nil {
				t.Fatal(err)
			}
			jobs = append(jobs, job)
		}
	}
	return runs, jobs
}
