//go:build integration

package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

const successHTML = `<html><head><script>
const $enabledRoomTypes = {"1":{"id":1,"name":"Room"}};
const $plansForJs = {"1":{"id":1,"name":"Rate","roomTypeIds":["1"]}};
const account_currency_iso4217 = "RUB";
</script></head><body><div class="room" data-roomtype-id="1">
<div class="plan_row" data-plan-id="1"><table><tr data-available="1">
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x1</span></span></span></td>
<td data-tariff-price="100"></td><td><select class="selectAvailable" data-room-id="1" data-real-room-id="1" data-plan-id="1" data-available="1"><option value="1">1</option></select></td>
</tr></table></div></div></body></html>`

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not finish")
		var zero T
		return zero
	}
}

func testService(t *testing.T, r Registry, server *httptest.Server, options collect.RuntimeOptions) *Service {
	t.Helper()
	t.Setenv("YANDEX_DISK_TOKEN", "") // Любая доставка в тестах — только через явно внедрённый mock.
	if os.Getenv("HOTEL_PARSER_CSV_DIR") == "" {
		t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	}
	s, err := Open(context.Background(), os.Getenv("HOTEL_PARSER_DATABASE_URL"), r, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if server != nil {
		client, err := bnovo.NewClient(server.URL, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		for i := range s.groups {
			for j := range s.groups[i].Properties {
				s.groups[i].Properties[j].Client = client
			}
		}
	}
	return s
}

func testGroups(t *testing.T, baseURL string) Registry {
	t.Helper()
	_, _, a := storagetest.Open(t)
	_, _, b := storagetest.Open(t)
	r := testRegistry(t)
	p := &r.Groups[0].Properties[0]
	p.Hotel.Source.BaseURL = baseURL
	p.Hotel.Inventory.Categories = p.Hotel.Inventory.Categories[:1]
	p.Hotel.Inventory.Categories[0].SourceRoomID = "1"
	p.Hotel.Inventory.PublicObservedMax = p.Hotel.Inventory.Categories[0].PublicObservedMax
	r.Groups[0].Schema = a
	second := r.Groups[0]
	second.ID = "second"
	second.Schema = b
	r.Groups = append(r.Groups, second)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPostgresScheduleConcurrentPlanningAndSlotHistory(t *testing.T) {
	r := testGroups(t, "https://reservationsteps.ru")
	a := testService(t, r, nil, collect.RuntimeOptions{})
	b := testService(t, r, nil, collect.RuntimeOptions{})
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b} {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			<-start
			_, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(8)})
			errs <- err
		}(s)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, hour := range []int{12, 16, 20} {
		if _, err := a.Execute(context.Background(), Request{Action: "plan", Slot: slot(hour)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range a.groups {
		runs, err := g.Store.RunProgress(context.Background())
		if err != nil || len(runs) != 4 {
			t.Fatalf("duplicate/missing slots: %+v %v", runs, err)
		}
		total := 0
		for _, p := range runs {
			total += p.Queued
			if p.Run.PropertyID != "arena33" {
				t.Fatal("property identity mixed")
			}
		}
		if total != 78 {
			t.Fatalf("daily durable jobs=%d", total)
		}
	}
	// Другой состав плана в том же слоте не дописывается незаметно.
	plans, _ := BuildPlans(r, slot(8))
	plans[0].Scenarios[0].Adults = 2
	if _, err := a.groups[0].Store.PlanRun(context.Background(), plans[0].Run, plans[0].Scenarios); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("changed plan accepted: %v", err)
	}
}

func TestPostgresScheduleFairParallelGroupsAndRunnerExclusion(t *testing.T) {
	entered, release := make(chan struct{}, 3), make(chan struct{})
	var calls, active, peak atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		entered <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, successHTML)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	r := testGroups(t, server.URL)
	competitor := r.Groups[0].Properties[0]
	competitor.Hotel.ID = "competitor"
	competitor.Hotel.Source.PropertyID = "00000000-0000-0000-0000-000000000002"
	r.Groups[0].Properties = append(r.Groups[0].Properties, competitor)
	options := collect.RuntimeOptions{Batch: collect.BatchOptions{Workers: 3}, MaxJobs: 3}
	a := testService(t, r, server, options)
	b := testService(t, r, server, options)
	plans, _ := BuildPlans(r, slot(8))
	for _, p := range plans[:2] {
		if _, err := a.groups[0].Store.PlanRun(context.Background(), p.Run, p.Scenarios); err != nil {
			t.Fatal(err)
		}
	}
	plans, _ = BuildPlans(r, slot(20))
	if _, err := a.groups[1].Store.PlanRun(context.Background(), plans[2].Run, plans[2].Scenarios); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.Execute(ctx, Request{Action: "resume"}); done <- err }()
	await(t, entered)
	await(t, entered)
	await(t, entered)
	if _, err := b.Execute(context.Background(), Request{Action: "resume"}); !errors.Is(err, storage.ErrRunnerBusy) {
		t.Fatalf("parallel runner bypassed source guard: %v", err)
	}
	if peak.Load() != 3 || calls.Load() != 3 {
		t.Fatalf("shared concurrency: peak=%d calls=%d", peak.Load(), calls.Load())
	}
	close(release)
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	first, _ := a.groups[0].Store.RunProgress(context.Background())
	second, _ := a.groups[1].Store.RunProgress(context.Background())
	if len(first) != 2 || first[0].Succeeded != 1 || first[0].Remaining != 60 || first[1].Succeeded != 1 || first[1].Remaining != 60 || second[0].Succeeded != 1 || second[0].Remaining != 0 || second[0].Run.FinishedAt == nil {
		t.Fatalf("small group starved/data mixed: %+v %+v", first, second)
	}
}

