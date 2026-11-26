//go:build integration

package schedule

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

type tlCapture struct {
	Path       string
	Status     int
	StartedAt  time.Time
	DurationMS int64
	Bytes      int
	SHA256     string
	body       []byte
}
type tlTransport struct {
	base  http.RoundTripper
	mu    sync.Mutex
	calls []tlCapture
}

func (p *tlTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	response, err := p.base.RoundTrip(r)
	capture := tlCapture{Path: r.URL.Path, StartedAt: start.UTC()}
	if err == nil {
		capture.Status = response.StatusCode
		capture.body, err = io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(capture.body))
		capture.Bytes = len(capture.body)
		hash := sha256.Sum256(capture.body)
		capture.SHA256 = hex.EncodeToString(hash[:])
	}
	capture.DurationMS = time.Since(start).Milliseconds()
	p.mu.Lock()
	p.calls = append(p.calls, capture)
	p.mu.Unlock()
	return response, err
}

func TestPostgresTravelLineQueuedPersistence(t *testing.T) { travelLineQueued(t, false) }
func TestLiveTravelLineSmoke(t *testing.T) {
	if os.Getenv("HOTEL_PARSER_LIVE_TRAVELLINE") != "1" {
		t.Skip("explicit single-scenario live smoke only")
	}
	travelLineQueued(t, true)
}
func travelLineQueued(t *testing.T, live bool) {
	t.Helper()
	hotel, err := config.Load("../../configs/aerostar.json")
	if err != nil {
		t.Fatal(err)
	}
	hotel.Collection.Enabled = true
	hotel.Collection.Nights = []int{1} // The bounded smoke/fixture covers one night.
	_, admin, schema := storagetest.Open(t)
	var mock *httptest.Server
	var bookingCalls atomic.Int32
	if !live {
		info, err := os.ReadFile("../source/travelline/testdata/aerostar-info.json")
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile("../source/travelline/testdata/aerostar-2026-11-17-adults-2.json")
		if err != nil {
			t.Fatal(err)
		}
		// Fixture transport отвечает для реального сценария planner (один взрослый).
		var data map[string]any
		json.Unmarshal(body, &data)
		for _, raw := range data["room_stays"].([]any) {
			stay := raw.(map[string]any)
			stay["guests"] = []any{map[string]any{"count": 1, "placement": map[string]any{"kind": "adult"}}}
		}
		body, _ = json.Marshal(data)
		mock = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if respondEmptyBookingRules(w, r, &bookingCalls) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/ApiWebDistribution/BookingForm/hotel_info" {
				w.Write(info)
				return
			}
			if r.URL.Path != "/ApiWebDistribution/BookingForm/hotel_availability" || r.URL.Query().Get("criterions[0].adults") != "1" {
				t.Error("wrong source request")
			}
			w.Write(body)
		}))
		defer mock.Close()
		hotel.Source.BaseURL = mock.URL
	}
	r := Registry{Groups: []Group{{ID: "aerostar", Schema: schema, Properties: []Property{{Hotel: hotel}}}}}
	options := collect.RuntimeOptions{MaxJobs: 1, Batch: collect.BatchOptions{Workers: 2, Retry: collect.RetryPolicy{MaxAttempts: 1}}}
	s := testService(t, r, nil, options)
	rates := []string{}
	for _, rate := range hotel.Rates {
		if rate.Enabled {
			rates = append(rates, rate.ID)
		}
	}
	transport := http.DefaultTransport
	if mock != nil {
		transport = mock.Client().Transport
	}
	probe := &tlTransport{base: transport}
	client, err := travelline.NewClient(hotel.Source.BaseURL, &http.Client{Transport: probe}, rates)
	if err != nil {
		t.Fatal(err)
	}
	s.groups[0].Properties[0].Client = client
	when := time.Date(2026, 11, 17, 20, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	start := time.Now()
	for _, req := range []Request{{Action: "plan", Slot: when}, {Action: "resume"}} {
		if _, err := s.Execute(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	var jobID string
	if err := admin.QueryRow(`SELECT id::text FROM "` + schema + `".scenario_jobs`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.groups[0].Store.GetAttempt(context.Background(), jobID, 1)
	if err != nil {
		t.Fatal(err)
	}
	evidence := struct {
		DurationMS int64
		Requests   []tlCapture
		Attempt    storage.Attempt
		Counts     map[string]int
	}{time.Since(start).Milliseconds(), probe.calls, attempt, map[string]int{}}
	defer func() {
		if live {
			path := os.Getenv("HOTEL_PARSER_E2E_OUTPUT")
			body, _ := json.MarshalIndent(evidence, "", "  ")
			if path == "" || os.WriteFile(path, body, 0600) != nil {
				t.Error("cannot write live evidence")
			}
		}
	}()
	if attempt.Error != nil {
		t.Fatalf("source failed safely: %s HTTP %d", attempt.Error.Code, attempt.HTTPStatus)
	}
	var primaryCalls []tlCapture
	var rulesRequests int
	for _, call := range probe.calls {
		if call.Path == "/ApiWebDistribution/AvailabilityCalendar/rate_plan_booking_rules" {
			rulesRequests++
		} else {
			primaryCalls = append(primaryCalls, call)
		}
	}
	if len(primaryCalls) != 2 || rulesRequests > 1 || (!live && (rulesRequests != 1 || bookingCalls.Load() != 1)) || attempt.HTTPStatus != 200 || attempt.Lease == nil || attempt.CollectedAt == nil || len(attempt.Observations) != 10 {
		t.Fatalf("incomplete persistence: calls=%d attempt=%+v", len(probe.calls), attempt)
	}
	req := source.SearchRequest{PropertyID: "585", CheckIn: when, CheckOut: when.AddDate(0, 0, 1), Adults: 1, Language: "ru"}
	parsed, err := travelline.Parse(primaryCalls[0].body, primaryCalls[1].body, req, rates)
	if err != nil {
		t.Fatal(err)
	}
	stocks := collect.NormalizeCategoryStock(parsed)
	expected := map[string]source.CategoryStock{}
	for _, stock := range stocks {
		expected[stock.CategoryID] = stock
	}
	for _, observation := range attempt.Observations {
		stock := expected[observation.CategoryID]
		if string(observation.StockStatus) != string(stock.Status) || (observation.Available == nil) != (stock.Available == nil) || stock.Available != nil && *observation.Available != *stock.Available {
			t.Fatal("quota changed in storage", observation.CategoryID)
		}
		evidence.Counts[string(stock.Status)]++
		for _, offer := range observation.Offers {
			found := false
			for _, raw := range parsed.Offers {
				if raw.CategoryID == observation.CategoryID && raw.RateID == offer.RateID && raw.VariantID == offer.VariantID {
					if offer.Price == nil || offer.Currency == nil || *offer.Currency != raw.Currency {
						t.Fatal("price/currency lost")
					}
					a, ok := new(big.Rat).SetString(*offer.Price)
					b, ok2 := new(big.Rat).SetString(raw.Price)
					if !ok || !ok2 || a.Cmp(b) != 0 {
						t.Fatal("price changed")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("unexpected stored offer")
			}
			evidence.Counts["offers"]++
		}
	}
	for _, req := range []Request{{Action: "plan", Slot: when}, {Action: "resume"}} {
		if _, err := s.Execute(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	for table, expectedCount := range map[string]int{"scenario_jobs": 1, "collection_attempts": 1, "observations": 10, "observation_offers": len(parsed.Offers)} {
		var count int
		if err := admin.QueryRow(`SELECT count(*) FROM "` + schema + `"."` + table + `"`).Scan(&count); err != nil || count != expectedCount {
			t.Fatalf("%s count=%d expected=%d err=%v", table, count, expectedCount, err)
		}
	}
	if len(probe.calls) != len(primaryCalls)+rulesRequests {
		t.Fatal("replay sent extra HTTP requests")
	}
	progress, err := s.groups[0].Store.RunProgress(context.Background())
	if err != nil || len(progress) != 1 || progress[0].Remaining != 0 {
		t.Fatal(progress, err)
	}
	t.Logf("TravelLine: HTTP calls=%d, duration=%dms, observations=%d, stocks=%v, outcome=%s", len(probe.calls), evidence.DurationMS, len(attempt.Observations), evidence.Counts, attempt.Outcome)
}
