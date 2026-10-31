//go:build integration

package schedule

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

type liveRequest struct {
	UID, CheckIn, CheckOut           string
	Adults                           string
	StartedAt, HeadersAt, FinishedAt time.Time
	HTTPStatus                       int
	Bytes                            int
	SHA256                           string
	TransportError                   bool
	ErrorType                        string `json:",omitempty"`
	UnknownCertificateAuthority      bool   `json:",omitempty"`
	body                             []byte
}
type liveTransport struct {
	base         http.RoundTripper
	mu           sync.Mutex
	requests     []*liveRequest
	active, peak int
	captured     int
	cancel       context.CancelFunc
}
type liveBody struct {
	io.ReadCloser
	buffer    bytes.Buffer
	closeOnce sync.Once
	finish    func([]byte)
}

func (b *liveBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.buffer.Len()+n <= 8<<20 {
		_, _ = b.buffer.Write(p[:n])
	}
	return n, err
}
func (b *liveBody) Close() error {
	err := b.ReadCloser.Close()
	b.closeOnce.Do(func() { b.finish(b.buffer.Bytes()) })
	return err
}
func (p *liveTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	q := r.URL.Query()
	request := &liveRequest{UID: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], CheckIn: q.Get("dfrom"), CheckOut: q.Get("dto"), Adults: q.Get("adults"), StartedAt: time.Now().UTC()}
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.active++
	p.peak = max(p.peak, p.active)
	p.mu.Unlock()
	transport := p.base
	if transport == nil {
		transport = http.DefaultTransport
	}
	response, err := transport.RoundTrip(r)
	if err != nil {
		p.mu.Lock()
		request.TransportError = true
		request.ErrorType = fmt.Sprintf("%T", err)
		var unknown x509.UnknownAuthorityError
		request.UnknownCertificateAuthority = errors.As(err, &unknown)
		request.FinishedAt = time.Now().UTC()
		p.active--
		p.mu.Unlock()
		return response, err
	}
	p.mu.Lock()
	request.HTTPStatus = response.StatusCode
	request.HeadersAt = time.Now().UTC()
	p.mu.Unlock()
	response.Body = &liveBody{ReadCloser: response.Body, finish: func(body []byte) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.captured+len(body) <= 64<<20 {
			request.body = append([]byte(nil), body...)
			p.captured += len(body)
		}
		request.Bytes = len(body)
		hash := sha256.Sum256(body)
		request.SHA256 = hex.EncodeToString(hash[:])
		request.FinishedAt = time.Now().UTC()
		p.active--
	}}
	if response.StatusCode == 403 || response.StatusCode == 429 {
		p.cancel()
	}
	return response, nil
}

type liveEvidence struct {
	BaseSHA                string `json:"base_sha"`
	Phase                  string `json:"phase"`
	StartedAt, FinishedAt  time.Time
	DurationMS             int64
	Schema                 string
	SmokePassed            bool `json:"smoke_passed"`
	LindsayTestOnlyEnabled bool
	ExecutionError         bool
	Requests               []*liveRequest
	PeakHTTP               int
	MinimumStartGapMS      float64
	Response               Response
	SQL                    map[string]json.RawMessage
	Samples                []storage.Attempt
	Events                 []liveEvent
	Verification           liveChecks
	Interrupted            bool
	AfterStop              map[string]json.RawMessage
}

