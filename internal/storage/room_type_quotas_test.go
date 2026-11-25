package storage

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestQuotaCanonicalPayloadAndLegacyOmission(t *testing.T) {
	input := validAttempt()
	legacy, _ := json.Marshal(input)
	if bytes.Contains(legacy, []byte("RoomTypeQuotas")) {
		t.Fatal("legacy payload changed")
	}
	n, cat := 0, "cat"
	input.RoomTypeQuotas = &RoomTypeQuotaSet{Items: []RoomTypeQuota{{Ordinal: 0, RPH: "same", Quantity: &n, SourceRoomID: &cat, MappingStatus: QuotaMapped}, {Ordinal: 1, RPH: "same", MappingStatus: QuotaUnmapped}}}
	before, _ := json.Marshal(input)
	normalized, hash, err := normalizeAttempt(input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	again, hash2, err := normalizeAttempt(normalized)
	if err != nil || !bytes.Equal(hash, hash2) || !reflect.DeepEqual(normalized, again) {
		t.Fatal("replay unstable", err)
	}
	n = 42
	cat = "changed"
	if *normalized.RoomTypeQuotas.Items[0].Quantity != 0 || *normalized.RoomTypeQuotas.Items[0].SourceRoomID != "cat" {
		t.Fatal("input aliases canonical payload")
	}
	for _, q := range []RoomTypeQuota{{Ordinal: 1, MappingStatus: QuotaUnmapped}, {Ordinal: 0, MappingStatus: "unknown"}, {Ordinal: 0, MappingStatus: QuotaMapped}, {Ordinal: 0, MappingStatus: QuotaConflict, SourceRoomID: &cat}} {
		input.RoomTypeQuotas = &RoomTypeQuotaSet{Items: []RoomTypeQuota{q}}
		if _, _, err := normalizeAttempt(input); err == nil {
			t.Fatal("invalid mapping accepted", q)
		}
	}
	input.RoomTypeQuotas = nil
	_, absent, _ := normalizeAttempt(input)
	input.RoomTypeQuotas = &RoomTypeQuotaSet{}
	empty, emptyHash, err := normalizeAttempt(input)
	if err != nil || bytes.Equal(absent, emptyHash) || empty.RoomTypeQuotas.Items == nil {
		t.Fatal("absent and [] collapsed", err)
	}
}
