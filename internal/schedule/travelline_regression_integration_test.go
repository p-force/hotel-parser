//go:build integration

package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

// Регулярный planner использует одного взрослого. Это синтетическая адаптация
// fixture; реальная семантика и цены другого состава гостей здесь не утверждаются.
func travelLineRegressionFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	info, err := os.ReadFile("../source/travelline/testdata/aerostar-info.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../source/travelline/testdata/aerostar-2026-11-17-adults-2.json")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatal(err)
	}
	for _, raw := range data["room_stays"].([]any) {
		raw.(map[string]any)["guests"] = []any{map[string]any{"count": 1, "placement": map[string]any{"kind": "adult"}}}
	}
	body, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return info, body
}

// Service.Open использует штатную фабрику, поэтому подменяем только transport,
// сохраняя реальные кеширование и dispatch. Тесты пакета не используют t.Parallel.
func regressionTransport(t *testing.T, server *httptest.Server) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
}
func regressionSlot() time.Time {
	return time.Date(2026, 11, 17, 20, 0, 0, 0, time.FixedZone("MSK", 3*3600))
}

func TestPostgresTravelLineClientPolicyIsolation(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			name := "forward"
			if reverse {
				name = "reverse"
			}
			if disabled {
				name += "_disabled"
			}
			t.Run(name, func(t *testing.T) {
				_, _, sa := storagetest.Open(t)
				_, _, sb := storagetest.Open(t)
				info, body := travelLineRegressionFixture(t)
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/ApiWebDistribution/BookingForm/hotel_info" {
						w.Write(info)
					} else {
						w.Write(body)
					}
				}))
				defer server.Close()
				regressionTransport(t, server)
				a, err := config.Load("../../configs/aerostar.json")
				if err != nil {
					t.Fatal(err)
				}
				b := a
				a.Source.BaseURL = server.URL
				b.Source.BaseURL = server.URL
				a.Collection.Enabled = !disabled
				b.Collection.Enabled = true
				a.Rates = []config.RateConfig{{ID: "128299", Name: "Public A", Enabled: true}}
				b.Rates = []config.RateConfig{{ID: "128301", Name: "Public B", Enabled: true}}
				r := Registry{Groups: []Group{{ID: "first", Schema: sa, Properties: []Property{{Hotel: a}}}, {ID: "second", Schema: sb, Properties: []Property{{Hotel: b}}}}}
				if reverse {
					r.Groups[0], r.Groups[1] = r.Groups[1], r.Groups[0]
				}
				s := testService(t, r, nil, collect.RuntimeOptions{})
				if s.groups[0].Properties[0].Client == s.groups[1].Properties[0].Client {
					t.Fatal("different allowlists share a client")
				}
				plans, err := BuildPlans(r, regressionSlot())
				wantPlans := 2
				if disabled {
					wantPlans = 1
				}
				if err != nil || len(plans) != wantPlans {
					t.Fatal(plans, err)
				}
				when := regressionSlot()
				req := source.SearchRequest{PropertyID: "585", CheckIn: when, CheckOut: when.AddDate(0, 0, 1), Adults: 1, Language: "ru"}
				for _, g := range s.groups {
					want := "128299"
					if g.ID == "second" {
						want = "128301"
					}
					result := g.Properties[0].Client.Collect(context.Background(), req)
					if result.Error != nil || len(result.Offers) != 8 {
						t.Fatal(result.Error, len(result.Offers))
					}
					for _, offer := range result.Offers {
						if offer.RateID != want {
							t.Fatalf("group %s rate=%s want=%s", g.ID, offer.RateID, want)
						}
					}
				}
			})
		}
	}
	t.Run("equivalent policies reuse", func(t *testing.T) {
		_, _, sa := storagetest.Open(t)
		_, _, sb := storagetest.Open(t)
		a, err := config.Load("../../configs/aerostar.json")
		if err != nil {
			t.Fatal(err)
		}
		a.Rates = []config.RateConfig{{ID: "128299", Name: "A", Enabled: true}, {ID: "128301", Name: "B", Enabled: true}}
		b := a
		b.Rates = []config.RateConfig{a.Rates[1], a.Rates[0]}
		r := Registry{Groups: []Group{{ID: "first", Schema: sa, Properties: []Property{{Hotel: a}}}, {ID: "second", Schema: sb, Properties: []Property{{Hotel: b}}}}}
		s := testService(t, r, nil, collect.RuntimeOptions{})
		if s.groups[0].Properties[0].Client != s.groups[1].Properties[0].Client {
			t.Fatal("equivalent policies did not reuse client")
		}
	})
}

