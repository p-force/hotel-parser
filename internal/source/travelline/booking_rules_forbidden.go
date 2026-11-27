package travelline

import "time"

// ConfirmCategoryBookingForbidden проверяет явный запрет для LOS=1/2/3:
// каждому применимому тарифу нужна хотя бы одна непротиворечиво запрещённая ночь
// в [checkIn, checkIn.AddDate(0, 0, los)). Даты запрета тарифов могут различаться.
// false означает отсутствие подтверждения, а не разрешение бронирования.
// Прочие ограничения не толкуются.
func ConfirmCategoryBookingForbidden(index BookingRulesIndex, checkIn time.Time, roomTypeID string, rateIDs []string, los int) bool {
	if los < 1 || los > 3 || len(rateIDs) == 0 || checkIn.IsZero() || roomTypeID == "" {
		return false
	}
	for _, rateID := range rateIDs {
		if rateID == "" {
			return false
		}
		blocked := false
		for night := 0; night < los; night++ {
			date := checkIn.AddDate(0, 0, night).Format("2006-01-02")
			match, found := index.Lookup(BookingRuleKey{Date: date, RoomTypeID: roomTypeID, RateID: rateID})
			if !found || match.Conflict || len(match.Rules) == 0 {
				continue
			}
			allForbidden := true
			for _, rule := range match.Rules {
				if rule.Forbidden == nil || !*rule.Forbidden {
					allForbidden = false
					break
				}
			}
			if allForbidden {
				blocked = true
				break
			}
		}
		if !blocked {
			return false
		}
	}
	return true
}
