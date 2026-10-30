package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func value[T any](v T) *T { return &v }
func validAttempt() AttemptInput {
	now := time.Now().UTC()
	return AttemptInput{JobID: "job", Number: 1, StartedAt: now, FinishedAt: now.Add(time.Millisecond), DurationMS: 1, Outcome: OutcomePartial, HTTPStatus: 200, Observations: []Observation{{CategoryID: "cat", StockStatus: StockUnknown, Variants: []Variant{{ID: "v2", Adults: 2}, {ID: "v1", Adults: 1}}, Offers: []Offer{{VariantID: "v2", RateID: "r2", Price: value("100.50"), Currency: value("RUB"), SelectableQuantities: []int{1}}, {VariantID: "v1", RateID: "r1", SelectableQuantities: []int{1}}}}}}
}
func TestNormalizeAttemptIdempotency(t *testing.T) {
	input := validAttempt()
	before, _ := json.Marshal(input)
	normalized, hash, err := normalizeAttempt(input)
	if err != nil {
		t.Fatal(err)
	}
	again, hash2, err := normalizeAttempt(normalized)
	if err != nil || !bytes.Equal(hash, hash2) || !reflect.DeepEqual(normalized, again) {
		t.Fatal("canonical payload not stable")
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	swapped := validAttempt()
	swapped.StartedAt = input.StartedAt
	swapped.FinishedAt = input.FinishedAt
	swapped.Observations[0].Offers[0], swapped.Observations[0].Offers[1] = swapped.Observations[0].Offers[1], swapped.Observations[0].Offers[0]
	_, hash3, err := normalizeAttempt(swapped)
	if err != nil || !bytes.Equal(hash, hash3) {
		t.Fatal("fact order changed idempotency")
	}
}
func TestInvalidAttempt(t *testing.T) {
	for _, change := range []func(*AttemptInput){
		func(a *AttemptInput) { a.DurationMS = -1 }, func(a *AttemptInput) { a.Number = 0 },
		func(a *AttemptInput) { a.Observations[0].Available = value(0) },
		func(a *AttemptInput) { a.Observations[0].Offers[0].Price = value("-1") },
		func(a *AttemptInput) {
			a.Warnings = []Warning{{Code: "warning", Context: "https://secret.invalid?token=private"}}
		},
		func(a *AttemptInput) {
			a.Outcome = OutcomeFailed
			a.Error = &AttemptError{Code: "http_error"}
			a.Observations[0].StockStatus = StockKnown
			a.Observations[0].Available = value(1)
		},
	} {
		a := validAttempt()
		change(&a)
		if _, _, err := normalizeAttempt(a); err == nil {
			t.Error("invalid facts accepted")
		}
	}
}
func TestScenarioIdentity(t *testing.T) {
	in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.FixedZone("hotel", 3*3600))
	scenario := Scenario{PropertyID: "hotel", Source: "source", SourceOrigin: "https://EXAMPLE.com:443/", SourcePropertyID: "public-id", CheckIn: in, CheckOut: in.AddDate(0, 0, 2), LOS: 2, Adults: 1, Rooms: 1, Language: "ru"}
	normalized, err := normalizeScenario(scenario)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.CheckIn.Format("2006-01-02") != "2026-12-21" || normalized.SourceOrigin != "https://example.com" {
		t.Fatal("civil date/origin changed")
	}
	a, _ := digest(normalized)
	normalized.Adults = 2
	b, _ := digest(normalized)
	if bytes.Equal(a, b) {
		t.Fatal("guest scenarios mixed")
	}
	scenario.SourceOrigin = "https://user:secret@example.com/path?token=private"
	if _, err = normalizeScenario(scenario); err == nil {
		t.Fatal("credential-bearing source accepted")
	}
}

func TestDatabaseErrorsDoNotExposeWrappedCredentials(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		wrapped := fmt.Errorf("postgres://user:private-password@private.invalid/db: %w", cause)
		err := dbError("save attempt", wrapped)
		if !errors.Is(err, cause) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "://") {
			t.Fatalf("unsafe database error: %v", err)
		}
	}
}
