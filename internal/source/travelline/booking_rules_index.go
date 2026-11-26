package travelline

// BookingRuleKey идентифицирует правило без склейки строк и неоднозначных разделителей.
// Date сохраняет исходную календарную дату TravelLine; RoomTypeID и RateID — source ID.
type BookingRuleKey struct {
	Date       string
	RoomTypeID string
	RateID     string
}

// BookingRuleMatch содержит все исходные записи для ключа, включая дубликаты.
// Conflict означает различие полей ограничений, а не их бизнес-интерпретацию.
// При конфликте ни одна запись не выбирается в качестве единственного правила.
type BookingRuleMatch struct {
	Rules    []bookingRule
	Conflict bool
}

// BookingRulesIndex — неизменяемый после построения снимок полученных правил.
type BookingRulesIndex struct {
	entries map[BookingRuleKey]BookingRuleMatch
}

// Index раскрывает room_types, сохраняя все поля исходных записей и их порядок.
// Записи без категорий не индексируются и не становятся глобальными правилами.
func (r bookingRulesResponse) Index() BookingRulesIndex {
	index := BookingRulesIndex{entries: make(map[BookingRuleKey]BookingRuleMatch)}
	for _, rule := range r.BookingRules {
		seen := make(map[string]bool, len(rule.RoomTypes))
		for _, roomTypeID := range rule.RoomTypes {
			// Повтор категории внутри одной записи не является второй записью.
			if seen[roomTypeID] {
				continue
			}
			seen[roomTypeID] = true
			key := BookingRuleKey{Date: rule.Date, RoomTypeID: roomTypeID, RateID: rule.Code}
			match := index.entries[key]
			if len(match.Rules) > 0 && !sameBookingRestrictions(match.Rules[0], rule) {
				match.Conflict = true
			}
			match.Rules = append(match.Rules, cloneBookingRule(rule))
			index.entries[key] = match
		}
	}
	return index
}

// Lookup различает отсутствие ключа (found=false), запись без ограничений
// (found=true, поля ограничений nil) и конфликтующие записи (Conflict=true).
// Копия результата не позволяет вызывающему изменить индекс.
func (i BookingRulesIndex) Lookup(key BookingRuleKey) (match BookingRuleMatch, found bool) {
	stored, found := i.entries[key]
	if !found {
		return BookingRuleMatch{}, false
	}
	match = BookingRuleMatch{Rules: make([]bookingRule, len(stored.Rules)), Conflict: stored.Conflict}
	for n, rule := range stored.Rules {
		match.Rules[n] = cloneBookingRule(rule)
	}
	return match, true
}

func sameBookingRestrictions(a, b bookingRule) bool {
	return sameBookingValue(a.Forbidden, b.Forbidden) &&
		sameBookingValue(a.MinLOS, b.MinLOS) &&
		sameBookingValue(a.MaxLOS, b.MaxLOS) &&
		sameBookingValue(a.MinAdvBooking, b.MinAdvBooking)
}

func sameBookingValue[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func cloneBookingRule(rule bookingRule) bookingRule {
	if rule.RoomTypes != nil {
		rule.RoomTypes = append([]string{}, rule.RoomTypes...)
	}
	if rule.Forbidden != nil {
		value := *rule.Forbidden
		rule.Forbidden = &value
	}
	if rule.MinLOS != nil {
		value := *rule.MinLOS
		rule.MinLOS = &value
	}
	if rule.MaxLOS != nil {
		value := *rule.MaxLOS
		rule.MaxLOS = &value
	}
	if rule.MinAdvBooking != nil {
		value := *rule.MinAdvBooking
		rule.MinAdvBooking = &value
	}
	return rule
}
