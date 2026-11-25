//go:build integration

package schedule

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/p-force/hotel-parser/internal/collect"
)

func TestPostgresPlanInventorySnapshotAndCSVVersion(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	registry := testGroups(t, server.URL)
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, server, collect.RuntimeOptions{MaxJobs: 1})
	planned, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)})
	if err != nil || len(planned.Planned) != 1 {
		t.Fatal(planned, err)
	}
	run := planned.Planned[0]
	snapshot, err := s.groups[0].Store.GetRunInventory(context.Background(), run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, &registry.Groups[0].Properties[0].Hotel.Inventory) {
		t.Fatal(snapshot, err)
	}
	// A v1 file must not be reused with the expanded CSV contract.
	legacy := filepath.Join(s.csvDirectory, "v1", s.groups[0].Store.Schema(), registry.Groups[0].ID, run.PropertyID, run.StartedAt.UTC().Format("20060102T150405Z"), run.ID+".csv")
	if err := os.MkdirAll(filepath.Dir(legacy), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy-header\nlegacy-data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	legacyV2 := strings.Replace(legacy, string(filepath.Separator)+"v1"+string(filepath.Separator), string(filepath.Separator)+"v2"+string(filepath.Separator), 1)
	if err := os.MkdirAll(filepath.Dir(legacyV2), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyV2, []byte("legacy-v2-header\nlegacy-v2-data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(response.Exports) != 1 || !response.Exports[0].Generated || calls.Load() != 1 {
		t.Fatal(response, err)
	}
	artifact := response.Exports[0]
	if !strings.Contains(artifact.Path, string(filepath.Separator)+"v3"+string(filepath.Separator)) {
		t.Fatal(artifact)
	}
	body, err := os.ReadFile(artifact.Path)
	if err != nil || !strings.Contains(string(body), "property_physical_total_rooms") || strings.Contains(string(body), "legacy-header") {
		t.Fatal(err, string(body))
	}
	v2, err := os.ReadFile(legacyV2)
	if err != nil || string(v2) != "legacy-v2-header\nlegacy-v2-data\n" {
		t.Fatal("legacy v2 changed", err)
	}
	old, err := os.ReadFile(legacy)
	if err != nil || string(old) != "legacy-header\nlegacy-data\n" {
		t.Fatal("legacy file changed", err)
	}
}
