package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MaxClaims и MaxAttempts фиксируются первым claim и не расширяются новым worker.
// MaxClaims ограничивает crash/re-execution; Attempts считает сохранённые HTTP-попытки.
type ClaimOptions struct {
	Owner                  string
	BatchSize              int
	LeaseDuration          time.Duration
	MaxClaims, MaxAttempts int
	PropertyID             string // Необязательный фильтр отеля для fair claim.
	RunID                  string // Необязательный фильтр одного запуска.
}

func (o ClaimOptions) normalized() (ClaimOptions, error) {
	o.Owner = strings.TrimSpace(o.Owner)
	if o.BatchSize == 0 {
		o.BatchSize = 1
	}
	if o.LeaseDuration == 0 {
		o.LeaseDuration = time.Minute
	}
	if o.MaxClaims == 0 {
		o.MaxClaims = 3
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 3
	}
	if !validID(o.Owner) || len(o.Owner) > 128 || o.BatchSize < 1 || o.BatchSize > 1000 || o.LeaseDuration < time.Microsecond || o.LeaseDuration > 24*time.Hour || o.MaxClaims < 1 || o.MaxClaims > 1000 || o.MaxAttempts < 1 || o.MaxAttempts > 1000 {
		return o, fmt.Errorf("invalid claim options")
	}
	return o, nil
}
func validLease(lease Lease) bool {
	return lease.JobID != "" && validID(lease.Owner) && len(lease.Owner) <= 128 && lease.Token > 0
}

