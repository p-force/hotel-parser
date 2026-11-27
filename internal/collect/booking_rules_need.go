package collect

import (
	"net/http"

	"github.com/p-force/hotel-parser/internal/storage"
)

// NeedsTravelLineBookingRules проверяет необходимость уточнения доступности
// после успешно полученного и разобранного (в том числе частично) ответа.
// Ошибки попытки не являются основанием для запроса правил.
func NeedsTravelLineBookingRules(scenario storage.Scenario, attempt storage.AttemptInput) bool {
	if scenario.Source != "travelline" || scenario.LOS < 1 || scenario.LOS > 3 || attempt.Error != nil || attempt.HTTPStatus != http.StatusOK {
		return false
	}
	switch attempt.Outcome {
	case storage.OutcomeSuccess, storage.OutcomePartial, storage.OutcomeNoOffers:
	default:
		return false
	}
	for _, observation := range attempt.Observations {
		if needsTravelLineBookingRulesObservation(observation) {
			return true
		}
	}
	return false
}

func needsTravelLineBookingRulesObservation(observation storage.Observation) bool {
	return observation.StockStatus == storage.StockUnknown && observation.Available == nil &&
		!observation.HasOffers && len(observation.Offers) == 0 && !observation.ConfirmedUnavailable
}
