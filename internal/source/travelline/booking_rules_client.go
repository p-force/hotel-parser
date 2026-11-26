package travelline

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
)

// BookingRules получает правила отдельным запросом, без retry и интерпретации.
// Успех всегда возвращает ненулевой ответ с массивом BookingRules (в том числе []).
// При ошибке ответ nil: отсутствие данных не представляется пустым набором правил.
func (c *Client) BookingRules(ctx context.Context, hotelID string, startDate, endDate time.Time, rateIDs []string) (*bookingRulesResponse, error) {
	endpoint, err := c.BookingRulesURL(hotelID, startDate, endDate, rateIDs)
	if err != nil {
		return nil, &source.CollectionError{Code: "invalid_request", Message: "invalid TravelLine booking rules request"}
	}
	requestCtx, cancel := context.WithTimeout(ctx, collectionTimeout)
	defer cancel()
	body, status, err := c.get(requestCtx, endpoint.Path, endpoint.Query())
	if err != nil {
		// Как в Collect: сохраняем классификацию get и добавляем полученный статус.
		var classified *source.CollectionError
		if errors.As(err, &classified) {
			copy := *classified
			copy.HTTPStatus = status
			return nil, &copy
		}
		return nil, err
	}
	var result bookingRulesResponse
	if json.Unmarshal(body, &result) != nil || result.BookingRules == nil {
		return nil, &source.CollectionError{Code: "parse_error", Message: "invalid TravelLine booking rules response", HTTPStatus: status}
	}
	return &result, nil
}
