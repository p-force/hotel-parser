package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNullableInventory(t *testing.T) {
	for _, tc := range []struct {
		name         string
		change       func(map[string]any, []any)
		total, first *int
		wantErr      string
	}{
		{"known_values", func(map[string]any, []any) {}, roomsPtr(19), roomsPtr(10), ""},
		{"unknown_total", func(i map[string]any, c []any) { i["total_rooms"] = nil }, nil, roomsPtr(10), ""},
		{"unknown_category", func(i map[string]any, c []any) { c[0].(map[string]any)["total_rooms"] = nil }, roomsPtr(19), nil, ""},
		{"unknown_total_and_category", func(i map[string]any, c []any) { i["total_rooms"] = nil; c[0].(map[string]any)["total_rooms"] = nil }, nil, nil, ""},
		{"partial_known_total_not_compared_to_subtotal", func(i map[string]any, c []any) { i["total_rooms"] = 20; c[0].(map[string]any)["total_rooms"] = nil }, roomsPtr(20), nil, ""},
		{"known_total_mismatch", func(i map[string]any, c []any) { i["total_rooms"] = 20 }, nil, nil, "sum of categories"},
		{"zero_total", func(i map[string]any, c []any) { i["total_rooms"] = 0 }, nil, nil, "total_rooms: must be positive"},
		{"negative_total", func(i map[string]any, c []any) { i["total_rooms"] = -1 }, nil, nil, "total_rooms: must be positive"},
		{"zero_category", func(i map[string]any, c []any) { c[0].(map[string]any)["total_rooms"] = 0 }, nil, nil, "categories[0].total_rooms: must be positive"},
		{"negative_category", func(i map[string]any, c []any) { c[0].(map[string]any)["total_rooms"] = -1 }, nil, nil, "categories[0].total_rooms: must be positive"},
		{"missing_total", func(i map[string]any, c []any) { delete(i, "total_rooms") }, nil, nil, "inventory.total_rooms: required"},
		{"missing_category_total", func(i map[string]any, c []any) { delete(c[0].(map[string]any), "total_rooms") }, nil, nil, "categories[0].total_rooms: required"},
		{"missing_category_with_unknown_total", func(i map[string]any, c []any) { i["total_rooms"] = nil; delete(c[0].(map[string]any), "total_rooms") }, nil, nil, "categories[0].total_rooms: required"},
		{"fractional_total", func(i map[string]any, c []any) { i["total_rooms"] = 19.5 }, nil, nil, "cannot unmarshal"},
		{"fractional_category", func(i map[string]any, c []any) { c[0].(map[string]any)["total_rooms"] = 1.5 }, nil, nil, "cannot unmarshal"},
		{"duplicate_ids_with_unknown_max", func(i map[string]any, c []any) {
			i["total_rooms"] = nil
			c[0].(map[string]any)["total_rooms"] = nil
			c[1].(map[string]any)["id"] = "stadium"
		}, nil, nil, "id: duplicate"},
		{"duplicate_source_ids", func(i map[string]any, c []any) { c[1].(map[string]any)["source_room_id"] = "561429" }, nil, nil, "source_room_id: duplicate"},
		{"unknown_inventory_field", func(i map[string]any, c []any) { i["extra"] = true }, nil, nil, "unknown field"},
		{"unknown_category_field", func(i map[string]any, c []any) { c[0].(map[string]any)["extra"] = true }, nil, nil, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := arenaJSON(t, func(fields map[string]any) {
				inventory := fields["inventory"].(map[string]any)
				tc.change(inventory, inventory["categories"].([]any))
			})
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
			if !reflect.DeepEqual(cfg.Inventory.TotalRooms, tc.total) || !reflect.DeepEqual(cfg.Inventory.Categories[0].TotalRooms, tc.first) {
				t.Errorf("не сохранены known/unknown: %+v", cfg.Inventory)
			}
			roundTrip, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := Parse(roundTrip)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg, restored) {
				t.Error("JSON round-trip потерял неизвестные максимумы")
			}
		})
	}
}

