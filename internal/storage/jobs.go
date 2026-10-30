package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type scanner interface{ Scan(...any) error }

const runColumns = `id::text,property_id,period_start,period_end,started_at,finished_at,status`

func scanRun(row scanner) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.PropertyID, &r.PeriodStart, &r.PeriodEnd, &r.StartedAt, &r.FinishedAt, &r.Status)
	return r, err
}

func (s *Store) CreateRun(ctx context.Context, input RunInput) (Run, error) {
	if !validID(input.PropertyID) || len(input.PropertyID) > 128 || input.PeriodStart.IsZero() || input.PeriodEnd.IsZero() {
		return Run{}, fmt.Errorf("invalid collection run")
	}
	input.PeriodStart = civil(input.PeriodStart)
	input.PeriodEnd = civil(input.PeriodEnd)
	if !input.PeriodEnd.After(input.PeriodStart) {
		return Run{}, fmt.Errorf("invalid run period")
	}
	explicitStart := !input.StartedAt.IsZero()
	if !explicitStart {
		input.StartedAt = time.Now()
	}
	input.StartedAt = stamp(input.StartedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, dbError("begin run", err)
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `INSERT INTO `+s.table("collection_runs")+`(id,property_id,period_start,period_end,started_at,status) VALUES(COALESCE(NULLIF($1,'')::uuid,gen_random_uuid()),$2,$3::date,$4::date,$5,'running') ON CONFLICT(id) DO NOTHING RETURNING id::text`, input.ID, input.PropertyID, input.PeriodStart.Format("2006-01-02"), input.PeriodEnd.Format("2006-01-02"), input.StartedAt).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = input.ID
	} else if err != nil {
		return Run{}, dbError("create run", err)
	}
	result, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM `+s.table("collection_runs")+` WHERE id=$1::uuid`, id))
	if err != nil {
		return Run{}, dbError("read run", err)
	}
	if result.PropertyID != input.PropertyID || !result.PeriodStart.Equal(input.PeriodStart) || !result.PeriodEnd.Equal(input.PeriodEnd) || (explicitStart && !result.StartedAt.Equal(input.StartedAt)) {
		return Run{}, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return Run{}, dbError("commit run", err)
	}
	return result, nil
}
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM `+s.table("collection_runs")+` WHERE id=$1::uuid`, id))
	if err != nil {
		return r, dbError("read run", err)
	}
	return r, nil
}

const jobColumns = `id::text,run_id::text,property_id,source,source_origin,source_property_id,check_in,check_out,los,adults,rooms,array_to_json(children_ages),language,status,attempts,COALESCE(last_error_code,''),created_at,started_at,finished_at,next_retry_at,COALESCE(lease_owner,''),lease_token,leased_until,max_claims,max_attempts`

