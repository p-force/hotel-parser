//go:build integration

package storage_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

const reasonValueCheck = "observations_unavailability_reason_value_check"
const reasonStateCheck = "observations_unavailability_reason_state_check"

func TestPostgresUnavailabilityReasonChecks(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	_, job := runJob(t, store, "reason_checks")
	attemptID, err := store.SaveAttempt(ctx, attempt(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	q := `"` + schema + `"`
	var dataType, nullable string
	var defaultValue sql.NullString
	if err := db.QueryRow(`SELECT data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema=$1 AND table_name='observations' AND column_name='unavailability_reason'`, schema).Scan(&dataType, &nullable, &defaultValue); err != nil {
		t.Fatal(err)
	}
	if dataType != "text" || nullable != "YES" || defaultValue.Valid {
		t.Fatalf("column contract: type=%s nullable=%s default=%v", dataType, nullable, defaultValue)
	}

	for n, tc := range []struct {
		name, constraint  string
		reason            *string
		confirmed, offers bool
	}{
		{name: "booking forbidden", reason: ptr("booking_forbidden"), confirmed: true},
		{name: "NULL confirmed unavailable", confirmed: true},
		{name: "NULL unknown"},
		{name: "NULL with offers", offers: true},
		{name: "legacy conflict without reason", confirmed: true, offers: true},
		{name: "unsupported reason", reason: ptr("unsupported"), confirmed: true, constraint: reasonValueCheck},
		{name: "empty reason", reason: ptr(""), confirmed: true, constraint: reasonValueCheck},
		{name: "reason without confirmation", reason: ptr("booking_forbidden"), constraint: reasonStateCheck},
		{name: "reason with offers", reason: ptr("booking_forbidden"), confirmed: true, offers: true, constraint: reasonStateCheck},
		{name: "reason with offers without confirmation", reason: ptr("booking_forbidden"), offers: true, constraint: reasonStateCheck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stored sql.NullString
			err := db.QueryRow(`INSERT INTO `+q+`.observations(attempt_id,category_id,category_name,stock_status,has_offers,confirmed_unavailable,warnings,unavailability_reason) VALUES($1::uuid,$2,'Category','unknown',$3,$4,'[]'::jsonb,$5) RETURNING unavailability_reason`, attemptID, fmt.Sprintf("reason_%d", n), tc.offers, tc.confirmed, tc.reason).Scan(&stored)
			if tc.constraint != "" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != tc.constraint {
					t.Fatalf("want CHECK %s, got %v", tc.constraint, err)
				}
				return
			}
			if err != nil || stored.Valid != (tc.reason != nil) || (tc.reason != nil && stored.String != *tc.reason) {
				t.Fatalf("stored=%v err=%v", stored, err)
			}
		})
	}
	// Omission also yields NULL; this is not supplied by an implicit default.
	var omitted sql.NullString
	if err := db.QueryRow(`INSERT INTO `+q+`.observations(attempt_id,category_id,category_name,stock_status,has_offers,confirmed_unavailable,warnings) VALUES($1::uuid,'omitted','Category','unknown',false,true,'[]') RETURNING unavailability_reason`, attemptID).Scan(&omitted); err != nil || omitted.Valid {
		t.Fatal("omitted reason must remain NULL", omitted, err)
	}
	// The original availability CHECK remains enforced alongside the new checks.
	if _, err := db.Exec(`INSERT INTO `+q+`.observations(attempt_id,category_id,category_name,stock_status,available,has_offers,confirmed_unavailable,warnings,unavailability_reason) VALUES($1::uuid,'bad_stock','Category','known',-1,false,true,'[]','booking_forbidden')`, attemptID); err == nil {
		t.Fatal("original stock constraint disappeared")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatal("expected original stock CHECK violation", err)
		}
	}
	for _, statement := range []string{
		`UPDATE ` + q + `.observations SET unavailability_reason=NULL WHERE category_id='reason_0'`,
		`DELETE FROM ` + q + `.observations WHERE category_id='reason_0'`,
		`TRUNCATE ` + q + `.observations CASCADE`,
	} {
		_, err := db.Exec(statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatal("append-only protection missing", err)
		}
	}
}

