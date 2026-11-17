package collect

import (
	"context"
	"fmt"
	"sync"
)

const DefaultGlobalConcurrency = 8

// Общий допуск для всех пакетных API этого пакета в одном процессе.
// Слот охватывает Collect целиком, включая чтение тела и парсинг.
var globalConcurrency = struct {
	sync.Mutex
	slots      chan struct{}
	batches    int
	active     int
	changed    chan struct{}
	properties map[string]int
	sources    map[string]*sourceState
	limits     Limits
	waiters    []admissionWaiter
}{slots: make(chan struct{}, DefaultGlobalConcurrency), changed: make(chan struct{}), properties: make(map[string]int), sources: make(map[string]*sourceState), limits: defaultLimits()}

// ConfigureGlobalConcurrency задаёт общий предел до запуска пакетного сбора.
// Перенастройка во время любого пакета отклоняется, чтобы не создать второй допуск.
// Прямые вызовы Client.Collect и другие процессы этим пределом не ограничиваются.
func ConfigureGlobalConcurrency(limit int) error {
	if limit < 1 || limit > MaxWorkers {
		return fmt.Errorf("global concurrency must be between 1 and %d", MaxWorkers)
	}
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	if globalConcurrency.batches != 0 {
		return fmt.Errorf("cannot configure global concurrency while a batch is running")
	}
	globalConcurrency.slots = make(chan struct{}, limit)
	return nil
}

func registerBatch() (chan struct{}, func()) {
	globalConcurrency.Lock()
	globalConcurrency.batches++
	slots := globalConcurrency.slots
	globalConcurrency.Unlock()
	return slots, func() { globalConcurrency.Lock(); globalConcurrency.batches--; globalConcurrency.Unlock() }
}

func acquireScenario(ctx context.Context, slots chan struct{}) (int, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	select {
	case <-ctx.Done():
		return 0, false
	case slots <- struct{}{}:
	}
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	if ctx.Err() != nil {
		<-slots
		return 0, false
	}
	globalConcurrency.active++
	return globalConcurrency.active, true
}

func releaseScenario(slots chan struct{}) {
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	globalConcurrency.active--
	<-slots
	signalLimits()
}
