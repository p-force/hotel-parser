package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAdultsConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  AdultCounts
	}{
		{"2", AdultCounts{2}}, {"1", AdultCounts{1}}, {"[1,2]", AdultCounts{1, 2}}, {"[2,1]", AdultCounts{2, 1}},
		{"[]", nil}, {"null", nil}, {"[1,1]", nil}, {"[1,3]", nil}, {"[null,2]", nil}, {"0", nil}, {"3", nil}, {"2.5", nil}, {`"2"`, nil}, {"[true]", nil},
	} {
		t.Run(tc.value, func(t *testing.T) {
			data := arenaJSON(t, func(fields map[string]any) {
				fields["collection"].(map[string]any)["adults"] = json.RawMessage(tc.value)
			})
			cfg, err := Parse(data)
			if tc.want == nil {
				if err == nil {
					t.Fatal("invalid adults accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Collection.Adults, tc.want) {
				t.Fatalf("adults=%v", cfg.Collection.Adults)
			}
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(encoded)
			if err != nil || !reflect.DeepEqual(parsed, cfg) {
				t.Fatalf("round trip: %v", err)
			}
		})
	}
	// Custom decoding must not bypass strict validation of unrelated fields.
	invalid := arenaJSON(t, func(fields map[string]any) {
		collection := fields["collection"].(map[string]any)
		collection["adults"] = []int{1, 2}
		collection["unexpected"] = true
	})
	if _, err := Parse(invalid); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown collection field: %v", err)
	}
}
