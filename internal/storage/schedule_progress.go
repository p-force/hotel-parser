package storage

import (
	"context"
	"time"
)

type RunProgress struct {
	Run                                                                        Run
	Queued, Running, Retryable, Succeeded, Partial, NoOffers, Failed, Canceled int
	Remaining                                                                  int
	Ready, WaitingLease, WaitingBackoff                                        int
	ObservedAt, LastActivityAt                                                 time.Time
}

func (s *Store) RunProgress(ctx context.Context) ([]RunProgress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id::text,r.property_id,r.period_start,r.period_end,r.started_at,r.finished_at,r.status,
 count(j.id) FILTER(WHERE j.status='queued'),count(j.id) FILTER(WHERE j.status='running'),
 count(j.id) FILTER(WHERE j.status='retryable'),count(j.id) FILTER(WHERE j.status='succeeded'),
 count(j.id) FILTER(WHERE j.status='partial'),count(j.id) FILTER(WHERE j.status='no_offers'),
 count(j.id) FILTER(WHERE j.status='failed'),count(j.id) FILTER(WHERE j.status='canceled'),
 count(j.id) FILTER(WHERE j.status='queued' OR (j.status IN ('running','retryable') AND (j.leased_until IS NULL OR j.leased_until<=clock_timestamp()) AND (j.status='running' OR j.next_retry_at<=clock_timestamp()))),
 count(j.id) FILTER(WHERE j.status IN ('running','retryable') AND j.leased_until>clock_timestamp()),
 count(j.id) FILTER(WHERE j.status='retryable' AND j.next_retry_at>clock_timestamp()),
 clock_timestamp(), COALESCE((SELECT MAX(a.finished_at) FROM `+s.table("collection_attempts")+` a JOIN `+s.table("scenario_jobs")+` aj ON aj.id=a.job_id WHERE aj.run_id=r.id), MAX(j.created_at), r.started_at)
 FROM `+s.table("collection_runs")+` r LEFT JOIN `+s.table("scenario_jobs")+` j ON j.run_id=r.id GROUP BY r.id ORDER BY r.started_at,r.id`)
	if err != nil {
		return nil, dbError("read run progress", err)
	}
	defer rows.Close()
	result := make([]RunProgress, 0)
	for rows.Next() {
		var p RunProgress
		err = rows.Scan(&p.Run.ID, &p.Run.PropertyID, &p.Run.PeriodStart, &p.Run.PeriodEnd, &p.Run.StartedAt, &p.Run.FinishedAt, &p.Run.Status, &p.Queued, &p.Running, &p.Retryable, &p.Succeeded, &p.Partial, &p.NoOffers, &p.Failed, &p.Canceled, &p.Ready, &p.WaitingLease, &p.WaitingBackoff, &p.ObservedAt, &p.LastActivityAt)
		if err != nil {
			return nil, dbError("scan run progress", err)
		}
		p.Remaining = p.Queued + p.Running + p.Retryable
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("iterate run progress", err)
	}
	return result, nil
}

// ClaimHistory позволяет сохранить fairness даже между короткими trigger-вызовами.
func (s *Store) ClaimHistory(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT property_id,MAX(last_claimed_at) FROM `+s.table("scenario_jobs")+` WHERE last_claimed_at IS NOT NULL GROUP BY property_id`)
	if err != nil {
		return nil, dbError("read claim history", err)
	}
	defer rows.Close()
	history := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, dbError("scan claim history", err)
		}
		history[id] = at
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("iterate claim history", err)
	}
	return history, nil
}
