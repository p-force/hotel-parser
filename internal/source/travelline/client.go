// Package travelline собирает публичные предложения TravelLine BookingForm.
package travelline

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
)

const PublicOrigin = "https://ru-ibe.tlintegration.ru"
const infoPath = "/ApiWebDistribution/BookingForm/hotel_info"
const availabilityPath = "/ApiWebDistribution/BookingForm/hotel_availability"
const DefaultStartInterval = time.Second
const DefaultConcurrency = 2
const collectionTimeout = 20 * time.Second
const maxBodyBytes = 4 << 20

var errRedirect = errors.New("TravelLine redirects are not supported")

type Client struct {
	origin      string
	http        *http.Client
	publicRates []string
}

func NewClient(baseURL string, original *http.Client, publicRates []string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(publicRates) == 0 {
		return nil, errors.New("invalid TravelLine source configuration")
	}
	origin, err := source.OriginKey(baseURL)
	if err != nil {
		return nil, errors.New("invalid TravelLine origin")
	}
	if original == nil {
		original = &http.Client{}
	}
	client := *original
	client.Jar = nil // Публичный поиск не использует cookies переданного клиента.
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &limitedTransport{base: base}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return errRedirect
	}
	return &Client{origin: origin, http: &client, publicRates: append([]string(nil), publicRates...)}, nil
}
func (c *Client) SourceOrigin() string { return c.origin }

func (c *Client) Collect(parent context.Context, req source.SearchRequest) (result source.SearchResult) {
	started := time.Now()
	httpDuration := time.Duration(0)
	parseDuration := time.Duration(0)
	httpAttempted, parseAttempted := false, false
	result = source.SearchResult{Source: "travelline", Request: req, Status: source.Failed, CollectedAt: started.UTC()}
	safePropertyID := req.PropertyID
	if !validPropertyID(safePropertyID) {
		safePropertyID = ""
	}
	log := slog.Default().With("attempt_id", rand.Text())
	defer func() {
		level := slog.LevelInfo
		if result.Status == source.Partial || len(result.Warnings) > 0 {
			level = slog.LevelWarn
		}
		if result.Error != nil {
			level = slog.LevelError
		}
		attrs := []any{"source", "travelline", "property_id", safePropertyID, "check_in", req.CheckIn.Format("2006-01-02"), "check_out", req.CheckOut.Format("2006-01-02"), "adults", req.Adults, "status", result.Status, "http_status", result.HTTPStatus, "categories_count", len(result.Categories), "offers_count", len(result.Offers), "warnings_count", len(result.Warnings), "collection_duration_ms", time.Since(started).Milliseconds()}
		if httpAttempted {
			attrs = append(attrs, "http_duration_ms", httpDuration.Milliseconds())
		}
		if parseAttempted {
			attrs = append(attrs, "parse_duration_ms", parseDuration.Milliseconds())
		}
		if result.Error != nil {
			attrs = append(attrs, "error_code", result.Error.Code)
		}
		log.Log(parent, level, "travelline.collect.completed", attrs...)
	}()
	if !validPropertyID(req.PropertyID) || req.Adults < 1 || req.Adults > 2 || len(req.ChildrenAges) != 0 || req.Language != "ru" || req.CheckIn.IsZero() || req.CheckOut.IsZero() || req.CheckIn.Format("2006-01-02") >= req.CheckOut.Format("2006-01-02") {
		result.Error = &source.CollectionError{Code: "invalid_request", Message: "unsupported TravelLine search scenario"}
		return result
	}
	ctx, cancel := context.WithTimeout(parent, collectionTimeout)
	defer cancel()
	log.DebugContext(ctx, "travelline.collect.started")
	query := url.Values{"hotels[0].code": {req.PropertyID}, "language": {"ru-ru"}, "audience": {"BookingForm"}}
	httpStarted := time.Now()
	httpAttempted = true
	info, status, err := c.get(ctx, infoPath, query)
	result.HTTPStatus = status
	if err == nil {
		var header metadata
		if json.Unmarshal(info, &header) != nil || len(header.Hotels) != 1 || header.Hotels[0].Code != req.PropertyID {
			err = &source.CollectionError{Code: "parse_error", Message: "TravelLine hotel identity is invalid"}
		}
		if len(header.Errors) > 0 {
			err = &source.CollectionError{Code: "source_error", Message: "TravelLine rejected hotel metadata request"}
		}
	}
	if err == nil {
		query = url.Values{"criterions[0].hotels[0].code": {req.PropertyID}, "criterions[0].adults": {strconv.Itoa(req.Adults)}, "criterions[0].dates": {req.CheckIn.Format("2006-01-02") + ";" + req.CheckOut.Format("2006-01-02")}, "include_all_placements": {"false"}, "include_promo_restricted": {"true"}, "include_rates": {"true"}, "include_transfers": {"true"}, "language": {"ru-ru"}, "audience": {"BookingForm"}}
		var body []byte
		body, status, err = c.get(ctx, availabilityPath, query)
		result.HTTPStatus = status
		httpDuration = time.Since(httpStarted)
		if err == nil {
			parsedAt := time.Now()
			parseAttempted = true
			result, err = Parse(info, body, req, c.publicRates)
			parseDuration = time.Since(parsedAt)
			result.CollectedAt = started.UTC()
			result.HTTPStatus = status
		}
	} else {
		httpDuration = time.Since(httpStarted)
	}
	if err != nil {
		result.Status = source.Failed
		var classified *source.CollectionError
		if errors.As(err, &classified) {
			copy := *classified
			copy.HTTPStatus = result.HTTPStatus
			result.Error = &copy
		} else {
			result.Error = &source.CollectionError{Code: "parse_error", Message: "invalid TravelLine response", HTTPStatus: result.HTTPStatus}
		}
	}
	return result
}

