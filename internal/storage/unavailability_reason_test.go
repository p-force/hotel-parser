package storage

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestObservationUnavailabilityReasonValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reason    *UnavailabilityReason
		confirmed bool
		hasOffers bool
		wantError bool
	}{
		{"booking forbidden", value(UnavailabilityReasonBookingForbidden), true, false, false},
		{"unconfirmed category", value(UnavailabilityReasonBookingForbidden), false, false, true},
		{"category with offers", value(UnavailabilityReasonBookingForbidden), true, true, true},
		{"offers without confirmation", value(UnavailabilityReasonBookingForbidden), false, true, true},
		{"unknown reason", value(UnavailabilityReason("unsupported")), true, false, true},
		{"empty reason is not nil", value(UnavailabilityReason("")), true, false, true},
		{"no reason or confirmation", nil, false, false, false},
		{"legacy confirmed unavailable without reason", nil, true, false, false},
		{"offers without reason", nil, false, true, false},
		{"legacy conflict without reason", nil, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validAttempt()
			input.Observations = []Observation{{
				CategoryID: "cat", StockStatus: StockUnknown,
				ConfirmedUnavailable: tc.confirmed, HasOffers: tc.hasOffers,
				UnavailabilityReason: tc.reason,
			}}
			normalized, _, err := normalizeAttempt(input)
			if (err != nil) != tc.wantError {
				t.Fatalf("validation error=%v, want error=%v", err, tc.wantError)
			}
			if err == nil {
				got := normalized.Observations[0]
				if got.ConfirmedUnavailable != tc.confirmed || got.HasOffers != tc.hasOffers || got.StockStatus != StockUnknown || got.Available != nil {
					t.Fatal("reason validation changed availability", got)
				}
				if tc.reason == nil {
					if got.UnavailabilityReason != nil {
						t.Fatal("reason inferred for legacy observation")
					}
				} else if got.UnavailabilityReason == nil || *got.UnavailabilityReason != *tc.reason {
					t.Fatal("reason lost during normalization")
				}
			}
		})
	}
}

func TestLegacyNoOffersWithoutUnavailabilityReason(t *testing.T) {
	input := validAttempt()
	input.Outcome = OutcomeNoOffers
	input.Observations = []Observation{{CategoryID: "cat", StockStatus: StockKnown, Available: value(0), ConfirmedUnavailable: true}}
	normalized, _, err := normalizeAttempt(input)
	if err != nil {
		t.Fatal(err)
	}
	got := normalized.Observations[0]
	if got.UnavailabilityReason != nil || !got.ConfirmedUnavailable || got.HasOffers || got.Available == nil || *got.Available != 0 || got.StockStatus != StockKnown {
		t.Fatal("legacy no_offers changed", got)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(`"UnavailabilityReason"`)) {
		t.Fatal("unset reason changed historical canonical payload")
	}
}

func TestObservationUnavailabilityReasonJSON(t *testing.T) {
	var observation Observation
	if err := json.Unmarshal([]byte(`{"CategoryID":"cat","StockStatus":"unknown","ConfirmedUnavailable":true,"HasOffers":false,"UnavailabilityReason":"booking_forbidden"}`), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.UnavailabilityReason == nil || *observation.UnavailabilityReason != UnavailabilityReasonBookingForbidden {
		t.Fatal("typed reason not decoded", observation)
	}
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["UnavailabilityReason"]) != `"booking_forbidden"` {
		t.Fatal("reason must remain a scalar string", string(payload))
	}
	var absent Observation
	if err := json.Unmarshal([]byte(`{"UnavailabilityReason":null}`), &absent); err != nil || absent.UnavailabilityReason != nil {
		t.Fatal("null reason must be unset", err)
	}
}

func TestNormalizeUnavailabilityReasonKeepsInputAndSnapshot(t *testing.T) {
	input := validAttempt()
	input.Observations = []Observation{{CategoryID: "cat", StockStatus: StockUnknown, ConfirmedUnavailable: true, UnavailabilityReason: value(UnavailabilityReasonBookingForbidden)}}
	before, _ := json.Marshal(input)
	normalized, hash, err := normalizeAttempt(input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) {
		t.Fatal("normalization changed caller input")
	}
	_, repeatedHash, err := normalizeAttempt(normalized)
	if err != nil || !bytes.Equal(hash, repeatedHash) {
		t.Fatal("canonical payload is not stable", err)
	}
	without := normalized
	without.Observations = append([]Observation(nil), normalized.Observations...)
	without.Observations[0].UnavailabilityReason = nil
	_, withoutHash, err := normalizeAttempt(without)
	if err != nil || bytes.Equal(hash, withoutHash) {
		t.Fatal("known reason must be part of canonical payload", err)
	}
	*input.Observations[0].UnavailabilityReason = "changed"
	if *normalized.Observations[0].UnavailabilityReason != UnavailabilityReasonBookingForbidden {
		t.Fatal("canonical payload aliases caller reason")
	}
}
