package collect

import (
	"context"
	"log/slog"
	"sort"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// InventoryIncrease описывает обнаруженное увеличение public_observed_max относительно конфигурации.
// Не означает, что конфигурация или физический фонд PMS были изменены.
type InventoryIncrease struct {
	CategoryID   string
	CategoryName string
	PreviousMax  int
	Observed     int
	NewMax       int
}

// DetectInventoryIncreases сравнивает один нормализованный сценарий с валидированной
// публичным максимумом конфигурации. Возвращает увеличения и диагностику без изменения входных данных.
// Неизвестные и повторяющиеся категории исключаются из сравнения с предупреждением.
// Оба списка упорядочены по CategoryID (Context у предупреждений).
func DetectInventoryIncreases(stocks []CategoryStock, inventory config.InventoryConfig) ([]InventoryIncrease, []bnovo.ParseWarning) {
	categories := make(map[string]config.RoomCategory, len(inventory.Categories))
	for _, category := range inventory.Categories {
		categories[category.SourceRoomID] = category
	}
	byID := make(map[string]CategoryStock, len(stocks))
	duplicates := make(map[string]bool)
	for _, stock := range stocks {
		if _, exists := byID[stock.CategoryID]; exists {
			duplicates[stock.CategoryID] = true
		}
		byID[stock.CategoryID] = stock
	}
	var increases []InventoryIncrease
	var warnings []bnovo.ParseWarning
	for id, stock := range byID {
		category, exists := categories[id]
		if !exists {
			warnings = append(warnings, bnovo.ParseWarning{Code: "inventory_category_unknown", Message: "stock category is absent from configuration", Context: id})
			continue
		}
		if duplicates[id] {
			warnings = append(warnings, bnovo.ParseWarning{Code: "inventory_stock_duplicate", Message: "expected one normalized stock per category and scenario", Context: id})
			continue
		}
		if category.PublicObservedMax == nil {
			warnings = append(warnings, bnovo.ParseWarning{Code: "inventory_max_unknown", Message: "configured public maximum is unknown; comparison skipped", Context: id})
			continue
		}
		if stock.Status != StockKnown || stock.Available == nil || *stock.Available <= *category.PublicObservedMax {
			continue
		}
		increases = append(increases, InventoryIncrease{
			CategoryID: id, CategoryName: category.Name, PreviousMax: *category.PublicObservedMax,
			Observed: *stock.Available, NewMax: *stock.Available,
		})
	}
	sort.Slice(increases, func(i, j int) bool { return increases[i].CategoryID < increases[j].CategoryID })
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].Context < warnings[j].Context })
	return increases, warnings
}

// LogInventoryIncreases пишет по одному WARN на результат DetectInventoryIncreases.
// log передаётся вызывающей стороной; глобальная конфигурация логгера не меняется.
// property_id — внутренний ID отеля, а не публичный UID из запроса к Bnovo.
func LogInventoryIncreases(ctx context.Context, log *slog.Logger, hotel config.Config, req bnovo.SearchRequest, increases []InventoryIncrease) {
	for _, increase := range increases {
		log.WarnContext(ctx, "bnovo.inventory.max_increased",
			"property_id", hotel.ID,
			"category_id", increase.CategoryID,
			"category_name", increase.CategoryName,
			"previous_max", increase.PreviousMax,
			"observed_available", increase.Observed,
			"new_max", increase.NewMax,
			"check_in", req.CheckIn.Format("2006-01-02"),
			"check_out", req.CheckOut.Format("2006-01-02"),
			"config_updated", false,
		)
	}
}
