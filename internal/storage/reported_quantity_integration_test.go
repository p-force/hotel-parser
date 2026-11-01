//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresReportedQuantityRoundTrip(t *testing.T) {
	store, admin, schema := storagetest.Open(t)
	run, job := runJob(t, store, "quantities")
	claimed, err := store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "quantity-worker", RunID: run.ID})
	if err != nil || len(claimed) != 1 {
		t.Fatal(claimed, err)
	}
	input := attempt(job.ID)
	lease := claimed[0].Lease()
	input.Lease = &lease
	input.Outcome = storage.OutcomePartial
	o := &input.Observations[0]
	o.StockStatus = storage.StockConflict
	o.Available = nil
	o.Variants = o.Variants[:1]
	o.Offers = []storage.Offer{}
	expected := map[string]*int{"a": ptr(9), "b": ptr(10), "zero": ptr(0), "missing": nil}
	for _, rate := range []string{"a", "b", "zero", "missing"} {
		o.Offers = append(o.Offers, storage.Offer{VariantID: "v2", RateID: rate, ReportedQuantity: expected[rate], Visible: true, Price: ptr("100.0"), Currency: ptr("RUB")})
	}
	id, err := store.SaveAttempt(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.SaveAttempt(context.Background(), input)
	if err != nil || again != id {
		t.Fatal("replay not idempotent", err)
	}
	loaded, err := store.GetAttempt(context.Background(), job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Observations) != 1 || loaded.Observations[0].Available != nil || loaded.Observations[0].StockStatus != storage.StockConflict || len(loaded.Observations[0].Offers) != 4 {
		t.Fatal(loaded)
	}
	for _, offer := range loaded.Observations[0].Offers {
		want, ok := expected[offer.RateID]
		if !ok || (offer.ReportedQuantity == nil) != (want == nil) || want != nil && *offer.ReportedQuantity != *want {
			t.Fatal("NULL, zero or quantity lost", offer)
		}
		if offer.DataAvailable != nil {
			t.Fatal("native quota became Bnovo HTML evidence")
		}
	}
	changed := loaded.AttemptInput
	changed.Observations[0].Offers[0].ReportedQuantity = ptr(11)
	if _, err := store.SaveAttempt(context.Background(), changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("changed quantity accepted", err)
	}
	if _, err := admin.Exec(`UPDATE "` + schema + `".observation_offers SET reported_quantity=11 WHERE rate_id='a'`); err == nil {
		t.Fatal("append-only offer mutated")
	}
	_, err = admin.Exec(`INSERT INTO "` + schema + `".observation_offers(observation_id,variant_id,rate_id,rate_name,price,currency,meal_plan,visible,data_available,row_available,data_available_for_guests,select_disabled,selectable_quantities,reported_quantity) SELECT observation_id,variant_id,'negative',rate_name,price,currency,meal_plan,visible,data_available,row_available,data_available_for_guests,select_disabled,selectable_quantities,-1 FROM "` + schema + `".observation_offers LIMIT 1`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatal("negative quota not rejected by CHECK", err)
	}
	var count int
	if err := admin.QueryRow(`SELECT count(*) FROM "` + schema + `".observation_offers`).Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
}
