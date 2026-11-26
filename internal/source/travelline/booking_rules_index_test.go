package travelline

import (
	"encoding/json"
	"reflect"
	"testing"
)

func bookingIndexResponse(t *testing.T, body string) bookingRulesResponse {
	t.Helper()
	var response bookingRulesResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func bookingIndexValue[T any](v T) *T { return &v }

func TestBookingRulesIndexMultipleCategories(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":false,"min_los":0,"max_los":7,"min_adv_booking":2}]}`)
	index := response.Index()
	for _, category := range []string{"4347", "4350"} {
		match, found := index.Lookup(BookingRuleKey{Date: "2026-11-25", RoomTypeID: category, RateID: "128299"})
		if !found || match.Conflict || !reflect.DeepEqual(match.Rules, response.BookingRules) {
			t.Fatalf("category %s lost fields: %+v found=%v", category, match, found)
		}
	}
}

func TestBookingRulesIndexDatesAndRates(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[
		{"room_types":["4347"],"code":"128299","date":"2026-11-25","min_los":1},
		{"room_types":["4347"],"code":"128301","date":"2026-11-25","min_los":2},
		{"room_types":["4347"],"code":"128299","date":"2026-11-26","min_los":3},
		{"room_types":["4350"],"code":"128301","date":"2026-11-26","min_los":4}
	]}`)
	index := response.Index()
	for _, rule := range response.BookingRules {
		key := BookingRuleKey{Date: rule.Date, RoomTypeID: rule.RoomTypes[0], RateID: rule.Code}
		match, found := index.Lookup(key)
		if !found || match.Conflict || !reflect.DeepEqual(match.Rules, []bookingRule{rule}) {
			t.Fatalf("mixed scenarios at %+v: %+v found=%v", key, match, found)
		}
	}
	for _, key := range []BookingRuleKey{
		{Date: "2026-11-27", RoomTypeID: "4347", RateID: "128299"},
		{Date: "2026-11-25", RoomTypeID: "missing", RateID: "128299"},
		{Date: "2026-11-25", RoomTypeID: "4347", RateID: "missing"},
		{Date: "2026-11-26", RoomTypeID: "4347", RateID: "128301"},
	} {
		if match, found := index.Lookup(key); found || match.Conflict || match.Rules != nil {
			t.Fatalf("invented rule at %+v: %+v found=%v", key, match, found)
		}
	}
}

func TestBookingRulesIndexNoRestrictionsIsPresent(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[{"room_types":["4348"],"code":"128299","date":"2026-11-26"}]}`)
	index := response.Index()
	key := BookingRuleKey{Date: "2026-11-26", RoomTypeID: "4348", RateID: "128299"}
	match, found := index.Lookup(key)
	if !found || match.Conflict || len(match.Rules) != 1 {
		t.Fatal("unrestricted rule treated as missing", match, found)
	}
	rule := match.Rules[0]
	if rule.Forbidden != nil || rule.MinLOS != nil || rule.MaxLOS != nil || rule.MinAdvBooking != nil {
		t.Fatal("absent restrictions became false/zero", rule)
	}
	key.RoomTypeID = "other"
	if _, found := index.Lookup(key); found {
		t.Fatal("unrestricted rule spread to another category")
	}
}

func TestBookingRulesIndexDoesNotExpandMissingRoomTypes(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[
		{"code":"128299","date":"2026-11-25","forbidden":true},
		{"room_types":[],"code":"128299","date":"2026-11-25","min_los":2},
		{"room_types":null,"code":"128299","date":"2026-11-25","max_los":7}
	]}`)
	before, _ := json.Marshal(response)
	index := response.Index()
	for _, category := range []string{"4347", "4350", ""} {
		if match, found := index.Lookup(BookingRuleKey{Date: "2026-11-25", RoomTypeID: category, RateID: "128299"}); found || match.Rules != nil {
			t.Fatal("rule without room_types became global", category, match)
		}
	}
	after, _ := json.Marshal(response)
	if string(before) != string(after) {
		t.Fatal("original unindexed records changed")
	}
	for _, empty := range []BookingRulesIndex{{}, (bookingRulesResponse{}).Index(), (bookingRulesResponse{BookingRules: []bookingRule{}}).Index()} {
		if _, found := empty.Lookup(BookingRuleKey{}); found {
			t.Fatal("empty index has a rule")
		}
	}
}

func TestBookingRulesIndexIdenticalDuplicates(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[
		{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":false,"min_los":2,"max_los":7,"min_adv_booking":0},
		{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":false,"min_los":2,"max_los":7,"min_adv_booking":0}
	]}`)
	index := response.Index()
	match, found := index.Lookup(BookingRuleKey{Date: "2026-11-25", RoomTypeID: "4347", RateID: "128299"})
	if !found || match.Conflict || !reflect.DeepEqual(match.Rules, response.BookingRules) {
		t.Fatal("identical duplicates lost or marked conflicting", match, found)
	}
	// Different source scopes still express identical restrictions at the shared key.
	response.BookingRules[1].RoomTypes = []string{"4350"}
	index = response.Index()
	for _, tc := range []struct {
		category string
		count    int
	}{{"4347", 1}, {"4350", 2}} {
		match, found := index.Lookup(BookingRuleKey{Date: "2026-11-25", RoomTypeID: tc.category, RateID: "128299"})
		if !found || match.Conflict || len(match.Rules) != tc.count {
			t.Fatal("scope difference confused with restriction conflict", match, found)
		}
	}
}

func TestBookingRulesIndexConflictingRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b bookingRule
	}{
		{"forbidden", bookingRule{Forbidden: bookingIndexValue(true)}, bookingRule{Forbidden: bookingIndexValue(false)}},
		{"min_los", bookingRule{MinLOS: bookingIndexValue(2)}, bookingRule{MinLOS: bookingIndexValue(3)}},
		{"max_los", bookingRule{MaxLOS: bookingIndexValue(7)}, bookingRule{MaxLOS: bookingIndexValue(8)}},
		{"min_adv_booking", bookingRule{MinAdvBooking: bookingIndexValue(0)}, bookingRule{MinAdvBooking: bookingIndexValue(1)}},
		{"absent vs false", bookingRule{}, bookingRule{Forbidden: bookingIndexValue(false)}},
		{"absent vs zero", bookingRule{}, bookingRule{MinLOS: bookingIndexValue(0)}},
		{"separate fields are not merged", bookingRule{MinLOS: bookingIndexValue(2)}, bookingRule{MaxLOS: bookingIndexValue(7)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := tc.a, tc.b
			for _, r := range []*bookingRule{&a, &b} {
				r.RoomTypes, r.Date, r.Code = []string{"4347"}, "2026-11-25", "128299"
			}
			for _, records := range [][]bookingRule{{a, b}, {b, a}, {a, b, a}, {b, a, b}} {
				index := (bookingRulesResponse{BookingRules: records}).Index()
				match, found := index.Lookup(BookingRuleKey{Date: a.Date, RoomTypeID: "4347", RateID: a.Code})
				if !found || !match.Conflict || !reflect.DeepEqual(match.Rules, records) {
					t.Fatalf("conflicting records overwritten: got %+v, records %+v", match, records)
				}
			}
		})
	}
}

func TestBookingRulesIndexConflictIsScopedToKey(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[
		{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","min_los":2},
		{"room_types":["4350"],"code":"128299","date":"2026-11-25","min_los":3}
	]}`)
	index := response.Index()
	for _, tc := range []struct {
		category string
		conflict bool
		count    int
	}{{"4347", false, 1}, {"4350", true, 2}} {
		match, found := index.Lookup(BookingRuleKey{Date: "2026-11-25", RoomTypeID: tc.category, RateID: "128299"})
		if !found || match.Conflict != tc.conflict || len(match.Rules) != tc.count {
			t.Fatal("conflict leaked to another key", tc, match)
		}
	}
}

func TestBookingRulesIndexRepeatedCategoryWithinOneRecord(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[{"room_types":["4347","4347","4350"],"code":"128299","date":"2026-11-25","min_los":2}]}`)
	index := response.Index()
	match, found := index.Lookup(BookingRuleKey{Date: "2026-11-25", RoomTypeID: "4347", RateID: "128299"})
	if !found || match.Conflict || !reflect.DeepEqual(match.Rules, response.BookingRules) {
		t.Fatal("repeated room ID invented a second source record", match, found)
	}
}

func TestBookingRulesIndexDoesNotAliasSourceOrLookupResult(t *testing.T) {
	response := bookingIndexResponse(t, `{"booking_rules":[{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":2,"max_los":7,"min_adv_booking":3}]}`)
	before, _ := json.Marshal(response)
	index := response.Index()
	after, _ := json.Marshal(response)
	if string(before) != string(after) {
		t.Fatal("Index changed source response")
	}
	var snapshot bookingRulesResponse
	if err := json.Unmarshal(before, &snapshot); err != nil {
		t.Fatal(err)
	}
	expected := snapshot.BookingRules[0]
	sourceRule := &response.BookingRules[0]
	sourceRule.RoomTypes[0], sourceRule.Date, sourceRule.Code = "other", "changed", "changed"
	*sourceRule.Forbidden, *sourceRule.MinLOS, *sourceRule.MaxLOS, *sourceRule.MinAdvBooking = false, 0, 0, 0
	key := BookingRuleKey{Date: "2026-11-25", RoomTypeID: "4347", RateID: "128299"}
	match, found := index.Lookup(key)
	if !found || !reflect.DeepEqual(match.Rules, []bookingRule{expected}) {
		t.Fatal("source mutation changed index", match)
	}
	match.Rules[0].RoomTypes[0], match.Rules[0].Code = "mutated", "mutated"
	*match.Rules[0].Forbidden, *match.Rules[0].MinLOS, *match.Rules[0].MaxLOS, *match.Rules[0].MinAdvBooking = false, 99, 99, 99
	match.Rules = append(match.Rules, bookingRule{})
	for _, category := range []string{"4347", "4350"} {
		key.RoomTypeID = category
		got, found := index.Lookup(key)
		if !found || !reflect.DeepEqual(got.Rules, []bookingRule{expected}) {
			t.Fatal("lookup mutation changed index or other category", got)
		}
	}
}
