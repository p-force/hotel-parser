package travelline

import "time"

// ConfirmCategoryBookingForbidden проверяет явный запрет только для LOS=1
// и переданного списка применимых тарифов. false означает отсутствие
// подтверждения, а не разрешение бронирования. Прочие ограничения не толкуются.
func ConfirmCategoryBookingForbidden(index BookingRulesIndex, checkIn time.Time, roomTypeID string, rateIDs []string, los int) bool {
	if los != 1 || len(rateIDs) == 0 || checkIn.IsZero() || roomTypeID == "" {
		return false
	}
	date := checkIn.Format("2006-01-02")
	for _, rateID := range rateIDs {
		if rateID == "" {
			return false
		}
		match, found := index.Lookup(BookingRuleKey{Date: date, RoomTypeID: roomTypeID, RateID: rateID})
		if !found || match.Conflict || len(match.Rules) == 0 {
			return false
		}
		for _, rule := range match.Rules {
			if rule.Forbidden == nil || !*rule.Forbidden {
				return false
			}
		}
	}
	return true
}
