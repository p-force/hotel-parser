package travelline

import (
	"encoding/json"
	"fmt"

	"os"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
)

var publicRates = []string{"128299", "128301", "128311", "128337", "917131"}

func fixture(t *testing.T) ([]byte, []byte, source.SearchRequest) {
	t.Helper()
	info, err := os.ReadFile("testdata/aerostar-info.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("testdata/aerostar-2026-11-17-adults-2.json")
	if err != nil {
		t.Fatal(err)
	}
	in := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	return info, body, source.SearchRequest{PropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), Adults: 2, Language: "ru"}
}
func TestConfirmedAerostarFixture(t *testing.T) {
	info, body, req := fixture(t)
	result, err := Parse(info, body, req, publicRates)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "travelline" || result.Status != source.Partial || len(result.Categories) != 10 || len(result.Offers) != 40 {
		t.Fatalf("unexpected fixture: status=%s categories=%d offers=%d warnings=%+v", result.Status, len(result.Categories), len(result.Offers), result.Warnings)
	}
	known, unknown := 0, 0
	for _, stock := range NormalizeCategoryStock(result) {
		switch stock.Status {
		case source.StockKnown:
			known++
			if stock.Available == nil || *stock.Available < 1 {
				t.Fatal(stock)
			}
		case source.StockUnknown:
			unknown++
			if stock.Available != nil {
				t.Fatal(stock)
			}
		default:
			t.Fatal(stock)
		}
	}
	if known != 8 || unknown != 2 {
		t.Fatalf("known=%d unknown=%d", known, unknown)
	}
	for _, offer := range result.Offers {
		if offer.RateID == "741827" || offer.RateID == "751714" {
			t.Fatal("loyalty rate treated as public")
		}
		if offer.CategoryID == "4347" && offer.RateID == "128299" {
			if offer.Price != "7500.0" || offer.Currency != "RUB" || offer.Availability.ReportedQuantity == nil || *offer.Availability.ReportedQuantity != 10 {
				t.Fatalf("price/quota lost: %+v", offer)
			}
		}
		if offer.Availability.DataAvailable != nil || len(offer.Availability.SelectableQuantities) != 0 {
			t.Fatal("invented Bnovo HTML evidence")
		}
	}
	for _, category := range result.Categories {
		if category.AreaM2 == nil || *category.AreaM2 <= 0 {
			t.Fatal("source area lost")
		}
	}
	before, _ := json.Marshal(result)
	stocks := NormalizeCategoryStock(result)
	for i := range stocks {
		if stocks[i].Available != nil {
			*stocks[i].Available = 999
		}
		if len(stocks[i].Warnings) > 0 {
			stocks[i].Warnings[0].Message = "changed"
		}
	}
	after, _ := json.Marshal(result)
	if string(before) != string(after) {
		t.Fatal("normalizer mutated source")
	}
}
func TestAvailabilityConservative(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*availability)
		status source.StockStatus
		error  bool
	}{
		{"empty_offers", func(d *availability) { d.RoomStays = d.RoomStays[:0] }, source.StockUnknown, false},
		{"missing_quantity", func(d *availability) { d.Quotas = nil }, source.StockUnknown, false},
		{"explicit_zero_without_unavailability_proof", func(d *availability) {
			for i := range d.Quotas {
				v := 0
				d.Quotas[i].Quantity = &v
			}
		}, source.StockUnknown, false},
		{"negative_quantity", func(d *availability) {
			for i := range d.Quotas {
				v := -1
				d.Quotas[i].Quantity = &v
			}
		}, source.StockUnknown, false},
		{"wrong_dates", func(d *availability) {
			for i := range d.RoomStays {
				d.RoomStays[i].Dates.End = "2026-11-19 12:00:00"
			}
		}, source.StockUnknown, false},
		{"wrong_adults", func(d *availability) {
			for i := range d.RoomStays {
				d.RoomStays[i].Guests = nil
			}
		}, source.StockUnknown, false},
		{"wrong_hotel", func(d *availability) {
			for i := range d.RoomStays {
				d.RoomStays[i].Hotel.Code = "101"
			}
		}, source.StockUnknown, false},
		{"unknown_category", func(d *availability) {
			for i := range d.RoomStays {
				d.RoomStays[i].Rooms[0].Code = "999"
			}
		}, source.StockUnknown, false},
		{"conflicting_quota", func(d *availability) { q := d.Quotas[0]; v := 77; q.Quantity = &v; d.Quotas = append(d.Quotas, q) }, source.StockConflict, false},
		// Nonempty provider diagnostics are deliberately opaque; do not guess min-stay codes.
		{"minimum_stay_diagnostic", func(d *availability) {
			d.RoomStays = d.RoomStays[:0]
			d.Errors = []json.RawMessage{json.RawMessage(`{"error_code":"450","message":"minimum stay"}`)}
		}, source.StockUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, body, req := fixture(t)
			var data availability
			if err := json.Unmarshal(body, &data); err != nil {
				t.Fatal(err)
			}
			tc.change(&data)
			body, _ = json.Marshal(data)
			result, err := Parse(info, body, req, publicRates)
			if (err != nil) != tc.error {
				t.Fatalf("error=%v", err)
			}
			if result.Status == source.NoOffers || result.Status == source.Success {
				t.Fatalf("ambiguous input acquired full status: %s", result.Status)
			}
			stocks := NormalizeCategoryStock(result)
			conflicts := 0
			for _, stock := range stocks {
				if stock.Status == source.StockConflict {
					conflicts++
				}
				if stock.Available != nil && *stock.Available == 0 {
					t.Fatal("invented zero")
				}
				if tc.status == source.StockUnknown && (stock.Status != source.StockUnknown || stock.Available != nil) {
					t.Fatalf("unconfirmed stock: %+v", stock)
				}
			}
			if tc.status == source.StockConflict && conflicts == 0 {
				t.Fatal("quota conflict lost")
			}
		})
	}
}
func TestMalformedMetadataAndAvailability(t *testing.T) {
	info, body, req := fixture(t)
	for _, pair := range [][2][]byte{{[]byte(`{`), body}, {info, []byte(`{`)}, {info, []byte(`{}`)}, {[]byte(`{"hotels":[]}`), body}} {
		result, err := Parse(pair[0], pair[1], req, publicRates)
		if err == nil || result.Status != source.Failed {
			t.Fatalf("invalid payload accepted: %+v %v", result, err)
		}
	}
}
func TestTariffsNotSummedAndDuplicateKeysStayExcluded(t *testing.T) {
	info, body, req := fixture(t)
	var data availability
	json.Unmarshal(body, &data)
	var chosen []int
	for i, s := range data.RoomStays {
		if s.Rooms[0].Code == "4347" && (s.Rates[0].Code == "128299" || s.Rates[0].Code == "128301") {
			chosen = append(chosen, i)
		}
	}
	if len(chosen) != 2 {
		t.Fatal("fixture changed")
	}
	a, b := data.RoomStays[chosen[0]], data.RoomStays[chosen[1]]
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "conflict_A_B_Aprime_A"}[conflict], func(t *testing.T) {
			duplicate := a
			if conflict {
				duplicate.Total.Price = "999.0"
			}
			data.RoomStays = append(data.RoomStays[:0], a, b, duplicate, a)
			raw, _ := json.Marshal(data)
			result, err := Parse(info, raw, req, publicRates)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if conflict {
				want = 1
			}
			if len(result.Offers) != want {
				t.Fatalf("offers=%+v", result.Offers)
			}
			for _, offer := range result.Offers {
				if conflict && offer.RateID == a.Rates[0].Code {
					t.Fatal("conflicting key restored")
				}
			}
			if !conflict {
				for _, stock := range NormalizeCategoryStock(result) {
					if stock.CategoryID == "4347" && (stock.Available == nil || *stock.Available != 10) {
						t.Fatal("rates summed or quantity lost")
					}
				}
			}
		})
	}
}

