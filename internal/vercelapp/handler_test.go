package vercelapp

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/schedule"
	"github.com/p-force/hotel-parser/internal/storage"
)

const testTrigger = "test-trigger-000000000000000000000000"
const testCron = "test-cron-000000000000000000000000000"

func testEnv() map[string]string {
	return map[string]string{"HOTEL_PARSER_TRIGGER_SECRET": testTrigger, "CRON_SECRET": testCron, "HOTEL_PARSER_DATABASE_URL": "postgres://user:password@" + databaseHost + "/neondb?sslmode=require", "YANDEX_DISK_TOKEN": "test-only-token", "HOTEL_PARSER_COLLECTION_ENABLED": "true"}
}
func testHandler(env map[string]string) handler {
	return handler{env: func(k string) string { return env[k] }, now: func() time.Time { return time.Date(2026, 11, 10, 17, 1, 0, 0, time.UTC) }, registry: func() (schedule.Registry, error) { return schedule.LoadRegistry("../../configs/groups.json") }, open: func(context.Context, string, schedule.Registry, collect.RuntimeOptions) (application, error) {
		panic("unexpected database access")
	}, status: func(context.Context, string, schedule.Registry) (any, error) { panic("unexpected status access") }}
}
func call(h handler, action, method, auth, query, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/"+action+query, strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", "Bearer "+auth)
	}
	w := httptest.NewRecorder()
	h.serve(action, w, r)
	return w
}
func TestAuthorizationAndRequestBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, action, method, auth, query, body string
		status                                  int
	}{
		{"no auth", "health", "GET", "", "", "", 401},
		{"wrong auth", "resume", "POST", "wrong", "", "", 401},
		{"cron cannot read status", "status", "GET", testCron, "", "", 401},
		{"cron cannot post", "resume", "POST", testCron, "", "", 401},
		{"query secret rejected", "resume", "GET", testTrigger, "?secret=not-a-secret", "", 400},
		{"body cannot select jobs", "plan", "POST", testTrigger, "", `{"slot":"2026-11-10"}`, 400},
		{"method rejected", "health", "POST", testTrigger, "", "", 405},
		{"health no database connection", "health", "GET", testTrigger, "", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(testHandler(testEnv()), tc.action, tc.method, tc.auth, tc.query, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("response cached")
			}
		})
	}
}
func TestMissingConfigurationDoesNotStartCollection(t *testing.T) {
	for _, key := range []string{"HOTEL_PARSER_TRIGGER_SECRET", "HOTEL_PARSER_DATABASE_URL", "YANDEX_DISK_TOKEN", "HOTEL_PARSER_COLLECTION_ENABLED"} {
		t.Run(key, func(t *testing.T) {
			env := testEnv()
			delete(env, key)
			w := call(testHandler(env), "resume", "POST", testTrigger, "", "")
			if w.Code != 503 {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
	env := testEnv()
	env["HOTEL_PARSER_MAX_JOBS"] = "1000"
	if w := call(testHandler(env), "resume", "POST", testTrigger, "", ""); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestDatabaseGuard(t *testing.T) {
	for _, tc := range []struct {
		dsn   string
		valid bool
	}{
		{"", false},
		{"postgres://u:p@old-project.neon.tech/neondb?sslmode=require", false},
		{"postgres://u:p@" + strings.Replace(databaseHost, ".c-7", "-pooler.c-7", 1) + "/neondb?sslmode=require", false},
		{"postgres://u:p@" + databaseHost + "/neondb?sslmode=disable", false},
		{"postgres://u:p@" + databaseHost + "/neondb?sslmode=prefer", false},
		{"postgres://u:p@" + databaseHost + "/other?sslmode=require", false},
		{"postgres://u:p@" + databaseHost + "/neondb?sslmode=require&host=localhost", false},
		{testEnv()["HOTEL_PARSER_DATABASE_URL"], true},
	} {
		if got := validDatabase(tc.dsn); got != tc.valid {
			t.Fatalf("valid=%v, want %v", got, tc.valid)
		}
	}
}

type fakeApplication struct {
	requests []schedule.Request
	closed   bool
	err      error
	context  context.Context
}

func (f *fakeApplication) Execute(ctx context.Context, r schedule.Request) (schedule.Response, error) {
	f.context = ctx
	f.requests = append(f.requests, r)
	return schedule.Response{ProgressKnown: true}, f.err
}
func (f *fakeApplication) Close() { f.closed = true }

func TestCronPlanUsesExistingDueSlotsAndClosesBeforeReturn(t *testing.T) {
	env := testEnv()
	env["HOTEL_PARSER_MAX_JOBS"] = "1"
	h := testHandler(env)
	f := &fakeApplication{}
	h.open = func(ctx context.Context, dsn string, r schedule.Registry, o collect.RuntimeOptions) (application, error) {
		if o.MaxJobs != 1 || o.Batch.Workers != 8 || o.ExecutionTimeout != 40*time.Second || o.ShutdownGracePeriod != 5*time.Second {
			t.Fatal(o)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("no open deadline")
		}
		return f, nil
	}
	w := call(h, "plan", "GET", testCron, "", "")
	if w.Code != 200 || !f.closed || len(f.requests) != 4 {
		t.Fatal(w.Code, w.Body, f)
	}
	for i, r := range f.requests {
		if r.Action != "plan" || r.Slot.UTC().Hour() != 5+4*i || r.Slot.Minute() != 0 {
			t.Fatal(r)
		}
	}
	if f.context.Err() != context.Canceled {
		t.Fatal("request resources remain alive")
	}
}
func TestResumeAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{nil, 200, ""}, {storage.ErrRunnerBusy, 409, "runner_busy"}, {errors.New("private URL?secret=hidden"), 503, "collection_incomplete"},
	} {
		h := testHandler(testEnv())
		f := &fakeApplication{err: tc.err}
		h.open = func(context.Context, string, schedule.Registry, collect.RuntimeOptions) (application, error) {
			return f, nil
		}
		w := call(h, "resume", "POST", testTrigger, "", "")
		if w.Code != tc.status || !f.closed || len(f.requests) != 1 || f.requests[0].Action != "resume" {
			t.Fatal(w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "hidden") {
			t.Fatal("unsafe error")
		}
		if tc.code != "" && !strings.Contains(w.Body.String(), tc.code) {
			t.Fatal(w.Body)
		}
	}
}
func TestStatusIsReadOnlyAndNeedsNoCollectionEnablement(t *testing.T) {
	env := testEnv()
	delete(env, "HOTEL_PARSER_COLLECTION_ENABLED")
	delete(env, "YANDEX_DISK_TOKEN")
	h := testHandler(env)
	calls := 0
	h.status = func(ctx context.Context, dsn string, r schedule.Registry) (any, error) {
		calls++
		return map[string]string{"state": "delivered"}, nil
	}
	w := call(h, "status", "GET", testTrigger, "", "")
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "delivered") {
		t.Fatal(w.Code, w.Body)
	}
}
func TestCanceledRequestReachesApplication(t *testing.T) {
	h := testHandler(testEnv())
	f := &fakeApplication{err: context.Canceled}
	h.open = func(context.Context, string, schedule.Registry, collect.RuntimeOptions) (application, error) {
		return f, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/api/resume", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+testTrigger)
	w := httptest.NewRecorder()
	h.serve("resume", w, r)
	if w.Code != 503 || f.context.Err() != context.Canceled || !f.closed {
		t.Fatal(w.Code, f)
	}
}

func TestEmbeddedRegistryDoesNotDependOnWorkingDirectory(t *testing.T) {
	expected, err := schedule.LoadRegistry("../../configs/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	actual, err := loadRegistry()
	if err != nil || len(actual.Groups) != len(expected.Groups) {
		t.Fatal(actual, err)
	}
	for i, group := range actual.Groups {
		if group.ID != expected.Groups[i].ID || group.Schema != expected.Groups[i].Schema || len(group.Properties) != len(expected.Groups[i].Properties) {
			t.Fatal(group)
		}
		for j, property := range group.Properties {
			if property.Hotel.ID != expected.Groups[i].Properties[j].Hotel.ID || property.Hotel.Source != expected.Groups[i].Properties[j].Hotel.Source {
				t.Fatal(property)
			}
		}
	}
}
