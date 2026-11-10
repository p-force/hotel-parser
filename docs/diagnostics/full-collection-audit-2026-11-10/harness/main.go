// Command harness runs an explicitly enabled, local-only diagnostic collection.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/report"
	"github.com/p-force/hotel-parser/internal/schedule"
	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/storage"
)

type callKey struct{}
type callInfo struct {
	ID               int64
	Property, Source string
	Request          source.SearchRequest
}
type httpMetric struct {
	CallID     int64     `json:"call_id"`
	Property   string    `json:"property"`
	Source     string    `json:"source"`
	Endpoint   string    `json:"endpoint"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	StartNS    int64     `json:"start_ns"`
	DurationNS int64     `json:"duration_ns"`
	HeadersNS  int64     `json:"headers_ns"`
	Bytes      int64     `json:"bytes"`
	Status     int       `json:"status"`
	RetryAfter string    `json:"retry_after,omitempty"`
	Error      bool      `json:"error"`
	SHA256     string    `json:"sha256"`
}
type callMetric struct {
	Info                                      callInfo  `json:"info"`
	Started                                   time.Time `json:"started"`
	DurationNS                                int64     `json:"duration_ns"`
	Status                                    string    `json:"status"`
	HTTPStatus                                int       `json:"http_status"`
	ErrorCode                                 string    `json:"error_code,omitempty"`
	Offers, Categories                        int
	WarningCodes                              []string
	Prices, Meals, Cancellations, Prepayments int
}
type measurement struct {
	mu                                           sync.Mutex
	epoch                                        time.Time
	http                                         []httpMetric
	calls                                        []callMetric
	activeHTTP, peakHTTP, activeCalls, peakCalls int
	samples                                      []sample
	next                                         atomic.Int64
}
type transport struct {
	base http.RoundTripper
	m    *measurement
}
type body struct {
	io.ReadCloser
	once   sync.Once
	finish func()
	read   func([]byte, error)
}

func (b *body) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	b.read(p[:n], e)
	return n, e
}
func (b *body) Close() error { e := b.ReadCloser.Close(); b.once.Do(b.finish); return e }
func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	// This is immediately before the base transport, after the adapter's start gate.
	at := time.Now()
	info, ok := r.Context().Value(callKey{}).(callInfo)
	if !ok || r.Method != "GET" || (r.URL.Hostname() != "reservationsteps.ru" && r.URL.Hostname() != "ru-ibe.tlintegration.ru") {
		return nil, errors.New("audit refuses unexpected HTTP target")
	}
	metric := httpMetric{CallID: info.ID, Property: info.Property, Source: info.Source, Endpoint: filepath.Base(r.URL.Path), Started: at.UTC(), StartNS: at.Sub(t.m.epoch).Nanoseconds()}
	if info.Source == "bnovo" {
		metric.Endpoint = "rooms"
	}
	t.m.mu.Lock()
	t.m.activeHTTP++
	t.m.peakHTTP = max(t.m.peakHTTP, t.m.activeHTTP)
	t.m.mu.Unlock()
	hash := sha256.New()
	var count int64
	var readError bool
	finish := func() {
		metric.Finished = time.Now().UTC()
		metric.DurationNS = time.Since(at).Nanoseconds()
		metric.Bytes = count
		metric.Error = metric.Error || readError
		metric.SHA256 = hex.EncodeToString(hash.Sum(nil))
		t.m.mu.Lock()
		t.m.activeHTTP--
		t.m.http = append(t.m.http, metric)
		t.m.mu.Unlock()
	}
	res, e := t.base.RoundTrip(r)
	if e != nil {
		metric.Error = true
		finish()
		return res, e
	}
	metric.Status = res.StatusCode
	metric.HeadersNS = time.Since(at).Nanoseconds()
	if value := res.Header.Get("Retry-After"); len(value) <= 128 {
		metric.RetryAfter = value
	}
	res.Body = &body{ReadCloser: res.Body, finish: finish, read: func(p []byte, e error) {
		count += int64(len(p))
		_, _ = hash.Write(p)
		if e != nil && e != io.EOF {
			readError = true
		}
	}}
	return res, nil
}

type client struct {
	source.Client
	property string
	m        *measurement
}

func (c *client) Collect(ctx context.Context, req source.SearchRequest) source.SearchResult {
	at := time.Now()
	info := callInfo{ID: c.m.next.Add(1), Property: c.property, Source: c.SourceType(), Request: req}
	c.m.mu.Lock()
	c.m.activeCalls++
	c.m.peakCalls = max(c.m.peakCalls, c.m.activeCalls)
	c.m.mu.Unlock()
	result := c.Client.Collect(context.WithValue(ctx, callKey{}, info), req)
	metric := callMetric{Info: info, Started: at.UTC(), DurationNS: time.Since(at).Nanoseconds(), Status: string(result.Status), HTTPStatus: result.HTTPStatus, Offers: len(result.Offers), Categories: len(result.Categories)}
	if result.Error != nil {
		metric.ErrorCode = result.Error.Code
	}
	for _, w := range result.Warnings {
		metric.WarningCodes = append(metric.WarningCodes, w.Code)
	}
	for _, o := range result.Offers {
		if o.Price != "" {
			metric.Prices++
		}
		if o.MealPlan != "" {
			metric.Meals++
		}
		if o.Cancellation != "" {
			metric.Cancellations++
		}
		if o.Prepayment != "" {
			metric.Prepayments++
		}
	}
	c.m.mu.Lock()
	c.m.activeCalls--
	c.m.calls = append(c.m.calls, metric)
	c.m.mu.Unlock()
	return result
}

type sample struct {
	At                      time.Time
	CPUSeconds              float64
	CPUPercent              float64
	PeakRSSBytes            int64
	Goroutines              int
	HeapBytes               uint64
	ActiveHTTP, ActiveCalls int
	DBActive, DBIdle        int
	DBSampleOK              bool
}

func cpu() (float64, int64) {
	var u syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &u)
	rss := u.Maxrss
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	return float64(u.Utime.Sec+u.Stime.Sec) + float64(u.Utime.Usec+u.Stime.Usec)/1e6, rss
}
func monitor(ctx context.Context, m *measurement, conn *pgx.Conn, app string, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := time.Now()
	lastCPU, _ := cpu()
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-ticker.C:
			used, rss := cpu()
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			s := sample{At: at.UTC(), CPUSeconds: used, CPUPercent: (used - lastCPU) / at.Sub(last).Seconds() * 100, PeakRSSBytes: rss, Goroutines: runtime.NumGoroutine(), HeapBytes: mem.HeapAlloc}
			last, lastCPU = at, used
			q, stop := context.WithTimeout(ctx, 500*time.Millisecond)
			s.DBSampleOK = conn.QueryRow(q, `SELECT count(*) FILTER(WHERE state='active'),count(*) FILTER(WHERE state='idle') FROM pg_stat_activity WHERE application_name=$1`, app).Scan(&s.DBActive, &s.DBIdle) == nil
			stop()
			m.mu.Lock()
			s.ActiveHTTP, s.ActiveCalls = m.activeHTTP, m.activeCalls
			m.samples = append(m.samples, s)
			m.mu.Unlock()
		}
	}
}
func save(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func id() string {
	s := sha256.Sum256([]byte(rand.Text()))
	s[6] = (s[6] & 15) | 64
	s[8] = (s[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", s[:4], s[4:6], s[6:8], s[8:10], s[10:16])
}
func scenario(h config.Config, day time.Time, adults, los int) storage.Scenario {
	return storage.Scenario{PropertyID: h.ID, Source: h.Source.Type, SourceOrigin: h.Source.BaseURL, SourcePropertyID: h.Source.PropertyID, CheckIn: day, CheckOut: day.AddDate(0, 0, los), LOS: los, Adults: adults, Rooms: 1, ChildrenAges: []int{}, Language: "ru"}
}
func scenarios(h config.Config, day time.Time) []storage.Scenario {
	var result []storage.Scenario
	for d := 0; d <= h.Collection.DaysAhead; d++ {
		in := day.AddDate(0, 0, d)
		seen := map[string]bool{}
		add := func(a, n int) {
			key := fmt.Sprintf("%d/%d", a, n)
			if !seen[key] {
				seen[key] = true
				result = append(result, scenario(h, in, a, n))
			}
		}
		for _, a := range h.Collection.Adults {
			for _, n := range h.Collection.Nights {
				add(a, n)
			}
		}
		add(1, 1)
	}
	return result
}
func snapshot(ctx context.Context, c *pgx.Conn, schema string) (map[string]any, error) {
	out := map[string]any{}
	var size, dbsize int64
	if e := c.QueryRow(ctx, `SELECT COALESCE(sum(pg_total_relation_size(c.oid)),0)::bigint FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','m')`, schema).Scan(&size); e != nil {
		return nil, e
	}
	_ = c.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&dbsize)
	out["schema_bytes"], out["database_bytes"] = size, dbsize
	counts := map[string]int64{}
	for _, table := range []string{"collection_runs", "collection_plans", "scenario_jobs", "collection_attempts", "observations", "observation_variants", "observation_offers", "csv_deliveries"} {
		var count int64
		if e := c.QueryRow(ctx, `SELECT count(*) FROM "`+schema+`".`+table).Scan(&count); e != nil {
			return nil, e
		}
		counts[table] = count
	}
	out["counts"] = counts
	return out, nil
}
func localConfig(dsn string) (*pgx.ConnConfig, error) {
	c, e := pgx.ParseConfig(dsn)
	if e != nil {
		return nil, errors.New("invalid local database configuration")
	}
	for _, host := range append([]string{c.Host}, fallbackHosts(c)...) {
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("audit refuses non-loopback database")
		}
	}
	return c, nil
}
func fallbackHosts(c *pgx.ConnConfig) []string {
	var h []string
	for _, f := range c.Fallbacks {
		h = append(h, f.Host)
	}
	return h
}

