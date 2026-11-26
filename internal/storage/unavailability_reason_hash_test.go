package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"
)

func TestNilUnavailabilityReasonPreservesHistoricalHash(t *testing.T) {
	// Frozen canonical JSON before the reason field was added, independent of
	// today's Observation type. A missing reason must retain these exact bytes.
	const legacy = `{"JobID":"job","Number":1,"StartedAt":"2026-11-25T00:00:00Z","FinishedAt":"2026-11-25T00:00:00Z","DurationMS":0,"QueueWaitMS":null,"CollectedAt":null,"Outcome":"no_offers","HTTPStatus":200,"Error":null,"Warnings":[],"NextRetryAt":null,"Observations":[{"CategoryID":"cat","CategoryName":"Legacy","AreaM2":null,"Available":0,"StockStatus":"known","HasOffers":false,"ConfirmedUnavailable":true,"Warnings":[],"Variants":[],"Offers":[]}]}`
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	input := AttemptInput{JobID: "job", Number: 1, StartedAt: at, FinishedAt: at, Outcome: OutcomeNoOffers, HTTPStatus: 200, Observations: []Observation{{CategoryID: "cat", CategoryName: "Legacy", StockStatus: StockKnown, Available: value(0), ConfirmedUnavailable: true}}}
	normalized, hash, err := normalizeAttempt(input)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(legacy))
	if string(payload) != legacy || !bytes.Equal(hash, expected[:]) {
		t.Fatalf("historical canonical payload/hash changed: %s", payload)
	}
	input.Observations[0].UnavailabilityReason = value(UnavailabilityReasonBookingForbidden)
	_, withReason, err := normalizeAttempt(input)
	if err != nil || bytes.Equal(withReason, expected[:]) {
		t.Fatal("reason does not affect hash", err)
	}
}
