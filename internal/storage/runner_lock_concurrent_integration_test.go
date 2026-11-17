//go:build integration

package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresRunnerLockConcurrentChecks(t *testing.T) {
	store, _, _ := storagetest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lock, err := store.TryRunnerLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	const workers, checks = 8, 100
	start := make(chan struct{})
	errs := make(chan error, workers*checks)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range checks {
				if err := lock.Check(ctx); err != nil {
					errs <- err
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("valid runner lock rejected under concurrent workers: %v", err)
		break
	}
	if n := len(errs); n > 0 {
		t.Logf("additional failed checks: %d", n)
	}
	if err := lock.Check(ctx); err != nil {
		t.Fatal("lock unusable after checks", err)
	}
}
