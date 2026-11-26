package travelline

// bookingRulesResponse — ответ AvailabilityCalendar/rate_plan_booking_rules.
// Модель хранит исходные значения, не интерпретируя ограничения.
type bookingRulesResponse struct {
	BookingRules []bookingRule `json:"booking_rules"`
}

// BookingRulesResponse открывает нативный DTO для преобразования в collect,
// сохраняя существующие типы и сигнатуру HTTP-клиента.
type BookingRulesResponse = bookingRulesResponse

// BookingRule открывает нативное правило для восстановления индекса из storage.
type BookingRule = bookingRule

type bookingRule struct {
	// nil означает отсутствие/null; [] — явно переданный пустой массив.
	RoomTypes []string `json:"room_types"`
	Code      string   `json:"code"`
	Date      string   `json:"date"`

	// Указатели различают отсутствие ограничения и явно переданные false/0.
	Forbidden     *bool `json:"forbidden"`
	MinLOS        *int  `json:"min_los"`
	MaxLOS        *int  `json:"max_los"`
	MinAdvBooking *int  `json:"min_adv_booking"`
}
