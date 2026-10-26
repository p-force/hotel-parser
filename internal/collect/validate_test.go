package collect

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func TestMissingConfiguredCategory(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "arena33.json"))
	if err != nil {
		t.Fatal(err)
	}
	req := bnovo.SearchRequest{
		PropertyID: cfg.Source.PropertyID,
		CheckIn:    time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		CheckOut:   time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
		Adults:     2, Language: "ru",
	}
	for _, tc := range []struct {
		name, content string
		parsedStatus  bnovo.SearchStatus
	}{
		{"offers", `<div class="plan_row" data-plan-id="1"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="4910.50"></td>
<td><select class="selectAvailable" data-room-id="561429" data-real-room-id="561429" data-plan-id="1"><option value="1">1</option></select></td>
</tr></table></div>`, bnovo.SearchStatusSuccess},
		{"unavailable", `<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>`, bnovo.SearchStatusNoOffers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`<html><head><script>
const $enabledRoomTypes = {"561429":{"id":561429,"name":"Stadium"}};
const $plansForJs = {"1":{"id":1,"name":"Основной","roomTypeIds":["561429"]}};
const account_currency_iso4217 = "RUB";
</script></head><body><div class="room" data-roomtype-id="561429">%s</div></body></html>`, tc.content)
			result, err := bnovo.Parse([]byte(body), req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.parsedStatus || len(result.Warnings) != 0 {
				t.Fatalf("неожиданный результат парсера до проверки конфигурации: %+v", result)
			}
			offersBefore := len(result.Offers)
			ValidateExpectedCategories(&result, cfg.Inventory)
			if result.Status != bnovo.SearchStatusPartial {
				t.Errorf("ожидался partial, получен %q", result.Status)
			}
			missing := map[string]bool{"459946": true, "639687": true}
			if len(result.Warnings) != 2 {
				t.Fatalf("ожидалось два предупреждения: %+v", result.Warnings)
			}
			for _, warning := range result.Warnings {
				if warning.Code != "expected_category_missing" || !missing[warning.Context] || warning.Message == "" {
					t.Errorf("неверное предупреждение: %+v", warning)
				}
				delete(missing, warning.Context)
			}
			if len(missing) != 0 {
				t.Errorf("нет предупреждений: %v", missing)
			}
			if len(result.Offers) != offersBefore || len(result.Categories) != 1 || len(result.CategoryAvailability) != 1 {
				t.Error("сверка должна сохранять данные источника")
			}
		})
	}
}

func TestExpectedCategoriesPreserveStatus(t *testing.T) {
	inventory := config.InventoryConfig{Categories: []config.RoomCategory{{ID: "stadium", SourceRoomID: "561429"}}}
	for _, status := range []bnovo.SearchStatus{bnovo.SearchStatusSuccess, bnovo.SearchStatusPartial, bnovo.SearchStatusNoOffers, bnovo.SearchStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			result := bnovo.SearchResult{Status: status, CategoryAvailability: []bnovo.CategoryAvailability{{CategoryID: "561429"}}}
			ValidateExpectedCategories(&result, inventory)
			if result.Status != status || len(result.Warnings) != 0 {
				t.Errorf("полный ответ не должен изменяться: %+v", result)
			}
		})
	}
	t.Run("failed_missing", func(t *testing.T) {
		result := bnovo.SearchResult{Status: bnovo.SearchStatusFailed}
		ValidateExpectedCategories(&result, inventory)
		if result.Status != bnovo.SearchStatusFailed {
			t.Errorf("failed не должен превращаться в partial: %q", result.Status)
		}
	})
}
