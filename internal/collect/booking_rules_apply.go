package collect

import (
	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

// ApplyTravelLineBookingRules подтверждает запрет для подходящих наблюдений.
// Изменяет только ConfirmedUnavailable и UnavailabilityReason; неизвестный
// физический остаток и остальные данные попытки сохраняются. Возвращает число
// новых подтверждений. rateIDs — применимые тарифы, а не тарифы из offers.
func ApplyTravelLineBookingRules(scenario storage.Scenario, attempt *storage.AttemptInput, index travelline.BookingRulesIndex, rateIDs []string) int {
	if attempt == nil || !NeedsTravelLineBookingRules(scenario, *attempt) {
		return 0
	}
	confirmed := 0
	for i := range attempt.Observations {
		observation := &attempt.Observations[i]
		if !needsTravelLineBookingRulesObservation(*observation) ||
			!travelline.ConfirmCategoryBookingForbidden(index, scenario.CheckIn, observation.CategoryID, rateIDs, scenario.LOS) {
			continue
		}
		reason := storage.UnavailabilityReasonBookingForbidden
		observation.ConfirmedUnavailable = true
		observation.UnavailabilityReason = &reason
		confirmed++
	}
	return confirmed
}
