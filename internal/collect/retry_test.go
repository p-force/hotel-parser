package collect

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

type signalHandler struct {
	slog.Handler
	event  string
	signal chan struct{}
}

func (h signalHandler) Handle(ctx context.Context, record slog.Record) error {
	err := h.Handler.Handle(ctx, record)
	if record.Message == h.event {
		select {
		case h.signal <- struct{}{}:
		default:
		}
	}
	return err
}
func (h signalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}
func (h signalHandler) WithGroup(name string) slog.Handler {
	h.Handler = h.Handler.WithGroup(name)
	return h
}
func signalLog(t *testing.T, event string) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{}, 8)
	previous := slog.Default()
	slog.SetDefault(slog.New(signalHandler{slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), event, ch}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return ch
}

func TestRetriesBoundedAndPermanentErrors(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504, 403, 400, 501, 505} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }, 0)
			results, summary, err := CollectBatch(context.Background(), client, batchRequests(1), BatchOptions{Retry: RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			switch status {
			case 429, 500, 502, 503, 504:
				want = 3
			}
			if calls.Load() != int32(want) || summary.RetryCount != want-1 || results[0].HTTPStatus != status || results[0].Error.Code != "http_error" {
				t.Fatalf("calls=%d summary=%+v result=%+v", calls.Load(), summary, results[0])
			}
		})
	}
}

func TestRetryReleasesPermitsAndDoesNotBlockOtherSource(t *testing.T) {
	globalLimit(t, 1)
	testLimits(t, Limits{PropertyConcurrency: 1, SourceConcurrency: 1})
	scheduled := signalLog(t, "bnovo.batch.retry.scheduled")
	var firstCalls atomic.Int32
	first := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}, 0)
	second := poolClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, propertySuccessHTML) }, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstJobs := inputJobs(func(int) *bnovo.Client { return first }, []string{"first"})
	done := launchJobs(ctx, firstJobs, BatchOptions{Workers: 1, Retry: RetryPolicy{MaxAttempts: 3}})
	receive(t, scheduled)
	// Retry-After удерживает только запись в очереди, ни worker, ни разрешения.
	results, summary, err := CollectBatch(context.Background(), second, batchRequests(1), BatchOptions{Workers: 1})
	if err != nil || summary.SuccessCount != 1 || len(results) != 1 {
		t.Fatalf("unrelated source blocked: %+v %v", summary, err)
	}
	cancel()
	outcome := receive(t, done)
	if outcome.err != nil || firstCalls.Load() != 1 || outcome.run.summary.RetryCount != 0 || outcome.run.results[0].Error.Code != "canceled" {
		t.Fatalf("retry after cancellation: %+v calls=%d err=%v", outcome.run.summary, firstCalls.Load(), outcome.err)
	}
	globalConcurrency.Lock()
	active := globalConcurrency.active
	globalConcurrency.Unlock()
	if active != 0 {
		t.Fatal("retry leaked global permit")
	}
}

