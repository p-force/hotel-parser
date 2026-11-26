package travelline

import (
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBookingRulesURL(t *testing.T) {
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 1, 24, 0, 0, 0, 0, time.UTC)
	manyRates := make([]string, 12)
	for i := range manyRates {
		manyRates[i] = strconv.Itoa(128299 + i)
	}
	for _, tc := range []struct {
		name, origin string
		rates        []string
	}{
		{"provided parameters", PublicOrigin, []string{"128299", "128301"}},
		{"single rate", PublicOrigin, []string{"128299"}},
		{"input order retained", PublicOrigin, []string{"128301", "128299"}},
		{"two digit indexes", PublicOrigin, manyRates},
		{"configured origin", "https://travelline.example", []string{"128299"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Cached publicRates must not be substituted for explicitly supplied IDs.
			client, err := NewClient(tc.origin, nil, []string{"999"})
			if err != nil {
				t.Fatal(err)
			}
			before := append([]string(nil), tc.rates...)
			got, err := client.BookingRulesURL("585", start, end, tc.rates)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(got.String())
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Scheme+"://"+parsed.Host != tc.origin || parsed.Path != "/ApiWebDistribution/AvailabilityCalendar/rate_plan_booking_rules" || parsed.Fragment != "" || parsed.User != nil {
				t.Fatalf("unexpected endpoint: %s", got)
			}
			want := url.Values{"hotel": {"585"}, "start_date": {"2026-11-25"}, "end_date": {"2027-01-24"}, "audience": {"BookingForm"}}
			for i, rate := range tc.rates {
				want["rate_plans["+strconv.Itoa(i)+"].code"] = []string{rate}
			}
			if !reflect.DeepEqual(parsed.Query(), want) {
				t.Fatalf("query mismatch: got %v, want %v", parsed.Query(), want)
			}
			if !strings.Contains(parsed.RawQuery, "rate_plans%5B0%5D.code=") {
				t.Fatal("array parameter name is not URL encoded", parsed.RawQuery)
			}
			if !reflect.DeepEqual(tc.rates, before) || !reflect.DeepEqual(client.publicRates, []string{"999"}) {
				t.Fatal("builder changed input rates or client policy")
			}
		})
	}
}

func TestBookingRulesURLPreservesCalendarDates(t *testing.T) {
	client, err := NewClient(PublicOrigin, nil, []string{"999"})
	if err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("UTC+3", 3*60*60)
	start := time.Date(2026, 11, 25, 0, 30, 0, 0, zone)
	end := time.Date(2026, 11, 26, 0, 30, 0, 0, zone)
	got, err := client.BookingRulesURL("585", start, end, []string{"128299"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Query().Get("start_date") != "2026-11-25" || got.Query().Get("end_date") != "2026-11-26" {
		t.Fatal("calendar dates shifted to UTC", got)
	}
}

func TestBookingRulesURLRejectsInvalidInput(t *testing.T) {
	client, err := NewClient(PublicOrigin, nil, []string{"999"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	for _, tc := range []struct {
		name, hotel string
		start, end  time.Time
		rates       []string
	}{
		{"empty hotel", "", start, end, []string{"128299"}},
		{"invalid hotel", "585&hotel=1", start, end, []string{"128299"}},
		{"whitespace hotel", " 585", start, end, []string{"128299"}},
		{"long hotel", strings.Repeat("1", 17), start, end, []string{"128299"}},
		{"nil rates", "585", start, end, nil},
		{"empty rates", "585", start, end, []string{}},
		{"empty rate", "585", start, end, []string{""}},
		{"whitespace rate", "585", start, end, []string{" "}},
		{"invalid second rate", "585", start, end, []string{"128299", "1&audience=other"}},
		{"long rate", "585", start, end, []string{strings.Repeat("1", 17)}},
		{"missing start", "585", time.Time{}, end, []string{"128299"}},
		{"missing end", "585", start, time.Time{}, []string{"128299"}},
		{"equal dates", "585", start, start, []string{"128299"}},
		{"same calendar day", "585", start, start.Add(time.Hour), []string{"128299"}},
		{"reversed dates", "585", end, start, []string{"128299"}},
		{"unsupported date year", "585", start, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), []string{"128299"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.BookingRulesURL(tc.hotel, tc.start, tc.end, tc.rates)
			if err == nil || got != nil {
				t.Fatalf("invalid parameters accepted: url=%v err=%v", got, err)
			}
		})
	}
	for _, uninitialized := range []*Client{nil, {}} {
		if got, err := uninitialized.BookingRulesURL("585", start, end, []string{"128299"}); err == nil || got != nil {
			t.Fatal("uninitialized client accepted")
		}
	}
}
