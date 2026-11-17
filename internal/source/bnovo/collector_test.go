package bnovo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func collectorHTML(content string) string {
	return `<html><head><script>
const $enabledRoomTypes = {"561429":{"id":561429,"name":"Stadium"}};
const $plansForJs = {"1":{"id":1,"name":"Основной","roomTypeIds":["561429"]}};
const account_currency_iso4217 = "RUB";
</script></head><body><div class="room" data-roomtype-id="561429">` + content + `</div></body></html>`
}

func TestCollectParsedResult(t *testing.T) {
	const offer = `<div class="plan_row" data-plan-id="1"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="4910.50"></td>
<td><select class="selectAvailable" data-room-id="561429" data-real-room-id="561429" data-plan-id="1"><option value="1">1</option></select></td>
</tr></table></div>`
	for _, tc := range []struct {
		name, content string
		status        SearchStatus
	}{
		{"success", offer, SearchStatusSuccess},
		{"partial", offer, SearchStatusPartial},
		{"no_offers", `<div class="tariff__nodates">Здесь нет свободных мест с 26 октября 2026 по 28 октября 2026</div>`, SearchStatusNoOffers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := collectorHTML(tc.content)
			if tc.status == SearchStatusPartial {
				// Один видимый тариф и вторая категория с неизвестной доступностью.
				body = strings.Replace(body, `"name":"Stadium"}}`, `"name":"Stadium"},"459946":{"id":459946,"name":"Garden"}}`, 1)
				body = strings.Replace(body, "</body>", `<div class="room" data-roomtype-id="459946"></div></body>`, 1)
			}
			req := testSearchRequest()
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Query().Get("dfrom") != "26-10-2026" || r.URL.Query().Get("dto") != "28-10-2026" {
					t.Errorf("неверные параметры Search: %s", r.URL)
				}
				fmt.Fprint(w, body)
			})
			started := time.Now()
			result := client.Collect(context.Background(), req)
			if result.Status != tc.status || result.Error != nil || result.HTTPStatus != 200 {
				t.Fatalf("неверный результат: %+v", result)
			}
			assertCollectedAt(t, result, started)
			if !reflect.DeepEqual(result.Request, req) {
				t.Error("параметры попытки не сохранены")
			}
			if len(result.Categories) == 0 || result.Categories[0].Name == "" {
				t.Fatalf("не сохранены категории: %+v", result.Categories)
			}
			if tc.status == SearchStatusSuccess || tc.status == SearchStatusPartial {
				if len(result.Variants) != 1 || result.Variants[0].ID != "561429" || result.Variants[0].CategoryID != "561429" || result.Variants[0].Adults != 2 {
					t.Errorf("не сохранён вариант: %+v", result.Variants)
				}
				if len(result.Offers) != 1 {
					t.Fatalf("не сохранён тариф: %+v", result.Offers)
				}
				offer := result.Offers[0]
				if offer.CategoryID != "561429" || offer.VariantID != "561429" || offer.RateID != "1" || offer.Price != "4910.50" || offer.Currency != "RUB" || !offer.Visible {
					t.Errorf("неверный тариф: %+v", offer)
				}
			}
			if tc.status == SearchStatusPartial {
				if len(result.Categories) != 2 || len(result.Warnings) != 1 || result.Warnings[0].Code != "category_availability_unknown" || result.Warnings[0].Context != "459946" {
					t.Errorf("не сохранена диагностика неизвестной доступности: %+v", result)
				}
			} else if len(result.Warnings) != 0 {
				t.Errorf("неожиданные предупреждения: %+v", result.Warnings)
			}
			if tc.status == SearchStatusNoOffers {
				if len(result.Offers) != 0 || len(result.Variants) != 0 || len(result.CategoryAvailability) != 1 || !result.CategoryAvailability[0].ConfirmedUnavailable {
					t.Errorf("неверное подтверждение отсутствия мест: %+v", result)
				}
			}
			parsed, err := Parse([]byte(body), req)
			if err != nil {
				t.Fatal(err)
			}
			parsed.CollectedAt, parsed.HTTPStatus = result.CollectedAt, 200
			if !reflect.DeepEqual(result, parsed) {
				t.Errorf("Collect потерял данные Parse:\nполучено: %+v\nожидалось: %+v", result, parsed)
			}
			if requests.Load() != 1 {
				t.Errorf("запросов = %d, ожидался один", requests.Load())
			}
		})
	}
}

func assertCollectedAt(t *testing.T, result SearchResult, started time.Time) {
	t.Helper()
	if result.CollectedAt.Before(started) || result.CollectedAt.After(time.Now()) || result.CollectedAt.Location() != time.UTC {
		t.Errorf("неверное время попытки в UTC: %v", result.CollectedAt)
	}
}

func TestCollectHTTPAndParseErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
		retryable  bool
	}{
		{"forbidden", 403, "secret HTML", "http_error", false},
		{"rate_limited", 429, "secret HTML", "http_error", true},
		{"server_error", 500, "secret HTML", "http_error", true},
		{"empty_body", 200, "", "parse_error", false},
		{"error_page", 200, "<h1>secret HTML</h1>", "parse_error", false},
		{"corrupt_metadata", 200, strings.Replace(collectorHTML(`<div class="plan_row"></div>`), `{"id":561429,"name":"Stadium"}`, `{broken secret HTML}`, 1), "parse_error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			req := testSearchRequest()
			started := time.Now()
			result := client.Collect(context.Background(), req)
			if result.Status != SearchStatusFailed || result.HTTPStatus != tc.status || !reflect.DeepEqual(result.Request, req) {
				t.Fatalf("неверный результат: %+v", result)
			}
			assertCollectionError(t, result.Error, tc.code, tc.status, tc.retryable)
			assertCollectedAt(t, result, started)
			if tc.code == "parse_error" && (result.Error.Message != "failed to parse Bnovo response" || strings.Contains(result.Error.Error(), "secret HTML")) {
				t.Errorf("ошибка парсинга содержит исходную диагностику: %+v", result.Error)
			}
			if requests.Load() != 1 {
				t.Errorf("запросов = %d, повторов быть не должно", requests.Load())
			}
		})
	}
}

func TestCollectContextErrors(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		for _, reason := range []string{"timeout", "canceled"} {
			t.Run(phase+"/"+reason, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var requests atomic.Int32
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if phase == "body" {
						fmt.Fprint(w, "partial HTML")
						w.(http.Flusher).Flush()
					}
					if reason == "canceled" {
						cancel()
					}
					<-r.Context().Done()
				})
				if reason == "timeout" {
					client.timeout = 100 * time.Millisecond
				}
				req := testSearchRequest()
				started := time.Now()
				result := client.Collect(ctx, req)
				status := 0
				if phase == "body" && reason == "timeout" {
					status = 200
				}
				// Отмена после Flush может опередить получение заголовков клиентом.
				if phase == "body" && reason == "canceled" && result.HTTPStatus == 200 {
					status = 200
				}
				if result.Status != SearchStatusFailed || result.HTTPStatus != status || !reflect.DeepEqual(result.Request, req) {
					t.Fatalf("неверный результат: %+v", result)
				}
				assertCollectionError(t, result.Error, reason, status, reason == "timeout")
				assertCollectedAt(t, result, started)
				if requests.Load() != 1 {
					t.Errorf("запросов = %d, повторов быть не должно", requests.Load())
				}
			})
		}
	}
	t.Run("already_canceled", func(t *testing.T) {
		var requests atomic.Int32
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		result := client.Collect(ctx, testSearchRequest())
		if result.Status != SearchStatusFailed || result.HTTPStatus != 0 {
			t.Fatalf("неверный результат: %+v", result)
		}
		assertCollectionError(t, result.Error, "canceled", 0, false)
		assertCollectedAt(t, result, started)
		if requests.Load() != 0 {
			t.Error("запрос с отменённым контекстом был отправлен")
		}
	})
}

func TestCollectionErrorFrom(t *testing.T) {
	original := &CollectionError{Code: "http_error", Message: "Bnovo returned HTTP 429", HTTPStatus: 429, Retryable: true}
	before := *original
	got := collectionErrorFrom(fmt.Errorf("wrapped: %w", original), 503)
	if got == original || got.Code != original.Code || got.Message != original.Message || got.Retryable != original.Retryable || got.HTTPStatus != 503 {
		t.Errorf("ошибка не скопирована корректно: %+v", got)
	}
	if *original != before {
		t.Error("исходная ошибка изменена")
	}
	unexpected := collectionErrorFrom(errors.New("URL?token=secret; cookies; HTML"), 200)
	if !reflect.DeepEqual(unexpected, &CollectionError{Code: "internal_error", Message: "unexpected Bnovo collection error", HTTPStatus: 200, Retryable: false}) {
		t.Errorf("неверная классификация неизвестной ошибки: %+v", unexpected)
	}
}

func TestCollectInvalidRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SearchRequest)
	}{
		{"invalid_uid", func(r *SearchRequest) { r.PropertyID = "../invalid" }},
		{"missing_check_in", func(r *SearchRequest) { r.CheckIn = time.Time{} }},
		{"reversed_dates", func(r *SearchRequest) { r.CheckOut = r.CheckIn.AddDate(0, 0, -1) }},
		{"no_adults", func(r *SearchRequest) { r.Adults = 0 }},
		{"unsupported_language", func(r *SearchRequest) { r.Language = "en" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			req := testSearchRequest()
			tc.change(&req)
			result := client.Collect(context.Background(), req)
			if result.Status != SearchStatusFailed || result.HTTPStatus != 0 || !reflect.DeepEqual(result.Request, req) {
				t.Fatalf("неверный результат: %+v", result)
			}
			assertCollectionError(t, result.Error, "invalid_request", 0, false)
			if requests.Load() != 0 {
				t.Error("некорректный запрос был отправлен серверу")
			}
		})
	}
}
