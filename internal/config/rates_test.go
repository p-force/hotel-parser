package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func arenaJSON(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "arena33.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	change(fields)
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestObjectTypesAndRoles(t *testing.T) {
	for _, tc := range []struct {
		field, value string
		valid        bool
	}{
		{"role", "own", true}, {"role", "competitor", true}, {"role", "other", false},
		{"type", "apart_hotel", true}, {"type", "hotel", true}, {"type", "other", false},
	} {
		t.Run(tc.field+"/"+tc.value, func(t *testing.T) {
			data := arenaJSON(t, func(fields map[string]any) { fields[tc.field] = tc.value })
			_, err := Parse(data)
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && (err == nil || !strings.Contains(err.Error(), tc.field+":")) {
				t.Errorf("неверная ошибка: %v", err)
			}
		})
	}
}

func TestConfigRates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rates   any
		wantErr string
	}{
		{"two_rates", []any{map[string]any{"id": "772150", "name": "Лояльный", "enabled": true}, map[string]any{"id": "772153", "name": "С гарантией", "enabled": true}}, ""},
		{"disabled", []any{map[string]any{"id": "1", "name": "Rate", "enabled": false}}, ""},
		{"empty_id", []any{map[string]any{"id": " ", "name": "Rate", "enabled": true}}, "id: required"},
		{"missing_id", []any{map[string]any{"name": "Rate", "enabled": true}}, "id: required"},
		{"missing_name", []any{map[string]any{"id": "1", "enabled": true}}, "name: required"},
		{"empty_name", []any{map[string]any{"id": "1", "name": " ", "enabled": true}}, "name: required"},
		{"duplicate_id", []any{map[string]any{"id": "1", "name": "First", "enabled": true}, map[string]any{"id": "1", "name": "Second", "enabled": false}}, "duplicate"},
		{"null_enabled", []any{map[string]any{"id": "1", "name": "Rate", "enabled": nil}}, "must be boolean"},
		{"string_enabled", []any{map[string]any{"id": "1", "name": "Rate", "enabled": "true"}}, "cannot unmarshal"},
		{"number_enabled", []any{map[string]any{"id": "1", "name": "Rate", "enabled": 1}}, "cannot unmarshal"},
		{"unknown_rate_field", []any{map[string]any{"id": "1", "name": "Rate", "enabled": true, "extra": true}}, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := arenaJSON(t, func(fields map[string]any) { fields["rates"] = tc.rates })
			cfg, err := Parse(data)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("ошибка=%v, ожидалось %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Rates) != len(tc.rates.([]any)) {
				t.Errorf("не сохранены тарифы: %+v", cfg.Rates)
			}
			if tc.name == "disabled" && cfg.Rates[0].Enabled {
				t.Error("false заменено на true")
			}
		})
	}
	t.Run("rates_optional", func(t *testing.T) {
		cfg, err := Parse(arenaJSON(t, func(map[string]any) {}))
		if err != nil || len(cfg.Rates) != 0 {
			t.Errorf("сломана конфигурация без rates: %+v %v", cfg.Rates, err)
		}
	})
}
