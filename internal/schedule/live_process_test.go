//go:build integration

package schedule

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

type liveEvent struct {
	Time    time.Time
	Message string
	Values  map[string]any
}
type liveLogs struct {
	mu     sync.Mutex
	events []liveEvent
}
type liveHandler struct {
	slog.Handler
	logs *liveLogs
}

func newLiveLogs() *liveLogs { return &liveLogs{} }
func (l *liveLogs) install() func() {
	old := slog.Default()
	slog.SetDefault(slog.New(liveHandler{slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}), l}))
	return func() { slog.SetDefault(old) }
}
func (l *liveLogs) snapshot() []liveEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]liveEvent(nil), l.events...)
}
func (h liveHandler) Handle(ctx context.Context, r slog.Record) error {
	e := liveEvent{Time: r.Time, Message: r.Message, Values: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool { e.Values[a.Key] = a.Value.Any(); return true })
	h.logs.mu.Lock()
	h.logs.events = append(h.logs.events, e)
	h.logs.mu.Unlock()
	return h.Handler.Handle(ctx, r)
}
func (h liveHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}

// Real child processes use exactly the same opt-in test/runtime. No proxy or
// alternate scheduler: SIGTERM reaches signal.NotifyContext and the lease API.
func runLiveProcesses(t *testing.T, ctx context.Context, schema, output string, probe *liveTransport, e *liveEvidence) {
	t.Helper()
	log, err := os.OpenFile(output+".processes.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for invocation := 1; invocation <= 2; invocation++ {
		name := output + ".worker-" + string(rune('0'+invocation)) + ".json"
		workerCtx, end := context.WithTimeout(ctx, 8*time.Minute)
		cmd := exec.CommandContext(workerCtx, os.Args[0], "-test.run=^TestLiveBnovoVerification$", "-test.v", "-test.timeout=8m")
		cmd.Env = append(os.Environ(), "HOTEL_PARSER_E2E_WORKER_SCHEMA="+schema, "HOTEL_PARSER_E2E_OUTPUT="+name)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			end()
			t.Fatal(err)
		}
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			end()
			t.Fatal(err)
		}
		count := 0
		sent := false
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 65536), 2<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			_, _ = log.Write(append(append([]byte(nil), line...), '\n'))
			var event map[string]any
			if json.Unmarshal(line, &event) == nil && event["msg"] == "bnovo.collect.completed" {
				count++
			}
			if invocation == 1 && count >= 12 && !sent {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Error("SIGTERM failed")
				}
				at := time.Now().UTC()
				e.SIGTERMSentAt = &at
				sent = true
			}
		}
		waitErr := cmd.Wait()
		end()
		if scanner.Err() != nil || waitErr != nil {
			t.Error("live worker process failed; see process log")
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal("worker evidence missing")
		}
		var part liveEvidence
		if err := json.Unmarshal(data, &part); err != nil {
			t.Fatal("invalid worker evidence")
		}
		probe.requests = append(probe.requests, part.Requests...)
		probe.peak = max(probe.peak, part.PeakHTTP)
		e.Samples = append(e.Samples, part.Samples...)
		e.Events = append(e.Events, part.Events...)
		e.Verification.merge(part.Verification)
		if invocation == 1 {
			e.AfterStop = part.SQL
			e.FirstWorkerInterrupted = part.Interrupted
			if !sent || !part.Interrupted {
				t.Error("actual worker interruption was not exercised")
			}
		}
		abort := false
		for _, r := range part.Requests {
			if r.HTTPStatus == 403 || r.HTTPStatus == 429 {
				abort = true
			}
		}
		if abort {
			e.ExecutionError = true
			return
		}
		if invocation == 2 && part.ExecutionError {
			e.ExecutionError = true
		}
	}
}
