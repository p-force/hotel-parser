package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// BookingRuleInput сохраняет нативные ограничения без интерпретации.
type BookingRuleInput struct {
	Date          time.Time
	RoomTypeID    *string
	RateID        string
	Forbidden     *bool
	MinLOS        *int
	MaxLOS        *int
	MinAdvBooking *int
}

type BookingRule struct {
	ID, BookingRulesID string
	BookingRuleInput
}

type RunBookingRulesInput struct {
	RunID                   string
	RequestedAt, ReceivedAt time.Time
	StartDate, EndDate      time.Time
	RequestedRateIDs        []string
	Rules                   []BookingRuleInput
}

type RunBookingRules struct {
	ID, RunID               string
	RequestedAt, ReceivedAt time.Time
	StartDate, EndDate      time.Time
	RequestedRateIDs        []string
	Rules                   []BookingRule
}

// SaveRunBookingRules атомарно сохраняет один успешный набор для run.
// Повтор сравнивает метаданные и мультимножество правил без generated IDs:
// порядок правил не значим, но дубликаты, NULL, false и 0 различаются.
func (s *Store) SaveRunBookingRules(ctx context.Context, input RunBookingRulesInput) (RunBookingRules, error) {
	input, hash, err := normalizeRunBookingRules(input)
	if err != nil {
		return RunBookingRules{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunBookingRules{}, dbError("begin booking rules", err)
	}
	defer tx.Rollback()
	var runID string
	if err := tx.QueryRowContext(ctx, `SELECT id::text FROM `+s.table("collection_runs")+` WHERE id=$1::uuid FOR UPDATE`, input.RunID).Scan(&runID); err != nil {
		return RunBookingRules{}, dbError("lock booking rules run", err)
	}
	// UUID из PostgreSQL каноничен и не зависит от регистра входной строки.
	if input.RunID != runID {
		input.RunID = runID
		hash, err = digest(input)
		if err != nil {
			return RunBookingRules{}, err
		}
	}
	existing, err := s.readRunBookingRules(ctx, tx, runID)
	if err == nil {
		stored := RunBookingRulesInput{RunID: existing.RunID, RequestedAt: existing.RequestedAt, ReceivedAt: existing.ReceivedAt, StartDate: existing.StartDate, EndDate: existing.EndDate, RequestedRateIDs: existing.RequestedRateIDs, Rules: make([]BookingRuleInput, len(existing.Rules))}
		for i, rule := range existing.Rules {
			stored.Rules[i] = rule.BookingRuleInput
		}
		_, storedHash, err := normalizeRunBookingRules(stored)
		if err != nil {
			return RunBookingRules{}, err
		}
		if !bytes.Equal(hash, storedHash) {
			return RunBookingRules{}, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return RunBookingRules{}, dbError("commit existing booking rules", err)
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return RunBookingRules{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `INSERT INTO `+s.table("run_booking_rules")+`(run_id,requested_at,received_at,start_date,end_date,requested_rate_ids) VALUES($1::uuid,$2,$3,$4::date,$5::date,$6::text[]) RETURNING id::text`, runID, input.RequestedAt, input.ReceivedAt, input.StartDate.Format("2006-01-02"), input.EndDate.Format("2006-01-02"), input.RequestedRateIDs).Scan(&id)
	if err != nil {
		return RunBookingRules{}, dbError("insert booking rules set", err)
	}
	for _, rule := range input.Rules {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("booking_rules")+`(booking_rules_id,date,room_type_id,rate_id,forbidden,min_los,max_los,min_adv_booking) VALUES($1::uuid,$2::date,$3,$4,$5,$6,$7,$8)`, id, rule.Date.Format("2006-01-02"), rule.RoomTypeID, rule.RateID, rule.Forbidden, rule.MinLOS, rule.MaxLOS, rule.MinAdvBooking); err != nil {
			return RunBookingRules{}, dbError("insert booking rule", err)
		}
	}
	result, err := s.readRunBookingRules(ctx, tx, runID)
	if err != nil {
		return RunBookingRules{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunBookingRules{}, dbError("commit booking rules", err)
	}
	return result, nil
}

// GetRunBookingRules читает полный сохранённый набор, включая пустой Rules=[].
// ErrNotFound означает отсутствие сохранённого набора (в том числе неизвестный run).
func (s *Store) GetRunBookingRules(ctx context.Context, runID string) (RunBookingRules, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RunBookingRules{}, dbError("begin read booking rules", err)
	}
	defer tx.Rollback()
	result, err := s.readRunBookingRules(ctx, tx, runID)
	if err != nil {
		return RunBookingRules{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunBookingRules{}, dbError("commit read booking rules", err)
	}
	return result, nil
}

func (s *Store) readRunBookingRules(ctx context.Context, tx *sql.Tx, runID string) (RunBookingRules, error) {
	var result RunBookingRules
	var rates []byte
	err := tx.QueryRowContext(ctx, `SELECT id::text,run_id::text,requested_at,received_at,start_date,end_date,array_to_json(requested_rate_ids) FROM `+s.table("run_booking_rules")+` WHERE run_id=$1::uuid`, runID).Scan(&result.ID, &result.RunID, &result.RequestedAt, &result.ReceivedAt, &result.StartDate, &result.EndDate, &rates)
	if err != nil {
		return RunBookingRules{}, dbError("read booking rules set", err)
	}
	if err := json.Unmarshal(rates, &result.RequestedRateIDs); err != nil {
		return RunBookingRules{}, dbError("decode booking rules requested rates", err)
	}
	result.Rules = []BookingRule{}
	rows, err := tx.QueryContext(ctx, `SELECT id::text,booking_rules_id::text,date,room_type_id,rate_id,forbidden,min_los,max_los,min_adv_booking FROM `+s.table("booking_rules")+` WHERE booking_rules_id=$1::uuid ORDER BY date,room_type_id NULLS FIRST,rate_id,forbidden NULLS FIRST,min_los NULLS FIRST,max_los NULLS FIRST,min_adv_booking NULLS FIRST,id`, result.ID)
	if err != nil {
		return RunBookingRules{}, dbError("read booking rules", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rule BookingRule
		if err := rows.Scan(&rule.ID, &rule.BookingRulesID, &rule.Date, &rule.RoomTypeID, &rule.RateID, &rule.Forbidden, &rule.MinLOS, &rule.MaxLOS, &rule.MinAdvBooking); err != nil {
			return RunBookingRules{}, dbError("scan booking rule", err)
		}
		result.Rules = append(result.Rules, rule)
	}
	if err := rows.Err(); err != nil {
		return RunBookingRules{}, dbError("read booking rules", err)
	}
	return result, nil
}
