package travelline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source"
)

func TestCompetitorPublicFixtures(t *testing.T) {
	for _, tc := range []struct {
		file, id                  string
		categories, offers, known int
	}{
		{"monarch", "17249", 8, 42, 8}, {"bega", "61", 8, 32, 8}, {"aerostar", "585", 10, 40, 8}, {"sovietsky", "333", 7, 12, 6}, {"petroff", "5787", 6, 2, 1},
	} {
		t.Run(tc.file, func(t *testing.T) {
			cfg, err := config.Load("../../../configs/" + tc.file + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.Collection.Enabled || cfg.Source.PropertyID != tc.id || cfg.Inventory.TotalRooms != nil {
				t.Fatal("invalid enabled public source config")
			}
			rates := []string{}
			for _, r := range cfg.Rates {
				if r.Enabled {
					if strings.Contains(strings.ToLower(r.Name), "bonus") || r.ID == "131363" {
						t.Fatal("restricted tariff enabled", r)
					}
					rates = append(rates, r.ID)
				}
			}
			info, err := os.ReadFile("testdata/competitors/" + tc.id + "-info.json")
			if err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob("testdata/competitors/" + tc.id + "-availability-*.json")
			if err != nil || len(files) == 0 {
				t.Fatal(files, err)
			}
			for _, file := range files {
				name := filepath.Base(file)
				date := name[len(tc.id)+14 : len(tc.id)+24]
				in, _ := time.Parse("2006-01-02", date)
				adults, los := 1, 1
				if strings.Contains(name, "a2-") {
					adults = 2
				}
				if strings.Contains(name, "los2") {
					los = 2
				}
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				req := source.SearchRequest{PropertyID: tc.id, CheckIn: in, CheckOut: in.AddDate(0, 0, los), Adults: adults, Language: "ru"}
				result, err := Parse(info, body, req, rates)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if len(result.Categories) != tc.categories {
					t.Fatal("metadata categories lost", name)
				}
				ids := map[string]bool{}
				for _, r := range rates {
					ids[r] = true
				}
				for _, o := range result.Offers {
					if !ids[o.RateID] || o.Currency != "RUB" || !decimalPrice.MatchString(o.Price) || o.Availability.ReportedQuantity == nil {
						t.Fatal(name, o)
					}
				}
				stocks := NormalizeCategoryStock(result)
				known := 0
				for _, s := range stocks {
					if s.Status == source.StockKnown {
						known++
						if s.Available == nil || *s.Available <= 0 {
							t.Fatal("false unavailable", name, s)
						}
					} else if s.Status != source.StockUnknown || s.Available != nil {
						t.Fatal("unknown/conflict with quantity", name, s)
					}
				}
				if date == "2026-11-17" && adults == 1 && los == 1 && (len(result.Offers) != tc.offers || known != tc.known) {
					t.Fatalf("%s offers=%d known=%d", name, len(result.Offers), known)
				}
				if tc.id == "5787" && date == "2026-12-21" {
					// Фактические apartment-категории должны сохранять не только metadata, но и offers.
					apartment := map[string]bool{}
					for _, o := range result.Offers {
						if o.CategoryID == "34190" || o.CategoryID == "34191" {
							apartment[o.CategoryID] = true
						}
					}
					if len(apartment) != 2 {
						t.Fatal("apartment offers lost", apartment)
					}
				}
			}
		})
	}
}
