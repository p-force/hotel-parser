package collect

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

const propertySuccessHTML = `<html><head><script>
const $enabledRoomTypes = {"1":{"id":1,"name":"Room"}};
const $plansForJs = {"1":{"id":1,"name":"Rate","roomTypeIds":["1"]}};
const account_currency_iso4217 = "RUB";
</script></head><body><div class="room" data-roomtype-id="1">
<div class="plan_row" data-plan-id="1"><table><tr data-available="1">
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="100"></td><td><select class="selectAvailable" data-room-id="1" data-real-room-id="1" data-plan-id="1" data-available="1"><option value="1">1</option></select></td>
</tr></table></div></div></body></html>`

func globalLimit(t *testing.T, limit int) {
	t.Helper()
	if err := ConfigureGlobalConcurrency(limit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ConfigureGlobalConcurrency(DefaultGlobalConcurrency); err != nil {
			t.Error(err)
		}
	})
}

func propertyInputs(t *testing.T, client *bnovo.Client) []PropertyCollection {
	t.Helper()
	in := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	properties := make([]PropertyCollection, 0, 2)
	for _, id := range []string{"arena33", "lindsay"} {
		hotel, err := config.Load("../../configs/" + id + ".json")
		if err != nil {
			t.Fatal(err)
		}
		// Матрица проверяет явный batch API, независимо от регулярных LOS=1/adults=1.
		hotel.Collection.Adults = config.AdultCounts{2}
		if id == "lindsay" {
			hotel.Collection.Adults = config.AdultCounts{1, 2}
		}
		hotel.Collection.Nights = []int{1, 2, 3}
		properties = append(properties, PropertyCollection{Hotel: hotel, CheckIn: in, Client: client})
	}
	return properties
}

type propertiesOutcome struct {
	results []PropertyResult
	summary PropertiesSummary
	err     error
}

func launchProperties(ctx context.Context, properties []PropertyCollection, options BatchOptions) <-chan propertiesOutcome {
	done := make(chan propertiesOutcome, 1)
	go func() {
		results, summary, err := CollectProperties(ctx, properties, options)
		done <- propertiesOutcome{results, summary, err}
	}()
	return done
}

func assertPropertyIdentity(t *testing.T, properties []PropertyCollection, results []PropertyResult) {
	t.Helper()
	index := 0
	for _, property := range properties {
		requests, err := configuredRequests(property.Hotel, property.CheckIn)
		if err != nil {
			t.Fatal(err)
		}
		for _, req := range requests {
			if index >= len(results) {
				t.Fatal("scenario missing")
			}
			result := results[index]
			wantLOS := int(req.CheckOut.Sub(req.CheckIn) / (24 * time.Hour))
			if result.PropertyID != property.Hotel.ID || result.Source != property.Hotel.Source || result.LOS != wantLOS || !reflect.DeepEqual(result.Result.Request, req) {
				t.Errorf("index=%d identity lost: %+v", index, result)
			}
			index++
		}
	}
	if index != len(results) {
		t.Fatalf("results=%d want=%d", len(results), index)
	}
}

func TestPropertiesConcurrencyAndIdentity(t *testing.T) {
	globalLimit(t, 4)
	entered := make(chan string, 9)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var active, peak atomic.Int32
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- r.URL.Path
		select {
		case <-release:
			fmt.Fprint(w, propertySuccessHTML)
		case <-r.Context().Done():
		}
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	properties := propertyInputs(t, client)
	done := launchProperties(ctx, properties, BatchOptions{})
	seen := map[string]bool{}
	for range 4 {
		seen[receive(t, entered)] = true
	}
	if len(seen) != 2 || active.Load() != 4 {
		t.Fatalf("hotels not concurrent: seen=%v active=%d", seen, active.Load())
	}
	if err := ConfigureGlobalConcurrency(5); err == nil {
		t.Error("limit changed during a batch")
	}
	once.Do(func() { close(release) })
	outcome := receive(t, done)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	assertPropertyIdentity(t, properties, outcome.results)
	if peak.Load() != 4 || outcome.summary.ConcurrencyLimit != 4 || outcome.summary.PeakActiveScenarios != 4 || outcome.summary.SuccessCount != 9 || outcome.summary.FailedCount != 0 {
		t.Fatalf("summary=%+v actual peak=%d", outcome.summary, peak.Load())
	}
	for _, result := range outcome.results {
		if result.QueueWaitMS == nil || *result.QueueWaitMS < 0 || *result.QueueWaitMS > outcome.summary.BatchDurationMS {
			t.Errorf("invalid queue metric: %+v", result)
		}
	}
	for _, summary := range outcome.summary.Properties {
		want := 3
		if summary.PropertyID == "lindsay" {
			want = 6
		}
		if summary.Source != "bnovo" || summary.SuccessCount != want {
			t.Errorf("property counts=%+v", summary)
		}
	}
}