// TestLiveBnovoVerification never runs as part of make test-integration-docker.
// Only scripts/verify-bnovo-docker.py supplies the explicit phase/environment.
func TestLiveBnovoVerification(t *testing.T) {
	phase := os.Getenv("HOTEL_PARSER_LIVE_BNOVO")
	if phase != "smoke" && phase != "full" && phase != "replay" {
		t.Skip("live Bnovo is opt-in")
	}
	output := os.Getenv("HOTEL_PARSER_E2E_OUTPUT")
	if output == "" {
		t.Fatal("evidence output is required")
	}
	if err := os.WriteFile(output+".pid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal("cannot record verification PID")
	}
	store, admin, schema := storagetest.Open(t)
	worker := os.Getenv("HOTEL_PARSER_E2E_WORKER_SCHEMA") != ""
	if worker {
		schema = os.Getenv("HOTEL_PARSER_E2E_WORKER_SCHEMA")
		if !strings.HasPrefix(schema, "hp_test_") {
			t.Fatal("unsafe worker schema")
		}
		var err error
		store, err = storage.Open(context.Background(), os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{Schema: schema})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
	}
	r, err := LoadRegistry("../../configs/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	r.Groups = r.Groups[:1]
	r.Groups[0].Schema = schema
	if phase == "smoke" {
		r.Groups[0].Properties = r.Groups[0].Properties[:1]
	} else {
		for i := range r.Groups[0].Properties {
			r.Groups[0].Properties[i].Hotel.Collection.Enabled = true
		}
	}
	options := collect.RuntimeOptions{ExecutionTimeout: 3 * time.Minute, MaxJobs: 64}
	if phase == "smoke" {
		options.Batch.Retry.MaxAttempts = 1
	} else {
		options.Claim.LeaseDuration = 5 * time.Second
		options.RenewInterval = time.Second
	}
	service, err := Open(context.Background(), os.Getenv("HOTEL_PARSER_DATABASE_URL"), r, options)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeoutCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer timeoutCancel()
	probe := &liveTransport{cancel: cancel}
	if phase == "replay" {
		probe.base = loadLiveReplay(t)
	}
	logs := newLiveLogs()
	restore := logs.install()
	defer restore()
	client, err := bnovo.NewClient("https://reservationsteps.ru", &http.Client{Transport: probe})
	if err != nil {
		t.Fatal(err)
	}
	for i := range service.groups[0].Properties {
		service.groups[0].Properties[i].Client = client
	}
	evidence := liveEvidence{BaseSHA: os.Getenv("HOTEL_PARSER_E2E_BASE_SHA"), Phase: phase, StartedAt: time.Now().UTC(), Schema: schema, LindsayTestOnlyEnabled: phase != "smoke", SQL: map[string]json.RawMessage{}}
	defer func() {
		evidence.Interrupted = ctx.Err() != nil
		evidence.Events = append(evidence.Events, logs.snapshot()...)
		evidence.FinishedAt = time.Now().UTC()
		evidence.DurationMS = evidence.FinishedAt.Sub(evidence.StartedAt).Milliseconds()
		probe.mu.Lock()
		evidence.Requests = probe.requests
		evidence.PeakHTTP = probe.peak
		probe.mu.Unlock()
		sort.Slice(evidence.Requests, func(i, j int) bool { return evidence.Requests[i].StartedAt.Before(evidence.Requests[j].StartedAt) })
		for i := 1; i < len(evidence.Requests); i++ {
			gap := float64(evidence.Requests[i].StartedAt.Sub(evidence.Requests[i-1].StartedAt)) / float64(time.Millisecond)
			if evidence.MinimumStartGapMS == 0 || gap < evidence.MinimumStartGapMS {
				evidence.MinimumStartGapMS = gap
			}
		}
		liveSQL(t, admin, schema, &evidence)
		if directory := os.Getenv("HOTEL_PARSER_E2E_PRIVATE_RAW"); directory != "" {
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Error("private raw directory failed")
			}
			for _, request := range probe.requests {
				if len(request.body) > 0 {
					if err := os.WriteFile(filepath.Join(directory, request.SHA256+".html"), request.body, 0600); err != nil {
						t.Error("private raw capture failed")
					}
				}
			}
		}
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Error("encode evidence failed")
			return
		}
		if err = os.WriteFile(output, append(data, '\n'), 0600); err != nil {
			t.Error("save evidence failed")
		}
	}()
	loc, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(loc)
	slot := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, loc)
	if phase == "smoke" {
		slot = time.Date(2026, 12, 21, 20, 0, 0, 0, loc)
	}
	if !worker {
		if _, err := service.Execute(ctx, Request{Action: "plan", Slot: slot}); err != nil {
			t.Fatal(err)
		}
	}
	if phase == "full" && !worker {
		runLiveProcesses(t, ctx, schema, output, probe, &evidence)
		response, e := service.progress(context.Background(), nil)
		if e != nil {
			t.Fatal(e)
		}
		evidence.Response.Groups = response
		evidence.Response.ProgressKnown = true
		progress, e := store.Progress(context.Background(), "")
		if e != nil || progress.Remaining != 0 || progress.Completed+progress.Failed != 122 {
			t.Error("full process run incomplete; see evidence")
		}
		return
	}
	for invocation := 0; invocation < 4; invocation++ {
		response, runErr := service.Execute(ctx, Request{Action: "resume"})
		evidence.Response = response
		if runErr != nil {
			evidence.ExecutionError = true
			break
		}
		progress, err := store.Progress(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if progress.Remaining == 0 {
			break
		}
	}
	rows, err := admin.Query(`SELECT id::text,attempts FROM "` + schema + `".scenario_jobs WHERE attempts>0 ORDER BY property_id,check_in`)
	if err != nil {
		t.Fatal(err)
	}
	type saved struct {
		id string
		n  int
	}
	var jobs []saved
	for rows.Next() {
		var job saved
		if err := rows.Scan(&job.id, &job.n); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	rows.Close()
	for _, job := range jobs {
		savedJob, err := store.GetJob(context.Background(), job.id)
		if err != nil {
			t.Fatal(err)
		}
		for number := 1; number <= job.n; number++ {
			a, err := store.GetAttempt(context.Background(), job.id, number)
			if err != nil {
				t.Fatal(err)
			}
			checked := verifyLiveAttempt(t, r, probe.requests, savedJob, a, &evidence.Verification)
			day := int(savedJob.Scenario.CheckIn.Sub(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)) / (24 * time.Hour))
			if checked && (phase == "smoke" || day == 0 || day == 30 || day == 60) {
				evidence.Samples = append(evidence.Samples, a)
			}
		}
	}
	progress, err := store.Progress(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if phase == "smoke" {
		evidence.SmokePassed = !evidence.ExecutionError && progress.Completed == 1 && len(evidence.Samples) == 1 && len(evidence.Samples[0].Observations) > 0
		offers := 0
		for _, a := range evidence.Samples {
			for _, o := range a.Observations {
				offers += len(o.Offers)
			}
		}
		evidence.SmokePassed = evidence.SmokePassed && offers > 0
		if !evidence.SmokePassed {
			t.Error("real smoke did not meet persistence/offer criteria; see evidence")
		}
	}
	if (phase == "full" || phase == "replay") && !worker && (progress.Remaining != 0 || progress.Completed+progress.Failed != 122) {
		t.Error("full collection incomplete; see evidence")
	}
}

func liveSQL(t *testing.T, db *sql.DB, schema string, e *liveEvidence) {
	t.Helper()
	queries := map[string]string{
		"server_version": `SELECT json_build_object('version',version())`,
		"property_jobs":  `SELECT COALESCE(json_agg(x),'[]') FROM (SELECT property_id,status,count(*) AS jobs,sum(attempts) AS attempts FROM "` + schema + `".scenario_jobs GROUP BY property_id,status ORDER BY property_id,status) x`,
		"table_counts":   `SELECT json_build_object('runs',(SELECT count(*) FROM "` + schema + `".collection_runs),'jobs',(SELECT count(*) FROM "` + schema + `".scenario_jobs),'attempts',(SELECT count(*) FROM "` + schema + `".collection_attempts),'observations',(SELECT count(*) FROM "` + schema + `".observations),'variants',(SELECT count(*) FROM "` + schema + `".observation_variants),'offers',(SELECT count(*) FROM "` + schema + `".observation_offers))`,
		"http_codes":     `SELECT COALESCE(json_agg(x),'[]') FROM (SELECT http_status,error_code,count(*) AS attempts FROM "` + schema + `".collection_attempts GROUP BY http_status,error_code ORDER BY http_status,error_code) x`,
		"stock_status":   `SELECT COALESCE(json_agg(x),'[]') FROM (SELECT stock_status,confirmed_unavailable,count(*) AS observations FROM "` + schema + `".observations GROUP BY stock_status,confirmed_unavailable) x`,
	}
	for name, query := range liveAuditQueries(schema) {
		queries[name] = query
	}
	for name, query := range queries {
		var data []byte
		if err := db.QueryRow(query).Scan(&data); err != nil {
			t.Error("SQL evidence failed:", name)
			continue
		}
		e.SQL[name] = json.RawMessage(data)
	}
}