func TestPostgresMixedSourcesInvalidCurrency(t *testing.T) {
	_, admin, schema := storagetest.Open(t)
	a, err := config.Load("../../configs/arena33.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.Load("../../configs/aerostar.json")
	if err != nil {
		t.Fatal(err)
	}
	a.Collection.Enabled = true
	b.Collection.Enabled = true
	a.Inventory.Categories = a.Inventory.Categories[:1]
	a.Inventory.Categories[0].SourceRoomID = "1"
	a.Inventory.TotalRooms = a.Inventory.Categories[0].TotalRooms
	info, body := travelLineRegressionFixture(t)
	info = []byte(strings.ReplaceAll(string(info), "RUB", "rub"))
	body = []byte(strings.ReplaceAll(string(body), "RUB", "rub"))
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/ApiWebDistribution/BookingForm/hotel_info":
			w.Write(info)
		case "/ApiWebDistribution/BookingForm/hotel_availability":
			w.Write(body)
		default:
			fmt.Fprint(w, successHTML)
		}
	}))
	defer server.Close()
	regressionTransport(t, server)
	a.Source.BaseURL = server.URL
	b.Source.BaseURL = server.URL
	r := Registry{Groups: []Group{{ID: "mixed", Schema: schema, Properties: []Property{{Hotel: a}, {Hotel: b}}}}}
	s := testService(t, r, nil, collect.RuntimeOptions{MaxJobs: 2, Batch: collect.BatchOptions{Workers: 2}})
	for _, req := range []Request{{Action: "plan", Slot: regressionSlot()}, {Action: "resume"}, {Action: "plan", Slot: regressionSlot()}, {Action: "resume"}} {
		if _, err := s.Execute(context.Background(), req); err != nil {
			t.Fatal("source response stopped runtime", err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected HTTP retry/replay", calls.Load())
	}
	for _, property := range []struct {
		id                   string
		status               storage.JobStatus
		outcome              storage.Outcome
		observations, offers int
	}{
		{a.ID, storage.JobSucceeded, storage.OutcomeSuccess, 1, 1},
		{b.ID, storage.JobPartial, storage.OutcomePartial, 10, 0},
	} {
		var jobID string
		if err := admin.QueryRow(`SELECT id::text FROM "`+schema+`".scenario_jobs WHERE property_id=$1`, property.id).Scan(&jobID); err != nil {
			t.Fatal(err)
		}
		job, err := s.groups[0].Store.GetJob(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := s.groups[0].Store.GetAttempt(context.Background(), jobID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != property.status || job.Attempts != 1 || attempt.Outcome != property.outcome || attempt.Error != nil || attempt.HTTPStatus != 200 || len(attempt.Observations) != property.observations {
			t.Fatal(job, attempt)
		}
		offers := 0
		for _, o := range attempt.Observations {
			offers += len(o.Offers)
			if property.id == b.ID && (o.StockStatus != storage.StockUnknown || o.Available != nil) {
				t.Fatal("bad response became known", o)
			}
		}
		if offers != property.offers {
			t.Fatal("wrong offer count", offers)
		}
		if property.id == b.ID {
			found := false
			for _, w := range attempt.Warnings {
				if w.Code == "offer_price_invalid" {
					found = true
				}
			}
			if !found {
				t.Fatal("currency diagnostic lost in PostgreSQL")
			}
		}
	}
	for table, want := range map[string]int{"scenario_jobs": 2, "collection_attempts": 2, "observations": 11, "observation_offers": 1} {
		var count int
		if err := admin.QueryRow(`SELECT count(*) FROM "` + schema + `"."` + table + `"`).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, err)
		}
	}
}
