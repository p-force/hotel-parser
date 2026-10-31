package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

var (
	ErrLeaseLost    = errors.New("job lease ownership lost")
	ErrAttemptLimit = errors.New("job attempt limit exhausted")
	ErrNotFound     = errors.New("storage record not found")
	ErrConflict     = errors.New("storage idempotency conflict")
	ErrTransition   = errors.New("invalid storage state transition")
	ErrIncomplete   = errors.New("collection run has unfinished jobs")
)

// Error не содержит DSN, SQL-параметров и PostgreSQL DETAIL, которые могут раскрыть данные.
type Error struct{ Operation, Code string }

func (e *Error) Error() string {
	if e.Code != "" {
		return e.Operation + " failed (SQLSTATE " + e.Code + ")"
	}
	return e.Operation + " failed"
}
func dbError(op string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", op, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", op, context.DeadlineExceeded)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return &Error{Operation: op, Code: pg.Code}
	}
	return &Error{Operation: op}
}

type Options struct{ Schema string }
type Store struct {
	db     *sql.DB
	schema string
}

var schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Open подключается к явно переданной БД. Миграции не запускаются автоматически.
func Open(ctx context.Context, dsn string, options Options) (*Store, error) {
	if options.Schema == "" {
		options.Schema = "hotel_parser"
	}
	if !schemaPattern.MatchString(options.Schema) {
		return nil, fmt.Errorf("invalid storage schema")
	}
	if dsn == "" {
		return nil, fmt.Errorf("HOTEL_PARSER_DATABASE_URL is required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid database configuration")
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, dbError("database connection", err)
	}
	return &Store{db: db, schema: options.Schema}, nil
}
func (s *Store) Close() error             { return s.db.Close() }
func (s *Store) table(name string) string { return `"` + s.schema + `"."` + name + `"` }

// Schema возвращает явно выбранную схему Store.
func (s *Store) Schema() string { return s.schema }
