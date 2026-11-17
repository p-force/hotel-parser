package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"time"
)

// SaveAttempt атомарно добавляет неизменяемые факты и меняет состояние задания.
// Один (job,number) с тем же payload — no-op, с другим — ErrConflict.
func (s *Store) SaveAttempt(ctx context.Context, input AttemptInput) (string, error) {
	return s.saveAttempt(ctx, input, nil)
}

func (s *Store) saveAttempt(ctx context.Context, input AttemptInput, policy *FailPolicy) (string, error) {
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
	if err = s.checkLease(ctx, tx, job, input.Lease, job.Status == JobRunning || (job.Status == JobRetryable && job.LeasedUntil != nil)); err != nil {
		return "", err
	}
	requestHash := hash
	var id string
	var existing, existingRequest []byte
	err = tx.QueryRowContext(ctx, `SELECT id::text,payload_hash,request_hash FROM `+s.table("collection_attempts")+` WHERE job_id=$1::uuid AND attempt_no=$2`, input.JobID, input.Number).Scan(&id, &existing, &existingRequest)
	if err == nil {
		if !bytes.Equal(existing, hash) && !bytes.Equal(existingRequest, hash) {
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
	if err = s.checkLease(ctx, tx, job, input.Lease, true); err != nil {
		return "", err
	}
	if job.LeaseToken > 0 && input.Number > job.MaxAttempts {
		return "", ErrAttemptLimit
	}
	if policy != nil {
		var next *time.Time
		if input.Error.Retryable && input.Error.Code != "canceled" && input.Number < job.MaxAttempts && job.LeaseToken < int64(job.MaxClaims) {
			now, err := databaseNow(ctx, tx)
			if err != nil {
				return "", err
			}
			ceiling := policy.InitialBackoff
			for i := 1; i < input.Number && ceiling < policy.MaxBackoff; i++ {
				if ceiling > policy.MaxBackoff/2 {
					ceiling = policy.MaxBackoff
				} else {
					ceiling *= 2
				}
			}
			delay := ceiling/2 + time.Duration(rand.Int64N(int64(ceiling-ceiling/2)))
			at := now.Add(delay)
			if input.NextRetryAt != nil && input.NextRetryAt.After(at) {
				at = *input.NextRetryAt
			}
			next = &at
		}
		input.NextRetryAt = next
		input, hash, err = normalizeAttempt(input)
		if err != nil {
			return "", err
		}
	} else if job.LeaseToken > 0 && input.NextRetryAt != nil && input.Number >= job.MaxAttempts {
		return "", ErrAttemptLimit
	}
	warnings, _ := json.Marshal(input.Warnings)
	var code *string
	retryable := false
	if input.Error != nil {
		code = &input.Error.Code
		retryable = input.Error.Retryable
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO `+s.table("collection_attempts")+`(job_id,attempt_no,payload_hash,started_at,finished_at,duration_ms,queue_wait_ms,collected_at,outcome,http_status,error_code,retryable,warnings,next_retry_at,lease_owner,lease_token,request_hash) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15,$16,$17) RETURNING id::text`, input.JobID, input.Number, hash, input.StartedAt, input.FinishedAt, input.DurationMS, input.QueueWaitMS, input.CollectedAt, input.Outcome, input.HTTPStatus, code, retryable, string(warnings), input.NextRetryAt, nullableOwner(input.Lease), job.LeaseToken, requestHash).Scan(&id)
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
	var leasedUntil any
	if input.NextRetryAt != nil && policy == nil && job.LeaseToken > 0 {
		leasedUntil = job.LeasedUntil
	}
	_, err = tx.ExecContext(ctx, `UPDATE `+s.table("scenario_jobs")+` SET status=$2,attempts=$3,started_at=COALESCE(started_at,$4),finished_at=$5,next_retry_at=$6,last_error_code=$7,leased_until=$8 WHERE id=$1::uuid`, input.JobID, status, input.Number, input.StartedAt, finished, input.NextRetryAt, code, leasedUntil)
	if err != nil {
		return "", dbError("update attempt job", err)
	}
	if err = s.checkLease(ctx, tx, job, input.Lease, true); err != nil {
		return "", err
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+s.table("observation_offers")+`(observation_id,variant_id,rate_id,rate_name,price,currency,meal_plan,visible,data_available,row_available,data_available_for_guests,select_disabled,selectable_quantities,reported_quantity) VALUES($1::uuid,$2,$3,$4,$5::numeric,$6,$7,$8,$9,$10,$11,$12,ARRAY(SELECT jsonb_array_elements_text($13::jsonb)::integer),$14)`, id, offer.VariantID, offer.RateID, offer.RateName, offer.Price, offer.Currency, offer.MealPlan, offer.Visible, offer.DataAvailable, offer.RowAvailable, offer.DataAvailableForGuests, offer.SelectDisabled, string(quantities), offer.ReportedQuantity); err != nil {
			return dbError("insert observed offer", err)
		}
	}
	return nil
}

func nullableOwner(lease *Lease) any {
	if lease == nil {
		return nil
	}
	return lease.Owner
}
func databaseNow(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return now, dbError("read lease clock", err)
	}
	return now, nil
}
func (s *Store) checkLease(ctx context.Context, tx *sql.Tx, job Job, lease *Lease, active bool) error {
	if job.LeaseToken == 0 {
		if lease != nil {
			return ErrLeaseLost
		}
		return nil
	}
	if lease == nil || lease.JobID != job.ID || lease.Owner != job.LeaseOwner || lease.Token != job.LeaseToken {
		return ErrLeaseLost
	}
	if active {
		now, err := databaseNow(ctx, tx)
		if err != nil {
			return err
		}
		if job.LeasedUntil == nil || !job.LeasedUntil.After(now) {
			return ErrLeaseLost
		}
	}
	return nil
}
