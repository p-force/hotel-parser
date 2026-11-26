package travelline

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBookingRulesResponseUnmarshal(t *testing.T) {
	yes, no := true, false
	zero, two, seven, three := 0, 2, 7, 3
	for _, tc := range []struct {
		name string
		body string
		want bookingRulesResponse
	}{
		{
			name: "full response",
			body: `{"booking_rules":[{"room_types":["4347","4350"],"forbidden":true,"code":"128299","date":"2026-11-25","min_los":2,"max_los":7,"min_adv_booking":3}]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{{RoomTypes: []string{"4347", "4350"}, Code: "128299", Date: "2026-11-25", Forbidden: &yes, MinLOS: &two, MaxLOS: &seven, MinAdvBooking: &three}}},
		},
		{
			name: "provided response with optional restrictions omitted",
			body: `{"booking_rules":[{"room_types":["4347","4350"],"forbidden":true,"code":"128299","date":"2026-11-25"},{"room_types":["4348"],"code":"128299","date":"2026-11-26"}]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{
				{RoomTypes: []string{"4347", "4350"}, Code: "128299", Date: "2026-11-25", Forbidden: &yes},
				{RoomTypes: []string{"4348"}, Code: "128299", Date: "2026-11-26"},
			}},
		},
		{
			name: "room types and restrictions absent",
			body: `{"booking_rules":[{"code":"128299","date":"2026-11-26"}]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{{Code: "128299", Date: "2026-11-26"}}},
		},
		{
			name: "explicit false zero and empty room types",
			body: `{"booking_rules":[{"room_types":[],"forbidden":false,"min_los":0,"max_los":0,"min_adv_booking":0}]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{{RoomTypes: []string{}, Forbidden: &no, MinLOS: &zero, MaxLOS: &zero, MinAdvBooking: &zero}}},
		},
		{
			name: "individual restriction without other optional fields",
			body: `{"booking_rules":[{"min_los":2},{"max_los":7},{"min_adv_booking":3}]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{{MinLOS: &two}, {MaxLOS: &seven}, {MinAdvBooking: &three}}},
		},
		{
			name: "explicit null optional fields",
			body: `{"booking_rules":[{"room_types":null,"forbidden":null,"min_los":null,"max_los":null,"min_adv_booking":null}]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{{}}},
		},
		{
			name: "empty rules array",
			body: `{"booking_rules":[]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{}},
		},
		{
			name: "rules field absent",
			body: `{}`,
			want: bookingRulesResponse{},
		},
		{
			name: "unknown response and rule fields",
			body: `{"future_field":{"nested":[1,2]},"booking_rules":[{"room_types":["4348"],"code":"128299","date":"2026-11-26","min_los":2,"unknown_restriction":true,"future_details":{"value":42}}]}`,
			want: bookingRulesResponse{BookingRules: []bookingRule{{RoomTypes: []string{"4348"}, Code: "128299", Date: "2026-11-26", MinLOS: &two}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got bookingRulesResponse
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded response mismatch:\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}
