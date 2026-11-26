package storage

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func bookingRulesUnitInput() RunBookingRulesInput {
	zone := time.FixedZone("UTC+3", 3*60*60)
	at := time.Date(2026, 11, 25, 0, 30, 0, 123456789, zone)
	return RunBookingRulesInput{RunID: "run", RequestedAt: at, ReceivedAt: at.Add(time.Second), StartDate: at, EndDate: at.AddDate(0, 0, 7), RequestedRateIDs: []string{"128301", "128299", "128301"}, Rules: []BookingRuleInput{
		{Date: at.AddDate(0, 0, 1), RoomTypeID: value("4347"), RateID: "128299", Forbidden: value(false), MinLOS: value(0)},
		{Date: at, RoomTypeID: nil, RateID: "other", Forbidden: value(true), MinLOS: value(5), MaxLOS: value(1), MinAdvBooking: value(-1)},
	}}
}

func TestNormalizeBookingRulesMultiset(t *testing.T) {
	input := bookingRulesUnitInput()
	input.Rules = append(input.Rules, input.Rules[0])
	before, _ := json.Marshal(input)
	got, hash, err := normalizeRunBookingRules(input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) || len(got.Rules) != 3 || !reflect.DeepEqual(got.RequestedRateIDs, input.RequestedRateIDs) {
		t.Fatal("normalization changed input, request order or duplicate count")
	}
	if got.StartDate.Format("2006-01-02") != "2026-11-25" || got.StartDate.Hour() != 0 || got.RequestedAt.Nanosecond() != 123456000 || !got.RequestedAt.Equal(input.RequestedAt.Truncate(time.Microsecond)) {
		t.Fatal("calendar day or database timestamp precision changed", got)
	}
	reversed := input
	reversed.Rules = []BookingRuleInput{input.Rules[2], input.Rules[1], input.Rules[0]}
	again, sameHash, err := normalizeRunBookingRules(reversed)
	if err != nil || !bytes.Equal(hash, sameHash) || !reflect.DeepEqual(got, again) {
		t.Fatal("rule order changed multiset identity", err)
	}
	*input.Rules[0].RoomTypeID = "changed"
	*input.Rules[0].Forbidden = true
	*input.Rules[0].MinLOS = 99
	*input.Rules[1].MaxLOS = 99
	*input.Rules[1].MinAdvBooking = 99
	input.RequestedRateIDs[0] = "changed"
	_, stable, err := normalizeRunBookingRules(got)
	if err != nil || !bytes.Equal(hash, stable) {
		t.Fatal("canonical data aliases input", err)
	}
}

func TestBookingRulesIdentityPreservesNullableFieldsAndMetadata(t *testing.T) {
	base := bookingRulesUnitInput()
	_, baseline, err := normalizeRunBookingRules(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*RunBookingRulesInput)
	}{
		{"request time", func(in *RunBookingRulesInput) { in.RequestedAt = in.RequestedAt.Add(time.Second) }},
		{"receipt time", func(in *RunBookingRulesInput) { in.ReceivedAt = in.ReceivedAt.Add(time.Second) }},
		{"range start", func(in *RunBookingRulesInput) { in.StartDate = in.StartDate.AddDate(0, 0, 1) }},
		{"range end", func(in *RunBookingRulesInput) { in.EndDate = in.EndDate.AddDate(0, 0, 1) }},
		{"requested rate order", func(in *RunBookingRulesInput) {
			in.RequestedRateIDs[0], in.RequestedRateIDs[1] = in.RequestedRateIDs[1], in.RequestedRateIDs[0]
		}},
		{"duplicate multiplicity", func(in *RunBookingRulesInput) { in.Rules = append(in.Rules, in.Rules[0]) }},
		{"rule date", func(in *RunBookingRulesInput) { in.Rules[0].Date = in.Rules[0].Date.AddDate(0, 0, 1) }},
		{"rule rate", func(in *RunBookingRulesInput) { in.Rules[0].RateID = "other" }},
		{"room NULL vs empty", func(in *RunBookingRulesInput) { in.Rules[1].RoomTypeID = value("") }},
		{"forbidden false vs NULL", func(in *RunBookingRulesInput) { in.Rules[0].Forbidden = nil }},
		{"min zero vs NULL", func(in *RunBookingRulesInput) { in.Rules[0].MinLOS = nil }},
		{"max NULL vs zero", func(in *RunBookingRulesInput) { in.Rules[0].MaxLOS = value(0) }},
		{"advance NULL vs zero", func(in *RunBookingRulesInput) { in.Rules[0].MinAdvBooking = value(0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := bookingRulesUnitInput()
			tc.change(&changed)
			_, hash, err := normalizeRunBookingRules(changed)
			if err != nil || bytes.Equal(hash, baseline) {
				t.Fatal("different facts lost from identity", err)
			}
		})
	}
}

func TestNormalizeBookingRulesEmptyAndRequiredFields(t *testing.T) {
	input := bookingRulesUnitInput()
	input.Rules, input.RequestedRateIDs = nil, nil
	got, hash, err := normalizeRunBookingRules(input)
	if err != nil || got.Rules == nil || got.RequestedRateIDs == nil {
		t.Fatal("empty rules should remain a valid set", err)
	}
	input.Rules, input.RequestedRateIDs = []BookingRuleInput{}, []string{}
	_, emptyHash, err := normalizeRunBookingRules(input)
	if err != nil || !bytes.Equal(hash, emptyHash) {
		t.Fatal("nil/empty slices do not represent different stored sets", err)
	}
	for _, change := range []func(*RunBookingRulesInput){
		func(in *RunBookingRulesInput) { in.RunID = "" },
		func(in *RunBookingRulesInput) { in.RequestedAt = time.Time{} },
		func(in *RunBookingRulesInput) { in.ReceivedAt = time.Time{} },
		func(in *RunBookingRulesInput) { in.StartDate = time.Time{} },
		func(in *RunBookingRulesInput) { in.EndDate = time.Time{} },
		func(in *RunBookingRulesInput) { in.Rules[0].Date = time.Time{} },
	} {
		invalid := bookingRulesUnitInput()
		change(&invalid)
		if _, _, err := normalizeRunBookingRules(invalid); err == nil {
			t.Fatal("required field accepted as unset")
		}
	}
}
