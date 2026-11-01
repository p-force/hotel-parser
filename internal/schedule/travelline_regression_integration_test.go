//go:build integration

package schedule

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source"
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
