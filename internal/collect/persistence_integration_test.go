//go:build integration

package collect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresStoredCollectionRetriesAndMultipleHotels(t *testing.T) {
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
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
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
	}, 0)
	properties := propertyInputs(t, client)
	for i := range properties {
		properties[i].CheckIn = lindsayRequest(2).CheckIn
	}
	options := BatchOptions{Workers: 4, Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}
	first, err := CollectPropertiesStored(context.Background(), store, properties, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Runs) != 2 || len(first.JobIDs) != 9 || len(first.Results) != 9 || first.Results[0].Attempts != 2 {
		t.Fatalf("history identity/retry lost: runs=%d jobs=%d results=%d", len(first.Runs), len(first.JobIDs), len(first.Results))
	}
	for i, result := range first.Results {
		job, err := store.GetJob(context.Background(), first.JobIDs[i])
		if err != nil {
			t.Fatal(err)
		}
		if job.Scenario.PropertyID != result.PropertyID || job.Scenario.LOS != result.LOS || job.Scenario.Adults != result.Result.Request.Adults || job.Attempts != result.Attempts {
			t.Fatalf("job/scenario identity changed: %+v %+v", job, result)
		}
		attempt, err := store.GetAttempt(context.Background(), job.ID, job.Attempts)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempt.Observations) != len(result.Result.Categories) || attempt.HTTPStatus != 200 || attempt.DurationMS < 0 || attempt.CollectedAt == nil {
			t.Fatalf("observations/diagnostics lost: %+v", attempt)
		}
	}
	retry, err := store.GetAttempt(context.Background(), first.JobIDs[0], 1)
	if err != nil || retry.HTTPStatus != 429 || retry.Error == nil || !retry.Error.Retryable || retry.NextRetryAt == nil {
		t.Fatalf("retry not saved: %+v %v", retry, err)
	}
	original, err := store.GetAttempt(context.Background(), first.JobIDs[0], 2)
	if err != nil {
		t.Fatal(err)
	}
	if *original.Observations[0].Offers[0].Price != "100" || *original.Observations[0].AreaM2 != 21.5 || *original.Observations[0].Available != 1 {
		t.Fatalf("area/price/stock changed: %+v", original.Observations[0])
	}
	firstPrice.Store(200)
	second, err := CollectPropertiesStored(context.Background(), store, properties, options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Runs[0].ID == first.Runs[0].ID || second.JobIDs[0] == first.JobIDs[0] {
		t.Fatal("new collection overwrote run/job")
	}
	updated, err := store.GetAttempt(context.Background(), second.JobIDs[0], 1)
	if err != nil || *updated.Observations[0].Offers[0].Price != "200" {
		t.Fatalf("new price not stored: %v", err)
	}
	previous, err := store.GetAttempt(context.Background(), first.JobIDs[0], 2)
	if err != nil || *previous.Observations[0].Offers[0].Price != "100" {
		t.Fatal("previous price overwritten")
	}
}

func TestPostgresStoredCollectionCancellation(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	entered := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
		finished <- struct{}{}
	}, 0)
	properties := propertyInputs(t, client)[:1]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		batch StoredBatch
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		batch, err := CollectPropertiesStored(ctx, store, properties, BatchOptions{Workers: 1})
		done <- outcome{batch, err}
	}()
	receive(t, entered)
	cancel()
	result := receive(t, done)
	receive(t, finished)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.batch.Runs) != 1 || result.batch.Runs[0].Status != storage.RunCanceled {
		t.Fatalf("canceled run not persisted: %+v", result.batch.Runs)
	}
	for i, id := range result.batch.JobIDs {
		job, err := store.GetJob(context.Background(), id)
		if err != nil || job.Status != storage.JobCanceled {
			t.Fatalf("canceled job lost: %+v %v", job, err)
		}
		if i == 0 {
			a, err := store.GetAttempt(context.Background(), id, 1)
			if err != nil || a.Error == nil || a.Error.Code != "canceled" {
				t.Fatalf("active cancellation not saved: %+v %v", a, err)
			}
		} else if job.Attempts != 0 {
			t.Fatal("unstarted job invented HTTP attempt")
		}
	}
}

func TestPostgresStoredRetryCancellation(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	scheduled := signalLog(t, "bnovo.batch.retry.scheduled")
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}, 0)
	properties := propertyInputs(t, client)[:1]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		batch StoredBatch
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		batch, err := CollectPropertiesStored(ctx, store, properties, BatchOptions{Workers: 1})
		done <- outcome{batch, err}
	}()
	receive(t, scheduled)
	cancel()
	result := receive(t, done)
	if result.err != nil {
		t.Fatal(result.err)
	}
	job, err := store.GetJob(context.Background(), result.batch.JobIDs[0])
	if err != nil || job.Status != storage.JobCanceled || job.NextRetryAt != nil {
		t.Fatalf("canceled retry job not finalized: %+v %v", job, err)
	}
	first, err := store.GetAttempt(context.Background(), job.ID, 1)
	if err != nil || first.HTTPStatus != 429 || first.NextRetryAt == nil {
		t.Fatalf("scheduled retry history was rewritten: %+v %v", first, err)
	}
	if _, err := store.GetAttempt(context.Background(), job.ID, 2); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cancellation invented a retry: %v", err)
	}
}