func TestPostgresScheduleHTTPRecoveryAndTickOrder(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("adults") != "1" {
			t.Error("regular collection sent adults != 1")
		}
		fmt.Fprint(w, successHTML)
	}))
	defer server.Close()
	r := testGroups(t, server.URL)
	r.Groups = r.Groups[:1]
	s := testService(t, r, server, collect.RuntimeOptions{MaxJobs: 1, Batch: collect.BatchOptions{Workers: 2}})
	old := slot(20).AddDate(0, 0, -1)
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: old}); err != nil {
		t.Fatal(err)
	}
	handler, err := s.Handler(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(req Request) Response {
		body, _ := json.Marshal(req)
		request := httptest.NewRequest("POST", "/trigger", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != 200 {
			t.Fatalf("trigger: %d %s", w.Code, w.Body.String())
		}
		var response Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := invoke(Request{Action: "tick", Slot: slot(20)})
	if len(first.Groups) != 1 || len(first.Groups[0].Runs) != 2 || first.Groups[0].Runs[0].Succeeded != 1 || first.Groups[0].Runs[1].Queued != 1 || calls.Load() != 1 {
		t.Fatalf("tick did not resume before plan: %+v calls=%d", first, calls.Load())
	}
	second := invoke(Request{Action: "resume"})
	if second.Groups[0].Runs[1].Succeeded != 1 || calls.Load() != 2 {
		t.Fatalf("next automatic trigger did not drain backlog: %+v", second)
	}
	invoke(Request{Action: "tick", Slot: slot(20)})
	if calls.Load() != 2 {
		t.Fatal("duplicate tick reran completed job")
	}
}

func TestPostgresScheduleStopRecoverAndPersistentFairness(t *testing.T) {
	entered := make(chan struct{}, 1)
	var recoverNow atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recoverNow.Load() {
			fmt.Fprint(w, successHTML)
			return
		}
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	r := testGroups(t, server.URL)
	s := testService(t, r, server, collect.RuntimeOptions{MaxJobs: 1, Batch: collect.BatchOptions{Workers: 1}})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Execute(ctx, Request{Action: "resume"}); done <- err }()
	await(t, entered)
	cancel()
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop: %v", err)
	}
	recoverNow.Store(true)
	other := testService(t, r, server, collect.RuntimeOptions{MaxJobs: 1, Batch: collect.BatchOptions{Workers: 1}})
	// Первый group всё ещё leased. Следующий короткий вызов обязан продвинуть второй.
	if _, err := other.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	b, _ := other.groups[1].Store.RunProgress(context.Background())
	if b[0].Succeeded != 1 {
		t.Fatalf("short triggers starved second group: %+v", b)
	}
	// Истечение аренды первого group в изолированной схеме имитирует crash/recovery.
	_, admin, _ := storagetest.Open(t)
	if _, err := admin.Exec(`UPDATE "` + r.Groups[0].Schema + `".scenario_jobs SET leased_until=clock_timestamp()-interval '1 second' WHERE status='running'`); err != nil {
		t.Fatal("expire isolated lease failed")
	}
	if _, err := other.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	a, _ := other.groups[0].Store.RunProgress(context.Background())
	if a[0].Succeeded != 1 || a[0].Remaining != 0 {
		t.Fatalf("lease recovery failed: %+v", a)
	}
}

type localTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (t localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	copyURL := *r.URL
	clone.URL = &copyURL
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return t.transport.RoundTrip(clone)
}

func TestPostgresScheduleBnovoRunnerHandoffRate(t *testing.T) {
	starts := make(chan time.Time, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { starts <- time.Now(); fmt.Fprint(w, successHTML) }))
	defer server.Close()
	r := testGroups(t, "https://reservationsteps.ru")
	r.Groups = r.Groups[:1]
	options := collect.RuntimeOptions{MaxJobs: 1, Batch: collect.BatchOptions{Workers: 2}}
	a := testService(t, r, nil, options)
	b := testService(t, r, nil, options)
	target, _ := url.Parse(server.URL)
	client, err := bnovo.NewClient("https://reservationsteps.ru", &http.Client{Transport: localTransport{target, server.Client().Transport}})
	if err != nil {
		t.Fatal(err)
	}
	a.groups[0].Properties[0].Client = client
	b.groups[0].Properties[0].Client = client
	if _, err := a.Execute(context.Background(), Request{Action: "plan", Slot: slot(12)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), Request{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	first, second := await(t, starts), await(t, starts)
	if second.Sub(first) < 500*time.Millisecond {
		t.Fatalf("runner handoff exceeded source start limit: gap=%v", second.Sub(first))
	}
}

func TestPostgresScheduleTickReservesPlanningBudget(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	r := testGroups(t, server.URL)
	r.Groups = r.Groups[:1]
	s := testService(t, r, server, collect.RuntimeOptions{ExecutionTimeout: 4 * time.Second, MaxJobs: 1, Batch: collect.BatchOptions{Workers: 1}})
	if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(20).AddDate(0, 0, -1)}); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(context.Background(), Request{Action: "tick", Slot: slot(20)})
	if !errors.Is(err, context.DeadlineExceeded) || len(response.Planned) != 1 || !response.ProgressKnown {
		t.Fatalf("slow backlog prevented current slot: %+v %v", response, err)
	}
	if len(response.Groups[0].Runs) != 2 || response.Groups[0].Runs[0].Remaining != 1 || response.Groups[0].Runs[1].Queued != 1 {
		t.Fatalf("unfinished slots lost: %+v", response)
	}
}
