package collect

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

func TestNeedsBookingRulesMultiLOSLimits(t *testing.T) {
	for _, los := range []int{2, 3} {
		for _, tc := range []struct {
			name   string
			change func(*storage.Scenario, *storage.AttemptInput)
			want   bool
		}{
			{"partial_candidate", func(*storage.Scenario, *storage.AttemptInput) {}, true},
			{"success_candidate", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = storage.OutcomeSuccess }, true},
			{"no_offers_candidate", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = storage.OutcomeNoOffers }, true},
			{"bnovo", func(s *storage.Scenario, _ *storage.AttemptInput) { s.Source = "bnovo" }, false},
			{"other_source", func(s *storage.Scenario, _ *storage.AttemptInput) { s.Source = "other" }, false},
			{"HTTP403", func(_ *storage.Scenario, a *storage.AttemptInput) { a.HTTPStatus = 403 }, false},
			{"HTTP429", func(_ *storage.Scenario, a *storage.AttemptInput) { a.HTTPStatus = 429 }, false},
			{"HTTP500", func(_ *storage.Scenario, a *storage.AttemptInput) { a.HTTPStatus = 500 }, false},
			{"HTTP_not_started", func(_ *storage.Scenario, a *storage.AttemptInput) { a.HTTPStatus = 0 }, false},
			{"parse_error", func(_ *storage.Scenario, a *storage.AttemptInput) {
				a.Error = &storage.AttemptError{Code: "parse_error"}
			}, false},
			{"failed_outcome", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = storage.OutcomeFailed }, false},
			{"unknown_outcome", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = "" }, false},
			{"known_stock", func(_ *storage.Scenario, a *storage.AttemptInput) {
				a.Observations[0].StockStatus = storage.StockKnown
				a.Observations[0].Available = bookingStorageField(0)
			}, false},
			{"conflict", func(_ *storage.Scenario, a *storage.AttemptInput) {
				a.Observations[0].StockStatus = storage.StockConflict
			}, false},
			{"non_NULL_available", func(_ *storage.Scenario, a *storage.AttemptInput) {
				a.Observations[0].Available = bookingStorageField(0)
			}, false},
			{"has_offers", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations[0].HasOffers = true }, false},
			{"actual_offers", func(_ *storage.Scenario, a *storage.AttemptInput) {
				a.Observations[0].Offers = []storage.Offer{{RateID: "38564"}}
			}, false},
			{"already_confirmed", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations[0].ConfirmedUnavailable = true }, false},
			{"no_observations", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations = nil }, false},
		} {
			name := "LOS" + strconv.Itoa(los) + "/" + tc.name
			t.Run(name, func(t *testing.T) {
				scenario, attempt := bookingApplyFixture()
				scenario.LOS = los
				scenario.CheckOut = scenario.CheckIn.AddDate(0, 0, los)
				tc.change(&scenario, &attempt)
				before, err := json.Marshal([]any{scenario, attempt})
				if err != nil {
					t.Fatal(err)
				}
				if got := NeedsTravelLineBookingRules(scenario, attempt); got != tc.want {
					t.Errorf("eligible=%v, want %v", got, tc.want)
				}
				after, err := json.Marshal([]any{scenario, attempt})
				if err != nil || string(before) != string(after) {
					t.Fatal("predicate mutated input", err)
				}
			})
		}
	}
}