func TestLoadLindsay(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "configs", "lindsay.json"))
	if err != nil {
		t.Fatalf("Load Lindsay: %v", err)
	}
	if cfg.ID != "lindsay" || cfg.Role != "competitor" || cfg.Type != "hotel" || cfg.Source.PropertyID != "5125b29f-f39e-498a-94cf-9e47889ff248" {
		t.Fatalf("неверные свойства Lindsay: %+v", cfg)
	}
	if cfg.Inventory.TotalRooms != nil || cfg.Inventory.Source != "public_observed_max" {
		t.Error("неизвестный общий фонд заменён суммой")
	}
	// Проверяем существующие локальные значения, не переоценивая их сетевыми запросами.
	want := map[string]*int{"188497": roomsPtr(2), "188498": roomsPtr(2), "188499": roomsPtr(1), "188500": roomsPtr(2), "188501": roomsPtr(1), "188502": roomsPtr(2), "188503": roomsPtr(1), "188504": nil, "188505": roomsPtr(1), "201948": roomsPtr(1)}
	names := map[string]string{
		"188497": "эконом с общей ванной комнатой", "188498": "улучшенный стандарный номер",
		"188499": "стандартный номер с одной двухспальной кроватью", "188500": "классический семейный номер",
		"188501": "стандарт с двумя раздельными кроватями", "188502": "эконом с раздельными кроватями и собственной ванной комнатой",
		"188503": "трехместный стандартный номер", "188504": "улучшенный  номер с одной кроватью",
		"188505": "одноместный номер с общей ванной комнатой", "201948": "студия",
	}
	if len(cfg.Inventory.Categories) != 10 {
		t.Fatalf("категорий=%d", len(cfg.Inventory.Categories))
	}
	for _, category := range cfg.Inventory.Categories {
		expected, exists := want[category.SourceRoomID]
		if !exists || !reflect.DeepEqual(category.TotalRooms, expected) || category.Name != names[category.SourceRoomID] {
			t.Errorf("изменены исходные данные категории: %+v", category)
		}
		delete(want, category.SourceRoomID)
	}
	if len(want) != 0 {
		t.Errorf("потеряны категории: %v", want)
	}
	expectedRates := []RateConfig{{ID: "772150", Name: "Лояльный", Enabled: true}, {ID: "772153", Name: "С гарантией", Enabled: true}}
	if !reflect.DeepEqual(cfg.Rates, expectedRates) {
		t.Errorf("тарифы=%+v", cfg.Rates)
	}
	if cfg.Collection.Enabled || cfg.Collection.DaysAhead != 60 || cfg.Collection.Adults != 2 || cfg.Collection.Rooms != 1 || len(cfg.Collection.ChildrenAges) != 0 || !reflect.DeepEqual(cfg.Collection.Nights, []int{1, 2, 3}) {
		t.Errorf("изменён сценарий MVP: %+v", cfg.Collection)
	}
}

func TestLoadArena33KnownInventory(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "configs", "arena33.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Role != "own" || cfg.Type != "apart_hotel" || len(cfg.Rates) != 0 || cfg.Inventory.TotalRooms == nil || *cfg.Inventory.TotalRooms != 19 {
		t.Fatalf("сломана совместимость Arena33: %+v", cfg)
	}
	for i, want := range []int{10, 4, 5} {
		if cfg.Inventory.Categories[i].TotalRooms == nil || *cfg.Inventory.Categories[i].TotalRooms != want {
			t.Errorf("категория %d потеряла максимум %d", i, want)
		}
	}
}

func TestUnknownTopLevelFieldStillRejected(t *testing.T) {
	_, err := Parse(arenaJSON(t, func(fields map[string]any) { fields["unexpected"] = true }))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("ошибка=%v", err)
	}
}
