package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// PlanRun публикует весь план атомарно: runner не видит частично созданный run.
// Стабильный ID задаёт planner; изменение сценариев при повторе слота — конфликт.
func (s *Store) PlanRun(ctx context.Context, input RunInput, scenarios []Scenario) (Run, error) {
	if input.ID == "" || len(scenarios) == 0 {
		return Run{}, fmt.Errorf("plan requires stable run ID and scenarios")
	}
	scenarios = append([]Scenario(nil), scenarios...)
	for i := range scenarios {
		var err error
		scenarios[i], err = normalizeScenario(scenarios[i])
		if err != nil {
			return Run{}, err
		}
	}
	sort.Slice(scenarios, func(i, j int) bool {
		a, _ := digest(scenarios[i])
		b, _ := digest(scenarios[j])
		return bytes.Compare(a, b) < 0
	})
	hash, err := digest(scenarios)
	if err != nil {
		return Run{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, dbError("begin plan", err)
	}
	defer tx.Rollback()
	run, err := s.createRunTx(ctx, tx, input)
	if err != nil {
		return Run{}, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT id FROM `+s.table("collection_runs")+` WHERE id=$1::uuid FOR UPDATE`, run.ID); err != nil {
		return Run{}, dbError("lock plan", err)
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT plan_hash FROM `+s.table("collection_plans")+` WHERE run_id=$1::uuid`, run.ID).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, hash) {
			return Run{}, ErrConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+s.table("collection_plans")+`(run_id,plan_hash) VALUES($1::uuid,$2)`, run.ID, hash); err != nil {
			return Run{}, dbError("record plan", err)
		}
	} else {
		return Run{}, dbError("read plan", err)
	}
	for _, scenario := range scenarios {
		if _, err = s.createJobTx(ctx, tx, run.ID, scenario); err != nil {
			return Run{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Run{}, dbError("commit plan", err)
	}
	return run, nil
}
