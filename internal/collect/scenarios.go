package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// CollectConfiguredBatch выполняет все комбинации adults и LOS для одной даты заезда.
// Это явный запуск: флаг enabled относится к будущему планировщику, а не к этому API.
// Каждый SearchResult сохраняет собственный Request, включая число гостей и даты/LOS.
func CollectConfiguredBatch(ctx context.Context, client *bnovo.Client, hotel config.Config, checkIn time.Time, options BatchOptions) ([]bnovo.SearchResult, BatchSummary, error) {
	if err := hotel.Collection.Validate(); err != nil {
		return nil, BatchSummary{}, err
	}
	if checkIn.IsZero() {
		return nil, BatchSummary{}, fmt.Errorf("check-in date is required")
	}
	loc, err := time.LoadLocation(hotel.Timezone)
	if err != nil {
		return nil, BatchSummary{}, err
	}
	day := checkIn.In(loc)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	requests := make([]bnovo.SearchRequest, 0, len(hotel.Collection.Adults)*len(hotel.Collection.Nights))
	for _, adults := range hotel.Collection.Adults {
		for _, nights := range hotel.Collection.Nights {
			requests = append(requests, bnovo.SearchRequest{PropertyID: hotel.Source.PropertyID, CheckIn: day, CheckOut: day.AddDate(0, 0, nights), Adults: adults, Language: "ru"})
		}
	}
	return CollectBatch(ctx, client, requests, options)
}
