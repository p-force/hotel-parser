package bnovo

import (
	"fmt"
	"reflect"
	"strings"
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

func TestParseCategoryAvailabilityStatus(t *testing.T) {
	const offer = `<div class="plan_row" data-plan-id="1"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="4910.50"></td>
<td><select class="selectAvailable" data-room-id="561429" data-real-room-id="561429" data-plan-id="1" data-available="1"><option value="1">1</option></select></td>
</tr></table></div>`
	const unavailable = `<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>`
	for _, tc := range []struct {
		name       string
		contents   []string
		wantStatus SearchStatus
		wantOffers int
		unknownID  string
	}{
		{"offers_and_unavailable", []string{offer, unavailable, unavailable}, SearchStatusSuccess, 1, ""},
		{"offers_and_unknown", []string{offer, ""}, SearchStatusPartial, 1, "459946"},
		{"all_unavailable", []string{unavailable, unavailable, unavailable}, SearchStatusNoOffers, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body strings.Builder
			body.WriteString(`<html><head><script>
const $enabledRoomTypes = {"561429":{"id":561429,"name":"Stadium"},"459946":{"id":459946,"name":"Garden"},"639687":{"id":639687,"name":"Premium"}};
const $plansForJs = {"1":{"id":1,"name":"Основной","roomTypeIds":["561429"]}};
const account_currency_iso4217 = "RUB";
</script></head><body>`)
			ids := []string{"561429", "459946", "639687"}
			for i, content := range tc.contents {
				fmt.Fprintf(&body, `<div class="room" data-roomtype-id="%s">%s</div>`, ids[i], content)
			}
			body.WriteString("</body></html>")
			result, err := Parse([]byte(body.String()), testSearchRequest())
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.wantStatus {
				t.Errorf("статус = %q, ожидался %q; предупреждения: %+v", result.Status, tc.wantStatus, result.Warnings)
			}
			if len(result.Offers) != tc.wantOffers || len(result.CategoryAvailability) != len(tc.contents) {
				t.Fatalf("неожиданные предложения или состояния: %+v", result)
			}
			contentsByID := make(map[string]string, len(tc.contents))
			for i, content := range tc.contents {
				contentsByID[ids[i]] = content
			}
			for _, state := range result.CategoryAvailability {
				content, exists := contentsByID[state.CategoryID]
				wantHasOffers := content == offer
				wantUnavailable := content == unavailable
				if !exists || state.HasOffers != wantHasOffers || state.ConfirmedUnavailable != wantUnavailable {
					t.Errorf("неверное состояние категории: %+v", state)
				}
			}
			if tc.unknownID == "" {
				if len(result.Warnings) != 0 {
					t.Errorf("неожиданные предупреждения: %+v", result.Warnings)
				}
			} else if len(result.Warnings) != 1 || result.Warnings[0].Code != "category_availability_unknown" || result.Warnings[0].Context != tc.unknownID {
				t.Errorf("ожидалось предупреждение о неизвестной категории: %+v", result.Warnings)
			}
		})
	}
}

func TestParseDocumentStructure(t *testing.T) {
	const metadata = `<script>
const $enabledRoomTypes = {"561429":{"id":561429,"name":"Stadium"}};
const $plansForJs = {"1":{"id":1,"name":"Основной","roomTypeIds":["561429"]}};
const account_currency_iso4217 = "RUB";
</script>`
	const offer = `<div class="plan_row" data-plan-id="1"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="4910.50"></td>
<td><select class="selectAvailable" data-room-id="561429" data-real-room-id="561429" data-plan-id="1"><option value="1">1</option></select></td>
</tr></table></div>`
	const unavailable = `<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>`
	page := func(scripts, content string) string {
		return `<html><head>` + scripts + `</head><body><div class="room" data-roomtype-id="561429">` + content + `</div></body></html>`
	}
	full := page(metadata, offer)
	for _, tc := range []struct {
		name, body                 string
		status                     SearchStatus
		errorContains, warningCode string
		wantOffers                 int
	}{
		{name: "full_offers", body: full, status: SearchStatusSuccess, wantOffers: 1},
		{name: "confirmed_unavailable", body: page(metadata, unavailable), status: SearchStatusNoOffers},
		{name: "error_page", body: "<html><body><h1>Internal Server Error</h1></body></html>", status: SearchStatusFailed, errorContains: "room metadata"},
		{name: "missing_room_metadata", body: page("", offer), status: SearchStatusFailed, errorContains: "$enabledRoomTypes JSON not found"},
		{name: "corrupted_room_metadata", body: page(strings.Replace(metadata, `{"id":561429,"name":"Stadium"}`, `{"id":`, 1), offer), status: SearchStatusFailed, errorContains: "decode enabled room types"},
		{name: "missing_room_cards", body: `<html><head>` + metadata + `</head><body>` + offer + `</body></html>`, status: SearchStatusFailed, errorContains: "room cards not found"},
		{name: "incomplete_price", body: page(metadata, strings.Replace(offer, ` data-tariff-price="4910.50"`, "", 1)), status: SearchStatusPartial, warningCode: "offer_price_invalid", wantOffers: 1},
		{name: "incomplete_tariff_structure", body: page(metadata, `<div class="plan_row" data-plan-id="1"><table><tr><td>Данные не получены</td></tr></table></div>`), status: SearchStatusPartial, warningCode: "category_availability_unknown"},
		{name: "no_search_structure", body: page(metadata, ""), status: SearchStatusFailed, errorContains: "no recognizable search state"},
		// HTML-парсер восстанавливает пропущенные закрывающие теги ячеек.
		{name: "recoverable_html", body: strings.ReplaceAll(full, "</td>", ""), status: SearchStatusSuccess, wantOffers: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Parse([]byte(tc.body), testSearchRequest())
			if result.Status != tc.status {
				t.Errorf("статус = %q, ожидался %q; ошибка: %v; предупреждения: %+v", result.Status, tc.status, err, result.Warnings)
			}
			if tc.errorContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorContains) {
					t.Fatalf("ожидалась ошибка с %q, получено: %v", tc.errorContains, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Categories) != 1 || len(result.Offers) != tc.wantOffers {
				t.Errorf("неожиданное число категорий или предложений: %+v", result)
			}
			if tc.warningCode == "" {
				if len(result.Warnings) != 0 {
					t.Errorf("неожиданные предупреждения: %+v", result.Warnings)
				}
			} else {
				found := false
				for _, warning := range result.Warnings {
					if warning.Code == tc.warningCode {
						found = true
					}
				}
				if !found {
					t.Errorf("нет предупреждения %q: %+v", tc.warningCode, result.Warnings)
				}
			}
		})
	}
}

