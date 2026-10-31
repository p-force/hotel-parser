package bnovo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// defaultTimeout — максимальная длительность одного HTTP-запроса,
	// включая получение заголовков и чтение тела ответа.
	defaultTimeout = 20 * time.Second

	// defaultMaxBodySize — максимально допустимый размер HTML-ответа.
	defaultMaxBodySize int64 = 8 << 20 // 8 MiB

	maxRedirects = 3
)

// Client выполняет публичные HTTP-запросы к модулю бронирования Bnovo.
//
// Клиент отвечает только за получение HTML.
// Он не парсит категории, тарифы и количество номеров,
// не рассчитывает занятость и не работает с базой данных.
type Client struct {
	// baseURL — адрес публичного модуля Bnovo.
	// Например: https://reservationsteps.ru
	baseURL *url.URL

	// httpClient выполняет HTTP-запросы.
	// Один экземпляр используется повторно для разных запросов.
	httpClient *http.Client

	// timeout — максимальная длительность одного запроса.
	timeout time.Duration

	// maxBodySize — максимальный размер тела ответа в байтах.
	maxBodySize int64
}

// HTTPResponse содержит результат одного HTTP-запроса.
//
// Метаданные сохраняются даже при HTTP-ошибке или ошибке
// чтения ответа. В дальнейшем их можно использовать
// для формирования SearchResult и записи collection_attempts.
type HTTPResponse struct {
	// Body содержит исходный HTML.
	// При ошибке тело не возвращается.
	Body []byte

	// StatusCode — HTTP-статус ответа.
	// Равен 0, если HTTP-ответ не был получен.
	StatusCode int

	// RequestedAt — момент начала запроса в UTC.
	RequestedAt time.Time

	// RequestStarted означает, что выполнение дошло до вызова http.Client.Do.
	// Отличает пропущенный HTTP-этап от выполненного с нулевой длительностью.
	RequestStarted bool

	// Duration — время от начала http.Client.Do до завершения обработки
	// ответа, включая чтение тела. Ноль, если HTTP-запрос не начинался.
	Duration time.Duration
}

// Error позволяет использовать CollectionError
// как стандартную Go-ошибку.
func (e *CollectionError) Error() string {
	if e == nil {
		return "<nil>"
	}

	return e.Code + ": " + e.Message
}

// redirectBlockedError означает, что HTTP-клиент
// отклонил перенаправление по правилам безопасности.
type redirectBlockedError struct {
	reason string
}

func (e *redirectBlockedError) Error() string {
	return e.reason
}

// NewClient создаёт HTTP-клиент для публичного модуля Bnovo.
//
// baseURL берётся из конфигурации отеля.
// Например: https://reservationsteps.ru
//
// Если httpClient == nil, создаётся стандартный http.Client.
//
// Собственный таймаут Bnovo-клиента действует независимо
// от значения httpClient.Timeout.
func NewClient(
	baseURL string,
	httpClient *http.Client,
) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse Bnovo base URL: %w", err)
	}

	if (u.Scheme != "https" && u.Scheme != "http") ||
		u.Host == "" ||
		u.User != nil ||
		u.RawQuery != "" ||
		u.ForceQuery ||
		u.Fragment != "" {
		return nil, errors.New("invalid Bnovo base URL")
	}

	// Клиент ожидает origin, а не URL конкретной страницы.
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New(
			"Bnovo base URL must not contain a path",
		)
	}

	if httpClient == nil {
		httpClient = &http.Client{}
	}

	// Создаём собственную копию конфигурации HTTP-клиента,
	// чтобы не изменять клиент, переданный вызывающей стороной.
	clientCopy := *httpClient
	base := clientCopy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if _, limited := base.(*startLimitedTransport); !limited {
		clientCopy.Transport = &startLimitedTransport{base: base}
	}

	clientCopy.CheckRedirect = func(
		req *http.Request,
		via []*http.Request,
	) error {
		if len(via) > maxRedirects {
			return &redirectBlockedError{
				reason: "too many redirects",
			}
		}

		// Разрешаем редиректы только внутри исходного origin.
		if req.URL.Scheme != u.Scheme ||
			!strings.EqualFold(req.URL.Hostname(), u.Hostname()) ||
			req.URL.Port() != u.Port() {
			return &redirectBlockedError{
				reason: "redirect to another origin",
			}
		}

		return nil
	}
	return &Client{
		baseURL:     u,
		httpClient:  &clientCopy,
		timeout:     defaultTimeout,
		maxBodySize: defaultMaxBodySize,
	}, nil
}

// Bootstrap получает первоначальную HTML-страницу модуля Bnovo.
//
// Запрос не содержит даты проживания и количество гостей.
// Используется для получения начальной страницы виджета.
//
// Bootstrap не определяет наличие свободных номеров.
func (c *Client) Bootstrap(
	ctx context.Context,
	propertyID string,
) (HTTPResponse, error) {
	if err := validatePropertyID(propertyID); err != nil {
		return HTTPResponse{}, err
	}

	endpoint := c.roomURL(propertyID)

	return c.get(ctx, endpoint, "ru")
}

