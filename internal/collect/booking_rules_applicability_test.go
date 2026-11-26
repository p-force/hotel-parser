package collect

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
)

func TestCheckRunBookingRulesApplicability(t *testing.T) {
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 7)
	const runID = "a7d802c7-016a-464b-b72c-c835d1199ef2"
	for _, tc := range []struct {
		name   string
		change func(*storage.RunBookingRules, *string, *time.Time, *time.Time, *[]string)
		reason string
	}{
		{name: "exact match"},
		{name: "wider saved range", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.StartDate = start.AddDate(0, 0, -1)
			s.EndDate = end.AddDate(0, 0, 1)
		}},
		{name: "start boundary missing", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.StartDate = start.AddDate(0, 0, 1)
		}, reason: "date_range_not_covered"},
		{name: "end boundary missing", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.EndDate = end.AddDate(0, 0, -1)
		}, reason: "date_range_not_covered"},
		{name: "disjoint range", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.StartDate, s.EndDate = end, end.AddDate(0, 0, 1)
		}, reason: "date_range_not_covered"},
		{name: "rates reordered", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.RequestedRateIDs = []string{"128301", "128299"}
		}},
		{name: "duplicate rate IDs are set members", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			s.RequestedRateIDs = []string{"128301", "128299", "128301"}
			*rates = []string{"128299", "128299", "128301"}
		}},
		{name: "added current rate", change: func(_ *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			*rates = append(*rates, "128311")
		}, reason: "rate_policy_mismatch"},
		{name: "removed current rate", change: func(_ *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			*rates = (*rates)[:1]
		}, reason: "rate_policy_mismatch"},
		{name: "different rate with same count", change: func(_ *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			(*rates)[1] = "128311"
		}, reason: "rate_policy_mismatch"},
		{name: "empty current rates", change: func(_ *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			*rates = []string{}
		}, reason: "invalid_requested_rate_policy"},
		{name: "nil current rates", change: func(_ *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			*rates = nil
		}, reason: "invalid_requested_rate_policy"},
		{name: "empty saved rates", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.RequestedRateIDs = nil
		}, reason: "invalid_saved_rate_policy"},
		{name: "both rate lists empty", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			s.RequestedRateIDs, *rates = nil, nil
		}, reason: "invalid_requested_rate_policy"},
		{name: "blank current rate ID", change: func(_ *storage.RunBookingRules, _ *string, _, _ *time.Time, rates *[]string) {
			(*rates)[0] = " "
		}, reason: "invalid_requested_rate_policy"},
		{name: "blank saved rate ID", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.RequestedRateIDs[0] = ""
		}, reason: "invalid_saved_rate_policy"},
		{name: "run mismatch", change: func(_ *storage.RunBookingRules, id *string, _, _ *time.Time, _ *[]string) {
			*id = "another-run"
		}, reason: "run_id_mismatch"},
		{name: "missing current run", change: func(_ *storage.RunBookingRules, id *string, _, _ *time.Time, _ *[]string) {
			*id = ""
		}, reason: "invalid_run_id"},
		{name: "missing saved run", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.RunID = ""
		}, reason: "invalid_run_id"},
		{name: "successful empty rules", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.Rules = []storage.BookingRule{}
		}},
		{name: "nil rules do not affect metadata applicability", change: func(s *storage.RunBookingRules, _ *string, _, _ *time.Time, _ *[]string) {
			s.Rules = nil
		}},
		{name: "calendar dates ignore hours and UTC offset", change: func(s *storage.RunBookingRules, _ *string, from, to *time.Time, _ *[]string) {
			s.StartDate = start.Add(20 * time.Hour)
			*from = time.Date(2026, 11, 25, 0, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
			*to = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 0, 0, time.FixedZone("west", -5*60*60))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := storage.RunBookingRules{
				RunID: runID, StartDate: start, EndDate: end,
				RequestedRateIDs: []string{"128299", "128301"},
				Rules: []storage.BookingRule{{BookingRuleInput: storage.BookingRuleInput{
					Date: start, RateID: "128299", RoomTypeID: bookingStorageField("4347"), Forbidden: bookingStorageField(true),
				}}},
			}
			id, from, to, rates := runID, start, end, []string{"128299", "128301"}
			if tc.change != nil {
				tc.change(&saved, &id, &from, &to, &rates)
			}
			before, err := json.Marshal([]any{saved, id, from, to, rates})
			if err != nil {
				t.Fatal(err)
			}
			ok, reason := CheckRunBookingRulesApplicability(saved, id, from, to, rates)
			if ok != (tc.reason == "") || reason != tc.reason {
				t.Fatalf("got (%v, %q), want (%v, %q)", ok, reason, tc.reason == "", tc.reason)
			}
			after, err := json.Marshal([]any{saved, id, from, to, rates})
			if err != nil || string(before) != string(after) {
				t.Fatal("applicability check changed inputs", err)
			}
		})
	}
}

func TestCheckRunBookingRulesApplicabilityInvalidDates(t *testing.T) {
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 7)
	for _, tc := range []struct {
		name       string
		start, end time.Time
	}{
		{"zero start", time.Time{}, end},
		{"zero end", start, time.Time{}},
		{"reversed", end, start},
		{"equal", start, start},
		{"same calendar day different hours", start, start.Add(time.Hour)},
		{"year zero", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), end},
		{"negative year", time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC), end},
		{"year above 9999", start, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		for _, savedDates := range []bool{false, true} {
			name, reason := "requested/", "invalid_requested_date_range"
			if savedDates {
				name, reason = "saved/", "invalid_saved_date_range"
			}
			t.Run(name+tc.name, func(t *testing.T) {
				saved := storage.RunBookingRules{RunID: "run", StartDate: start, EndDate: end, RequestedRateIDs: []string{"128299"}}
				from, to := tc.start, tc.end
				if savedDates {
					saved.StartDate, saved.EndDate = tc.start, tc.end
					from, to = start, end
				}
				if ok, got := CheckRunBookingRulesApplicability(saved, "run", from, to, []string{"128299"}); ok || got != reason {
					t.Fatalf("got (%v, %q), want (false, %q)", ok, got, reason)
				}
			})
		}
	}
}
