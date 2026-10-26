package collect

import (
	"context"
	"fmt"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLimits(t *testing.T, limits Limits) {
	t.Helper()
	if err := ConfigureLimits(limits); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ConfigureLimits(Limits{}); err != nil {
			t.Error(err)
		}
	})
}
func inputJobs(clientIndex func(int) *bnovo.Client, properties []string) []batchJob {
	requests := batchRequests(len(properties))
	jobs := make([]batchJob, len(properties))
	ids := make(map[string]string)
	for i, property := range properties {
		id, ok := ids[property]
		if !ok {
			id = fmt.Sprintf("00000000-0000-0000-0000-%012x", len(ids)+1)
			ids[property] = id
		}
		requests[i].PropertyID = id
		requests[i].CheckIn = requests[i].CheckIn.AddDate(0, 0, i)
		requests[i].CheckOut = requests[i].CheckOut.AddDate(0, 0, i)
		jobs[i] = batchJob{request: requests[i], client: clientIndex(i), propertyID: property, source: "bnovo"}
	}
	return jobs
}

type jobOutcome struct {
	run batchRun
	err error
}

func launchJobs(ctx context.Context, jobs []batchJob, options BatchOptions) <-chan jobOutcome {
	ch := make(chan jobOutcome, 1)
	go func() { run, err := collectJobs(ctx, jobs, options); ch <- jobOutcome{run, err} }()
	return ch
}

func TestSourceKey(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"HTTPS://User:secret@Example.COM.:443/path?token=secret#secret", "https://example.com"},
		{"http://EXAMPLE.com:80/a", "http://example.com"},
		{"https://example.com:8443/path", "https://example.com:8443"},
		{"https://[::1]:443/a", "https://[::1]"},
	} {
		key, err := SourceKey(tc.raw)
		if err != nil || key != tc.want {
			t.Errorf("key=%q err=%v want=%q", key, err, tc.want)
		}
	}
	for _, raw := range []string{"ftp://example.com", "invalid", "https://example.com:99999", "https://example.com:bad"} {
		if _, err := SourceKey(raw); err == nil {
			t.Errorf("accepted invalid source")
		}
	}
	for _, limits := range []Limits{{PropertyConcurrency: -1}, {SourceConcurrency: MaxWorkers + 1}, {SourceStartInterval: -time.Second}, {Sources: map[string]SourceLimit{"https://example.com": {StartInterval: -1}}}, {Sources: map[string]SourceLimit{"https://EXAMPLE.com:443/a": {}, "https://example.com/": {}}}} {
		if err := ConfigureLimits(limits); err == nil {
			t.Errorf("invalid limits accepted")
		}
	}
}

func TestPropertyLimitAndRoundRobin(t *testing.T) {
	globalLimit(t, 8)
	testLimits(t, Limits{})
	var active, peak atomic.Int32
	entered := make(chan int, 306)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p; p = peak.Load() {
			if peak.CompareAndSwap(p, n) {
				break
			}
		}
		entered <- scenarioIndex(t, r)
		select {
		case <-release:
			fmt.Fprint(w, propertySuccessHTML)
		case <-r.Context().Done():
		}
	}, 0)
	properties := make([]string, 306)
	for i := range properties {
		properties[i] = "arena33"
		if i >= 300 {
			properties[i] = "lindsay"
		}
	}
	jobs := inputJobs(func(int) *bnovo.Client { return client }, properties)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := launchJobs(ctx, jobs, BatchOptions{Workers: 8})
	counts := map[string]int{}
	for range 8 {
		index := receive(t, entered)
		if index == 0 {
			counts["arena33"]++
		} else {
			counts["lindsay"]++
		}
	}
	if counts["arena33"] != 4 || counts["lindsay"] != 4 {
		t.Fatalf("unfair/over-limit starts: %v", counts)
	}
	once.Do(func() { close(release) })
	outcome := receive(t, done)
	if outcome.err != nil || peak.Load() > 8 || outcome.run.summary.SuccessCount != 306 {
		t.Fatalf("run failed: peak=%d summary=%+v err=%v", peak.Load(), outcome.run.summary, outcome.err)
	}
	for i, result := range outcome.run.results {
		if !reflect.DeepEqual(result.Request, jobs[i].request) {
			t.Errorf("result order lost at %d", i)
		}
	}
}

