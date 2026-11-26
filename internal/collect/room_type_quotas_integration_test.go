//go:build integration

package collect

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresTravelLineRawQuotaPipelineAndDiagnostics(t *testing.T) {
	store, _, schema := storagetest.Open(t)
	ctx := context.Background()
	info, err := os.ReadFile("../source/travelline/testdata/aerostar-info.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../source/travelline/testdata/aerostar-2026-11-17-adults-2.json")
	if err != nil {
		t.Fatal(err)
	}
	in := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	req := source.SearchRequest{PropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), Adults: 2, Language: "ru"}
	// All original tariffs are excluded; quotas must still survive the whole path.
	result, err := travelline.Parse(info, body, req, []string{"not-allowlisted"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Offers) != 0 || result.RoomTypeQuotas == nil || len(result.RoomTypeQuotas.Items) != 8 {
		t.Fatal("unexpected fixture")
	}
	beforeStocks := NormalizeCategoryStock(result)
	without := result
	without.RoomTypeQuotas = nil
	if !reflect.DeepEqual(beforeStocks, NormalizeCategoryStock(without)) {
		t.Fatal("raw quotas change normalized stock")
	}
	run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: "aerostar", PeriodStart: in, PeriodEnd: req.CheckOut})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, run.ID, storage.Scenario{PropertyID: "aerostar", Source: "travelline", SourceOrigin: "https://ru-ibe.tlintegration.ru", SourcePropertyID: "585", CheckIn: in, CheckOut: req.CheckOut, LOS: 1, Adults: 2, Rooms: 1, Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	input, err := storageAttemptContext(ctx, result, job.ID, 1, now, now, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveAttempt(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	restarted := runtimeSecondPool(t, schema)
	read, err := restarted.GetAttempt(ctx, job.ID, 1)
	if err != nil || !reflect.DeepEqual(read.RoomTypeQuotas, input.RoomTypeQuotas) {
		t.Fatal("quota restart", err)
	}
	var reportJSON bytes.Buffer
	if err := WriteRunDiagnostics(ctx, restarted, run.ID, &reportJSON); err != nil {
		t.Fatal(err)
	}
	var report RunDiagnostics
	if err := json.Unmarshal(reportJSON.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Data.Scenarios) != 1 || !reflect.DeepEqual(report.Data.Scenarios[0].Attempt.RoomTypeQuotas, input.RoomTypeQuotas) {
		t.Fatal("diagnostic quota loss")
	}
	var output bytes.Buffer
	if _, err := restarted.WriteRunCSV(ctx, "group", run.ID, &output); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&output).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 11 || len(rows[0]) != 36 || rows[0][35] != "unavailability_reason" {
		t.Fatalf("CSV changed rows=%d cols=%d", len(rows), len(rows[0]))
	}
	for _, row := range rows[1:] {
		if row[22] != "" || row[24] != "" {
			t.Fatal("raw quotas leaked into price/reported_quantity", row)
		}
	}
}
