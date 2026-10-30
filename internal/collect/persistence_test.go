package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
)

func TestStorageAdapterPreservesSemanticsAndRedacts(t *testing.T) {
	for _, adults := range []int{1, 2} {
		result, err := bnovo.Parse(lindsayHTML(t, adults), lindsayRequest(adults))
		if err != nil {
			t.Fatal(err)
		}
		result.CollectedAt = time.Now().UTC()
		result.HTTPStatus = 200
		result.Warnings = append(result.Warnings, bnovo.ParseWarning{Code: "offer_price_invalid", Context: "https://secret.invalid?token=secret", Message: "private secret HTML"})
		for i := range result.Offers {
			result.Offers[i].Prepayment = "payment secret https://pay.invalid/private"
			result.Offers[i].Cancellation = "private secret"
		}
		before, _ := json.Marshal(result)
		finish := time.Now().UTC()
		input := storageAttempt(result, "job", 1, finish.Add(-time.Millisecond), finish, 1, nil, nil)
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "secret") || strings.Contains(string(data), "pay.invalid") || strings.Contains(string(data), "Prepayment") {
			t.Fatalf("source secrets reached persistence: %s", data)
		}
		stocks := NormalizeCategoryStock(result)
		if len(input.Observations) != len(stocks) {
			t.Fatal("categories lost")
		}
		for i, o := range input.Observations {
			if o.CategoryID != stocks[i].CategoryID || o.StockStatus != storage.StockStatus(stocks[i].Status) {
				t.Fatal("normalization changed")
			}
			if stocks[i].Available == nil {
				if o.Available != nil {
					t.Fatal("unknown converted to zero")
				}
			} else if o.Available == nil || *o.Available != *stocks[i].Available {
				t.Fatal("stock changed")
			}
			for _, offer := range o.Offers {
				if offer.Currency == nil || *offer.Currency != "RUB" || offer.Price == nil {
					t.Fatal("offer facts lost")
				}
			}
		}
		after, _ := json.Marshal(result)
		if !bytes.Equal(before, after) {
			t.Fatal("adapter mutated source result")
		}
	}
}

func TestAttemptObserverSeesEveryRetryAndStorageErrors(t *testing.T) {
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dfrom") == "26-10-2026" {
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(propertySuccessHTML))
	}, 0)
	jobs := inputJobs(func(int) *bnovo.Client { return client }, []string{"a", "b"})
	var seen []int
	observer := &attemptObserver{after: func(ctx context.Context, done completedAttempt, number int, next *time.Time) error {
		seen = append(seen, done.index)
		if done.index == 0 {
			if number != 1 || next == nil || done.durationMS < 0 || done.startedAt.IsZero() || done.finishedAt.Before(done.startedAt) {
				t.Error("attempt metadata or retry plan lost")
			}
			return errors.New("controlled persistence failure")
		}
		return nil
	}}
	run, err := collectJobsObserved(context.Background(), jobs, BatchOptions{Workers: 2, Retry: RetryPolicy{MaxAttempts: 3}}, observer)
	if err == nil || len(run.results) != 2 || run.results[1].Status != bnovo.SearchStatusSuccess || run.attempts[0] != 1 || len(seen) != 2 {
		t.Fatalf("storage error changed other job/retried unrecorded HTTP: %+v %v", run.summary, err)
	}
	if run.results[0].HTTPStatus != 429 || run.results[0].Error.Code != "http_error" {
		t.Fatal("persistence error overwrote source result")
	}
}
