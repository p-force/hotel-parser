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
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresBookingRulesSchemaAndConstraints(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	run, _ := runJob(t, store, "booking_rules")
	q := `"` + schema + `"`
	requested := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	received := requested.Add(time.Second)
	insertSet := `INSERT INTO ` + q + `.run_booking_rules(run_id,requested_at,received_at,start_date,end_date,requested_rate_ids) VALUES($1::uuid,$2,$3,'2026-11-25','2027-01-24',ARRAY['128299','128301']) RETURNING id::text`
	var setID string
	if err := db.QueryRow(insertSet, run.ID, requested, received).Scan(&setID); err != nil || setID == "" {
		t.Fatal("failed to create booking rule set", err)
	}
	var storedRequested, storedReceived time.Time
	var storedStart, storedEnd string
	var correctRates bool
	if err := db.QueryRow(`SELECT requested_at,received_at,start_date::text,end_date::text,requested_rate_ids=ARRAY['128299','128301']::text[] FROM `+q+`.run_booking_rules WHERE id=$1::uuid`, setID).Scan(&storedRequested, &storedReceived, &storedStart, &storedEnd, &correctRates); err != nil || !storedRequested.Equal(requested) || !storedReceived.Equal(received) || storedStart != "2026-11-25" || storedEnd != "2027-01-24" || !correctRates {
		t.Fatal("request metadata changed", err)
	}
	for _, tc := range []struct {
		name string
		run  any
		code string
	}{
		{"one set per run", run.ID, "23505"},
		{"unknown run FK", "00000000-0000-0000-0000-000000000001", "23503"},
		{"run required", nil, "23502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var id string
			err := db.QueryRow(insertSet, tc.run, requested, received).Scan(&id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatal("unexpected constraint result", err)
			}
		})
	}
	// Defaults for restrictions would incorrectly replace absent values with false/0.
	for _, column := range []struct {
		name, dataType string
	}{
		{"room_type_id", "text"}, {"forbidden", "boolean"},
		{"min_los", "integer"}, {"max_los", "integer"}, {"min_adv_booking", "integer"},
	} {
		var dataType, nullable string
		var defaultValue sql.NullString
		if err := db.QueryRow(`SELECT data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema=$1 AND table_name='booking_rules' AND column_name=$2`, schema, column.name).Scan(&dataType, &nullable, &defaultValue); err != nil || dataType != column.dataType || nullable != "YES" || defaultValue.Valid {
			t.Fatal("nullable column contract changed", column.name, dataType, nullable, defaultValue, err)
		}
	}
	var indexDefinition string
	if err := db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename='booking_rules' AND indexname='booking_rules_lookup_idx'`, schema).Scan(&indexDefinition); err != nil || !strings.Contains(indexDefinition, "(booking_rules_id, date, room_type_id, rate_id)") || strings.Contains(indexDefinition, "UNIQUE") {
		t.Fatal("lookup index missing or unexpectedly unique", indexDefinition, err)
	}

	insertRule := `INSERT INTO ` + q + `.booking_rules(booking_rules_id,date,room_type_id,rate_id,forbidden,min_los,max_los,min_adv_booking) VALUES($1::uuid,'2026-11-25',$2,'128299',$3,$4,$5,$6) RETURNING id::text`
	ids := map[string]bool{}
	for _, tc := range []struct {
		roomID        *string
		forbidden     *bool
		min, max, adv *int
	}{
		{ptr("4347"), ptr(true), ptr(5), ptr(1), ptr(2)},
		{ptr("4347"), ptr(true), ptr(5), ptr(1), ptr(2)},  // Identical duplicate.
		{ptr("4347"), ptr(false), ptr(0), ptr(0), ptr(0)}, // Contradicts the same key.
		{ptr("4347"), nil, nil, nil, nil},
		{nil, nil, nil, nil, nil},
	} {
		var id string
		if err := db.QueryRow(insertRule, setID, tc.roomID, tc.forbidden, tc.min, tc.max, tc.adv).Scan(&id); err != nil || id == "" || ids[id] {
			t.Fatal("duplicate/conflicting source rule rejected or overwritten", err)
		}
		ids[id] = true
		var roomID sql.NullString
		var forbidden sql.NullBool
		var minLOS, maxLOS, adv sql.NullInt64
		if err := db.QueryRow(`SELECT room_type_id,forbidden,min_los,max_los,min_adv_booking FROM `+q+`.booking_rules WHERE id=$1::uuid`, id).Scan(&roomID, &forbidden, &minLOS, &maxLOS, &adv); err != nil {
			t.Fatal(err)
		}
		if roomID.Valid != (tc.roomID != nil) || (tc.roomID != nil && roomID.String != *tc.roomID) || forbidden.Valid != (tc.forbidden != nil) || (tc.forbidden != nil && forbidden.Bool != *tc.forbidden) {
			t.Fatal("NULL room/forbidden changed")
		}
		for i, value := range []sql.NullInt64{minLOS, maxLOS, adv} {
			want := []*int{tc.min, tc.max, tc.adv}[i]
			if value.Valid != (want != nil) || (want != nil && value.Int64 != int64(*want)) {
				t.Fatal("NULL/zero or contradictory restriction changed", i, value)
			}
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM `+q+`.booking_rules WHERE booking_rules_id=$1::uuid AND date='2026-11-25' AND room_type_id='4347' AND rate_id='128299'`, setID).Scan(&count); err != nil || count != 4 {
		t.Fatal("same-key records lost", count, err)
	}
	for _, tc := range []struct {
		name string
		set  any
		code string
	}{
		{"set FK", "00000000-0000-0000-0000-000000000001", "23503"},
		{"set required", nil, "23502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var id string
			err := db.QueryRow(insertRule, tc.set, nil, nil, nil, nil, nil).Scan(&id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code {
				t.Fatal("rule set constraint missing", err)
			}
		})
	}
	for _, statement := range []string{
		`UPDATE ` + q + `.booking_rules SET forbidden=false`,
		`DELETE FROM ` + q + `.booking_rules`,
		`TRUNCATE ` + q + `.booking_rules`,
		`UPDATE ` + q + `.run_booking_rules SET received_at=received_at`,
		`DELETE FROM ` + q + `.run_booking_rules`,
		`TRUNCATE ` + q + `.run_booking_rules CASCADE`,
	} {
		_, err := db.Exec(statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatal("booking rule history must be append-only", err)
		}
	}
}

