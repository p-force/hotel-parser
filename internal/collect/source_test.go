package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/source/travelline"
)

func TestSourceFactoryAndPublicRateIsolation(t *testing.T) {
	cfg, err := config.Load("../../configs/aerostar.json")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Collection.Enabled {
		t.Fatal("verified Aerostar source is not enabled")
	}
	tl, err := NewSourceClient(cfg)
	if err != nil || tl.SourceType() != "travelline" || tl.SourceOrigin() != travelline.PublicOrigin {
		t.Fatal(tl, err)
	}
	other := cfg
	other.Source.PropertyID = "586"
	if SourceClientKey(cfg, travelline.PublicOrigin) == SourceClientKey(other, travelline.PublicOrigin) {
		t.Fatal("public tariff lists shared across properties")
	}
	cfg.Rates = nil
	if cfg.Validate() == nil {
		t.Fatal("TravelLine without public rates accepted")
	}
	cfg.Source.Type = "unsupported"
	if _, err := NewSourceClient(cfg); err == nil {
		t.Fatal("unsupported adapter accepted")
	}
	cfg, err = config.Load("../../configs/arena33.json")
	if err != nil {
		t.Fatal(err)
	}
	bn, err := NewSourceClient(cfg)
	if err != nil || bn.SourceType() != "bnovo" {
		t.Fatal(bn, err)
	}
}

func TestTravelLineRetryUsesExistingPool(t *testing.T) {
	info, err := os.ReadFile("../source/travelline/testdata/aerostar-info.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../source/travelline/testdata/aerostar-2026-11-17-adults-2.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/ApiWebDistribution/BookingForm/hotel_info" {
			w.Write(info)
		} else {
			w.Write(body)
		}
	}))
	defer server.Close()
	client, err := travelline.NewClient(server.URL, server.Client(), []string{"128299"})
	if err != nil {
		t.Fatal(err)
	}
	in := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	req := source.SearchRequest{PropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), Adults: 2, Language: "ru"}
	run, err := collectJobs(context.Background(), []batchJob{{request: req, client: client, propertyID: "aerostar", source: "travelline"}}, BatchOptions{Workers: 1, Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}})
	if err != nil || calls.Load() != 3 || len(run.results) != 1 || run.attempts[0] != 2 || run.results[0].Error != nil || len(run.results[0].Offers) != 8 {
		t.Fatalf("calls=%d attempts=%v err=%v result=%+v", calls.Load(), run.attempts, err, run.results)
	}
	if run.results[0].Status != bnovo.SearchStatusPartial {
		t.Fatal(run.results[0].Status)
	}
	stocks := NormalizeCategoryStock(run.results[0])
	known := 0
	for _, stock := range stocks {
		if stock.Status == StockKnown {
			known++
		}
	}
	if known != 8 {
		t.Fatalf("normalization dispatch lost: %v", stocks)
	}
}

func TestTravelLineClientPolicyIdentity(t *testing.T) {
	cfg, err := config.Load("../../configs/aerostar.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Rates = []config.RateConfig{{ID: "128299", Enabled: true}, {ID: "128301", Enabled: true}}
	base := SourceClientKey(cfg, cfg.Source.BaseURL)
	for _, tc := range []struct {
		name   string
		change func(*config.Config)
		origin string
		equal  bool
	}{
		{"reordered", func(c *config.Config) {
			c.Rates = []config.RateConfig{{ID: "128301", Enabled: true}, {ID: "128299", Enabled: true}}
		}, cfg.Source.BaseURL, true},
		{"duplicate", func(c *config.Config) { c.Rates = append(c.Rates, c.Rates[0]) }, cfg.Source.BaseURL, true},
		{"disabled rate", func(c *config.Config) { c.Rates = append(c.Rates, config.RateConfig{ID: "other", Enabled: false}) }, cfg.Source.BaseURL, true},
		{"local identity", func(c *config.Config) { c.ID = "another"; c.Collection.Enabled = !c.Collection.Enabled }, cfg.Source.BaseURL, true},
		{"canonical origin", func(*config.Config) {}, "https://RU-IBE.TLINTEGRATION.RU:443/", true},
		{"different rates", func(c *config.Config) { c.Rates = c.Rates[:1] }, cfg.Source.BaseURL, false},
		{"different property", func(c *config.Config) { c.Source.PropertyID = "586" }, cfg.Source.BaseURL, false},
		{"different origin", func(*config.Config) {}, "https://another.example", false},
		{"different source", func(c *config.Config) { c.Source.Type = "bnovo" }, cfg.Source.BaseURL, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := cfg
			copy.Rates = append([]config.RateConfig(nil), cfg.Rates...)
			tc.change(&copy)
			if equal := SourceClientKey(copy, tc.origin) == base; equal != tc.equal {
				t.Fatalf("key equality=%v want=%v", equal, tc.equal)
			}
		})
	}
	cfg.Source.Type = "bnovo"
	if got := SourceClientKey(cfg, "https://EXAMPLE.com:443/"); got != "bnovo/https://EXAMPLE.com:443/" {
		t.Fatal("Bnovo key changed", got)
	}
}
