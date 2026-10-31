package collect

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

const exactUnavailable = `<div class="tariff__nodates">Здесь нет свободных мест с 30 декабря 2026 по 31 декабря 2026</div>`

func unavailableHotel(t *testing.T, id string) (config.Config, bnovo.SearchRequest) {
	t.Helper()
	hotel, err := config.Load("../../configs/" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	in := time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)
	return hotel, bnovo.SearchRequest{PropertyID: hotel.Source.PropertyID, CheckIn: in, CheckOut: in.AddDate(0, 0, 1), Adults: 1, Language: "ru"}
}

// Обезличенная структура Bnovo: ID из конфигурации, точные даты, без plans JS.
// Отсутствие ключа contents означает отсутствие карточки, а не недоступность.
func confirmedUnavailableHTML(categories []config.RoomCategory, contents map[string]string) []byte {
	var body strings.Builder
	body.WriteString(`<html><head><script>const $enabledRoomTypes = {`)
	for i, category := range categories {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `%q:{"id":%s,"name":%q,"amenities":{"1":{"value":"12"}}}`, category.SourceRoomID, category.SourceRoomID, category.Name)
	}
	body.WriteString(`}; const account_currency_iso4217 = "RUB";</script></head><body>`)
	for _, category := range categories {
		if content, ok := contents[category.SourceRoomID]; ok {
			fmt.Fprintf(&body, `<div class="room" data-roomtype-id="%s">%s</div>`, category.SourceRoomID, content)
		}
	}
	body.WriteString(`</body></html>`)
	return []byte(body.String())
}
func unavailableContents(hotel config.Config, content string) map[string]string {
	contents := make(map[string]string, len(hotel.Inventory.Categories))
	for _, category := range hotel.Inventory.Categories {
		contents[category.SourceRoomID] = content
	}
	return contents
}
func publicOfferHTML(category string) string {
	return fmt.Sprintf(`<div class="plan_row" data-plan-id="10" data-available-for-guests="1"><table><tr data-available="1">
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i></span></span></td>
<td data-tariff-price="4910.50"></td><td><select class="selectAvailable" data-room-id="%s" data-real-room-id="%s" data-plan-id="10" data-available="3"><option value="0">0</option><option value="1">1</option><option value="3">3</option></select></td>
</tr></table></div>`, category, category)
}
func warningCode(warnings []bnovo.ParseWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestConfirmedUnavailableWithoutPlansForConfiguredHotels(t *testing.T) {
	for _, tc := range []struct {
		id         string
		categories int
	}{{"arena33", 3}, {"lindsay", 10}} {
		t.Run(tc.id, func(t *testing.T) {
			hotel, request := unavailableHotel(t, tc.id)
			result, err := bnovo.Parse(confirmedUnavailableHTML(hotel.Inventory.Categories, unavailableContents(hotel, exactUnavailable)), request)
			if err != nil {
				t.Fatal(err)
			}
			ValidateExpectedCategories(&result, hotel.Inventory)
			if result.Status != bnovo.SearchStatusNoOffers || len(result.Categories) != tc.categories || len(result.CategoryAvailability) != tc.categories || len(result.Offers) != 0 || len(result.Variants) != 0 || len(result.Warnings) != 0 {
				t.Fatalf("not a complete no_offers response: %+v", result)
			}
			if !reflect.DeepEqual(result.Request, request) {
				t.Fatal("scenario changed")
			}
			before := stockSnapshot(t, result)
			stocks := NormalizeCategoryStock(result)
			if len(stocks) != tc.categories {
				t.Fatalf("stocks=%+v", stocks)
			}
			expected := make(map[string]string)
			for _, category := range hotel.Inventory.Categories {
				expected[category.SourceRoomID] = category.Name
			}
			for i, stock := range stocks {
				if stock.Status != StockKnown || stock.Available == nil || *stock.Available != 0 || len(stock.Warnings) != 0 {
					t.Errorf("not confirmed zero: %+v", stock)
				}
				state := result.CategoryAvailability[i]
				if state.CategoryID != stock.CategoryID || !state.ConfirmedUnavailable || state.HasOffers || state.Reason != "Здесь нет свободных мест с 30 декабря 2026 по 31 декабря 2026" {
					t.Errorf("evidence lost: %+v", state)
				}
				if name, ok := expected[stock.CategoryID]; !ok || result.Categories[i].Name != name || result.Categories[i].AreaM2 == nil || *result.Categories[i].AreaM2 != 12 {
					t.Errorf("category identity/area lost: %+v", result.Categories[i])
				}
				delete(expected, stock.CategoryID)
			}
			if len(expected) != 0 || !bytes.Equal(before, stockSnapshot(t, result)) {
				t.Fatal("category missing or normalization mutated input")
			}
		})
	}
}

func TestUnavailabilityWithoutPlansRemainsConservative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content func(string) string
		change  func([]byte) []byte
		missing bool
		status  bnovo.SearchStatus
		stock   StockStatus
		warning string
	}{
		{name: "missing_expected_category", content: func(string) string { return exactUnavailable }, missing: true, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "expected_category_missing"},
		{name: "date_mismatch", content: func(string) string {
			return strings.ReplaceAll(exactUnavailable, "30 декабря", "29 декабря")
		}, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "unavailable_period_mismatch"},
		{name: "invalid_date", content: func(string) string {
			return strings.ReplaceAll(exactUnavailable, "30 декабря", "31 ноября")
		}, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "unavailable_period_mismatch"},
		{name: "no_exact_signal", content: func(string) string { return `<div class="tariff__nodates"></div>` }, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "category_availability_unknown"},
		{name: "generic_no_offers", content: func(string) string { return `<div class="tariff__nodates">Нет предложений</div>` }, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "category_availability_unknown"},
		{name: "minimum_stay", content: func(string) string {
			return `<div class="tariff__nodates">Минимальный срок проживания — 2 ночи</div>`
		}, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "category_availability_unknown"},
		{name: "minimum_stay_with_exact_dates", content: func(string) string {
			return strings.Replace(exactUnavailable, "</div>", ". Минимальный срок проживания — 2 ночи</div>", 1)
		}, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "unavailable_period_mismatch"},
		{name: "ambiguous_prefixed_message", content: func(string) string {
			return strings.Replace(exactUnavailable, "Здесь", "Ограничение тарифа. Здесь", 1)
		}, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "unavailable_period_mismatch"},
		{name: "contradictory_available_offer", content: func(id string) string { return exactUnavailable + publicOfferHTML(id) }, status: bnovo.SearchStatusPartial, stock: StockConflict, warning: "availability_conflict"},
		{name: "incomplete_tariff_block", content: func(string) string {
			return exactUnavailable + `<div class="plan_row" data-plan-id="10" data-available-for-guests="1"><table><tr><td>Неполные данные</td></tr></table></div>`
		}, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "plans_metadata_missing"},
		{name: "potential_offers_without_plans", content: publicOfferHTML, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "plans_metadata_missing"},
		{name: "corrupted_plans_with_offers", content: publicOfferHTML, change: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`const account_currency`), []byte(`const $plansForJs = {broken}; const account_currency`), 1)
		}, status: bnovo.SearchStatusPartial, stock: StockUnknown, warning: "plans_metadata_missing"},
		{name: "unknown_category_list", content: func(string) string { return exactUnavailable }, change: func(body []byte) []byte {
			return bytes.Replace(body, []byte("$enabledRoomTypes"), []byte("$missingRoomTypes"), 1)
		}, status: bnovo.SearchStatusFailed},
		{name: "corrupted_category_list", content: func(string) string { return exactUnavailable }, change: func(body []byte) []byte {
			return bytes.Replace(body, []byte("$enabledRoomTypes = {"), []byte("$enabledRoomTypes = {broken,"), 1)
		}, status: bnovo.SearchStatusFailed},
		{name: "no_room_cards", content: func(string) string { return exactUnavailable }, change: func(body []byte) []byte {
			return bytes.ReplaceAll(body, []byte(`class="room"`), []byte(`class="other"`))
		}, status: bnovo.SearchStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hotel, request := unavailableHotel(t, "arena33")
			contents := make(map[string]string)
			for _, category := range hotel.Inventory.Categories {
				contents[category.SourceRoomID] = tc.content(category.SourceRoomID)
			}
			missingID := hotel.Inventory.Categories[0].SourceRoomID
			if tc.missing {
				delete(contents, missingID)
			}
			body := confirmedUnavailableHTML(hotel.Inventory.Categories, contents)
			if tc.change != nil {
				body = tc.change(body)
			}
			result, err := bnovo.Parse(body, request)
			if (err != nil) != (tc.status == bnovo.SearchStatusFailed) {
				t.Fatalf("unexpected parse error: %v", err)
			}
			ValidateExpectedCategories(&result, hotel.Inventory)
			if result.Status != tc.status || (tc.warning != "" && !warningCode(result.Warnings, tc.warning)) {
				t.Fatalf("status/warnings: %+v error=%v", result, err)
			}
			before := stockSnapshot(t, result)
			stocks := NormalizeCategoryStock(result)
			if err != nil {
				if len(stocks) != 0 {
					t.Fatalf("invented stock for failed HTML: %+v", stocks)
				}
				return
			}
			count := 3
			if tc.missing {
				count--
			}
			if len(stocks) != count {
				t.Fatalf("stocks=%+v", stocks)
			}
			for _, stock := range stocks {
				if stock.Status != tc.stock || stock.Available != nil {
					t.Errorf("false known stock: %+v", stock)
				}
				if tc.missing && stock.CategoryID == missingID {
					t.Error("invented absent category")
				}
				if tc.missing && !warningCode(stock.Warnings, "expected_category_missing") {
					t.Errorf("missing completeness diagnostic: %+v", stock)
				}
			}
			if !bytes.Equal(before, stockSnapshot(t, result)) {
				t.Fatal("normalizer mutated input")
			}
		})
	}
}

