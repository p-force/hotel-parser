package bnovo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testPropertyID = "7f3f84ae-1f13-419f-af60-ae782d454666"

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testSearchRequest() SearchRequest {
	loc := time.FixedZone("UTC+3", 3*60*60)
	return SearchRequest{
		PropertyID: testPropertyID,
		CheckIn:    time.Date(2026, 10, 26, 0, 0, 0, 0, loc),
		CheckOut:   time.Date(2026, 10, 28, 0, 0, 0, 0, loc),
		Adults:     2,
		Language:   "ru",
	}
}

func assertCollectionError(t *testing.T, err error, code string, status int, retryable bool) {
	t.Helper()
	var got *CollectionError
	if !errors.As(err, &got) {
		t.Fatalf("ожидалась *CollectionError, получено %T: %v", err, err)
	}
	if got.Code != code || got.HTTPStatus != status || got.Retryable != retryable {
		t.Errorf("ошибка: %+v; ожидались code=%s, status=%d, retryable=%t", got, code, status, retryable)
	}
	if got.Message == "" {
		t.Error("диагностическое сообщение пустое")
	}
}

func assertResponseMetadata(t *testing.T, response HTTPResponse, started time.Time) {
	t.Helper()
	if response.RequestedAt.Before(started) || response.RequestedAt.After(time.Now()) {
		t.Errorf("некорректное время начала запроса: %v", response.RequestedAt)
	}
	if response.RequestedAt.Location() != time.UTC {
		t.Errorf("время запроса должно быть в UTC: %v", response.RequestedAt)
	}
	if response.Duration < 0 {
		t.Errorf("продолжительность не должна быть отрицательной: %v", response.Duration)
	}
}

func TestClientHTTPStatus(t *testing.T) {
	for _, status := range []int{200, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			const body = "<html>Нет предложений</html>"
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/rooms/index/"+testPropertyID {
					t.Errorf("неожиданный запрос: %s %s", r.Method, r.URL)
				}
				if r.URL.RawQuery != "" {
					t.Errorf("Bootstrap не должен передавать параметры поиска: %s", r.URL.RawQuery)
				}
				if r.Header.Get("Accept") != "text/html" || r.Header.Get("Accept-Language") != "ru" {
					t.Errorf("некорректные заголовки: %v", r.Header)
				}
				w.WriteHeader(status)
				fmt.Fprint(w, body)
			})
			started := time.Now()
			response, err := client.Bootstrap(context.Background(), testPropertyID)
			if response.StatusCode != status {
				t.Errorf("статус = %d, ожидался %d", response.StatusCode, status)
			}
			assertResponseMetadata(t, response, started)
			if !response.RequestStarted {
				t.Error("нет признака запуска HTTP-запроса")
			}
			if status == http.StatusOK {
				if err != nil || string(response.Body) != body {
					t.Fatalf("ответ = %q, ошибка = %v", response.Body, err)
				}
			} else {
				assertCollectionError(t, err, "http_error", status, status == 429 || status == 500)
				if response.Body != nil {
					t.Error("при ошибке тело ответа должно быть nil")
				}
			}
			if requests.Load() != 1 {
				t.Errorf("клиент выполнил %d запросов вместо одного", requests.Load())
			}
		})
	}
}

func TestClientTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		for _, source := range []string{"client", "context", "http_client"} {
			t.Run(phase+"/"+source, func(t *testing.T) {
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					if phase == "body" {
						fmt.Fprint(w, "partial")
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
				})
				ctx := context.Background()
				client.timeout = 5 * time.Second
				const timeout = 100 * time.Millisecond
				switch source {
				case "client":
					client.timeout = timeout
				case "context":
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, timeout)
					defer cancel()
				case "http_client":
					client.httpClient.Timeout = timeout
				}
				started := time.Now()
				response, err := client.Bootstrap(ctx, testPropertyID)
				// При таймауте до получения заголовков HTTP-статус неизвестен.
				status := 0
				if phase == "body" {
					status = http.StatusOK
				}
				assertCollectionError(t, err, "timeout", status, true)
				if response.StatusCode != status || response.Body != nil {
					t.Errorf("неожиданный ответ: %+v", response)
				}
				assertResponseMetadata(t, response, started)
			})
		}
	}
}

