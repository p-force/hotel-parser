//go:build integration

package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

// Отдельный незакрытый дефект старого API; не считается прошедшей проверкой.
// Штатный RunQueued имеет ValidateExpectedCategories и проходит негативные тесты.
func TestPostgresKnownDefectStoredMissingExpectedCategory(t *testing.T) {
	if os.Getenv("HOTEL_PARSER_REPRO_KNOWN_DEFECTS") != "1" {
		t.Skip("opt-in reproduction: legacy stored batch omits configured category validation")
	}
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
	batch, err := CollectPropertiesStored(context.Background(), store, []PropertyCollection{{Hotel: hotel, Client: client, CheckIn: request.CheckIn}}, BatchOptions{Workers: 1, Retry: RetryPolicy{MaxAttempts: 1}})
	if err != nil || len(batch.JobIDs) != 1 {
		t.Fatalf("reproduction setup: %+v %v", batch, err)
	}
	attempt, err := store.GetAttempt(context.Background(), batch.JobIDs[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Outcome != storage.OutcomePartial || len(attempt.Warnings) == 0 {
		t.Fatalf("missing configured category persisted as complete: outcome=%s observations=%d warnings=%v", attempt.Outcome, len(attempt.Observations), attempt.Warnings)
	}
}
