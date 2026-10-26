// Package bnovo содержит типы и логику извлечения данных из публичного
// виджета бронирования Bnovo. Эти типы описывают наблюдаемые публичные предложения,
// а не подтверждённый номерной фонд или бронирования в PMS.
package bnovo

import "time"

// SearchRequest описывает один запрос к публичному виджету бронирования отеля.
// Вызывающий код передаёт календарные даты в часовом поясе отеля.
type SearchRequest struct {
	// PropertyID — публичный UID виджета Bnovo, а не идентификатор отеля в PMS.
	PropertyID string

	// CheckIn — дата заезда в часовом поясе отеля, включённая в период проживания.
	CheckIn time.Time
	// CheckOut — дата выезда в часовом поясе отеля, не включённая в период проживания.
	// Длительность проживания — число календарных дней между CheckIn и CheckOut.
	CheckOut time.Time

	// Adults — число взрослых гостей для одного варианта размещения.
	Adults int
	// ChildrenAges содержит возраст каждого ребёнка; пустой список означает отсутствие детей.
	ChildrenAges []int
	// Language — запрашиваемый язык публичного виджета, например "ru".
	Language string
}

// RoomCategory определяет базовую категорию номеров, общую для вариантов размещения.
// Она не представляет отдельный физический номер или снимок номерного фонда.
type RoomCategory struct {
	// ID — идентификатор базового типа номера Bnovo (data-real-room-id, если указан).
	ID string
	// Name — название категории, полученное из Bnovo.
	Name string
	// AreaM2 — площадь номера категории в квадратных метрах; nil означает, что она неизвестна.
	AreaM2 *float64
}

// AccommodationVariant определяет один вариант размещения в базовой категории номеров.
// Варианты для двух, трёх и четырёх взрослых могут использовать одни и те же физические номера.
type AccommodationVariant struct {
	// ID — идентификатор типа номера для варианта размещения Bnovo (data-room-id).
	ID string
	// CategoryID ссылается на RoomCategory.ID (data-real-room-id).
	CategoryID string
	// Adults — число взрослых для этого варианта размещения, а не максимальная
	// вместимость категории. Ноль означает, что число гостей не определено.
	Adults int
}

// RateOffer — одно наблюдаемое предложение для дат и гостей из соответствующего SearchResult.
// Несколько тарифов и вариантов размещения могут использовать одни и те же физические номера.
type RateOffer struct {
	// CategoryID ссылается на базовую категорию физических номеров RoomCategory.ID.
	CategoryID string
	// VariantID ссылается на AccommodationVariant.ID.
	VariantID string
	// RateID — идентификатор тарифа в источнике.
	RateID string
	// RateName — название тарифа в источнике.
	RateName string

	// Price — точная десятичная сумма за ВЕСЬ период проживания, а не за ночь.
	// Формат: цифры с необязательной дробной частью через точку (например, "4910.50").
	// Пустая строка означает, что цена недоступна или не распознана; "0" — реальная нулевая сумма.
	Price string
	// Currency — код валюты ISO 4217, например "RUB"; пустая строка, если неизвестен.
	Currency string

	// MealPlan сохраняет полученное описание питания; пустая строка, если неизвестно.
	MealPlan string
	// Cancellation сохраняет полученные условия отмены без дополнительных выводов
	// о возможности возврата средств; пустая строка, если условия неизвестны.
	Cancellation string
	// Prepayment сохраняет полученные условия оплаты и предоплаты; пустая строка, если неизвестны.
	Prepayment string

	// Visible указывает, была ли строка предложения видимой в HTML.
	// Скрытые предложения не считаются автоматически доступными для публичного бронирования.
	Visible bool
	// Availability содержит исходные признаки доступности из публичного интерфейса для этого тарифа и варианта.
	Availability AvailabilityEvidence
}

