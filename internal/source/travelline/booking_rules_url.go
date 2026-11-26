package travelline

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

const bookingRulesPath = "/ApiWebDistribution/AvailabilityCalendar/rate_plan_booking_rules"

// BookingRulesURL формирует URL без HTTP-запроса. Тарифы передаются явно,
// независимо от publicRates клиента. Даты используются как календарные даты
// в переданной временной зоне; конечная дата должна быть позже начальной.
func (c *Client) BookingRulesURL(hotelID string, startDate, endDate time.Time, rateIDs []string) (*url.URL, error) {
	if c == nil || c.origin == "" {
		return nil, fmt.Errorf("TravelLine client is not initialized")
	}
	if !validPropertyID(hotelID) {
		return nil, fmt.Errorf("invalid TravelLine hotel ID")
	}
	if startDate.IsZero() || endDate.IsZero() || startDate.Year() < 1 || startDate.Year() > 9999 || endDate.Year() < 1 || endDate.Year() > 9999 {
		return nil, fmt.Errorf("invalid TravelLine booking rules dates")
	}
	start, end := startDate.Format("2006-01-02"), endDate.Format("2006-01-02")
	if start >= end {
		return nil, fmt.Errorf("invalid TravelLine booking rules date range")
	}
	if len(rateIDs) == 0 {
		return nil, fmt.Errorf("TravelLine booking rules require at least one rate ID")
	}
	query := url.Values{
		"hotel":      {hotelID},
		"start_date": {start},
		"end_date":   {end},
		"audience":   {"BookingForm"},
	}
	for i, id := range rateIDs {
		// Нативные hotel/rate code используют числовой формат TravelLine ID.
		if !validPropertyID(id) {
			return nil, fmt.Errorf("invalid TravelLine rate ID at index %d", i)
		}
		query.Set("rate_plans["+strconv.Itoa(i)+"].code", id)
	}
	endpoint, err := url.Parse(c.origin)
	if err != nil {
		return nil, fmt.Errorf("invalid TravelLine origin")
	}
	endpoint.Path = bookingRulesPath
	endpoint.RawQuery = query.Encode()
	return endpoint, nil
}
