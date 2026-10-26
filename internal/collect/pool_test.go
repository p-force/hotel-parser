package collect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

const unavailableHTML = `<html><head><script>
const $enabledRoomTypes = {"1":{"id":1,"name":"Room"}};
const $plansForJs = {"1":{"id":1,"name":"Rate","roomTypeIds":["1"]}};
const account_currency_iso4217 = "RUB";
</script></head><body><div class="room" data-roomtype-id="1">
<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>
</div></body></html>`

func batchRequests(n int) []bnovo.SearchRequest {
	requests := make([]bnovo.SearchRequest, n)
	for i := range requests {
		requests[i] = bnovo.SearchRequest{
			PropertyID: fmt.Sprintf("00000000-0000-0000-0000-%012x", i+1),
			CheckIn:    time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
			CheckOut:   time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Adults: 2, Language: "ru",
		}
	}
	return requests
}

func scenarioIndex(t *testing.T, r *http.Request) int {
	t.Helper()
	parts := strings.Split(r.URL.Path, "-")
	value, err := strconv.ParseInt(parts[len(parts)-1], 16, 64)
	if err != nil {
		t.Errorf("неверный UID: %s", r.URL.Path)
		return 0
	}
	return int(value) - 1
}

func poolClient(t *testing.T, handler http.HandlerFunc, timeout time.Duration) *bnovo.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	httpClient := server.Client()
	httpClient.Timeout = timeout
	client, err := bnovo.NewClient(server.URL, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type batchOutcome struct {
	results []bnovo.SearchResult
	summary BatchSummary
	err     error
}

func launchBatch(ctx context.Context, client *bnovo.Client, requests []bnovo.SearchRequest, options BatchOptions) <-chan batchOutcome {
	done := make(chan batchOutcome, 1)
	go func() {
		results, summary, err := CollectBatch(ctx, client, requests, options)
		done <- batchOutcome{results, summary, err}
	}()
	return done
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("обработка не завершилась по сигналу")
		var zero T
		return zero
	}
}

func TestBatchWorkersAndOrder(t *testing.T) {
	for _, workers := range []int{1, 3, 0} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			const n = 6
			limit := workers
			if limit == 0 {
				limit = DefaultWorkers
			}
			entered := make(chan int, n)
			release := make([]chan struct{}, n)
			for i := range release {
				release[i] = make(chan struct{})
			}
			var active, peak atomic.Int32
			client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
				current := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); current > old; old = peak.Load() {
					if peak.CompareAndSwap(old, current) {
						break
					}
				}
				index := scenarioIndex(t, r)
				entered <- index
				select {
				case <-release[index]:
				case <-r.Context().Done():
					return
				}
				fmt.Fprint(w, unavailableHTML)
			}, 0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			requests := batchRequests(n)
			done := launchBatch(ctx, client, requests, BatchOptions{Workers: workers})
			running := make([]int, 0, limit)
			for range limit {
				running = append(running, receive(t, entered))
			}
			// Все workers одновременно дошли до HTTP до освобождения хотя бы одного запроса.
			if int(active.Load()) != limit {
				t.Errorf("параллельных запросов = %d, ожидалось %d", active.Load(), limit)
			}
			for remaining := n - limit; remaining > 0; remaining-- {
				// Освобождаем последний запрос первым: завершение не обязано совпадать с порядком входа.
				index := running[len(running)-1]
				running = running[:len(running)-1]
				close(release[index])
				next := receive(t, entered)
				if workers == 1 && next != n-remaining {
					t.Errorf("нарушен последовательный порядок: %d", next)
				}
				running = append(running, next)
			}
			for _, index := range running {
				close(release[index])
			}
			outcome := receive(t, done)
			if outcome.err != nil {
				t.Fatal(outcome.err)
			}
			if peak.Load() > int32(limit) {
				t.Errorf("превышен лимит: peak=%d, limit=%d", peak.Load(), limit)
			}
			if len(outcome.results) != n || outcome.summary.NoOffersCount != n {
				t.Fatalf("неверные результаты: %+v", outcome)
			}
			for i, result := range outcome.results {
				if !reflect.DeepEqual(result.Request, requests[i]) || result.Status != bnovo.SearchStatusNoOffers {
					t.Errorf("результат %d не соответствует входу: %+v", i, result)
				}
			}
		})
	}
}

func captureBatchLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func batchEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func TestBatchOutcomesAndMetrics(t *testing.T) {
	buf := captureBatchLogs(t)
	const row = `<div class="plan_row" data-plan-id="1"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="4910.50"></td><td><select class="selectAvailable" data-room-id="1" data-real-room-id="1" data-plan-id="1"><option value="1">1</option></select></td>
</tr></table></div>`
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch scenarioIndex(t, r) {
		case 0:
			fmt.Fprint(w, strings.Replace(unavailableHTML, `<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>`, row, 1))
		case 1:
			fmt.Fprint(w, strings.Replace(unavailableHTML, "tariff__nodates", "plan_row", 1))
		case 2:
			fmt.Fprint(w, unavailableHTML)
		case 3:
			w.WriteHeader(403)
		}
	}, 0)
	requests := batchRequests(4)
	results, summary, err := CollectBatch(context.Background(), client, requests, BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bnovo.SearchStatus{bnovo.SearchStatusSuccess, bnovo.SearchStatusPartial, bnovo.SearchStatusNoOffers, bnovo.SearchStatusFailed} {
		if results[i].Status != want || !reflect.DeepEqual(results[i].Request, requests[i]) {
			t.Errorf("результат %d: %+v", i, results[i])
		}
	}
	if results[0].Offers[0].Price != "4910.50" || results[3].Error.Code != "http_error" {
		t.Error("изменены данные сценариев")
	}
	if summary.SuccessCount != 1 || summary.PartialCount != 1 || summary.NoOffersCount != 1 || summary.FailedCount != 1 || summary.CanceledCount != 0 || summary.NotStartedCount != 0 {
		t.Errorf("неверная сводка: %+v", summary)
	}
	seen := make(map[int]bool)
	attempts := make(map[string]bool)
	summaries := 0
	for _, event := range batchEvents(t, buf) {
		switch event["msg"] {
		case "bnovo.batch.scenario.started":
			index := int(event["scenario_index"].(float64))
			wait, ok := event["queue_wait_ms"].(float64)
			if !ok || wait < 0 || wait > float64(summary.BatchDurationMS) || seen[index] {
				t.Errorf("неверная очередь: %v", event)
			}
			if event["batch_id"] == "" {
				t.Error("нет batch_id")
			}
			seen[index] = true
		case "bnovo.collect.completed":
			id := event["attempt_id"].(string)
			if id == "" || attempts[id] {
				t.Error("attempt_id отсутствует или повторяется")
			}
			attempts[id] = true
			for _, key := range []string{"collection_duration_ms", "http_duration_ms"} {
				if value, ok := event[key].(float64); !ok || value < 0 {
					t.Errorf("нет метрики #13 %s: %v", key, event)
				}
			}
			if event["status"] != string(bnovo.SearchStatusFailed) {
				if value, ok := event["parse_duration_ms"].(float64); !ok || value < 0 {
					t.Errorf("нет parse-duration: %v", event)
				}
			}
		case "bnovo.batch.completed":
			summaries++
			for key, want := range map[string]float64{"batch_duration_ms": float64(summary.BatchDurationMS), "success_count": 1, "partial_count": 1, "no_offers_count": 1, "failed_count": 1, "canceled_count": 0, "not_started_count": 0} {
				if event[key] != want {
					t.Errorf("сводка %s: %v", key, event)
				}
			}
		}
	}
	if len(seen) != 4 || len(attempts) != 4 || summaries != 1 {
		t.Errorf("неполные события: scenarios=%d attempts=%d summaries=%d", len(seen), len(attempts), summaries)
	}
}

func TestBatchCancellation(t *testing.T) {
	const workers, n = 2, 6
	entered, finished := make(chan struct{}, n), make(chan struct{}, n)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		defer func() { finished <- struct{}{} }()
		<-r.Context().Done()
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := batchRequests(n)
	done := launchBatch(ctx, client, requests, BatchOptions{Workers: workers})
	for range workers {
		receive(t, entered)
	}
	cancel()
	outcome := receive(t, done)
	for range workers {
		receive(t, finished)
	} // Все HTTP-обработчики также завершились.
	if outcome.err != nil || len(outcome.results) != n {
		t.Fatalf("неверный итог: %+v", outcome)
	}
	if outcome.summary.CanceledCount != workers || outcome.summary.NotStartedCount != n-workers || outcome.summary.FailedCount != n {
		t.Errorf("неверные счётчики отмены: %+v", outcome.summary)
	}
	for i, result := range outcome.results {
		if result.Status != bnovo.SearchStatusFailed || result.Error.Code != "canceled" || !reflect.DeepEqual(result.Request, requests[i]) {
			t.Errorf("потерян результат %d: %+v", i, result)
		}
		if i >= workers && (!result.CollectedAt.IsZero() || result.HTTPStatus != 0) {
			t.Errorf("незапущенный сценарий получил метаданные попытки: %+v", result)
		}
	}
	if len(entered) != 0 {
		t.Error("после отмены начались новые HTTP-запросы")
	}
}