func TestValidateAndDeduplicateOffers(t *testing.T) {
	categories := []RoomCategory{{ID: "c1"}, {ID: "c2"}}
	variants := []AccommodationVariant{
		{ID: "v1", CategoryID: "c1"},
		{ID: "v2", CategoryID: "c2"},
		{ID: "v3", CategoryID: "c1"},
	}
	available := 2
	guestAvailability := "2"
	a := RateOffer{CategoryID: "c1", VariantID: "v1", RateID: "r1", Price: "100.00", Currency: "RUB", Visible: true,
		Availability: AvailabilityEvidence{DataAvailable: &available, DataAvailableForGuests: &guestAvailability, SelectableQuantities: []int{1, 2}}}
	b := a
	b.RateID, b.Price = "r2", "200.00"
	c := a
	c.CategoryID, c.VariantID = "c2", "v2"
	d := a
	d.VariantID = "v3"
	changed := func(change func(*RateOffer)) RateOffer { result := a; change(&result); return result }
	priceConflict := changed(func(o *RateOffer) { o.Price = "101.00" })
	type warningIdentity struct{ code, context string }
	conflictWarning := []warningIdentity{{"offer_duplicate_conflict", "c1/v1/r1"}}
	type testCase struct {
		name         string
		offers, want []RateOffer
		warnings     []warningIdentity
	}
	identicalCopy := changed(func(o *RateOffer) {
		value := *o.Availability.DataAvailable
		guests := *o.Availability.DataAvailableForGuests
		o.Availability.DataAvailable = &value
		o.Availability.DataAvailableForGuests = &guests
		o.Availability.SelectableQuantities = append([]int(nil), o.Availability.SelectableQuantities...)
	})
	cases := []testCase{
		{"unique_order", []RateOffer{b, a, c, d}, []RateOffer{b, a, c, d}, nil},
		{"identical_pair", []RateOffer{a, a}, []RateOffer{a}, nil},
		{"identical_independent_values", []RateOffer{a, identicalCopy}, []RateOffer{a}, nil},
		{"identical_with_other_key", []RateOffer{a, a, b, b}, []RateOffer{a, b}, nil},
		{"price_conflict", []RateOffer{a, priceConflict}, []RateOffer{}, conflictWarning},
		{"A_B_conflictingA", []RateOffer{a, b, priceConflict}, []RateOffer{b}, conflictWarning},
		{"conflict_stays_excluded", []RateOffer{a, priceConflict, a}, []RateOffer{}, conflictWarning},
		{"duplicate_then_conflict", []RateOffer{a, a, priceConflict}, []RateOffer{}, conflictWarning},
		{"survivor_order", []RateOffer{b, a, c, priceConflict, d}, []RateOffer{b, c, d}, conflictWarning},
	}
	for _, tc := range []struct {
		name   string
		change func(*RateOffer)
	}{
		{"visible", func(o *RateOffer) { o.Visible = false }},
		{"data_available", func(o *RateOffer) { value := 3; o.Availability.DataAvailable = &value }},
		{"guests", func(o *RateOffer) { value := "3"; o.Availability.DataAvailableForGuests = &value }},
		{"quantities", func(o *RateOffer) { o.Availability.SelectableQuantities = []int{1} }},
		{"disabled", func(o *RateOffer) { o.Availability.SelectDisabled = true }},
	} {
		cases = append(cases, testCase{tc.name, []RateOffer{a, changed(tc.change)}, []RateOffer{}, conflictWarning})
	}
	for _, tc := range []struct {
		name, code string
		change     func(*RateOffer)
	}{
		{"missing_category", "offer_identity_missing", func(o *RateOffer) { o.CategoryID = "" }},
		{"missing_variant", "offer_identity_missing", func(o *RateOffer) { o.VariantID = "" }},
		{"missing_rate", "offer_identity_missing", func(o *RateOffer) { o.RateID = "" }},
		{"unknown_category", "offer_category_unknown", func(o *RateOffer) { o.CategoryID = "unknown" }},
		{"unknown_variant", "offer_variant_unknown", func(o *RateOffer) { o.VariantID = "unknown" }},
		{"category_mismatch", "offer_category_mismatch", func(o *RateOffer) { o.VariantID = "v2" }},
	} {
		invalid := changed(tc.change)
		cases = append(cases, testCase{tc.name, []RateOffer{invalid, b}, []RateOffer{b}, []warningIdentity{{tc.code, invalid.CategoryID + "/" + invalid.VariantID + "/" + invalid.RateID}}})
	}
	// Все перестановки A, B, A': независимо от порядка остаётся только B.
	for i, offers := range [][]RateOffer{
		{a, b, priceConflict}, {a, priceConflict, b}, {b, a, priceConflict},
		{b, priceConflict, a}, {priceConflict, a, b}, {priceConflict, b, a},
	} {
		cases = append(cases, testCase{fmt.Sprintf("permutation_%d", i), offers, []RateOffer{b}, conflictWarning})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, preserveExisting := range []bool{false, true} {
				t.Run(fmt.Sprintf("existing_warnings=%t", preserveExisting), func(t *testing.T) {
					initial := ParseWarning{Code: "existing_warning", Message: "keep diagnostic", Context: "source"}
					var inputWarnings []ParseWarning
					if preserveExisting {
						inputWarnings = []ParseWarning{initial}
					}
					got, warnings := validateAndDeduplicateOffers(tc.offers, categories, variants, inputWarnings)
					// Сравниваем все значения и порядок; лишний конфликтующий ключ тоже провалит проверку.
					if !reflect.DeepEqual(got, tc.want) {
						t.Errorf("предложения = %+v, ожидались %+v", got, tc.want)
					}
					if len(warnings) != len(inputWarnings)+len(tc.warnings) {
						t.Fatalf("неверное число предупреждений: %+v", warnings)
					}
					if preserveExisting && warnings[0] != initial {
						t.Error("потеряно исходное предупреждение")
					}
					for i, want := range tc.warnings {
						warning := warnings[i+len(inputWarnings)]
						if warning.Code != want.code || warning.Context != want.context || warning.Message == "" {
							t.Errorf("неверное предупреждение: %+v, ожидалось %+v", warning, want)
						}
					}
				})
			}
		})
	}
}

