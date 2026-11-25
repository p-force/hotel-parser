package storage

import (
	"context"
	"strings"
	"time"
)

// Delivery — отдельное от collection состояние; не содержит токенов или upload href.
type Delivery struct {
	GroupID       string    `json:"group_id"`
	RunID         string    `json:"run_id"`
	RemotePath    string    `json:"remote_path"`
	State         string    `json:"state"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	ErrorCode     string    `json:"error_code,omitempty"`
	LeaseToken    string    `json:"-"`
}

const deliveryColumns = `group_id,run_id::text,remote_path,state,attempts,next_attempt_at,error_code,COALESCE(lease_token::text,'')`

// DeliveredRunIDs позволяет автоматическому экспорту не восстанавливать архив,
// уже подтверждённый на Диске. Явный report.Generate по-прежнему читает любой run.
func (s *Store) DeliveredRunIDs(ctx context.Context, groupID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id::text FROM `+s.table("csv_deliveries")+` WHERE group_id=$1 AND state='delivered'`, groupID)
	if err != nil {
		return nil, dbError("read delivered runs", err)
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbError("scan delivered run", err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("iterate delivered runs", err)
	}
	return ids, nil
}

func scanDelivery(row scanner) (d Delivery, err error) {
	err = row.Scan(&d.GroupID, &d.RunID, &d.RemotePath, &d.State, &d.Attempts, &d.NextAttemptAt, &d.ErrorCode, &d.LeaseToken)
	return
}

// EnsureDeliveries закрывает crash gap после FinishRun, включая runs прошлых
// процессов. started_at регулярного run — логический слот planner, не upload time.
// Идентичность пути фиксируется один раз; смена root не дублирует файл.
func (s *Store) EnsureDeliveries(ctx context.Context, groupID, root string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("csv_deliveries")+`(group_id,run_id,remote_path)
 SELECT $1,id,$2||'/'||CASE WHEN $1 ~ '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$' THEN $1 ELSE 'id-'||encode(convert_to($1,'UTF8'),'hex') END||'/'||CASE WHEN property_id ~ '^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$' THEN property_id ELSE 'id-'||encode(convert_to(property_id,'UTF8'),'hex') END||'/'||to_char(started_at AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD_HH24-MI')||'.v3.csv'
 FROM `+s.table("collection_runs")+` WHERE status<>'running' AND finished_at IS NOT NULL
 ON CONFLICT(group_id,run_id) DO NOTHING`, groupID, strings.TrimRight(root, "/"))
	if err != nil {
		return dbError("enqueue CSV deliveries", err)
	}
	return nil
}

// ClaimDelivery — один атомарный claim без транзакции, открытой во время HTTP.
func (s *Store) ClaimDelivery(ctx context.Context, groupID string) (Delivery, error) {
	d, err := scanDelivery(s.db.QueryRowContext(ctx, `UPDATE `+s.table("csv_deliveries")+` d
 SET state='in_progress',attempts=attempts+1,lease_token=gen_random_uuid(),leased_until=now()+interval '2 minutes'
 WHERE (group_id,run_id)=(SELECT group_id,run_id FROM `+s.table("csv_deliveries")+`
 WHERE group_id=$1 AND state<>'delivered' AND next_attempt_at<=now()
 AND (leased_until IS NULL OR leased_until<=now())
 ORDER BY next_attempt_at,run_id FOR UPDATE SKIP LOCKED LIMIT 1)
 RETURNING `+deliveryColumns, groupID))
	if err != nil {
		return d, dbError("claim CSV delivery", err)
	}
	return d, nil
}
func (s *Store) CompleteDelivery(ctx context.Context, d Delivery, code string, delay time.Duration) error {
	result, err := s.db.ExecContext(ctx, `UPDATE `+s.table("csv_deliveries")+`
 SET state=CASE WHEN $4='' THEN 'delivered' ELSE 'retryable_failed' END,error_code=$4,
 next_attempt_at=now()+$5*interval '1 millisecond',delivered_at=CASE WHEN $4='' THEN now() ELSE NULL END,
 lease_token=NULL,leased_until=NULL WHERE group_id=$1 AND run_id=$2::uuid AND lease_token=$3::uuid
 AND state='in_progress' AND leased_until>now()`, d.GroupID, d.RunID, d.LeaseToken, code, delay.Milliseconds())
	if err != nil {
		return dbError("finish CSV delivery", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return dbError("finish CSV delivery", err)
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) Deliveries(ctx context.Context, groupID string, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deliveryColumns+` FROM `+s.table("csv_deliveries")+` WHERE group_id=$1 ORDER BY next_attempt_at,run_id LIMIT $2`, groupID, limit)
	if err != nil {
		return nil, dbError("read CSV deliveries", err)
	}
	defer rows.Close()
	ds := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, dbError("read CSV delivery", err)
		}
		ds = append(ds, d)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("read CSV deliveries", err)
	}
	return ds, nil
}
