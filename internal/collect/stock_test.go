package collect

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func stockInt(value int) *int { return &value }

func stockFixture() bnovo.SearchResult {
	return bnovo.SearchResult{
		Status:     bnovo.SearchStatusSuccess,
		Request:    bnovo.SearchRequest{PropertyID: "public-uid", CheckIn: time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), CheckOut: time.Date(2026, 12, 22, 0, 0, 0, 0, time.UTC), Adults: 2, Language: "ru"},
		Categories: []bnovo.RoomCategory{{ID: "561429", Name: "Stadium"}},
		Variants:   []bnovo.AccommodationVariant{{ID: "561429", CategoryID: "561429", Adults: 2}},
		Offers: []bnovo.RateOffer{{CategoryID: "561429", VariantID: "561429", RateID: "1", Visible: true,
			Availability: bnovo.AvailabilityEvidence{DataAvailable: stockInt(2), SelectableQuantities: []int{1, 2}}}},
		CategoryAvailability: []bnovo.CategoryAvailability{{CategoryID: "561429", HasOffers: true}},
	}
}

func stockSnapshot(t *testing.T, result bnovo.SearchResult) []byte {
	t.Helper()
	snapshot, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestNormalizeCategoryStock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*bnovo.SearchResult)
		status    StockStatus
		available *int
		warning   string
	}{
		{"matching_signals", func(r *bnovo.SearchResult) { r.Offers[0].Availability.SelectableQuantities = []int{2, 1, 2} }, StockKnown, stockInt(2), ""},
		{"equal_rates", func(r *bnovo.SearchResult) {
			other := r.Offers[0]
			other.RateID = "2"
			r.Offers = append(r.Offers, other)
		}, StockKnown, stockInt(2), ""},
		{"equal_variants", func(r *bnovo.SearchResult) {
			r.Variants = append(r.Variants, bnovo.AccommodationVariant{ID: "561430", CategoryID: "561429", Adults: 2})
			other := r.Offers[0]
			other.VariantID = "561430"
			r.Offers = append(r.Offers, other)
		}, StockKnown, stockInt(2), ""},
		{"conflicting_rates", func(r *bnovo.SearchResult) {
			other := r.Offers[0]
			other.RateID = "2"
			other.Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(3), SelectableQuantities: []int{1, 2, 3}}
			r.Offers = append(r.Offers, other)
		}, StockConflict, nil, "stock_quantity_conflict"},
		{"conflicting_variants", func(r *bnovo.SearchResult) {
			r.Variants = append(r.Variants, bnovo.AccommodationVariant{ID: "561430", CategoryID: "561429", Adults: 2})
			other := r.Offers[0]
			other.VariantID = "561430"
			other.Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(3), SelectableQuantities: []int{1, 2, 3}}
			r.Offers = append(r.Offers, other)
		}, StockConflict, nil, "stock_quantity_conflict"},
		{"conflicting_signals", func(r *bnovo.SearchResult) { r.Offers[0].Availability.SelectableQuantities = []int{1, 3} }, StockConflict, nil, "stock_quantity_conflict"},
		{"data_available_only", func(r *bnovo.SearchResult) { r.Offers[0].Availability.SelectableQuantities = nil }, StockUnknown, nil, "stock_evidence_incomplete"},
		{"options_only", func(r *bnovo.SearchResult) { r.Offers[0].Availability.DataAvailable = nil }, StockUnknown, nil, "stock_evidence_incomplete"},
		{"no_signals", func(r *bnovo.SearchResult) { r.Offers[0].Availability = bnovo.AvailabilityEvidence{} }, StockUnknown, nil, "stock_evidence_incomplete"},
		{"one_complete_one_incomplete", func(r *bnovo.SearchResult) {
			other := r.Offers[0]
			other.RateID = "2"
			other.Availability.SelectableQuantities = nil
			r.Offers = append(r.Offers, other)
		}, StockUnknown, nil, "stock_evidence_incomplete"},
		{"incomplete_contradicting_complete", func(r *bnovo.SearchResult) {
			other := r.Offers[0]
			other.RateID = "2"
			other.Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(3)}
			r.Offers = append(r.Offers, other)
		}, StockConflict, nil, "stock_quantity_conflict"},
		{"negative_quantity", func(r *bnovo.SearchResult) { r.Offers[0].Availability.DataAvailable = stockInt(-1) }, StockUnknown, nil, "stock_quantity_invalid"},
		{"zero_without_confirmation", func(r *bnovo.SearchResult) {
			r.Offers[0].Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(0), SelectableQuantities: []int{0}}
		}, StockUnknown, nil, "stock_evidence_incomplete"},
		{"zero_data_positive_options", func(r *bnovo.SearchResult) { r.Offers[0].Availability.DataAvailable = stockInt(0) }, StockConflict, nil, "stock_quantity_conflict"},
		{"hidden_only", func(r *bnovo.SearchResult) { r.Offers[0].Visible = false }, StockUnknown, nil, "stock_evidence_missing"},
		{"disabled_only", func(r *bnovo.SearchResult) { r.Offers[0].Availability.SelectDisabled = true }, StockUnknown, nil, "stock_evidence_missing"},
		{"hidden_conflict_ignored", func(r *bnovo.SearchResult) {
			other := r.Offers[0]
			other.Visible = false
			other.Availability.DataAvailable = stockInt(7)
			r.Offers = append(r.Offers, other)
		}, StockKnown, stockInt(2), ""},
		{"disabled_conflict_ignored", func(r *bnovo.SearchResult) {
			other := r.Offers[0]
			other.Availability.SelectDisabled = true
			other.Availability.DataAvailable = stockInt(7)
			r.Offers = append(r.Offers, other)
		}, StockKnown, stockInt(2), ""},
		{"different_guest_count_ignored", func(r *bnovo.SearchResult) {
			r.Variants = append(r.Variants, bnovo.AccommodationVariant{ID: "561430", CategoryID: "561429", Adults: 3})
			other := r.Offers[0]
			other.VariantID = "561430"
			other.Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(7), SelectableQuantities: []int{7}}
			r.Offers = append(r.Offers, other)
		}, StockKnown, stockInt(2), ""},
		{"only_different_guest_count", func(r *bnovo.SearchResult) { r.Variants[0].Adults = 3 }, StockUnknown, nil, "stock_evidence_missing"},
		{"unknown_variant", func(r *bnovo.SearchResult) { r.Variants = nil }, StockUnknown, nil, "stock_variant_unknown"},
		{"unknown_guest_count", func(r *bnovo.SearchResult) { r.Variants[0].Adults = 0 }, StockUnknown, nil, "stock_variant_unknown"},
		{"variant_category_mismatch", func(r *bnovo.SearchResult) { r.Variants[0].CategoryID = "459946" }, StockUnknown, nil, "stock_variant_unknown"},
		{"conflicting_variant_metadata", func(r *bnovo.SearchResult) {
			r.Variants = append(r.Variants, bnovo.AccommodationVariant{ID: "561429", CategoryID: "561429", Adults: 3})
		}, StockUnknown, nil, "stock_variant_unknown"},
		{"confirmed_unavailable", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusNoOffers
			r.Offers = nil
			r.CategoryAvailability[0] = bnovo.CategoryAvailability{CategoryID: "561429", ConfirmedUnavailable: true}
		}, StockKnown, stockInt(0), ""},
		{"unavailable_with_hidden_offer", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusNoOffers
			r.Offers[0].Visible = false
			r.CategoryAvailability[0] = bnovo.CategoryAvailability{CategoryID: "561429", ConfirmedUnavailable: true}
		}, StockKnown, stockInt(0), ""},
		{"unavailable_with_disabled_offer", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusNoOffers
			r.Offers[0].Availability.SelectDisabled = true
			r.CategoryAvailability[0] = bnovo.CategoryAvailability{CategoryID: "561429", ConfirmedUnavailable: true}
		}, StockKnown, stockInt(0), ""},
		{"unavailable_and_positive", func(r *bnovo.SearchResult) {
			r.CategoryAvailability[0].ConfirmedUnavailable = true
			r.CategoryAvailability[0].HasOffers = false
		}, StockConflict, nil, "stock_quantity_conflict"},
		{"contradictory_category_state", func(r *bnovo.SearchResult) { r.Offers = nil; r.CategoryAvailability[0].ConfirmedUnavailable = true }, StockConflict, nil, "stock_quantity_conflict"},
		{"no_offers_without_confirmation", func(r *bnovo.SearchResult) { r.Status = bnovo.SearchStatusNoOffers; r.Offers = nil }, StockUnknown, nil, "stock_evidence_missing"},
		{"no_offers_but_positive", func(r *bnovo.SearchResult) { r.Status = bnovo.SearchStatusNoOffers }, StockConflict, nil, "stock_quantity_conflict"},
		{"no_offers", func(r *bnovo.SearchResult) { r.Offers = nil }, StockUnknown, nil, "stock_evidence_missing"},
		{"partial_valid_category", func(r *bnovo.SearchResult) { r.Status = bnovo.SearchStatusPartial }, StockKnown, stockInt(2), ""},
		{"partial_quantity_warning", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusPartial
			r.Warnings = []bnovo.ParseWarning{{Code: "available_quantity_invalid", Context: "1/561429", Message: "invalid quantity"}}
		}, StockUnknown, nil, "available_quantity_invalid"},
		{"partial_duplicate_warning", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusPartial
			r.Warnings = []bnovo.ParseWarning{{Code: "offer_duplicate_conflict", Context: "561429/561429/2", Message: "discarded duplicate"}}
		}, StockUnknown, nil, "offer_duplicate_conflict"},
		{"partial_price_warning", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusPartial
			r.Warnings = []bnovo.ParseWarning{{Code: "offer_price_invalid", Context: "1/561429", Message: "invalid price"}}
		}, StockKnown, stockInt(2), "offer_price_invalid"},
		{"partial_unavailable_date_warning", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusPartial
			r.Offers = nil
			r.CategoryAvailability[0] = bnovo.CategoryAvailability{CategoryID: "561429", ConfirmedUnavailable: true}
			r.Warnings = []bnovo.ParseWarning{{Code: "unavailable_period_mismatch", Context: "561429", Message: "different dates"}}
		}, StockUnknown, nil, "unavailable_period_mismatch"},
		{"unscoped_availability_warning", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusPartial
			r.Warnings = []bnovo.ParseWarning{{Code: "available_quantity_invalid", Context: "unresolved", Message: "unknown scope"}}
		}, StockUnknown, nil, "available_quantity_invalid"},
		{"failed", func(r *bnovo.SearchResult) { r.Status = bnovo.SearchStatusFailed }, StockUnknown, nil, "stock_result_unusable"},
		{"failed_with_zero", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusFailed
			r.Offers = nil
			r.CategoryAvailability[0].ConfirmedUnavailable = true
		}, StockUnknown, nil, "stock_result_unusable"},
		{"invalid_dates", func(r *bnovo.SearchResult) { r.Request.CheckOut = r.Request.CheckIn }, StockUnknown, nil, "stock_scenario_invalid"},
		{"children_scenario_unsupported", func(r *bnovo.SearchResult) { r.Request.ChildrenAges = []int{7} }, StockUnknown, nil, "stock_scenario_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := stockFixture()
			tc.change(&input)
			before := stockSnapshot(t, input)
			stocks := NormalizeCategoryStock(input)
			if len(stocks) != 1 {
				t.Fatalf("ожидалась одна категория: %+v", stocks)
			}
			stock := stocks[0]
			if stock.CategoryID != "561429" || stock.Status != tc.status || !reflect.DeepEqual(stock.Available, tc.available) {
				t.Errorf("получено %+v, ожидались %s/%v", stock, tc.status, tc.available)
			}
			found := false
			for _, warning := range stock.Warnings {
				if warning.Code == tc.warning {
					found = true
				}
				if warning.Message == "" {
					t.Error("пустая диагностика")
				}
			}
			if tc.warning != "" && !found {
				t.Errorf("нет предупреждения %s: %+v", tc.warning, stock.Warnings)
			}
			if tc.warning == "" && len(stock.Warnings) != 0 {
				t.Errorf("неожиданная диагностика: %+v", stock.Warnings)
			}
			if !reflect.DeepEqual(before, stockSnapshot(t, input)) {
				t.Fatal("функция изменила вход")
			}
			// Возвращаемые указатели и срезы также не должны ссылаться на изменяемые входные данные.
			if stocks[0].Available != nil {
				*stocks[0].Available = 999
			}
			if len(stocks[0].Warnings) > 0 {
				stocks[0].Warnings[0].Message = "changed output"
			}
			if !reflect.DeepEqual(before, stockSnapshot(t, input)) {
				t.Error("выходной результат разделяет изменяемую память со входом")
			}
		})
	}
}