func TestClientCanceledContext(t *testing.T) {
	for _, phase := range []string{"before_request", "headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if phase == "body" {
					fmt.Fprint(w, "partial")
					w.(http.Flusher).Flush()
				}
				cancel()
				<-r.Context().Done()
			})
			if phase == "before_request" {
				cancel()
			}
			started := time.Now()
			response, err := client.Bootstrap(ctx, testPropertyID)
			status := 0
			if phase == "body" {
				status = http.StatusOK
			}
			// Отмена сразу после Flush может опередить получение заголовков клиентом.
			if phase == "body" && response.StatusCode == 0 {
				status = 0
			}
			assertCollectionError(t, err, "canceled", status, false)
			if response.StatusCode != status || response.Body != nil {
				t.Errorf("неожиданный ответ: %+v", response)
			}
			assertResponseMetadata(t, response, started)
			if phase == "before_request" && requests.Load() != 0 {
				t.Error("отменённый контекст не должен приводить к HTTP-запросу")
			}
			if phase == "before_request" && (response.Duration != 0 || response.RequestStarted) {
				t.Errorf("неотправленный запрос получил длительность: %v", response.Duration)
			}
			if phase != "before_request" && (!response.RequestStarted || response.Duration < 0) {
				t.Errorf("нет длительности начатого HTTP-запроса: %v", response.Duration)
			}
		})
	}
}

func TestClientBodySize(t *testing.T) {
	const limit = 16
	for _, streaming := range []bool{false, true} {
		for _, size := range []int{0, limit - 1, limit, limit + 1, limit * 100} {
			t.Run(fmt.Sprintf("streaming=%t/size=%d", streaming, size), func(t *testing.T) {
				body := strings.Repeat("x", size)
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					if streaming {
						w.(http.Flusher).Flush()
					}
					fmt.Fprint(w, body)
				})
				client.maxBodySize = limit
				response, err := client.Bootstrap(context.Background(), testPropertyID)
				if response.StatusCode != http.StatusOK {
					t.Errorf("статус = %d", response.StatusCode)
				}
				if size > limit {
					assertCollectionError(t, err, "response_too_large", http.StatusOK, false)
					if response.Body != nil {
						t.Error("слишком большое тело не должно возвращаться даже частично")
					}
				} else if err != nil || string(response.Body) != body {
					t.Errorf("ответ = %q, ошибка = %v", response.Body, err)
				}
			})
		}
	}
}

func TestClientSearchDates(t *testing.T) {
	for _, tc := range []struct {
		name            string
		in, out         time.Time
		wantIn, wantOut string
	}{
		{"local_dates", testSearchRequest().CheckIn, testSearchRequest().CheckOut, "26-10-2026", "28-10-2026"},
		// Календарные даты важнее порядка абсолютных моментов времени.
		{"different_zones", time.Date(2026, 12, 31, 23, 0, 0, 0, time.FixedZone("west", -12*3600)), time.Date(2027, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 14*3600)), "31-12-2026", "01-01-2027"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				want := map[string][]string{"dfrom": {tc.wantIn}, "dto": {tc.wantOut}, "adults": {"2"}, "lang": {"ru"}}
				if !reflect.DeepEqual(map[string][]string(r.URL.Query()), want) {
					t.Errorf("параметры = %v, ожидались %v", r.URL.Query(), want)
				}
				if r.URL.Path != "/rooms/index/"+testPropertyID {
					t.Errorf("неверный путь: %s", r.URL.Path)
				}
				fmt.Fprint(w, "<html></html>")
			})
			req := testSearchRequest()
			req.CheckIn, req.CheckOut = tc.in, tc.out
			if _, err := client.Search(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientSearchInvalidDates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SearchRequest)
	}{
		{"missing_check_in", func(r *SearchRequest) { r.CheckIn = time.Time{} }},
		{"missing_check_out", func(r *SearchRequest) { r.CheckOut = time.Time{} }},
		{"same_date", func(r *SearchRequest) { r.CheckOut = r.CheckIn.Add(time.Hour) }},
		{"reversed_dates", func(r *SearchRequest) { r.CheckOut = r.CheckIn.AddDate(0, 0, -1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			req := testSearchRequest()
			tc.change(&req)
			response, err := client.Search(context.Background(), req)
			assertCollectionError(t, err, "invalid_request", 0, false)
			if requests.Load() != 0 || !reflect.DeepEqual(response, HTTPResponse{}) {
				t.Errorf("некорректные даты должны отклоняться до HTTP-запроса: %+v", response)
			}
		})
	}
}