func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, 0, &source.CollectionError{Code: "invalid_request", Message: "invalid TravelLine request"}
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, 0, requestError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		retry := response.StatusCode == 429 || response.StatusCode == 500 || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504
		return nil, response.StatusCode, &source.CollectionError{Code: "http_error", Message: "TravelLine HTTP request failed", HTTPStatus: response.StatusCode, Retryable: retry, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return nil, response.StatusCode, requestError(ctx, err)
	}
	if len(body) > maxBodyBytes {
		return nil, response.StatusCode, &source.CollectionError{Code: "response_too_large", Message: "TravelLine response exceeds limit"}
	}
	if !json.Valid(body) {
		return nil, response.StatusCode, &source.CollectionError{Code: "parse_error", Message: "TravelLine response is not JSON"}
	}
	return body, response.StatusCode, nil
}
func requestError(ctx context.Context, err error) *source.CollectionError {
	code := "transport_error"
	retry := true
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		code = "timeout"
	}
	if errors.Is(err, errRedirect) {
		return &source.CollectionError{Code: "redirect_blocked", Message: "TravelLine redirect rejected"}
	}

	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		code = "canceled"
		retry = false
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = "timeout"
	}
	return &source.CollectionError{Code: code, Message: "TravelLine request did not complete", Retryable: retry}
}
func retryAfter(raw string) time.Duration {
	if seconds, err := strconv.ParseInt(raw, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(raw); err == nil {
		if delay := time.Until(date); delay > 0 {
			return delay
		}
	}
	return 0
}

// Общая история фактических стартов всех TravelLine-клиентов процесса.
// 1 с и concurrency=2 — консервативная политика приложения, не SLA поставщика.
var starts = struct {
	sync.Mutex
	last      map[string]time.Time
	intervals map[string]time.Duration
	changed   chan struct{}
}{last: map[string]time.Time{}, intervals: map[string]time.Duration{PublicOrigin: DefaultStartInterval}, changed: make(chan struct{})}

func ConfigureStartIntervals(defaultInterval time.Duration, intervals map[string]time.Duration) {
	starts.Lock()
	defer starts.Unlock()
	copy := map[string]time.Duration{PublicOrigin: DefaultStartInterval}
	for origin, interval := range intervals {
		copy[origin] = interval
	}
	// Непубличные тестовые origins по умолчанию не замедляются; общая fallback
	// политика collect передаётся в отдельной записи и применяется к ним тоже.
	copy[""] = defaultInterval
	starts.intervals = copy
	close(starts.changed)
	starts.changed = make(chan struct{})
}

type limitedTransport struct{ base http.RoundTripper }

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, err := source.OriginKey(req.URL.String())
	if err != nil {
		return nil, err
	}
	for {
		starts.Lock()
		if err := req.Context().Err(); err != nil {
			starts.Unlock()
			return nil, err
		}
		interval, ok := starts.intervals[origin]
		if !ok {
			interval = starts.intervals[""]
		}
		now := time.Now()
		delay := starts.last[origin].Add(interval).Sub(now)
		if delay <= 0 {
			starts.last[origin] = now
			starts.Unlock()
			break
		}
		changed := starts.changed
		starts.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}
func (t *limitedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (c *Client) SourceType() string { return "travelline" }

func validPropertyID(id string) bool {
	if id == "" || len(id) > 16 {
		return false
	}
	for _, digit := range id {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
