package collect

import (
	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/storage"
)

// Коллектор переносит source evidence без фильтрации и нормализации количества.
type RoomTypeQuota = source.RoomTypeQuota
type RoomTypeQuotaSet = source.RoomTypeQuotaSet

func storedRoomTypeQuotas(input *RoomTypeQuotaSet) *storage.RoomTypeQuotaSet {
	if input == nil {
		return nil
	}
	result := &storage.RoomTypeQuotaSet{Items: make([]storage.RoomTypeQuota, 0, len(input.Items))}
	for _, q := range input.Items {
		item := storage.RoomTypeQuota{Ordinal: q.Ordinal, RPH: q.RPH, MappingStatus: storage.QuotaMappingStatus(q.MappingStatus)}
		if q.Quantity != nil {
			v := *q.Quantity
			item.Quantity = &v
		}
		if q.SourceRoomID != nil {
			v := *q.SourceRoomID
			item.SourceRoomID = &v
		}
		result.Items = append(result.Items, item)
	}
	return result
}