func TestParseConflictingOffersAvailability(t *testing.T) {
	const metadata = `<script>
const $enabledRoomTypes = {"561429":{"id":561429,"name":"Stadium"}};
const $plansForJs = {"1":{"id":1,"name":"Основной","roomTypeIds":["561429"]},"2":{"id":2,"name":"Другой","roomTypeIds":["561429"]}};
const account_currency_iso4217 = "RUB";
</script>`
	row := func(rate, price string) string {
		return fmt.Sprintf(`<div class="plan_row" data-plan-id="%s"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="%s"></td>
<td><select class="selectAvailable" data-room-id="561429" data-real-room-id="561429" data-plan-id="%s"><option value="1">1</option></select></td>
</tr></table></div>`, rate, price, rate)
	}
	for _, keepOther := range []bool{false, true} {
		t.Run(fmt.Sprintf("other_valid_offer=%t", keepOther), func(t *testing.T) {
			content := row("1", "100") + row("1", "200") + row("1", "100")
			if keepOther {
				content += row("2", "300")
			}
			body := metadata + `<div class="room" data-roomtype-id="561429">` + content + `</div>`
			result, err := Parse([]byte(body), testSearchRequest())
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != SearchStatusPartial {
				t.Errorf("ожидался partial, получен %q", result.Status)
			}
			wantOffers := 0
			if keepOther {
				wantOffers = 1
			}
			if len(result.Offers) != wantOffers {
				t.Fatalf("неверные предложения: %+v", result.Offers)
			}
			if keepOther && (result.Offers[0].RateID != "2" || result.Offers[0].Price != "300") {
				t.Errorf("неверное оставшееся предложение: %+v", result.Offers[0])
			}
			if len(result.CategoryAvailability) != 1 || result.CategoryAvailability[0].HasOffers != keepOther || result.CategoryAvailability[0].ConfirmedUnavailable {
				t.Errorf("неверная доступность: %+v", result.CategoryAvailability)
			}
			conflicts := 0
			for _, warning := range result.Warnings {
				if warning.Code == "offer_duplicate_conflict" && warning.Context == "561429/561429/1" {
					conflicts++
				}
			}
			if conflicts != 1 {
				t.Errorf("ожидалось одно предупреждение о конфликте: %+v", result.Warnings)
			}
		})
	}
}