// ConnConfig.ConnString returns the original string, not modified RuntimeParams.
func taggedDSN(dsn, application string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", errors.New("audit requires a PostgreSQL URL")
	}
	query := u.Query()
	query.Set("application_name", application)
	u.RawQuery = query.Encode()
	return u.String(), nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "audit failed:", e)
		os.Exit(1)
	}
}
func run() error {
	live := flag.Bool("live", false, "explicitly allow bounded public source HTTP")
	out := flag.String("out", "", "private local output directory")
	schema := flag.String("schema", "", "isolated hp_audit_ schema")
	flag.Parse()
	if !*live || *out == "" || !strings.HasPrefix(*schema, "hp_audit_") {
		return errors.New("explicit --live, --out and isolated --schema are required")
	}
	if os.Getenv("YANDEX_DISK_TOKEN") != "" {
		return errors.New("unset YANDEX_DISK_TOKEN: real delivery is forbidden")
	}
	dsn := os.Getenv("HOTEL_PARSER_DATABASE_URL")
	cfg, e := localConfig(dsn)
	if e != nil {
		return e
	}
	app := *schema
	dsn, e = taggedDSN(dsn, app)
	if e != nil {
		return e
	}
	cfg, e = localConfig(dsn)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(*out, 0700); e != nil {
		return e
	}
	logFile, e := os.OpenFile(filepath.Join(*out, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	defer logFile.Close()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})))
	overall := time.Now()
	startCPU, _ := cpu()
	m := &measurement{epoch: overall}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, deadline := context.WithTimeout(ctx, 65*time.Minute)
	defer deadline()
	monitorCfg := cfg.Copy()
	monitorCfg.RuntimeParams["application_name"] = app + "_monitor"
	connection, e := pgx.ConnectConfig(ctx, monitorCfg)
	if e != nil {
		return errors.New("local monitor connection failed")
	}
	defer connection.Close(context.Background())
	store, e := storage.Open(ctx, dsn, storage.Options{Schema: *schema})
	if e != nil {
		return e
	}
	defer store.Close()
	var exists bool
	if e = connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, *schema).Scan(&exists); e != nil {
		return errors.New("schema preflight failed")
	}
	if exists {
		return errors.New("refuse to reuse an existing audit schema")
	}
	if e = store.Migrate(ctx); e != nil {
		return e
	}
	before, e := snapshot(ctx, connection, *schema)
	if e != nil {
		return errors.New("baseline snapshot failed")
	}
	monitorCtx, endMonitor := context.WithCancel(context.Background())
	monitorDone := make(chan struct{})
	go monitor(monitorCtx, m, connection, app, monitorDone)
	defer func() { endMonitor(); <-monitorDone }()
	registry, e := schedule.LoadRegistry("configs/groups.json")
	if e != nil {
		return e
	}
	loc, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	original := http.DefaultTransport
	http.DefaultTransport = &transport{base: original, m: m}
	defer func() { http.DefaultTransport = original }()
	if len(registry.Groups) != 1 {
		return errors.New("audit expects the existing single group")
	}
	group := collect.RuntimeGroup{ID: registry.Groups[0].ID, Store: store}
	runs := []storage.Run{}
	plans := map[string][]storage.Scenario{}
	hotels := []config.Config{}
	expectedHTTP := 0
	planStart := time.Now()
	for _, g := range registry.Groups {
		for _, p := range g.Properties {
			h := p.Hotel
			if !h.Collection.Enabled {
				continue
			}
			ss := scenarios(h, day)
			plans[h.ID] = ss
			hotels = append(hotels, h)
			latest := day
			for _, s := range ss {
				if s.CheckOut.After(latest) {
					latest = s.CheckOut
				}
			}
			input := storage.RunInput{ID: id(), PropertyID: h.ID, PeriodStart: day, PeriodEnd: latest, StartedAt: now.UTC()}
			r, e := store.PlanRun(ctx, input, ss)
			if e != nil {
				return e
			}
			runs = append(runs, r)
			c, e := collect.NewSourceClient(h)
			if e != nil {
				return e
			}
			group.Properties = append(group.Properties, collect.PropertyCollection{Hotel: h, Client: &client{Client: c, property: h.ID, m: m}})
			expectedHTTP += len(ss)
			if h.Source.Type == "travelline" {
				expectedHTTP += len(ss)
			}
		}
	}
	planning := time.Since(planStart)
	if e = save(filepath.Join(*out, "plan.json"), map[string]any{"schema": *schema, "started_at": overall.UTC(), "hotels": hotels, "runs": runs, "scenarios": plans, "expected_http_without_retries": expectedHTTP, "planning_ns": planning.Nanoseconds(), "go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}); e != nil {
		return e
	}
	planned := 0
	for _, s := range plans {
		planned += len(s)
	}
	fmt.Printf("PLAN hotels=%d jobs=%d expected_http=%d schema=%s\n", len(hotels), planned, expectedHTTP, *schema)
	runStart := time.Now()
	guard, e := store.TryRunnerLock(ctx)
	if e != nil {
		return e
	}
	results, runErr := collect.RunGroups(ctx, []collect.RuntimeGroup{group}, collect.RuntimeOptions{Batch: collect.BatchOptions{Workers: 8}, MaxJobs: planned * 3, Claim: storage.ClaimOptions{Owner: "full-audit"}, ExecutionTimeout: 55 * time.Minute, ShutdownGracePeriod: 5 * time.Second, RunnerLock: guard})
	guard.Close()
	runtimeDuration := time.Since(runStart)
	finalizeStart := time.Now()
	artifacts := []report.Artifact{}
	csvMetrics := []map[string]any{}
	for _, r := range runs {
		progress, e := store.Progress(ctx, r.ID)
		if e != nil {
			return e
		}
		if progress.Remaining != 0 {
			continue
		}
		if _, e = store.FinishRun(ctx, r.ID); e != nil {
			return e
		}
		at := time.Now()
		artifact, e := report.Generate(ctx, store, group.ID, filepath.Join(*out, "csv"), r.ID)
		artifacts = append(artifacts, artifact)
		if e != nil {
			return e
		}
		data, e := os.ReadFile(artifact.Path)
		if e != nil {
			return e
		}
		reader := csv.NewReader(strings.NewReader(string(data)))
		rows, e := reader.ReadAll()
		if e != nil {
			return e
		}
		sha := sha256.Sum256(data)
		csvMetrics = append(csvMetrics, map[string]any{"property": r.PropertyID, "run_id": r.ID, "duration_ns": time.Since(at).Nanoseconds(), "bytes": len(data), "rows": len(rows) - 1, "columns": len(rows[0]), "sha256": hex.EncodeToString(sha[:]), "path": artifact.Path})
	}
	finalization := time.Since(finalizeStart)
	// Avoid concurrent queries on the single monitor connection before readback snapshots.
	endMonitor()
	<-monitorDone
	monitorDone = make(chan struct{})
	close(monitorDone)
	after, e := snapshot(ctx, connection, *schema)
	if e != nil {
		return errors.New("final snapshot failed")
	}
	m.mu.Lock()
	httpBefore, callsBefore := len(m.http), len(m.calls)
	m.mu.Unlock()
	repeatStart := time.Now()
	guard, e = store.TryRunnerLock(ctx)
	if e != nil {
		return e
	}
	_, repeatErr := collect.RunGroups(ctx, []collect.RuntimeGroup{group}, collect.RuntimeOptions{Batch: collect.BatchOptions{Workers: 8}, MaxJobs: planned, Claim: storage.ClaimOptions{Owner: "full-audit-replay"}, ExecutionTimeout: time.Minute, RunnerLock: guard})
	guard.Close()
	repeatDuration := time.Since(repeatStart)
	afterRepeat, e := snapshot(ctx, connection, *schema)
	if e != nil {
		return e
	}
	endCPU, peakRSS := cpu()
	progress, e := store.RunProgress(ctx)
	if e != nil {
		return e
	}
	summary := map[string]any{"started_at": overall.UTC(), "finished_at": time.Now().UTC(), "wall_ns": time.Since(overall).Nanoseconds(), "planning_ns": planning.Nanoseconds(), "runtime_ns": runtimeDuration.Nanoseconds(), "finalization_ns": finalization.Nanoseconds(), "cpu_seconds": endCPU - startCPU, "peak_rss_bytes": peakRSS, "before": before, "after": after, "after_replay": afterRepeat, "runtime_result": results, "runtime_error": runErr != nil, "repeat_error": repeatErr != nil, "repeat_ns": repeatDuration.Nanoseconds(), "repeat_http": len(m.http) - httpBefore, "repeat_calls": len(m.calls) - callsBefore, "peak_http": m.peakHTTP, "peak_collect_calls": m.peakCalls, "progress": progress, "csv": csvMetrics, "artifacts": artifacts, "delivery": "disabled; no Yandex Disk token or HTTP"}
	if e = save(filepath.Join(*out, "summary.json"), summary); e != nil {
		return e
	}
	if e = save(filepath.Join(*out, "http.json"), m.http); e != nil {
		return e
	}
	if e = save(filepath.Join(*out, "calls.json"), m.calls); e != nil {
		return e
	}
	if e = save(filepath.Join(*out, "samples.json"), m.samples); e != nil {
		return e
	}
	fmt.Printf("DONE jobs=%d http=%d wall_seconds=%.3f peak_http=%d peak_rss_bytes=%d\n", planned, len(m.http), time.Since(overall).Seconds(), m.peakHTTP, peakRSS)
	return runErr
}

// quantile uses the observed nearest rank; it does not interpolate unmeasured values.
func quantile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	v := append([]int64(nil), values...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	index := int(float64(len(v)) * p)
	if float64(index) < float64(len(v))*p {
		index++
	}
	if index < 1 {
		index = 1
	}
	return v[index-1]
}