func TestPropertiesIndependentFailuresAndMetrics(t *testing.T) {
	logs := captureBatchLogs(t)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		isArena := strings.HasSuffix(r.URL.Path, "7f3f84ae-1f13-419f-af60-ae782d454666")
		if isArena && r.URL.Query().Get("dto") == "27-10-2026" {
			fmt.Fprint(w, "<html>error</html>")
			return
		}
		if !isArena && r.URL.Query().Get("adults") == "1" && r.URL.Query().Get("dto") == "28-10-2026" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if !isArena && r.URL.Query().Get("adults") == "1" && r.URL.Query().Get("dto") == "27-10-2026" {
			fmt.Fprint(w, strings.ReplaceAll(unavailableHTML, "28 октября", "27 октября"))
			return
		}
		if !isArena && r.URL.Query().Get("adults") == "2" && r.URL.Query().Get("dto") == "29-10-2026" {
			fmt.Fprint(w, strings.ReplaceAll(propertySuccessHTML, `data-tariff-price="100"`, ""))
			return
		}
		fmt.Fprint(w, propertySuccessHTML)
	}, 0)
	properties := propertyInputs(t, client)
	results, summary, err := CollectProperties(context.Background(), properties, BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertPropertyIdentity(t, properties, results)
	if summary.SuccessCount != 5 || summary.FailedCount != 2 || summary.PartialCount != 1 || summary.NoOffersCount != 1 {
		t.Fatalf("counts=%+v", summary)
	}
	if results[0].Result.Error == nil || results[0].Result.Error.Code != "parse_error" || results[4].Result.Error == nil || results[4].Result.Error.Code != "http_error" || !results[4].Result.Error.Retryable {
		t.Fatal("individual errors not preserved")
	}
	propertyEvents, starts := 0, 0
	batchID := ""
	for _, event := range batchEvents(t, logs) {
		switch event["msg"] {
		case "bnovo.batch.scenario.started":
			starts++
			if event["property_id"] != "arena33" && event["property_id"] != "lindsay" {
				t.Errorf("missing hotel identity: %v", event)
			}
			current := event["active_scenarios"].(float64)
			if current < 1 || current > 8 || event["queue_wait_ms"].(float64) < 0 {
				t.Errorf("invalid metrics: %v", event)
			}
			if batchID == "" {
				batchID = event["batch_id"].(string)
			}
		case "bnovo.batch.property.completed":
			propertyEvents++
			if event["batch_id"] != batchID || event["source"] != "bnovo" {
				t.Errorf("missing correlation: %v", event)
			}
			if event["property_id"] == "arena33" {
				if event["failed_count"] != float64(1) || event["success_count"] != float64(2) {
					t.Errorf("arena counts: %v", event)
				}
			} else if event["failed_count"] != float64(1) || event["success_count"] != float64(3) || event["partial_count"] != float64(1) || event["no_offers_count"] != float64(1) {
				t.Errorf("lindsay counts: %v", event)
			}
		}
	}
	if starts != 9 || propertyEvents != 2 {
		t.Errorf("start events=%d property events=%d", starts, propertyEvents)
	}
}

