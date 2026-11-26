package collect

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

func decodeRulesForStorage(t *testing.T, body string) *travelline.BookingRulesResponse {
	t.Helper()
	var response travelline.BookingRulesResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	return &response
}

func bookingStorageField[T any](value T) *T { return &value }

func TestBookingRulesToStorage(t *testing.T) {
	date := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body string
		want       []storage.BookingRuleInput
	}{
		{
			name: "multiple categories and all restrictions",
			body: `{"booking_rules":[{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":5,"max_los":1,"min_adv_booking":3}]}`,
			want: []storage.BookingRuleInput{
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(true), MinLOS: bookingStorageField(5), MaxLOS: bookingStorageField(1), MinAdvBooking: bookingStorageField(3)},
				{Date: date, RoomTypeID: bookingStorageField("4350"), RateID: "128299", Forbidden: bookingStorageField(true), MinLOS: bookingStorageField(5), MaxLOS: bookingStorageField(1), MinAdvBooking: bookingStorageField(3)},
			},
		},
		{
			name: "room types absent empty or null each retain one row",
			body: `{"booking_rules":[{"code":"128299","date":"2026-11-25","forbidden":true},{"room_types":[],"code":"128301","date":"2026-11-25","min_los":2},{"room_types":null,"code":"128311","date":"2026-11-25"}]}`,
			want: []storage.BookingRuleInput{
				{Date: date, RateID: "128299", Forbidden: bookingStorageField(true)},
				{Date: date, RateID: "128301", MinLOS: bookingStorageField(2)},
				{Date: date, RateID: "128311"},
			},
		},
		{
			name: "NULL false zero and negative values",
			body: `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false,"min_los":0,"max_los":0,"min_adv_booking":0},{"room_types":[""],"code":"128301","date":"2026-11-25","forbidden":null,"min_los":null,"max_los":null,"min_adv_booking":null},{"room_types":["4350"],"code":"128311","date":"2026-11-25","min_los":-1}]}`,
			want: []storage.BookingRuleInput{
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(false), MinLOS: bookingStorageField(0), MaxLOS: bookingStorageField(0), MinAdvBooking: bookingStorageField(0)},
				{Date: date, RoomTypeID: bookingStorageField(""), RateID: "128301"},
				{Date: date, RoomTypeID: bookingStorageField("4350"), RateID: "128311", MinLOS: bookingStorageField(-1)},
			},
		},
		{
			name: "identical duplicate source records",
			body: `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true}]}`,
			want: []storage.BookingRuleInput{
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(true)},
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(true)},
			},
		},
		{
			name: "duplicate categories inside source record",
			body: `{"booking_rules":[{"room_types":["4347","4347"],"code":"128299","date":"2026-11-25","forbidden":false}]}`,
			want: []storage.BookingRuleInput{
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(false)},
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(false)},
			},
		},
		{
			name: "contradictory rules not merged",
			body: `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":2},{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false,"min_los":3}]}`,
			want: []storage.BookingRuleInput{
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(true), MinLOS: bookingStorageField(2)},
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(false), MinLOS: bookingStorageField(3)},
			},
		},
		{
			name: "source order dates and rate IDs retained",
			body: `{"booking_rules":[{"room_types":["4350","4347"],"code":"128301","date":"2026-11-26"},{"room_types":["4347"],"code":"128299","date":"2026-11-25"}]}`,
			want: []storage.BookingRuleInput{
				{Date: date.AddDate(0, 0, 1), RoomTypeID: bookingStorageField("4350"), RateID: "128301"},
				{Date: date.AddDate(0, 0, 1), RoomTypeID: bookingStorageField("4347"), RateID: "128301"},
				{Date: date, RoomTypeID: bookingStorageField("4347"), RateID: "128299"},
			},
		},
		{
			name: "successful empty array",
			body: `{"booking_rules":[]}`,
			want: []storage.BookingRuleInput{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := decodeRulesForStorage(t, tc.body)
			before, _ := json.Marshal(response)
			got, err := BookingRulesToStorage(response)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("conversion got %+v, want %+v, err=%v", got, tc.want, err)
			}
			after, _ := json.Marshal(response)
			if string(before) != string(after) {
				t.Fatal("converter changed source response")
			}
		})
	}
}

func TestBookingRulesToStorageNoPartialResultOnInvalidResponse(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing rules", `{}`},
		{"null rules", `{"booking_rules":null}`},
		{"invalid date after valid row", `{"booking_rules":[{"room_types":["4347"],"date":"2026-11-25","code":"128299"},{"room_types":["4350"],"date":"2026-02-31","code":"128299"}]}`},
		{"missing date", `{"booking_rules":[{"code":"128299"}]}`},
		{"unexpected date format", `{"booking_rules":[{"date":"25.11.2026","code":"128299"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BookingRulesToStorage(decodeRulesForStorage(t, tc.body))
			if err == nil || got != nil {
				t.Fatal("invalid response became a successful/partial set", got, err)
			}
		})
	}
	if got, err := BookingRulesToStorage(nil); err == nil || got != nil {
		t.Fatal("missing response became successful empty data")
	}
}

func TestBookingRulesToStorageDoesNotAliasFields(t *testing.T) {
	response := decodeRulesForStorage(t, `{"booking_rules":[{"room_types":["4347","4350"],"code":"128299","date":"2026-11-25","forbidden":true,"min_los":2,"max_los":7,"min_adv_booking":3}]}`)
	got, err := BookingRulesToStorage(response)
	if err != nil {
		t.Fatal(err)
	}
	// Independent expected data, not built using the converter's copying helper.
	want := storage.BookingRuleInput{Date: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC), RoomTypeID: bookingStorageField("4350"), RateID: "128299", Forbidden: bookingStorageField(true), MinLOS: bookingStorageField(2), MaxLOS: bookingStorageField(7), MinAdvBooking: bookingStorageField(3)}
	response.BookingRules[0].RoomTypes[0] = "changed"
	*response.BookingRules[0].Forbidden = false
	*response.BookingRules[0].MinLOS = 99
	*response.BookingRules[0].MaxLOS = 99
	*response.BookingRules[0].MinAdvBooking = 99
	if *got[0].RoomTypeID != "4347" || !*got[0].Forbidden || *got[0].MinLOS != 2 || *got[0].MaxLOS != 7 || *got[0].MinAdvBooking != 3 {
		t.Fatal("source mutation changed converted data")
	}
	*got[0].RoomTypeID = "mutated"
	*got[0].Forbidden = false
	*got[0].MinLOS = 10
	*got[0].MaxLOS = 10
	*got[0].MinAdvBooking = 10
	if !reflect.DeepEqual(got[1], want) {
		t.Fatal("expanded categories share mutable fields", got[1])
	}
}