//go:embed migrations/000[1-8]_*.sql
var unavailabilityLegacyMigrations embed.FS

func TestPostgresUnavailabilityReasonUpgradeFrom0008(t *testing.T) {
	_, db, parent := storagetest.Open(t)
	ctx := context.Background()
	schema := parent + "_u8"
	q := `"` + schema + `"`
	if _, err := db.Exec(`CREATE SCHEMA ` + q + `; CREATE TABLE ` + q + `.schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL CHECK(length(checksum)=64),applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + q + ` CASCADE`) })
	entries, err := unavailabilityLegacyMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := unavailabilityLegacyMigrations.ReadFile("migrations/" + entry.Name())
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
	_, job := runJob(t, store, "legacy_reason")
	input := attempt(job.ID)
	input.Outcome = storage.OutcomePartial
	input.Observations = []storage.Observation{
		{CategoryID: "known_unavailable", StockStatus: storage.StockKnown, Available: ptr(0), ConfirmedUnavailable: true},
		{CategoryID: "unknown", StockStatus: storage.StockUnknown},
		{CategoryID: "offered", StockStatus: storage.StockKnown, Available: ptr(1), HasOffers: true},
		{CategoryID: "conflict", StockStatus: storage.StockConflict, HasOffers: true, ConfirmedUnavailable: true},
	}
	id, err := storage.SavePreReasonAttemptForTest(ctx, store, input)
	if err != nil {
		t.Fatal(err)
	}
	var before, attemptBefore, constraintsBefore string
	if err := db.QueryRow(`SELECT jsonb_agg(to_jsonb(o) ORDER BY category_id)::text FROM ` + q + `.observations o`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT to_jsonb(a)::text FROM `+q+`.collection_attempts a WHERE id=$1::uuid`, id).Scan(&attemptBefore); err != nil {
		t.Fatal(err)
	}
	constraintQuery := `SELECT jsonb_agg(jsonb_build_object('name',conname,'definition',pg_get_constraintdef(oid)) ORDER BY conname)::text FROM pg_constraint WHERE conrelid=$1::regclass AND conname NOT IN ($2,$3)`
	if err := db.QueryRow(constraintQuery, q+".observations", reasonValueCheck, reasonStateCheck).Scan(&constraintsBefore); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var after, attemptAfter, constraintsAfter string
	if err := db.QueryRow(`SELECT jsonb_agg(to_jsonb(o)-'unavailability_reason' ORDER BY category_id)::text FROM ` + q + `.observations o`).Scan(&after); err != nil || after != before {
		t.Fatal("historical observations changed", err)
	}
	if err := db.QueryRow(`SELECT to_jsonb(a)::text FROM `+q+`.collection_attempts a WHERE id=$1::uuid`, id).Scan(&attemptAfter); err != nil || attemptAfter != attemptBefore {
		t.Fatal("historical attempt/hash changed", err)
	}
	if err := db.QueryRow(constraintQuery, q+".observations", reasonValueCheck, reasonStateCheck).Scan(&constraintsAfter); err != nil || constraintsAfter != constraintsBefore {
		t.Fatal("existing observation constraints changed", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM ` + q + `.observations WHERE unavailability_reason IS NOT NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatal("reason backfilled", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM ` + q + `.schema_migrations`).Scan(&count); err != nil || count != 10 {
		t.Fatal("migration missing or replay duplicated ledger", count, err)
	}
	replay, err := store.SaveAttempt(ctx, input)
	if err != nil || replay != id {
		t.Fatal("historical replay broken", err)
	}
	read, err := store.GetAttempt(ctx, job.ID, 1)
	if err != nil || len(read.Observations) != 4 {
		t.Fatal("historical read broken", err)
	}
	for _, observation := range read.Observations {
		if observation.UnavailabilityReason != nil {
			t.Fatal("historical reason invented")
		}
	}
}
