// Package collect проверяет результаты сбора относительно конфигурации отеля.
package collect

import (
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// ValidateExpectedCategories отмечает категории конфигурации, отсутствующие в ответе.
// Статус failed сохраняется; неполный успешный ответ получает статус partial.
func ValidateExpectedCategories(result *bnovo.SearchResult, inventory config.InventoryConfig) {
	expectedIDs := make([]string, 0, len(inventory.Categories))
	for _, category := range inventory.Categories {
		expectedIDs = append(expectedIDs, category.SourceRoomID)
	}
	warnings := validateExpectedCategories(expectedIDs, result.CategoryAvailability)
	result.Warnings = append(result.Warnings, warnings...)
	if len(warnings) > 0 && result.Status != bnovo.SearchStatusFailed {
		result.Status = bnovo.SearchStatusPartial
	}
}

func validateExpectedCategories(
	expectedIDs []string,
	actual []bnovo.CategoryAvailability,
) []bnovo.ParseWarning {
	actualIDs := make(map[string]struct{}, len(actual))

	for _, state := range actual {
		actualIDs[state.CategoryID] = struct{}{}
	}

	var warnings []bnovo.ParseWarning

	for _, expectedID := range expectedIDs {
		if _, exists := actualIDs[expectedID]; !exists {
			warnings = append(warnings, bnovo.ParseWarning{
				Code:    "expected_category_missing",
				Message: "expected category missing from source response",
				Context: expectedID,
			})
		}
	}

	return warnings
}

