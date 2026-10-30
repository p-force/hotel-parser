//go:build integration

// Package storagetest открывает только явно заданную локальную PostgreSQL.
package storagetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/p-force/hotel-parser/internal/storage"
)

func Open(t *testing.T) (*storage.Store, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("HOTEL_PARSER_DATABASE_URL")
	if dsn == "" {
		t.Fatal("HOTEL_PARSER_DATABASE_URL is required for integration tests")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid integration database configuration")
	}
	hosts := []string{config.Host}
	for _, fallback := range config.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	for _, host := range hosts {
		ip := net.ParseIP(host)
		if host != "localhost" && !strings.HasPrefix(host, "/") && (ip == nil || !ip.IsLoopback()) {
			t.Fatal("integration tests refuse a non-local database/fallback")
		}
	}
	schema := "hp_test_" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, dsn, storage.Options{Schema: schema})
	if err != nil {
		t.Fatalf("local PostgreSQL unavailable: %v", err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		defer db.Close()
		defer store.Close()
		if !strings.HasPrefix(schema, "hp_test_") {
			t.Error("refusing unsafe schema cleanup")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Error("failed to remove isolated integration schema")
		}
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("isolated migrations: %v", err)
	}
	return store, db, schema
}
