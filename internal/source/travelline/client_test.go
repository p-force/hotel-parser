package travelline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
)

func TestClientVerifiedGETContract(t *testing.T) {
	info, body, req := fixture(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.ContentLength > 0 || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Accept") != "application/json" {
			t.Error("unexpected HTTP contract")
		}
		q := r.URL.Query()
		if q.Get("language") != "ru-ru" || q.Get("audience") != "BookingForm" {
			t.Error(q)
		}
		switch r.URL.Path {
		case infoPath:
			if len(q) != 3 || q.Get("hotels[0].code") != "585" {
				t.Error(q)
			}
			w.Write(info)
		case availabilityPath:
			if len(q) != 9 || q.Get("criterions[0].hotels[0].code") != "585" || q.Get("criterions[0].adults") != "2" || q.Get("criterions[0].dates") != "2026-11-17;2026-11-18" || q.Get("include_all_placements") != "false" || q.Get("include_rates") != "true" || q.Get("include_promo_restricted") != "true" || q.Get("include_transfers") != "true" {
				t.Error(q)
			}
			w.Write(body)
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	original := server.Client()
	before := *original
	client, err := NewClient(server.URL, original, publicRates)
	if err != nil {
		t.Fatal(err)
	}
	result := client.Collect(context.Background(), req)
	if result.Error != nil || result.HTTPStatus != 200 || result.CollectedAt.IsZero() || len(result.Offers) != 40 || calls.Load() != 2 {
		t.Fatalf("result=%+v calls=%d", result.Error, calls.Load())
	}
	if !reflect.DeepEqual(before, *original) {
		t.Fatal("caller client changed")
	}
}

func TestClientFailures(t *testing.T) {
	info, _, req := fixture(t)
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
		retry      bool
	}{
		{"forbidden", 403, "secret URL cookies", "http_error", false},
		{"limited", 429, "secret", "http_error", true},
		{"server", 500, "secret", "http_error", true},
		{"HTML", 200, "<html>private</html>", "parse_error", false},
		{"semantic", 200, `{"errors":[{"error_code":450,"message":"private"}]}`, "source_error", false},
		{"body limit", 200, strings.Repeat(" ", maxBodyBytes+1), "response_too_large", false},
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
			client, _ := NewClient(server.URL, server.Client(), publicRates)
			result := client.Collect(context.Background(), req)
			if result.Status != source.Failed || result.Error == nil || result.Error.Code != tc.code || result.Error.Retryable != tc.retry || result.HTTPStatus != tc.status || calls.Load() != 1 {
				t.Fatalf("result=%+v calls=%d", result.Error, calls.Load())
			}
			if strings.Contains(result.Error.Message, "private") || strings.Contains(result.Error.Message, "secret") {
				t.Fatal("unsafe diagnostic")
			}
			if tc.status == 429 && result.Error.RetryAfter != 2*time.Second {
				t.Fatal(result.Error)
			}
		})
	}
	t.Run("invalid request sends nothing", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write(info) }))
		defer server.Close()
		client, _ := NewClient(server.URL, server.Client(), publicRates)
		req.Adults = 0
		result := client.Collect(context.Background(), req)
		if result.Error == nil || result.Error.Code != "invalid_request" || calls.Load() != 0 {
			t.Fatal(result.Error, calls.Load())
		}
	})
}

func TestClientCancellationAndTimeout(t *testing.T) {
	_, _, req := fixture(t)
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			entered := make(chan struct{})
			stopped := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(stopped) }))
			defer server.Close()
			original := server.Client()
			if timeout {
				original.Timeout = 100 * time.Millisecond
			}
			client, _ := NewClient(server.URL, original, publicRates)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan source.SearchResult, 1)
			go func() { done <- client.Collect(ctx, req) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("not started")
			}
			code := "timeout"
			if !timeout {
				cancel()
				code = "canceled"
			}
			select {
			case result := <-done:
				if result.Status != source.Failed || result.Error == nil || result.Error.Code != code || result.CollectedAt.IsZero() {
					t.Fatal(result.Error)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("collector stuck")
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP request leaked")
			}
		})
	}
}

func TestActualHTTPStartsLimitedAcrossClients(t *testing.T) {
	info, body, req := fixture(t)
	startsCh := make(chan time.Time, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == infoPath {
			w.Write(info)
		} else {
			w.Write(body)
		}
	}))
	defer server.Close()
	interval := 30 * time.Millisecond
	ConfigureStartIntervals(0, map[string]time.Duration{server.URL: interval})
	defer ConfigureStartIntervals(0, nil)
	original := server.Client()
	base := original.Transport
	original.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		startsCh <- time.Now()
		return base.RoundTrip(r)
	})
	a, _ := NewClient(server.URL, original, publicRates)
	b, _ := NewClient(server.URL, original, publicRates)
	done := make(chan source.SearchResult, 2)
	go func() { done <- a.Collect(context.Background(), req) }()
	go func() { done <- b.Collect(context.Background(), req) }()
	for i := 0; i < 2; i++ {
		select {
		case r := <-done:
			if r.Error != nil {
				t.Fatal(r.Error)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("limiter stuck")
		}
	}
	first := <-startsCh
	for i := 1; i < 4; i++ {
		next := <-startsCh
		if next.Sub(first) < interval-5*time.Millisecond {
			t.Fatalf("HTTP starts too close: %v", next.Sub(first))
		}
		first = next
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := a.Collect(ctx, req)
	if result.Error == nil || result.Error.Code != "canceled" {
		t.Fatal(result.Error)
	}
	select {
	case <-startsCh:
		t.Fatal("canceled request started")
	default:
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
