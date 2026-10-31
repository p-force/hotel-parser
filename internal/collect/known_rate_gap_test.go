package collect

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

type startTransportFunc func(*http.Request) (*http.Response, error)

func (f startTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Регрессия #36: долгий observer.before не должен сближать реальные HTTP-старты.
// Измерение на входе базового transport исключает разницу сетевых RTT до сервера.
func TestKnownDefectAdmissionDoesNotLimitActualHTTPStarts(t *testing.T) {
	const interval = 500 * time.Millisecond
	testLimits(t, Limits{})
	starts := make(chan time.Time, 2)
	client, err := bnovo.NewClient("https://reservationsteps.ru", &http.Client{Transport: startTransportFunc(func(r *http.Request) (*http.Response, error) {
		starts <- time.Now()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(propertySuccessHTML)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	observer := &attemptObserver{before: func(ctx context.Context, index, number int, started time.Time) error {
		if index == 0 {
			select {
			case <-gate:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		close(gate)
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input := []batchJob{{client: client, request: batchRequests(2)[0], propertyID: "a", source: "bnovo"}, {client: client, request: batchRequests(2)[1], propertyID: "b", source: "bnovo"}}
	if _, err := collectJobsObserved(ctx, input, BatchOptions{Workers: 2}, observer); err != nil {
		t.Fatal(err)
	}
	a, b := <-starts, <-starts
	gap := b.Sub(a)
	if gap < 0 {
		gap = -gap
	}
	if gap < interval {
		t.Fatalf("actual HTTP starts coalesced after pre-HTTP work: gap=%v, required >=%v", gap, interval)
	}
	t.Logf("actual RoundTrip start timestamps: %s, %s; gap=%v", a.UTC().Format(time.RFC3339Nano), b.UTC().Format(time.RFC3339Nano), gap)
}

func TestHTTPStartGateWithRetryBackoffAndRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backoff time.Duration
		after   string
		minimum time.Duration
	}{
		{"source_interval", time.Millisecond, "", 500 * time.Millisecond},
		{"backoff", 1200 * time.Millisecond, "", 600 * time.Millisecond},
		{"retry_after", time.Millisecond, "1", time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := "https://" + strings.ReplaceAll(tc.name, "_", "-") + ".retry-start.invalid"
			testLimits(t, Limits{Sources: map[string]SourceLimit{origin: {StartInterval: 500 * time.Millisecond}}})
			starts := make(chan time.Time, 2)
			client, err := bnovo.NewClient(origin, &http.Client{Transport: startTransportFunc(func(r *http.Request) (*http.Response, error) {
				starts <- time.Now()
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(propertySuccessHTML)), Request: r}
				if len(starts) == 1 {
					response.StatusCode = 429
					if tc.after != "" {
						response.Header.Set("Retry-After", tc.after)
					}
				}
				return response, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			results, summary, err := CollectBatch(context.Background(), client, batchRequests(1), BatchOptions{Workers: 2, Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: tc.backoff, MaxBackoff: tc.backoff}})
			if err != nil || len(results) != 1 || results[0].Status != bnovo.SearchStatusSuccess || summary.RetryCount != 1 {
				t.Fatalf("retry outcome: %+v %+v %v", results, summary, err)
			}
			first, second := <-starts, <-starts
			if gap := second.Sub(first); gap < tc.minimum {
				t.Errorf("retry gap=%v want >=%v", gap, tc.minimum)
			} else {
				t.Logf("RoundTrip retry starts=%s, %s gap=%v", first.UTC().Format(time.RFC3339Nano), second.UTC().Format(time.RFC3339Nano), gap)
			}
		})
	}
}
