package vercelapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/schedule"
	"github.com/p-force/hotel-parser/internal/storage"
)

const shortCron = "schedule-test-12" // Official path minimum: 16, trigger minimum remains32.
func scheduleHeaders(r *http.Request, action, at string) {
	for k, v := range map[string]string{"ce-specversion": "1.0", "ce-type": "com.vercel.schedule.v1beta", "ce-source": "/schedule/default/morning-" + action, "ce-id": "schx_fixture", "ce-vssscheduleid": "sch_fixture", "ce-vssschedulename": "morning-" + action, "ce-vssnamespace": "default", "ce-vssscheduledat": at, "ce-vssschedulesource": "static"} {
		r.Header.Set(k, v)
	}
}
func scheduleCall(t *testing.T, h handler, action, token, at string, headers bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/"+action, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if headers {
		scheduleHeaders(r, action, at)
	}
	w := httptest.NewRecorder()
	h.serve(action, w, r)
	return w
}
func events(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var v map[string]any
		if e := json.Unmarshal([]byte(line), &v); e != nil {
			t.Fatal(e)
		}
		out = append(out, v)
	}
	return out
}
func TestSchedulesBearerAndUntrustedHeaders(t *testing.T) {
	for _, secret := range []string{"", strings.Repeat("x", 15), strings.Repeat(" ", 32), "bad-secret-00000\n", shortCron} {
		for _, token := range []string{"wrong", secret} {
			env := testEnv()
			env["CRON_SECRET"] = secret
			env["HOTEL_PARSER_COLLECTION_ENABLED"] = "false"
			h := testHandler(env)
			var logs bytes.Buffer
			h.log = slog.New(slog.NewJSONHandler(&logs, nil))
			w := scheduleCall(t, h, "start", token, "2026-11-10T05:00:00Z", true)
			want := 401
			if secret == shortCron && token == secret {
				want = 503
			}
			if w.Code != want {
				t.Fatal(secret == shortCron, w.Code, w.Body)
			}
			if w.Code == 401 && strings.Contains(logs.String(), "schx_fixture") {
				t.Fatal("untrusted metadata logged")
			}
			if strings.Contains(logs.String(), "Bearer ") {
				t.Fatal("credential logged")
			}
		}
	}
}
func TestScheduleMetadataDelayAndManualIdentity(t *testing.T) {
	for _, token := range []string{shortCron, testTrigger} {
		env := testEnv()
		env["CRON_SECRET"] = shortCron
		h := testHandler(env)
		h.now = func() time.Time { return time.Date(2026, 11, 10, 5, 2, 0, 0, time.UTC) }
		var logs bytes.Buffer
		h.log = slog.New(slog.NewJSONHandler(&logs, nil))
		f := &fakeApplication{}
		h.open = func(context.Context, string, schedule.Registry, collect.RuntimeOptions) (application, error) {
			return f, nil
		}
		w := scheduleCall(t, h, "start", token, "2026-11-10T05:00:00Z", true)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		found := false
		for _, e := range events(t, &logs) {
			if e["msg"] == "schedule.execution.completed" {
				found = true
				if token == shortCron {
					if e["schedule_execution_id"] != "schx_fixture" || e["delay_ms"] != float64(120000) {
						t.Fatal(e)
					}
				} else if _, ok := e["schedule_execution_id"]; ok {
					t.Fatal("manual request forged schedule identity", e)
				}
			}
		}
		if !found {
			t.Fatal("completion missing")
		}
	}
}
func TestScheduleInitialResumeAndInvalidMetadata(t *testing.T) {
	env := testEnv()
	env["CRON_SECRET"] = shortCron
	h := testHandler(env)
	if w := scheduleCall(t, h, "resume", shortCron, "2026-11-10T05:00:00Z", true); w.Code != 200 || !strings.Contains(w.Body.String(), "initial_resume_skipped") {
		t.Fatal(w.Code, w.Body)
	}
	for _, value := range []string{"not-a-time", "2026-02-31T05:00:00Z"} {
		if w := scheduleCall(t, h, "start", shortCron, value, true); w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("GET", "/api/start?secret=ignored", nil)
	r.Header.Set("Authorization", "Bearer "+shortCron)
	scheduleHeaders(r, "start", "2026-11-10T05:00:00Z")
	w := httptest.NewRecorder()
	h.serve("start", w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "/api/start", strings.NewReader("x"))
	r.Header.Set("Authorization", "Bearer "+shortCron)
	w = httptest.NewRecorder()
	h.serve("start", w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestBusyDoesNotHideJoinedFailure(t *testing.T) {
	for _, e := range []error{errors.Join(storage.ErrRunnerBusy, context.DeadlineExceeded), errors.Join(storage.ErrRunnerBusy, errors.New("private PostgreSQL error"))} {
		h := testHandler(testEnv())
		h.open = func(context.Context, string, schedule.Registry, collect.RuntimeOptions) (application, error) {
			return &fakeApplication{err: e}, nil
		}
		w := call(h, "resume", "GET", testTrigger, "", "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body)
		}
	}
}
func TestProgressScopeNoProgressAndRestart(t *testing.T) {
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	p := storage.RunProgress{Run: storage.Run{RunInput: storage.RunInput{ID: "run", StartedAt: now.Add(-time.Hour)}, Status: storage.RunRunning}, Queued: 3, Remaining: 3, Ready: 3, ObservedAt: now, LastActivityAt: now.Add(-31 * time.Minute)}
	before := schedule.GroupProgress{GroupID: "g", Runs: []storage.RunProgress{p}}
	response := schedule.Response{ProgressKnown: true, Before: []schedule.GroupProgress{before}, Groups: []schedule.GroupProgress{before}}
	for i := 0; i < 2; i++ { // Fresh logger/observer has no in-memory stall history.
		var logs bytes.Buffer
		x := executionLog{logger: slog.New(slog.NewJSONHandler(&logs, nil)), ctx: context.Background(), action: "resume", actual: now, measured: time.Now(), authorized: true, httpStatus: 200, responses: []schedule.Response{response}}
		x.finish()
		if !strings.Contains(logs.String(), "collection.no_progress") || !strings.Contains(logs.String(), `"remaining_before":3`) {
			t.Fatal(logs.String())
		}
	}
	response.Groups[0].Runs = append([]storage.RunProgress(nil), before.Runs...)
	response.Groups[0].Runs[0].Remaining = 2
	response.Groups[0].Runs[0].Queued = 2
	response.Groups[0].Runs[0].Succeeded = 1
	var logs bytes.Buffer
	x := executionLog{logger: slog.New(slog.NewJSONHandler(&logs, nil)), ctx: context.Background(), action: "resume", actual: now, measured: time.Now(), authorized: true, httpStatus: 200, responses: []schedule.Response{response}}
	x.finish()
	if strings.Contains(logs.String(), "collection.no_progress") || !strings.Contains(logs.String(), `"progress_status":"advanced"`) {
		t.Fatal(logs.String())
	}
	response.Groups[0].Runs[0].Queued++ // Different job set, not a false regression.
	logs.Reset()
	x.finished = false
	x.responses = []schedule.Response{response}
	x.finish()
	if strings.Contains(logs.String(), "collection.no_progress") {
		t.Fatal(logs.String())
	}
}
func TestSchedulesConfig(t *testing.T) {
	data, e := os.ReadFile("../../vercel.json")
	if e != nil {
		t.Fatal(e)
	}
	var config map[string]json.RawMessage
	if e = json.Unmarshal(data, &config); e != nil {
		t.Fatal(e)
	}
	if _, ok := config["crons"]; ok {
		t.Fatal("legacy cron retained")
	}
	var entries map[string]struct{ Schedule, Timezone, Path, RequestMethod string }
	if e = json.Unmarshal(config["schedules"], &entries); e != nil {
		t.Fatal(e)
	}
	expected := map[string]struct{ Schedule, Path string }{
		"morning-start":  {"0 8 * * *", "/api/start"},
		"midday-plan":    {"0 12 * * *", "/api/plan"},
		"afternoon-plan": {"0 16 * * *", "/api/plan"},
		"evening-plan":   {"0 20 * * *", "/api/plan"},
		"morning-resume": {"*/15 8-21 * * *", "/api/resume"},
	}
	if len(entries) != len(expected) {
		t.Fatal(entries)
	}
	for name, want := range expected {
		v, ok := entries[name]
		if !ok || v.Timezone != "Europe/Moscow" || v.RequestMethod != "GET" || v.Schedule != want.Schedule || v.Path != want.Path {
			t.Fatal(name, v)
		}
	}
}

func TestWaitingAndEmptyProgress(t *testing.T) {
	now := time.Now().UTC()
	for _, reason := range []string{"waiting_backoff", "waiting_lease"} {
		p := storage.RunProgress{Run: storage.Run{RunInput: storage.RunInput{ID: "pending"}, Status: storage.RunRunning}, Remaining: 1, ObservedAt: now, LastActivityAt: now.Add(-time.Minute)}
		if reason == "waiting_backoff" {
			p.Retryable = 1
			p.WaitingBackoff = 1
		} else {
			p.Running = 1
			p.WaitingLease = 1
		}
		g := schedule.GroupProgress{GroupID: "g", Runs: []storage.RunProgress{p}}
		r := schedule.Response{ProgressKnown: true, Before: []schedule.GroupProgress{g}, Groups: []schedule.GroupProgress{g}}
		var logs bytes.Buffer
		x := executionLog{logger: slog.New(slog.NewJSONHandler(&logs, nil)), ctx: context.Background(), action: "resume", actual: now, measured: now, authorized: true, httpStatus: 200, responses: []schedule.Response{r}}
		x.finish()
		if !strings.Contains(logs.String(), `"progress_status":"`+reason+`"`) || strings.Contains(logs.String(), "collection.no_progress") {
			t.Fatal(logs.String())
		}
	}
	if v := resultSummary("resume", []schedule.Response{{ProgressKnown: false}}); v != nil {
		t.Fatal("unknown progress replaced", v)
	}
}

type responseBoundary struct {
	*httptest.ResponseRecorder
	before func()
}

func (w responseBoundary) WriteHeader(status int) { w.before(); w.ResponseRecorder.WriteHeader(status) }
func TestFinalLogsAndResourcesPrecedeResponse(t *testing.T) {
	h := testHandler(testEnv())
	var logs bytes.Buffer
	h.log = slog.New(slog.NewJSONHandler(&logs, nil))
	f := &fakeApplication{}
	h.open = func(context.Context, string, schedule.Registry, collect.RuntimeOptions) (application, error) {
		return f, nil
	}
	r := httptest.NewRequest("GET", "/api/resume", nil)
	r.Header.Set("Authorization", "Bearer "+testTrigger)
	w := responseBoundary{httptest.NewRecorder(), func() {
		if !f.closed {
			t.Error("resources alive at HTTP publication")
		}
		if !strings.Contains(logs.String(), "schedule.execution.completed") {
			t.Error("final log emitted after HTTP publication")
		}
	}}
	h.serve("resume", w, r)
	if strings.Count(logs.String(), "schedule.execution.completed") != 1 {
		t.Fatal("duplicate terminal event", logs.String())
	}
}
