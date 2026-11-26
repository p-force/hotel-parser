//go:build integration

package schedule

import (
	"bytes"
	"context"
	"encoding/csv"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresCSVDeliveryFormatRecovery(t *testing.T) {
	for _, format := range []string{"v1", "v2-inventory", "v2", "v3", "v4"} {
		t.Run(format, func(t *testing.T) {
			store, db, schema := storagetest.Open(t)
			ctx := context.Background()
			in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
			run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: "hotel", PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			job, err := store.CreateJob(ctx, run.ID, storage.Scenario{PropertyID: "hotel", Source: "travelline", SourceOrigin: "https://example.test", SourcePropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			reason := storage.UnavailabilityReasonBookingForbidden
			if _, err := store.SaveAttempt(ctx, storage.AttemptInput{JobID: job.ID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomePartial, HTTPStatus: 200, Warnings: []storage.Warning{{Code: "category_availability_unknown", Context: "4347"}}, Observations: []storage.Observation{{CategoryID: "4347", CategoryName: "Room", StockStatus: storage.StockUnknown, ConfirmedUnavailable: true, UnavailabilityReason: &reason}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.FinishRun(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			path := "/old/group/hotel/slot.csv"
			if format == "v3" || format == "v4" {
				path = "/new/group/hotel/slot." + format + ".csv"
			}
			// Seed the actual pre-existing ledger entry; EnsureDeliveries must not rewrite it.
			if _, err := db.Exec(`INSERT INTO "`+schema+`".csv_deliveries(group_id,run_id,remote_path,state,lease_token,leased_until) VALUES('g',$1,$2,'in_progress',gen_random_uuid(),clock_timestamp()-interval '1 second')`, run.ID, path); err != nil {
				t.Fatal(err)
			}
			var legacy bytes.Buffer
			if _, err := store.WriteRunCSVFormat(ctx, "g", run.ID, format, &legacy); err != nil {
				t.Fatal(err)
			}
			records, err := csv.NewReader(bytes.NewReader(legacy.Bytes())).ReadAll()
			wantColumns := map[string]int{"v1": 35, "v2-inventory": 41, "v2": 45, "v3": 35, "v4": 36}[format]
			if err != nil || len(records) != 2 || len(records[0]) != wantColumns {
				t.Fatal(records, err)
			}
			if format == "v1" && records[0][1] != "schema" {
				t.Fatal("legacy header changed", records[0])
			}
			if format == "v4" && (records[0][35] != "unavailability_reason" || records[1][35] != "booking_forbidden") {
				t.Fatal("v4 reason missing", records)
			}
			m := &deliveryMock{files: map[string][]byte{path: append([]byte(nil), legacy.Bytes()...)}}
			uploader := deliveryUploader(t, m)
			delivered := uploader.Deliver(ctx, store, "g", "/different-root", 1)
			if len(delivered) != 1 || delivered[0].State != "delivered" || delivered[0].ErrorCode != "" || delivered[0].RemotePath != path || m.puts != 0 || len(m.files) != 1 || !bytes.Equal(m.files[path], legacy.Bytes()) {
				t.Fatal("legacy retry changed format/file", delivered, m.puts)
			}
			if replay := deliveryUploader(t, m).Deliver(ctx, store, "g", "/another-root", 1); len(replay) != 0 || m.puts != 0 {
				t.Fatal("delivered run repeated", replay, m.puts)
			}
		})
	}
}

func TestPostgresLegacyPendingCSVAndForeignFile(t *testing.T) {
	for _, format := range []string{"v2", "v3", "v4"} {
		for _, remoteExists := range []bool{false, true} {
			t.Run(format+"/"+map[bool]string{false: "missing", true: "foreign"}[remoteExists], func(t *testing.T) {
				store, db, schema := storagetest.Open(t)
				ctx := context.Background()
				in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
				run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: "h", PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 1)})
				if err != nil {
					t.Fatal(err)
				}
				job, err := store.CreateJob(ctx, run.ID, storage.Scenario{PropertyID: "h", Source: "bnovo", SourceOrigin: "https://example.test", SourcePropertyID: "public", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now()
				if _, err := store.SaveAttempt(ctx, storage.AttemptInput{JobID: job.ID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeFailed, HTTPStatus: 403, Error: &storage.AttemptError{Code: "http_error"}}); err != nil {
					t.Fatal(err)
				}
				if _, err := store.FinishRun(ctx, run.ID); err != nil {
					t.Fatal(err)
				}
				path := "/legacy/g/h/slot.csv"
				if format != "v2" {
					path = "/legacy/g/h/slot." + format + ".csv"
				}
				if _, err := db.Exec(`INSERT INTO "`+schema+`".csv_deliveries(group_id,run_id,remote_path) VALUES('g',$1,$2)`, run.ID, path); err != nil {
					t.Fatal(err)
				}
				m := &deliveryMock{files: map[string][]byte{}}
				if remoteExists {
					m.files[path] = []byte("user-owned-content")
				}
				delivered := deliveryUploader(t, m).Deliver(ctx, store, "g", "/new", 1)
				if len(delivered) != 1 {
					t.Fatal(delivered)
				}
				if remoteExists {
					if delivered[0].State != "retryable_failed" || m.puts != 0 || string(m.files[path]) != "user-owned-content" {
						t.Fatal(delivered, m.puts)
					}
				} else {
					var expected bytes.Buffer
					if _, err := store.WriteRunCSVFormat(ctx, "g", run.ID, format, &expected); err != nil {
						t.Fatal(err)
					}
					if delivered[0].State != "delivered" || m.puts != 1 || !bytes.Equal(expected.Bytes(), m.files[path]) {
						t.Fatal(delivered, m.puts)
					}
				}
			})
		}
	}
}
