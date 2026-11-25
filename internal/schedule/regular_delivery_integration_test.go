//go:build integration

package schedule

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/report"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresRegularMixedSourcesToDeliveryAcrossRestart(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	t.Setenv("YANDEX_DISK_DIR", "/hotel-parser-test")
	info, body := travelLineRegressionFixture(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/ApiWebDistribution/BookingForm/hotel_info":
			w.Write(info)
		case "/ApiWebDistribution/BookingForm/hotel_availability":
			w.Write(body)
		default:
			fmt.Fprint(w, successHTML)
		}
	}))
	defer server.Close()
	regressionTransport(t, server)
	registry := testGroups(t, server.URL)
	registry.Groups = registry.Groups[:1]
	hotel, err := config.Load("../../configs/aerostar.json")
	if err != nil {
		t.Fatal(err)
	}
	hotel.Source.BaseURL = server.URL
	hotel.Collection.Enabled = true
	registry.Groups[0].Properties = append(registry.Groups[0].Properties, Property{Hotel: hotel})
	options := collect.RuntimeOptions{MaxJobs: 1, Batch: collect.BatchOptions{Workers: 2}}
	mock := &deliveryMock{files: map[string][]byte{}, status: 503}
	fresh := func() *Service {
		s := testService(t, registry, nil, options)
		s.uploader = deliveryUploader(t, mock)
		return s
	}
	s := fresh()
	when := time.Date(2026, 11, 17, 20, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	for range 2 {
		r, err := s.Execute(context.Background(), Request{Action: "plan", Slot: when})
		if err != nil || len(r.Planned) != 2 {
			t.Fatal(r, err)
		}
	}
	first, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, r := range first.Groups[0].Runs {
		if r.Run.FinishedAt != nil {
			terminal++
		}
	}
	if terminal != 1 || len(deliveryState(t, s)) != 1 {
		t.Fatal("first resume did not finish exactly one run", first)
	}
	s.Close()
	s = fresh()
	if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || mock.puts != 2 {
		t.Fatal("unexpected collection or failed upload count", calls.Load(), mock.puts)
	}
	for _, d := range deliveryState(t, s) {
		if d.State != "retryable_failed" || d.ErrorCode != "disk_http_503" {
			t.Fatal(d)
		}
	}
	// Only the isolated test ledger is made due; production Retry-After is not shortened.
	// Use the existing store's test database through its schema, without changing other ledgers.
	_, admin, _ := storagetest.Open(t)
	if _, err := admin.Exec(`UPDATE "` + registry.Groups[0].Schema + `".csv_deliveries SET next_attempt_at=clock_timestamp() WHERE state='retryable_failed'`); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	mock.status = 0
	mock.mu.Unlock()
	s.Close()
	s = fresh()
	if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	ds := deliveryState(t, s)
	if len(ds) != 2 || mock.puts != 4 || calls.Load() != 3 {
		t.Fatal(ds, mock.puts, calls.Load())
	}
	for _, d := range ds {
		if d.State != "delivered" || d.Attempts != 2 || !strings.HasPrefix(d.RemotePath, "/hotel-parser-test/arena33/") || !strings.HasSuffix(d.RemotePath, "/2026-11-17_20-00.v3.csv") {
			t.Fatal(d)
		}
		var expected bytes.Buffer
		if _, err := s.groups[0].Store.WriteRunCSV(context.Background(), "arena33", d.RunID, &expected); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(expected.Bytes(), mock.files[d.RemotePath]) {
			t.Fatal("upload differs from committed PostgreSQL CSV")
		}
		rows, err := csv.NewReader(bytes.NewReader(expected.Bytes())).ReadAll()
		if err != nil || len(rows) < 2 || len(rows[0]) != 35 {
			t.Fatal(len(rows), err)
		}
	}
	for range 2 {
		if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 3 || mock.puts != 4 {
		t.Fatal("resume repeated source or PUT", calls.Load(), mock.puts)
	}
}

func TestPostgresDuePlanningRecoversMissedSlotsAcrossGroups(t *testing.T) {
	registry := testGroups(t, "https://reservationsteps.ru")
	s := testService(t, registry, nil, collect.RuntimeOptions{})
	now := slot(12).Add(30 * time.Minute)
	due, err := DueSlots(now)
	if err != nil || len(due) != 2 {
		t.Fatal(due, err)
	}
	for range 2 {
		for _, at := range due {
			if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: at}); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
		s = testService(t, registry, nil, collect.RuntimeOptions{})
	}
	for _, group := range s.groups {
		runs, err := group.Store.RunProgress(context.Background())
		if err != nil || len(runs) != 2 {
			t.Fatal(runs, err)
		}
		total := 0
		for _, r := range runs {
			total += r.Queued
		}
		if total != 69 {
			t.Fatal("duplicate/lost morning or midday jobs", total)
		}
	}
}

