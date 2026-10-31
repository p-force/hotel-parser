//go:build integration

package collect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresConfirmedUnavailabilityWithoutPlans(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	bodies := make(map[string][]byte)
	var properties []PropertyCollection
	expected := make(map[string]config.Config)
	for _, id := range []string{"arena33", "lindsay"} {
		hotel, request := unavailableHotel(t, id)
		bodies[hotel.Source.PropertyID] = confirmedUnavailableHTML(hotel.Inventory.Categories, unavailableContents(hotel, exactUnavailable))
		// Только тестовая копия конфигурации: один взрослый, LOS=1, обе группы.
		hotel.Collection.Adults = config.AdultCounts{1}
		hotel.Collection.Nights = []int{1}
		properties = append(properties, PropertyCollection{Hotel: hotel, CheckIn: request.CheckIn})
		expected[id] = hotel
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := strings.TrimPrefix(r.URL.Path, "/rooms/index/")
		body, ok := bodies[uid]
		if !ok || r.URL.Query().Get("dfrom") != "30-12-2026" || r.URL.Query().Get("dto") != "31-12-2026" || r.URL.Query().Get("adults") != "1" {
			t.Errorf("scenario sent incorrectly: path=%s query=%s", r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(400)
			return
		}
		w.Write(body)
	}))
	defer server.Close()
	client, err := bnovo.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for i := range properties {
		properties[i].Client = client
		properties[i].Hotel.Source.BaseURL = client.SourceOrigin()
	}
	batch, err := CollectPropertiesStored(context.Background(), store, properties, BatchOptions{Workers: 2, Retry: RetryPolicy{MaxAttempts: 1}})
	if err != nil || len(batch.Results) != 2 || len(batch.JobIDs) != 2 || batch.Summary.NoOffersCount != 2 {
		t.Fatalf("batch failed: %+v %v", batch, err)
	}
	observations := 0
	for i, id := range batch.JobIDs {
		job, err := store.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		hotel, ok := expected[job.Scenario.PropertyID]
		if !ok || job.Scenario.SourcePropertyID != hotel.Source.PropertyID || job.Scenario.Source != "bnovo" || job.Scenario.CheckIn.Format("2006-01-02") != "2026-12-30" || job.Scenario.CheckOut.Format("2006-01-02") != "2026-12-31" || job.Scenario.LOS != 1 || job.Scenario.Adults != 1 || job.Scenario.Rooms != 1 || job.Status != storage.JobNoOffers || job.Attempts != 1 {
			t.Fatalf("scenario/status lost: %+v", job)
		}
		if batch.Results[i].PropertyID != hotel.ID {
			t.Fatal("hotel association lost")
		}
		attempt, err := store.GetAttempt(context.Background(), id, 1)
		if err != nil || attempt.Outcome != storage.OutcomeNoOffers || attempt.HTTPStatus != 200 || attempt.Error != nil || len(attempt.Warnings) != 0 || attempt.CollectedAt == nil || len(attempt.Observations) != len(hotel.Inventory.Categories) {
			t.Fatalf("no_offers attempt lost: %+v %v", attempt, err)
		}
		categories := make(map[string]string)
		for _, category := range hotel.Inventory.Categories {
			categories[category.SourceRoomID] = category.Name
		}
		for _, observation := range attempt.Observations {
			name, ok := categories[observation.CategoryID]
			if !ok || observation.CategoryName != name || observation.StockStatus != storage.StockKnown || observation.Available == nil || *observation.Available != 0 || !observation.ConfirmedUnavailable || observation.HasOffers || len(observation.Warnings) != 0 || len(observation.Offers) != 0 || len(observation.Variants) != 0 || observation.AreaM2 == nil || *observation.AreaM2 != 12 {
				t.Errorf("zero/evidence/identity lost: %+v", observation)
			}
			delete(categories, observation.CategoryID)
			observations++
		}
		if len(categories) != 0 {
			t.Errorf("missing categories: %v", categories)
		}
		// Повторная запись подтверждённой попытки идемпотентна, не добавляет observation.
		againID, err := store.SaveAttempt(context.Background(), attempt.AttemptInput)
		if err != nil || againID != attempt.ID {
			t.Fatalf("duplicate history: %s %v", againID, err)
		}
		again, err := store.GetAttempt(context.Background(), id, 1)
		if err != nil || len(again.Observations) != len(attempt.Observations) {
			t.Fatalf("observations duplicated: %+v %v", again, err)
		}
	}
	if observations != 13 {
		t.Fatalf("observations=%d, want 13", observations)
	}
}

func TestPostgresAmbiguousUnavailabilityStaysUnknown(t *testing.T) {
	for _, name := range []string{"date_mismatch", "missing_category", "minimum_stay"} {
		t.Run(name, func(t *testing.T) {
			store, _, _ := storagetest.Open(t)
			hotel, request := unavailableHotel(t, "arena33")
			contents := unavailableContents(hotel, exactUnavailable)
			switch name {
			case "date_mismatch":
				for id, content := range contents {
					contents[id] = strings.ReplaceAll(content, "30 декабря", "29 декабря")
				}
			case "missing_category":
				delete(contents, hotel.Inventory.Categories[0].SourceRoomID)
			case "minimum_stay":
				for id := range contents {
					contents[id] = `<div class="tariff__nodates">Минимальный срок проживания — 2 ночи</div>`
				}
			}
			body := confirmedUnavailableHTML(hotel.Inventory.Categories, contents)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, string(body)) }))
			defer server.Close()
			client, err := bnovo.NewClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			hotel.Source.BaseURL = client.SourceOrigin()
			hotel.Collection.Adults = config.AdultCounts{1}
			hotel.Collection.Nights = []int{1}
			run, err := store.CreateRun(context.Background(), storage.RunInput{PropertyID: hotel.ID, PeriodStart: request.CheckIn, PeriodEnd: request.CheckOut})
			if err != nil {
				t.Fatal(err)
			}
			job, err := store.CreateJob(context.Background(), run.ID, storage.Scenario{PropertyID: hotel.ID, Source: "bnovo", SourceOrigin: client.SourceOrigin(), SourcePropertyID: hotel.Source.PropertyID, CheckIn: request.CheckIn, CheckOut: request.CheckOut, LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
			if err != nil {
				t.Fatal(err)
			}
			progress, err := RunQueued(context.Background(), store, []PropertyCollection{{Hotel: hotel, CheckIn: request.CheckIn, Client: client}}, runtimeOptions(run.ID, 1))
			if err != nil || !progress.ProgressKnown || progress.Progress.Completed != 1 || progress.Progress.Remaining != 0 {
				t.Fatalf("runtime: %+v %v", progress, err)
			}
			attempt, err := store.GetAttempt(context.Background(), job.ID, 1)
			count := 3
			if name == "missing_category" {
				count--
			}
			if err != nil || attempt.Outcome != storage.OutcomePartial || len(attempt.Warnings) == 0 || len(attempt.Observations) != count {
				t.Fatalf("partial attempt lost: %+v %v", attempt, err)
			}
			for _, observation := range attempt.Observations {
				if observation.StockStatus != storage.StockUnknown || observation.Available != nil || len(observation.Warnings) == 0 {
					t.Errorf("ambiguous HTML stored as zero: %+v", observation)
				}
			}
		})
	}
}
