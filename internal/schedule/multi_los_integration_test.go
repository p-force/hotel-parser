//go:build integration

package schedule

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresMultiLOSLegacyPlanTransition(t *testing.T) {
	registry := testRegistry(t)
	_, db, schema := storagetest.Open(t)
	registry.Groups[0].Schema = schema
	registry.Groups[0].Properties[0].Hotel.Collection.Nights = []int{1, 2, 3}
	service := testService(t, registry, nil, collect.RuntimeOptions{})
	ctx := context.Background()
	at := slot(20)
	old, err := legacyPlans(registry, at)
	if err != nil || len(old[0].Scenarios) != 1 {
		t.Fatal(old, err)
	}
	store := service.groups[0].Store
	run, err := store.PlanRun(ctx, old[0].Run, old[0].Scenarios, old[0].Inventory)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, run.ID, old[0].Scenarios[0])
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	one := 1
	if _, err := store.SaveAttempt(ctx, storage.AttemptInput{JobID: job.ID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeSuccess, HTTPStatus: 200, Observations: []storage.Observation{{CategoryID: "561429", CategoryName: "Stadium", Available: &one, StockStatus: storage.StockKnown}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var before bytes.Buffer
	if _, err := store.WriteRunCSV(ctx, registry.Groups[0].ID, run.ID, &before); err != nil {
		t.Fatal(err)
	}
	fresh, _ := BuildPlans(registry, at)
	if _, err := store.PlanRun(ctx, fresh[0].Run, fresh[0].Scenarios); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("PlanRun hash validation bypassed", err)
	}
	for range 2 {
		response, err := service.Execute(ctx, Request{Action: "plan", Slot: at})
		if err != nil || response.Planned[0].ID != run.ID || !response.Planned[0].PeriodEnd.Equal(run.PeriodEnd) {
			t.Fatal(response, err)
		}
	}
	var after bytes.Buffer
	if _, err := store.WriteRunCSV(ctx, registry.Groups[0].ID, run.ID, &after); err != nil || !bytes.Equal(before.Bytes(), after.Bytes()) {
		t.Fatal("historical observation changed", err)
	}
	future := at.AddDate(0, 0, 1)
	for range 2 {
		if _, err := service.Execute(ctx, Request{Action: "plan", Slot: future}); err != nil {
			t.Fatal(err)
		}
	}
	var count, attempts int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".scenario_jobs`).Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".collection_attempts`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal(attempts, err)
	}
	// A new multi-LOS plan is equally immutable: reverting the config cannot replace it.
	registry.Groups[0].Properties[0].Hotel.Collection.Nights = []int{1}
	changed := testService(t, registry, nil, collect.RuntimeOptions{})
	if _, err := changed.Execute(ctx, Request{Action: "plan", Slot: future}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("new plan silently replaced", err)
	}
	registry.Groups[0].Properties[0].Hotel.Source.PropertyID = "changed-source-id"
	changedSource := testService(t, registry, nil, collect.RuntimeOptions{})
	if _, err := changedSource.Execute(ctx, Request{Action: "plan", Slot: at}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("source change bypassed hash", err)
	}
}

func TestPostgresMultiLOSSourcesResumeRecoveryAndDelivery(t *testing.T) {
	var calls atomic.Int32
	var block atomic.Bool
	block.Store(true)
	entered := make(chan struct{}, 1)
	info := `{"hotels":[{"code":"585","currency":"RUB","room_types":[{"code":"1","name":"Room","kind":"room","size":{"unit":"square_metre","value":20}}],"rate_plans":[{"code":"r","name":"Public"}]}]}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if block.CompareAndSwap(true, false) {
			entered <- struct{}{}
			<-r.Context().Done()
			return
		}
		if strings.HasSuffix(r.URL.Path, "/hotel_info") {
			fmt.Fprint(w, info)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/hotel_availability") {
			dates := strings.Split(r.URL.Query().Get("criterions[0].dates"), ";")
			if len(dates) != 2 || r.URL.Query().Get("criterions[0].adults") != "1" {
				t.Error("invalid TL scenario", r.URL.Query())
				w.WriteHeader(400)
				return
			}
			in, _ := time.Parse("2006-01-02", dates[0])
			out, _ := time.Parse("2006-01-02", dates[1])
			los := int(out.Sub(in) / (24 * time.Hour))
			stays := []any{}
			quotas := []any{}
			if los > 1 {
				stays = append(stays, map[string]any{"hotel_ref": map[string]any{"code": "585"}, "guests": []any{map[string]any{"count": 1, "placement": map[string]any{"kind": "adult"}}}, "room_types": []any{map[string]any{"code": "1", "room_type_quota_rph": "q", "placements": []any{map[string]any{"code": "p"}}}}, "rate_plans": []any{map[string]any{"code": "r"}}, "total": map[string]any{"price_after_tax": json.Number(fmt.Sprintf("%d.50", 100*los)), "currency": "RUB"}, "stay_dates": map[string]any{"start_date": dates[0] + " 00:00:00", "end_date": dates[1] + " 00:00:00"}})
				if los == 2 {
					quotas = append(quotas, map[string]any{"rph": "q", "quantity": 2})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"room_stays": stays, "room_type_quotas": quotas})
			return
		}
		in, _ := time.Parse("02-01-2006", r.URL.Query().Get("dfrom"))
		out, _ := time.Parse("02-01-2006", r.URL.Query().Get("dto"))
		los := int(out.Sub(in) / (24 * time.Hour))
		if los < 1 || los > 3 || r.URL.Query().Get("adults") != "1" {
			t.Error("invalid Bnovo scenario", r.URL.Query())
			w.WriteHeader(400)
			return
		}
		if los == 1 {
			fmt.Fprint(w, `<html><script>const $enabledRoomTypes = {"1":{"id":1,"name":"Room"}};const account_currency_iso4217 = "RUB";</script><div class="room" data-roomtype-id="1"><div class="tariff__nodates">Здесь нет свободных мест с 31 октября 2026 по 1 ноября 2026</div></div></html>`)
			return
		}
		body := strings.ReplaceAll(successHTML, `data-tariff-price="100"`, fmt.Sprintf(`data-tariff-price="%d"`, 100*los))
		if los == 2 {
			body = strings.ReplaceAll(body, `data-available="1"`, `data-available="invalid"`)
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	regressionTransport(t, server)
	registry := testRegistry(t)
	_, admin, schema := storagetest.Open(t)
	registry.Groups[0].Schema = schema
	a := &registry.Groups[0].Properties[0]
	a.Hotel.Source.BaseURL = server.URL
	a.Hotel.Collection.Nights = []int{1, 2, 3}
	a.Hotel.Inventory.Categories = append([]config.RoomCategory(nil), a.Hotel.Inventory.Categories[:1]...)
	a.Hotel.Inventory.Categories[0].SourceRoomID = "1"
	b := *a
	b.Hotel.ID = "tl_hotel"
	b.Hotel.Role = "competitor"
	b.Hotel.Source.Type = "travelline"
	b.Hotel.Source.PropertyID = "585"
	b.Hotel.Inventory.PublicSource = "travelline"
	b.Hotel.Rates = []config.RateConfig{{ID: "r", Name: "Public", Enabled: true}}
	registry.Groups[0].Properties = append(registry.Groups[0].Properties, b)
	mock := &deliveryMock{files: map[string][]byte{}}
	options := collect.RuntimeOptions{MaxJobs: 1, Batch: collect.BatchOptions{Workers: 1}}
	fresh := func() *Service {
		s := testService(t, registry, nil, options)
		s.uploader = deliveryUploader(t, mock)
		return s
	}
	s := fresh()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Execute(ctx, Request{Action: "resume"}); done <- err }()
	await(t, entered)
	cancel()
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.Close()
	if _, err := admin.Exec(`UPDATE "` + schema + `".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE status='running'`); err != nil {
		t.Fatal(err)
	}
	s = fresh()
	for i := 0; i < 6; i++ {
		response, err := s.Execute(context.Background(), Request{Action: "resume"})
		if err != nil {
			t.Fatal(err)
		}
		remaining := 0
		for _, group := range response.Groups {
			for _, progress := range group.Runs {
				remaining += progress.Remaining
				if progress.Remaining > 0 {
					if progress.Run.FinishedAt != nil {
						t.Fatal("premature finish", progress)
					}
					for _, artifact := range response.Exports {
						if artifact.RunID == progress.Run.ID {
							t.Fatal("premature CSV", artifact)
						}
					}
					var n int
					if err := admin.QueryRow(`SELECT count(*) FROM "`+schema+`".csv_deliveries WHERE run_id=$1`, progress.Run.ID).Scan(&n); err != nil || n != 0 {
						t.Fatal("premature delivery", n, err)
					}
				}
			}
		}
		if remaining != 5-i {
			t.Fatal("MaxJobs/resume lost scenarios", remaining, i)
		}
		if i == 1 {
			s.Close()
			s = fresh()
		}
	}
	if mock.puts != 2 || len(mock.files) != 2 {
		t.Fatal("delivery count", mock.puts, len(mock.files))
	}
	for table, want := range map[string]int{"scenario_jobs": 6, "collection_attempts": 6, "observations": 6, "observation_offers": 4} {
		var n int
		if err := admin.QueryRow(`SELECT count(*) FROM "` + schema + `".` + table).Scan(&n); err != nil || n != want {
			t.Fatal(table, n, want, err)
		}
	}
	var recovered int
	if err := admin.QueryRow(`SELECT count(*) FROM "` + schema + `".scenario_jobs WHERE lease_token>1`).Scan(&recovered); err != nil || recovered != 1 {
		t.Fatal("lease recovery missing", recovered, err)
	}
	for _, raw := range mock.files {
		records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
		if err != nil || len(records) != 4 || len(records[0]) != 35 {
			t.Fatal(records, err)
		}
		index := map[string]int{}
		for i, key := range records[0] {
			index[key] = i
		}
		for _, row := range records[1:] {
			get := func(k string) string { return row[index[k]] }
			los, _ := strconv.Atoi(get("los"))
			provider := get("source")
			in, _ := time.Parse("2006-01-02", get("check_in"))
			out, _ := time.Parse("2006-01-02", get("check_out"))
			if !out.Equal(in.AddDate(0, 0, los)) || strings.Contains(get("attempt_warning_codes"), "minimum_stay") {
				t.Fatal(row)
			}
			if los == 1 {
				if get("rate_id") != "" || get("has_offers") != "false" {
					t.Fatal(row)
				}
				if provider == "bnovo" {
					if get("job_status") != "no_offers" || get("available") != "0" || get("confirmed_unavailable") != "true" {
						t.Fatal(row)
					}
				} else {
					if get("job_status") != "partial" || get("stock_status") != "unknown" || get("available") != "" || get("confirmed_unavailable") != "false" {
						t.Fatal(row)
					}
				}
			} else {
				want := fmt.Sprint(100 * los)
				if provider == "travelline" {
					want += ".50"
				}
				if get("price") != want || get("has_offers") != "true" || get("confirmed_unavailable") != "false" {
					t.Fatal("LOS prices mixed", row, want)
				}
				success := (provider == "bnovo" && los == 3) || (provider == "travelline" && los == 2)
				if success {
					if get("job_status") != "succeeded" || get("stock_status") != "known" {
						t.Fatal(row)
					}
				} else {
					if get("job_status") != "partial" || get("stock_status") != "unknown" {
						t.Fatal(row)
					}
				}
			}
		}
	}
	before := calls.Load()
	if _, err := s.Execute(context.Background(), Request{Action: "start", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before || mock.puts != 2 {
		t.Fatal("completed scenarios repeated", calls.Load(), before, mock.puts)
	}
}

func TestPostgresMultiLOSMorningMaxJobs64(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	registry := testGroups(t, server.URL)
	registry.Groups = registry.Groups[:1]
	registry.Groups[0].Properties[0].Hotel.Collection.Nights = []int{1, 2, 3}
	mock := &deliveryMock{files: map[string][]byte{}}
	options := collect.RuntimeOptions{MaxJobs: 64, Batch: collect.BatchOptions{Workers: 4}}
	fresh := func() *Service {
		s := testService(t, registry, server, options)
		s.uploader = deliveryUploader(t, mock)
		return s
	}
	s := fresh()
	first, err := s.Execute(context.Background(), Request{Action: "start", Slot: slot(8)})
	if err != nil || calls.Load() != 64 || len(first.Exports) != 0 || mock.puts != 0 {
		t.Fatal(first, err, calls.Load(), mock.puts)
	}
	p := first.Groups[0].Runs[0]
	if p.Remaining != 13 || p.Succeeded != 64 || p.Run.FinishedAt != nil {
		t.Fatal("77-job run finished early", p)
	}
	s.Close()
	s = fresh()
	second, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || calls.Load() != 77 || len(second.Exports) != 1 || mock.puts != 1 {
		t.Fatal(second, err, calls.Load(), mock.puts)
	}
	p = second.Groups[0].Runs[0]
	if p.Remaining != 0 || p.Succeeded != 77 || p.Run.FinishedAt == nil {
		t.Fatal(p)
	}
	for _, body := range mock.files {
		records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
		if err != nil || len(records) != 78 || len(records[0]) != 35 {
			t.Fatal(len(records), err)
		}
		column := 0
		for i, name := range records[0] {
			if name == "los" {
				column = i
			}
		}
		counts := map[string]int{}
		for _, row := range records[1:] {
			counts[row[column]]++
		}
		if counts["1"] != 61 || counts["2"] != 8 || counts["3"] != 8 {
			t.Fatal(counts)
		}
	}
	if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil || calls.Load() != 77 || mock.puts != 1 {
		t.Fatal("resume duplicated jobs/delivery", err, calls.Load(), mock.puts)
	}
}
