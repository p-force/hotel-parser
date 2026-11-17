package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate изменяет только выделенную схему. Все DDL и запись checksum одной
// миграции атомарны; advisory lock сериализует миграторы одной схемы.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError("begin migration", err)
	}
	defer tx.Rollback()
	hash := sha256.Sum256([]byte("hotel_parser_migrations:" + s.schema))
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(hash[:8]))); err != nil {
		return dbError("lock migrations", err)
	}
	if _, err = tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS "`+s.schema+`"`); err != nil {
		return dbError("create schema", err)
	}
	ledger := s.table("schema_migrations")
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+ledger+`(version bigint PRIMARY KEY,checksum text NOT NULL CHECK(length(checksum)=64),applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return dbError("create migration ledger", err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		data, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read embedded migration")
		}
		version, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid embedded migration version")
		}
		checksum := sha256.Sum256(data)
		want := hex.EncodeToString(checksum[:])
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT checksum FROM "+ledger+" WHERE version=$1", version).Scan(&existing)
		if err == nil {
			if existing != want {
				return fmt.Errorf("migration %d checksum mismatch", version)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return dbError("read migration ledger", err)
		}
		query := strings.ReplaceAll(string(data), "{{schema}}", `"`+s.schema+`"`)
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return dbError("apply migration", err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+ledger+"(version,checksum) VALUES($1,$2)", version, want); err != nil {
			return dbError("record migration", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return dbError("commit migrations", err)
	}
	return nil
}
