package collect

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

func TestBookingRulesIndexFromStorageRoundTrip(t *testing.T) {
	date := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body string
		count      int
		conflict   bool
		forbidden  bool
	}{
		{"multiple categories dates and rates", `{"booking_rules":[
			{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":5,"max_los":1,"min_adv_booking":3},
			{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":true},
			{"room_types":["4350"],"code":"128301","date":"2026-11-26","forbidden":false}]}`, 1, false, true},
		{"identical duplicates", `{"booking_rules":[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":true}]}`, 2, false, true},
		{"contradictory forbidden", `{"booking_rules":[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false}]}`, 2, true, false},
		{"NULL differs from false and zero", `{"booking_rules":[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25"},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false,"min_los":0,"max_los":0,"min_adv_booking":0}]}`, 2, true, false},
		{"conflicting restrictions with forbidden true", `{"booking_rules":[
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":0},
			{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true}]}`, 2, true, false},
		{"rule without restrictions", `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25"}]}`, 1, false, false},
		{"missing empty and null room types", `{"booking_rules":[
			{"code":"128299","date":"2026-11-25","forbidden":true},
			{"room_types":[],"code":"128301","date":"2026-11-25","forbidden":true},
			{"room_types":null,"code":"128299","date":"2026-11-25","forbidden":true}]}`, 0, false, false},
		{"empty set", `{"booking_rules":[]}`, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := decodeRulesForStorage(t, tc.body)
			inputs, err := BookingRulesToStorage(response)
			if err != nil {
				t.Fatal(err)
			}
			saved := storage.RunBookingRules{Rules: make([]storage.BookingRule, len(inputs))}
			for i, input := range inputs {
				saved.Rules[i] = storage.BookingRule{BookingRuleInput: input}
			}
			before, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			original, restored := response.Index(), BookingRulesIndexFromStorage(saved)
			after, err := json.Marshal(saved)
			if err != nil || string(before) != string(after) {
				t.Fatal("restoring index changed saved rules", err)
			}
			for _, day := range []string{"2026-11-25", "2026-11-26", "2026-11-27"} {
				for _, room := range []string{"4347", "4350", "missing", ""} {
					for _, rate := range []string{"128299", "128301", "missing"} {
						key := travelline.BookingRuleKey{Date: day, RoomTypeID: room, RateID: rate}
						want, wantFound := original.Lookup(key)
						got, found := restored.Lookup(key)
						// Storage хранит отдельную запись на категорию, а не исходную
						// группировку room_types. Сравниваем правила данного ключа.
						for i := range want.Rules {
							want.Rules[i].RoomTypes = []string{room}
						}
						if found != wantFound || !reflect.DeepEqual(got, want) {
							t.Fatalf("Lookup(%+v): got %+v/%v, want %+v/%v", key, got, found, want, wantFound)
						}
						checkIn, err := time.Parse("2006-01-02", day)
						if err != nil {
							t.Fatal(err)
						}
						for _, rates := range [][]string{nil, {rate}, {"128299", "128301"}} {
							for _, los := range []int{1, 2, 3} {
								want := travelline.ConfirmCategoryBookingForbidden(original, checkIn, room, rates, los)
								if got := travelline.ConfirmCategoryBookingForbidden(restored, checkIn, room, rates, los); got != want {
									t.Fatalf("forbidden differs for %s/%s/%v LOS=%d: got %v, want %v", day, room, rates, los, got, want)
								}
							}
						}
					}
				}
			}
			match, found := restored.Lookup(travelline.BookingRuleKey{Date: "2026-11-25", RoomTypeID: "4347", RateID: "128299"})
			if found != (tc.count > 0) || len(match.Rules) != tc.count || match.Conflict != tc.conflict {
				t.Fatalf("unexpected match: %+v, found=%v", match, found)
			}
			if got := travelline.ConfirmCategoryBookingForbidden(restored, date, "4347", []string{"128299", "128301"}, 1); got != tc.forbidden {
				t.Fatalf("forbidden=%v, want %v", got, tc.forbidden)
			}
		})
	}
}

func TestBookingRulesIndexFromStorageOwnsFields(t *testing.T) {
	date := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	saved := storage.RunBookingRules{Rules: []storage.BookingRule{{BookingRuleInput: storage.BookingRuleInput{
		Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299",
		Forbidden: bookingStorageField(false), MinLOS: bookingStorageField(0),
		MaxLOS: bookingStorageField(0), MinAdvBooking: bookingStorageField(0),
	}}}}
	index := BookingRulesIndexFromStorage(saved)
	key := travelline.BookingRuleKey{Date: "2026-11-25", RoomTypeID: "4347", RateID: "128299"}
	want := travelline.BookingRuleMatch{Rules: []travelline.BookingRule{{
		RoomTypes: []string{"4347"}, Code: "128299", Date: "2026-11-25",
		Forbidden: bookingStorageField(false), MinLOS: bookingStorageField(0),
		MaxLOS: bookingStorageField(0), MinAdvBooking: bookingStorageField(0),
	}}}
	if got, found := index.Lookup(key); !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v/%v, want %+v", got, found, want)
	}
	// Lookup возвращает копию: её изменение не меняет ни storage, ни индекс.
	match, _ := index.Lookup(key)
	*match.Rules[0].Forbidden = true
	*match.Rules[0].MinLOS = 99
	if *saved.Rules[0].Forbidden || *saved.Rules[0].MinLOS != 0 {
		t.Fatal("lookup mutation changed storage")
	}
	*saved.Rules[0].RoomTypeID = "changed"
	*saved.Rules[0].Forbidden = true
	*saved.Rules[0].MinLOS = 99
	*saved.Rules[0].MaxLOS = 99
	*saved.Rules[0].MinAdvBooking = 99
	if got, found := index.Lookup(key); !found || !reflect.DeepEqual(got, want) {
		t.Fatal("storage mutation changed index", got)
	}
}

func TestBookingRulesIndexFromStorageEmpty(t *testing.T) {
	for _, saved := range []storage.RunBookingRules{{}, {Rules: []storage.BookingRule{}}} {
		index := BookingRulesIndexFromStorage(saved)
		key := travelline.BookingRuleKey{Date: "2026-11-25", RoomTypeID: "4347", RateID: "128299"}
		if match, found := index.Lookup(key); found || len(match.Rules) != 0 || match.Conflict {
			t.Fatal("empty saved set produced a rule", match)
		}
	}
}