func TestPropertiesCancellationAndNotStarted(t *testing.T) {
	globalLimit(t, 4)
	entered, finished := make(chan struct{}, 9), make(chan struct{}, 9)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
		finished <- struct{}{}
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	properties := propertyInputs(t, client)
	done := launchProperties(ctx, properties, BatchOptions{Workers: 8})
	for range 4 {
		receive(t, entered)
	}
	cancel()
	outcome := receive(t, done)
	for range 4 {
		receive(t, finished)
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	assertPropertyIdentity(t, properties, outcome.results)
	if outcome.summary.CanceledCount != 4 || outcome.summary.NotStartedCount != 5 || outcome.summary.FailedCount != 9 {
		t.Fatalf("summary=%+v", outcome.summary)
	}
	nilWaits := 0
	for _, result := range outcome.results {
		if result.QueueWaitMS == nil {
			nilWaits++
		}
		if result.Result.Status != bnovo.SearchStatusFailed || result.Result.Error == nil || result.Result.Error.Code != "canceled" {
			t.Errorf("bad canceled result: %+v", result)
		}
		if result.QueueWaitMS == nil && (!result.Result.CollectedAt.IsZero() || result.Result.HTTPStatus != 0) {
			t.Errorf("unstarted result has HTTP metadata: %+v", result)
		}
	}
	if nilWaits != outcome.summary.NotStartedCount {
		t.Errorf("unstarted jobs with nil wait=%d, want=%d", nilWaits, outcome.summary.NotStartedCount)
	}
	globalConcurrency.Lock()
	active, batches := globalConcurrency.active, globalConcurrency.batches
	globalConcurrency.Unlock()
	if active != 0 || batches != 0 || len(entered) != 0 {
		t.Errorf("workers/permits remain: active=%d batches=%d extra=%d", active, batches, len(entered))
	}
	// Повторный пакет доказывает возврат всех допусков, без накопления фоновых workers.
	healthy := poolClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, propertySuccessHTML) }, 0)
	results, summary, err := CollectProperties(context.Background(), propertyInputs(t, healthy), BatchOptions{})
	if err != nil || len(results) != 9 || summary.SuccessCount != 9 {
		t.Fatalf("pool did not recover: %+v %v", summary, err)
	}
}

func TestGlobalLimitAcrossLegacyAndPropertyBatches(t *testing.T) {
	globalLimit(t, 2)
	entered := make(chan struct{}, 12)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls, active, peak atomic.Int32
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, propertySuccessHTML)
		case <-r.Context().Done():
		}
	}, 0)
	legacyCtx, stopLegacy := context.WithCancel(context.Background())
	defer stopLegacy()
	legacy := launchBatch(legacyCtx, client, batchRequests(3), BatchOptions{Workers: 3})
	for range 2 {
		receive(t, entered)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	properties := propertyInputs(t, client)
	second := launchProperties(ctx, properties, BatchOptions{})
	// Обе пачки завершаются через один и тот же процессный допуск.

	once.Do(func() { close(release) })
	if outcome := receive(t, legacy); outcome.err != nil {
		t.Fatal(outcome.err)
	}
	outcome := receive(t, second)
	if peak.Load() > 2 || outcome.err != nil || len(outcome.results) != 9 || outcome.summary.ConcurrencyLimit != 2 || calls.Load() != 12 {
		t.Fatalf("shared batches: %+v calls=%d", outcome.summary, calls.Load())
	}
}

