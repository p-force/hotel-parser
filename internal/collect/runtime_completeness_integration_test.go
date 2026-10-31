//go:build integration

package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

// Регрессия #41: три ожидаемые категории, две карточки с точными nodates.
func TestPostgresRuntimeMissingExpectedCategory(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	hotel, request := unavailableHotel(t, "arena33")
	contents := unavailableContents(hotel, exactUnavailable)
	delete(contents, hotel.Inventory.Categories[0].SourceRoomID)
	body := confirmedUnavailableHTML(hotel.Inventory.Categories, contents)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer server.Close()
	client, err := bnovo.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	hotel.Source.BaseURL = server.URL
	hotel.Collection.Adults = config.AdultCounts{1}
	hotel.Collection.Nights = []int{1}
	run, err := store.CreateRun(context.Background(), storage.RunInput{PropertyID: hotel.ID, PeriodStart: request.CheckIn, PeriodEnd: request.CheckOut})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(context.Background(), run.ID, storage.Scenario{PropertyID: hotel.ID, Source: "bnovo", SourceOrigin: server.URL, SourcePropertyID: request.PropertyID, CheckIn: request.CheckIn, CheckOut: request.CheckOut, LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunQueued(context.Background(), store, []PropertyCollection{{Hotel: hotel, Client: client}}, runtimeOptions(run.ID, 1))
	if err != nil || !result.ProgressKnown || result.Progress.Completed != 1 || result.Progress.Remaining != 0 {
		t.Fatalf("runtime: %+v %v", result, err)
	}
	saved, err := store.GetJob(context.Background(), job.ID)
	if err != nil || saved.Status != storage.JobPartial || saved.Attempts != 1 {
		t.Fatalf("incomplete job finalized incorrectly: %+v %v", saved, err)
	}
	attempt, err := store.GetAttempt(context.Background(), job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Lease == nil || attempt.Outcome != storage.OutcomePartial || len(attempt.Observations) != 2 || len(attempt.Warnings) != 1 || attempt.Warnings[0].Code != "expected_category_missing" || attempt.Warnings[0].Context != hotel.Inventory.Categories[0].SourceRoomID {
		t.Fatalf("missing category not diagnosed: %+v", attempt)
	}
	expected := map[string]bool{hotel.Inventory.Categories[1].SourceRoomID: true, hotel.Inventory.Categories[2].SourceRoomID: true}
	for _, observation := range attempt.Observations {
		if !expected[observation.CategoryID] || observation.StockStatus != storage.StockUnknown || observation.Available != nil || !observation.ConfirmedUnavailable || observation.HasOffers {
			t.Errorf("incomplete response acquired false zero/category: %+v", observation)
		}
		if len(observation.Warnings) != 1 || observation.Warnings[0].Code != "expected_category_missing" || observation.Warnings[0].Context != hotel.Inventory.Categories[0].SourceRoomID {
			t.Errorf("completeness warning lost: %+v", observation)
		}
		delete(expected, observation.CategoryID)
	}
	if len(expected) != 0 {
		t.Fatalf("returned categories lost: %v", expected)
	}
}
