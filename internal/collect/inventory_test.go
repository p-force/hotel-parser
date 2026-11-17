package collect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func arenaInventoryConfig(t *testing.T) config.Config {
	t.Helper()
	hotel, err := config.Load(filepath.Join("..", "..", "configs", "arena33.json"))
	if err != nil {
		t.Fatal(err)
	}
	return hotel
}

func knownInventoryStock(id string, value int) CategoryStock {
	return CategoryStock{CategoryID: id, Available: stockInt(value), Status: StockKnown}
}

func inventorySnapshot(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDetectInventoryIncreases(t *testing.T) {
	hotel := arenaInventoryConfig(t)
	stadium := InventoryIncrease{CategoryID: "561429", CategoryName: "Stadium", PreviousMax: 10, Observed: 12, NewMax: 12}
	garden := InventoryIncrease{CategoryID: "459946", CategoryName: "Garden", PreviousMax: 4, Observed: 6, NewMax: 6}
	premium := InventoryIncrease{CategoryID: "639687", CategoryName: "Premium", PreviousMax: 5, Observed: 7, NewMax: 7}
	for _, tc := range []struct {
		name                   string
		stocks                 []CategoryStock
		want                   []InventoryIncrease
		warningCode, warningID string
	}{
		{name: "arena33_baseline", stocks: []CategoryStock{knownInventoryStock("561429", 10), knownInventoryStock("459946", 4), knownInventoryStock("639687", 5)}},
		{name: "stadium_increase", stocks: []CategoryStock{knownInventoryStock("561429", 12)}, want: []InventoryIncrease{stadium}},
		{name: "garden_decrease_ignored", stocks: []CategoryStock{knownInventoryStock("459946", 2)}},
		{name: "multiple_increases", stocks: []CategoryStock{knownInventoryStock("639687", 7), knownInventoryStock("561429", 12), knownInventoryStock("459946", 6)}, want: []InventoryIncrease{garden, stadium, premium}},
		{name: "unknown", stocks: []CategoryStock{{CategoryID: "561429", Status: StockUnknown, Available: stockInt(12)}}},
		{name: "conflict", stocks: []CategoryStock{{CategoryID: "561429", Status: StockConflict, Available: stockInt(12)}}},
		{name: "nil_available", stocks: []CategoryStock{{CategoryID: "561429", Status: StockKnown}}},
		{name: "zero", stocks: []CategoryStock{knownInventoryStock("561429", 0)}},
		{name: "negative", stocks: []CategoryStock{knownInventoryStock("561429", -1)}},
		{name: "absent_stocks"},
		{name: "unconfigured_category", stocks: []CategoryStock{knownInventoryStock("unconfigured", 20)}, warningCode: "inventory_category_unknown", warningID: "unconfigured"},
		{name: "unconfigured_unknown", stocks: []CategoryStock{{CategoryID: "unconfigured", Status: StockUnknown}}, warningCode: "inventory_category_unknown", warningID: "unconfigured"},
		{name: "categories_independent", stocks: []CategoryStock{knownInventoryStock("561429", 12), {CategoryID: "459946", Status: StockConflict, Available: stockInt(6)}, knownInventoryStock("unconfigured", 20)}, want: []InventoryIncrease{stadium}, warningCode: "inventory_category_unknown", warningID: "unconfigured"},
		{name: "duplicate_category_not_combined", stocks: []CategoryStock{knownInventoryStock("561429", 12), knownInventoryStock("561429", 15)}, warningCode: "inventory_stock_duplicate", warningID: "561429"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configBefore := inventorySnapshot(t, hotel)
			stocksBefore := inventorySnapshot(t, tc.stocks)
			increases, warnings := DetectInventoryIncreases(tc.stocks, hotel.Inventory)
			if !reflect.DeepEqual(increases, tc.want) {
				t.Errorf("увеличения = %+v, ожидались %+v", increases, tc.want)
			}
			if tc.warningCode == "" {
				if len(warnings) != 0 {
					t.Errorf("неожиданная диагностика: %+v", warnings)
				}
			} else if len(warnings) != 1 || warnings[0].Code != tc.warningCode || warnings[0].Context != tc.warningID || warnings[0].Message == "" {
				t.Errorf("неверная диагностика: %+v", warnings)
			}
			if !bytes.Equal(configBefore, inventorySnapshot(t, hotel)) || !bytes.Equal(stocksBefore, inventorySnapshot(t, tc.stocks)) {
				t.Error("входные структуры изменены")
			}
			if hotel.Inventory.TotalRooms == nil || *hotel.Inventory.TotalRooms != 19 {
				t.Error("изменён общий фонд")
			}
		})
	}
}

func TestInventoryComparisonOrderIndependent(t *testing.T) {
	hotel := arenaInventoryConfig(t)
	stocks := []CategoryStock{knownInventoryStock("561429", 12), knownInventoryStock("459946", 6), knownInventoryStock("639687", 7), knownInventoryStock("unknown-b", 10), knownInventoryStock("unknown-a", 11)}
	expected, expectedWarnings := DetectInventoryIncreases(stocks, hotel.Inventory)
	shuffled := append([]CategoryStock(nil), stocks...)
	slices.Reverse(shuffled)
	inventory := hotel.Inventory
	inventory.Categories = append([]config.RoomCategory(nil), inventory.Categories...)
	slices.Reverse(inventory.Categories)
	got, warnings := DetectInventoryIncreases(shuffled, inventory)
	if !reflect.DeepEqual(got, expected) || !reflect.DeepEqual(warnings, expectedWarnings) {
		t.Errorf("результат зависит от порядка: %+v %+v", got, warnings)
	}
}

