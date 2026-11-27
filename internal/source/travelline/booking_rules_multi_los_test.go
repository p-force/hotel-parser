package travelline

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	_ "time/tzdata"
)

func TestBookingForbiddenMultiLOSCalendar(t *testing.T) {
	rule := func(date, rate string, forbidden *bool) bookingRule {
		return bookingRule{Date: date, Code: rate, RoomTypes: []string{"29344"}, Forbidden: forbidden}
	}
	blocked := func(date string) []bookingRule {
		return []bookingRule{rule(date, "38564", bookingIndexValue(true)), rule(date, "49218", bookingIndexValue(true))}
	}
	conflict := append(blocked("2026-12-03"), rule("2026-12-03", "38564", bookingIndexValue(false)))
	for _, tc := range []struct {
		name, in, zone string
		los            int
		rules          []bookingRule
		rates          []string
		category       string
		want           bool
	}{
		{name: "LOS1_sovietsky_control", in: "2026-12-05", los: 1, rules: blocked("2026-12-05"), want: true},
		{name: "LOS2_arrival_only", los: 2, rules: blocked("2026-12-03"), want: true},
		{name: "LOS3_arrival_only", los: 3, rules: blocked("2026-12-03"), want: true},
		{name: "LOS2_second_night", los: 2, rules: blocked("2026-12-04"), want: true},
		{name: "LOS3_second_night", los: 3, rules: blocked("2026-12-04"), want: true},
		{name: "LOS3_third_night", los: 3, rules: blocked("2026-12-05"), want: true},
		{name: "LOS2_checkout_excluded", los: 2, rules: blocked("2026-12-05")},
		{name: "LOS3_checkout_excluded", los: 3, rules: blocked("2026-12-06")},
		{name: "LOS3_rates_blocked_on_different_nights", los: 3, rules: []bookingRule{rule("2026-12-03", "38564", bookingIndexValue(true)), rule("2026-12-05", "49218", bookingIndexValue(true))}, want: true},
		{name: "LOS2_one_rate_only", los: 2, rules: []bookingRule{rule("2026-12-04", "38564", bookingIndexValue(true))}},
		{name: "LOS2_false_is_not_evidence", los: 2, rules: []bookingRule{rule("2026-12-03", "38564", bookingIndexValue(false)), rule("2026-12-04", "49218", bookingIndexValue(false))}},
		{name: "LOS3_NULL_is_not_evidence", los: 3, rules: []bookingRule{rule("2026-12-03", "38564", nil), rule("2026-12-05", "49218", nil)}},
		{name: "LOS2_no_records", los: 2},
		{name: "LOS2_identical_duplicates", los: 2, rules: append(blocked("2026-12-04"), rule("2026-12-04", "38564", bookingIndexValue(true))), want: true},
		{name: "LOS2_conflict_without_alternative", los: 2, rules: conflict},
		{name: "LOS2_conflict_then_unambiguous_day", los: 2, rules: append(append([]bookingRule{}, conflict...), rule("2026-12-04", "38564", bookingIndexValue(true))), want: true},
		{name: "LOS3_NULL_then_blocked", los: 3, rules: append([]bookingRule{rule("2026-12-03", "38564", nil), rule("2026-12-03", "49218", nil)}, blocked("2026-12-05")...), want: true},
		{name: "LOS2_empty_rates", los: 2, rules: blocked("2026-12-03"), rates: []string{}},
		{name: "LOS3_empty_rate_ID", los: 3, rules: blocked("2026-12-03"), rates: []string{"38564", ""}},
		{name: "LOS2_invalid_rate_ID", los: 2, rules: blocked("2026-12-03"), rates: []string{"38564", "not-a-rate"}},
		{name: "LOS2_other_category", los: 2, rules: blocked("2026-12-03"), category: "2416"},
		{name: "LOS2_month_boundary", in: "2027-01-31", los: 2, rules: blocked("2027-02-01"), want: true},
		{name: "LOS3_year_boundary", in: "2026-12-31", los: 3, rules: blocked("2027-01-02"), want: true},
		{name: "LOS3_DST_spring_calendar", in: "2027-03-27", zone: "Europe/Berlin", los: 3, rules: blocked("2027-03-29"), want: true},
		{name: "LOS3_DST_autumn_calendar", in: "2026-10-24", zone: "Europe/Berlin", los: 3, rules: blocked("2026-10-26"), want: true},
		{name: "LOS3_DST_checkout_excluded", in: "2026-10-24", zone: "Europe/Berlin", los: 3, rules: blocked("2026-10-27")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc := time.UTC
			if tc.zone != "" {
				var err error
				loc, err = time.LoadLocation(tc.zone)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.in == "" {
				tc.in = "2026-12-03"
			}
			in, err := time.ParseInLocation("2006-01-02", tc.in, loc)
			if err != nil {
				t.Fatal(err)
			}
			category := tc.category
			if category == "" {
				category = "29344"
			}
			rates := tc.rates
			if rates == nil {
				rates = []string{"38564", "49218"}
			}
			before := append([]string{}, rates...)
			index := (bookingRulesResponse{BookingRules: tc.rules}).Index()
			got := ConfirmCategoryBookingForbidden(index, in, category, rates, tc.los)
			if !reflect.DeepEqual(before, append([]string{}, rates...)) {
				t.Error("confirmation mutated tariff policy")
			}
			if got != tc.want {
				t.Errorf("[%s,%s) LOS=%d: forbidden=%v, want %v", in.Format("2006-01-02"), in.AddDate(0, 0, tc.los).Format("2006-01-02"), tc.los, got, tc.want)
			}
		})
	}
}

func TestBookingForbiddenSovietskyFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/sovietsky-booking-rules-2026-12.json")
	if err != nil {
		t.Fatal(err)
	}
	index := bookingIndexResponse(t, string(body)).Index()
	cases, err := os.ReadFile("testdata/sovietsky-multi-los-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Category string   `json:"room_type_id"`
		Rates    []string `json:"rate_ids"`
		Cases    []struct {
			Name     string `json:"name"`
			CheckIn  string `json:"check_in"`
			CheckOut string `json:"check_out"`
			LOS      int    `json:"los"`
			Want     bool   `json:"confirmed_forbidden"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(cases, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			in, err := time.Parse("2006-01-02", tc.CheckIn)
			if err != nil || in.AddDate(0, 0, tc.LOS).Format("2006-01-02") != tc.CheckOut {
				t.Fatal("invalid scenario fixture", err)
			}
			if got := ConfirmCategoryBookingForbidden(index, in, fixture.Category, fixture.Rates, tc.LOS); got != tc.Want {
				t.Errorf("forbidden=%v, want %v", got, tc.Want)
			}
		})
	}
}
