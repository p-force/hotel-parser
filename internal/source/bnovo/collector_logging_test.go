package bnovo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func captureCollectionLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func readCollectionLogs(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func TestCollectStructuredLogging(t *testing.T) {
	const secret = "private@example.test token=secret-cookie-value"
	const offer = `<div class="plan_row" data-plan-id="1"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="4910.50"></td>
<td><select class="selectAvailable" data-room-id="561429" data-real-room-id="561429" data-plan-id="1"><option value="1">1</option></select></td>
</tr></table></div>`
	for _, tc := range []struct {
		name             string
		httpStatus       int
		content          string
		status           SearchStatus
		level, errorCode string
		parse            bool
	}{
		{"success", 200, offer, SearchStatusSuccess, "INFO", "", true},
		{"partial", 200, `<div class="plan_row" data-plan-id="1"></div>`, SearchStatusPartial, "WARN", "", true},
		{"no_offers", 200, `<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>`, SearchStatusNoOffers, "INFO", "", true},
		{"forbidden", 403, secret, SearchStatusFailed, "ERROR", "http_error", false},
		{"rate_limited", 429, secret, SearchStatusFailed, "ERROR", "http_error", false},
		{"parse_failed", 200, "error page", SearchStatusFailed, "ERROR", "parse_error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureCollectionLogs(t)
			body := strings.ReplaceAll(collectorHTML(tc.content), "Stadium", secret)
			if tc.errorCode == "parse_error" {
				body = "<h1>" + secret + "</h1>"
			}
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Set-Cookie", "session=secret-cookie-value")
				w.WriteHeader(tc.httpStatus)
				fmt.Fprint(w, body)
			})
			req := testSearchRequest()
			result := client.Collect(context.Background(), req)
			if result.Status != tc.status {
				t.Fatalf("неверный статус: %+v", result)
			}
			// Логирование не добавляет поля и не меняет решения парсера.
			if tc.errorCode == "" {
				expected, err := Parse([]byte(body), req)
				if err != nil {
					t.Fatal(err)
				}
				expected.HTTPStatus, expected.CollectedAt = result.HTTPStatus, result.CollectedAt
				if !reflect.DeepEqual(result, expected) {
					t.Error("логирование изменило результат Parse")
				}
			}
			events := readCollectionLogs(t, buf)
			wantNames := []string{"bnovo.collect.started", "bnovo.http.completed", "bnovo.collect.completed"}
			if tc.parse {
				wantNames = []string{"bnovo.collect.started", "bnovo.http.completed", "bnovo.parse.completed", "bnovo.collect.completed"}
			}
			if len(events) != len(wantNames) {
				t.Fatalf("неверная цепочка событий: %s", buf.String())
			}
			attempt, ok := events[0]["attempt_id"].(string)
			if !ok || attempt == "" {
				t.Fatal("нет attempt_id")
			}
			for i, event := range events {
				if event["msg"] != wantNames[i] || event["attempt_id"] != attempt {
					t.Errorf("нарушена корреляция/очерёдность: %v", event)
				}
				for key, want := range map[string]any{"property_id": req.PropertyID, "check_in": "2026-10-26", "check_out": "2026-10-28", "los": float64(2), "adults": float64(2)} {
					if event[key] != want {
						t.Errorf("поле %s = %v, ожидалось %v", key, event[key], want)
					}
				}
				wantLevel := "INFO"
				if event["msg"] == "bnovo.http.completed" && !tc.parse {
					wantLevel = "ERROR"
				}
				if event["msg"] == "bnovo.parse.completed" || event["msg"] == "bnovo.collect.completed" {
					wantLevel = tc.level
				}
				if event["level"] != wantLevel {
					t.Errorf("неверный уровень события: %v", event)
				}
			}
			final := events[len(events)-1]
			for key, want := range map[string]any{"status": string(tc.status), "http_status": float64(tc.httpStatus), "categories_count": float64(len(result.Categories)), "offers_count": float64(len(result.Offers)), "warnings_count": float64(len(result.Warnings)), "error_code": tc.errorCode} {
				if final[key] != want {
					t.Errorf("итоговое поле %s = %v, ожидалось %v", key, final[key], want)
				}
			}
			if _, ok := final["duration_ms"].(float64); !ok {
				t.Error("нет длительности попытки")
			}
			for _, forbidden := range []string{secret, "secret-cookie-value", "<html>", "<h1>", client.baseURL.String(), "4910.50"} {
				if strings.Contains(buf.String(), forbidden) {
					t.Errorf("в логи попали данные источника: %q", forbidden)
				}
			}
		})
	}
}

func TestCollectLoggingInvalidRequest(t *testing.T) {
	buf := captureCollectionLogs(t)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("некорректный запрос отправлен")
	})
	req := testSearchRequest()
	req.PropertyID = "https://private.test/?token=secret&email=private@example.test"
	result := client.Collect(context.Background(), req)
	events := readCollectionLogs(t, buf)
	if result.Status != SearchStatusFailed || result.Error.Code != "invalid_request" {
		t.Fatalf("неверный результат: %+v", result)
	}
	if strings.Contains(buf.String(), "secret") || strings.Contains(buf.String(), "private") {
		t.Error("в логи попал некорректный UID")
	}
	if events[len(events)-1]["error_code"] != "invalid_request" {
		t.Error("нет кода ошибки")
	}
}

func TestCollectLoggingAttemptIDs(t *testing.T) {
	buf := captureCollectionLogs(t)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	for range 2 {
		client.Collect(context.Background(), testSearchRequest())
	}
	events := readCollectionLogs(t, buf)
	if len(events) != 6 {
		t.Fatalf("неверное число событий: %s", buf.String())
	}
	if events[0]["attempt_id"] == events[3]["attempt_id"] {
		t.Error("разные попытки получили одинаковый ID")
	}
}

func TestCalendarLOS(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []time.Time{time.Date(2026, 3, 28, 0, 0, 0, 0, loc), time.Date(2026, 10, 24, 0, 0, 0, 0, loc)} {
		req := SearchRequest{CheckIn: day, CheckOut: day.AddDate(0, 0, 2)}
		if got := calendarLOS(req); got != 2 {
			t.Errorf("LOS = %d, ожидалось 2 для %v", got, day)
		}
	}
	if calendarLOS(SearchRequest{}) != 0 {
		t.Error("для отсутствующих дат ожидается LOS=0")
	}
}

func TestCollectLoggingCanceledContext(t *testing.T) {
	buf := captureCollectionLogs(t)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("отменённый запрос отправлен")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := client.Collect(ctx, testSearchRequest())
	events := readCollectionLogs(t, buf)
	if result.Status != SearchStatusFailed || result.Error.Code != "canceled" {
		t.Fatalf("неверный результат: %+v", result)
	}
	if len(events) != 3 {
		t.Fatalf("неверная цепочка: %s", buf.String())
	}
	final := events[len(events)-1]
	if final["level"] != "ERROR" || final["error_code"] != "canceled" || final["http_status"] != float64(0) {
		t.Errorf("неверная диагностика отмены: %v", final)
	}
}
