package storage_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

func TestAppliedBookingRulesPassAttemptValidation(t *testing.T) {
	date := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	forbidden := true
	response := travelline.BookingRulesResponse{BookingRules: []travelline.BookingRule{{
		Date: "2026-11-25", RoomTypes: []string{"4347"}, Code: "128299", Forbidden: &forbidden,
	}}}
	for _, outcome := range []storage.Outcome{storage.OutcomeSuccess, storage.OutcomePartial, storage.OutcomeNoOffers} {
		t.Run(string(outcome), func(t *testing.T) {
			attempt := storage.AttemptInput{
				JobID: "job", Number: 1, StartedAt: date, FinishedAt: date.Add(time.Second),
				DurationMS: 1000, HTTPStatus: 200, Outcome: outcome,
				SelectionPolicy: &storage.SelectionPolicy{Mode: "allowlist", RateIDs: []string{"128299"}},
				Observations:    []storage.Observation{{CategoryID: "4347", StockStatus: storage.StockUnknown}},
			}
			scenario := storage.Scenario{Source: "travelline", LOS: 1, CheckIn: date, CheckOut: date.AddDate(0, 0, 1)}
			if _, _, err := storage.NormalizeAttemptForBookingRulesTest(attempt); err != nil {
				t.Fatal("invalid initial attempt", err)
			}
			if got := collect.ApplyTravelLineBookingRules(scenario, &attempt, response.Index(), []string{"128299"}); got != 1 {
				t.Fatal("category was not enriched", got)
			}
			normalized, hash, err := storage.NormalizeAttemptForBookingRulesTest(attempt)
			if err != nil {
				t.Fatal("enriched attempt failed existing validation", err)
			}
			observation := normalized.Observations[0]
			if observation.StockStatus != storage.StockUnknown || observation.Available != nil || observation.HasOffers ||
				!observation.ConfirmedUnavailable || observation.UnavailabilityReason == nil ||
				*observation.UnavailabilityReason != storage.UnavailabilityReasonBookingForbidden || normalized.Outcome != outcome {
				t.Fatal("validation changed enriched state", normalized)
			}
			if got := collect.ApplyTravelLineBookingRules(scenario, &attempt, response.Index(), []string{"128299"}); got != 0 {
				t.Fatal("repeated enrichment changed attempt", got)
			}
			_, repeatedHash, err := storage.NormalizeAttemptForBookingRulesTest(attempt)
			if err != nil || !bytes.Equal(hash, repeatedHash) {
				t.Fatal("repeat application changed storage payload hash", err)
			}
		})
	}
}
