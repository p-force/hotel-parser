//go:build integration

package storage_test

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

//go:embed migrations/*.sql
var quotaMigrationFiles embed.FS

func TestPostgresQuotasUpgradeFrom0007WithoutBackfill(t *testing.T) {
	_, db, parent := storagetest.Open(t)
	ctx := context.Background()
	schema := parent + "_q7"
	q := `"` + schema + `"`
	if _, err := db.Exec(`CREATE SCHEMA ` + q + `; CREATE TABLE ` + q + `.schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + q + ` CASCADE`) })
	entries, err := quotaMigrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0008" {
			continue
		}
		body, err := quotaMigrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(strings.ReplaceAll(string(body), "{{schema}}", q)); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(body)
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO `+q+`.schema_migrations(version,checksum) VALUES($1,$2)`, version, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
	}
	store, err := storage.Open(ctx, os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, job := runJob(t, store, "legacy")
	input := attempt(job.ID)
	id, err := store.SaveAttempt(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT to_jsonb(a)::text FROM `+q+`.collection_attempts a WHERE id=$1::uuid`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	read, err := store.GetAttempt(ctx, job.ID, 1)
	if err != nil || read.RoomTypeQuotas != nil {
		t.Fatal("historical quotas invented", err)
	}
	replay, err := store.SaveAttempt(ctx, input)
	if err != nil || replay != id {
		t.Fatal("legacy hash/replay broken", err)
	}
	var after string
	if err := db.QueryRow(`SELECT to_jsonb(a)::text FROM `+q+`.collection_attempts a WHERE id=$1::uuid`, id).Scan(&after); err != nil || after != before {
		t.Fatal("attempt changed", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM ` + q + `.attempt_room_type_quota_sets`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unexpected backfill", count, err)
	}
}
