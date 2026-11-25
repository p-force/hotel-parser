package storage

import (
	"context"
	"database/sql"
	"fmt"
)

type QuotaMappingStatus string

const (
	QuotaMapped   QuotaMappingStatus = "mapped"
	QuotaUnmapped QuotaMappingStatus = "unmapped"
	QuotaConflict QuotaMappingStatus = "conflict"
)

type RoomTypeQuota struct {
	Ordinal       int
	RPH           string
	Quantity      *int
	SourceRoomID  *string
	MappingStatus QuotaMappingStatus
}

// Оболочка отличает пустой исходный массив от отсутствия evidence/старой истории.
type RoomTypeQuotaSet struct{ Items []RoomTypeQuota }

func normalizedRoomTypeQuotas(input *RoomTypeQuotaSet) (*RoomTypeQuotaSet, error) {
	if input == nil {
		return nil, nil
	}
	result := &RoomTypeQuotaSet{Items: make([]RoomTypeQuota, len(input.Items))}
	for i, q := range input.Items {
		if q.Ordinal != i {
			return nil, fmt.Errorf("quota ordinal must match array position")
		}
		switch q.MappingStatus {
		case QuotaMapped:
			if q.SourceRoomID == nil || *q.SourceRoomID == "" {
				return nil, fmt.Errorf("mapped quota requires category")
			}
		case QuotaUnmapped, QuotaConflict:
			if q.SourceRoomID != nil {
				return nil, fmt.Errorf("uncertain quota must not assign category")
			}
		default:
			return nil, fmt.Errorf("invalid quota mapping status")
		}
		if q.Quantity != nil {
			v := *q.Quantity
			q.Quantity = &v
		}
		if q.SourceRoomID != nil {
			v := *q.SourceRoomID
			q.SourceRoomID = &v
		}
		result.Items[i] = q
	}
	return result, nil
}

func (s *Store) insertRoomTypeQuotas(ctx context.Context, tx *sql.Tx, attemptID string, quotas *RoomTypeQuotaSet) error {
	if quotas == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("attempt_room_type_quota_sets")+`(attempt_id) VALUES($1::uuid)`, attemptID); err != nil {
		return dbError("insert quota set", err)
	}
	for _, q := range quotas.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("attempt_room_type_quotas")+`(attempt_id,ordinal,rph,quantity,source_room_id,mapping_status) VALUES($1::uuid,$2,$3,$4,$5,$6)`, attemptID, q.Ordinal, q.RPH, q.Quantity, q.SourceRoomID, q.MappingStatus); err != nil {
			return dbError("insert raw quota", err)
		}
	}
	return nil
}

func (s *Store) readRoomTypeQuotas(ctx context.Context, attemptID string) (*RoomTypeQuotaSet, error) {
	var present bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+s.table("attempt_room_type_quota_sets")+` WHERE attempt_id=$1::uuid)`, attemptID).Scan(&present); err != nil {
		return nil, dbError("read quota presence", err)
	}
	if !present {
		return nil, nil
	}
	result := &RoomTypeQuotaSet{Items: []RoomTypeQuota{}}
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal,rph,quantity,source_room_id,mapping_status FROM `+s.table("attempt_room_type_quotas")+` WHERE attempt_id=$1::uuid ORDER BY ordinal`, attemptID)
	if err != nil {
		return nil, dbError("read raw quotas", err)
	}
	defer rows.Close()
	for rows.Next() {
		var q RoomTypeQuota
		if err := rows.Scan(&q.Ordinal, &q.RPH, &q.Quantity, &q.SourceRoomID, &q.MappingStatus); err != nil {
			return nil, dbError("scan raw quota", err)
		}
		result.Items = append(result.Items, q)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("read raw quotas", err)
	}
	return result, nil
}