func scanJob(row scanner) (Job, error) {
	var j Job
	var children []byte
	err := row.Scan(&j.ID, &j.RunID, &j.Scenario.PropertyID, &j.Scenario.Source, &j.Scenario.SourceOrigin, &j.Scenario.SourcePropertyID, &j.Scenario.CheckIn, &j.Scenario.CheckOut, &j.Scenario.LOS, &j.Scenario.Adults, &j.Scenario.Rooms, &children, &j.Scenario.Language, &j.Status, &j.Attempts, &j.LastErrorCode, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.NextRetryAt, &j.LeaseOwner, &j.LeaseToken, &j.LeasedUntil, &j.MaxClaims, &j.MaxAttempts)
	if err == nil {
		err = json.Unmarshal(children, &j.Scenario.ChildrenAges)
	}
	return j, err
}
func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM `+s.table("scenario_jobs")+` WHERE id=$1::uuid`, id))
	if err != nil {
		return j, dbError("read job", err)
	}
	return j, nil
}
func (s *Store) CreateJob(ctx context.Context, runID string, input Scenario) (Job, error) {
	input, err := normalizeScenario(input)
	if err != nil {
		return Job{}, err
	}
	key, err := digest(input)
	if err != nil {
		return Job{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, dbError("begin job", err)
	}
	defer tx.Rollback()
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM `+s.table("collection_runs")+` WHERE id=$1::uuid FOR UPDATE`, runID))
	if err != nil {
		return Job{}, dbError("read job run", err)
	}
	existing, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM `+s.table("scenario_jobs")+` WHERE run_id=$1::uuid AND scenario_key=$2`, runID, key))
	if err == nil {
		if err = tx.Commit(); err != nil {
			return Job{}, dbError("commit existing job", err)
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, dbError("read existing job", err)
	}
	if run.Status != RunRunning {
		return Job{}, ErrTransition
	}
	if input.PropertyID != run.PropertyID || input.CheckIn.Before(run.PeriodStart) || input.CheckOut.After(run.PeriodEnd) {
		return Job{}, fmt.Errorf("scenario is outside its run")
	}
	children, _ := json.Marshal(input.ChildrenAges)
	result, err := scanJob(tx.QueryRowContext(ctx, `INSERT INTO `+s.table("scenario_jobs")+`(run_id,scenario_key,property_id,source,source_origin,source_property_id,check_in,check_out,los,adults,rooms,children_ages,language,status) VALUES($1::uuid,$2,$3,$4,$5,$6,$7::date,$8::date,$9,$10,$11,ARRAY(SELECT jsonb_array_elements_text($12::jsonb)::integer),$13,'queued') RETURNING `+jobColumns, runID, key, input.PropertyID, input.Source, input.SourceOrigin, input.SourcePropertyID, input.CheckIn.Format("2006-01-02"), input.CheckOut.Format("2006-01-02"), input.LOS, input.Adults, input.Rooms, string(children), input.Language))
	if err != nil {
		return Job{}, dbError("create job", err)
	}
	if err = tx.Commit(); err != nil {
		return Job{}, dbError("commit job", err)
	}
	return result, nil
}

// MarkRunning — legacy-запись для никогда не захваченных заданий.
// После первого claim требуется lease-aware API; иначе ErrLeaseLost.
func (s *Store) MarkRunning(ctx context.Context, jobID string, number int, startedAt time.Time) error {
	if number < 1 || startedAt.IsZero() {
		return fmt.Errorf("invalid attempt start")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE `+s.table("scenario_jobs")+` SET status='running',started_at=COALESCE(started_at,$3),next_retry_at=NULL WHERE id=$1::uuid AND attempts=$2-1 AND lease_token=0 AND status IN ('queued','retryable','running')`, jobID, number, stamp(startedAt))
	if err != nil {
		return dbError("mark job running", err)
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		job, readErr := s.GetJob(ctx, jobID)
		if readErr != nil {
			return readErr
		}
		if job.LeaseToken > 0 {
			return ErrLeaseLost
		}
		return ErrTransition
	}
	return nil
}
func (s *Store) CancelPending(ctx context.Context, runID, code string) error {
	if code != "canceled" && code != "timeout" {
		return fmt.Errorf("invalid cancellation code")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE `+s.table("scenario_jobs")+` SET status='canceled',finished_at=GREATEST(now(),COALESCE(started_at,now())),next_retry_at=NULL,last_error_code=$2 WHERE run_id=$1::uuid AND lease_token=0 AND status IN ('queued','running','retryable')`, runID, code)
	if err != nil {
		return dbError("cancel pending jobs", err)
	}
	return nil
}
func (s *Store) FinishRun(ctx context.Context, id string) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, dbError("begin finish run", err)
	}
	defer tx.Rollback()
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM `+s.table("collection_runs")+` WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return Run{}, dbError("read run to finish", err)
	}
	if run.Status != RunRunning {
		return run, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT status,count(*) FROM `+s.table("scenario_jobs")+` WHERE run_id=$1::uuid GROUP BY status`, id)
	if err != nil {
		return Run{}, dbError("count run jobs", err)
	}
	counts := make(map[JobStatus]int)
	total := 0
	for rows.Next() {
		var status JobStatus
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			rows.Close()
			return Run{}, dbError("read job counts", err)
		}
		counts[status] = count
		total += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Run{}, dbError("read job counts", err)
	}
	if total == 0 || counts[JobQueued]+counts[JobRunning]+counts[JobRetryable] > 0 {
		return Run{}, ErrIncomplete
	}
	status := RunSucceeded
	good := counts[JobSucceeded] + counts[JobNoOffers]
	switch {
	case counts[JobCanceled] == total:
		status = RunCanceled
	case good == 0 && counts[JobPartial] == 0:
		status = RunFailed
	case good != total:
		status = RunPartial
	}
	run, err = scanRun(tx.QueryRowContext(ctx, `UPDATE `+s.table("collection_runs")+` SET status=$2,finished_at=GREATEST(now(),started_at) WHERE id=$1::uuid RETURNING `+runColumns, id, status))
	if err != nil {
		return Run{}, dbError("finish run", err)
	}
	if err = tx.Commit(); err != nil {
		return Run{}, dbError("commit finish run", err)
	}
	return run, nil
}
