package schedule

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/storage"
)

type Plan struct {
	GroupID   string
	Schema    string
	Run       storage.RunInput
	Scenarios []storage.Scenario
}

func SlotHorizon(slot time.Time) (int, error) {
	if slot.IsZero() {
		return 0, fmt.Errorf("slot timestamp is required")
	}
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return 0, err
	}
	slot = slot.In(loc)
	if slot.Minute() != 0 || slot.Second() != 0 || slot.Nanosecond() != 0 {
		return 0, fmt.Errorf("slot must be exactly 08:00/12:00/16:00/20:00 Europe/Moscow")
	}
	switch slot.Hour() {
	case 8:
		return 60, nil
	case 12, 16:
		return 7, nil
	case 20:
		return 0, nil
	}
	return 0, fmt.Errorf("unsupported Moscow slot")
}

// BuildPlans не обращается к HTTP/БД. Slot — логическое время cron, а не delivery.
func BuildPlans(registry Registry, slot time.Time) ([]Plan, error) {
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	horizon, err := SlotHorizon(slot)
	if err != nil {
		return nil, err
	}
	plans := make([]Plan, 0)
	for _, group := range registry.Groups {
		for _, property := range group.Properties {
			hotel := property.Hotel
			if !hotel.Collection.Enabled {
				continue
			}
			loc, err := time.LoadLocation(hotel.Timezone)
			if err != nil {
				return nil, err
			}
			local := slot.In(loc)
			today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
			los := property.LOS
			if los == 0 {
				los = 1
			}
			// UUID включает слот/group/property; не зависит от времени доставки trigger.
			identity, _ := json.Marshal([]string{"hotel-parser/schedule/v1", group.ID, hotel.ID, slot.UTC().Format(time.RFC3339)})
			hash := sha256.Sum256(identity)
			hash[6] = (hash[6] & 0x0f) | 0x50
			hash[8] = (hash[8] & 0x3f) | 0x80
			id := fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
			plan := Plan{GroupID: group.ID, Schema: group.Schema, Run: storage.RunInput{ID: id, PropertyID: hotel.ID, PeriodStart: today, PeriodEnd: today.AddDate(0, 0, horizon+los), StartedAt: slot.UTC()}}
			origin, err := collect.SourceKey(hotel.Source.BaseURL)
			if err != nil {
				return nil, err
			}
			for day := 0; day <= horizon; day++ {
				in := today.AddDate(0, 0, day)
				plan.Scenarios = append(plan.Scenarios, storage.Scenario{PropertyID: hotel.ID, Source: hotel.Source.Type, SourceOrigin: origin, SourcePropertyID: hotel.Source.PropertyID, CheckIn: in, CheckOut: in.AddDate(0, 0, los), LOS: los, Adults: 1, Rooms: 1, ChildrenAges: []int{}, Language: "ru"})
			}
			plans = append(plans, plan)
		}
	}
	return plans, nil
}