// Search запрашивает HTML с предложениями проживания.
//
// req содержит UID отеля, даты и количество гостей.
//
// Метод возвращает HTTPResponse с исходным HTML.
// Наличие предложений, цены, категории и тарифы
// будут определяться отдельно в parser.go.
//
// HTTP 200 без предложений НЕ является ошибкой HTTP-клиента.
func (c *Client) Search(
	ctx context.Context,
	req SearchRequest,
) (HTTPResponse, error) {
	if err := validateSearchRequest(req); err != nil {
		return HTTPResponse{}, err
	}

	endpoint := c.roomURL(req.PropertyID)

	query := endpoint.Query()

	// Формат дат, используемый публичным модулем Bnovo.
	query.Set("dfrom", req.CheckIn.Format("02-01-2006"))
	query.Set("dto", req.CheckOut.Format("02-01-2006"))

	// Количество взрослых в одном размещении.
	query.Set("adults", strconv.Itoa(req.Adults))

	// Язык ответа.
	query.Set("lang", req.Language)

	endpoint.RawQuery = query.Encode()

	return c.get(ctx, endpoint, req.Language)
}

// roomURL формирует адрес страницы бронирования.
//
// Например:
// https://reservationsteps.ru/rooms/index/{uid}
//
// UID предварительно проверяется вызывающим методом.
func (c *Client) roomURL(propertyID string) *url.URL {
	return c.baseURL.JoinPath(
		"rooms",
		"index",
		propertyID,
	)
}

// get выполняет один HTTP GET и возвращает результат
// вместе с HTTP-метаданными.
//
// Обрабатывает:
//   - отмену context;
//   - превышение таймаута;
//   - сетевые ошибки;
//   - HTTP-статусы, отличные от 200;
//   - ошибки чтения тела;
//   - превышение допустимого размера ответа.
//
// При ошибке частичное тело HTML не возвращается.
func (c *Client) get(
	ctx context.Context,
	endpoint *url.URL,
	language string,
) (result HTTPResponse, err error) {
	// Запоминаем момент начала попытки.
	result.RequestedAt = time.Now().UTC()

	// Собственный timeout клиента ограничивает весь запрос,
	// включая чтение тела ответа.
	//
	// Если вызывающая сторона задала более короткий deadline,
	// он имеет приоритет.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Проверяем отмену до создания и отправки HTTP-запроса.
	if ctx.Err() != nil {
		code := classifyRequestError(ctx, ctx.Err())

		return result, newRequestError(
			code,
			"Bnovo request context is already canceled",
			0,
		)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		endpoint.String(),
		nil,
	)
	if err != nil {
		return result, &CollectionError{
			Code:    "invalid_request",
			Message: "failed to create HTTP request",
		}
	}

	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", language)

	// Измеряем только HTTP-этап, включая чтение тела и закрытие ответа.
	// started сохраняет монотонную часть времени; RequestedAt в UTC её не имеет.
	result.RequestStarted = true
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// При отказе CheckRedirect http.Client может вернуть
		// одновременно response и error. Тело такого response
		// нужно закрыть.
		if resp != nil {
			result.StatusCode = resp.StatusCode

			if resp.Body != nil {
				resp.Body.Close()
			}
		}

		var redirectErr *redirectBlockedError
		if errors.As(err, &redirectErr) {
			return result, &CollectionError{
				Code:       "redirect_blocked",
				Message:    redirectErr.reason,
				HTTPStatus: result.StatusCode,
				Retryable:  false,
			}
		}

		code := classifyRequestError(ctx, err)

		classified := newRequestError(code, "Bnovo HTTP request failed", result.StatusCode)
		if code == "transport_error" {
			classified.Retryable = temporaryTransportError(err)
		}
		return result, classified
	}
	defer resp.Body.Close()

	// HTTP-статус фиксируется сразу после получения ответа.
	// Он сохраняется даже при последующей ошибке чтения тела.
	result.StatusCode = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		return result, &CollectionError{
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			Code:       "http_error",
			Message:    fmt.Sprintf("Bnovo returned HTTP %d", resp.StatusCode),
			HTTPStatus: resp.StatusCode,
			Retryable: resp.StatusCode == http.StatusTooManyRequests ||
				resp.StatusCode >= http.StatusInternalServerError,
		}
	}

	// Ограничиваем чтение maxBodySize + 1 байтом.
	//
	// Дополнительный байт позволяет определить превышение
	// лимита без загрузки всего потенциально большого ответа.
	body, err := io.ReadAll(
		io.LimitReader(resp.Body, c.maxBodySize+1),
	)
	if err != nil {
		code := classifyRequestError(ctx, err)

		classified := newRequestError(code, "failed to read Bnovo response body", resp.StatusCode)
		if code == "transport_error" {
			classified.Retryable = temporaryTransportError(err)
		}
		return result, classified
	}

	if int64(len(body)) > c.maxBodySize {
		return result, &CollectionError{
			Code:       "response_too_large",
			Message:    "Bnovo response exceeds body size limit",
			HTTPStatus: resp.StatusCode,
			Retryable:  false,
		}
	}

	// Проверяем context после чтения.
	// Не считаем запрос успешным, если он был отменён
	// до завершения операции.
	if ctx.Err() != nil {
		code := classifyRequestError(ctx, ctx.Err())

		return result, newRequestError(
			code,
			"Bnovo request was canceled while reading response",
			resp.StatusCode,
		)
	}

	// Только полностью прочитанный ответ передаётся парсеру.
	result.Body = body

	return result, nil
}