func TestApplyBookingRulesMultiLOSOnlyEvidenceFields(t *testing.T) {
	body, err := os.ReadFile("../source/travelline/testdata/sovietsky-booking-rules-2026-12.json")
	if err != nil {
		t.Fatal(err)
	}
	index := decodeRulesForStorage(t, string(body)).Index()
	for _, los := range []int{1, 2, 3} {
		for _, tc := range []struct {
			name   string
			change func(*storage.AttemptInput, *travelline.BookingRulesIndex)
			want   int
		}{
			{"confirmed", func(*storage.AttemptInput, *travelline.BookingRulesIndex) {}, 1},
			{"offers_override_rules", func(a *storage.AttemptInput, _ *travelline.BookingRulesIndex) {
				a.Observations[0].HasOffers = true
				a.Observations[0].Offers = []storage.Offer{{VariantID: "v1", RateID: "38564", Price: bookingStorageField("100.00"), Currency: bookingStorageField("RUB")}}
			}, 0},
			{"unknown_no_evidence", func(_ *storage.AttemptInput, i *travelline.BookingRulesIndex) { *i = travelline.BookingRulesIndex{} }, 0},
			{"one_rate_only", func(_ *storage.AttemptInput, i *travelline.BookingRulesIndex) {
				*i = decodeRulesForStorage(t, `{"booking_rules":[{"room_types":["29344"],"code":"38564","date":"2026-12-05","forbidden":true}]}`).Index()
			}, 0},
			{"conflicting_evidence", func(_ *storage.AttemptInput, i *travelline.BookingRulesIndex) {
				*i = decodeRulesForStorage(t, `{"booking_rules":[{"room_types":["29344"],"code":"38564","date":"2026-12-05","forbidden":true},{"room_types":["29344"],"code":"38564","date":"2026-12-05","forbidden":false},{"room_types":["29344"],"code":"49218","date":"2026-12-05","forbidden":true}]}`).Index()
			}, 0},
			{"known_zero", func(a *storage.AttemptInput, _ *travelline.BookingRulesIndex) {
				a.Observations[0].StockStatus = storage.StockKnown
				a.Observations[0].Available = bookingStorageField(0)
			}, 0},
			{"conflict_stock", func(a *storage.AttemptInput, _ *travelline.BookingRulesIndex) {
				a.Observations[0].StockStatus = storage.StockConflict
			}, 0},
			{"already_unavailable", func(a *storage.AttemptInput, _ *travelline.BookingRulesIndex) {
				a.Observations[0].ConfirmedUnavailable = true
			}, 0},
		} {
			t.Run("LOS"+strconv.Itoa(los)+"/"+tc.name, func(t *testing.T) {
				scenario, attempt := bookingApplyFixture()
				// LOS1: 05–06; LOS2: 04–06; LOS3: 03–06. Все включают 05 декабря.
				scenario.CheckIn = time.Date(2026, 12, 6-los, 0, 0, 0, 0, time.UTC)
				scenario.CheckOut = scenario.CheckIn.AddDate(0, 0, los)
				scenario.LOS = los
				attempt.Observations[0].CategoryID = "29344"
				attempt.Observations[0].CategoryName = "Апартаменты Яръ"
				attempt.Warnings[0].Context = "29344"
				attempt.Observations[0].Warnings[0].Context = "29344"
				attempt.SelectionPolicy = &storage.SelectionPolicy{Mode: "allowlist", RateIDs: []string{"38564", "49218"}}
				attempt.Observations = append(attempt.Observations, storage.Observation{CategoryID: "other", StockStatus: storage.StockUnknown})
				current := index
				tc.change(&attempt, &current)
				encoded, err := json.Marshal(attempt)
				if err != nil {
					t.Fatal(err)
				}
				var expected storage.AttemptInput
				if err := json.Unmarshal(encoded, &expected); err != nil {
					t.Fatal(err)
				}
				if tc.want == 1 {
					expected.Observations[0].ConfirmedUnavailable = true
					expected.Observations[0].UnavailabilityReason = bookingStorageField(storage.UnavailabilityReasonBookingForbidden)
				}
				rates := []string{"38564", "49218"}
				if got := ApplyTravelLineBookingRules(scenario, &attempt, current, rates); got != tc.want {
					t.Errorf("confirmed=%d, want %d", got, tc.want)
				}
				if !reflect.DeepEqual(attempt, expected) {
					t.Error("attempt differs beyond the two evidence fields, or confirmation is missing")
				}
				if got := ApplyTravelLineBookingRules(scenario, &attempt, current, rates); got != 0 {
					t.Error("repeat application is not idempotent", got)
				}
			})
		}
	}
}
