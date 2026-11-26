package collect

import (
	"strings"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
)

// CheckRunBookingRulesApplicability проверяет только метаданные сохранённого набора.
// При успехе возвращает true и пустую причину, иначе false и диагностический код.
// Даты сравниваются как календарные в переданных временных зонах, без перевода в UTC.
// Как в BookingRulesURL, конец каждого диапазона должен быть позже начала;
// покрытие проверяется включительно по обеим границам.
// Тарифы сравниваются как множества: порядок и повторения ID не значимы.
func CheckRunBookingRulesApplicability(saved storage.RunBookingRules, runID string, startDate, endDate time.Time, rateIDs []string) (bool, string) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(saved.RunID) == "" {
		return false, "invalid_run_id"
	}
	if saved.RunID != runID {
		return false, "run_id_mismatch"
	}
	if !validBookingRulesDateRange(startDate, endDate) {
		return false, "invalid_requested_date_range"
	}
	if !validBookingRulesDateRange(saved.StartDate, saved.EndDate) {
		return false, "invalid_saved_date_range"
	}
	if saved.StartDate.Format("2006-01-02") > startDate.Format("2006-01-02") || saved.EndDate.Format("2006-01-02") < endDate.Format("2006-01-02") {
		return false, "date_range_not_covered"
	}
	currentRates, valid := bookingRulesRateSet(rateIDs)
	if !valid {
		return false, "invalid_requested_rate_policy"
	}
	savedRates, valid := bookingRulesRateSet(saved.RequestedRateIDs)
	if !valid {
		return false, "invalid_saved_rate_policy"
	}
	if len(currentRates) != len(savedRates) {
		return false, "rate_policy_mismatch"
	}
	for id := range currentRates {
		if _, found := savedRates[id]; !found {
			return false, "rate_policy_mismatch"
		}
	}
	return true, ""
}

func validBookingRulesDateRange(start, end time.Time) bool {
	return !start.IsZero() && !end.IsZero() &&
		start.Year() >= 1 && start.Year() <= 9999 && end.Year() >= 1 && end.Year() <= 9999 &&
		start.Format("2006-01-02") < end.Format("2006-01-02")
}

func bookingRulesRateSet(ids []string) (map[string]struct{}, bool) {
	if len(ids) == 0 {
		return nil, false
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, false
		}
		set[id] = struct{}{}
	}
	return set, true
}