// AvailabilityEvidence хранит исходные данные о количестве из публичного виджета
// независимо от оценок фактической доступности, занятости или подтверждённых бронирований.
type AvailabilityEvidence struct {
	// DataAvailable — целочисленное значение атрибута data-available; nil означает,
	// что атрибут отсутствует или некорректен, а не отсутствие доступных номеров.
	DataAvailable *int
	// DataAvailableForGuests — исходное значение атрибута data-available-for-guests.
	// Его смысл не проверен; nil означает, что атрибут не обнаружен.
	DataAvailableForGuests *string
	// SelectableQuantities — положительные значения доступных элементов <option>
	// соответствующего списка выбора количества. Их нельзя суммировать по тарифам и вариантам.
	SelectableQuantities []int
	// SelectDisabled указывает, что список выбора количества был отключён.
	SelectDisabled bool
}

// SearchStatus описывает результат обработки одного запроса к публичному виджету.
type SearchStatus string

const (
	// SearchStatusSuccess означает, что ответ разобран без существенных проблем.
	SearchStatusSuccess SearchStatus = "success"
	// SearchStatusPartial означает, что часть предложений разобрана, но другие потеряны
	// или возникли существенные предупреждения. Корректные предложения должны сохраняться.
	SearchStatusPartial SearchStatus = "partial"
	// SearchStatusNoOffers означает, что ответ разобран, но подходящих предложений нет;
	// это НЕ доказывает, что все физические номера проданы.
	SearchStatusNoOffers SearchStatus = "no_offers"
	// SearchStatusFailed означает ошибку запроса или обработки его результата.
	SearchStatusFailed SearchStatus = "failed"
)

// CollectionError — машиночитаемая ошибка, которую впоследствии можно сохранить
// вместе с записью попытки; список кодов можно расширять при появлении новых ошибок источника.
type CollectionError struct {
	// Code — стабильный внутренний код ошибки, например "timeout" или "http_error".
	Code string
	// Message — краткое диагностическое сообщение без файлов cookie и секретов.
	Message string
	// HTTPStatus — HTTP-код ответа или ноль, если ответ не получен.
	HTTPStatus int
	// Retryable указывает, допустима ли повторная попытка после этой ошибки.
	Retryable bool
}

// ParseWarning описывает некритичную проблему с неполным или неоднозначным полем
// либо предложением. Предупреждения сохраняются даже при успешном разборе части предложений.
type ParseWarning struct {
	// Code — стабильный код предупреждения, например "price_missing".
	Code string
	// Message — краткое пояснение о неразобранных данных, очищенное от чувствительной информации.
	Message string
	// Context определяет затронутое поле, категорию, вариант или тариф
	// без исходного HTML, персональных данных и учётных данных.
	Context string
}

// SearchResult описывает ОДИН поисковый запрос и сохраняет диагностику, даже если
// предложения не получены. Будущий слой хранения может сохранять каждую попытку.
type SearchResult struct {
	// Request содержит точные параметры запроса для этой попытки.
	Request SearchRequest
	// CollectedAt — время начала попытки в UTC.
	CollectedAt time.Time
	// Status различает успех, частичный разбор, отсутствие предложений и ошибку.
	Status SearchStatus

	// Categories содержит базовые категории номеров, обнаруженные в ответе.
	Categories []RoomCategory
	// Variants содержит варианты размещения, связанные с базовыми категориями.
	Variants []AccommodationVariant
	// Offers содержит разобранные тарифные предложения для запроса; список может быть пустым.
	Offers []RateOffer

	// HTTPStatus — HTTP-код ответа или ноль, если ответ не получен.
	HTTPStatus int
	// Error содержит сведения о критической ошибке; nil, если попытка не завершилась ошибкой.
	Error *CollectionError
	// Warnings содержит некритичные проблемы извлечения данных, в том числе при частичном разборе.
	Warnings []ParseWarning

	CategoryAvailability []CategoryAvailability
}

// CategoryAvailability описывает доступность категории
// для конкретного поискового сценария.
type CategoryAvailability struct {
	CategoryID string

	// Есть ли извлечённые предложения по категории.
	HasOffers bool

	// Bnovo явно сообщил об отсутствии свободных мест.
	ConfirmedUnavailable bool

	// Исходный текст сообщения Bnovo.
	Reason string
}