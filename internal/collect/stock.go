package collect

import (
	"strings"

	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/source/travelline"
)

type StockStatus = source.StockStatus

const (
	StockKnown    = source.StockKnown
	StockUnknown  = source.StockUnknown
	StockConflict = source.StockConflict
)

type CategoryStock = source.CategoryStock

// NormalizeCategoryStock возвращает по одному остатку для непустых ID из Categories,
// сохраняя порядок первого появления. Отсутствующие категории не создаются.
// Предполагается, что даты предложений и ConfirmedUnavailable проверены Parse для Request.
// Варианты дополнительно сверяются по категории и числу взрослых. Входные данные не меняются.
func NormalizeCategoryStock(result bnovo.SearchResult) []CategoryStock {
	if result.Source == "travelline" {
		return travelline.NormalizeCategoryStock(result)
	}

	stocks := make([]CategoryStock, 0, len(result.Categories))
	seen := make(map[string]bool, len(result.Categories))
	for _, category := range result.Categories {
		if category.ID == "" || seen[category.ID] {
			continue
		}
		seen[category.ID] = true
		stocks = append(stocks, normalizeStock(result, category.ID))
	}
	return stocks
}

func normalizeStock(result bnovo.SearchResult, categoryID string) CategoryStock {
	stock := CategoryStock{CategoryID: categoryID, Status: StockUnknown}
	warn := func(code, message string) {
		stock.Warnings = append(stock.Warnings, bnovo.ParseWarning{Code: code, Message: message, Context: categoryID})
	}
	blocked, conflict := false, false
	unavailable := false
	for _, state := range result.CategoryAvailability {
		if state.CategoryID == categoryID && state.ConfirmedUnavailable {
			unavailable = true
			if state.HasOffers {
				conflict = true
			}
		}
	}
	for _, warning := range result.Warnings {
		// Неполный список ожидаемых категорий не подтверждает общий пустой ответ.
		if !stockWarningApplies(warning, categoryID, result) &&
			!(unavailable && warning.Code == "expected_category_missing") {
			continue
		}
		stock.Warnings = append(stock.Warnings, warning)
		switch warning.Code {
		case "category_metadata_missing":
			// Для нуля нужны и точные даты, и подтверждённая идентичность категории.
			blocked = blocked || unavailable
		case "room_area_invalid", "currency_unknown", "offer_price_invalid", "offers_not_found", "category_availability_unknown":
			// Эти предупреждения сами по себе не опровергают признаки количества.
		default:
			blocked = true
			if warning.Code == "availability_conflict" || warning.Code == "category_availability_conflict" {
				conflict = true
			}
		}
	}
	if result.Status == bnovo.SearchStatusFailed || result.Error != nil ||
		(result.Status != bnovo.SearchStatusSuccess && result.Status != bnovo.SearchStatusPartial && result.Status != bnovo.SearchStatusNoOffers) {
		warn("stock_result_unusable", "source result cannot confirm public stock")
		return stock
	}
	req := result.Request
	if req.CheckIn.IsZero() || req.CheckOut.IsZero() || req.CheckOut.Format("2006-01-02") <= req.CheckIn.Format("2006-01-02") || req.Adults <= 0 || len(req.ChildrenAges) > 0 {
		warn("stock_scenario_invalid", "search scenario cannot be matched to adult-only accommodation variants")
		return stock
	}
	var observed int
	hasObserved, incomplete, positive := false, false, false
	observe := func(value int) {
		if hasObserved && value != observed {
			conflict = true
		}
		if !hasObserved {
			observed = value
			hasObserved = true
		}
		if value > 0 {
			positive = true
		}
	}
	for _, offer := range result.Offers {
		if offer.CategoryID != categoryID || offer.Availability.SelectDisabled {
			continue
		}
		if offer.RateID == "" || offer.VariantID == "" {
			incomplete = true
			warn("stock_identity_missing", "offer rate and variant IDs are required")
			continue
		}
		matches, uncertain := stockVariantMatches(result, offer)
		if uncertain {
			incomplete = true
			warn("stock_variant_unknown", "offer has no unambiguous matching accommodation variant")
			continue
		}
		if !matches {
			continue
		} // Применимость строки не подтверждена для запрошенного числа гостей.
		evidence := offer.Availability
		if evidence.DataAvailable != nil && *evidence.DataAvailable < 0 {
			incomplete = true
			warn("stock_quantity_invalid", "data-available must not be negative")
			continue
		}
		maxOption := 0
		for _, quantity := range evidence.SelectableQuantities {
			if quantity > maxOption {
				maxOption = quantity
			}
		}
		if evidence.DataAvailable != nil {
			observe(*evidence.DataAvailable)
		}
		if maxOption > 0 {
			observe(maxOption)
		}
		if evidence.DataAvailable == nil || maxOption == 0 {
			incomplete = true
			warn("stock_evidence_incomplete", "both data-available and positive selectable quantities are required")
		}
	}
	if (unavailable || result.Status == bnovo.SearchStatusNoOffers) && positive {
		conflict = true
	}
	if conflict {
		stock.Status = StockConflict
		warn("stock_quantity_conflict", "category availability signals contradict each other")
		return stock
	}
	if blocked || incomplete {
		return stock
	}
	if unavailable {
		zero := 0
		stock.Available = &zero
		stock.Status = StockKnown
	} else if hasObserved && observed > 0 {
		// Ни тарифы, ни варианты не суммируются: все числа уже проверены на равенство.
		stock.Available = &observed
		stock.Status = StockKnown
	} else {
		warn("stock_evidence_missing", "public stock is not confirmed for this category and scenario")
	}
	return stock
}

