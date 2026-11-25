//go:build integration

package storage_test

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

//go:embed migrations/0001_history.sql migrations/0002_job_leases.sql migrations/0003_collection_plans.sql
var legacyMigrations embed.FS

func TestPostgresReportedQuantityUpgrade(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	quoted := `"` + schema + `"`
	// Строим настоящую схему версии 3 из неизменённых SQL, без удаления строк
	// immutable ledger и без отключения append-only triggers.
	if _, err := db.Exec(`DROP SCHEMA ` + quoted + ` CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE SCHEMA ` + quoted + `; CREATE TABLE ` + quoted + `.schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL CHECK(length(checksum)=64),applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"0001_history.sql", "0002_job_leases.sql", "0003_collection_plans.sql"} {
		body, err := legacyMigrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(strings.ReplaceAll(string(body), "{{schema}}", quoted)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO `+quoted+`.schema_migrations(version,checksum) VALUES($1,$2)`, i+1, hex.EncodeToString(sum[:])); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	_, job := runJob(t, store, "legacy")
	input := attempt(job.ID)
	input.Observations[0].Variants = input.Observations[0].Variants[:1]
	input.Observations[0].Offers = input.Observations[0].Offers[:1]
	// Этот payload уже каноничен: UTC/microsecond timestamps, отсортированные
	// варианты/тарифы, пустые warnings как []. Новое nil-поле отсутствует в JSON.
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "ReportedQuantity") {
		t.Fatal("legacy payload changed")
	}
	hash := sha256.Sum256(payload)
	var attemptID, observationID string
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = tx.QueryRow(`INSERT INTO `+quoted+`.collection_attempts(job_id,attempt_no,payload_hash,request_hash,started_at,finished_at,duration_ms,queue_wait_ms,outcome,http_status,retryable,warnings) VALUES($1,1,$2,$2,$3,$4,$5,$6,'success',200,false,'[]') RETURNING id::text`, job.ID, hash[:], input.StartedAt, input.FinishedAt, input.DurationMS, input.QueueWaitMS).Scan(&attemptID)
	if err != nil {
		t.Fatal(err)
	}
	o := input.Observations[0]
	f := o.Offers[0]
	err = tx.QueryRow(`INSERT INTO `+quoted+`.observations(attempt_id,category_id,category_name,area_m2,available,stock_status,has_offers,confirmed_unavailable,warnings) VALUES($1,$2,$3,$4,$5,'known',true,false,'[]') RETURNING id::text`, attemptID, o.CategoryID, o.CategoryName, o.AreaM2, o.Available).Scan(&observationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO `+quoted+`.observation_variants(observation_id,variant_id,adults) VALUES($1,$2,$3)`, observationID, o.Variants[0].ID, o.Variants[0].Adults); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO `+quoted+`.observation_offers(observation_id,variant_id,rate_id,rate_name,price,currency,meal_plan,visible,data_available,row_available,select_disabled,selectable_quantities) VALUES($1,$2,$3,$4,$5::numeric,$6,$7,$8,$9,$10,false,ARRAY[1,10])`, observationID, f.VariantID, f.RateID, f.RateName, f.Price, f.Currency, f.MealPlan, f.Visible, f.DataAvailable, f.RowAvailable); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE `+quoted+`.scenario_jobs SET status='succeeded',attempts=1,started_at=$2,finished_at=$3 WHERE id=$1`, job.ID, input.StartedAt, input.FinishedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.GetAttempt(context.Background(), job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != attemptID || len(loaded.Observations) != 1 || len(loaded.Observations[0].Offers) != 1 {
		t.Fatal("old history lost", loaded)
	}
	old := loaded.Observations[0].Offers[0]
	if old.ReportedQuantity != nil || old.DataAvailable == nil || *old.DataAvailable != 10 || old.RowAvailable == nil || !*old.RowAvailable || old.Price == nil || *old.Price != "4910.50" || fmt.Sprint(old.SelectableQuantities) != "[1 10]" {
		t.Fatal("Bnovo evidence reinterpreted", old)
	}
	replay, err := store.SaveAttempt(context.Background(), input)
	if err != nil || replay != attemptID {
		t.Fatal("legacy idempotency broken", err)
	}
	_, freshJob := runJob(t, store, "after_upgrade")
	fresh := attempt(freshJob.ID)
	fresh.Observations[0].Offers[0].ReportedQuantity = ptr(9)
	if _, err := store.SaveAttempt(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	read, err := store.GetAttempt(context.Background(), freshJob.ID, 1)
	if err != nil || read.Observations[0].Offers[0].ReportedQuantity == nil || *read.Observations[0].Offers[0].ReportedQuantity != 9 {
		t.Fatal("new quota lost after upgrade", err)
	}
	var versions int
	if err := db.QueryRow(`SELECT count(*) FROM ` + quoted + `.schema_migrations`).Scan(&versions); err != nil || versions != 8 {
		t.Fatal(versions, err)
	}
	for table, want := range map[string]int{"collection_attempts": 2, "observations": 2, "observation_offers": 3} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + quoted + `."` + table + `"`).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, err)
		}
	}
	// Добавление колонки не отменяет защиту прежней истории.
	if _, err := db.Exec(`UPDATE `+quoted+`.observation_offers SET reported_quantity=1 WHERE observation_id=$1`, observationID); err == nil {
		t.Fatal("old offer is mutable after migration")
	}
}