//go:embed migrations/000*.sql
var bookingRulesLegacyMigrations embed.FS

func TestPostgresBookingRulesMigrationFrom0009(t *testing.T) {
	_, db, parent := storagetest.Open(t)
	ctx := context.Background()
	schema := parent + "_br9"
	q := `"` + schema + `"`
	if _, err := db.Exec(`CREATE SCHEMA ` + q + `; CREATE TABLE ` + q + `.schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL CHECK(length(checksum)=64),applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + q + ` CASCADE`) })
	entries, err := bookingRulesLegacyMigrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := bookingRulesLegacyMigrations.ReadFile("migrations/" + entry.Name())
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
	_, job := runJob(t, store, "booking_upgrade")
	input := attempt(job.ID)
	input.Outcome = storage.OutcomeNoOffers
	input.Observations = []storage.Observation{{CategoryID: "4347", StockStatus: storage.StockKnown, Available: ptr(0), ConfirmedUnavailable: true, UnavailabilityReason: ptr(storage.UnavailabilityReasonBookingForbidden)}}
	if _, err := store.SaveAttempt(ctx, input); err != nil {
		t.Fatal(err)
	}
	tables := []string{"collection_runs", "scenario_jobs", "collection_attempts", "observations", "observation_variants", "observation_offers"}
	snapshot := func(table string) string {
		t.Helper()
		var value string
		if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + q + `.` + table + ` t`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := map[string]string{}
	for _, table := range tables {
		before[table] = snapshot(table)
	}
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range tables {
		if snapshot(table) != before[table] {
			t.Fatal("existing history changed", table)
		}
	}
	for _, table := range []string{"run_booking_rules", "booking_rules"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + q + `.` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("unexpected booking rules backfill", table, count, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM ` + q + `.schema_migrations`).Scan(&count); err != nil || count != 10 {
		t.Fatal("migration missing or ledger duplicated", count, err)
	}
}