func TestScenarioEchoAndNoPersonalRates(t *testing.T) {
	for _, adults := range []int{1, 2} {
		for _, los := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("adults%d_LOS%d", adults, los), func(t *testing.T) {
				info, body, req := fixture(t)
				req.Adults = adults
				req.CheckOut = req.CheckIn.AddDate(0, 0, los)
				var data availability
				json.Unmarshal(body, &data)
				for i := range data.RoomStays {
					stay := &data.RoomStays[i]
					stay.Dates.End = req.CheckOut.Format("2006-01-02") + " 12:00:00"
					stay.Guests = stay.Guests[:1]
					stay.Guests[0].Count = adults
				}
				body, _ = json.Marshal(data)
				result, err := Parse(info, body, req, append(append([]string(nil), publicRates...), "751714", "741398", "741402", "741827", "741828"))
				if err != nil || len(result.Offers) != 40 {
					t.Fatal(err, len(result.Offers))
				}
				for _, v := range result.Variants {
					if v.Adults != adults {
						t.Fatal(v)
					}
				}
				for _, offer := range result.Offers {
					for _, id := range []string{"751714", "741398", "741402", "741827", "741828"} {
						if offer.RateID == id {
							t.Fatal("BONUS rate published")
						}
					}
				}
			})
		}
	}
}

func TestKnownOnlyForCompletePublicCategories(t *testing.T) {
	info, body, req := fixture(t)
	var meta metadata
	json.Unmarshal(info, &meta)
	var data availability
	json.Unmarshal(body, &data)
	available := map[string]bool{}
	for _, stay := range data.RoomStays {
		available[stay.Rooms[0].Code] = true
	}
	rooms := meta.Hotels[0].RoomTypes[:0]
	for _, room := range meta.Hotels[0].RoomTypes {
		if available[room.Code] {
			rooms = append(rooms, room)
		}
	}
	meta.Hotels[0].RoomTypes = rooms
	info, _ = json.Marshal(meta)
	result, err := Parse(info, body, req, publicRates)
	if err != nil || result.Status != source.Success || len(result.Warnings) != 0 || len(result.Categories) != 8 {
		t.Fatal(err, result.Status, result.Warnings)
	}
	for i := range result.Offers {
		result.Offers[i].Visible = false
	}
	for _, stock := range NormalizeCategoryStock(result) {
		if stock.Status == source.StockKnown {
			t.Fatal("hidden offer confirms quota")
		}
	}
	for i := range result.Offers {
		result.Offers[i].Visible = true
		result.Offers[i].Availability.SelectDisabled = true
	}
	for _, stock := range NormalizeCategoryStock(result) {
		if stock.Status == source.StockKnown {
			t.Fatal("disabled offer confirms quota")
		}
	}
}
