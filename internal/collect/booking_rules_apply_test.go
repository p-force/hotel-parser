package collect

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

func bookingApplyFixture() (storage.Scenario, storage.AttemptInput) {
	date := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	scenario := storage.Scenario{Source: "travelline", CheckIn: date, CheckOut: date.AddDate(0, 0, 1), LOS: 1}
	attempt := storage.AttemptInput{
		JobID: "job", Number: 1, StartedAt: date, FinishedAt: date.Add(time.Second), DurationMS: 1000,
		HTTPStatus: 200, Outcome: storage.OutcomePartial,
		SelectionPolicy: &storage.SelectionPolicy{Mode: "allowlist", RateIDs: []string{"128299", "128301"}},
		Warnings:        []storage.Warning{{Code: "category_availability_unknown", Context: "4347"}},
		Observations: []storage.Observation{{
			CategoryID: "4347", CategoryName: "Standard", StockStatus: storage.StockUnknown,
			Warnings: []storage.Warning{{Code: "category_availability_unknown", Context: "4347"}},
			Variants: []storage.Variant{{ID: "v1", Adults: 1}},
		}},
	}
	return scenario, attempt
}

func bookingApplyIndex(categories ...string) travelline.BookingRulesIndex {
	response := travelline.BookingRulesResponse{}
	for _, id := range categories {
		for _, rate := range []string{"128299", "128301"} {
			response.BookingRules = append(response.BookingRules, travelline.BookingRule{
				Date: "2026-11-25", RoomTypes: []string{id}, Code: rate, Forbidden: bookingStorageField(true),
			})
		}
	}
	return response.Index()
}

func TestApplyTravelLineBookingRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*storage.Scenario, *storage.AttemptInput, *travelline.BookingRulesIndex, *[]string)
		want   int
	}{
		{name: "all rates forbidden", want: 1},
		{name: "identical duplicate rule", want: 1, change: func(_ *storage.Scenario, _ *storage.AttemptInput, index *travelline.BookingRulesIndex, _ *[]string) {
			response := decodeRulesForStorage(t, `{"booking_rules":[
				{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
				{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
				{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":true}]}`)
			*index = response.Index()
		}},
		{name: "partially forbidden rates", change: func(_ *storage.Scenario, _ *storage.AttemptInput, index *travelline.BookingRulesIndex, _ *[]string) {
			*index = decodeRulesForStorage(t, `{"booking_rules":[
				{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
				{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":false}]}`).Index()
		}},
		{name: "missing rate rule", change: func(_ *storage.Scenario, _ *storage.AttemptInput, index *travelline.BookingRulesIndex, _ *[]string) {
			*index = decodeRulesForStorage(t, `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true}]}`).Index()
		}},
		{name: "conflicting rules", change: func(_ *storage.Scenario, _ *storage.AttemptInput, index *travelline.BookingRulesIndex, _ *[]string) {
			*index = decodeRulesForStorage(t, `{"booking_rules":[
				{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true},
				{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":false},
				{"room_types":["4347"],"code":"128301","date":"2026-11-25","forbidden":true}]}`).Index()
		}},
		{name: "empty index", change: func(_ *storage.Scenario, _ *storage.AttemptInput, index *travelline.BookingRulesIndex, _ *[]string) {
			*index = travelline.BookingRulesIndex{}
		}},
		{name: "empty rate policy", change: func(_ *storage.Scenario, _ *storage.AttemptInput, _ *travelline.BookingRulesIndex, rates *[]string) {
			*rates = nil
		}},
		{name: "different check-in", change: func(s *storage.Scenario, _ *storage.AttemptInput, _ *travelline.BookingRulesIndex, _ *[]string) {
			s.CheckIn = s.CheckIn.AddDate(0, 0, 1)
		}},
		{name: "LOS 2", want: 1, change: func(s *storage.Scenario, _ *storage.AttemptInput, _ *travelline.BookingRulesIndex, _ *[]string) {
			s.LOS = 2
			s.CheckOut = s.CheckIn.AddDate(0, 0, 2)
		}},
		{name: "LOS 3", want: 1, change: func(s *storage.Scenario, _ *storage.AttemptInput, _ *travelline.BookingRulesIndex, _ *[]string) {
			s.LOS = 3
			s.CheckOut = s.CheckIn.AddDate(0, 0, 3)
		}},
		{name: "Bnovo", change: func(s *storage.Scenario, _ *storage.AttemptInput, _ *travelline.BookingRulesIndex, _ *[]string) {
			s.Source = "bnovo"
		}},
		{name: "other source", change: func(s *storage.Scenario, _ *storage.AttemptInput, _ *travelline.BookingRulesIndex, _ *[]string) {
			s.Source = "other"
		}},
		{name: "HTTP failure", change: func(_ *storage.Scenario, a *storage.AttemptInput, _ *travelline.BookingRulesIndex, _ *[]string) {
			a.HTTPStatus, a.Outcome, a.Error = 500, storage.OutcomeFailed, &storage.AttemptError{Code: "http_error"}
		}},
		{name: "parse failure with HTTP 200", change: func(_ *storage.Scenario, a *storage.AttemptInput, _ *travelline.BookingRulesIndex, _ *[]string) {
			a.Outcome, a.Error = storage.OutcomeFailed, &storage.AttemptError{Code: "parse_error"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scenario, attempt := bookingApplyFixture()
			index, rates := bookingApplyIndex("4347"), []string{"128299", "128301"}
			if tc.change != nil {
				tc.change(&scenario, &attempt, &index, &rates)
			}
			// Независимая копия выявляет любые изменения вложенных данных.
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
			if got := ApplyTravelLineBookingRules(scenario, &attempt, index, rates); got != tc.want {
				t.Fatalf("confirmed=%d, want %d", got, tc.want)
			}
			if !reflect.DeepEqual(attempt, expected) {
				t.Fatalf("unexpected attempt changes: got %+v, want %+v", attempt, expected)
			}
			if got := ApplyTravelLineBookingRules(scenario, &attempt, index, rates); got != 0 || !reflect.DeepEqual(attempt, expected) {
				t.Fatal("repeat application was not idempotent", got)
			}
		})
	}
}