func TestStockCategoryOrderAndWarningScope(t *testing.T) {
	input := stockFixture()
	input.Status = bnovo.SearchStatusPartial
	input.Categories = nil
	input.Variants = nil
	input.Offers = nil
	input.CategoryAvailability = nil
	for _, item := range []struct {
		id    string
		count int
	}{{"561429", 10}, {"459946", 4}, {"639687", 5}} {
		input.Categories = append(input.Categories, bnovo.RoomCategory{ID: item.id})
		input.Variants = append(input.Variants, bnovo.AccommodationVariant{ID: item.id, CategoryID: item.id, Adults: 2})
		input.Offers = append(input.Offers, bnovo.RateOffer{CategoryID: item.id, VariantID: item.id, RateID: "1", Visible: true, Availability: bnovo.AvailabilityEvidence{DataAvailable: stockInt(item.count), SelectableQuantities: []int{1, item.count}}})
	}
	input.Warnings = []bnovo.ParseWarning{
		{Code: "available_quantity_invalid", Context: "1/459946", Message: "Garden quantity invalid"},
		{Code: "expected_category_missing", Context: "absent", Message: "missing category"},
	}
	stocks := NormalizeCategoryStock(input)
	if len(stocks) != 3 {
		t.Fatalf("неверное число категорий: %+v", stocks)
	}
	for i, want := range []struct {
		id        string
		status    StockStatus
		available *int
	}{{"561429", StockKnown, stockInt(10)}, {"459946", StockUnknown, nil}, {"639687", StockKnown, stockInt(5)}} {
		if stocks[i].CategoryID != want.id || stocks[i].Status != want.status || !reflect.DeepEqual(stocks[i].Available, want.available) {
			t.Errorf("неверный остаток: %+v", stocks[i])
		}
	}
	if len(stocks[1].Warnings) != 1 || stocks[1].Warnings[0] != input.Warnings[0] {
		t.Errorf("исходное предупреждение не сохранено: %+v", stocks[1].Warnings)
	}
	if len(stocks[0].Warnings) != 0 || len(stocks[2].Warnings) != 0 {
		t.Error("предупреждение другой категории повлияло на независимые остатки")
	}
}