func TestClientInvalidPropertyID(t *testing.T) {
	for _, uid := range []string{"", " ", "../abc", "abc/def", "abc\\def", "abc?x=1", "abc#fragment", "%2f", "abc\n", "отель", "xyz"} {
		t.Run(fmt.Sprintf("%q", uid), func(t *testing.T) {
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
			for _, method := range []string{"Bootstrap", "Search"} {
				t.Run(method, func(t *testing.T) {
					var response HTTPResponse
					var err error
					if method == "Bootstrap" {
						response, err = client.Bootstrap(context.Background(), uid)
					} else {
						req := testSearchRequest()
						req.PropertyID = uid
						response, err = client.Search(context.Background(), req)
					}
					assertCollectionError(t, err, "invalid_request", 0, false)
					if !reflect.DeepEqual(response, HTTPResponse{}) {
						t.Errorf("ожидался пустой результат: %+v", response)
					}
				})
			}
			if requests.Load() != 0 {
				t.Error("некорректный UID не должен отправляться серверу")
			}
		})
	}
}

func TestClientAllowedRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, absolute := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/absolute=%t", status, absolute), func(t *testing.T) {
				var requests atomic.Int32
				client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.URL.Path != "/final" {
						target := "/final?lang=ru"
						if absolute {
							target = "http://" + r.Host + target
						}
						http.Redirect(w, r, target, status)
						return
					}
					if r.Method != http.MethodGet || r.URL.Query().Get("lang") != "ru" {
						t.Errorf("неверный запрос после редиректа: %s %s", r.Method, r.URL)
					}
					if r.Header.Get("Accept") != "text/html" || r.Header.Get("Accept-Language") != "ru" {
						t.Errorf("потеряны заголовки после редиректа: %v", r.Header)
					}
					fmt.Fprint(w, "final HTML")
				})
				response, err := client.Bootstrap(context.Background(), testPropertyID)
				if err != nil || response.StatusCode != http.StatusOK || string(response.Body) != "final HTML" {
					t.Fatalf("ответ = %+v, ошибка = %v", response, err)
				}
				if requests.Load() != 2 {
					t.Errorf("запросов = %d, ожидалось 2", requests.Load())
				}
			})
		}
	}
}

func TestClientBlockedRedirects(t *testing.T) {
	for _, kind := range []string{"host", "port", "scheme"} {
		t.Run(kind, func(t *testing.T) {
			var targetRequests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetRequests.Add(1)
				fmt.Fprint(w, "must not be fetched")
			}))
			defer target.Close()
			var originRequests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				originRequests.Add(1)
				if r.URL.Path == "/forbidden" {
					targetRequests.Add(1)
					return
				}
				location := "http://" + r.Host + "/forbidden"
				switch kind {
				case "host":
					location = strings.Replace(location, "127.0.0.1", "localhost", 1)
				case "port":
					location = target.URL + "/forbidden"
				case "scheme":
					location = "https://" + r.Host + "/forbidden"
				}
				http.Redirect(w, r, location, http.StatusFound)
			})
			started := time.Now()
			response, err := client.Bootstrap(context.Background(), testPropertyID)
			assertCollectionError(t, err, "redirect_blocked", http.StatusFound, false)
			assertResponseMetadata(t, response, started)
			if response.StatusCode != http.StatusFound || response.Body != nil {
				t.Errorf("неожиданный ответ: %+v", response)
			}
			if originRequests.Load() != 1 || targetRequests.Load() != 0 {
				t.Errorf("запросы: исходный origin = %d, запрещённая цель = %d", originRequests.Load(), targetRequests.Load())
			}
		})
	}
}

