package travelline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
)

func TestClientBookingRulesSuccess(t *testing.T) {
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 1, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body string
	}{
		{"rules", `{"booking_rules":[{"room_types":["4347","4350"],"forbidden":true,"code":"128299","date":"2026-11-25","min_los":2,"max_los":7,"min_adv_booking":3},{"room_types":["4348"],"code":"128301","date":"2026-11-26"}]}`},
		{"empty", `{"booking_rules":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != bookingRulesPath || r.ContentLength > 0 || r.Header.Get("Accept") != "application/json" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("unexpected booking rules HTTP contract")
				}
				want := url.Values{"hotel": {"585"}, "start_date": {"2026-11-25"}, "end_date": {"2027-01-24"}, "rate_plans[0].code": {"128299"}, "rate_plans[1].code": {"128301"}, "audience": {"BookingForm"}}
				if !reflect.DeepEqual(r.URL.Query(), want) {
					t.Errorf("query: got %v want %v", r.URL.Query(), want)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			original := server.Client()
			before := *original
			client, err := NewClient(server.URL, original, []string{"999"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.BookingRules(context.Background(), "585", start, end, []string{"128299", "128301"})
			if err != nil || got == nil || got.BookingRules == nil || calls.Load() != 1 {
				t.Fatalf("response=%+v err=%v calls=%d", got, err, calls.Load())
			}
			if !reflect.DeepEqual(before, *original) {
				t.Fatal("caller HTTP client changed")
			}
			if tc.name == "empty" {
				if len(got.BookingRules) != 0 {
					t.Fatal("empty response changed", got)
				}
				return
			}
			if len(got.BookingRules) != 2 {
				t.Fatal("rules lost", got)
			}
			first, second := got.BookingRules[0], got.BookingRules[1]
			if !reflect.DeepEqual(first.RoomTypes, []string{"4347", "4350"}) || first.Code != "128299" || first.Date != "2026-11-25" || first.Forbidden == nil || !*first.Forbidden || first.MinLOS == nil || *first.MinLOS != 2 || first.MaxLOS == nil || *first.MaxLOS != 7 || first.MinAdvBooking == nil || *first.MinAdvBooking != 3 {
				t.Fatalf("first rule changed: %+v", first)
			}
			if !reflect.DeepEqual(second.RoomTypes, []string{"4348"}) || second.Code != "128301" || second.Date != "2026-11-26" || second.Forbidden != nil || second.MinLOS != nil || second.MaxLOS != nil || second.MinAdvBooking != nil {
				t.Fatalf("missing restrictions changed: %+v", second)
			}
		})
	}
}

func TestClientBookingRulesFailures(t *testing.T) {
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body, code string
		status           int
		retry            bool
	}{
		{"429", "private source body", "http_error", 429, true},
		{"500", "private source body", "http_error", 500, true},
		{"invalid JSON", `{"private":`, "parse_error", 200, false},
		{"invalid field type", `{"booking_rules":[{"min_los":"private"}]}`, "parse_error", 200, false},
		{"missing rules", `{}`, "parse_error", 200, false},
		{"null rules", `{"booking_rules":null}`, "parse_error", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, server.Client(), publicRates)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.BookingRules(context.Background(), "585", start, start.AddDate(0, 0, 1), []string{"128299"})
			var classified *source.CollectionError
			if got != nil || !errors.As(err, &classified) {
				t.Fatalf("failure represented as response: %+v err=%v", got, err)
			}
			if classified.Code != tc.code || classified.HTTPStatus != tc.status || classified.Retryable != tc.retry || calls.Load() != 1 {
				t.Fatalf("error=%+v calls=%d", classified, calls.Load())
			}
			if tc.status == 429 && classified.RetryAfter != 2*time.Second {
				t.Fatal("Retry-After lost", classified)
			}
			if strings.Contains(classified.Message, "private") {
				t.Fatal("source body leaked into error")
			}
		})
	}
}

func TestClientBookingRulesCancellation(t *testing.T) {
	entered := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client(), publicRates)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	type outcome struct {
		response *bookingRulesResponse
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := client.BookingRules(ctx, "585", start, start.AddDate(0, 0, 1), []string{"128299"})
		done <- outcome{response, err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case result := <-done:
		var classified *source.CollectionError
		if result.response != nil || !errors.As(result.err, &classified) || classified.Code != "canceled" || classified.Retryable {
			t.Fatalf("cancellation: response=%+v err=%v", result.response, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled method did not finish")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("server request was not canceled")
	}
}

func TestClientBookingRulesInvalidRequestSendsNothing(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client(), publicRates)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	response, err := client.BookingRules(context.Background(), "585", start, start.AddDate(0, 0, 1), nil)
	var classified *source.CollectionError
	if response != nil || !errors.As(err, &classified) || classified.Code != "invalid_request" || calls.Load() != 0 {
		t.Fatalf("invalid request: response=%+v err=%v calls=%d", response, err, calls.Load())
	}
}