func TestMissingPlansWarningScopeInMixedAvailability(t *testing.T) {
	for _, plans := range []bool{false, true} {
		t.Run(fmt.Sprint("plans=", plans), func(t *testing.T) {
			hotel, request := unavailableHotel(t, "arena33")
			first, second, third := hotel.Inventory.Categories[0].SourceRoomID, hotel.Inventory.Categories[1].SourceRoomID, hotel.Inventory.Categories[2].SourceRoomID
			contents := unavailableContents(hotel, exactUnavailable)
			contents[second] = publicOfferHTML(second)
			body := confirmedUnavailableHTML(hotel.Inventory.Categories, contents)
			if plans {
				body = bytes.Replace(body, []byte(`const account_currency`), []byte(`const $plansForJs = {"10":{"id":10,"name":"Rate"}}; const account_currency`), 1)
			}
			result, err := bnovo.Parse(body, request)
			if err != nil {
				t.Fatal(err)
			}
			ValidateExpectedCategories(&result, hotel.Inventory)
			expectedStatus := bnovo.SearchStatusPartial
			if plans {
				expectedStatus = bnovo.SearchStatusSuccess
			}
			if result.Status != expectedStatus || len(result.Offers) != 1 {
				t.Fatalf("mixed status: %+v", result)
			}
			if plans {
				if len(result.Warnings) != 0 {
					t.Fatal(result.Warnings)
				}
			} else if len(result.Warnings) != 1 || result.Warnings[0].Code != "plans_metadata_missing" || result.Warnings[0].Context != second {
				t.Fatalf("wrong warning scope: %+v", result.Warnings)
			}
			stocks := NormalizeCategoryStock(result)
			for _, stock := range stocks {
				switch stock.CategoryID {
				case first, third:
					if stock.Status != StockKnown || stock.Available == nil || *stock.Available != 0 || len(stock.Warnings) != 0 {
						t.Errorf("unrelated tariff warning blocked exact unavailability: %+v", stock)
					}
				case second:
					if plans {
						if stock.Status != StockKnown || stock.Available == nil || *stock.Available != 3 {
							t.Errorf("available stock lost: %+v", stock)
						}
					} else if stock.Status != StockUnknown || stock.Available != nil || !warningCode(stock.Warnings, "plans_metadata_missing") {
						t.Errorf("missing metadata accepted: %+v", stock)
					}
				default:
					t.Fatalf("invented category: %+v", stock)
				}
			}
			offer := result.Offers[0]
			if offer.CategoryID != second || offer.VariantID != second || offer.RateID != "10" || offer.Price != "4910.50" || offer.Currency != "RUB" || !offer.Visible || offer.Availability.DataAvailable == nil || *offer.Availability.DataAvailable != 3 || !reflect.DeepEqual(offer.Availability.SelectableQuantities, []int{1, 3}) || offer.Availability.DataAvailableForGuests == nil || *offer.Availability.DataAvailableForGuests != "1" || offer.Availability.SelectDisabled {
				t.Fatalf("offer evidence changed: %+v", offer)
			}
		})
	}
}

