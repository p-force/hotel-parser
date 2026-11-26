package collect

import (
	"encoding/json"
	"testing"

	"github.com/p-force/hotel-parser/internal/storage"
)

func TestNeedsTravelLineBookingRulesConditions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*storage.Scenario, *storage.AttemptInput)
		want   bool
	}{
		{"matching partial result", func(*storage.Scenario, *storage.AttemptInput) {}, true},
		{"matching successful result", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = storage.OutcomeSuccess }, true},
		{"no_offers outcome does not replace category flags", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = storage.OutcomeNoOffers }, true},
		{"Bnovo", func(s *storage.Scenario, _ *storage.AttemptInput) { s.Source = "bnovo" }, false},
		{"other source", func(s *storage.Scenario, _ *storage.AttemptInput) { s.Source = "other" }, false},
		{"missing source", func(s *storage.Scenario, _ *storage.AttemptInput) { s.Source = "" }, false},
		{"LOS 2", func(s *storage.Scenario, _ *storage.AttemptInput) { s.LOS = 2 }, false},
		{"LOS 3", func(s *storage.Scenario, _ *storage.AttemptInput) { s.LOS = 3 }, false},
		{"unset LOS", func(s *storage.Scenario, _ *storage.AttemptInput) { s.LOS = 0 }, false},
		{"known stock", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations[0].StockStatus = storage.StockKnown }, false},
		{"conflict stock", func(_ *storage.Scenario, a *storage.AttemptInput) {
			a.Observations[0].StockStatus = storage.StockConflict
		}, false},
		{"missing stock status", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations[0].StockStatus = "" }, false},
		{"available zero is not NULL", func(_ *storage.Scenario, a *storage.AttemptInput) {
			a.Observations[0].Available = bookingStorageField(0)
		}, false},
		{"available positive", func(_ *storage.Scenario, a *storage.AttemptInput) {
			a.Observations[0].Available = bookingStorageField(2)
		}, false},
		{"has offers with unknown quantity", func(_ *storage.Scenario, a *storage.AttemptInput) {
			a.Observations[0].HasOffers = true
			a.Observations[0].Offers = []storage.Offer{{RateID: "128299", Price: bookingStorageField("1000"), Currency: bookingStorageField("RUB"), Visible: true}}
		}, false},
		{"confirmed unavailable", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations[0].ConfirmedUnavailable = true }, false},
		{"offers despite unset flag", func(_ *storage.Scenario, a *storage.AttemptInput) {
			a.Observations[0].Offers = []storage.Offer{{RateID: "128299"}}
		}, false},
		{"HTTP error status", func(_ *storage.Scenario, a *storage.AttemptInput) { a.HTTPStatus = 500 }, false},
		{"HTTP throttled", func(_ *storage.Scenario, a *storage.AttemptInput) { a.HTTPStatus = 429 }, false},
		{"HTTP not performed", func(_ *storage.Scenario, a *storage.AttemptInput) { a.HTTPStatus = 0 }, false},
		{"HTTP error object", func(_ *storage.Scenario, a *storage.AttemptInput) {
			a.Error = &storage.AttemptError{Code: "http_error"}
		}, false},
		{"parse error with HTTP 200", func(_ *storage.Scenario, a *storage.AttemptInput) {
			a.Error = &storage.AttemptError{Code: "parse_error"}
		}, false},
		{"timeout", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Error = &storage.AttemptError{Code: "timeout"} }, false},
		{"failed outcome without error object", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = storage.OutcomeFailed }, false},
		{"unset outcome", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Outcome = "" }, false},
		{"nil observations", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations = nil }, false},
		{"empty observations", func(_ *storage.Scenario, a *storage.AttemptInput) { a.Observations = []storage.Observation{} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scenario := storage.Scenario{Source: "travelline", LOS: 1}
			attempt := storage.AttemptInput{HTTPStatus: 200, Outcome: storage.OutcomePartial, Observations: []storage.Observation{{CategoryID: "4347", StockStatus: storage.StockUnknown}}}
			tc.change(&scenario, &attempt)
			beforeScenario, _ := json.Marshal(scenario)
			beforeAttempt, _ := json.Marshal(attempt)
			if got := NeedsTravelLineBookingRules(scenario, attempt); got != tc.want {
				t.Fatalf("needs rules=%v, want %v", got, tc.want)
			}
			afterScenario, _ := json.Marshal(scenario)
			afterAttempt, _ := json.Marshal(attempt)
			if string(beforeScenario) != string(afterScenario) || string(beforeAttempt) != string(afterAttempt) {
				t.Fatal("predicate changed scenario or observation data")
			}
		})
	}
}

func TestNeedsTravelLineBookingRulesMixedObservations(t *testing.T) {
	scenario := storage.Scenario{Source: "travelline", LOS: 1}
	unsuitable := []storage.Observation{
		{CategoryID: "known", StockStatus: storage.StockKnown, Available: bookingStorageField(2)},
		{CategoryID: "conflict", StockStatus: storage.StockConflict},
		{CategoryID: "offers", StockStatus: storage.StockUnknown, HasOffers: true},
		{CategoryID: "confirmed", StockStatus: storage.StockUnknown, ConfirmedUnavailable: true},
	}
	candidate := storage.Observation{CategoryID: "candidate", StockStatus: storage.StockUnknown}
	for _, position := range []int{0, 2, len(unsuitable)} {
		observations := make([]storage.Observation, 0, len(unsuitable)+1)
		observations = append(observations, unsuitable[:position]...)
		observations = append(observations, candidate)
		observations = append(observations, unsuitable[position:]...)
		attempt := storage.AttemptInput{HTTPStatus: 200, Outcome: storage.OutcomePartial, Observations: observations}
		before, _ := json.Marshal(attempt)
		if !NeedsTravelLineBookingRules(scenario, attempt) {
			t.Fatal("candidate lost in mixed data", position)
		}
		after, _ := json.Marshal(attempt)
		if string(before) != string(after) {
			t.Fatal("mixed observations were modified")
		}
		attempt.Outcome = storage.OutcomeFailed
		if NeedsTravelLineBookingRules(scenario, attempt) {
			t.Fatal("candidate observation from failed attempt must be excluded")
		}
	}
	if NeedsTravelLineBookingRules(scenario, storage.AttemptInput{HTTPStatus: 200, Outcome: storage.OutcomePartial, Observations: unsuitable}) {
		t.Fatal("all unsuitable observations still triggered rules")
	}
}
