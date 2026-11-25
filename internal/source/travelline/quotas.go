package travelline

import (
	"encoding/json"
	"github.com/p-force/hotel-parser/internal/source"
)

// rawRoomTypeQuotas работает до проверки/отбора offers. Связь принадлежит
// исходному room_stay, а не тарифному allowlist или метаданным категорий.
func rawRoomTypeQuotas(body []byte, propertyID string) *source.RoomTypeQuotaSet {
	// Ошибка типа в total/guests/rates не должна отбрасывать корректный массив.
	var data struct {
		Quotas []struct {
			RPH      string `json:"rph"`
			Quantity *int   `json:"quantity"`
		} `json:"room_type_quotas"`
		Stays json.RawMessage `json:"room_stays"`
	}
	if json.Unmarshal(body, &data) != nil {
		return nil
	}
	if data.Quotas == nil {
		return nil
	}
	links := map[string]map[string]bool{}
	uncertain := map[string]bool{}
	var stays []struct {
		Hotel reference `json:"hotel_ref"`
		Rooms []struct {
			Code  string `json:"code"`
			Quota string `json:"room_type_quota_rph"`
		} `json:"room_types"`
	}
	// Повреждённые ссылки не дают права назначать категорию. Сам массив квот
	// при этом остаётся сохранённым, без частично декодированного mapping.
	if json.Unmarshal(data.Stays, &stays) != nil {
		stays = nil
	}
	for _, stay := range stays {
		for _, room := range stay.Rooms {
			if room.Quota == "" {
				continue
			}
			if stay.Hotel.Code != propertyID || room.Code == "" {
				uncertain[room.Quota] = true
				continue
			}
			if links[room.Quota] == nil {
				links[room.Quota] = map[string]bool{}
			}
			links[room.Quota][room.Code] = true
		}
	}
	result := &source.RoomTypeQuotaSet{Items: make([]source.RoomTypeQuota, 0, len(data.Quotas))}
	for i, quota := range data.Quotas {
		item := source.RoomTypeQuota{Ordinal: i, RPH: quota.RPH, Quantity: quota.Quantity, MappingStatus: source.QuotaUnmapped}
		categories := links[quota.RPH]
		if len(categories) > 1 || (len(categories) > 0 && uncertain[quota.RPH]) {
			item.MappingStatus = source.QuotaConflict
		} else if len(categories) == 1 {
			item.MappingStatus = source.QuotaMapped
			for id := range categories {
				category := id
				item.SourceRoomID = &category
			}
		}
		result.Items = append(result.Items, item)
	}
	return result
}
