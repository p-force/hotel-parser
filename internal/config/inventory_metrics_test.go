package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAllInventoryConfigs(t *testing.T) {
	files, err := filepath.Glob("../../configs/*.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][2]*int{
		"arena33": {roomsPtr(19), roomsPtr(19)}, "azimut_aerostar": {roomsPtr(410), roomsPtr(70)},
		"bega": {roomsPtr(160), roomsPtr(49)}, "monarch": {roomsPtr(366), roomsPtr(70)},
		"sovietsky": {roomsPtr(109), roomsPtr(52)}, "petroff": {roomsPtr(43), roomsPtr(16)}, "lindsay": {nil, nil},
	}
	count := 0
	for _, file := range files {
		if filepath.Base(file) == "groups.json" {
			continue
		}
		cfg, err := Load(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if want, ok := expected[cfg.ID]; ok {
			if !reflect.DeepEqual(cfg.Inventory.PhysicalTotalRooms, want[0]) || !reflect.DeepEqual(cfg.Inventory.PublicObservedMax, want[1]) {
				t.Fatal(cfg.ID, cfg.Inventory)
			}
		}
		if cfg.Inventory.CategoriesFormCompletePartition {
			t.Fatal("partition must not be inferred", cfg.ID)
		}
		count++
	}
	if count != 7 {
		t.Fatal("hotel configs", count)
	}
}

func TestIndependentInventoryMetrics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(map[string]any, []any)
		wantErr string
	}{
		{"independent", func(i map[string]any, c []any) {
			i["physical_total_rooms"] = 410
			i["public_observed_max"] = 70
			c[0].(map[string]any)["physical_total_rooms"] = nil
		}, ""},
		{"unknown_physical", func(i map[string]any, c []any) {
			i["physical_total_rooms"] = nil
			c[0].(map[string]any)["physical_total_rooms"] = nil
		}, ""},
		{"unknown_public", func(i map[string]any, c []any) {
			i["public_observed_max"] = nil
			c[0].(map[string]any)["public_observed_max"] = nil
		}, ""},
		{"all_unknown", func(i map[string]any, c []any) {
			i["physical_total_rooms"] = nil
			i["public_observed_max"] = nil
			i["physical_source"] = ""
			i["public_source"] = ""
			for _, x := range c {
				x.(map[string]any)["physical_total_rooms"] = nil
				x.(map[string]any)["public_observed_max"] = nil
			}
		}, ""},
		{"public_exceeds_physical", func(i map[string]any, c []any) { i["public_observed_max"] = 20 }, "must not exceed"},
		{"category_public_exceeds_physical", func(i map[string]any, c []any) { c[0].(map[string]any)["public_observed_max"] = 11 }, "must not exceed"},
		{"no_partition_inference", func(i map[string]any, c []any) { i["physical_total_rooms"] = 410 }, ""},
		{"explicit_partition_mismatch", func(i map[string]any, c []any) {
			i["physical_total_rooms"] = 410
			i["categories_form_complete_partition"] = true
		}, "sum of categories"},
		{"explicit_partition_incomplete_counts", func(i map[string]any, c []any) {
			i["categories_form_complete_partition"] = true
			c[0].(map[string]any)["physical_total_rooms"] = nil
		}, ""},
		{"public_sum_unrelated", func(i map[string]any, c []any) {
			i["categories_form_complete_partition"] = true
			i["public_observed_max"] = 1
		}, ""},
		{"custom_sources", func(i map[string]any, c []any) {
			i["physical_source"] = "official_verified_inventory"
			i["public_source"] = "confirmed_public_api"
		}, ""},
		{"missing_physical_source", func(i map[string]any, c []any) { i["physical_source"] = "" }, "physical_source"},
		{"missing_public_source", func(i map[string]any, c []any) { i["public_source"] = "" }, "public_source"},
		{"old_field_rejected", func(i map[string]any, c []any) { i["total_rooms"] = 19 }, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := arenaJSON(t, func(fields map[string]any) {
				i := fields["inventory"].(map[string]any)
				tc.change(i, i["categories"].([]any))
			})
			cfg, err := Parse(data)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var expected struct {
				Inventory InventoryConfig `json:"inventory"`
			}
			if err := json.Unmarshal(data, &expected); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Inventory, expected.Inventory) {
				t.Fatal("metric substituted or lost", cfg.Inventory, expected.Inventory)
			}
			roundTrip, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := Parse(roundTrip)
			if err != nil || !reflect.DeepEqual(cfg, restored) {
				t.Fatal("round trip", restored, err)
			}
		})
	}
	for _, field := range []string{"physical_total_rooms", "public_observed_max"} {
		for _, value := range []any{0, -1, 1.5, "19"} {
			for _, category := range []bool{false, true} {
				data := arenaJSON(t, func(f map[string]any) {
					i := f["inventory"].(map[string]any)
					if category {
						i = i["categories"].([]any)[0].(map[string]any)
					}
					i[field] = value
				})
				if _, err := Parse(data); err == nil {
					t.Fatal("invalid metric accepted", field, value, category)
				}
			}
		}
		for _, category := range []bool{false, true} {
			data := arenaJSON(t, func(f map[string]any) {
				i := f["inventory"].(map[string]any)
				if category {
					i = i["categories"].([]any)[0].(map[string]any)
				}
				delete(i, field)
			})
			if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "required") {
				t.Fatal(field, category, err)
			}
		}
	}
}