func stockVariantMatches(result bnovo.SearchResult, offer bnovo.RateOffer) (matches, uncertain bool) {
	var found *bnovo.AccommodationVariant
	for _, variant := range result.Variants {
		if variant.ID != offer.VariantID {
			continue
		}
		if found != nil && *found != variant {
			return false, true
		}
		copy := variant
		found = &copy
	}
	if found == nil || found.CategoryID != offer.CategoryID || found.Adults <= 0 {
		return false, true
	}
	return found.MatchesAdults(result.Request.Adults, offer.Availability), false
}

// Контексты предупреждений соответствуют существующему parser.go:
// category, variant, category/variant/rate или rate/variant. Неопределённый
// контекст консервативно относится ко всем категориям, а не теряется.
func stockWarningApplies(warning bnovo.ParseWarning, categoryID string, result bnovo.SearchResult) bool {
	variantCategory := func(id string) (matches, found bool) {
		for _, variant := range result.Variants {
			if variant.ID == id {
				found = true
				matches = matches || variant.CategoryID == categoryID
			}
		}
		return
	}
	switch warning.Code {
	case "variant_id_missing", "rate_id_missing", "offer_room_id_missing", "currency_unknown", "offers_not_found":
		return true
	case "availability_conflict", "category_availability_conflict", "category_availability_unknown", "unavailable_period_mismatch", "expected_category_missing", "category_metadata_missing", "room_area_invalid":
		return warning.Context == categoryID || warning.Context == ""
	case "variant_conflict", "guest_count_unknown":
		matches, found := variantCategory(warning.Context)
		return matches || !found
	case "offer_identity_missing", "offer_category_unknown", "offer_variant_unknown", "offer_category_mismatch", "offer_duplicate_conflict":
		parts := strings.Split(warning.Context, "/")
		if len(parts) != 3 {
			return true
		}
		matches, found := variantCategory(parts[1])
		if parts[0] == categoryID || matches {
			return true
		}
		for _, category := range result.Categories {
			if category.ID == parts[0] {
				return false
			}
		}
		return !found
	case "row_availability_invalid", "available_quantity_invalid", "offer_rate_mismatch", "offer_plan_variant_mismatch", "offer_price_invalid":
		parts := strings.Split(warning.Context, "/")
		if len(parts) != 2 {
			return true
		}
		matches, found := variantCategory(parts[1])
		return matches || !found
	default:
		// Включая plans_metadata_missing: категория тарифного блока либо
		// глобальный $plansForJs, если привязка неизвестна.
		// Общая диагностика либо неизвестный новый код с доступной привязкой к категории.
		for _, category := range result.Categories {
			if category.ID == warning.Context {
				return category.ID == categoryID
			}
		}
		return true
	}
}
