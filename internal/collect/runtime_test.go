package collect

import (
	"context"
	"errors"
	"github.com/p-force/hotel-parser/internal/storage"
	"testing"
	"time"
)

func TestRuntimeOptions(t *testing.T) {
	o, err := (RuntimeOptions{}).normalized()
	if err != nil || o.Batch.Workers != 8 || o.Claim.BatchSize != 8 || o.ShutdownGracePeriod != 5*time.Second || o.RenewInterval != 20*time.Second {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	for _, o := range []RuntimeOptions{
		{ExecutionTimeout: -1}, {ShutdownGracePeriod: -1}, {Batch: BatchOptions{Workers: -1}},
		{Batch: BatchOptions{Workers: 1}, Claim: storage.ClaimOptions{BatchSize: 2}}, {RenewInterval: time.Minute},
	} {
		if _, err := o.normalized(); err == nil {
			t.Fatalf("invalid options accepted: %+v", o)
		}
	}
}

func TestShutdownContextAndNormalizationCancellation(t *testing.T) {
	execution, cancel := context.WithCancel(context.Background())
	flush, stop := shutdownContext(execution, 20*time.Millisecond)
	defer stop()
	if flush.Err() != nil {
		t.Fatal("flush canceled without grace")
	}
	cancel()
	select {
	case <-flush.Done():
	case <-time.After(time.Second):
		t.Fatal("flush budget did not expire")
	}
	_, err := storageAttemptContext(flush, canceledAttempt(execution, batchRequests(1)[0]), "job", 1, time.Now(), time.Now(), 0, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("normalization ignored context: %v", err)
	}
}