func TestPropertiesValidationAndEmpty(t *testing.T) {
	for _, limit := range []int{-1, 0, MaxWorkers + 1} {
		if err := ConfigureGlobalConcurrency(limit); err == nil {
			t.Errorf("accepted global limit=%d", limit)
		}
	}
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP request") }, 0)
	properties := propertyInputs(t, client)
	for _, tc := range []struct {
		name   string
		change func([]PropertyCollection)
	}{
		{"hotel_id", func(p []PropertyCollection) { p[1].Hotel.ID = "" }},
		{"source", func(p []PropertyCollection) { p[1].Hotel.Source.Type = "unsupported" }},
		{"date", func(p []PropertyCollection) { p[1].CheckIn = time.Time{} }},
		{"adults", func(p []PropertyCollection) { p[1].Hotel.Collection.Adults = config.AdultCounts{3} }},
		{"conflicting_source", func(p []PropertyCollection) { p[1].Hotel.ID = p[0].Hotel.ID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := append([]PropertyCollection(nil), properties...)
			tc.change(input)
			if _, _, err := CollectProperties(context.Background(), input, BatchOptions{}); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	for _, options := range []BatchOptions{{Workers: -1}, {Workers: MaxWorkers + 1}, {StartInterval: -time.Second}} {
		if _, _, err := CollectProperties(context.Background(), properties, options); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	results, summary, err := CollectProperties(context.Background(), nil, BatchOptions{})
	if err != nil || len(results) != 0 || summary.PeakActiveScenarios != 0 || summary.BatchDurationMS < 0 || summary.SuccessCount != 0 {
		t.Fatalf("empty batch: %+v %v", summary, err)
	}
}

func TestPropertiesDefaultLimitAndRepeatedHotel(t *testing.T) {
	globalLimit(t, DefaultGlobalConcurrency)
	entered := make(chan struct{}, 12)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, propertySuccessHTML)
		case <-r.Context().Done():
		}
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	properties := propertyInputs(t, client)
	// Повторный отель с другой датой остаётся отдельным заданием и одной группой метрик.
	next := properties[0]
	next.CheckIn = next.CheckIn.AddDate(0, 0, 1)
	properties = append(properties, next)
	done := launchProperties(ctx, properties, BatchOptions{})
	for range DefaultGlobalConcurrency {
		receive(t, entered)
	}
	once.Do(func() { close(release) })
	outcome := receive(t, done)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	assertPropertyIdentity(t, properties, outcome.results)
	if outcome.summary.PeakActiveScenarios != 8 || outcome.summary.ConcurrencyLimit != 8 || outcome.summary.SuccessCount != 12 || len(outcome.summary.Properties) != 2 || outcome.summary.Properties[0].SuccessCount != 6 {
		t.Fatalf("defaults/repeated hotel: %+v", outcome.summary)
	}
}

func TestPropertiesQueueWaitPerJob(t *testing.T) {
	globalLimit(t, 1)
	logs := captureBatchLogs(t)
	firstEntered := make(chan struct{}, 1)
	thirdEntered := make(chan time.Time, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			firstEntered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		case 3:
			thirdEntered <- time.Now()
		}
		fmt.Fprint(w, propertySuccessHTML)
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	properties := propertyInputs(t, client)[:1]
	done := launchProperties(ctx, properties, BatchOptions{Workers: 1})
	receive(t, firstEntered)
	// Задержку создаём только для проверки метрики; порядок задают каналы.
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	releasedAt := time.Now()
	once.Do(func() { close(release) })
	thirdAt := receive(t, thirdEntered)
	outcome := receive(t, done)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	assertPropertyIdentity(t, properties, outcome.results)
	if calls.Load() != 3 || outcome.summary.SuccessCount != 3 || outcome.summary.PeakActiveScenarios != 1 {
		t.Fatalf("bad execution: %+v calls=%d", outcome.summary, calls.Load())
	}
	waits := make([]int64, len(outcome.results))
	for i, result := range outcome.results {
		if result.QueueWaitMS == nil {
			t.Fatalf("started job %d lost wait metric", i)
		}
		waits[i] = *result.QueueWaitMS
	}
	if waits[1] <= 0 {
		t.Errorf("second job did not record waiting for the only worker/slot: %v", waits)
	}
	// С единственным worker dispatch ставит третье задание после передачи второго,
	// то есть после освобождения первого. Эта верхняя граница измеряется по событиям,
	// а не по фиксированному допуску на скорость машины. Расчёт от начала batch её нарушает.
	if upper := thirdAt.Sub(releasedAt).Milliseconds(); waits[2] > upper {
		t.Errorf("third wait includes execution before it was queued: wait=%d upper=%d all=%v", waits[2], upper, waits)
	}
	seen := 0
	for _, event := range batchEvents(t, logs) {
		if event["msg"] != "bnovo.batch.scenario.started" {
			continue
		}
		index := int(event["scenario_index"].(float64))
		if int64(event["queue_wait_ms"].(float64)) != waits[index] {
			t.Errorf("log/result wait mismatch: %v", event)
		}
		seen++
	}
	if seen != 3 {
		t.Errorf("start metrics=%d want 3", seen)
	}
}
