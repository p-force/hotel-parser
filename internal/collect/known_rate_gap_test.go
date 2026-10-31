package collect

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in failing contract reproduction. Do not interpret the default skip as a
// passing rate-limit check; the verification report records its non-zero exit.
func TestKnownDefectAdmissionDoesNotLimitActualHTTPStarts(t *testing.T) {
	if os.Getenv("HOTEL_PARSER_REPRO_KNOWN_DEFECTS") != "1" {
		t.Skip("known defect reproduction; see Issue #35 verification report")
	}
	const interval = 250 * time.Millisecond
	testLimits(t, Limits{SourceStartInterval: interval})
	starts := make(chan time.Time, 2)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { starts <- time.Now(); fmt.Fprint(w, propertySuccessHTML) }, 0)
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
}