func TestLogInventoryIncreases(t *testing.T) {
	hotel := arenaInventoryConfig(t)
	req := stockFixture().Request
	req.PropertyID = "not-the-internal-hotel-id"
	for _, tc := range []struct {
		name   string
		stocks []CategoryStock
		events int
	}{
		{"one", []CategoryStock{knownInventoryStock("561429", 12)}, 1},
		{"several", []CategoryStock{knownInventoryStock("561429", 12), knownInventoryStock("459946", 6)}, 2},
		{"baseline", []CategoryStock{knownInventoryStock("561429", 10), knownInventoryStock("459946", 4), knownInventoryStock("639687", 5)}, 0},
		{"lower", []CategoryStock{knownInventoryStock("459946", 2)}, 0},
		{"unknown", []CategoryStock{{CategoryID: "561429", Status: StockUnknown, Available: stockInt(12)}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			increases, _ := DetectInventoryIncreases(tc.stocks, hotel.Inventory)
			before := inventorySnapshot(t, struct {
				Hotel     config.Config
				Request   bnovo.SearchRequest
				Increases []InventoryIncrease
			}{hotel, req, increases})
			previousDefault := slog.Default()
			var output bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&output, nil))
			LogInventoryIncreases(context.Background(), log, hotel, req, increases)
			if slog.Default() != previousDefault {
				t.Error("изменён глобальный логгер")
			}
			scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
			count := 0
			for scanner.Scan() {
				if count >= len(increases) {
					t.Fatalf("лишнее событие: %s", scanner.Text())
				}
				var event map[string]any
				if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
					t.Fatal(err)
				}
				increase := increases[count]
				expected := map[string]any{
					"level": "WARN", "msg": "bnovo.inventory.max_increased", "property_id": "arena33",
					"category_id": increase.CategoryID, "category_name": increase.CategoryName,
					"previous_max": float64(increase.PreviousMax), "observed_available": float64(increase.Observed), "new_max": float64(increase.NewMax),
					"check_in": "2026-12-21", "check_out": "2026-12-22", "config_updated": false,
				}
				if _, err := time.Parse(time.RFC3339Nano, event["time"].(string)); err != nil {
					t.Fatal(err)
				}
				delete(event, "time")
				if !reflect.DeepEqual(event, expected) {
					t.Errorf("неверные атрибуты: %+v", event)
				}
				count++
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if count != tc.events {
				t.Errorf("событий = %d, ожидалось %d: %s", count, tc.events, output.String())
			}
			if strings.Contains(output.String(), req.PropertyID) || strings.Contains(output.String(), hotel.Source.PropertyID) {
				t.Error("в лог попал публичный UID вместо ID конфигурации")
			}
			after := inventorySnapshot(t, struct {
				Hotel     config.Config
				Request   bnovo.SearchRequest
				Increases []InventoryIncrease
			}{hotel, req, increases})
			if !bytes.Equal(before, after) {
				t.Error("логирование изменило входные данные")
			}
		})
	}
}

func TestInventoryComparisonConcurrentReadOnly(t *testing.T) {
	hotel := arenaInventoryConfig(t)
	stocks := []CategoryStock{knownInventoryStock("561429", 12)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				got, warnings := DetectInventoryIncreases(stocks, hotel.Inventory)
				if len(got) != 1 || got[0].NewMax != 12 || len(warnings) != 0 {
					t.Errorf("неверный конкурентный результат: %+v %+v", got, warnings)
					return
				}
			}
		}()
	}
	wg.Wait()
	if hotel.Inventory.Categories[0].TotalRooms == nil || *hotel.Inventory.Categories[0].TotalRooms != 10 || *stocks[0].Available != 12 {
		t.Error("входные данные изменены")
	}
}

func TestInventoryUnknownConfiguredMaximum(t *testing.T) {
	hotel := arenaInventoryConfig(t)
	hotel.Inventory.TotalRooms = nil
	hotel.Inventory.Categories[0].TotalRooms = nil
	stocks := []CategoryStock{knownInventoryStock("561429", 12)}
	increases, warnings := DetectInventoryIncreases(stocks, hotel.Inventory)
	if len(increases) != 0 || len(warnings) != 1 || warnings[0].Code != "inventory_max_unknown" || warnings[0].Context != "561429" {
		t.Errorf("неизвестный максимум был принят за ноль: %+v %+v", increases, warnings)
	}
	var output bytes.Buffer
	LogInventoryIncreases(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)), hotel, bnovo.SearchRequest{}, increases)
	if output.Len() != 0 {
		t.Error("увеличение неизвестного максимума не должно логироваться")
	}
	if hotel.Inventory.TotalRooms != nil || hotel.Inventory.Categories[0].TotalRooms != nil {
		t.Error("неизвестный максимум автоматически заполнен")
	}
}
