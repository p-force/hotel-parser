//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func inventoryFixture() config.InventoryConfig {
	return config.InventoryConfig{PhysicalTotalRooms: ptr(410), PublicObservedMax: ptr(70), PhysicalSource: "fsa_registry", PublicSource: "travelline", Categories: []config.RoomCategory{
		{ID: "tl_4347", Name: "Стандартный номер", SourceRoomID: "4347", PhysicalTotalRooms: nil, PublicObservedMax: ptr(10)},
		{ID: "physical_only", Name: "Без публичной квоты", SourceRoomID: "4350", PhysicalTotalRooms: ptr(20)},
		{ID: "unknown", Name: "Без данных", SourceRoomID: "unknown"},
	}}
}

func TestPostgresInventorySnapshotsAndCSV(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	ctx := context.Background()
	inventory := inventoryFixture()
	in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
	input := storage.RunInput{ID: "50100000-0000-4000-8000-000000000001", PropertyID: "hotel", PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 2), StartedAt: in.Add(-24 * time.Hour)}
	sc := scenario("hotel")
	sc.CheckIn, sc.CheckOut = in, in.AddDate(0, 0, 1)
	run, err := store.PlanRun(ctx, input, []storage.Scenario{sc}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.GetRunInventory(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(restored, &inventory) {
		t.Fatal(restored, err)
	}
	// Today's changed config must not rewrite an already published snapshot.
	changed := inventoryFixture()
	changed.PhysicalTotalRooms = ptr(500)
	changed.PublicObservedMax = ptr(80)
	changed.PhysicalSource = "manager_confirmed"
	if _, err := store.PlanRun(ctx, input, []storage.Scenario{sc}, changed); err != nil {
		t.Fatal(err)
	}
	again, err := store.GetRunInventory(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(restored, again) {
		t.Fatal("snapshot rewritten", again, err)
	}
	job, err := store.CreateJob(ctx, run.ID, sc)
	if err != nil {
		t.Fatal(err)
	}
	observation := storage.Observation{CategoryID: "4347", CategoryName: "Стандартный номер", StockStatus: storage.StockKnown, Available: ptr(9), HasOffers: true, Variants: []storage.Variant{{ID: "v", Adults: 1}}, Offers: []storage.Offer{{VariantID: "v", RateID: "r", RateName: "Public", Visible: true, Price: ptr("12500.50"), Currency: ptr("RUB"), ReportedQuantity: ptr(9)}}}
	other := storage.Observation{CategoryID: "4350", CategoryName: "Без публичной квоты", StockStatus: storage.StockUnknown}
	unconfigured := storage.Observation{CategoryID: "new", CategoryName: "Неизвестная категория", StockStatus: storage.StockUnknown}
	if _, err := store.SaveAttempt(ctx, csvAttempt(job.ID, 1, storage.OutcomePartial, []storage.Observation{observation, other, unconfigured})); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	count, err := store.WriteRunCSV(ctx, "group", run.ID, &output)
	if err != nil || count != 3 {
		t.Fatal(count, err)
	}
	rows := readCSVRows(t, output.Bytes())
	for _, row := range rows {
		if row["property_physical_total_rooms"] != "410" || row["property_public_observed_max"] != "70" || row["property_physical_source"] != "fsa_registry" || row["property_public_source"] != "travelline" {
			t.Fatal(row)
		}
		switch row["category_id"] {
		case "4347":
			if row["category_physical_total_rooms"] != "" || row["category_public_observed_max"] != "10" || row["available"] != "9" || row["reported_quantity"] != "9" || row["stock_status"] != "known" || row["price"] != "12500.50" {
				t.Fatal(row)
			}
		case "4350":
			if row["category_physical_total_rooms"] != "20" || row["category_public_observed_max"] != "" || row["stock_status"] != "unknown" || row["available"] != "" || row["rate_id"] != "" {
				t.Fatal(row)
			}
		case "new":
			if row["category_physical_total_rooms"] != "" || row["category_public_observed_max"] != "" {
				t.Fatal(row)
			}
		default:
			t.Fatal(row)
		}
	}
	// Snapshot tables are immutable just like the collection history.
	if _, err := db.Exec(`UPDATE "`+schema+`".run_inventory SET physical_total_rooms=999 WHERE run_id=$1`, run.ID); err == nil {
		t.Fatal("mutable snapshot")
	}
	if _, err := db.Exec(`DELETE FROM "`+schema+`".run_category_inventory WHERE run_id=$1`, run.ID); err == nil {
		t.Fatal("mutable category snapshot")
	}
}

func TestPostgresInventoryNullsAndFailedCSV(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	ctx := context.Background()
	inventory := inventoryFixture()
	inventory.PhysicalTotalRooms = nil
	inventory.PublicObservedMax = nil
	inventory.PhysicalSource = ""
	inventory.PublicSource = ""
	for i := range inventory.Categories {
		inventory.Categories[i].PhysicalTotalRooms = nil
		inventory.Categories[i].PublicObservedMax = nil
	}
	sc := scenario("hotel")
	run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut}, inventory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.GetRunInventory(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, &inventory) {
		t.Fatal(snapshot, err)
	}
	job, err := store.CreateJob(ctx, run.ID, sc)
	if err != nil {
		t.Fatal(err)
	}
	failed := csvAttempt(job.ID, 1, storage.OutcomeFailed, nil)
	failed.Error = &storage.AttemptError{Code: "http_error"}
	failed.HTTPStatus = 403
	if _, err := store.SaveAttempt(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if _, err := store.WriteRunCSV(ctx, "g", run.ID, &b); err != nil {
		t.Fatal(err)
	}
	rows := readCSVRows(t, b.Bytes())
	if len(rows) != 1 || rows[0]["category_id"] != "" || rows[0]["job_status"] != "failed" {
		t.Fatal(rows)
	}
	for _, key := range inventoryCSVColumns {
		if rows[0][key] != "" {
			t.Fatal(key, rows[0])
		}
	}
	// The same failed row can retain known property metrics without inventing a category.
	known, err := store.CreateRun(ctx, storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut}, inventoryFixture())
	if err != nil {
		t.Fatal(err)
	}
	kj, err := store.CreateJob(ctx, known.ID, sc)
	if err != nil {
		t.Fatal(err)
	}
	failed.JobID = kj.ID
	if _, err := store.SaveAttempt(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, known.ID); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if _, err := store.WriteRunCSV(ctx, "g", known.ID, &b); err != nil {
		t.Fatal(err)
	}
	row := readCSVRows(t, b.Bytes())[0]
	if row["property_physical_total_rooms"] != "410" || row["category_physical_total_rooms"] != "" || row["category_public_observed_max"] != "" {
		t.Fatal(row)
	}
}

var inventoryCSVColumns = []string{"property_physical_total_rooms", "property_public_observed_max", "property_physical_source", "property_public_source", "category_physical_total_rooms", "category_public_observed_max"}

func TestPostgresInvalidInventoryRollsBackRun(t *testing.T) {
	store, db, schema := storagetest.Open(t)
	sc := scenario("hotel")
	inventory := inventoryFixture()
	inventory.PublicObservedMax = ptr(411)
	if _, err := store.CreateRun(context.Background(), storage.RunInput{PropertyID: sc.PropertyID, PeriodStart: sc.CheckIn, PeriodEnd: sc.CheckOut}, inventory); err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatal(err)
	}
	for _, table := range []string{"collection_runs", "run_inventory", "run_category_inventory"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}