func TestSourceLimitsAndIndependentOrigins(t *testing.T) {
	for _, separate := range []bool{false, true} {
		t.Run(fmt.Sprint(separate), func(t *testing.T) {
			globalLimit(t, 8)
			testLimits(t, Limits{PropertyConcurrency: 4, SourceConcurrency: 1})
			entered := make(chan string, 6)
			release := make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			handler := func(label string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					entered <- label
					select {
					case <-release:
						fmt.Fprint(w, propertySuccessHTML)
					case <-r.Context().Done():
					}
				}
			}
			first := poolClient(t, handler("first"), 0)
			second := first
			if separate {
				second = poolClient(t, handler("second"), 0)
			}
			properties := []string{"a", "a", "a", "b", "b", "b"}
			jobs := inputJobs(func(i int) *bnovo.Client {
				if i >= 3 {
					return second
				}
				return first
			}, properties)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := launchJobs(ctx, jobs, BatchOptions{Workers: 6})
			labels := map[string]bool{receive(t, entered): true}
			if separate {
				labels[receive(t, entered)] = true
				if len(labels) != 2 {
					t.Errorf("independent source not admitted: %v", labels)
				}
			}
			globalConcurrency.Lock()
			occupied := globalConcurrency.active
			globalConcurrency.Unlock()
			want := 1
			if separate {
				want = 2
			}
			if occupied != want {
				t.Errorf("occupied=%d want=%d", occupied, want)
			}
			once.Do(func() { close(release) })
			outcome := receive(t, done)
			if outcome.err != nil || outcome.run.summary.SuccessCount != 6 {
				t.Fatalf("summary=%+v err=%v", outcome.run.summary, outcome.err)
			}
		})
	}
}

func TestCancelWaitingLimits(t *testing.T) {
	for _, level := range []string{"global", "property", "source", "rate"} {
		t.Run(level, func(t *testing.T) {
			globalLimit(t, 8)
			limits := Limits{PropertyConcurrency: 4, SourceConcurrency: 8}
			switch level {
			case "global":
				if err := ConfigureGlobalConcurrency(1); err != nil {
					t.Fatal(err)
				}
			case "property":
				limits.PropertyConcurrency = 1
			case "source":
				limits.SourceConcurrency = 1
			case "rate":
				limits.SourceStartInterval = time.Hour
			}
			testLimits(t, limits)
			entered := make(chan struct{}, 4)
			finished := make(chan struct{}, 4)
			client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				if level == "rate" {
					fmt.Fprint(w, propertySuccessHTML)
				} else {
					<-r.Context().Done()
				}
				finished <- struct{}{}
			}, 0)
			jobs := inputJobs(func(int) *bnovo.Client { return client }, []string{"a", "a", "a", "a"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := launchJobs(ctx, jobs, BatchOptions{Workers: 4})
			receive(t, entered)
			if err := ConfigureLimits(Limits{}); err == nil {
				t.Error("limits changed during batch")
			}
			cancel()
			outcome := receive(t, done)
			receive(t, finished)
			if outcome.err != nil || len(entered) != 0 {
				t.Fatalf("extra HTTP started: %v", outcome.err)
			}
			for i, result := range outcome.run.results {
				if !outcome.run.executed[i] && (outcome.run.queueWaitMS[i] != nil || result.Error.Code != "canceled") {
					t.Fatalf("bad unstarted job: %+v", result)
				}
			}
			globalConcurrency.Lock()
			active, batches := globalConcurrency.active, globalConcurrency.batches
			for _, state := range globalConcurrency.sources {
				if state.active != 0 {
					t.Error("source permit leaked")
				}
			}
			if len(globalConcurrency.waiters) != 0 {
				t.Error("admission waiter leaked")
			}
			if len(globalConcurrency.properties) != 0 {
				t.Error("property permit leaked")
			}
			globalConcurrency.Unlock()
			if active != 0 || batches != 0 {
				t.Fatalf("leaked state: active=%d batches=%d", active, batches)
			}
		})
	}
}

func TestSharedAdmissionFairnessAndPhysicalIdentity(t *testing.T) {
	globalLimit(t, 8)
	testLimits(t, Limits{PropertyConcurrency: 1, SourceConcurrency: 1})
	slots, unregister := registerBatch()
	defer unregister()
	held, _, _, ok := tryAdmission(context.Background(), slots, "origin/physical-a", "http://source.test", "batch0/0/1")
	if !ok {
		t.Fatal("initial admission failed")
	}
	if _, _, _, ok := tryAdmission(context.Background(), slots, "origin/physical-a", "http://source.test", "batch1/0/1"); ok {
		t.Fatal("property/source limit exceeded")
	}
	if _, _, _, ok := tryAdmission(context.Background(), slots, "origin/physical-b", "http://source.test", "batch2/0/1"); ok {
		t.Fatal("source limit exceeded")
	}
	if len(slots) != 1 {
		t.Fatal("blocked attempts acquired global permits")
	}
	held.release()
	first, _, _, ok := tryAdmission(context.Background(), slots, "origin/physical-a", "http://source.test", "batch1/0/1")
	if !ok {
		t.Fatal("earlier batch was starved")
	}
	first.release()
	// Освободившийся отель не может обогнать уже ожидающий другой отель.
	if _, _, _, ok := tryAdmission(context.Background(), slots, "origin/physical-a", "http://source.test", "batch1/1/1"); ok {
		t.Fatal("same hotel overtook another hotel's waiting batch")
	}
	second, _, _, ok := tryAdmission(context.Background(), slots, "origin/physical-b", "http://source.test", "batch2/0/1")
	if !ok {
		t.Fatal("other batch starved")
	}
	second.release()
	withdrawAdmissions("batch1")
	globalConcurrency.Lock()
	waiters := len(globalConcurrency.waiters)
	globalConcurrency.Unlock()
	if waiters != 0 {
		t.Fatal("pending admissions leaked")
	}
}