func TestStockDoesNotInventCategories(t *testing.T) {
	input := stockFixture()
	input.Categories = nil
	if got := NormalizeCategoryStock(input); len(got) != 0 {
		t.Errorf("категории созданы из предложений или CategoryAvailability: %+v", got)
	}
	input.Categories = []bnovo.RoomCategory{{ID: "561429"}, {ID: ""}, {ID: "561429"}}
	if got := NormalizeCategoryStock(input); len(got) != 1 || got[0].CategoryID != "561429" {
		t.Errorf("неверная дедупликация категорий: %+v", got)
	}
}

func TestStockLOS(t *testing.T) {
	for _, los := range []int{1, 2, 3} {
		t.Run(string(rune('0'+los)), func(t *testing.T) {
			input := stockFixture()
			input.Request.CheckOut = input.Request.CheckIn.AddDate(0, 0, los)
			got := NormalizeCategoryStock(input)
			if got[0].Status != StockKnown || got[0].Available == nil || *got[0].Available != 2 {
				t.Errorf("остаток не должен умножаться на LOS: %+v", got)
			}
		})
	}
}

func TestStockConflictIndependentOfOfferOrder(t *testing.T) {
	input := stockFixture()
	other := input.Offers[0]
	other.RateID = "2"
	other.Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(3), SelectableQuantities: []int{3}}
	input.Offers = append(input.Offers, other)
	forward := NormalizeCategoryStock(input)
	input.Offers[0], input.Offers[1] = input.Offers[1], input.Offers[0]
	reverse := NormalizeCategoryStock(input)
	if !reflect.DeepEqual(forward, reverse) || forward[0].Status != StockConflict {
		t.Errorf("результат зависит от порядка: %+v / %+v", forward, reverse)
	}
}