func TestBatchAlreadyCanceled(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("неожиданный HTTP-запрос") }, 0)
			ctx, cancel := context.WithCancel(context.Background())
			code := "canceled"
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				code = "timeout"
			} else {
				cancel()
			}
			defer cancel()
			requests := batchRequests(3)
			results, summary, err := CollectBatch(ctx, client, requests, BatchOptions{})
			if err != nil || len(results) != 3 || summary.NotStartedCount != 3 || summary.CanceledCount != 0 || summary.FailedCount != 3 {
				t.Fatalf("неверный итог: %+v %v", summary, err)
			}
			for i, result := range results {
				if result.Error.Code != code || result.Status != bnovo.SearchStatusFailed || !result.CollectedAt.IsZero() || result.HTTPStatus != 0 || !reflect.DeepEqual(result.Request, requests[i]) {
					t.Errorf("потерян незапущенный сценарий: %+v", result)
				}
			}
		})
	}
}

func TestBatchHTTPTimeout(t *testing.T) {
	finished := make(chan struct{}, 3)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		defer func() { finished <- struct{}{} }()
		<-r.Context().Done()
	}, 30*time.Millisecond)
	results, summary, err := CollectBatch(context.Background(), client, batchRequests(3), BatchOptions{Workers: 2, Retry: RetryPolicy{MaxAttempts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for range results {
		receive(t, finished)
	}
	for _, result := range results {
		if result.Status != bnovo.SearchStatusFailed || result.Error.Code != "timeout" || !result.Error.Retryable {
			t.Errorf("неверный результат таймаута: %+v", result)
		}
	}
	if summary.FailedCount != 3 || summary.CanceledCount != 0 || summary.NotStartedCount != 0 {
		t.Errorf("таймаут сценария отменил пакет: %+v", summary)
	}
}

func TestBatchStartInterval(t *testing.T) {
	buf := captureBatchLogs(t)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, unavailableHTML) }, 0)
	const interval = 40 * time.Millisecond
	_, summary, err := CollectBatch(context.Background(), client, batchRequests(4), BatchOptions{Workers: 3, StartInterval: interval})
	if err != nil {
		t.Fatal(err)
	}
	var waits []float64
	var starts []time.Time
	for _, event := range batchEvents(t, buf) {
		if event["msg"] == "bnovo.batch.scenario.started" {
			waits = append(waits, event["queue_wait_ms"].(float64))
			startedAt, err := time.Parse(time.RFC3339Nano, event["time"].(string))
			if err != nil {
				t.Fatal(err)
			}
			starts = append(starts, startedAt)
		}
	}
	if len(waits) != 4 {
		t.Fatalf("неполные метрики очереди: %v", waits)
	}
	for i := 1; i < len(waits); i++ {
		// Нижняя граница допускает усечение до целых мс; верхнюю не ограничиваем.
		if starts[i].Sub(starts[i-1]) < interval-time.Millisecond {
			t.Errorf("интервал стартов не выдержан: %v", starts)
		}
	}
	for _, wait := range waits {
		if wait < 0 || summary.BatchDurationMS < int64(wait) {
			t.Errorf("неверное ожидание %v: %+v", wait, summary)
		}
	}
}

func TestBatchCancelWhileRateLimited(t *testing.T) {
	entered := make(chan struct{}, 3)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; fmt.Fprint(w, unavailableHTML) }, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := launchBatch(ctx, client, batchRequests(3), BatchOptions{Workers: 3, StartInterval: time.Hour})
	receive(t, entered)
	cancel()
	outcome := receive(t, done) // Не ждём час и не оставляем ожидающих workers/timer.
	if outcome.err != nil || outcome.summary.NotStartedCount != 2 || len(outcome.results) != 3 {
		t.Fatalf("неверная отмена ожидания: %+v", outcome)
	}
	if len(entered) != 0 {
		t.Error("ограничитель пропустил дополнительные запросы")
	}
}

func TestBatchOptionsAndEmptyInput(t *testing.T) {
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("неожиданный запрос") }, 0)
	for _, options := range []BatchOptions{{Workers: -1}, {Workers: MaxWorkers + 1}, {StartInterval: -time.Second}} {
		if _, _, err := CollectBatch(context.Background(), client, batchRequests(1), options); err == nil {
			t.Errorf("приняты неверные настройки: %+v", options)
		}
	}
	if _, _, err := CollectBatch(context.Background(), nil, batchRequests(1), BatchOptions{}); err == nil {
		t.Error("принят nil client")
	}
	results, summary, err := CollectBatch(context.Background(), client, nil, BatchOptions{Workers: MaxWorkers})
	if err != nil || len(results) != 0 || summary.SuccessCount+summary.PartialCount+summary.NoOffersCount+summary.FailedCount != 0 || summary.BatchDurationMS < 0 {
		t.Errorf("неверный пустой пакет: %+v %v", summary, err)
	}
}
