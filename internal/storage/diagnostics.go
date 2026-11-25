package storage

import (
	"context"
	"github.com/p-force/hotel-parser/internal/config"
)

// DiagnosticAttempt сохраняет отсутствие попытки отдельным nil, а не пустым успехом.
type DiagnosticAttempt struct {
	Job     Job      `json:"job"`
	Attempt *Attempt `json:"attempt"`
}

type RunDiagnosticData struct {
	Run       Run                     `json:"run"`
	Inventory *config.InventoryConfig `json:"inventory"`
	Scenarios []DiagnosticAttempt     `json:"scenarios"`
}

// ReadRunDiagnostics читает только завершённый run. Его immutable attempts и
// terminal jobs образуют стабильный набор без runner lock и без HTTP.
func (s *Store) ReadRunDiagnostics(ctx context.Context, runID string) (RunDiagnosticData, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return RunDiagnosticData{}, err
	}
	if run.Status == RunRunning || run.FinishedAt == nil {
		return RunDiagnosticData{}, ErrIncomplete
	}
	data := RunDiagnosticData{Run: run, Scenarios: []DiagnosticAttempt{}}
	data.Inventory, err = s.GetRunInventory(ctx, runID)
	if err != nil {
		return data, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM `+s.table("scenario_jobs")+` WHERE run_id=$1::uuid ORDER BY check_in,los,adults,rooms,id`, runID)
	if err != nil {
		return data, dbError("read diagnostic jobs", err)
	}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return data, dbError("scan diagnostic job", err)
		}
		data.Scenarios = append(data.Scenarios, DiagnosticAttempt{Job: job})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return data, dbError("read diagnostic jobs", err)
	}
	for i := range data.Scenarios {
		scenario := &data.Scenarios[i]
		if scenario.Job.Attempts == 0 {
			continue
		}
		attempt, err := s.GetAttempt(ctx, scenario.Job.ID, scenario.Job.Attempts)
		if err != nil {
			return data, err
		}
		scenario.Attempt = &attempt
	}
	return data, ctx.Err()
}