func TestClientRedirectLimit(t *testing.T) {
	for _, redirects := range []int{3, 4} {
		t.Run(fmt.Sprintf("redirects=%d", redirects), func(t *testing.T) {
			var requests atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				step, _ := strconv.Atoi(r.URL.Query().Get("step"))
				if step < redirects {
					http.Redirect(w, r, fmt.Sprintf("/next?step=%d", step+1), http.StatusFound)
					return
				}
				fmt.Fprint(w, "final HTML")
			})
			response, err := client.Bootstrap(context.Background(), testPropertyID)
			if redirects == 3 {
				if err != nil || response.StatusCode != http.StatusOK || string(response.Body) != "final HTML" {
					t.Fatalf("три перехода должны быть разрешены: ответ = %+v, ошибка = %v", response, err)
				}
			} else {
				assertCollectionError(t, err, "redirect_blocked", http.StatusFound, false)
				if response.StatusCode != http.StatusFound || response.Body != nil {
					t.Errorf("неожиданный ответ при превышении лимита: %+v", response)
				}
			}
			// Начальный запрос и три разрешённых перехода; четвёртый не отправляется.
			if requests.Load() != 4 {
				t.Errorf("запросов = %d, ожидалось 4", requests.Load())
			}
		})
	}
}

func TestNewClientPreservesHTTPClient(t *testing.T) {
	for _, customRedirect := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom_redirect=%t", customRedirect), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/final" {
					http.Redirect(w, r, "/final", http.StatusFound)
					return
				}
				fmt.Fprint(w, "final HTML")
			}))
			defer server.Close()
			original := server.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			original.Jar = jar
			original.Timeout = 2 * time.Second
			var callbackCalls atomic.Int32
			if customRedirect {
				original.CheckRedirect = func(req *http.Request, via []*http.Request) error {
					callbackCalls.Add(1)
					return http.ErrUseLastResponse
				}
			}
			before := *original
			client, err := NewClient(server.URL, original)
			if err != nil {
				t.Fatal(err)
			}
			if client.httpClient == original {
				t.Fatal("Bnovo-клиент должен использовать отдельную копию http.Client")
			}
			if client.httpClient.Transport != before.Transport || client.httpClient.Jar != before.Jar || client.httpClient.Timeout != before.Timeout {
				t.Error("копия потеряла настройки транспорта, cookie jar или таймаута")
			}
			response, err := client.Bootstrap(context.Background(), testPropertyID)
			if err != nil || string(response.Body) != "final HTML" {
				t.Fatalf("собственная политика редиректов не сработала: ответ = %+v, ошибка = %v", response, err)
			}
			if callbackCalls.Load() != 0 {
				t.Error("Bnovo-клиент вызвал исходную политику редиректов")
			}
			if original.Transport != before.Transport || original.Jar != before.Jar || original.Timeout != before.Timeout {
				t.Error("изменены настройки исходного http.Client")
			}
			if !customRedirect && original.CheckRedirect != nil {
				t.Fatal("исходная политика редиректов больше не nil")
			}
			// Проверяем поведение исходного клиента после создания и использования копии.
			resp, err := original.Get(server.URL + "/start")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			wantStatus := http.StatusOK
			if customRedirect {
				wantStatus = http.StatusFound
				if callbackCalls.Load() != 1 {
					t.Errorf("исходный CheckRedirect вызван %d раз вместо одного", callbackCalls.Load())
				}
			}
			if resp.StatusCode != wantStatus {
				t.Errorf("исходный клиент вернул статус %d вместо %d", resp.StatusCode, wantStatus)
			}
			// Изменение настройки копии также не должно менять исходный клиент.
			client.httpClient.Timeout = time.Second
			if original.Timeout != before.Timeout {
				t.Error("изменение таймаута копии затронуло исходный клиент")
			}
		})
	}
}

func TestClientBlocksHTTPSDowngrade(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "http://"+r.Host+"/insecure", http.StatusFound)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Bootstrap(context.Background(), testPropertyID)
	assertCollectionError(t, err, "redirect_blocked", http.StatusFound, false)
	if response.StatusCode != http.StatusFound || response.Body != nil {
		t.Errorf("неожиданный ответ: %+v", response)
	}
	if requests.Load() != 1 {
		t.Errorf("запросов = %d, ожидался только исходный HTTPS-запрос", requests.Load())
	}
}
