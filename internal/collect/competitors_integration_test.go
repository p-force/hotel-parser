//go:build integration

package collect_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresTravelLineCompetitors(t *testing.T) {
	ctx := context.Background()
	store, db, schema := storagetest.Open(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		id := r.URL.Query().Get("hotels[0].code")
		suffix := "-info.json"
		if id == "" {
			id = r.URL.Query().Get("criterions[0].hotels[0].code")
			suffix = "-availability-2026-11-17-a1-los1.json"
		}
		if r.URL.Query().Get("criterions[0].adults") != "" && r.URL.Query().Get("criterions[0].adults") != "1" {
			http.Error(w, "wrong guests", 400)
			return
		}
		body, err := os.ReadFile("../source/travelline/testdata/competitors/" + id + suffix)
		if err != nil {
			http.Error(w, "fixture unavailable", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer server.Close()
	properties := []collect.PropertyCollection{}
	runs := []storage.Run{}
	jobs := []storage.Job{}
	expected := []int{42, 32, 40, 12, 2}
	known := []int{8, 8, 8, 6, 1}
	categories := []int{8, 8, 10, 7, 6}
	in := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for _, key := range []string{"monarch", "bega", "aerostar", "sovietsky", "petroff"} {
		cfg, err := config.Load("../../configs/" + key + ".json")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Source.BaseURL = server.URL
		rates := []string{}
		for _, r := range cfg.Rates {
			if r.Enabled {
				rates = append(rates, r.ID)
			}
		}
		client, err := travelline.NewClient(server.URL, server.Client(), rates)
		if err != nil {
			t.Fatal(err)
		}
		properties = append(properties, collect.PropertyCollection{Hotel: cfg, Client: client})
		run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: cfg.ID, PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
		job, err := store.CreateJob(ctx, run.ID, storage.Scenario{PropertyID: cfg.ID, Source: "travelline", SourceOrigin: server.URL, SourcePropertyID: cfg.Source.PropertyID, CheckIn: in, CheckOut: in.AddDate(0, 0, 1), LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	options := collect.RuntimeOptions{MaxJobs: 5, Claim: storage.ClaimOptions{Owner: "competitor-test"}, Batch: collect.BatchOptions{Workers: 2}}
	if _, err := collect.RunQueued(ctx, store, properties, options); err != nil {
		t.Fatal(err)
	}
	for i, job := range jobs {
		attempt, err := store.GetAttempt(ctx, job.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempt.Observations) != categories[i] || attempt.HTTPStatus != 200 {
			t.Fatal("incomplete persisted result", i, attempt)
		}
		offers, stock := 0, 0
		for _, o := range attempt.Observations {
			offers += len(o.Offers)
			if o.StockStatus == storage.StockKnown {
				stock++
				if o.Available == nil || *o.Available <= 0 {
					t.Fatal(o)
				}
			} else if o.StockStatus != storage.StockUnknown || o.Available != nil {
				t.Fatal("unknown became zero", o)
			}
			for _, offer := range o.Offers {
				if offer.Price == nil || offer.Currency == nil || *offer.Currency != "RUB" || offer.ReportedQuantity == nil {
					t.Fatal(offer)
				}
			}
			if properties[i].Hotel.ID == "petroff" && (o.CategoryID == "34190" || o.CategoryID == "34191") && (o.AreaM2 == nil || *o.AreaM2 != 120) {
				t.Fatal("apartment metadata lost", o)
			}
		}
		if offers != expected[i] || stock != known[i] {
			t.Fatal(i, offers, stock)
		}
		if _, err := store.FinishRun(ctx, runs[i].ID); err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		n, err := store.WriteRunCSV(ctx, "arena33", runs[i].ID, &body)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(&body).ReadAll()
		if err != nil || int(n) != expected[i]+categories[i]-known[i] || len(rows) != int(n)+1 {
			t.Fatal(i, n, err)
		}
		index := map[string]int{}
		for k, name := range rows[0] {
			index[name] = k
		}
		byKey := map[string]storage.Offer{}
		for _, o := range attempt.Observations {
			for _, r := range o.Offers {
				byKey[o.CategoryID+"/"+r.VariantID+"/"+r.RateID] = r
			}
		}
		for _, row := range rows[1:] {
			get := func(k string) string { return row[index[k]] }
			if get("check_in") != "2026-11-17" || get("check_out") != "2026-11-18" || get("adults") != "1" || get("los") != "1" || get("source") != "travelline" || get("property_id") != properties[i].Hotel.ID {
				t.Fatal("scenario mixed", row)
			}
			if get("rate_id") == "" {
				if get("available") != "" {
					t.Fatal("offerless became zero", row)
				}
				continue
			}
			offer, ok := byKey[get("category_id")+"/"+get("variant_id")+"/"+get("rate_id")]
			if !ok {
				t.Fatal("CSV unknown offer", row)
			}
			a, aok := new(big.Rat).SetString(get("price"))
			b, bok := new(big.Rat).SetString(*offer.Price)
			if !aok || !bok || a.Cmp(b) != 0 || get("reported_quantity") != fmt.Sprint(*offer.ReportedQuantity) {
				t.Fatal("CSV data changed", row)
			}
		}
	}
	if _, err := collect.RunQueued(ctx, store, properties, options); err != nil || calls.Load() != 10 {
		t.Fatal("duplicate HTTP", calls.Load(), err)
	}
	for table, want := range map[string]int{"scenario_jobs": 5, "collection_attempts": 5, "observations": 39, "observation_offers": 128} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".` + table).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, want, err)
		}
	}
}
