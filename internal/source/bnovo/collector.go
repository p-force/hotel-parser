package bnovo

import (
	"context"
	"errors"
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
) SearchResult {
	result := SearchResult{
		Request: req,
		Status:  SearchStatusFailed,
	}

	// Выполняем публичный запрос.
	response, err := c.Search(ctx, req)

	// Сохраняем метаданные независимо от успешности запроса.
	result.CollectedAt = response.RequestedAt
	result.HTTPStatus = response.StatusCode

	if err != nil {
		result.Error = collectionErrorFrom(err, response.StatusCode)
		return result
	}

	// Разбираем полученный HTML.
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

		return result
	}

	// При успехе используем полный результат парсера.
	// Метаданные HTTP-запроса переносим отдельно, поскольку
	// Parse не выполняет HTTP и не знает время начала запроса.
	parsed.CollectedAt = response.RequestedAt
	parsed.HTTPStatus = response.StatusCode

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
