//go:build integration

package storage_test

import (
	"bytes"
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

//go:embed migrations/0001_history.sql migrations/0002_job_leases.sql migrations/0003_collection_plans.sql migrations/0004_offer_reported_quantity.sql migrations/0005_csv_deliveries.sql
var inventoryLegacyMigrations embed.FS

func TestPostgresInventoryUpgradePreservesHistory(t *testing.T) {
	_, db, parent := storagetest.Open(t)
	ctx := context.Background()
	schema := parent + "_old"
	q := `"` + schema + `"`
	if _, err := db.Exec(`CREATE SCHEMA ` + q + `; CREATE TABLE ` + q + `.schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL CHECK(length(checksum)=64),applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + q + ` CASCADE`) }) // Only this new isolated fixture schema.
	for i, name := range []string{"0001_history.sql", "0002_job_leases.sql", "0003_collection_plans.sql", "0004_offer_reported_quantity.sql", "0005_csv_deliveries.sql"} {
		body, err := inventoryLegacyMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if _, err := db.Exec(strings.ReplaceAll(string(body), "{{schema}}", q)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO `+q+`.schema_migrations(version,checksum) VALUES($1,$2)`, i+1, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	store, err := storage.Open(ctx, os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, job := runJob(t, store, "legacy")
	input := attempt(job.ID)
	if _, err := store.SaveAttempt(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureDeliveries(ctx, "g", "/test"); err != nil {
		t.Fatal(err)
	}
	tables := []string{"collection_runs", "scenario_jobs", "collection_attempts", "observations", "observation_variants", "observation_offers", "csv_deliveries"}
	before := map[string]string{}
	for _, table := range tables {
		var data string
		if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + q + `.` + table + ` t`).Scan(&data); err != nil {
			t.Fatal(err)
		}
		before[table] = data
	}
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range tables {
		var data string
		if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + q + `.` + table + ` t`).Scan(&data); err != nil || data != before[table] {
			t.Fatal("history changed", table, err)
		}
	}
	inventory, err := store.GetRunInventory(ctx, run.ID)
	if err != nil || inventory != nil {
		t.Fatal("inventory invented for legacy run", inventory, err)
	}
	var b bytes.Buffer
	if _, err := store.WriteRunCSV(ctx, "g", run.ID, &b); err != nil {
		t.Fatal(err)
	}
	rows := readCSVRows(t, b.Bytes())
	if len(rows) == 0 {
		t.Fatal("legacy offers lost")
	}
	for _, row := range rows {
		for _, key := range inventoryCSVColumns {
			if row[key] != "" {
				t.Fatal("legacy metric inferred", row)
			}
		}
	}
	// Persisting today's config during replay cannot retroactively fill old metadata.
	_, err = store.CreateRun(ctx, run.RunInput, inventoryFixture())
	if err != nil {
		t.Fatal(err)
	}
	inventory, err = store.GetRunInventory(ctx, run.ID)
	if err != nil || inventory != nil {
		t.Fatal(inventory, err)
	}
	var versions int
	if err := db.QueryRow(`SELECT count(*) FROM ` + q + `.schema_migrations`).Scan(&versions); err != nil || versions != 8 {
		t.Fatal(versions, err)
	}
	// Original ledger checksums and provenance remain intact after the forward migration.
	for i, name := range []string{"0001_history.sql", "0002_job_leases.sql", "0003_collection_plans.sql", "0004_offer_reported_quantity.sql", "0005_csv_deliveries.sql"} {
		data, _ := inventoryLegacyMigrations.ReadFile("migrations/" + name)
		sum := sha256.Sum256(data)
		var checksum string
		if err := db.QueryRow(`SELECT checksum FROM `+q+`.schema_migrations WHERE version=$1`, i+1).Scan(&checksum); err != nil || checksum != fmt.Sprintf("%x", sum) {
			t.Fatal(checksum, err)
		}
	}
}