func TestPostgresCommittedJobFinalizesAfterExpiredInvocation(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	r := testGroups(t, "https://reservationsteps.ru")
	r.Groups = r.Groups[:1]
	s := testService(t, r, nil, collect.RuntimeOptions{})
	response, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.groups[0].Store.ClaimJobs(context.Background(), storage.ClaimOptions{Owner: "deadline-finalization"})
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	j := jobs[0]
	now := time.Now().UTC()
	lease := storage.Lease{JobID: j.ID, Owner: j.LeaseOwner, Token: j.LeaseToken}
	if _, err := s.groups[0].Store.CompleteJob(context.Background(), lease, storage.AttemptInput{JobID: j.ID, Number: 1, StartedAt: now, FinishedAt: now, Outcome: storage.OutcomeNoOffers, HTTPStatus: 200}); err != nil {
		t.Fatal(err)
	}
	// Reproduce the boundary after the committed SaveAttempt but before progress/CSV.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := s.Execute(expired, Request{Action: "resume"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	pending, err := s.groups[0].Store.RunProgress(context.Background())
	if err != nil || pending[0].Run.Status != storage.RunRunning || pending[0].Remaining != 0 {
		t.Fatal(pending, err)
	}
	s.Close()
	s = testService(t, r, nil, collect.RuntimeOptions{})
	mock := &deliveryMock{files: map[string][]byte{}}
	s.uploader = deliveryUploader(t, mock)
	done, err := s.Execute(context.Background(), Request{Action: "resume"})
	if err != nil || len(done.Exports) != 1 || !done.Exports[0].Ready {
		t.Fatal(done, err)
	}
	ds := deliveryState(t, s)
	if len(ds) != 1 || ds[0].RunID != response.Planned[0].ID || ds[0].State != "delivered" || mock.puts != 1 {
		t.Fatal(ds, mock.puts)
	}
	a, err := s.groups[0].Store.GetAttempt(context.Background(), j.ID, 1)
	if err != nil || a.Outcome != storage.OutcomeNoOffers {
		t.Fatal(a, err)
	}
}

// Проверяем бюджеты context без sleep и реальной сетевой задержки.
func TestPostgresDeliveryStepDoesNotTruncateUploaderBudget(t *testing.T) {
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	hotel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, successHTML) }))
	defer hotel.Close()
	registry := testGroups(t, hotel.URL)
	registry.Groups = registry.Groups[:1]
	s := testService(t, registry, hotel, collect.RuntimeOptions{})
	mock := &deliveryMock{files: map[string][]byte{}}
	server := httptest.NewTLSServer(http.HandlerFunc(mock.handler))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	base := server.Client().Transport
	budget := make(chan time.Duration, 1)
	t.Setenv("YANDEX_DISK_TOKEN", "delivery-test")
	s.uploader = report.NewYandexDiskUploader(&http.Client{Transport: diskTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut && r.URL.Hostname() == "uploader1.dst.yandex.net" {
			deadline, ok := r.Context().Deadline()
			if !ok {
				return nil, fmt.Errorf("unbounded PUT")
			}
			budget <- time.Until(deadline)
		}
		q := r.Clone(r.Context())
		q.URL = new(url.URL)
		*q.URL = *r.URL
		q.URL.Host = target.Host
		return base.RoundTrip(q)
	})})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	// Client timeout=15s must remain usable; old service step reduced it below 10s.
	if remaining := await(t, budget); remaining < 12*time.Second {
		t.Fatal("service truncated the uploader request timeout", remaining)
	}
	if mock.puts != 1 || deliveryState(t, s)[0].State != "delivered" {
		t.Fatal(mock.puts)
	}
}
