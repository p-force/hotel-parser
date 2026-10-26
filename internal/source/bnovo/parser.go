package bnovo

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ParseCategories извлекает категории и варианты размещения
// из HTML Bnovo. Это временная точка входа для проверки
// первой части парсера до реализации полной Parse().
func ParseCategories(body []byte) (
	[]RoomCategory,
	[]AccommodationVariant,
	[]ParseWarning,
	error,
) {
	doc, err := parseDocument(body)
	if err != nil {
		return nil, nil, nil, err
	}

	return parseCategoriesAndVariants(doc)
}

// ParseOffers извлекает предложения из HTML Bnovo.
// Временная публичная точка входа до реализации Parse().
func ParseOffers(body []byte) (
	[]RateOffer,
	[]ParseWarning,
	error,
) {
	doc, err := parseDocument(body)
	if err != nil {
		return nil, nil, err
	}

	return parseRateOffers(doc)
}

// bnovoRoomType — промежуточная структура для JSON
// $enabledRoomTypes, встроенного в HTML Bnovo.
//
// Здесь описывается исходный тип номера Bnovo.
// Не все такие типы являются отдельными физическими категориями.
type bnovoRoomType struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`

	// В исследованном HTML amenities["1"].value содержит
	// площадь номера в квадратных метрах.
	Amenities map[string]struct {
		Value string `json:"value"`
	} `json:"amenities"`
}

// parseCategoriesAndVariants извлекает физические категории
// и варианты размещения из одного HTML-ответа.
//
// Функция не извлекает тарифы, цены и остатки.
// При частичных проблемах возвращает корректно найденные
// данные вместе с предупреждениями.
func parseCategoriesAndVariants(
	doc *html.Node,
) (
	[]RoomCategory,
	[]AccommodationVariant,
	[]ParseWarning,
	error,
) {
	roomTypes, err := parseEnabledRoomTypes(doc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"parse enabled room types: %w", err,
		)
	}

	categories := make(map[string]RoomCategory)
	variants := make(map[string]AccommodationVariant)
	var warnings []ParseWarning

	// Базовые категории могут присутствовать на странице,
	// даже если для них нет доступного тарифа.
	walkHTML(doc, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		categoryID := attr(node, "data-roomtype-id")
		if categoryID == "" {
			return
		}

		addCategory(
			categories,
			categoryID,
			roomTypes,
			&warnings,
		)
	})

	// Каждый селектор количества связывает конкретный
	// вариант размещения с базовой категорией.
	walkHTML(doc, func(node *html.Node) {
		if node.Type != html.ElementNode ||
			node.Data != "select" ||
			!hasClass(node, "selectAvailable") {
			return
		}

		variantID := attr(node, "data-room-id")
		categoryID := attr(node, "data-real-room-id")

		if variantID == "" || categoryID == "" {
			warnings = append(warnings, ParseWarning{
				Code:    "variant_id_missing",
				Message: "room variant or base category ID is missing",
				Context: "select.selectAvailable",
			})
			return
		}

		addCategory(
			categories,
			categoryID,
			roomTypes,
			&warnings,
		)

		// Число гостей извлекаем из тарифной строки,
		// содержащей текущий селектор.
		//
		// Нельзя определять его по всему блоку категории:
		// разные строки могут соответствовать разному
		// количеству гостей.
		row := closestAncestor(node, "tr")
		adults := parseGuestCount(row)

		if existing, ok := variants[variantID]; ok {
			if existing.CategoryID != categoryID ||
				(existing.Adults != 0 && adults != 0 &&
					existing.Adults != adults) {
				warnings = append(warnings, ParseWarning{
					Code:    "variant_conflict",
					Message: "variant has inconsistent category or guest count",
					Context: variantID,
				})
			}
			return
		}

		if adults == 0 {
			warnings = append(warnings, ParseWarning{
				Code:    "guest_count_unknown",
				Message: "could not determine guests for accommodation variant",
				Context: variantID,
			})
		}

		variants[variantID] = AccommodationVariant{
			ID:         variantID,
			CategoryID: categoryID,
			Adults:     adults,
		}
	})

	// Преобразуем map в отсортированные срезы.
	// Это обеспечивает детерминированный результат.
	resultCategories := make([]RoomCategory, 0, len(categories))
	for _, category := range categories {
		resultCategories = append(resultCategories, category)
	}

	sort.Slice(resultCategories, func(i, j int) bool {
		return resultCategories[i].ID < resultCategories[j].ID
	})

	resultVariants := make([]AccommodationVariant, 0, len(variants))
	for _, variant := range variants {
		resultVariants = append(resultVariants, variant)
	}

	sort.Slice(resultVariants, func(i, j int) bool {
		return resultVariants[i].ID < resultVariants[j].ID
	})

	return resultCategories, resultVariants, warnings, nil
}

// parseEnabledRoomTypes извлекает JSON-объект из JavaScript:
//
// const $enabledRoomTypes = {...};
//
// JavaScript не исполняется.
// Используем json.Decoder для чтения только JSON-литерала.
func parseEnabledRoomTypes(
	doc *html.Node,
) (map[string]bnovoRoomType, error) {
	const marker = "$enabledRoomTypes ="

	var rawJSON string

	walkHTML(doc, func(node *html.Node) {
		if rawJSON != "" ||
			node.Type != html.ElementNode ||
			node.Data != "script" {
			return
		}

		script := nodeText(node)
		index := strings.Index(script, marker)
		if index < 0 {
			return
		}

		after := strings.TrimSpace(
			script[index+len(marker):],
		)

		if strings.HasPrefix(after, "{") {
			rawJSON = after
		}
	})

	if rawJSON == "" {
		return nil, fmt.Errorf(
			"$enabledRoomTypes JSON not found",
		)
	}

	var roomTypes map[string]bnovoRoomType

	decoder := json.NewDecoder(strings.NewReader(rawJSON))

	// Decoder читает один JSON-объект и не требует,
	// чтобы после него заканчивался JavaScript.
	if err := decoder.Decode(&roomTypes); err != nil {
		return nil, fmt.Errorf(
			"decode enabled room types: %w", err,
		)
	}

	if len(roomTypes) == 0 {
		return nil, fmt.Errorf(
			"enabled room types JSON is empty",
		)
	}

	return roomTypes, nil
}

// addCategory добавляет базовую категорию один раз.
//
// Название и площадь берём из $enabledRoomTypes.
// Если метаданные отсутствуют, сохраняем ID и предупреждение.
func addCategory(
	categories map[string]RoomCategory,
	categoryID string,
	roomTypes map[string]bnovoRoomType,
	warnings *[]ParseWarning,
) {
	if _, exists := categories[categoryID]; exists {
		return
	}

	category := RoomCategory{
		ID: categoryID,
	}

	source, exists := roomTypes[categoryID]
	if !exists {
		*warnings = append(*warnings, ParseWarning{
			Code:    "category_metadata_missing",
			Message: "base category metadata not found",
			Context: categoryID,
		})
		categories[categoryID] = category
		return
	}

	category.Name = source.Name

	// Для исследованной версии Bnovo ключ "1"
	// соответствует площади в квадратных метрах.
	if amenity, ok := source.Amenities["1"]; ok {
		area, err := strconv.ParseFloat(
			strings.ReplaceAll(amenity.Value, ",", "."),
			64,
		)

		if err == nil && area > 0 {
			category.AreaM2 = &area
		} else if amenity.Value != "" {
			*warnings = append(*warnings, ParseWarning{
				Code:    "room_area_invalid",
				Message: "category area is not a positive number",
				Context: categoryID,
			})
		}
	}

	categories[categoryID] = category
}

// parseGuestCount определяет число взрослых,
// представленное в конкретной строке тарифа.
//
// Bnovo использует два подтверждённых формата:
// - несколько .guest-list__item с иконками взрослых;
// - один .guest-list__item со счётчиком xN.
//
// Это число гостей варианта размещения,
// а НЕ максимальная вместимость номера.
func parseGuestCount(row *html.Node) int {
	if row == nil {
		return 0
	}

	guestList := findFirstByClass(row, "guest-list")
	if guestList == nil {
		return 0
	}

	total := 0

	walkHTML(guestList, func(node *html.Node) {
		if node.Type != html.ElementNode ||
			!hasClass(node, "guest-list__item") {
			return
		}

		// Учитываем только элементы с иконкой взрослого.
		if findFirstByClass(node, "bnovobook-icon--adult") == nil {
			return
		}

		counter := findFirstByClass(node, "guest-list__counter")
		if counter == nil {
			total++
			return
		}

		value := strings.TrimSpace(nodeText(counter))
		value = strings.TrimPrefix(value, "x")
		value = strings.TrimPrefix(value, "×")

		count, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && count > 0 {
			total += count
		}
	})

	return total
}

// walkHTML обходит DOM-дерево в глубину.
func walkHTML(node *html.Node, visit func(*html.Node)) {
	if node == nil {
		return
	}

	visit(node)

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

// attr возвращает значение HTML-атрибута.
// Пустая строка означает отсутствие атрибута
// или пустое значение.
func attr(node *html.Node, name string) string {
	if node == nil {
		return ""
	}

	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}

// hasClass проверяет наличие отдельного CSS-класса,
// а не подстроки в атрибуте class.
func hasClass(node *html.Node, className string) bool {
	for _, class := range strings.Fields(attr(node, "class")) {
		if class == className {
			return true
		}
	}

	return false
}

// closestAncestor ищет ближайший родительский HTML-элемент
// с заданным именем тега.
func closestAncestor(node *html.Node, tag string) *html.Node {
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if parent.Type == html.ElementNode && parent.Data == tag {
			return parent
		}
	}

	return nil
}

// findFirstByClass находит первый вложенный HTML-элемент
// с указанным классом.
func findFirstByClass(
	root *html.Node,
	className string,
) *html.Node {
	var found *html.Node

	walkHTML(root, func(node *html.Node) {
		if found == nil &&
			node.Type == html.ElementNode &&
			hasClass(node, className) {
			found = node
		}
	})

	return found
}

// nodeText собирает текст всех дочерних текстовых узлов.
// Подходит также для содержимого script.
func nodeText(node *html.Node) string {
	var builder strings.Builder

	walkHTML(node, func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
		}
	})

	return builder.String()
}

// parseDocument создаёт DOM-дерево из HTML-ответа.
//
// Выделяем отдельную функцию, поскольку позже
// этот же документ понадобится парсеру тарифов.
func parseDocument(body []byte) (*html.Node, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty Bnovo HTML response")
	}

	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("parse Bnovo HTML: %w", err)
	}

	return doc, nil
}

// Parse преобразует HTML публичного модуля Bnovo
// в единый результат сбора.
//
// Функция:
//   - проверяет параметры поиска;
//   - строит DOM;
//   - извлекает категории и варианты размещения;
//   - извлекает тарифные предложения;
//   - проверяет связи между сущностями;
//   - удаляет полностью идентичные дубликаты;
//   - формирует предупреждения и итоговый статус.
//
// HTTP-запросы здесь не выполняются.
//
// Важно: скрытые предложения сохраняются, но не считаются
// подтверждённо доступными для текущего сценария.
func Parse(
	body []byte,
	req SearchRequest,
) (SearchResult, error) {
	result := SearchResult{
		Request: req,
		Status:  SearchStatusFailed,
	}

	// Проверяем запрос независимо от HTTP-клиента.
	// Это позволяет безопасно использовать Parse()
	// с сохранёнными HTML-фикстурами.
	if err := validateSearchRequest(req); err != nil {
		return result, err
	}

	// Преобразуем HTML в DOM-дерево.
	doc, err := parseDocument(body)
	if err != nil {
		return result, fmt.Errorf(
			"parse Bnovo document: %w", err,
		)
	}

	// Проверяем, что полученный HTML содержит
	// минимальную ожидаемую структуру Bnovo.
	if err := validateBnovoDocument(doc); err != nil {
		return result, fmt.Errorf(
			"validate Bnovo document: %w", err,
		)
	}

	if !hasRecognizedSearchState(doc) {
		return result, fmt.Errorf(
			"Bnovo response has no recognizable search state",
		)
	}

	// Извлекаем категории и варианты размещения.
	categories, variants, categoryWarnings, err :=
		parseCategoriesAndVariants(doc)
	if err != nil {
		return result, fmt.Errorf(
			"parse Bnovo categories: %w", err,
		)
	}

	result.Categories = categories
	result.Variants = variants
	result.Warnings = append(
		result.Warnings,
		categoryWarnings...,
	)

	// Извлекаем тарифы и публичные признаки доступности.
	offers, offerWarnings, err := parseRateOffers(doc)
	if err != nil {
		return result, fmt.Errorf(
			"parse Bnovo offers: %w", err,
		)
	}

	result.Warnings = append(
		result.Warnings,
		offerWarnings...,
	)

	// Проверяем ссылки на категории и варианты,
	// а также отсутствие противоречивых дубликатов.
	result.Offers, result.Warnings =
		validateAndDeduplicateOffers(
			offers,
			result.Categories,
			result.Variants,
			result.Warnings,
		)

	// Определяем доступность каждой физической категории.
	categoryAvailability, availabilityWarnings :=
		parseCategoryAvailability(
			doc,
			req,
			result.Categories,
			result.Offers,
		)

	result.CategoryAvailability = categoryAvailability

	// 2. Сохраняем предупреждения, возникшие при разборе HTML.
	result.Warnings = append(
		result.Warnings,
		availabilityWarnings...,
	)

	// 3. Проверяем полноту и непротиворечивость результатов.
	result.Warnings = append(
		result.Warnings,
		validateCategoryAvailability(
			result.CategoryAvailability,
		)...,
	)
	// Проверяем, подтверждена ли недоступность всех категорий.
	allUnavailable := len(result.CategoryAvailability) > 0 &&
		len(result.CategoryAvailability) == len(result.Categories)

	for _, state := range result.CategoryAvailability {
		if !state.ConfirmedUnavailable {
			allUnavailable = false
			break
		}
	}

	// Если предложения отсутствуют, но источник не подтвердил
	// недоступность всех категорий, фиксируем предупреждение.
	//
	// Если все категории явно недоступны, предупреждение
	// не нужно: это корректный ответ Bnovo.
	if len(result.Offers) == 0 && !allUnavailable {
		result.Warnings = append(result.Warnings, ParseWarning{
			Code:    "offers_not_found",
			Message: "no offers extracted; source unavailability not confirmed",
			Context: "plan_row",
		})
	}

	// Определяем результат парсинга.
	result.Status = determineSearchStatus(
		result.Categories,
		result.Variants,
		result.Offers,
		result.CategoryAvailability,
		result.Warnings,
	)

	return result, nil
}

// hasRecognizedSearchState проверяет наличие элементов,
// описывающих результат поиска на публичной странице.
func hasRecognizedSearchState(doc *html.Node) bool {
	found := false

	walkHTML(doc, func(node *html.Node) {
		if found || node.Type != html.ElementNode {
			return
		}

		if hasClass(node, "plan_row") ||
			hasClass(node, "tariff__nodates") {
			found = true
		}
	})

	return found
}

// validateAndDeduplicateOffers проверяет целостность
// найденных предложений.
//
// Уникальность предложения определяется комбинацией:
// CategoryID + VariantID + RateID.
//
// Идентичные дубликаты объединяются без предупреждения.
// При расхождении данных весь конфликтующий ключ исключается.
// Порядок первого появления непротиворечивых предложений сохраняется.
func validateAndDeduplicateOffers(
	offers []RateOffer,
	categories []RoomCategory,
	variants []AccommodationVariant,
	warnings []ParseWarning,
) ([]RateOffer, []ParseWarning) {
	categoryIDs := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		categoryIDs[category.ID] = struct{}{}
	}

	variantByID := make(
		map[string]AccommodationVariant,
		len(variants),
	)
	for _, variant := range variants {
		variantByID[variant.ID] = variant
	}

	type offerKey struct {
		CategoryID string
		VariantID  string
		RateID     string
	}

	seen := make(map[offerKey]RateOffer)
	conflicts := make(map[offerKey]struct{})
	order := make([]offerKey, 0, len(offers))

	for _, offer := range offers {
		contextID := offer.CategoryID + "/" +
			offer.VariantID + "/" + offer.RateID

		// Не допускаем предложения без базовых ID.
		if offer.CategoryID == "" ||
			offer.VariantID == "" ||
			offer.RateID == "" {
			warnings = append(warnings, ParseWarning{
				Code:    "offer_identity_missing",
				Message: "offer has incomplete identity",
				Context: contextID,
			})
			continue
		}

		// Проверяем существование физической категории.
		if _, exists := categoryIDs[offer.CategoryID]; !exists {
			warnings = append(warnings, ParseWarning{
				Code:    "offer_category_unknown",
				Message: "offer references unknown category",
				Context: contextID,
			})
			continue
		}

		// Проверяем существование варианта размещения.
		variant, exists := variantByID[offer.VariantID]
		if !exists {
			warnings = append(warnings, ParseWarning{
				Code:    "offer_variant_unknown",
				Message: "offer references unknown variant",
				Context: contextID,
			})
			continue
		}

		// Вариант должен принадлежать той же категории,
		// на которую ссылается тарифное предложение.
		if variant.CategoryID != offer.CategoryID {
			warnings = append(warnings, ParseWarning{
				Code:    "offer_category_mismatch",
				Message: "variant belongs to another category",
				Context: contextID,
			})
			continue
		}

		key := offerKey{
			CategoryID: offer.CategoryID,
			VariantID:  offer.VariantID,
			RateID:     offer.RateID,
		}

		// Обнаруженный конфликт нельзя устранить последующими дубликатами.
		if _, conflict := conflicts[key]; conflict {
			continue
		}

		if previous, exists := seen[key]; exists {
			if !reflect.DeepEqual(previous, offer) {
				conflicts[key] = struct{}{}
				warnings = append(warnings, ParseWarning{
					Code:    "offer_duplicate_conflict",
					Message: "same offer identity has conflicting values; all offers for this identity excluded",
					Context: contextID,
				})
			}
			continue
		}

		seen[key] = offer
		order = append(order, key)
	}

	// Первое предложение принимается только после проверки всех его дубликатов.
	valid := make([]RateOffer, 0, len(order))
	for _, key := range order {
		if _, conflict := conflicts[key]; !conflict {
			valid = append(valid, seen[key])
		}
	}

	return valid, warnings
}

// determineSearchStatus определяет результат парсинга.
//
// success: есть видимые предложения и нет предупреждений.
// no_offers: все известные категории явно недоступны.
// partial: неполные или противоречивые данные.
func determineSearchStatus(
	categories []RoomCategory,
	variants []AccommodationVariant,
	offers []RateOffer,
	availability []CategoryAvailability,
	warnings []ParseWarning,
) SearchStatus {
	if len(warnings) > 0 {
		return SearchStatusPartial
	}

	hasVisibleOffer := false

	for _, offer := range offers {
		if offer.Visible &&
			offer.Price != "" &&
			offer.Currency != "" {
			hasVisibleOffer = true
			break
		}
	}

	if hasVisibleOffer {
		return SearchStatusSuccess
	}

	// Подтверждаем отсутствие предложений только если
	// состояние известно для каждой категории.
	if len(categories) > 0 &&
		len(availability) == len(categories) {
		allUnavailable := true

		for _, state := range availability {
			if !state.ConfirmedUnavailable {
				allUnavailable = false
				break
			}
		}

		if allUnavailable {
			return SearchStatusNoOffers
		}
	}

	return SearchStatusPartial
}

// bnovoPlan описывает тариф из $plansForJs.
// RoomTypeIDs — разрешённые варианты размещения тарифа,
// а не отдельные физические номера.
type bnovoPlan struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	RoomTypeIDs []string `json:"roomTypeIds"`
}

// parseRateOffers извлекает тарифные предложения из HTML.
//
// Один .plan_row может содержать несколько <tr>:
// по одному на каждый вариант размещения.
//
// Возвращает предложения и предупреждения.
// Отсутствие тарифных строк само по себе не считается ошибкой:
// позже Parse() определит, действительно ли предложений нет.
func parseRateOffers(
	doc *html.Node,
) ([]RateOffer, []ParseWarning, error) {
	plans, plansErr := parsePlansForJS(doc)

	var offers []RateOffer
	var warnings []ParseWarning

	currency, currencyErr := parseCurrency(doc)
	if currencyErr != nil {
		warnings = append(warnings, ParseWarning{
			Code:    "currency_unknown",
			Message: currencyErr.Error(),
			Context: "account_currency_iso4217",
		})
	}

	if plansErr != nil {
		warnings = append(warnings, ParseWarning{
			Code:    "plans_metadata_missing",
			Message: plansErr.Error(),
			Context: "$plansForJs",
		})
	}

	// Находим все тарифные блоки документа.
	walkHTML(doc, func(planRow *html.Node) {
		if planRow.Type != html.ElementNode ||
			!hasClass(planRow, "plan_row") {
			return
		}

		rateID := attr(planRow, "data-plan-id")
		if rateID == "" {
			warnings = append(warnings, ParseWarning{
				Code:    "rate_id_missing",
				Message: "tariff block has no plan ID",
				Context: "plan_row",
			})
			return
		}

		rateName := parseRateName(planRow)

		// Если название отсутствует в HTML-блоке,
		// используем метаданные $plansForJs.
		plan, planKnown := plans[rateID]
		if rateName == "" && planKnown {
			rateName = plan.Name
		}

		mealPlan := parseRateMealPlan(planRow)
		cancellation := parseRateTerms(
			planRow,
			"tariff__descriptionCancel",
		)
		prepayment := parseRateTerms(
			planRow,
			"tariff__descriptionPayment",
		)

		planVisible := isNodeVisible(planRow)

		// Каждая строка таблицы — отдельный вариант
		// размещения по этому тарифу.
		walkHTML(planRow, func(tr *html.Node) {
			if tr.Type != html.ElementNode ||
				tr.Data != "tr" {
				return
			}

			// Необходим реальный селектор Bnovo.
			selector := findFirstByClass(
				tr,
				"selectAvailable",
			)
			if selector == nil ||
				selector.Data != "select" {
				return
			}

			variantID := attr(selector, "data-room-id")
			categoryID := attr(
				selector,
				"data-real-room-id",
			)

			if variantID == "" || categoryID == "" {
				warnings = append(warnings, ParseWarning{
					Code:    "offer_room_id_missing",
					Message: "offer has no variant or category ID",
					Context: rateID,
				})
				return
			}

			// Сверяем идентификатор тарифа между
			// родительским блоком и селектором.
			selectRateID := attr(selector, "data-plan-id")
			if selectRateID != rateID {
				warnings = append(warnings, ParseWarning{
					Code:    "offer_rate_mismatch",
					Message: "selector tariff ID differs from parent tariff",
					Context: rateID + "/" + variantID,
				})
				return
			}

			// Дополнительная сверка связей по JS-метаданным.
			if planKnown &&
				len(plan.RoomTypeIDs) > 0 &&
				!containsString(plan.RoomTypeIDs, variantID) {
				warnings = append(warnings, ParseWarning{
					Code:    "offer_plan_variant_mismatch",
					Message: "variant not listed for tariff in JS metadata",
					Context: rateID + "/" + variantID,
				})
				return
			}

			price, priceErr := parseOfferPrice(tr)
			if priceErr != nil {
				warnings = append(warnings, ParseWarning{
					Code:    "offer_price_invalid",
					Message: priceErr.Error(),
					Context: rateID + "/" + variantID,
				})
			}

			// Сохраняем именно исходные признаки
			// количества для данного предложения.
			evidence, evidenceWarnings :=
				parseAvailabilityEvidence(selector, planRow)

			warnings = append(
				warnings,
				evidenceWarnings...,
			)

			offer := RateOffer{
				CategoryID: categoryID,
				VariantID:  variantID,
				RateID:     rateID,
				RateName:   rateName,

				Price:    price,
				Currency: currency,

				MealPlan:     mealPlan,
				Cancellation: cancellation,
				Prepayment:   prepayment,

				// Строка должна быть видима
				// одновременно на уровне тарифа и варианта.
				Visible: planVisible && isNodeVisible(tr),

				Availability: evidence,
			}

			offers = append(offers, offer)
		})
	})

	return offers, warnings, nil
}

// isNodeVisible проверяет, не скрыт ли элемент
// или любой из его родительских элементов.
//
// Проверка основана на статических HTML-атрибутах.
// CSS и JavaScript не выполняются.
func isNodeVisible(node *html.Node) bool {
	for current := node; current != nil; current = current.Parent {
		if current.Type != html.ElementNode {
			continue
		}

		if hasClass(current, "d-none") ||
			hasClass(current, "hidden") {
			return false
		}

		for _, attribute := range current.Attr {
			if attribute.Key == "hidden" {
				return false
			}
		}

		style := strings.ToLower(
			strings.ReplaceAll(
				attr(current, "style"),
				" ",
				"",
			),
		)

		if strings.Contains(style, "display:none") {
			return false
		}
	}

	return true
}

// containsString проверяет наличие идентификатора
// в списке разрешённых вариантов тарифа.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

// parseAvailabilityEvidence собирает исходные признаки
// количества из тарифного селектора.
//
// Эти данные НЕ являются подтверждённым физическим
// остатком номеров и не суммируются между тарифами.
func parseAvailabilityEvidence(
	selector *html.Node,
	planRow *html.Node,
) (AvailabilityEvidence, []ParseWarning) {
	var result AvailabilityEvidence
	var warnings []ParseWarning

	contextID := attr(selector, "data-plan-id") +
		"/" + attr(selector, "data-room-id")

	// Публичное количество из селектора тарифа.
	if raw := attr(selector, "data-available"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err == nil && value >= 0 {
			result.DataAvailable = &value
		} else {
			warnings = append(warnings, ParseWarning{
				Code:    "available_quantity_invalid",
				Message: "invalid data-available attribute",
				Context: contextID,
			})
		}
	}

	// Атрибут присутствует на уровне .plan_row.
	// Пока сохраняем буквально, без интерпретации.
	if raw := attr(planRow, "data-available-for-guests"); raw != "" {
		value := raw
		result.DataAvailableForGuests = &value
	}

	// Наличие disabled не зависит от значения атрибута.
	for _, attribute := range selector.Attr {
		if attribute.Key == "disabled" {
			result.SelectDisabled = true
			break
		}
	}

	// Извлекаем только положительные значения
	// активных option соответствующего селектора.
	walkHTML(selector, func(node *html.Node) {
		if node.Type != html.ElementNode ||
			node.Data != "option" {
			return
		}

		for _, attribute := range node.Attr {
			if attribute.Key == "disabled" {
				return
			}
		}

		value, err := strconv.Atoi(attr(node, "value"))
		if err != nil || value <= 0 {
			return
		}

		result.SelectableQuantities = append(
			result.SelectableQuantities,
			value,
		)
	})

	return result, warnings
}

// parsePlansForJS читает JSON-литерал $plansForJs,
// не выполняя JavaScript.
func parsePlansForJS(
	doc *html.Node,
) (map[string]bnovoPlan, error) {
	const marker = "$plansForJs ="

	var rawJSON string

	walkHTML(doc, func(node *html.Node) {
		if rawJSON != "" ||
			node.Type != html.ElementNode ||
			node.Data != "script" {
			return
		}

		script := nodeText(node)
		index := strings.Index(script, marker)
		if index < 0 {
			return
		}

		after := strings.TrimSpace(
			script[index+len(marker):],
		)

		if strings.HasPrefix(after, "{") {
			rawJSON = after
		}
	})

	if rawJSON == "" {
		return nil, fmt.Errorf("$plansForJs JSON not found")
	}

	var plans map[string]bnovoPlan

	decoder := json.NewDecoder(
		strings.NewReader(rawJSON),
	)

	if err := decoder.Decode(&plans); err != nil {
		return nil, fmt.Errorf(
			"decode $plansForJs: %w", err,
		)
	}

	return plans, nil
}

// parseRateName извлекает название тарифа
// из его видимого заголовка.
func parseRateName(planRow *html.Node) string {
	title := findFirstByClass(
		planRow,
		"tariff__modal--short",
	)
	if title == nil {
		return ""
	}

	return normalizeText(nodeText(title))
}

// parseRateMealPlan извлекает текст о питании,
// например "Без питания" или "Завтрак".
func parseRateMealPlan(planRow *html.Node) string {
	food := findFirstByClass(
		planRow,
		"tariff__descriptionFood",
	)
	if food == nil {
		return ""
	}

	return normalizeText(nodeText(food))
}

// parseRateTerms извлекает заголовок условия
// и подробное описание из связанного модального блока.
//
// Не делаем выводов о возвратности или предоплате
// на основании отдельных ключевых слов.
func parseRateTerms(
	planRow *html.Node,
	className string,
) string {
	labelNode := findFirstByClass(planRow, className)
	if labelNode == nil {
		return ""
	}

	label := normalizeText(nodeText(labelNode))

	// Bnovo связывает кнопку открытия и модальное окно
	// через data-popup-content="#some-id".
	target := strings.TrimPrefix(
		attr(labelNode, "data-popup-content"),
		"#",
	)

	if target == "" {
		return label
	}

	var content string

	walkHTML(planRow, func(node *html.Node) {
		if content != "" ||
			node.Type != html.ElementNode ||
			attr(node, "id") != target {
			return
		}

		body := findFirstByClass(
			node,
			"js-modal-content",
		)
		if body != nil {
			content = normalizeText(nodeText(body))
		}
	})

	if content == "" || content == label {
		return label
	}

	if label == "" {
		return content
	}

	return label + ". " + content
}

// normalizeText удаляет лишние пробелы, переносы строк
// и неразрывные пробелы из HTML-текста.
func normalizeText(value string) string {
	value = strings.ReplaceAll(value, "\u00a0", " ")
	return strings.Join(strings.Fields(value), " ")
}

// parseOfferPrice извлекает стоимость всего проживания
// из атрибута data-tariff-price.
//
// Не умножаем цену на количество ночей.
// Возвращаем нормализованную десятичную строку.
func parseOfferPrice(tr *html.Node) (string, error) {
	var rawPrice string

	walkHTML(tr, func(node *html.Node) {
		if rawPrice != "" ||
			node.Type != html.ElementNode {
			return
		}

		rawPrice = attr(node, "data-tariff-price")
	})

	if rawPrice == "" {
		return "", fmt.Errorf("data-tariff-price missing")
	}

	return normalizePrice(rawPrice)
}

// normalizePrice приводит цену к виду:
// "4910", "4910.50".
//
// Тысячные пробелы удаляются.
// Десятичная запятая заменяется точкой.
func normalizePrice(raw string) (string, error) {
	value := normalizeText(raw)
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, ",", ".")

	if value == "" {
		return "", fmt.Errorf("price is empty")
	}

	dots := 0
	digits := 0

	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dots++
		default:
			return "", fmt.Errorf(
				"price contains invalid characters",
			)
		}
	}

	if digits == 0 || dots > 1 ||
		strings.HasPrefix(value, ".") ||
		strings.HasSuffix(value, ".") {
		return "", fmt.Errorf("invalid decimal price")
	}

	return value, nil
}

// parseCurrency извлекает ISO-код валюты отеля
// из встроенных настроек страницы Bnovo.
func parseCurrency(doc *html.Node) (string, error) {
	const marker = "account_currency_iso4217"

	var currency string

	walkHTML(doc, func(node *html.Node) {
		if currency != "" ||
			node.Type != html.ElementNode ||
			node.Data != "script" {
			return
		}

		script := nodeText(node)
		index := strings.Index(script, marker)
		if index < 0 {
			return
		}

		after := script[index+len(marker):]
		equal := strings.Index(after, "=")
		if equal < 0 {
			return
		}

		after = strings.TrimSpace(after[equal+1:])
		if len(after) < 5 {
			return
		}

		quote := after[0]
		if quote != '\'' && quote != '"' {
			return
		}

		end := strings.IndexByte(after[1:], quote)
		if end < 0 {
			return
		}

		currency = strings.ToUpper(after[1 : end+1])
	})

	if len(currency) != 3 {
		return "", fmt.Errorf("currency not found")
	}

	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return "", fmt.Errorf("invalid currency code")
		}
	}

	return currency, nil
}

// parseUnavailableMessage извлекает подтверждённое
// сообщение Bnovo из HTML-карточки категории.
//
// Возвращает пустую строку, если известный признак
// недоступности отсутствует.
func parseUnavailableMessage(categoryNode *html.Node) string {
	if categoryNode == nil {
		return ""
	}

	messageNode := findFirstByClass(
		categoryNode,
		"tariff__nodates",
	)

	if messageNode == nil {
		return ""
	}

	message := normalizeText(nodeText(messageNode))

	if !strings.Contains(
		strings.ToLower(message),
		"здесь нет свободных мест",
	) {
		return ""
	}

	return message
}

// parseCategoryAvailability определяет состояние каждой категории
// для конкретного поискового сценария.
//
// Сообщение о недоступности ищем только внутри .room
// с соответствующим data-roomtype-id.
//
// Наличие сообщения не доказывает, что физически
// все номера забронированы.
func parseCategoryAvailability(
	doc *html.Node,
	req SearchRequest,
	categories []RoomCategory,
	offers []RateOffer,
) ([]CategoryAvailability, []ParseWarning) {
	var warnings []ParseWarning

	// Индексируем категории из уже разобранного результата.
	availability := make(
		map[string]CategoryAvailability,
		len(categories),
	)

	for _, category := range categories {
		availability[category.ID] = CategoryAvailability{
			CategoryID: category.ID,
		}
	}

	// Определяем категории, у которых есть видимые
	// предложения с известной ценой.
	for _, offer := range offers {
		state, exists := availability[offer.CategoryID]
		if !exists {
			continue
		}

		if offer.Visible &&
			offer.Price != "" &&
			offer.Currency != "" {
			state.HasOffers = true
			availability[offer.CategoryID] = state
		}
	}

	// Ищем физические карточки категорий.
	walkHTML(doc, func(node *html.Node) {
		if node.Type != html.ElementNode ||
			!hasClass(node, "room") {
			return
		}

		categoryID := attr(node, "data-roomtype-id")
		if categoryID == "" {
			return
		}

		state, exists := availability[categoryID]
		if !exists {
			return
		}

		// Сообщение проверяется исключительно
		// внутри карточки данной категории.
		message := parseUnavailableMessage(node)

		if message == "" {
			return
		}

		// Источник не должен одновременно сообщать
		// об отсутствии мест и показывать видимый тариф.
		// В случае противоречия фиксируем предупреждение,
		// не подтверждая недоступность.
		if state.HasOffers {
			warnings = append(warnings, ParseWarning{
				Code:    "availability_conflict",
				Message: "category has offers and unavailable message",
				Context: categoryID,
			})
			return
		}

		confirmed, err := matchesUnavailablePeriod(message, req)

		if err != nil {
			warnings = append(warnings, ParseWarning{
				Code:    "unavailable_period_mismatch",
				Message: err.Error(),
				Context: categoryID,
			})

			// Сохраняем исходное сообщение для диагностики,
			// но не подтверждаем недоступность.
			state.Reason = message
			state.ConfirmedUnavailable = false
			availability[categoryID] = state
			return
		}

		state.ConfirmedUnavailable = confirmed
		state.Reason = message
		availability[categoryID] = state
	})

	// Возвращаем результаты в порядке categories,
	// чтобы JSON оставался детерминированным.
	result := make(
		[]CategoryAvailability,
		0,
		len(categories),
	)

	for _, category := range categories {
		result = append(
			result,
			availability[category.ID],
		)
	}

	return result, warnings
}

// matchesUnavailablePeriod проверяет, что сообщение Bnovo
// относится строго к запрошенному периоду проживания.
//
// confirmed=false означает отсутствие достоверного подтверждения.
// error сообщает о неожиданном формате или несовпадении.
func matchesUnavailablePeriod(
	message string,
	req SearchRequest,
) (confirmed bool, err error) {
	matches := unavailableDatesPattern.FindStringSubmatch(
		normalizeText(message),
	)

	if len(matches) != 7 {
		return false, fmt.Errorf(
			"unavailable message dates not recognized",
		)
	}

	checkIn, err := parseRussianDate(
		matches[1], matches[2], matches[3],
	)
	if err != nil {
		return false, fmt.Errorf("parse unavailable check-in: %w", err)
	}

	checkOut, err := parseRussianDate(
		matches[4], matches[5], matches[6],
	)
	if err != nil {
		return false, fmt.Errorf("parse unavailable check-out: %w", err)
	}

	expectedIn := req.CheckIn.Format("2006-01-02")
	expectedOut := req.CheckOut.Format("2006-01-02")

	if checkIn != expectedIn || checkOut != expectedOut {
		return false, fmt.Errorf(
			"unavailable dates mismatch: got %s..%s, expected %s..%s",
			checkIn,
			checkOut,
			expectedIn,
			expectedOut,
		)
	}

	return true, nil
}

var russianMonths = map[string]time.Month{
	"января":   time.January,
	"февраля":  time.February,
	"марта":    time.March,
	"апреля":   time.April,
	"мая":      time.May,
	"июня":     time.June,
	"июля":     time.July,
	"августа":  time.August,
	"сентября": time.September,
	"октября":  time.October,
	"ноября":   time.November,
	"декабря":  time.December,
}

var unavailableDatesPattern = regexp.MustCompile(
	`(?i)здесь\s+нет\s+свободных\s+мест\s+с\s+(\d{1,2})\s+([а-яё]+)\s+(\d{4})\s+по\s+(\d{1,2})\s+([а-яё]+)\s+(\d{4})`,
)

// parseRussianDate преобразует дату Bnovo в YYYY-MM-DD.
//
// Используем строковое представление даты, потому что
// нам не требуется время суток и сравнение по часовым поясам.
func parseRussianDate(day, monthName, year string) (string, error) {
	month, ok := russianMonths[strings.ToLower(monthName)]
	if !ok {
		return "", fmt.Errorf("unknown Russian month: %q", monthName)
	}

	d, err := strconv.Atoi(day)
	if err != nil {
		return "", fmt.Errorf("invalid day: %w", err)
	}

	y, err := strconv.Atoi(year)
	if err != nil {
		return "", fmt.Errorf("invalid year: %w", err)
	}

	// time.Date нормализует некорректные даты,
	// поэтому обязательно проверяем исходные компоненты.
	date := time.Date(y, month, d, 0, 0, 0, 0, time.UTC)

	if date.Year() != y ||
		date.Month() != month ||
		date.Day() != d {
		return "", fmt.Errorf("invalid calendar date")
	}

	return date.Format("2006-01-02"), nil
}

// validateCategoryAvailability проверяет, что для каждой
// ожидаемой категории известен результат публичного поиска.
//
// Отсутствие предложений без явного сообщения Bnovo
// не считается подтверждённой недоступностью.
func validateCategoryAvailability(
	availability []CategoryAvailability,
) []ParseWarning {
	var warnings []ParseWarning

	for _, state := range availability {
		switch {
		case state.HasOffers && state.ConfirmedUnavailable:
			warnings = append(warnings, ParseWarning{
				Code:    "category_availability_conflict",
				Message: "category has offers and confirmed unavailability",
				Context: state.CategoryID,
			})

		case !state.HasOffers && !state.ConfirmedUnavailable:
			warnings = append(warnings, ParseWarning{
				Code:    "category_availability_unknown",
				Message: "category has no visible offers or confirmed unavailability",
				Context: state.CategoryID,
			})
		}
	}

	return warnings
}

// validateBnovoDocument проверяет минимальную структуру
// публичной страницы бронирования.
//
// Функция не проверяет доступность номеров и не определяет
// SearchStatus. Она только исключает ответы, которые
// невозможно достоверно интерпретировать как поиск Bnovo.
func validateBnovoDocument(doc *html.Node) error {
	roomTypes, err := parseEnabledRoomTypes(doc)
	if err != nil {
		return fmt.Errorf("invalid Bnovo room metadata: %w", err)
	}

	roomCards := 0
	recognizedCards := 0

	walkHTML(doc, func(node *html.Node) {
		if node.Type != html.ElementNode ||
			!hasClass(node, "room") {
			return
		}

		categoryID := attr(node, "data-roomtype-id")
		if categoryID == "" {
			return
		}

		roomCards++

		if _, exists := roomTypes[categoryID]; exists {
			recognizedCards++
		}
	})

	if roomCards == 0 {
		return fmt.Errorf("Bnovo room cards not found")
	}

	if recognizedCards == 0 {
		return fmt.Errorf(
			"Bnovo room cards do not match room metadata",
		)
	}

	return nil
}
