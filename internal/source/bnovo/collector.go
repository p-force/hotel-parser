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
	// nil означает, что этап не выполнялся; короткий выполненный этап может занять 0 мс.
	var httpDuration, parseDuration *time.Duration
	propertyID := req.PropertyID
	if validatePropertyID(propertyID) != nil {
		// Некорректный UID может содержать URL, токен или произвольный текст.
		propertyID = ""
	}
	log := slog.Default().With(
		"attempt_id", rand.Text(),
		"collected_at", started.UTC(),
		"property_id", propertyID,
		"check_in", req.CheckIn.Format("2006-01-02"),
		"check_out", req.CheckOut.Format("2006-01-02"),
		"los", calendarLOS(req),
		"adults", req.Adults,
	)
	log.InfoContext(ctx, "bnovo.collect.started")
	defer func() {
		duration := time.Since(started)
		attrs := []slog.Attr{slog.Int64("collection_duration_ms", duration.Milliseconds())}
		if httpDuration != nil {
			attrs = append(attrs, slog.Int64("http_duration_ms", httpDuration.Milliseconds()))
		}
		if parseDuration != nil {
			attrs = append(attrs, slog.Int64("parse_duration_ms", parseDuration.Milliseconds()))
		}
		logCollectionResult(ctx, log, "bnovo.collect.completed", result, duration, attrs...)
	}()

	result = SearchResult{
		Request:     req,
		Status:      SearchStatusFailed,
		CollectedAt: started.UTC(),
	}

	// Выполняем публичный запрос.
	response, err := c.Search(ctx, req)

	// Сохраняем метаданные независимо от успешности запроса.
	if response.Duration > 0 {
		httpDuration = &response.Duration
	}
	result.HTTPStatus = response.StatusCode

	httpLevel := slog.LevelInfo
	errorCode := ""
	if err != nil {
		result.Error = collectionErrorFrom(err, response.StatusCode)
		httpLevel = slog.LevelError
		errorCode = result.Error.Code
	}
	httpAttrs := []slog.Attr{
		slog.Int("http_status", response.StatusCode),
		slog.String("error_code", errorCode),
	}
	if httpDuration != nil {
		httpAttrs = append(httpAttrs,
			slog.Int64("http_duration_ms", httpDuration.Milliseconds()),
			slog.Int64("duration_ms", httpDuration.Milliseconds()),
		)
	}
	log.LogAttrs(ctx, httpLevel, "bnovo.http.completed", httpAttrs...)
	if err != nil {
		return result
	}

	// Разбираем полученный HTML.
	parseStarted := time.Now()
	parsed, err := Parse(response.Body, req)
	parseElapsed := time.Since(parseStarted)
	parseDuration = &parseElapsed
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
		logCollectionResult(ctx, log, "bnovo.parse.completed", result, parseElapsed, slog.Int64("parse_duration_ms", parseElapsed.Milliseconds()))

		return result
	}

	// При успехе используем полный результат парсера.
	// Метаданные HTTP-запроса переносим отдельно, поскольку
	// Parse не выполняет HTTP и не знает время начала запроса.
	parsed.CollectedAt = result.CollectedAt
	parsed.HTTPStatus = response.StatusCode
	logCollectionResult(ctx, log, "bnovo.parse.completed", parsed, parseElapsed, slog.Int64("parse_duration_ms", parseElapsed.Milliseconds()))

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
func logCollectionResult(ctx context.Context, log *slog.Logger, event string, result SearchResult, duration time.Duration, timingAttrs ...slog.Attr) {
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
	attrs := []slog.Attr{
		slog.Int("http_status", result.HTTPStatus),
		slog.String("status", string(result.Status)),
		slog.Int("categories_count", len(result.Categories)),
		slog.Int("offers_count", len(result.Offers)),
		slog.Int("warnings_count", len(result.Warnings)),
		slog.String("error_code", errorCode),
		slog.Int64("duration_ms", duration.Milliseconds()),
	}
	attrs = append(attrs, timingAttrs...)
	log.LogAttrs(ctx, level, event, attrs...)
}