func TestPropertyAliasesShareLimitAcrossAPIs(t *testing.T) {
	globalLimit(t, 8)
	testLimits(t, Limits{PropertyConcurrency: 1, SourceConcurrency: 8})
	entered := make(chan struct{}, 4)
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
		entered <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, propertySuccessHTML)
		case <-r.Context().Done():
		}
	}, 0)
	input := inputJobs(func(int) *bnovo.Client { return client }, []string{"configured-hotel", "configured-hotel"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := launchJobs(ctx, input, BatchOptions{Workers: 2})
	receive(t, entered)
	// Прежний CollectBatch знает только UID. Должен разделять тот же физический лимит.
	second := launchBatch(ctx, client, []bnovo.SearchRequest{input[0].request, input[1].request}, BatchOptions{Workers: 2})
	once.Do(func() { close(release) })
	if outcome := receive(t, first); outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if outcome := receive(t, second); outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if peak.Load() != 1 {
		t.Errorf("hotel alias bypassed property limit: peak=%d", peak.Load())
	}
}

func TestAutomaticBnovoSourceLimit(t *testing.T) {
	// ConfigureLimits до первого допуска не вызывается: правило уже загружено процессом.
	t.Cleanup(func() {
		if err := ConfigureLimits(Limits{}); err != nil {
			t.Error(err)
		}
	})
	origin, err := SourceKey("https://RESERVATIONSTEPS.RU:443/rooms?ignored=true")
	if err != nil {
		t.Fatal(err)
	}
	globalConcurrency.Lock()
	configured := globalConcurrency.limits.Sources[origin]
	propertyLimit := globalConcurrency.limits.PropertyConcurrency
	globalConcurrency.Unlock()
	want := SourceLimit{Concurrency: 8, StartInterval: 500 * time.Millisecond}
	if configured != want || propertyLimit != 4 {
		t.Fatalf("automatic defaults: source=%+v property=%d", configured, propertyLimit)
	}
	slots, unregister := registerBatch()
	defer unregister()
	first, _, _, ok := tryAdmission(context.Background(), slots, "first-hotel", origin)
	if !ok {
		t.Fatal("initial Bnovo admission failed")
	}
	globalConcurrency.Lock()
	startedAt := globalConcurrency.sources[origin].lastStart
	globalConcurrency.Unlock()
	first.release()
	if permit, rateAt, _, ok := tryAdmission(context.Background(), slots, "second-hotel", origin); ok {
		permit.release()
		t.Fatal("Bnovo default did not throttle another hotel on the same source")
	} else if rateAt.Sub(startedAt) != 500*time.Millisecond {
		t.Fatalf("rate deadline=%s want 500ms", rateAt.Sub(startedAt))
	}
}

func TestBnovoSourceDefaultResetAndOverride(t *testing.T) {
	defer func() {
		if err := ConfigureLimits(Limits{}); err != nil {
			t.Error(err)
		}
	}()
	for _, tc := range []struct {
		name    string
		options Limits
		want    SourceLimit
	}{
		{"reset_keeps_default", Limits{}, SourceLimit{Concurrency: 8, StartInterval: 500 * time.Millisecond}},
		{"explicit_zero_disables_pause", Limits{Sources: map[string]SourceLimit{"https://RESERVATIONSTEPS.RU:443/path": {Concurrency: 2, StartInterval: 0}}}, SourceLimit{Concurrency: 2}},
		{"custom_interval", Limits{Sources: map[string]SourceLimit{"https://reservationsteps.ru": {Concurrency: 3, StartInterval: time.Second}}}, SourceLimit{Concurrency: 3, StartInterval: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ConfigureLimits(tc.options); err != nil {
				t.Fatal(err)
			}
			globalConcurrency.Lock()
			got := globalConcurrency.limits.Sources["https://reservationsteps.ru"]
			globalConcurrency.Unlock()
			if got != tc.want {
				t.Fatalf("source=%+v want=%+v", got, tc.want)
			}
			if tc.want.StartInterval == 0 {
				slots, unregister := registerBatch()
				defer unregister()
				first, _, _, ok := tryAdmission(context.Background(), slots, "a", "https://reservationsteps.ru")
				if !ok {
					t.Fatal("first admission failed")
				}
				defer first.release()
				second, _, _, ok := tryAdmission(context.Background(), slots, "b", "https://reservationsteps.ru")
				if !ok {
					t.Fatal("explicit zero interval ignored")
				}
				defer second.release()
				if extra, _, _, ok := tryAdmission(context.Background(), slots, "c", "https://reservationsteps.ru"); ok {
					extra.release()
					t.Fatal("custom concurrency ignored")
				}
			}
		})
	}
}
