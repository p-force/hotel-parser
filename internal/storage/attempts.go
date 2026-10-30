package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// SaveAttempt атомарно добавляет неизменяемые факты и меняет состояние задания.
// Один (job,number) с тем же payload — no-op, с другим — ErrConflict.
func (s *Store) SaveAttempt(ctx context.Context, input AttemptInput) (string, error) {
	input, hash, err := normalizeAttempt(input)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", dbError("begin attempt", err)
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM `+s.table("scenario_jobs")+` WHERE id=$1::uuid FOR UPDATE`, input.JobID))
	if err != nil {
		return "", dbError("lock attempt job", err)
	}
	var id string
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT id::text,payload_hash FROM `+s.table("collection_attempts")+` WHERE job_id=$1::uuid AND attempt_no=$2`, input.JobID, input.Number).Scan(&id, &existing)
	if err == nil {
		if !bytes.Equal(existing, hash) {
			return "", ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return "", dbError("commit existing attempt", err)
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", dbError("read existing attempt", err)
	}
	if input.Number != job.Attempts+1 || (job.Status != JobQueued && job.Status != JobRunning && job.Status != JobRetryable) {
		return "", ErrTransition
	}
	warnings, _ := json.Marshal(input.Warnings)
	var code *string
	retryable := false
	if input.Error != nil {
		code = &input.Error.Code
		retryable = input.Error.Retryable
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO `+s.table("collection_attempts")+`(job_id,attempt_no,payload_hash,started_at,finished_at,duration_ms,queue_wait_ms,collected_at,outcome,http_status,error_code,retryable,warnings,next_retry_at) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14) RETURNING id::text`, input.JobID, input.Number, hash, input.StartedAt, input.FinishedAt, input.DurationMS, input.QueueWaitMS, input.CollectedAt, input.Outcome, input.HTTPStatus, code, retryable, string(warnings), input.NextRetryAt).Scan(&id)
	if err != nil {
		return "", dbError("insert attempt", err)
	}
	for _, observation := range input.Observations {
		if err = s.insertObservation(ctx, tx, id, observation); err != nil {
			return "", err
		}
	}
	status := JobSucceeded
	switch input.Outcome {
	case OutcomePartial:
		status = JobPartial
	case OutcomeNoOffers:
		status = JobNoOffers
	case OutcomeFailed:
		status = JobFailed
		if input.NextRetryAt != nil {
			status = JobRetryable
		} else if input.Error.Code == "canceled" {
			status = JobCanceled
		}
	}
	var finished any = input.FinishedAt
	if status == JobRetryable {
		finished = nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE `+s.table("scenario_jobs")+` SET status=$2,attempts=$3,started_at=COALESCE(started_at,$4),finished_at=$5,next_retry_at=$6,last_error_code=$7 WHERE id=$1::uuid`, input.JobID, status, input.Number, input.StartedAt, finished, input.NextRetryAt, code)
	if err != nil {
		return "", dbError("update attempt job", err)
	}
	if err = tx.Commit(); err != nil {
		return "", dbError("commit attempt", err)
	}
	return id, nil
}
func (s *Store) insertObservation(ctx context.Context, tx *sql.Tx, attemptID string, o Observation) error {
	var id string
	warnings, _ := json.Marshal(o.Warnings)
	err := tx.QueryRowContext(ctx, `INSERT INTO `+s.table("observations")+`(attempt_id,category_id,category_name,area_m2,available,stock_status,has_offers,confirmed_unavailable,warnings) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9::jsonb) RETURNING id::text`, attemptID, o.CategoryID, o.CategoryName, o.AreaM2, o.Available, o.StockStatus, o.HasOffers, o.ConfirmedUnavailable, string(warnings)).Scan(&id)
	if err != nil {
		return dbError("insert category observation", err)
	}
	for _, variant := range o.Variants {
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+s.table("observation_variants")+`(observation_id,variant_id,adults) VALUES($1::uuid,$2,$3)`, id, variant.ID, variant.Adults); err != nil {
			return dbError("insert observed variant", err)
		}
	}
	for _, offer := range o.Offers {
		quantities, _ := json.Marshal(offer.SelectableQuantities)
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+s.table("observation_offers")+`(observation_id,variant_id,rate_id,rate_name,price,currency,meal_plan,visible,data_available,row_available,data_available_for_guests,select_disabled,selectable_quantities) VALUES($1::uuid,$2,$3,$4,$5::numeric,$6,$7,$8,$9,$10,$11,$12,ARRAY(SELECT jsonb_array_elements_text($13::jsonb)::integer))`, id, offer.VariantID, offer.RateID, offer.RateName, offer.Price, offer.Currency, offer.MealPlan, offer.Visible, offer.DataAvailable, offer.RowAvailable, offer.DataAvailableForGuests, offer.SelectDisabled, string(quantities)); err != nil {
			return dbError("insert observed offer", err)
		}
	}
	return nil
}