// classifyRequestError определяет категорию ошибки запроса.
//
// Порядок проверок важен:
//  1. Явная отмена context.
//  2. Истечение deadline.
//  3. Сетевой timeout.
//  4. Прочие транспортные ошибки.
func classifyRequestError(
	ctx context.Context,
	err error,
) string {
	if errors.Is(ctx.Err(), context.Canceled) ||
		errors.Is(err, context.Canceled) {
		return "canceled"
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}

	var netErr net.Error

	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	return "transport_error"
}

// newRequestError создаёт CollectionError
// для ошибок HTTP-транспорта и context.
//
// Retryable обозначает потенциальную возможность повтора,
// но сам HTTP-клиент повторные запросы не выполняет.
func newRequestError(
	code string,
	message string,
	httpStatus int,
) *CollectionError {
	retryable := false

	switch code {
	case "timeout", "transport_error":
		retryable = true
	}

	return &CollectionError{
		Code:       code,
		Message:    message,
		HTTPStatus: httpStatus,
		Retryable:  retryable,
	}
}

// validatePropertyID проверяет публичный UID Bnovo.
//
// UID должен быть одним безопасным сегментом URL.
// Запрещаем символы, которые могли бы изменить путь,
// query-параметры или фрагмент URL.
func validatePropertyID(propertyID string) error {
	if propertyID == "" {
		return &CollectionError{
			Code:    "invalid_request",
			Message: "property ID is empty",
		}
	}

	// В текущей интеграции UID Bnovo — UUID.
	// Разрешаем только шестнадцатеричные символы и дефисы.
	for _, r := range propertyID {
		valid := (r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'f') ||
			(r >= 'A' && r <= 'F') ||
			r == '-'

		if !valid {
			return &CollectionError{
				Code:    "invalid_request",
				Message: "property ID contains invalid characters",
			}
		}
	}

	return nil
}

// validateSearchRequest проверяет параметры поискового запроса.
//
// Проверка выполняется до отправки HTTP-запроса,
// чтобы не создавать обращения к Bnovo с некорректными данными.
func validateSearchRequest(req SearchRequest) error {
	if err := validatePropertyID(req.PropertyID); err != nil {
		return err
	}

	if req.CheckIn.IsZero() || req.CheckOut.IsZero() {
		return &CollectionError{
			Code:    "invalid_request",
			Message: "check-in or check-out date is missing",
		}
	}

	// Сравниваем календарные даты, а не time.Duration.
	// Из-за переходов на летнее/зимнее время
	// календарные сутки не всегда равны 24 часам.
	checkIn := req.CheckIn.Format("2006-01-02")
	checkOut := req.CheckOut.Format("2006-01-02")

	if checkOut <= checkIn {
		return &CollectionError{
			Code:    "invalid_request",
			Message: "check-out must be after check-in",
		}
	}

	if req.Adults <= 0 {
		return &CollectionError{
			Code:    "invalid_request",
			Message: "adults must be positive",
		}
	}

	// В текущем MVP поддерживаются только взрослые.
	// Добавление детей потребует исследования
	// соответствующих query-параметров Bnovo.
	if len(req.ChildrenAges) != 0 {
		return &CollectionError{
			Code:    "invalid_request",
			Message: "children are not supported yet",
		}
	}

	// Пока поддерживаем только русскоязычный ответ,
	// поскольку HTML-фикстуры и текстовые условия
	// исследованы на русском языке.
	if req.Language != "ru" {
		return &CollectionError{
			Code:    "invalid_request",
			Message: "only Russian language is supported in MVP",
		}
	}

	return nil
}

// SourceOrigin возвращает только схему и адрес источника, без пути и query.
func (c *Client) SourceOrigin() string { return c.baseURL.Scheme + "://" + c.baseURL.Host }

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

func temporaryTransportError(err error) bool {
	var network net.Error
	if errors.As(err, &network) && (network.Timeout() || network.Temporary()) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}
