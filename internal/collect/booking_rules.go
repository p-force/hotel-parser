package collect

import (
	"fmt"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

// BookingRulesToStorage раскрывает исходные room_types без фильтрации, дедупликации
// и интерпретации ограничений. Порядок исходных записей и категорий сохраняется.
// Метаданные RunBookingRulesInput заполняются вызывающим кодом отдельно.
// Успешный пустой массив даёт ненулевой пустой slice; отсутствующий ответ — ошибку.
func BookingRulesToStorage(response *travelline.BookingRulesResponse) ([]storage.BookingRuleInput, error) {
	if response == nil || response.BookingRules == nil {
		return nil, fmt.Errorf("TravelLine booking rules response is missing")
	}
	result := make([]storage.BookingRuleInput, 0)
	for i, rule := range response.BookingRules {
		date, err := time.Parse("2006-01-02", rule.Date)
		if err != nil || date.Year() < 1 || date.IsZero() || date.Format("2006-01-02") != rule.Date {
			// Нельзя подменять некорректную дату нулевой или терять исходную запись.
			return nil, fmt.Errorf("invalid TravelLine booking rule date at index %d", i)
		}
		appendRule := func(roomTypeID *string) {
			result = append(result, storage.BookingRuleInput{
				Date:          date,
				RoomTypeID:    roomTypeID,
				RateID:        rule.Code,
				Forbidden:     copiedBookingRuleField(rule.Forbidden),
				MinLOS:        copiedBookingRuleField(rule.MinLOS),
				MaxLOS:        copiedBookingRuleField(rule.MaxLOS),
				MinAdvBooking: copiedBookingRuleField(rule.MinAdvBooking),
			})
		}
		if len(rule.RoomTypes) == 0 {
			appendRule(nil)
			continue
		}
		for _, id := range rule.RoomTypes {
			roomTypeID := id
			appendRule(&roomTypeID)
		}
	}
	return result, nil
}

// BookingRulesIndexFromStorage восстанавливает индекс без повторного HTTP-запроса.
// Storage уже раскрывает категории: исходная группировка room_types не восстанавливается.
// Index самостоятельно копирует поля, сохраняет дубликаты и обнаруживает конфликты.
func BookingRulesIndexFromStorage(saved storage.RunBookingRules) travelline.BookingRulesIndex {
	response := travelline.BookingRulesResponse{
		BookingRules: make([]travelline.BookingRule, 0, len(saved.Rules)),
	}
	for _, rule := range saved.Rules {
		var roomTypes []string
		if rule.RoomTypeID != nil {
			roomTypes = []string{*rule.RoomTypeID}
		}
		response.BookingRules = append(response.BookingRules, travelline.BookingRule{
			RoomTypes:     roomTypes,
			Code:          rule.RateID,
			Date:          rule.Date.Format("2006-01-02"),
			Forbidden:     rule.Forbidden,
			MinLOS:        rule.MinLOS,
			MaxLOS:        rule.MaxLOS,
			MinAdvBooking: rule.MinAdvBooking,
		})
	}
	return response.Index()
}

func copiedBookingRuleField[T any](field *T) *T {
	if field == nil {
		return nil
	}
	copy := *field
	return &copy
}