func TestUnavailabilityRequiresCategoryIdentityAndPreservesWarnings(t *testing.T) {
	hotel, request := unavailableHotel(t, "arena33")
	missingMetadata := hotel.Inventory.Categories[0].SourceRoomID
	body := confirmedUnavailableHTML(hotel.Inventory.Categories[1:], unavailableContents(hotel, exactUnavailable))
	body = bytes.Replace(body, []byte("</body>"), []byte(fmt.Sprintf(`<div class="room" data-roomtype-id="%s">%s</div></body>`, missingMetadata, exactUnavailable)), 1)
	result, err := bnovo.Parse(body, request)
	if err != nil {
		t.Fatal(err)
	}
	ValidateExpectedCategories(&result, hotel.Inventory)
	if result.Status != bnovo.SearchStatusPartial || len(result.Warnings) != 1 || result.Warnings[0].Code != "category_metadata_missing" || result.Warnings[0].Context != missingMetadata {
		t.Fatalf("identity diagnostic lost: %+v", result)
	}
	for _, stock := range NormalizeCategoryStock(result) {
		if stock.CategoryID == missingMetadata {
			if stock.Status != StockUnknown || stock.Available != nil || !warningCode(stock.Warnings, "category_metadata_missing") {
				t.Errorf("unidentified category acquired zero: %+v", stock)
			}
		} else if stock.Status != StockKnown || stock.Available == nil || *stock.Available != 0 {
			t.Errorf("independent category lost: %+v", stock)
		}
	}
	// Диагностика с неопределённой привязкой не должна теряться при новом scoping.
	body = confirmedUnavailableHTML(hotel.Inventory.Categories, unavailableContents(hotel, exactUnavailable))
	for _, warning := range []bnovo.ParseWarning{
		{Code: "plans_metadata_missing", Context: "$plansForJs", Message: "unknown tariff scope"},
		{Code: "unavailable_period_mismatch", Message: "unknown date scope"},
		{Code: "future_availability_warning", Message: "unrecognized availability warning"},
	} {
		t.Run(warning.Code, func(t *testing.T) {
			result, err := bnovo.Parse(body, request)
			if err != nil {
				t.Fatal(err)
			}
			result.Status = bnovo.SearchStatusPartial
			result.Warnings = append(result.Warnings, warning)
			for _, stock := range NormalizeCategoryStock(result) {
				if stock.Status != StockUnknown || stock.Available != nil || !warningCode(stock.Warnings, warning.Code) {
					t.Errorf("global warning ignored: %+v", stock)
				}
			}
		})
	}
}