func TestApplyTravelLineBookingRulesMixedObservations(t *testing.T) {
	scenario, attempt := bookingApplyFixture()
	attempt.Observations = append(attempt.Observations,
		storage.Observation{CategoryID: "second", StockStatus: storage.StockUnknown},
		storage.Observation{CategoryID: "known", StockStatus: storage.StockKnown, Available: bookingStorageField(2)},
		storage.Observation{CategoryID: "known_zero", StockStatus: storage.StockKnown, Available: bookingStorageField(0)},
		storage.Observation{CategoryID: "conflict", StockStatus: storage.StockConflict},
		storage.Observation{CategoryID: "offers", StockStatus: storage.StockUnknown, HasOffers: true, Offers: []storage.Offer{{VariantID: "v1", RateID: "128299", Price: bookingStorageField("5000.00"), Currency: bookingStorageField("RUB")}}},
		storage.Observation{CategoryID: "offers_without_flag", StockStatus: storage.StockUnknown, Offers: []storage.Offer{{RateID: "128299"}}},
		storage.Observation{CategoryID: "confirmed", StockStatus: storage.StockUnknown, ConfirmedUnavailable: true},
		storage.Observation{CategoryID: "reason", StockStatus: storage.StockUnknown, ConfirmedUnavailable: true, UnavailabilityReason: bookingStorageField(storage.UnavailabilityReasonBookingForbidden)},
		storage.Observation{CategoryID: "no_rule", StockStatus: storage.StockUnknown},
	)
	encoded, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	var expected storage.AttemptInput
	if err := json.Unmarshal(encoded, &expected); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		expected.Observations[i].ConfirmedUnavailable = true
		expected.Observations[i].UnavailabilityReason = bookingStorageField(storage.UnavailabilityReasonBookingForbidden)
	}
	index := bookingApplyIndex("4347", "second", "known", "known_zero", "conflict", "offers", "offers_without_flag", "confirmed", "reason")
	rates := []string{"128299", "128301"}
	if got := ApplyTravelLineBookingRules(scenario, &attempt, index, rates); got != 2 || !reflect.DeepEqual(attempt, expected) {
		t.Fatalf("mixed observations: confirmed=%d, got %+v, want %+v", got, attempt, expected)
	}
	if attempt.Observations[0].UnavailabilityReason == attempt.Observations[1].UnavailabilityReason {
		t.Fatal("observations share mutable reason pointer")
	}
	if got := ApplyTravelLineBookingRules(scenario, &attempt, index, rates); got != 0 || !reflect.DeepEqual(attempt, expected) {
		t.Fatal("mixed observations changed on repeated application", got)
	}
}

func TestApplyTravelLineBookingRulesNilAttempt(t *testing.T) {
	scenario, _ := bookingApplyFixture()
	if got := ApplyTravelLineBookingRules(scenario, nil, bookingApplyIndex("4347"), []string{"128299", "128301"}); got != 0 {
		t.Fatal("nil attempt was changed", got)
	}
}
