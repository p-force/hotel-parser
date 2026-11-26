//go:build integration

package schedule

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func bookingRulesStart() time.Time {
	return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
}

// Отдельный счётчик сохраняет проверки количества основных запросов в прежних
// regression-тестах. Пустой успешный ответ не добавляет причин недоступности.
func respondEmptyBookingRules(w http.ResponseWriter, r *http.Request, calls *atomic.Int32) bool {
	if r.URL.Path != "/ApiWebDistribution/AvailabilityCalendar/rate_plan_booking_rules" {
		return false
	}
	calls.Add(1)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"booking_rules":[]}`)
	return true
}

type bookingRulesHTTPMock struct {
	server              *httptest.Server
	rules, availability atomic.Int32
	mu                  sync.Mutex
	hotels              map[string]int
	starts, ends        []string
	parallel            chan struct{}
	offers              bool
	status              int
}

func newBookingRulesHTTPMock(t *testing.T, offers bool, status int, parallel bool) *bookingRulesHTTPMock {
	t.Helper()
	t.Setenv("HOTEL_PARSER_BOOKING_RULES_ENABLED", "true")
	m := &bookingRulesHTTPMock{offers: offers, status: status, hotels: map[string]int{}}
	if parallel {
		m.parallel = make(chan struct{})
	}
	m.server = httptest.NewTLSServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	regressionTransport(t, m.server)
	if err := collect.ConfigureLimits(collect.Limits{PropertyConcurrency: 3, Sources: map[string]collect.SourceLimit{m.server.URL: {Concurrency: 3}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := collect.ConfigureLimits(collect.Limits{}); err != nil {
			t.Error(err)
		}
	})
	return m
}

func TestPostgresBookingRulesFeatureFlag(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		enabled     bool
	}{
		{"unset", "", false},
		{"disabled", "false", false},
		{"enabled", "true", true},
		{"invalid", "invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db, schema := storagetest.Open(t)
			mock := newBookingRulesHTTPMock(t, false, 200, false)
			t.Setenv("HOTEL_PARSER_BOOKING_RULES_ENABLED", tc.value)
			if tc.name == "unset" {
				if err := os.Unsetenv("HOTEL_PARSER_BOOKING_RULES_ENABLED"); err != nil {
					t.Fatal(err)
				}
			}
			r := bookingRulesRegistry(t, schema, mock.server.URL, 1)
			s := testService(t, r, nil, collect.RuntimeOptions{MaxJobs: 2, Batch: collect.BatchOptions{Workers: 2}})
			run := queueBookingRulesRun(t, s, 0, 2)
			response, err := s.Execute(context.Background(), Request{Action: "resume"})
			if err != nil || mock.availability.Load() != 2 || len(response.Exports) != 1 || !response.Exports[0].Ready {
				t.Fatal("flag changed main collection/export", response, err)
			}
			want := int32(0)
			if tc.enabled {
				want = 1
			}
			if mock.rules.Load() != want {
				t.Fatal("unexpected booking rules HTTP count", mock.rules.Load(), want)
			}
			assertBookingRulesAttempts(t, db, schema, s.groups[0].Store, run.ID, 2, tc.enabled, false)
			if _, err := s.groups[0].Store.GetRunBookingRules(context.Background(), run.ID); (tc.enabled && err != nil) || (!tc.enabled && !errors.Is(err, storage.ErrNotFound)) {
				t.Fatal("disabled flag changed migrations or saved rules", err)
			}
			file, err := os.Open(response.Exports[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := csv.NewReader(file).ReadAll()
			file.Close()
			if err != nil || len(rows) != 5 || len(rows[0]) != 36 || rows[0][35] != "unavailability_reason" {
				t.Fatal("flag disabled CSV v4", rows, err)
			}
		})
	}
}

func (m *bookingRulesHTTPMock) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	switch r.URL.Path {
	case "/ApiWebDistribution/BookingForm/hotel_info":
		fmt.Fprintf(w, `{"hotels":[{"code":%q,"currency":"RUB","room_types":[{"code":"4347","name":"Standard","kind":"room"},{"code":"4350","name":"Twin","kind":"room"}],"rate_plans":[{"code":"128299","name":"Public A"},{"code":"128301","name":"Public B"}]}]}`, q.Get("hotels[0].code"))
	case "/ApiWebDistribution/BookingForm/hotel_availability":
		n := m.availability.Add(1)
		// Первые три исходных запроса одновременно занимают workers.
		if m.parallel != nil && n <= 3 {
			if n == 3 {
				close(m.parallel)
			}
			select {
			case <-m.parallel:
			case <-r.Context().Done():
				return
			}
		}
		if !m.offers {
			fmt.Fprint(w, `{"room_stays":[],"room_type_quotas":[]}`)
			return
		}
		dates := strings.Split(q.Get("criterions[0].dates"), ";")
		if len(dates) != 2 {
			w.WriteHeader(400)
			return
		}
		var stays []string
		for _, id := range []string{"4347", "4350"} {
			stays = append(stays, fmt.Sprintf(`{"hotel_ref":{"code":%q},"guests":[{"count":1,"placement":{"kind":"adult"}}],"room_types":[{"code":%q,"room_type_quota_rph":%q,"placements":[{"code":"adult"}]}],"rate_plans":[{"code":"128299","promo":false}],"total":{"price_after_tax":5000,"currency":"RUB"},"stay_dates":{"start_date":%q,"end_date":%q}}`, q.Get("criterions[0].hotels[0].code"), id, id, dates[0]+" 00:00:00", dates[1]+" 00:00:00"))
		}
		fmt.Fprintf(w, `{"room_stays":[%s],"room_type_quotas":[{"rph":"4347","quantity":2},{"rph":"4350","quantity":2}]}`, strings.Join(stays, ","))
	case "/ApiWebDistribution/AvailabilityCalendar/rate_plan_booking_rules":
		m.rules.Add(1)
		m.mu.Lock()
		m.hotels[q.Get("hotel")]++
		m.starts = append(m.starts, q.Get("start_date"))
		m.ends = append(m.ends, q.Get("end_date"))
		m.mu.Unlock()
		if m.status != http.StatusOK {
			w.WriteHeader(m.status)
			fmt.Fprint(w, `{"private_secret":"must_not_log"}`)
			return
		}
		if q.Get("rate_plans[0].code") != "128299" || q.Get("rate_plans[1].code") != "128301" || q.Get("audience") != "BookingForm" {
			w.WriteHeader(400)
			return
		}
		var rules []map[string]any
		for day := 0; day < 10; day++ {
			for _, rate := range []string{"128299", "128301"} {
				rules = append(rules, map[string]any{"date": bookingRulesStart().AddDate(0, 0, day).Format("2006-01-02"), "room_types": []string{"4347", "4350"}, "code": rate, "forbidden": true})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"booking_rules": rules})
	default:
		w.WriteHeader(404)
	}
}

func bookingRulesRegistry(t *testing.T, schema, origin string, hotels int) Registry {
	t.Helper()
	r := Registry{Groups: []Group{{ID: "booking_group", Schema: schema}}}
	for i := 0; i < hotels; i++ {
		hotel, err := config.Load("../../configs/aerostar.json")
		if err != nil {
			t.Fatal(err)
		}
		hotel.ID = fmt.Sprintf("booking_hotel_%d", i)
		hotel.Source.BaseURL, hotel.Source.PropertyID = origin, fmt.Sprint(585+i)
		hotel.Inventory.Categories = hotel.Inventory.Categories[:2]
		hotel.Collection.Enabled, hotel.Collection.Nights = true, []int{1}
		hotel.Rates = []config.RateConfig{{ID: "128299", Name: "Public A", Enabled: true}, {ID: "128301", Name: "Public B", Enabled: true}, {ID: "999", Name: "Disabled", Enabled: false}}
		r.Groups[0].Properties = append(r.Groups[0].Properties, Property{Hotel: hotel})
	}
	return r
}

func queueBookingRulesRun(t *testing.T, s *Service, property int, count int) storage.Run {
	t.Helper()
	hotel := s.groups[0].Properties[property].Hotel
	var scenarios []storage.Scenario
	for i := 0; i < count; i++ {
		date := bookingRulesStart().AddDate(0, 0, i)
		scenarios = append(scenarios, storage.Scenario{PropertyID: hotel.ID, Source: "travelline", SourceOrigin: hotel.Source.BaseURL,
			SourcePropertyID: hotel.Source.PropertyID, CheckIn: date, CheckOut: date.AddDate(0, 0, 1), LOS: 1, Adults: 1, Rooms: 1, Language: "ru"})
	}
	run, err := s.groups[0].Store.PlanRun(context.Background(), storage.RunInput{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", property+1), PropertyID: hotel.ID,
		PeriodStart: bookingRulesStart(), PeriodEnd: bookingRulesStart().AddDate(0, 0, count), StartedAt: regressionSlot()}, scenarios, hotel.Inventory)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func assertBookingRulesAttempts(t *testing.T, db *sql.DB, schema string, store *storage.Store, runID string, count int, confirmed, offers bool) {
	t.Helper()
	rows, err := db.Query(`SELECT id::text FROM "`+schema+`".scenario_jobs WHERE run_id=$1::uuid ORDER BY check_in`, runID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(ids) != count {
		t.Fatal("job count changed", len(ids), count)
	}
	for _, id := range ids {
		job, err := store.GetJob(context.Background(), id)
		if err != nil || job.Attempts != 1 || job.LeaseToken < 1 {
			t.Fatal("job retried or lost fencing", job, err)
		}
		attempt, err := store.GetAttempt(context.Background(), id, 1)
		if err != nil || attempt.Error != nil || attempt.HTTPStatus != 200 || len(attempt.Observations) != 2 {
			t.Fatal("primary attempt changed", attempt, err)
		}
		wantOutcome := storage.OutcomePartial
		wantJobStatus := storage.JobPartial
		if offers {
			wantOutcome = storage.OutcomeSuccess
			wantJobStatus = storage.JobSucceeded
		}
		if attempt.Outcome != wantOutcome || job.Status != wantJobStatus || attempt.NextRetryAt != nil {
			t.Fatal("auxiliary endpoint changed outcome/retry", attempt)
		}
		for _, observation := range attempt.Observations {
			if observation.ConfirmedUnavailable != confirmed || observation.HasOffers != offers {
				t.Fatal("availability flags changed", observation)
			}
			if !offers && (observation.StockStatus != storage.StockUnknown || observation.Available != nil) {
				t.Fatal("booking rules became physical stock", observation)
			}
			if confirmed {
				if observation.UnavailabilityReason == nil || *observation.UnavailabilityReason != storage.UnavailabilityReasonBookingForbidden {
					t.Fatal("reason lost in SaveAttempt", observation)
				}
			} else if observation.UnavailabilityReason != nil {
				t.Fatal("unexpected reason", observation)
			}
		}
	}
}

func TestPostgresBookingRulesPipeline(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		jobs, workers        int
		status               int
		offers, incompatible bool
		wantRequests         int32
	}{
		{"parallel_and_waves", 6, 3, 200, false, false, 1},
		{"no_candidates", 3, 2, 200, true, false, 0},
		{"endpoint_error", 6, 2, 500, false, false, 1},
		{"incompatible_policy", 3, 2, 200, false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db, schema := storagetest.Open(t)
			mock := newBookingRulesHTTPMock(t, tc.offers, tc.status, tc.name == "parallel_and_waves")
			r := bookingRulesRegistry(t, schema, mock.server.URL, 1)
			s := testService(t, r, nil, collect.RuntimeOptions{MaxJobs: tc.jobs, Batch: collect.BatchOptions{Workers: tc.workers}})
			run := queueBookingRulesRun(t, s, 0, tc.jobs)
			if tc.incompatible {
				_, err := s.groups[0].Store.SaveRunBookingRules(context.Background(), storage.RunBookingRulesInput{RunID: run.ID,
					RequestedAt: time.Now(), ReceivedAt: time.Now(), StartDate: run.PeriodStart, EndDate: run.PeriodEnd, RequestedRateIDs: []string{"128311"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil {
				t.Fatal(err)
			}
			if mock.rules.Load() != tc.wantRequests || mock.availability.Load() != int32(tc.jobs) {
				t.Fatal("wrong HTTP counts", mock.rules.Load(), mock.availability.Load())
			}
			confirmed := tc.wantRequests == 1 && tc.status == 200
			assertBookingRulesAttempts(t, db, schema, s.groups[0].Store, run.ID, tc.jobs, confirmed, tc.offers)
			if strings.Contains(logs.String(), "must_not_log") {
				t.Fatal("unsafe source error reached logs")
			}
			if confirmed {
				if !strings.Contains(logs.String(), `"confirmed_count":2`) {
					t.Fatal("missing structured confirmation count")
				}
				saved, err := s.groups[0].Store.GetRunBookingRules(context.Background(), run.ID)
				if err != nil || !saved.StartDate.Equal(run.PeriodStart) || !saved.EndDate.Equal(run.PeriodEnd) {
					t.Fatal("rules do not cover the entire run", saved, err)
				}
				mock.mu.Lock()
				start, end := mock.starts[0], mock.ends[0]
				mock.mu.Unlock()
				if start != run.PeriodStart.Format("2006-01-02") || end != run.PeriodEnd.Format("2006-01-02") {
					t.Fatal("request used current job range", start, end)
				}
			} else if !tc.offers && !strings.Contains(logs.String(), "travelline.booking_rules.failed") {
				t.Fatal("missing safe failure diagnostic")
			}
		})
	}
}

func TestPostgresBookingRulesResumeAndIndependentHotels(t *testing.T) {
	_, db, schema := storagetest.Open(t)
	mock := newBookingRulesHTTPMock(t, false, 200, false)
	r := bookingRulesRegistry(t, schema, mock.server.URL, 2)
	options := collect.RuntimeOptions{MaxJobs: 2, Batch: collect.BatchOptions{Workers: 2}}
	s := testService(t, r, nil, options)
	runs := []storage.Run{queueBookingRulesRun(t, s, 0, 3), queueBookingRulesRun(t, s, 1, 3)}
	for i := 0; i < 3; i++ {
		if i > 0 {
			// Новый Service и новый координатор: только PostgreSQL связывает resume.
			s.Close()
			s = testService(t, r, nil, options)
		}
		if _, err := s.Execute(context.Background(), Request{Action: "resume"}); err != nil {
			t.Fatal(err)
		}
	}
	mock.mu.Lock()
	first, second := mock.hotels["585"], mock.hotels["586"]
	mock.mu.Unlock()
	if mock.rules.Load() != 2 || mock.availability.Load() != 6 || first != 1 || second != 1 {
		t.Fatal("hotels shared rules or resume repeated request", mock.rules.Load(), first, second)
	}
	for _, run := range runs {
		assertBookingRulesAttempts(t, db, schema, s.groups[0].Store, run.ID, 3, true, false)
	}
}

func TestPostgresBookingRulesTickSharesFailureAcrossRunGroups(t *testing.T) {
	_, db, schema := storagetest.Open(t)
	mock := newBookingRulesHTTPMock(t, false, 500, false)
	r := bookingRulesRegistry(t, schema, mock.server.URL, 1)
	s := testService(t, r, nil, collect.RuntimeOptions{MaxJobs: 4, Batch: collect.BatchOptions{Workers: 2}})
	run := queueBookingRulesRun(t, s, 0, 4)
	if _, err := s.Execute(context.Background(), Request{Action: "tick", Slot: regressionSlot().AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	if mock.rules.Load() != 1 || mock.availability.Load() != 4 {
		t.Fatal("tick recreated coordinator between RunGroups", mock.rules.Load(), mock.availability.Load())
	}
	assertBookingRulesAttempts(t, db, schema, s.groups[0].Store, run.ID, 4, false, false)
}
