//go:build integration

package schedule

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/report"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

type diskTransport func(*http.Request) (*http.Response, error)

func (f diskTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type deliveryMock struct {
	mu      sync.Mutex
	files   map[string][]byte
	puts    int
	status  int
	cut     bool
	entered chan struct{}
	release chan struct{}
}

func (m *deliveryMock) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/upload" && m.entered != nil {
		m.entered <- struct{}{}
		<-m.release
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := r.URL.Query().Get("path")
	if r.URL.Path == "/upload" {
		if r.Header.Get("Authorization") != "" {
			panic("token leak")
		}
		m.puts++
		if m.status != 0 {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(m.status)
			return
		}
		b, _ := io.ReadAll(r.Body)
		m.files[p] = b
		if m.cut {
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		w.WriteHeader(201)
		return
	}
	if r.Header.Get("Authorization") != "OAuth delivery-test" {
		panic("OAuth")
	}
	if strings.HasSuffix(r.URL.Path, "/upload") {
		json.NewEncoder(w).Encode(map[string]any{"href": "https://uploader1.dst.yandex.net/upload?path=" + url.QueryEscape(p), "method": "PUT"})
		return
	}
	if r.Method == http.MethodPut {
		w.WriteHeader(201)
		return
	}
	b, ok := m.files[p]
	if !ok {
		w.WriteHeader(404)
		return
	}
	h := md5.Sum(b)
	json.NewEncoder(w).Encode(map[string]any{"type": "file", "size": len(b), "md5": hex.EncodeToString(h[:])})
}
func deliveryUploader(t *testing.T, m *deliveryMock) *report.YandexDiskUploader {
	t.Helper()
	t.Setenv("YANDEX_DISK_TOKEN", "delivery-test")
	server := httptest.NewTLSServer(http.HandlerFunc(m.handler))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	return report.NewYandexDiskUploader(&http.Client{Transport: diskTransport(func(r *http.Request) (*http.Response, error) {
		q := r.Clone(r.Context())
		q.URL = new(url.URL)
		*q.URL = *r.URL
		q.URL.Host = endpoint.Host
		return transport.RoundTrip(q)
	})})
}
func deliveryState(t *testing.T, s *Service) []storage.Delivery {
	t.Helper()
	ds, err := s.groups[0].Store.Deliveries(context.Background(), s.groups[0].ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}
func TestPostgresAutomaticDiskDeliveryRecovery(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	t.Setenv("YANDEX_DISK_DIR", "/reports with spaces")
	var httpCalls atomic.Int32
	hotel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpCalls.Add(1); fmt.Fprint(w, successHTML) }))
	defer hotel.Close()
	registry := testGroups(t, hotel.URL)
	registry.Groups = registry.Groups[:1]
	options := collect.RuntimeOptions{MaxJobs: 4, Batch: collect.BatchOptions{Workers: 1}}
	s := testService(t, registry, hotel, options)
	m := &deliveryMock{files: map[string][]byte{}, status: 503}
	s.uploader = deliveryUploader(t, m)
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(12)}); err != nil {
		t.Fatal(err)
	}
	first, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(first.Deliveries) != 0 || len(deliveryState(t, s)) != 0 {
		t.Fatal(first, err)
	}
	second, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || second.Groups[0].Runs[0].Run.Status != storage.RunSucceeded {
		t.Fatal(second, err)
	}
	ds := deliveryState(t, s)
	if len(ds) != 1 || ds[0].State != "retryable_failed" || ds[0].ErrorCode != "disk_http_503" || ds[0].NextAttemptAt.Before(time.Now().Add(55*time.Second)) {
		t.Fatal(ds)
	}
	if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil || m.puts != 1 || httpCalls.Load() != 8 {
		t.Fatal("retry too soon", err, m.puts, httpCalls.Load())
	}
	// В новом процессе нет локального CSV; состояние и байты получаются из БД.
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	fresh := testService(t, registry, hotel, options)
	fresh.uploader = deliveryUploader(t, m)
	_, db, _ := storagetest.Open(t)
	if _, err := db.Exec(`UPDATE "` + s.groups[0].Store.Schema() + `".csv_deliveries SET next_attempt_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	m.status = 0
	m.cut = true
	replay, err := fresh.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(replay.Deliveries) != 1 || replay.Deliveries[0].State != "delivered" {
		t.Fatal(replay, err)
	}
	ds = deliveryState(t, fresh)
	if ds[0].State != "delivered" || ds[0].Attempts != 2 || m.puts != 2 || httpCalls.Load() != 8 {
		t.Fatal(ds, m.puts, httpCalls.Load())
	}
	var expected bytes.Buffer
	if _, err := fresh.groups[0].Store.WriteRunCSV(context.Background(), registry.Groups[0].ID, ds[0].RunID, &expected); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected.Bytes(), m.files[ds[0].RemotePath]) {
		t.Fatal("uploaded CSV differs")
	}
	if _, err := csv.NewReader(bytes.NewReader(m.files[ds[0].RemotePath])).ReadAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Execute(context.Background(), Request{Action: "tick", Slot: slot(12)}); err != nil || m.puts != 2 || httpCalls.Load() != 8 {
		t.Fatal("duplicate delivery", err)
	}
}
func TestPostgresDeliveryDoesNotHoldRunnerLock(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	hotel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, successHTML) }))
	defer hotel.Close()
	registry := testGroups(t, hotel.URL)
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, hotel, collect.RuntimeOptions{})
	m := &deliveryMock{files: map[string][]byte{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.uploader = deliveryUploader(t, m)
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Execute(context.Background(), Request{Action: "resume"}); done <- err }()
	await(t, m.entered)
	guard, err := s.groups[0].Store.TryRunnerLock(context.Background())
	if err != nil {
		close(m.release)
		t.Fatal("runner locked by delivery", err)
	}
	guard.Close()
	// Другой delivery worker видит активный lease и не делает второй PUT.
	other := s.uploader.Deliver(context.Background(), s.groups[0].Store, registry.Groups[0].ID, "/hotel-parser", 1)
	if len(other) != 0 {
		close(m.release)
		t.Fatal("concurrent upload", other)
	}
	close(m.release)
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	if m.puts != 1 || deliveryState(t, s)[0].State != "delivered" {
		t.Fatal(m.puts)
	}
}
func TestPostgresDeliveryMissingToken(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	hotel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, successHTML) }))
	defer hotel.Close()
	registry := testGroups(t, hotel.URL)
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, hotel, collect.RuntimeOptions{})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(r.Deliveries) != 1 || r.Deliveries[0].ErrorCode != "disk_token_missing" || r.Deliveries[0].Attempts != 0 {
		t.Fatal(r, err)
	}
	ds := deliveryState(t, s)
	if ds[0].State != "pending" {
		t.Fatal(ds)
	}
	// Токен появляется после рестарта — без новой коллекции.
	m := &deliveryMock{files: map[string][]byte{}}
	s.uploader = deliveryUploader(t, m)
	if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil || deliveryState(t, s)[0].State != "delivered" {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Exports[0].Path); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresDiskDeliveryCommittedCSVSemantics(t *testing.T) {
	t.Setenv("YANDEX_DISK_TOKEN", "")
	m := &deliveryMock{files: map[string][]byte{}}
	uploader := deliveryUploader(t, m)
	ctx := context.Background()
	for _, provider := range []string{"bnovo", "travelline"} {
		t.Run(provider, func(t *testing.T) {
			store, _, _ := storagetest.Open(t)
			in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
			run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: "hotel", PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			job, err := store.CreateJob(ctx, run.ID, storage.Scenario{PropertyID: "hotel", Source: provider, SourceOrigin: "https://example.test", SourcePropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
			if err != nil {
				t.Fatal(err)
			}
			zero := 0
			price, currency := "100.50", "RUB"
			now := time.Now()
			input := storage.AttemptInput{JobID: job.ID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomePartial, HTTPStatus: 200, Observations: []storage.Observation{
				{CategoryID: "zero", CategoryName: "Sold out", StockStatus: storage.StockKnown, Available: &zero, ConfirmedUnavailable: true},
				{CategoryID: "unknown", CategoryName: "Unknown", StockStatus: storage.StockUnknown, HasOffers: true, Variants: []storage.Variant{{ID: "v", Adults: 1}}, Offers: []storage.Offer{{VariantID: "v", RateID: "r1", Price: &price, Currency: &currency, Visible: true}, {VariantID: "v", RateID: "r2", ReportedQuantity: &zero, Visible: true}}},
				{CategoryID: "conflict", CategoryName: "Conflict", StockStatus: storage.StockConflict},
			}}
			if _, err := store.SaveAttempt(ctx, input); err != nil {
				t.Fatal(err)
			}
			if _, err := store.FinishRun(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			ds := uploader.Deliver(ctx, store, "group", "/root", 1)
			if len(ds) != 1 || ds[0].State != "delivered" {
				t.Fatal(ds)
			}
			records, err := csv.NewReader(bytes.NewReader(m.files[ds[0].RemotePath])).ReadAll()
			if err != nil || len(records) != 5 {
				t.Fatal(len(records), err)
			}
			columns := map[string]int{}
			for i, name := range records[0] {
				columns[name] = i
			}
			rows := records[1:]
			get := func(i int, k string) string { return rows[i][columns[k]] }
			if get(0, "stock_status") != "conflict" || get(0, "available") != "" || get(1, "reported_quantity") != "" || get(1, "price") != "100.50" || get(2, "reported_quantity") != "0" || get(3, "available") != "0" || get(3, "rate_id") != "" || get(0, "attempt_outcome") != "partial" {
				t.Fatal(records)
			}
			// Смена процесса после успешного PUT, но до commit delivery: recover по metadata.
			_, admin, _ := storagetest.Open(t)
			if _, err := admin.Exec(`UPDATE "` + store.Schema() + `".csv_deliveries SET state='in_progress',lease_token=gen_random_uuid(),leased_until=now()-interval '1 second',delivered_at=NULL`); err != nil {
				t.Fatal(err)
			}
			before := m.puts
			ds = deliveryUploader(t, m).Deliver(ctx, store, "group", "/root", 1)
			if len(ds) != 1 || ds[0].State != "delivered" || m.puts != before {
				t.Fatal("lost commit recovery", ds, m.puts, before)
			}
		})
	}
}

func TestPostgresDiskDeliveryFailedRunsAndBound(t *testing.T) {
	ctx := context.Background()
	store, _, _ := storagetest.Open(t)
	in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
	for range 3 {
		run, err := store.CreateRun(ctx, storage.RunInput{PropertyID: "hotel", PeriodStart: in, PeriodEnd: in.AddDate(0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		job, err := store.CreateJob(ctx, run.ID, storage.Scenario{PropertyID: "hotel", Source: "travelline", SourceOrigin: "https://example.test", SourcePropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
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
	}
	m := &deliveryMock{files: map[string][]byte{}}
	u := deliveryUploader(t, m)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if ds := u.Deliver(canceled, store, "group", "/root", 2); len(ds) != 0 || m.puts != 0 {
		t.Fatal("canceled delivery started", ds)
	}
	ds := u.Deliver(ctx, store, "group", "/root", 2)
	if len(ds) != 2 || m.puts != 2 {
		t.Fatal("invocation limit", ds, m.puts)
	}
	for _, d := range ds {
		if d.State != "delivered" {
			t.Fatal(d)
		}
		records, err := csv.NewReader(bytes.NewReader(m.files[d.RemotePath])).ReadAll()
		if err != nil || len(records) != 2 {
			t.Fatal(records, err)
		}
		row := map[string]string{}
		for i, k := range records[0] {
			row[k] = records[1][i]
		}
		if row["run_status"] != "failed" || row["attempt_outcome"] != "failed" || row["error_code"] != "http_error" || row["available"] != "" || row["price"] != "" {
			t.Fatal(row)
		}
	}
	if ds := u.Deliver(ctx, store, "group", "/root", 2); len(ds) != 1 || m.puts != 3 {
		t.Fatal(ds, m.puts)
	}
}
