package bnovo

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"
)

// Collect выполняет одну попытку публичного сбора Bnovo.
//
// Последовательность:
//  1. HTTP-запрос через Client.Search.
//  2. Разбор HTML через Parse.
//  3. Заполнение метаданных и диагностики.
//
// Метод не повторяет запросы и не сохраняет данные.
// Даже при ошибке возвращает SearchResult со статусом failed.
func (c *Client) Collect(
	ctx context.Context,
	req SearchRequest,
) (result SearchResult) {
	started := time.Now()
	propertyID := req.PropertyID
	if validatePropertyID(propertyID) != nil {
		// Некорректный UID может содержать URL, токен или произвольный текст.
		propertyID = ""
	}
	log := slog.Default().With(
		"attempt_id", rand.Text(),
		"property_id", propertyID,
		"check_in", req.CheckIn.Format("2006-01-02"),
		"check_out", req.CheckOut.Format("2006-01-02"),
		"los", calendarLOS(req),
		"adults", req.Adults,
	)
	log.InfoContext(ctx, "bnovo.collect.started")
	defer func() {
		logCollectionResult(ctx, log, "bnovo.collect.completed", result, time.Since(started))
	}()

	result = SearchResult{
		Request: req,
		Status:  SearchStatusFailed,
	}

	// Выполняем публичный запрос.
	response, err := c.Search(ctx, req)

	// Сохраняем метаданные независимо от успешности запроса.
	result.CollectedAt = response.RequestedAt
	result.HTTPStatus = response.StatusCode

	httpLevel := slog.LevelInfo
	errorCode := ""
	if err != nil {
		result.Error = collectionErrorFrom(err, response.StatusCode)
		httpLevel = slog.LevelError
		errorCode = result.Error.Code
	}
	log.Log(ctx, httpLevel, "bnovo.http.completed",
		"http_status", response.StatusCode,
		"error_code", errorCode,
		"duration_ms", response.Duration.Milliseconds(),
	)
	if err != nil {
		return result
	}

	// Разбираем полученный HTML.
	parseStarted := time.Now()
	parsed, err := Parse(response.Body, req)
	if err != nil {
		result.Error = &CollectionError{
			Code:       "parse_error",
			Message:    "failed to parse Bnovo response",
			HTTPStatus: response.StatusCode,
			Retryable:  false,
		}

		// Сохраняем уже извлечённые данные и предупреждения,
		// если Parse успел получить их до возникновения ошибки.
		result.Categories = parsed.Categories
		result.Variants = parsed.Variants
		result.Offers = parsed.Offers
		result.CategoryAvailability = parsed.CategoryAvailability
		result.Warnings = parsed.Warnings
		logCollectionResult(ctx, log, "bnovo.parse.completed", result, time.Since(parseStarted))

		return result
	}

	// При успехе используем полный результат парсера.
	// Метаданные HTTP-запроса переносим отдельно, поскольку
	// Parse не выполняет HTTP и не знает время начала запроса.
	parsed.CollectedAt = response.RequestedAt
	parsed.HTTPStatus = response.StatusCode
	logCollectionResult(ctx, log, "bnovo.parse.completed", parsed, time.Since(parseStarted))

	return parsed
}

// collectionErrorFrom преобразует ошибку HTTP-клиента
// в машиночитаемую CollectionError.
//
// Ошибки, которые уже классифицированы в client.go,
// сохраняют свой код и признак Retryable.
//
// Диагностика не должна содержать URL с query-параметрами,
// cookies или исходный HTML.
func collectionErrorFrom(err error, httpStatus int) *CollectionError {
	var collectionErr *CollectionError

	if errors.As(err, &collectionErr) {
		// Копируем структуру, чтобы не менять исходную ошибку.
		result := *collectionErr
		result.HTTPStatus = httpStatus
		return &result
	}

	return &CollectionError{
		Code:       "internal_error",
		Message:    "unexpected Bnovo collection error",
		HTTPStatus: httpStatus,
		Retryable:  false,
	}
}

// calendarLOS считает календарные дни, независимо от часовых поясов и переходов DST.
func calendarLOS(req SearchRequest) int {
	if req.CheckIn.IsZero() || req.CheckOut.IsZero() {
		return 0
	}
	inYear, inMonth, inDay := req.CheckIn.Date()
	outYear, outMonth, outDay := req.CheckOut.Date()
	in := time.Date(inYear, inMonth, inDay, 0, 0, 0, 0, time.UTC)
	out := time.Date(outYear, outMonth, outDay, 0, 0, 0, 0, time.UTC)
	return int(out.Sub(in).Hours() / 24)
}

// В логи попадают только счётчики и коды, без текста предупреждений и ошибок.
func logCollectionResult(ctx context.Context, log *slog.Logger, event string, result SearchResult, duration time.Duration) {
	level := slog.LevelInfo
	if result.Status == SearchStatusPartial || len(result.Warnings) > 0 {
		level = slog.LevelWarn
	}
	errorCode := ""
	if result.Error != nil {
		errorCode = result.Error.Code
	}
	if result.Status == SearchStatusFailed {
		level = slog.LevelError
	}
	log.Log(ctx, level, event,
		"http_status", result.HTTPStatus,
		"status", string(result.Status),
		"categories_count", len(result.Categories),
		"offers_count", len(result.Offers),
		"warnings_count", len(result.Warnings),
		"error_code", errorCode,
		"duration_ms", duration.Milliseconds(),
	)
}
