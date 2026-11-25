package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/p-force/hotel-parser/internal/config"
)

// Снимок записывается в той же транзакции, что и новый run. Повтор плана не
// подменяет прошлую конфигурацию актуальными значениями и не меняет историю.
func (s *Store) saveRunInventoryTx(ctx context.Context, tx *sql.Tx, runID string, inventory config.InventoryConfig) error {
	if err := inventory.Validate(); err != nil {
		return fmt.Errorf("invalid inventory snapshot: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("run_inventory")+`(run_id,physical_total_rooms,public_observed_max,physical_source,public_source,categories_form_complete_partition) VALUES($1::uuid,$2,$3,NULLIF($4,''),NULLIF($5,''),$6)`, runID, inventory.PhysicalTotalRooms, inventory.PublicObservedMax, inventory.PhysicalSource, inventory.PublicSource, inventory.CategoriesFormCompletePartition)
	if err != nil {
		return dbError("save run inventory", err)
	}
	for i, c := range inventory.Categories {
		_, err = tx.ExecContext(ctx, `INSERT INTO `+s.table("run_category_inventory")+`(run_id,source_room_id,category_id,category_name,position,physical_total_rooms,public_observed_max) VALUES($1::uuid,$2,$3,$4,$5,$6,$7)`, runID, c.SourceRoomID, c.ID, c.Name, i, c.PhysicalTotalRooms, c.PublicObservedMax)
		if err != nil {
			return dbError("save category inventory", err)
		}
	}
	return nil
}

// GetRunInventory читает исходную конфигурацию run. nil означает, что снимка
// нет (например, run создан до 0006); значения не выводятся из публичных остатков.
func (s *Store) GetRunInventory(ctx context.Context, runID string) (*config.InventoryConfig, error) {
	var inventory config.InventoryConfig
	err := s.db.QueryRowContext(ctx, `SELECT physical_total_rooms,public_observed_max,COALESCE(physical_source,''),COALESCE(public_source,''),categories_form_complete_partition FROM `+s.table("run_inventory")+` WHERE run_id=$1::uuid`, runID).Scan(&inventory.PhysicalTotalRooms, &inventory.PublicObservedMax, &inventory.PhysicalSource, &inventory.PublicSource, &inventory.CategoriesFormCompletePartition)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, dbError("read run inventory", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT category_id,category_name,source_room_id,physical_total_rooms,public_observed_max FROM `+s.table("run_category_inventory")+` WHERE run_id=$1::uuid ORDER BY position`, runID)
	if err != nil {
		return nil, dbError("read category inventory", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c config.RoomCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.SourceRoomID, &c.PhysicalTotalRooms, &c.PublicObservedMax); err != nil {
			return nil, dbError("scan category inventory", err)
		}
		inventory.Categories = append(inventory.Categories, c)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("iterate category inventory", err)
	}
	return &inventory, nil
}
