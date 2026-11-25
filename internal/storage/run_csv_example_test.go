package storage_test

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/travelline"
)

func TestCSVExampleMatchesTravelLineParser(t *testing.T) {
	file, err := os.Open("../../docs/csv/example.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil || len(records) != 6 || len(records[0]) != 35 {
		t.Fatal(records, err)
	}
	columns := map[string]int{}
	for i, name := range records[0] {
		columns[name] = i
	}
	value := func(row []string, key string) string { return row[columns[key]] }
	var rooms, rates, stays, quotas []map[string]any
	seen := map[string]bool{}
	var allowed []string
	for _, row := range records[1:] {
		category := value(row, "category_id")
		if category == "" {
			if value(row, "job_status") != "failed" || value(row, "has_offers") != "" || value(row, "confirmed_unavailable") != "" {
				t.Fatal(row)
			}
			continue
		}
		if !seen[category] {
			area, _ := strconv.ParseFloat(value(row, "area_m2"), 64)
			rooms = append(rooms, map[string]any{"code": category, "name": value(row, "category_name"), "kind": "room", "size": map[string]any{"unit": "square_metre", "value": area}})
			seen[category] = true
		}
		rate := value(row, "rate_id")
		if rate == "" {
			continue
		}
		rates = append(rates, map[string]any{"code": rate, "name": value(row, "rate_name")})
		allowed = append(allowed, rate)
		q, err := strconv.Atoi(value(row, "reported_quantity"))
		if err != nil {
			t.Fatal(err)
		}
		quota := "q-" + rate
		quotas = append(quotas, map[string]any{"rph": quota, "quantity": q})
		stays = append(stays, map[string]any{
			"hotel_ref":  map[string]any{"code": "585"},
			"guests":     []map[string]any{{"count": 1, "placement": map[string]any{"kind": "adult"}}},
			"room_types": []map[string]any{{"code": category, "room_type_quota_rph": quota, "placements": []map[string]any{{"code": "11511"}}}},
			"rate_plans": []map[string]any{{"code": rate}},
			"total":      map[string]any{"price_after_tax": json.Number(value(row, "price")), "currency": value(row, "currency")},
			"stay_dates": map[string]any{"start_date": "2026-11-17 00:00:00", "end_date": "2026-11-18 00:00:00"},
		})
	}
	info, _ := json.Marshal(map[string]any{"hotels": []map[string]any{{"code": "585", "currency": "RUB", "room_types": rooms, "rate_plans": rates}}})
	body, _ := json.Marshal(map[string]any{"room_stays": stays, "room_type_quotas": quotas})
	in := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	result, err := travelline.Parse(info, body, source.SearchRequest{PropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), Adults: 1, Language: "ru"}, allowed)
	if err != nil {
		t.Fatal(err)
	}
	stocks := map[string]source.CategoryStock{}
	for _, stock := range travelline.NormalizeCategoryStock(result) {
		stocks[stock.CategoryID] = stock
	}
	states := map[string]source.CategoryAvailability{}
	for _, state := range result.CategoryAvailability {
		states[state.CategoryID] = state
	}
	for _, row := range records[1:] {
		category := value(row, "category_id")
		if category == "" {
			continue
		}
		stock := stocks[category]
		state := states[category]
		if value(row, "job_status") != string(result.Status) || value(row, "stock_status") != string(stock.Status) || value(row, "has_offers") != strconv.FormatBool(state.HasOffers) || value(row, "confirmed_unavailable") != strconv.FormatBool(state.ConfirmedUnavailable) {
			t.Fatal("example contradicts real parser", row, result.Status, stock, state)
		}
		if stock.Available == nil && value(row, "available") != "" {
			t.Fatal("invented availability", row)
		}
		type warning struct{ Code, Context string }
		compare := func(cell string, warnings []source.ParseWarning) {
			t.Helper()
			var got []warning
			if err := json.Unmarshal([]byte(cell), &got); err != nil {
				t.Fatal(err)
			}
			want := map[warning]int{}
			actual := map[warning]int{}
			for _, w := range warnings {
				want[warning{w.Code, w.Context}]++
			}
			for _, w := range got {
				actual[w]++
			}
			if !reflect.DeepEqual(want, actual) {
				t.Fatal("example warning mismatch", cell, want)
			}
		}
		compare(value(row, "attempt_warning_codes"), result.Warnings)
		compare(value(row, "category_warning_codes"), stock.Warnings)
	}
}
