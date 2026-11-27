package travelline

import (
	"reflect"
	"testing"
	"time"
)

func TestConfirmCategoryBookingForbidden(t *testing.T) {
	const allForbidden = `[
		{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
		{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":true}
	]`
	checkIn := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, rules string
		rates       []string
		los         int
		want        bool
	}{
		{"all rates forbidden", allForbidden, []string{"128299", "128301"}, 1, true},
		{"only some rates forbidden", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":false}
		]`, []string{"128299", "128301"}, 1, false},
		{"first rate not forbidden", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false},
			{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":true}
		]`, []string{"128299", "128301"}, 1, false},
		{"missing last rate", allForbidden, []string{"128299", "999999"}, 1, false},
		{"missing first rate", allForbidden, []string{"999999", "128299"}, 1, false},
		{"forbidden absent", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25"}
		]`, []string{"128299"}, 1, false},
		{"forbidden false", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false}
		]`, []string{"128299"}, 1, false},
		{"conflicting forbidden", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false}
		]`, []string{"128299"}, 1, false},
		{"conflict persists after repeated true", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true}
		]`, []string{"128299"}, 1, false},
		{"conflict in other fields blocks confirmation", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":2},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":3}
		]`, []string{"128299"}, 1, false},
		{"identical duplicates", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":true}
		]`, []string{"128299", "128301"}, 1, true},
		{"empty rates", allForbidden, []string{}, 1, false},
		{"nil rates", allForbidden, nil, 1, false},
		{"empty rate ID", allForbidden, []string{"128299", ""}, 1, false},
		{"LOS 2", allForbidden, []string{"128299", "128301"}, 2, true},
		{"LOS 3", allForbidden, []string{"128299", "128301"}, 3, true},
		{"LOS zero", allForbidden, []string{"128299", "128301"}, 0, false},
		{"other restrictions do not override explicit forbidden", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":5,"max_los":0,"min_adv_booking":999}
		]`, []string{"128299"}, 1, true},
		{"other restrictions do not establish forbidden", `[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","min_los":5,"max_los":0,"min_adv_booking":999}
		]`, []string{"128299"}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := bookingIndexResponse(t, `{"booking_rules":`+tc.rules+`}`)
			index := response.Index()
			if got := ConfirmCategoryBookingForbidden(index, checkIn, "4347", tc.rates, tc.los); got != tc.want {
				t.Fatalf("confirmation=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfirmCategoryBookingForbiddenUsesExactScope(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[
		{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":true},
		{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":false},
		{"room_types":["4350"],"code":"128299","date":"2026-11-26","forbidden":false}
	]}`)
	index := response.Index()
	zone := time.FixedZone("UTC+3", 3*60*60)
	checkIn := time.Date(2026, 11, 25, 0, 30, 0, 0, zone)
	rates := []string{"128299"}
	key := BookingRuleKey{Date: "2026-11-25", RoomTypeID: "4350", RateID: "128299"}
	before, _ := index.Lookup(key)
	if !ConfirmCategoryBookingForbidden(index, checkIn, "4350", rates, 1) {
		t.Fatal("calendar date shifted or unrelated rate/category affected confirmation")
	}
	for _, tc := range []struct {
		name, category string
		date           time.Time
	}{
		{"next date", "4350", checkIn.AddDate(0, 0, 1)},
		{"missing category", "other", checkIn},
		{"missing date", "4350", time.Time{}},
		{"empty category", "", checkIn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ConfirmCategoryBookingForbidden(index, tc.date, tc.category, rates, 1) {
				t.Fatal("confirmation escaped exact date/category scope")
			}
		})
	}
	after, _ := index.Lookup(key)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(rates, []string{"128299"}) {
		t.Fatal("check changed index or supplied rates")
	}
	if ConfirmCategoryBookingForbidden(BookingRulesIndex{}, checkIn, "4350", rates, 1) {
		t.Fatal("empty index confirms forbidden")
	}
}