// ClaimJobs не исполняет HTTP. Транзакция заканчивается до возврата заданий.
// PostgreSQL clock_timestamp — единственный источник времени аренды/готовности.
func (s *Store) ClaimJobs(ctx context.Context, options ClaimOptions) ([]Job, error) {
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbError("begin claim", err)
	}
	defer tx.Rollback()
	jobs, runs := s.table("scenario_jobs"), s.table("collection_runs")
	// Running без lease — зависшее legacy-задание #25; после claim старые API его не изменят.
	ready := `((j.status='queued') OR (j.status='retryable' AND j.next_retry_at<=clock_timestamp() AND (j.leased_until IS NULL OR j.leased_until<=clock_timestamp())) OR (j.status='running' AND (j.leased_until IS NULL OR j.leased_until<=clock_timestamp())))`
	maxClaims := `CASE WHEN j.lease_token=0 THEN $4 ELSE j.max_claims END`
	maxAttempts := `CASE WHEN j.lease_token=0 THEN $5 ELSE j.max_attempts END`
	filter := ` AND ($6='' OR j.run_id=NULLIF($6,'')::uuid) AND ($7='' OR j.property_id=$7)`
	args := []any{options.Owner, options.BatchSize, options.LeaseDuration.Microseconds(), options.MaxClaims, options.MaxAttempts, options.RunID, options.PropertyID}
	// Истёкший worker, исчерпавший бюджет, не оставляет вечное running/retryable.
	// Фиктивной HTTP-попытки нет: результат потерян при crash и восстановить его нельзя.
	exhausted := `WITH exhausted AS (SELECT j.id FROM ` + jobs + ` j JOIN ` + runs + ` r ON r.id=j.run_id AND r.status='running' WHERE ` + ready + filter + ` AND (j.lease_token>=(` + maxClaims + `) OR j.attempts>=(` + maxAttempts + `)) ORDER BY j.created_at,j.id FOR UPDATE OF j SKIP LOCKED LIMIT $2) UPDATE ` + jobs + ` j SET status='failed',finished_at=GREATEST(clock_timestamp(),COALESCE(j.started_at,clock_timestamp())),next_retry_at=NULL,leased_until=NULL,last_error_code=CASE WHEN j.attempts>=(` + maxAttempts + `) THEN 'attempt_limit' ELSE 'lease_exhausted' END FROM exhausted e WHERE j.id=e.id`
	// $1/$3 не используются в sweep, но типы должны быть известны протоколу PostgreSQL.
	exhausted = `WITH params AS (SELECT $1::text AS owner,$3::bigint AS duration), ` + strings.TrimPrefix(exhausted, "WITH ")
	if _, err = tx.ExecContext(ctx, exhausted, args...); err != nil {
		return nil, dbError("finalize exhausted jobs", err)
	}
	query := `WITH picked AS (SELECT j.id FROM ` + jobs + ` j JOIN ` + runs + ` r ON r.id=j.run_id AND r.status='running' WHERE ` + ready + filter + ` AND j.lease_token<(` + maxClaims + `) AND j.attempts<(` + maxAttempts + `) ORDER BY j.created_at,j.id FOR UPDATE OF j SKIP LOCKED LIMIT $2), claimed AS (UPDATE ` + jobs + ` j SET status='running',last_claimed_at=clock_timestamp(),lease_owner=$1,lease_token=j.lease_token+1,leased_until=clock_timestamp()+$3::bigint*interval '1 microsecond',max_claims=CASE WHEN j.lease_token=0 THEN $4 ELSE j.max_claims END,max_attempts=CASE WHEN j.lease_token=0 THEN $5 ELSE j.max_attempts END,next_retry_at=NULL,finished_at=NULL,started_at=COALESCE(j.started_at,clock_timestamp()) FROM picked p WHERE j.id=p.id RETURNING j.*) SELECT ` + jobColumns + ` FROM claimed ORDER BY created_at,id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbError("claim jobs", err)
	}
	claimed := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, dbError("scan claimed job", err)
		}
		claimed = append(claimed, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, dbError("read claimed jobs", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, dbError("commit claim", err)
	}
	return claimed, nil
}

// RenewLease не оживляет просроченную или завершённую аренду и не сокращает срок.
func (s *Store) RenewLease(ctx context.Context, lease Lease, duration time.Duration) (time.Time, error) {
	if !validLease(lease) || duration < time.Microsecond || duration > 24*time.Hour {
		return time.Time{}, fmt.Errorf("invalid lease renewal")
	}
	var until time.Time
	err := s.db.QueryRowContext(ctx, `UPDATE `+s.table("scenario_jobs")+` SET leased_until=GREATEST(leased_until,clock_timestamp()+$4::bigint*interval '1 microsecond') WHERE id=$1::uuid AND lease_owner=$2 AND lease_token=$3 AND status IN ('running','retryable') AND leased_until>clock_timestamp() RETURNING leased_until`, lease.JobID, lease.Owner, lease.Token, duration.Microseconds()).Scan(&until)
	if err != nil {
		if dbError("renew lease", err) == ErrNotFound {
			return time.Time{}, ErrLeaseLost
		}
		return time.Time{}, dbError("renew lease", err)
	}
	return until, nil
}

func (s *Store) CompleteJob(ctx context.Context, lease Lease, input AttemptInput) (string, error) {
	if !validLease(lease) || input.JobID != lease.JobID {
		return "", fmt.Errorf("invalid lease completion")
	}
	if input.Outcome == OutcomeFailed || input.NextRetryAt != nil {
		return "", fmt.Errorf("use FailJob for failed execution")
	}
	input.Lease = &lease
	return s.SaveAttempt(ctx, input)
}

// FailPolicy планирует только DB retry; не выполняет и не повторяет HTTP.
// Если #23 уже исчерпал оставшийся MaxAttempts, дальнейший re-execution запрещён.
type FailPolicy struct{ InitialBackoff, MaxBackoff time.Duration }

func (p FailPolicy) normalized() (FailPolicy, error) {
	if p.InitialBackoff == 0 {
		p.InitialBackoff = time.Second
	}
	if p.MaxBackoff == 0 {
		p.MaxBackoff = 10 * time.Second
	}
	if p.InitialBackoff < time.Microsecond || p.MaxBackoff < p.InitialBackoff || p.MaxBackoff > 24*time.Hour {
		return p, fmt.Errorf("invalid job backoff")
	}
	return p, nil
}
func (s *Store) FailJob(ctx context.Context, lease Lease, input AttemptInput, policy FailPolicy) (string, error) {
	if !validLease(lease) || input.JobID != lease.JobID || input.Outcome != OutcomeFailed || input.Error == nil {
		return "", fmt.Errorf("invalid lease failure")
	}
	policy, err := policy.normalized()
	if err != nil {
		return "", err
	}
	input.Lease = &lease
	return s.saveAttempt(ctx, input, &policy)
}
