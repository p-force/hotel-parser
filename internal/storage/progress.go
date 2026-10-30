package storage

import "context"

// JobProgress — подтверждённое состояние PostgreSQL; completed не включает failed.
type JobProgress struct {
	Completed, Failed, Canceled int
	Queued, Running, Retryable  int
	Remaining                   int
}

// Progress читает один согласованный снимок; пустой runID означает все задания.
func (s *Store) Progress(ctx context.Context, runID string) (JobProgress, error) {
	var p JobProgress
	err := s.db.QueryRowContext(ctx, `SELECT
	 count(*) FILTER (WHERE status IN ('succeeded','partial','no_offers')),
	 count(*) FILTER (WHERE status='failed'), count(*) FILTER (WHERE status='canceled'),
	 count(*) FILTER (WHERE status='queued'), count(*) FILTER (WHERE status='running'),
	 count(*) FILTER (WHERE status='retryable') FROM `+s.table("scenario_jobs")+`
	 WHERE ($1='' OR run_id=NULLIF($1,'')::uuid)`, runID).Scan(&p.Completed, &p.Failed, &p.Canceled, &p.Queued, &p.Running, &p.Retryable)
	if err != nil {
		return p, dbError("read durable progress", err)
	}
	p.Remaining = p.Queued + p.Running + p.Retryable
	return p, nil
}