func TestStockConcurrentReadOnly(t *testing.T) {
	input := stockFixture()
	before := stockSnapshot(t, input)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				got := NormalizeCategoryStock(input)
				if len(got) != 1 || got[0].Status != StockKnown || got[0].Available == nil || *got[0].Available != 2 {
					t.Errorf("неверный конкурентный результат: %+v", got)
					return
				}
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(before, stockSnapshot(t, input)) {
		t.Error("конкурентные вызовы изменили вход")
	}
}

func TestStockUnscopedRowWarning(t *testing.T) {
	input := stockFixture()
	input.Status = bnovo.SearchStatusPartial
	input.Categories = append(input.Categories, bnovo.RoomCategory{ID: "459946"})
	input.Variants = append(input.Variants, bnovo.AccommodationVariant{ID: "459946", CategoryID: "459946", Adults: 2})
	other := input.Offers[0]
	other.CategoryID, other.VariantID = "459946", "459946"
	input.Offers = append(input.Offers, other)
	// Context здесь — ID тарифа, даже если его строка совпала с ID категории.
	// При отсутствующем room ID нельзя привязать повреждённую строку к одной категории.
	warning := bnovo.ParseWarning{Code: "offer_room_id_missing", Context: "561429", Message: "room identity missing"}
	input.Warnings = []bnovo.ParseWarning{warning}
	for _, stock := range NormalizeCategoryStock(input) {
		if stock.Status != StockUnknown || stock.Available != nil || len(stock.Warnings) != 1 || stock.Warnings[0] != warning {
			t.Errorf("неопределённый контекст ошибочно сузил область предупреждения: %+v", stock)
		}
	}
}
