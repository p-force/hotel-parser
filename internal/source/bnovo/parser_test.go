package bnovo

import (
	"fmt"
	"testing"
	"time"
)

func parseUnavailableFixture(t *testing.T, req SearchRequest, message string) SearchResult {
	t.Helper()
	// Единственная категория без тарифов: итоговый статус зависит от того,
	// подтверждает ли сообщение недоступность именно на запрошенные даты.
	body := fmt.Sprintf(`<!doctype html>
<html><head><script>
const $enabledRoomTypes = {"561429":{"id":561429,"name":"Stadium"}};
const $plansForJs = {};
const account_currency_iso4217 = "RUB";
</script></head><body>
<div class="room" data-roomtype-id="561429">
  <div class="tariff__nodates">%s</div>
</div>
</body></html>`, message)
	result, err := Parse([]byte(body), req)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Categories) != 1 || result.Categories[0].ID != "561429" {
		t.Fatalf("неожиданные категории: %+v", result.Categories)
	}
	if len(result.CategoryAvailability) != 1 {
		t.Fatalf("ожидалось одно состояние категории: %+v", result.CategoryAvailability)
	}
	state := result.CategoryAvailability[0]
	if state.CategoryID != "561429" || state.HasOffers || len(result.Offers) != 0 {
		t.Fatalf("неожиданное состояние категории: %+v; предложения: %+v", state, result.Offers)
	}
	if state.Reason != message {
		t.Errorf("исходное сообщение не сохранено: %q", state.Reason)
	}
	return result
}

func assertUnavailablePeriodWarning(t *testing.T, result SearchResult) {
	t.Helper()
	if result.CategoryAvailability[0].ConfirmedUnavailable {
		t.Error("нельзя подтверждать недоступность по сообщению с неверными датами")
	}
	if result.Status == SearchStatusNoOffers {
		t.Fatal("сообщение с неверными датами не должно устанавливать SearchStatusNoOffers")
	}
	if result.Status != SearchStatusPartial {
		t.Errorf("статус = %q, ожидался %q", result.Status, SearchStatusPartial)
	}
	for _, warning := range result.Warnings {
		if warning.Code == "unavailable_period_mismatch" && warning.Context == "561429" && warning.Message != "" {
			return
		}
	}
	t.Errorf("нет предупреждения о датах для категории: %+v", result.Warnings)
}

func TestParseUnavailableMatchingDates(t *testing.T) {
	req := testSearchRequest()
	result := parseUnavailableFixture(t, req,
		"Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026")
	if !result.CategoryAvailability[0].ConfirmedUnavailable {
		t.Error("совпадающие даты должны подтверждать недоступность")
	}
	if result.Status != SearchStatusNoOffers {
		t.Errorf("статус = %q, ожидался %q", result.Status, SearchStatusNoOffers)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("неожиданные предупреждения: %+v", result.Warnings)
	}
}

func TestParseUnavailableMismatchedDates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
	}{
		{"check_in", "Здесь нет свободных мест с 25 октября 2026 по 28 октября 2026"},
		{"check_out", "Здесь нет свободных мест с 26 октября 2026 по 29 октября 2026"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := parseUnavailableFixture(t, testSearchRequest(), tc.message)
			assertUnavailablePeriodWarning(t, result)
		})
	}
}

func TestParseUnavailableInvalidDate(t *testing.T) {
	req := testSearchRequest()
	// time.Date нормализует 31 февраля 2026 в 3 марта. Запрашиваем этот
	// период, чтобы обнаружить ошибочное принятие нормализованной даты.
	req.CheckIn = time.Date(2026, time.March, 3, 0, 0, 0, 0, req.CheckIn.Location())
	req.CheckOut = req.CheckIn.AddDate(0, 0, 2)
	result := parseUnavailableFixture(t, req,
		"Здесь нет свободных мест с 31 февраля 2026 по 5 марта 2026")
	assertUnavailablePeriodWarning(t, result)
}
