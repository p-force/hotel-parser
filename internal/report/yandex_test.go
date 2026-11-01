package report

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type diskMock struct {
	mu          sync.Mutex
	files       map[string][]byte
	dirs        map[string]bool
	puts, links int
	status      int
	failStage   string
	href        string
	disconnect  bool
}

func (m *diskMock) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := r.URL.Query().Get("path")
	if r.URL.Path == "/upload-target" {
		if r.Header.Get("Authorization") != "" {
			panic("OAuth leaked to upload")
		}
		m.puts++
		if m.failStage == "put" {
			w.Header().Set("Retry-After", "123")
			w.WriteHeader(m.status)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		m.files[r.URL.Query().Get("target")] = b
		if m.disconnect {
			h := w.(http.Hijacker)
			c, _, _ := h.Hijack()
			c.Close()
			return
		}
		w.WriteHeader(201)
		return
	}
	if r.Header.Get("Authorization") != "OAuth mock-only-token" {
		panic("missing API OAuth")
	}
	stage := "metadata"
	if r.Method == "PUT" {
		stage = "mkdir"
	}
	if strings.HasSuffix(r.URL.Path, "/upload") {
		stage = "link"
	}
	if m.failStage == stage {
		w.Header().Set("Retry-After", "123")
		w.WriteHeader(m.status)
		return
	}
	if stage == "mkdir" {
		if m.dirs[p] {
			w.WriteHeader(409)
		} else {
			m.dirs[p] = true
			w.WriteHeader(201)
		}
		return
	}
	if stage == "link" {
		m.links++
		href := m.href
		if href == "" {
			href = "https://uploader1.dst.yandex.net/upload-target?target=" + url.QueryEscape(p)
		}
		json.NewEncoder(w).Encode(map[string]any{"href": href, "method": "PUT"})
		return
	}
	if m.dirs[p] {
		fmt.Fprint(w, `{"type":"dir"}`)
		return
	}
	b, ok := m.files[p]
	if !ok {
		w.WriteHeader(404)
		return
	}
	hash := md5.Sum(b)
	json.NewEncoder(w).Encode(map[string]any{"type": "file", "size": len(b), "md5": hex.EncodeToString(hash[:])})
}
func mockUploader(t *testing.T, m *diskMock) *YandexDiskUploader {
	t.Helper()
	t.Setenv("YANDEX_DISK_TOKEN", "mock-only-token")
	m.files = map[string][]byte{}
	m.dirs = map[string]bool{}
	server := httptest.NewTLSServer(http.HandlerFunc(m.serve))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		q := r.Clone(r.Context())
		q.URL = new(url.URL)
		*q.URL = *r.URL
		q.URL.Host = endpoint.Host
		return transport.RoundTrip(q)
	})}
	return NewYandexDiskUploader(client)
}
func csvContent(content string) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := io.WriteString(w, content)
		return err
	}
}
func TestYandexUploadAndConfirmation(t *testing.T) {
	m := &diskMock{}
	u := mockUploader(t, m)
	ctx := context.Background()
	path := "/reports with spaces/a&b/run.csv"
	csv := "known,0,100.50,RUB\nunknown,,,\n"
	// Проверка уже существующих каталогов (409 -> GET type=dir).
	m.dirs["/reports with spaces"] = true
	if err := u.upload(ctx, path, csvContent(csv)); err != nil {
		t.Fatal(err)
	}
	if m.puts != 1 || !bytes.Equal(m.files[path], []byte(csv)) || !m.dirs["/reports with spaces/a&b"] {
		t.Fatal(m)
	}
	if err := u.upload(ctx, path, csvContent(csv)); err != nil || m.puts != 1 {
		t.Fatal("duplicate PUT", err, m.puts)
	}
	if err := u.upload(ctx, path, csvContent("different")); err == nil || m.puts != 1 {
		t.Fatal("foreign file overwritten", err)
	}
}
func TestYandexUncertainPUTRecovery(t *testing.T) {
	m := &diskMock{disconnect: true}
	u := mockUploader(t, m)
	if err := u.upload(context.Background(), "/root/run.csv", csvContent("x,y\n")); err != nil {
		t.Fatal(err)
	}
	if err := u.upload(context.Background(), "/root/run.csv", csvContent("x,y\n")); err != nil || m.puts != 1 {
		t.Fatal(err, m.puts)
	}
}
func TestYandexErrors(t *testing.T) {
	for _, stage := range []string{"metadata", "mkdir", "link", "put"} {
		for _, status := range []int{401, 403, 429, 500, 503} {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				m := &diskMock{failStage: stage, status: status}
				u := mockUploader(t, m)
				err := u.upload(context.Background(), "/root/run.csv", csvContent("csv"))
				var e *diskError
				if !errors.As(err, &e) || e.code != fmt.Sprintf("disk_http_%d", status) || e.retryAfter != 123*time.Second || e.permanent != (status == 401 || status == 403) {
					t.Fatal(err)
				}
				if strings.Contains(err.Error(), "mock-only-token") {
					t.Fatal("token leak")
				}
			})
		}
	}
}
func TestYandexHrefAndRedirectRejected(t *testing.T) {
	for _, href := range []string{"http://uploader1.dst.yandex.net/a", "https://evil.test/a", "https://uploader1.dst.yandex.net.evil.test/a", "https://user:pass@uploader1.dst.yandex.net/a", "https://uploader1.dst.yandex.net:444/a"} {
		t.Run(href, func(t *testing.T) {
			m := &diskMock{href: href}
			u := mockUploader(t, m)
			err := u.upload(context.Background(), "/root/run.csv", csvContent("csv"))
			if err == nil || err.Error() != "disk_untrusted_href" || m.puts != 0 {
				t.Fatal(err)
			}
		})
	}
	t.Run("redirect", func(t *testing.T) {
		var calls int
		t.Setenv("YANDEX_DISK_TOKEN", "mock-only-token")
		u := NewYandexDiskUploader(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.test/"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})})
		err := u.upload(context.Background(), "/run.csv", csvContent("csv"))
		if err == nil || calls != 1 {
			t.Fatal(err, calls)
		}
	})
}
func TestYandexCancellationAndEarlyResponse(t *testing.T) {
	t.Setenv("YANDEX_DISK_TOKEN", "mock-only-token")
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			u := NewYandexDiskUploader(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if err := u.upload(ctx, "/run.csv", csvContent("csv")); err == nil {
				t.Fatal("expected cancellation")
			}
		})
	}
	// 403 до чтения тела не должен оставлять producer на заблокированном Pipe.Write.
	m := &diskMock{failStage: "put", status: 403}
	u := mockUploader(t, m)
	done := make(chan error, 1)
	go func() {
		done <- u.upload(context.Background(), "/root/run.csv", csvContent(strings.Repeat("x", 2<<20)))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected 403")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("producer leak")
	}
}
func TestRetryAfter(t *testing.T) {
	if retryAfter("10") != 10*time.Second || retryAfter("bad") != 0 || retryAfter("-1") != 0 {
		t.Fatal("seconds")
	}
	d := retryAfter(time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	if d < 59*time.Minute || d > time.Hour {
		t.Fatal(d)
	}
}

func TestYandexCancelActiveStreamingPUT(t *testing.T) {
	t.Setenv("YANDEX_DISK_TOKEN", "mock-only-token")
	entered := make(chan struct{})
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		status, body := 200, `{}`
		switch {
		case r.URL.Host == "uploader1.dst.yandex.net":
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		case strings.HasSuffix(r.URL.Path, "/upload"):
			body = `{"href":"https://uploader1.dst.yandex.net/upload","method":"PUT"}`
		case r.Method == "PUT":
			status = 201
		default:
			status = 404
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	u := NewYandexDiskUploader(&http.Client{Transport: transport})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- u.upload(ctx, "/root/run.csv", csvContent(strings.Repeat("x", 2<<20))) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("PUT not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled PUT accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("stream leaked after cancellation")
	}
}

func TestYandexInvalidTokenNeverSent(t *testing.T) {
	t.Setenv("YANDEX_DISK_TOKEN", "invalid\ntoken")
	u := NewYandexDiskUploader(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid token sent")
		return nil, io.ErrUnexpectedEOF
	})})
	err := u.upload(context.Background(), "/run.csv", csvContent("csv"))
	var diskErr *diskError
	if !errors.As(err, &diskErr) || diskErr.code != "disk_token_invalid" || !diskErr.permanent {
		t.Fatal(err)
	}
}
