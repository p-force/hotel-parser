package storage

import (
	"context"
	"database/sql"
	"encoding/json"
)

func (s *Store) GetAttempt(ctx context.Context, jobID string, number int) (Attempt, error) {
	var a Attempt
	var code sql.NullString
	var retryable bool
	var owner sql.NullString
	var token int64
	var warnings []byte
	a.JobID = jobID
	a.Number = number
	err := s.db.QueryRowContext(ctx, `SELECT id::text,started_at,finished_at,duration_ms,queue_wait_ms,collected_at,outcome,http_status,error_code,retryable,warnings,next_retry_at,lease_owner,lease_token FROM `+s.table("collection_attempts")+` WHERE job_id=$1::uuid AND attempt_no=$2`, jobID, number).Scan(&a.ID, &a.StartedAt, &a.FinishedAt, &a.DurationMS, &a.QueueWaitMS, &a.CollectedAt, &a.Outcome, &a.HTTPStatus, &code, &retryable, &warnings, &a.NextRetryAt, &owner, &token)
	if err != nil {
		return a, dbError("read attempt", err)
	}
	if token > 0 {
		a.Lease = &Lease{JobID: jobID, Owner: owner.String, Token: token}
	}
	if code.Valid {
		a.Error = &AttemptError{Code: code.String, Retryable: retryable}
	}
	if err = json.Unmarshal(warnings, &a.Warnings); err != nil {
		return a, dbError("decode attempt warnings", err)
	}
	a.Observations = make([]Observation, 0)
	rows, err := s.db.QueryContext(ctx, `SELECT id::text,category_id,category_name,area_m2,available,stock_status,has_offers,confirmed_unavailable,warnings FROM `+s.table("observations")+` WHERE attempt_id=$1::uuid ORDER BY category_id`, a.ID)
	if err != nil {
		return a, dbError("read observations", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var o Observation
		var id string
		var diagnostic []byte
		if err = rows.Scan(&id, &o.CategoryID, &o.CategoryName, &o.AreaM2, &o.Available, &o.StockStatus, &o.HasOffers, &o.ConfirmedUnavailable, &diagnostic); err != nil {
			rows.Close()
			return a, dbError("scan observation", err)
		}
		if err = json.Unmarshal(diagnostic, &o.Warnings); err != nil {
			rows.Close()
			return a, dbError("decode observation warnings", err)
		}
		ids = append(ids, id)
		a.Observations = append(a.Observations, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return a, dbError("read observations", err)
	}
	for i, id := range ids {
		o := &a.Observations[i]
		o.Variants = make([]Variant, 0)
		o.Offers = make([]Offer, 0)
		variants, err := s.db.QueryContext(ctx, `SELECT variant_id,adults FROM `+s.table("observation_variants")+` WHERE observation_id=$1::uuid ORDER BY variant_id`, id)
		if err != nil {
			return a, dbError("read variants", err)
		}
		for variants.Next() {
			var v Variant
			if err = variants.Scan(&v.ID, &v.Adults); err != nil {
				variants.Close()
				return a, dbError("scan variant", err)
			}
			o.Variants = append(o.Variants, v)
		}
		err = variants.Err()
		variants.Close()
		if err != nil {
			return a, dbError("read variants", err)
		}
		offers, err := s.db.QueryContext(ctx, `SELECT variant_id,rate_id,rate_name,price::text,currency,meal_plan,visible,data_available,row_available,data_available_for_guests,select_disabled,array_to_json(selectable_quantities) FROM `+s.table("observation_offers")+` WHERE observation_id=$1::uuid ORDER BY variant_id,rate_id`, id)
		if err != nil {
			return a, dbError("read offers", err)
		}
		for offers.Next() {
			var offer Offer
			var quantities []byte
			if err = offers.Scan(&offer.VariantID, &offer.RateID, &offer.RateName, &offer.Price, &offer.Currency, &offer.MealPlan, &offer.Visible, &offer.DataAvailable, &offer.RowAvailable, &offer.DataAvailableForGuests, &offer.SelectDisabled, &quantities); err != nil {
				offers.Close()
				return a, dbError("scan offer", err)
			}
			if err = json.Unmarshal(quantities, &offer.SelectableQuantities); err != nil {
				offers.Close()
				return a, dbError("decode quantities", err)
			}
			o.Offers = append(o.Offers, offer)
		}
		err = offers.Err()
		offers.Close()
		if err != nil {
			return a, dbError("read offers", err)
		}
	}
	return a, nil
}
