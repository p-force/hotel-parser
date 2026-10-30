package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

var ErrRunnerBusy = errors.New("another Bnovo runner is active")

// Один ключ на БД, общий для всех group schemas и экземпляров приложения.
const runnerLockKey int64 = 726182731

type RunnerLock struct{ conn *sql.Conn }

// TryRunnerLock требует session/direct connection (не transaction-pooling DSN).
// Lock удерживается до окончания runtime, flush и всех HTTP goroutine.
func (s *Store) TryRunnerLock(ctx context.Context) (*RunnerLock, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, dbError("runner connection", err)
	}
	lock := &RunnerLock{conn: conn}
	var acquired bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, runnerLockKey).Scan(&acquired); err != nil {
		lock.Close()
		return nil, dbError("acquire runner lock", err)
	}
	if !acquired {
		lock.Close()
		return nil, ErrRunnerBusy
	}
	return lock, nil
}

// Check использует то же pinned соединение, не переподключаясь после его потери.
func (l *RunnerLock) Check(ctx context.Context) error {
	var held bool
	err := l.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid() AND classid=0 AND objid=$1 AND objsubid=1 AND granted)`, runnerLockKey).Scan(&held)
	if err != nil {
		return dbError("check runner lock", err)
	}
	if !held {
		return ErrLeaseLost
	}
	return nil
}

func (l *RunnerLock) Close() error {
	// Не возвращаем session lock в пул даже при ошибке/отмене unlock.
	_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = l.conn.Close()
	return nil
}