func TestUnscopedTariffMetadataStillBlocksConfirmedZero(t *testing.T) {
	hotel, request := unavailableHotel(t, "arena33")
	body := confirmedUnavailableHTML(hotel.Inventory.Categories, unavailableContents(hotel, exactUnavailable))
	// Повреждённый/непривязанный блок потенциального тарифа нельзя отнести к категории.
	body = bytes.Replace(body, []byte("</body>"), []byte(`<div class="plan_row" data-plan-id="10"></div></body>`), 1)
	result, err := bnovo.Parse(body, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != bnovo.SearchStatusPartial || len(result.Warnings) != 1 || result.Warnings[0].Code != "plans_metadata_missing" || result.Warnings[0].Context != "$plansForJs" {
		t.Fatalf("unscoped tariff not diagnosed: %+v", result)
	}
	for _, stock := range NormalizeCategoryStock(result) {
		if stock.Status != StockUnknown || stock.Available != nil {
			t.Errorf("unscoped potential tariff produced zero: %+v", stock)
		}
	}
}

func TestMissingPlansTracksCrossCategorySelector(t *testing.T) {
	hotel, request := unavailableHotel(t, "arena33")
	first, second, third := hotel.Inventory.Categories[0].SourceRoomID, hotel.Inventory.Categories[1].SourceRoomID, hotel.Inventory.Categories[2].SourceRoomID
	contents := unavailableContents(hotel, exactUnavailable)
	// Повреждённая вложенность: тариф в первой карточке ссылается на вторую.
	contents[first] += publicOfferHTML(second)
	result, err := bnovo.Parse(confirmedUnavailableHTML(hotel.Inventory.Categories, contents), request)
	if err != nil {
		t.Fatal(err)
	}
	contexts := make(map[string]bool)
	for _, warning := range result.Warnings {
		if warning.Code == "plans_metadata_missing" {
			contexts[warning.Context] = true
		}
	}
	if result.Status != bnovo.SearchStatusPartial || len(contexts) != 2 || !contexts[first] || !contexts[second] {
		t.Fatalf("tariff scope lost: %+v", result.Warnings)
	}
	for _, stock := range NormalizeCategoryStock(result) {
		switch stock.CategoryID {
		case first:
			if stock.Status != StockUnknown || stock.Available != nil {
				t.Errorf("parent category falsely unavailable: %+v", stock)
			}
		case second:
			if stock.Status != StockConflict || stock.Available != nil {
				t.Errorf("selector category contradiction lost: %+v", stock)
			}
		case third:
			if stock.Status != StockKnown || stock.Available == nil || *stock.Available != 0 {
				t.Errorf("unrelated category changed: %+v", stock)
			}
		}
	}
}
