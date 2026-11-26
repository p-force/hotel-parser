//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"fmt"
)

// SavePreReasonAttemptForTest creates canonical fixtures on schemas 0005–0008.
// It is available only in test builds: production SaveAttempt always requires the
// migrated schema. No triggers are disabled and no existing history is rewritten.
func SavePreReasonAttemptForTest(ctx context.Context, s *Store, input AttemptInput) (string, error) {
	if input.Lease != nil || input.RoomTypeQuotas != nil || input.SelectionPolicy != nil || input.Error != nil || input.NextRetryAt != nil || input.Number != 1 {
		return "", fmt.Errorf("unsupported legacy fixture")
	}
	for _, o := range input.Observations {
		if o.UnavailabilityReason != nil {
			return "", fmt.Errorf("legacy fixture cannot contain a reason")
		}
	}
	input, hash, err := normalizeAttempt(input)
	if err != nil {
		return "", err
	}
	status := JobSucceeded
	switch input.Outcome {
	case OutcomePartial:
		status = JobPartial
	case OutcomeNoOffers:
		status = JobNoOffers
	case OutcomeSuccess:
	default:
		return "", fmt.Errorf("unsupported legacy fixture outcome")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	warnings, _ := json.Marshal(input.Warnings)
	var id string
	err = tx.QueryRowContext(ctx, `INSERT INTO `+s.table("collection_attempts")+`(job_id,attempt_no,payload_hash,request_hash,started_at,finished_at,duration_ms,queue_wait_ms,collected_at,outcome,http_status,retryable,warnings,lease_token) VALUES($1::uuid,$2,$3,$3,$4,$5,$6,$7,$8,$9,$10,false,$11::jsonb,0) RETURNING id::text`, input.JobID, input.Number, hash, input.StartedAt, input.FinishedAt, input.DurationMS, input.QueueWaitMS, input.CollectedAt, input.Outcome, input.HTTPStatus, string(warnings)).Scan(&id)
	if err != nil {
		return "", err
	}
	for _, o := range input.Observations {
		var observationID string
		warnings, _ := json.Marshal(o.Warnings)
		err := tx.QueryRowContext(ctx, `INSERT INTO `+s.table("observations")+`(attempt_id,category_id,category_name,area_m2,available,stock_status,has_offers,confirmed_unavailable,warnings) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9::jsonb) RETURNING id::text`, id, o.CategoryID, o.CategoryName, o.AreaM2, o.Available, o.StockStatus, o.HasOffers, o.ConfirmedUnavailable, string(warnings)).Scan(&observationID)
		if err != nil {
			return "", err
		}
		for _, v := range o.Variants {
			if _, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("observation_variants")+`(observation_id,variant_id,adults) VALUES($1::uuid,$2,$3)`, observationID, v.ID, v.Adults); err != nil {
				return "", err
			}
		}
		for _, o := range o.Offers {
			quantities, _ := json.Marshal(o.SelectableQuantities)
			if _, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("observation_offers")+`(observation_id,variant_id,rate_id,rate_name,price,currency,meal_plan,visible,data_available,row_available,data_available_for_guests,select_disabled,selectable_quantities,reported_quantity) VALUES($1::uuid,$2,$3,$4,$5::numeric,$6,$7,$8,$9,$10,$11,$12,ARRAY(SELECT jsonb_array_elements_text($13::jsonb)::integer),$14)`, observationID, o.VariantID, o.RateID, o.RateName, o.Price, o.Currency, o.MealPlan, o.Visible, o.DataAvailable, o.RowAvailable, o.DataAvailableForGuests, o.SelectDisabled, string(quantities), o.ReportedQuantity); err != nil {
				return "", err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+s.table("scenario_jobs")+` SET status=$2,attempts=1,started_at=$3,finished_at=$4 WHERE id=$1::uuid AND status='queued' AND attempts=0`, input.JobID, status, input.StartedAt, input.FinishedAt); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}
