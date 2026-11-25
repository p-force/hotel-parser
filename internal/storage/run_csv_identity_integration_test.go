//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresCSVCombinedRunScenarioIdentity(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	ctx := context.Background()
	in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
	scenarios := map[string]int{}
	for _, hour := range []int{8, 12} {
		run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: "hotel", PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 1), StartedAt: in.Add(time.Duration(hour) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		for _, adults := range []int{1, 2} {
			sc := scenario("hotel")
			sc.CheckIn = in
			sc.CheckOut = in.AddDate(0, 0, 1)
			sc.Adults = adults
			sc.Source = "travelline"
			job, err := store.CreateJob(ctx, run.ID, sc)
			if err != nil {
				t.Fatal(err)
			}
			input := csvAttempt(job.ID, 1, storage.OutcomeSuccess, []storage.Observation{{CategoryID: "c", CategoryName: "Room", StockStatus: storage.StockKnown, Available: ptr(9), HasOffers: true, Variants: []storage.Variant{{ID: "v", Adults: adults}}, Offers: []storage.Offer{
				{VariantID: "v", RateID: "r1", Price: ptr("100.50"), Currency: ptr("RUB"), ReportedQuantity: ptr(9), Visible: true},
				{VariantID: "v", RateID: "r2", Price: ptr("200.50"), Currency: ptr("RUB"), ReportedQuantity: ptr(9), Visible: true},
			}}})
			if _, err := store.SaveAttempt(ctx, input); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.FinishRun(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if count, err := store.WriteRunCSV(ctx, "group", run.ID, &b); err != nil || count != 4 {
			t.Fatal(count, err)
		}
		for _, row := range readCSVRows(t, b.Bytes()) {
			var identity []string
			for _, field := range []string{"group_id", "run_id", "property_id", "source", "check_in", "check_out", "los", "adults", "rooms"} {
				if row[field] == "" {
					t.Fatal(field, row)
				}
				identity = append(identity, row[field])
			}
			scenarios[strings.Join(identity, "|")]++
		}
	}
	if len(scenarios) != 4 {
		t.Fatal("runs/scenarios merged", scenarios)
	}
	for key, count := range scenarios {
		if count != 2 {
			t.Fatal("offer lost/duplicated", key, count)
		}
	}
}
