package collect

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

// BookingRulesStore — операции существующего storage, необходимые координатору.
type BookingRulesStore interface {
	Schema() string
	GetRunBookingRules(context.Context, string) (storage.RunBookingRules, error)
	SaveRunBookingRules(context.Context, storage.RunBookingRulesInput) (storage.RunBookingRules, error)
}

// BookingRulesClient реализуется существующим клиентом TravelLine.
type BookingRulesClient interface {
	BookingRules(context.Context, string, time.Time, time.Time, []string) (*travelline.BookingRulesResponse, error)
}

type bookingRulesRunKey struct {
	schema, runID string
}

type bookingRulesRunResult struct {
	done  chan struct{}
	saved storage.RunBookingRules
	index travelline.BookingRulesIndex
	err   error
}

// BookingRulesCoordinator объединяет обращения в пределах одного экземпляра.
// Ошибки также кешируются; повторное получение возможно с новым координатором.
type BookingRulesCoordinator struct {
	mu   sync.Mutex
	runs map[bookingRulesRunKey]*bookingRulesRunResult
}

func NewBookingRulesCoordinator() *BookingRulesCoordinator {
	return &BookingRulesCoordinator{runs: make(map[bookingRulesRunKey]*bookingRulesRunResult)}
}

// Get возвращает только индекс подтверждённо сохранённых правил.
// Первый вызывающий выполняет операции со своим context; его отмена кешируется
// как общий сбой. Отмена context ожидающего не отменяет общее получение.
// Координатор не приобретает HTTP-допуск: вызывающий код должен обеспечить его.
func (c *BookingRulesCoordinator) Get(ctx context.Context, store BookingRulesStore, client BookingRulesClient, runID, hotelID string, startDate, endDate time.Time, rateIDs []string) (travelline.BookingRulesIndex, error) {
	if err := ctx.Err(); err != nil {
		return travelline.BookingRulesIndex{}, err
	}
	key := bookingRulesRunKey{schema: store.Schema(), runID: runID}
	c.mu.Lock()
	if c.runs == nil {
		c.runs = make(map[bookingRulesRunKey]*bookingRulesRunResult)
	}
	result, exists := c.runs[key]
	if !exists {
		result = &bookingRulesRunResult{done: make(chan struct{})}
		c.runs[key] = result
	}
	c.mu.Unlock()
	if !exists {
		// Единственный владелец пишет результат, close публикует его ожидающим.
		result.saved, result.index, result.err = loadBookingRules(ctx, store, client, runID, hotelID, startDate, endDate, append([]string(nil), rateIDs...))
		close(result.done)
	}
	select {
	case <-ctx.Done():
		return travelline.BookingRulesIndex{}, ctx.Err()
	case <-result.done:
	}
	if err := ctx.Err(); err != nil {
		return travelline.BookingRulesIndex{}, err
	}
	if result.err != nil {
		return travelline.BookingRulesIndex{}, result.err
	}
	// Проверяется и cache hit: другой диапазон/политика не получает чужой индекс.
	if ok, reason := CheckRunBookingRulesApplicability(result.saved, runID, startDate, endDate, rateIDs); !ok {
		return travelline.BookingRulesIndex{}, fmt.Errorf("booking rules incompatible: %s", reason)
	}
	return result.index, nil
}

func loadBookingRules(ctx context.Context, store BookingRulesStore, client BookingRulesClient, runID, hotelID string, startDate, endDate time.Time, rateIDs []string) (storage.RunBookingRules, travelline.BookingRulesIndex, error) {
	saved, err := store.GetRunBookingRules(ctx, runID)
	if errors.Is(err, storage.ErrNotFound) {
		// Валидируем политику и диапазон до дополнительного HTTP-запроса.
		metadata := storage.RunBookingRules{RunID: runID, StartDate: startDate, EndDate: endDate, RequestedRateIDs: rateIDs}
		if ok, reason := CheckRunBookingRulesApplicability(metadata, runID, startDate, endDate, rateIDs); !ok {
			return storage.RunBookingRules{}, travelline.BookingRulesIndex{}, fmt.Errorf("booking rules incompatible: %s", reason)
		}
		if err := ctx.Err(); err != nil {
			return storage.RunBookingRules{}, travelline.BookingRulesIndex{}, err
		}
		if client == nil {
			return storage.RunBookingRules{}, travelline.BookingRulesIndex{}, fmt.Errorf("booking rules client is missing")
		}
		requestedAt := time.Now().UTC()
		response, fetchErr := client.BookingRules(ctx, hotelID, startDate, endDate, rateIDs)
		receivedAt := time.Now().UTC()
		if fetchErr != nil {
			return storage.RunBookingRules{}, travelline.BookingRulesIndex{}, fmt.Errorf("fetch booking rules: %w", fetchErr)
		}
		rules, convertErr := BookingRulesToStorage(response)
		if convertErr != nil {
			return storage.RunBookingRules{}, travelline.BookingRulesIndex{}, convertErr
		}
		saved, err = store.SaveRunBookingRules(ctx, storage.RunBookingRulesInput{
			RunID: runID, RequestedAt: requestedAt, ReceivedAt: receivedAt,
			StartDate: startDate, EndDate: endDate, RequestedRateIDs: rateIDs, Rules: rules,
		})
	}
	if err != nil {
		return storage.RunBookingRules{}, travelline.BookingRulesIndex{}, fmt.Errorf("store booking rules: %w", err)
	}
	if ok, reason := CheckRunBookingRulesApplicability(saved, runID, startDate, endDate, rateIDs); !ok {
		return storage.RunBookingRules{}, travelline.BookingRulesIndex{}, fmt.Errorf("booking rules incompatible: %s", reason)
	}
	// Метаданные кеша не должны зависеть от изменяемого slice владельца storage.
	saved.RequestedRateIDs = append([]string(nil), saved.RequestedRateIDs...)
	return saved, BookingRulesIndexFromStorage(saved), nil
}
