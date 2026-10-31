package bnovo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type startProbeTransport func(*http.Request) (*http.Response, error)

func (f startProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func probeResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}
}
func probeReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(4 * time.Second):
		t.Fatal("transport probe timed out")
		var zero T
		return zero
	}
}

func TestHTTPStartsSharedAcrossClientsAndOriginAliases(t *testing.T) {
	// No ConfigureStartIntervals: the public source's automatic default is 500ms.
	starts := make(chan time.Time, 3)
	release := make(chan struct{})
	defer close(release)
	var active, peak atomic.Int32
	base := startProbeTransport(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		starts <- time.Now()
		select {
		case <-release:
			return probeResponse(r), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var callers sync.WaitGroup
	done := make(chan error, 3)
	original := &http.Client{Transport: base}
	for _, origin := range []string{"https://reservationsteps.ru", "https://RESERVATIONSTEPS.RU:443", "https://reservationsteps.ru.:0443"} {
		client, err := NewClient(origin, original)
		if err != nil {
			t.Fatal(err)
		}
		callers.Add(1)
		go func() { defer callers.Done(); _, err := client.Search(ctx, testSearchRequest()); done <- err }()
	}
	// All three transports must enter while the previous calls are still active.
	times := []time.Time{probeReceive(t, starts), probeReceive(t, starts), probeReceive(t, starts)}
	t.Logf("RoundTrip start=%s", times[0].UTC().Format(time.RFC3339Nano))
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		t.Logf("RoundTrip start=%s gap=%v", times[i].UTC().Format(time.RFC3339Nano), gap)
		if gap < 500*time.Millisecond {
			t.Errorf("gap=%v <500ms", gap)
		}
	}
	if peak.Load() != 3 {
		t.Errorf("in-flight calls serialized: peak=%d", peak.Load())
	}
	if _, wrapped := original.Transport.(*startLimitedTransport); wrapped {
		t.Error("caller transport modified")
	}
	cancel()
	callers.Wait()
	for range 3 {
		if err := probeReceive(t, done); err == nil {
			t.Error("blocked call ignored cancellation")
		}
	}
}

// Observe entry into the limiter's cancellable select without sleeps or hooks
// between its permit and the base transport call.
type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestHTTPStartWaitCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			origin := fmt.Sprintf("https://%s-%d.start-test.invalid", name, time.Now().UnixNano())
			if err := ConfigureStartIntervals(0, map[string]time.Duration{origin: time.Hour}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := ConfigureStartIntervals(0, map[string]time.Duration{"https://reservationsteps.ru": 500 * time.Millisecond}); err != nil {
					t.Error(err)
				}
			})
			var calls atomic.Int32
			transport := &startLimitedTransport{base: startProbeTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return probeResponse(r), nil })}
			request, _ := http.NewRequest(http.MethodGet, origin, nil)
			first, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			first.Body.Close()
			httpStarts.Lock()
			previous := httpStarts.last[origin]
			httpStarts.Unlock()
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			observed := &waitingContext{Context: ctx, waiting: make(chan struct{})}
			done := make(chan error, 1)
			go func() { _, err := transport.RoundTrip(request.WithContext(observed)); done <- err }()
			probeReceive(t, observed.waiting)
			if !deadline {
				cancel()
			}
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			if err := probeReceive(t, done); !errors.Is(err, want) {
				t.Errorf("error=%v want=%v", err, want)
			}
			httpStarts.Lock()
			current := httpStarts.last[origin]
			httpStarts.Unlock()
			if calls.Load() != 1 || !current.Equal(previous) {
				t.Error("canceled waiter started HTTP or consumed next interval")
			}
			canceled, cancelNow := context.WithCancel(context.Background())
			cancelNow()
			if _, err := transport.RoundTrip(request.WithContext(canceled)); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Error("pre-canceled context started HTTP")
			}
		})
	}
}

func TestHTTPStartGateIncludesRedirectsAndIndependentOrigins(t *testing.T) {
	const origin = "https://redirect.start-test.invalid"
	if err := ConfigureStartIntervals(0, map[string]time.Duration{origin: 500 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ConfigureStartIntervals(0, map[string]time.Duration{"https://reservationsteps.ru": 500 * time.Millisecond}); err != nil {
			t.Error(err)
		}
	})
	starts := make(chan time.Time, 2)
	base := startProbeTransport(func(r *http.Request) (*http.Response, error) {
		response := probeResponse(r)
		if r.URL.Host == "redirect.start-test.invalid" {
			starts <- time.Now()
			if r.URL.Path != "/redirected" {
				response.StatusCode = 302
				response.Header.Set("Location", "/redirected")
			}
		}
		return response, nil
	})
	client, err := NewClient(origin, &http.Client{Transport: base})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := client.Search(context.Background(), testSearchRequest()); done <- err }()
	first := probeReceive(t, starts)
	// A different source has no dependency on this origin's blocked redirect.
	other := &startLimitedTransport{base: base}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://other.start-test.invalid", nil)
	response, err := other.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	second := probeReceive(t, starts)
	t.Logf("redirect starts=%s, %s gap=%v", first.UTC().Format(time.RFC3339Nano), second.UTC().Format(time.RFC3339Nano), second.Sub(first))
	if gap := second.Sub(first); gap < 500*time.Millisecond {
		t.Errorf("redirect bypassed gate: %v", gap)
	}
	if err := probeReceive(t, done); err != nil {
		t.Fatal(err)
	}
}