func TestSourceRateAcrossBatchesAndRetry(t *testing.T) {
	globalLimit(t, 8)
	var calls atomic.Int32
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, propertySuccessHTML)
	}, 0)
	const interval = 25 * time.Millisecond
	key, err := SourceKey(client.SourceOrigin())
	if err != nil {
		t.Fatal(err)
	}
	testLimits(t, Limits{Sources: map[string]SourceLimit{key: {Concurrency: 4, StartInterval: interval}}})
	logs := captureBatchLogs(t)
	jobs := inputJobs(func(int) *bnovo.Client { return client }, []string{"a"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := BatchOptions{Workers: 2, Retry: RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}
	first, second := launchJobs(ctx, jobs, options), launchJobs(ctx, jobs, options)
	a, b := receive(t, first), receive(t, second)
	if a.err != nil || b.err != nil || calls.Load() != 3 || a.run.summary.RetryCount+b.run.summary.RetryCount != 1 || a.run.summary.RateWaitCount+b.run.summary.RateWaitCount == 0 {
		t.Fatalf("rate/retry counters: a=%+v b=%+v calls=%d", a.run.summary, b.run.summary, calls.Load())
	}
	var starts []time.Time
	for _, event := range batchEvents(t, logs) {
		if event["msg"] == "bnovo.batch.attempt.started" {
			at, err := time.Parse(time.RFC3339Nano, event["time"].(string))
			if err != nil {
				t.Fatal(err)
			}
			starts = append(starts, at)
			if event["source_origin"] != key {
				t.Errorf("wrong source identity: %v", event)
			}
		}
	}
	if len(starts) != 3 {
		t.Fatalf("starts=%d", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < interval-3*time.Millisecond {
			t.Errorf("source interval violated across batches/retry: %v", starts)
		}
	}
}

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("test transport panic") }
func TestPanicReleasesAllPermits(t *testing.T) {
	testLimits(t, Limits{})
	slots, unregister := registerBatch()
	defer unregister()
	permit, _, _, ok := tryAdmission(context.Background(), slots, "property", "http://example.test")
	if !ok {
		t.Fatal("no permit")
	}
	client, err := bnovo.NewClient("http://example.test", &http.Client{Transport: panicTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	caught := false
	func() {
		defer func() {
			if recover() != nil {
				caught = true
			}
		}()
		executeAttempt(context.Background(), batchJob{client: client, request: batchRequests(1)[0]}, permit)
	}()
	if !caught {
		t.Fatal("programming panic was swallowed")
	}
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	if globalConcurrency.active != 0 || len(slots) != 0 || len(globalConcurrency.properties) != 0 || globalConcurrency.sources["http://example.test"].active != 0 {
		t.Fatal("panic leaked permit")
	}
}

func TestRetryPolicyAndDelay(t *testing.T) {
	for _, policy := range []RetryPolicy{{MaxAttempts: 6}, {MaxAttempts: -1}, {InitialBackoff: -1}, {InitialBackoff: time.Second, MaxBackoff: time.Millisecond}, {MaxBackoff: 2 * time.Minute}} {
		if _, err := policy.normalized(); err == nil {
			t.Fatal("invalid retry policy accepted")
		}
	}
	policy := RetryPolicy{MaxAttempts: 5, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond}
	for attempt := 1; attempt <= 5; attempt++ {
		ceiling := min(policy.InitialBackoff*time.Duration(1<<(attempt-1)), policy.MaxBackoff)
		for range 20 {
			delay := retryDelay(policy, attempt, 0)
			if delay < ceiling/2 || delay >= ceiling {
				t.Errorf("jitter/backoff out of bounds: %v", delay)
			}
		}
	}
	if got := retryDelay(policy, 3, time.Hour); got != time.Hour {
		t.Errorf("Retry-After shortened: %v", got)
	}
	for _, status := range []bnovo.SearchStatus{bnovo.SearchStatusSuccess, bnovo.SearchStatusPartial, bnovo.SearchStatusNoOffers} {
		if retryable(bnovo.SearchResult{Status: status, Error: &bnovo.CollectionError{Code: "timeout", Retryable: true}}) {
			t.Fatalf("non-transport status retried: %s", status)
		}
	}
}

func TestRequestTimeoutRetry(t *testing.T) {
	var calls atomic.Int32
	finished := make(chan struct{}, 1)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			finished <- struct{}{}
			return
		}
		fmt.Fprint(w, propertySuccessHTML)
	}, 30*time.Millisecond)
	results, summary, err := CollectBatch(context.Background(), client, batchRequests(1), BatchOptions{Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}})
	receive(t, finished)
	if err != nil || calls.Load() != 2 || summary.SuccessCount != 1 || summary.RetryCount != 1 || results[0].Error != nil {
		t.Fatalf("timeout retry: calls=%d summary=%+v err=%v", calls.Load(), summary, err)
	}
}

type panicDispatchHandler struct{ slog.Handler }

func (h panicDispatchHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "bnovo.batch.scenario.started" {
		panic("dispatch log panic")
	}
	return h.Handler.Handle(ctx, record)
}
func TestDispatchPanicReleasesPermits(t *testing.T) {
	testLimits(t, Limits{})
	previous := slog.Default()
	slog.SetDefault(slog.New(panicDispatchHandler{slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})}))
	defer slog.SetDefault(previous)
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("panic before dispatch should not send HTTP") }, 0)
	caught := false
	func() {
		defer func() { caught = recover() != nil }()
		CollectBatch(context.Background(), client, batchRequests(1), BatchOptions{})
	}()
	if !caught {
		t.Fatal("dispatch panic swallowed")
	}
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	if globalConcurrency.active != 0 || globalConcurrency.batches != 0 || len(globalConcurrency.waiters) != 0 || len(globalConcurrency.properties) != 0 {
		t.Fatal("dispatch panic leaked permits/workers")
	}
}

func TestRetryBackoffDoesNotOccupySingleWorker(t *testing.T) {
	globalLimit(t, 1)
	testLimits(t, Limits{PropertyConcurrency: 1, SourceConcurrency: 1})
	var calls atomic.Int32
	first := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}, 0)
	entered := make(chan struct{}, 1)
	second := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, propertySuccessHTML)
		entered <- struct{}{}
	}, 0)
	jobs := inputJobs(func(i int) *bnovo.Client {
		if i == 0 {
			return first
		}
		return second
	}, []string{"first", "second"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := launchJobs(ctx, jobs, BatchOptions{Workers: 1, Retry: RetryPolicy{MaxAttempts: 3}})
	// Второй источник обслуживается тем же единственным worker, пока первый в backoff.
	receive(t, entered)
	cancel()
	outcome := receive(t, done)
	if outcome.err != nil || calls.Load() != 1 || outcome.run.attempts[0] != 1 || outcome.run.attempts[1] != 1 || outcome.run.summary.ThrottledCount != 1 {
		t.Fatalf("backoff held worker or retried after cancel: %+v err=%v", outcome.run.summary, outcome.err)
	}
}
