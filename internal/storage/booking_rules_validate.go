package storage

import (
	"encoding/json"
	"fmt"
	"sort"
)

func normalizeRunBookingRules(in RunBookingRulesInput) (RunBookingRulesInput, []byte, error) {
	if in.RunID == "" || in.RequestedAt.IsZero() || in.ReceivedAt.IsZero() || in.StartDate.IsZero() || in.EndDate.IsZero() {
		return in, nil, fmt.Errorf("invalid booking rules metadata")
	}
	in.RequestedAt, in.ReceivedAt = stamp(in.RequestedAt), stamp(in.ReceivedAt)
	in.StartDate, in.EndDate = civil(in.StartDate), civil(in.EndDate)
	if in.StartDate.IsZero() || in.EndDate.IsZero() {
		return in, nil, fmt.Errorf("invalid booking rules dates")
	}
	in.RequestedRateIDs = append([]string{}, in.RequestedRateIDs...)
	type orderedRule struct {
		rule BookingRuleInput
		key  string
	}
	ordered := make([]orderedRule, len(in.Rules))
	for i, rule := range in.Rules {
		if rule.Date.IsZero() {
			return in, nil, fmt.Errorf("booking rule date is required")
		}
		rule.Date = civil(rule.Date)
		if rule.Date.IsZero() {
			return in, nil, fmt.Errorf("invalid booking rule date")
		}
		rule.RoomTypeID = copyBookingRuleValue(rule.RoomTypeID)
		rule.Forbidden = copyBookingRuleValue(rule.Forbidden)
		rule.MinLOS = copyBookingRuleValue(rule.MinLOS)
		rule.MaxLOS = copyBookingRuleValue(rule.MaxLOS)
		rule.MinAdvBooking = copyBookingRuleValue(rule.MinAdvBooking)
		encoded, err := json.Marshal(rule)
		if err != nil {
			return in, nil, fmt.Errorf("invalid booking rule")
		}
		ordered[i] = orderedRule{rule: rule, key: string(encoded)}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].key < ordered[j].key })
	in.Rules = make([]BookingRuleInput, len(ordered))
	for i, item := range ordered {
		in.Rules[i] = item.rule
	}
	// Не проверяем смысл ограничений, диапазон min/max и соответствие тарифов
	// requested_rate_ids: сохраняются также противоречивые/неожиданные source rows.
	hash, err := digest(in)
	return in, hash, err
}

func copyBookingRuleValue[T any](input *T) *T {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}
