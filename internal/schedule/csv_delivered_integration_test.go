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
	"github.com/p-force/hotel-parser/internal/report"
)

func TestPostgresDeliveredArchiveSkippedAndExplicitlyRecoverable(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	registry := testGroups(t, server.URL)
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 1})
	first, err := s.Execute(context.Background(), Request{Action: "start", Slot: slot(20)})
	if err != nil || len(first.Exports) != 1 || !first.Exports[0].Ready {
		t.Fatal(first, err)
	}
	artifact := first.Exports[0]
	store := s.groups[0].Store
	delivery, err := store.ClaimDelivery(context.Background(), s.groups[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteDelivery(context.Background(), delivery, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifact.Path); err != nil {
		t.Fatal(err)
	}
	// A new process/disk must not regenerate the delivered historical archive.
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	restarted := testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 1})
	for range 2 {
		response, err := restarted.Execute(context.Background(), Request{Action: "resume"})
		if err != nil || len(response.Exports) != 0 || calls.Load() != 1 {
			t.Fatal(response, err, calls.Load())
		}
	}
	recovered, err := report.Generate(context.Background(), store, s.groups[0].ID, t.TempDir(), artifact.RunID)
	if err != nil || !recovered.Ready || !recovered.Generated || calls.Load() != 1 {
		t.Fatal(recovered, err)
	}
}
