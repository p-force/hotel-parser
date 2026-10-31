package bnovo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SourceOriginKey — canonical scheme/host/port, без пути, query и credentials.
func SourceOriginKey(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid source origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("invalid source scheme")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid source port")
		}
		port = strconv.Itoa(n)
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

// Одна история стартов на origin во всём процессе, включая разные Client,
// Bootstrap/Search, redirects и retry. Не является distributed rate limiter.
var httpStarts = struct {
	sync.Mutex
	defaultInterval time.Duration
	intervals       map[string]time.Duration
	last            map[string]time.Time
	changed         chan struct{}
}{intervals: map[string]time.Duration{"https://reservationsteps.ru": 500 * time.Millisecond}, last: map[string]time.Time{}, changed: make(chan struct{})}

// ConfigureStartIntervals вызывается настройкой collect до начала batch.
// История не сбрасывается: смена настроек не создаёт burst между клиентами.
func ConfigureStartIntervals(defaultInterval time.Duration, intervals map[string]time.Duration) error {
	if defaultInterval < 0 {
		return fmt.Errorf("negative source start interval")
	}
	normalized := make(map[string]time.Duration, len(intervals))
	for raw, interval := range intervals {
		key, err := SourceOriginKey(raw)
		if err != nil {
			return err
		}
		if interval < 0 {
			return fmt.Errorf("negative source start interval")
		}
		if _, exists := normalized[key]; exists {
			return fmt.Errorf("duplicate source start interval")
		}
		normalized[key] = interval
	}
	httpStarts.Lock()
	defer httpStarts.Unlock()
	httpStarts.defaultInterval = defaultInterval
	httpStarts.intervals = normalized
	close(httpStarts.changed)
	httpStarts.changed = make(chan struct{})
	return nil
}

func waitHTTPStart(ctx context.Context, origin string) error {
	for {
		httpStarts.Lock()
		if err := ctx.Err(); err != nil {
			httpStarts.Unlock()
			return err
		}
		interval, exists := httpStarts.intervals[origin]
		if !exists {
			interval = httpStarts.defaultInterval
		}
		now := time.Now()
		pause := httpStarts.last[origin].Add(interval).Sub(now)
		if interval == 0 || pause <= 0 {
			httpStarts.last[origin] = now
			httpStarts.Unlock()
			return nil
		}
		changed := httpStarts.changed
		httpStarts.Unlock()
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
		// Не резервируем будущий slot: отменённый waiter не расходует интервал.
	}
}

type startLimitedTransport struct{ base http.RoundTripper }

func (t *startLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, err := SourceOriginKey(req.URL.String())
	if err != nil {
		return nil, err
	}
	if err := waitHTTPStart(req.Context(), origin); err != nil {
		return nil, err
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	// После gate только проверка context и вызов. Ни SQL, ни observer callbacks,
	// ни сетевого ожидания под mutex здесь нет. In-flight запросы не сериализуются.
	return t.base.RoundTrip(req)
}

func (t *startLimitedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
