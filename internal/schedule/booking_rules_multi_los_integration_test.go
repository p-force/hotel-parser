//go:build integration

package schedule

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

type sovietskyLOSCase struct {
	Name      string `json:"name"`
	CheckIn   string `json:"check_in"`
	CheckOut  string `json:"check_out"`
	LOS       int    `json:"los"`
	HasOffers bool   `json:"has_offers"`
	Confirmed bool   `json:"confirmed_forbidden"`
}

func TestPostgresBookingRulesMultiLOS(t *testing.T) {
	for _, mode := range []string{"calendar_once", "resume_saved", "endpoint_error"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOTEL_PARSER_BOOKING_RULES_ENABLED", "true")
			_, db, schema := storagetest.Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			body, err := os.ReadFile("../source/travelline/testdata/sovietsky-multi-los-cases.json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Property string             `json:"property_id"`
				Category string             `json:"room_type_id"`
				Rates    []string           `json:"rate_ids"`
				Cases    []sovietskyLOSCase `json:"cases"`
			}
			if err := json.Unmarshal(body, &fixture); err != nil {
				t.Fatal(err)
			}
			// Синтетический LOS1-контроль: не результат ручного запроса отеля.
			fixture.Cases = append(fixture.Cases, sovietskyLOSCase{Name: "LOS1_control", CheckIn: "2026-12-05", CheckOut: "2026-12-06", LOS: 1, Confirmed: true})
			rulesBody, err := os.ReadFile("../source/travelline/testdata/sovietsky-booking-rules-2026-12.json")
			if err != nil {
				t.Fatal(err)
			}
			var infoCalls, availabilityCalls, rulesCalls atomic.Int32
			var mu sync.Mutex
			starts := map[string]int{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				q := r.URL.Query()
				switch r.URL.Path {
				case "/ApiWebDistribution/BookingForm/hotel_info":
					infoCalls.Add(1)
					if q.Get("hotels[0].code") != fixture.Property {
						t.Error("wrong native hotel ID")
					}
					fmt.Fprint(w, `{"hotels":[{"code":"333","currency":"RUB","room_types":[{"code":"29344","name":"Апартаменты Яръ","kind":"room"}],"rate_plans":[{"code":"38564","name":"Цена дня"},{"code":"49218","name":"Тариф без завтрака"}]}]}`)
				case "/ApiWebDistribution/BookingForm/hotel_availability":
					availabilityCalls.Add(1)
					dates := strings.Split(q.Get("criterions[0].dates"), ";")
					if len(dates) != 2 || q.Get("criterions[0].adults") != "1" || q.Get("criterions[0].hotels[0].code") != fixture.Property {
						t.Error("wrong main search request")
						w.WriteHeader(400)
						return
					}
					key := dates[0] + "/" + dates[1]
					mu.Lock()
					starts[key]++
					mu.Unlock()
					if key != "2026-12-03/2026-12-05" {
						fmt.Fprint(w, `{"room_stays":[],"room_type_quotas":[]}`)
						return
					}
					// Цена 100 — только синтетическое значение для валидного mock offer.
					// Никакого доказанного физического/публичного количества не задано.
					var stays []string
					for _, rate := range fixture.Rates {
						stays = append(stays, fmt.Sprintf(`{"hotel_ref":{"code":"333"},"guests":[{"count":1,"placement":{"kind":"adult"}}],"room_types":[{"code":"29344","room_type_quota_rph":"q","placements":[{"code":"adult"}]}],"rate_plans":[{"code":%q,"promo":false}],"total":{"price_after_tax":100,"currency":"RUB"},"stay_dates":{"start_date":"2026-12-03 00:00:00","end_date":"2026-12-05 00:00:00"}}`, rate))
					}
					fmt.Fprintf(w, `{"room_stays":[%s],"room_type_quotas":[]}`, strings.Join(stays, ","))
				case "/ApiWebDistribution/AvailabilityCalendar/rate_plan_booking_rules":
					rulesCalls.Add(1)
					if q.Get("hotel") != fixture.Property || q.Get("start_date") != "2026-12-03" || q.Get("end_date") != "2026-12-06" || q.Get("rate_plans[0].code") != "38564" || q.Get("rate_plans[1].code") != "49218" {
						t.Error("calendar request lost full run range or tariff policy")
					}
					if mode == "endpoint_error" {
						w.WriteHeader(500)
						return
					}
					w.Write(rulesBody)
				default:
					t.Error("unexpected mock endpoint", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			regressionTransport(t, server)
			if err := collect.ConfigureLimits(collect.Limits{Sources: map[string]collect.SourceLimit{server.URL: {Concurrency: 3}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := collect.ConfigureLimits(collect.Limits{}); err != nil {
					t.Error(err)
				}
			})
			hotel, err := config.Load("../../configs/sovietsky.json")
			if err != nil {
				t.Fatal(err)
			}
			hotel.Source.BaseURL = server.URL
			hotel.Collection.Enabled, hotel.Collection.Nights = true, []int{1, 2, 3}
			for _, category := range hotel.Inventory.Categories {
				if category.SourceRoomID == fixture.Category {
					hotel.Inventory.Categories = []config.RoomCategory{category}
					break
				}
			}
			registry := Registry{Groups: []Group{{ID: "sovietsky_test", Schema: schema, Properties: []Property{{Hotel: hotel}}}}}
			options := collect.RuntimeOptions{MaxJobs: 4, Batch: collect.BatchOptions{Workers: 3}}
			if mode == "resume_saved" {
				options.MaxJobs = 1
			}
			s := testService(t, registry, nil, options)
			in, _ := time.Parse("2006-01-02", "2026-12-03")
			var scenarios []storage.Scenario
			for _, tc := range fixture.Cases {
				from, err := time.Parse("2006-01-02", tc.CheckIn)
				if err != nil || from.AddDate(0, 0, tc.LOS).Format("2006-01-02") != tc.CheckOut {
					t.Fatal("invalid scenario fixture", err)
				}
				scenarios = append(scenarios, storage.Scenario{PropertyID: hotel.ID, Source: "travelline", SourceOrigin: server.URL, SourcePropertyID: fixture.Property, CheckIn: from, CheckOut: from.AddDate(0, 0, tc.LOS), LOS: tc.LOS, Adults: 1, Rooms: 1, Language: "ru"})
			}
			run, err := s.groups[0].Store.PlanRun(ctx, storage.RunInput{ID: "00000000-0000-4000-8000-000000000333", PropertyID: hotel.ID, PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 3), StartedAt: regressionSlot()}, scenarios, hotel.Inventory)
			if err != nil {
				t.Fatal(err)
			}
			invocations := 1
			if mode == "resume_saved" {
				var response travelline.BookingRulesResponse
				if err := json.Unmarshal(rulesBody, &response); err != nil {
					t.Fatal(err)
				}
				rules, err := collect.BookingRulesToStorage(&response)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.groups[0].Store.SaveRunBookingRules(ctx, storage.RunBookingRulesInput{RunID: run.ID, RequestedAt: in, ReceivedAt: in, StartDate: run.PeriodStart, EndDate: run.PeriodEnd, RequestedRateIDs: fixture.Rates, Rules: rules})
				if err != nil {
					t.Fatal(err)
				}
				invocations = 4
			}
			for i := 0; i < invocations; i++ {
				if mode == "resume_saved" {
					s.Close()
					s = testService(t, registry, nil, options)
				}
				if _, err := s.Execute(ctx, Request{Action: "resume"}); err != nil {
					t.Fatal(err)
				}
			}
			wantRequests := int32(1)
			if mode == "resume_saved" {
				wantRequests = 0
			}
			if rulesCalls.Load() != wantRequests || infoCalls.Load() != 4 || availabilityCalls.Load() != 4 {
				t.Error("unexpected HTTP count", rulesCalls.Load(), infoCalls.Load(), availabilityCalls.Load())
			}
			mu.Lock()
			for _, tc := range fixture.Cases {
				if starts[tc.CheckIn+"/"+tc.CheckOut] != 1 {
					t.Error("scenario lost, combined across LOS, or retried", tc.Name, starts)
				}
			}
			mu.Unlock()
			rows, err := db.QueryContext(ctx, `SELECT id::text FROM "`+schema+`".scenario_jobs WHERE run_id=$1`, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if len(ids) != 4 {
				t.Fatal("scenario count changed", len(ids))
			}
			for _, id := range ids {
				job, err := s.groups[0].Store.GetJob(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				attempt, err := s.groups[0].Store.GetAttempt(ctx, id, 1)
				if err != nil || job.Attempts != 1 || attempt.Error != nil || attempt.HTTPStatus != 200 || attempt.Outcome != storage.OutcomePartial || attempt.NextRetryAt != nil || len(attempt.Observations) != 1 {
					t.Fatal("main attempt changed/retried", attempt, job, err)
				}
				for _, tc := range fixture.Cases {
					if tc.CheckIn != job.Scenario.CheckIn.Format("2006-01-02") || tc.LOS != job.Scenario.LOS {
						continue
					}
					observation := attempt.Observations[0]
					want := tc.Confirmed && mode != "endpoint_error"
					reason := ""
					if observation.UnavailabilityReason != nil {
						reason = string(*observation.UnavailabilityReason)
					}
					wantReason := ""
					if want {
						wantReason = "booking_forbidden"
					}
					if observation.ConfirmedUnavailable != want || reason != wantReason {
						t.Errorf("%s persisted confirmed=%v reason=%q, want %v/%q", tc.Name, observation.ConfirmedUnavailable, reason, want, wantReason)
					}
					if observation.CategoryID != fixture.Category || observation.HasOffers != tc.HasOffers || observation.StockStatus != storage.StockUnknown || observation.Available != nil {
						t.Error("stock/offers/category changed", observation)
					}
					if tc.HasOffers && len(observation.Offers) != 2 {
						t.Error("public offers lost", observation.Offers)
					}
				}
			}
			var output bytes.Buffer
			if _, err := s.groups[0].Store.WriteRunCSV(ctx, "sovietsky_test", run.ID, &output); err != nil {
				t.Fatal(err)
			}
			csvRows, err := csv.NewReader(&output).ReadAll()
			if err != nil || len(csvRows) != 6 || len(csvRows[0]) != 36 || csvRows[0][35] != "unavailability_reason" {
				t.Fatal("CSV contract changed", csvRows, err)
			}
			columns := map[string]int{}
			for i, col := range csvRows[0] {
				columns[col] = i
			}
			for _, row := range csvRows[1:] {
				for _, tc := range fixture.Cases {
					if row[columns["check_in"]] != tc.CheckIn || row[columns["los"]] != fmt.Sprint(tc.LOS) {
						continue
					}
					want := ""
					if tc.Confirmed && mode != "endpoint_error" {
						want = "booking_forbidden"
					}
					if row[35] != want || row[columns["available"]] != "" || row[columns["stock_status"]] != "unknown" {
						t.Errorf("%s CSV reason=%q, want %q; stock must remain unknown/NULL", tc.Name, row[35], want)
					}
				}
			}
		})
	}
}
