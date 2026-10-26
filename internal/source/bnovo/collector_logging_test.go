package bnovo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	appLogger "github.com/p-force/hotel-parser/internal/logger"
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
				wantLevel := "DEBUG"
				if event["msg"] == "bnovo.collect.completed" {
					wantLevel = tc.level
				}
				if event["level"] != wantLevel {
					t.Errorf("неверный уровень события: %v", event)
				}
			}
			final := events[len(events)-1]
			assertCollectionTiming(t, events, result, true, tc.parse)
			for key, want := range map[string]any{"status": string(tc.status), "http_status": float64(tc.httpStatus), "categories_count": float64(len(result.Categories)), "offers_count": float64(len(result.Offers)), "warnings_count": float64(len(result.Warnings))} {
				if final[key] != want {
					t.Errorf("итоговое поле %s = %v, ожидалось %v", key, final[key], want)
				}
			}
			for key, want := range map[string]any{"property_id": req.PropertyID, "check_in": "2026-10-26", "check_out": "2026-10-28", "los": float64(2), "adults": float64(2)} {
				if final[key] != want {
					t.Errorf("итоговое поле %s = %v, ожидалось %v", key, final[key], want)
				}
			}
			if tc.errorCode == "" {
				if _, exists := final["error_code"]; exists {
					t.Error("пустой error_code не должен выводиться")
				}
			} else if final["error_code"] != tc.errorCode {
				t.Errorf("неверный error_code: %v", final)
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

// Проверяем заполненность, границы этапов и отсутствие неприменимых метрик.
func assertCollectionTiming(t *testing.T, events []map[string]any, result SearchResult, wantHTTP, wantParse bool) {
	t.Helper()
	final := events[len(events)-1]
	total, ok := final["collection_duration_ms"].(float64)
	if !ok || total < 0 {
		t.Fatalf("нет корректного времени попытки: %v", final)
	}

	var stages float64
	for key, want := range map[string]bool{"http_duration_ms": wantHTTP, "parse_duration_ms": wantParse} {
		value, exists := final[key]
		if exists != want {
			t.Errorf("поле %s присутствует=%t, ожидалось %t", key, exists, want)
			continue
		}
		if !exists {
			continue
		}
		duration, ok := value.(float64)
		if !ok || duration < 0 {
			t.Errorf("неверная метрика %s=%v", key, value)
			continue
		}
		stages += duration
		eventName := "bnovo.http.completed"
		if key == "parse_duration_ms" {
			eventName = "bnovo.parse.completed"
		}
		found := false
		for _, event := range events {
			if event["msg"] == eventName {
				found = true
				if event[key] != value {
					t.Errorf("метрика %s измерена повторно: %v, итог: %v", key, event, final)
				}
			}
		}
		if !found {
			t.Errorf("нет события этапа %s", key)
		}
	}
	if total < stages {
		t.Errorf("время попытки %v меньше суммы этапов %v", total, stages)
	}
	for _, event := range events {
		if _, exists := event["duration_ms"]; exists {
			t.Error("универсальный duration_ms не должен выводиться")
		}
		if event["msg"] == "bnovo.collect.completed" {
			stamp, ok := event["collected_at"].(string)
			if !ok {
				t.Fatalf("нет UTC начала попытки: %v", event)
			}
			parsed, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil || !parsed.Equal(result.CollectedAt) || !strings.HasSuffix(stamp, "Z") {
				t.Errorf("неверное начало попытки: %q", stamp)
			}
		} else {
			for _, key := range []string{"collected_at", "property_id", "check_in", "check_out", "los", "adults"} {
				if _, exists := event[key]; exists {
					t.Errorf("контекст %s повторяется на промежуточном этапе", key)
				}
			}
		}
		if code, exists := event["error_code"]; exists && code == "" {
			t.Error("пустой error_code")
		}
		if _, exists := event["queue_wait_ms"]; exists {
			t.Error("метрика очереди появилась без очереди")
		}
		if event["msg"] != "bnovo.collect.completed" {
			if _, exists := event["collection_duration_ms"]; exists {
				t.Error("полное время записано до завершения")
			}
		}
	}
}

func TestCollectTimingWithoutHTTPRequest(t *testing.T) {
	for _, reason := range []string{"invalid_request", "canceled", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			buf := captureCollectionLogs(t)
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("HTTP-запрос не должен отправляться")
			})
			req := testSearchRequest()
			ctx := context.Background()
			if reason == "invalid_request" {
				req.Adults = 0
			}
			if reason == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if reason == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			started := time.Now()
			result := client.Collect(ctx, req)
			if result.Status != SearchStatusFailed {
				t.Fatalf("неверный результат: %+v", result)
			}
			assertCollectedAt(t, result, started)
			events := readCollectionLogs(t, buf)
			assertCollectionTiming(t, events, result, false, false)
			for _, event := range events {
				if event["msg"] == "bnovo.http.completed" {
					if _, exists := event["duration_ms"]; exists {
						t.Error("неотправленный запрос получил HTTP-duration")
					}
				}
				if event["msg"] == "bnovo.parse.completed" {
					t.Error("парсинг не должен начинаться")
				}
			}
		})
	}
}

func TestCollectTimingHTTPTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			buf := captureCollectionLogs(t)
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if phase == "body" {
					fmt.Fprint(w, "partial body")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})
			client.timeout = 100 * time.Millisecond
			started := time.Now()
			result := client.Collect(context.Background(), testSearchRequest())
			elapsed := time.Since(started)
			if result.Status != SearchStatusFailed || result.Error.Code != "timeout" {
				t.Fatalf("неверный результат: %+v", result)
			}
			events := readCollectionLogs(t, buf)
			assertCollectionTiming(t, events, result, true, false)
			final := events[len(events)-1]
			httpMS := final["http_duration_ms"].(float64)
			if httpMS < 80 || httpMS > float64(elapsed.Milliseconds()+1) {
				t.Errorf("HTTP-duration %v не отражает таймаут: фактически %v", httpMS, elapsed)
			}
			wantStatus := float64(0)
			if phase == "body" {
				wantStatus = 200
			}
			if final["http_status"] != wantStatus {
				t.Errorf("неверный HTTP-статус: %v", final)
			}
		})
	}
}

func TestCollectInfoHasOnlyFinalRecord(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, tc := range []struct {
			name, content string
			httpStatus    int
			level         string
		}{
			{"no_offers", `<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>`, 200, "INFO"},
			{"partial", `<div class="plan_row" data-plan-id="1"></div>`, 200, "WARN"},
			{"failed", "error", 403, "ERROR"},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				var buf bytes.Buffer
				previous := slog.Default()
				if format == "json" {
					slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
				} else {
					slog.SetDefault(appLogger.New(&buf, time.UTC))
				}
				t.Cleanup(func() { slog.SetDefault(previous) })
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.httpStatus)
					fmt.Fprint(w, collectorHTML(tc.content))
				})
				result := client.Collect(context.Background(), testSearchRequest())
				if strings.Count(buf.String(), "\n") != 1 {
					t.Fatalf("на INFO ожидается одна строка: %s", buf.String())
				}
				for _, forbidden := range []string{"bnovo.collect.started", "bnovo.http.completed", "bnovo.parse.completed", " duration_ms=", `"duration_ms":`} {
					if strings.Contains(buf.String(), forbidden) {
						t.Errorf("лишние подробности на INFO: %s", buf.String())
					}
				}
				if format == "text" {
					if !strings.HasPrefix(buf.String(), "["+tc.level+"] ") {
						t.Errorf("формат PrettyHandler изменился: %s", buf.String())
					}
					if result.Error == nil && strings.Contains(buf.String(), "error_code=") {
						t.Error("выведен пустой error_code")
					}
				} else {
					events := readCollectionLogs(t, &buf)
					final := events[0]
					if final["level"] != tc.level || final["msg"] != "bnovo.collect.completed" {
						t.Errorf("неверный итог: %v", final)
					}
					keys := []string{"time", "level", "msg", "attempt_id", "property_id", "check_in", "check_out", "los", "adults", "collected_at", "collection_duration_ms", "http_duration_ms", "status", "http_status", "categories_count", "offers_count", "warnings_count"}
					if result.Error == nil {
						keys = append(keys, "parse_duration_ms")
					} else {
						keys = append(keys, "error_code")
					}
					if len(final) != len(keys) {
						t.Errorf("неверный набор полей JSON: %v", final)
					}
					for _, key := range keys {
						if _, exists := final[key]; !exists {
							t.Errorf("в JSON нет поля %s", key)
						}
					}
					if result.Error == nil {
						if _, exists := final["error_code"]; exists {
							t.Error("в JSON выведен пустой error_code")
						}
					} else if final["error_code"] != result.Error.Code {
						t.Error("потерян код ошибки")
					}
				}
			})
		}
	}
}
