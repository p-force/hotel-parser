package collect

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Сигнализируем о входе в select ожидания допуска, без sleep и сравнений времени.
type admissionContext struct {
	context.Context
	waiting chan struct{}
}

func (c admissionContext) Done() <-chan struct{} { c.waiting <- struct{}{}; return c.Context.Done() }

func TestGlobalAdmissionWaitAndCancellation(t *testing.T) {
	globalLimit(t, 1)
	slots, unregister := registerBatch()
	defer unregister()
	if active, ok := acquireScenario(context.Background(), slots); !ok || active != 1 {
		t.Fatal("initial admission failed")
	}
	defer releaseScenario(slots)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan struct{}, 1)
	done := make(chan bool, 1)
	go func() { _, ok := acquireScenario(admissionContext{Context: ctx, waiting: waiting}, slots); done <- ok }()
	receive(t, waiting)
	cancel()
	if receive(t, done) {
		t.Fatal("canceled waiter acquired a full global budget")
	}
	if len(slots) != 1 {
		t.Fatal("canceled waiter consumed a permit")
	}
}

func TestPropertiesPreCanceledDeadline(t *testing.T) {
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP request") }, 0)
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		code := "canceled"
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			code = "timeout"
		}
		results, summary, err := CollectProperties(ctx, propertyInputs(t, client), BatchOptions{})
		if err != nil || len(results) != 9 || summary.NotStartedCount != 9 || summary.CanceledCount != 0 {
			t.Fatalf("summary=%+v err=%v", summary, err)
		}
		for _, result := range results {
			if result.QueueWaitMS != nil || result.Result.Error.Code != code || !result.Result.CollectedAt.IsZero() {
				t.Fatalf("bad unstarted result: %+v", result)
			}
		}
	}
}
