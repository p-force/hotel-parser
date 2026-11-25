package storage

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestSelectionPolicyNormalization(t *testing.T) {
	input := validAttempt()
	input.SelectionPolicy = &SelectionPolicy{Mode: "allowlist", RateIDs: []string{"b", "a"}}
	before, _ := json.Marshal(input)
	normalized, h1, err := normalizeAttempt(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.SelectionPolicy.RateIDs, []string{"a", "b"}) {
		t.Fatal(normalized.SelectionPolicy)
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	input.SelectionPolicy = &SelectionPolicy{Mode: "allowlist", RateIDs: []string{"a", "b"}}
	_, h2, err := normalizeAttempt(input)
	if err != nil || !bytes.Equal(h1, h2) {
		t.Fatal("policy order changes identity", err)
	}
	input.SelectionPolicy = &SelectionPolicy{Mode: "allowlist", RateIDs: []string{"a"}}
	_, h3, _ := normalizeAttempt(input)
	if bytes.Equal(h1, h3) {
		t.Fatal("changed policy must conflict")
	}
	var original AttemptInput
	if err := json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	if original.SelectionPolicy.RateIDs[0] != "b" {
		t.Fatal("source mutated")
	}
	for _, p := range []SelectionPolicy{{Mode: "other"}, {Mode: "allowlist"}, {Mode: "all_visible", RateIDs: []string{"r"}}, {Mode: "allowlist", RateIDs: []string{"r", "r"}}, {Mode: "allowlist", RateIDs: []string{"https://secret"}}} {
		input.SelectionPolicy = &p
		if _, _, err := normalizeAttempt(input); err == nil {
			t.Fatal("invalid policy accepted", p)
		}
	}
	input.SelectionPolicy = nil
	payload, _ := json.Marshal(input)
	if bytes.Contains(payload, []byte("SelectionPolicy")) {
		t.Fatal("nil policy changes legacy payload")
	}
}
