package collect

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
)

type RuntimeOptions struct {
	Batch               BatchOptions
	Claim               storage.ClaimOptions
	ExecutionTimeout    time.Duration // 0: только родительский context.
	ShutdownGracePeriod time.Duration // 0: 5 секунд на весь shutdown.
	RenewInterval       time.Duration // 0: треть срока lease.
}

type RuntimeResult struct {
	JobIDs        []string // Захваченные задания; не означает успешную запись.
	Progress      storage.JobProgress
	ProgressKnown bool // false: снимок не удалось прочитать, например истёк flush budget.
}

func (o RuntimeOptions) normalized() (RuntimeOptions, error) {
	if o.Batch.Workers == 0 {
		o.Batch.Workers = DefaultGlobalConcurrency
	}
	if o.ShutdownGracePeriod == 0 {
		o.ShutdownGracePeriod = 5 * time.Second
	}
	if o.Claim.LeaseDuration == 0 {
		o.Claim.LeaseDuration = time.Minute
	}
	if o.RenewInterval == 0 {
		o.RenewInterval = o.Claim.LeaseDuration / 3
	}
	if o.Claim.BatchSize == 0 {
		o.Claim.BatchSize = o.Batch.Workers
	}
	if o.Batch.Workers < 1 || o.Batch.Workers > MaxWorkers || o.Batch.StartInterval < 0 || o.ExecutionTimeout < 0 || o.ShutdownGracePeriod < 0 || o.Claim.BatchSize < 1 || o.Claim.BatchSize > o.Batch.Workers || o.RenewInterval <= 0 || o.RenewInterval >= o.Claim.LeaseDuration {
		return o, fmt.Errorf("invalid runtime options")
	}
	var err error
	o.Batch.Retry, err = o.Batch.Retry.normalized()
	return o, err
}

// shutdownContext даёт всем flush один общий grace budget после отмены execution.
// stop дожидается monitor; после возврата runtime фоновой работы не остаётся.
func shutdownContext(execution context.Context, grace time.Duration) (context.Context, func()) {
	flush, cancel := context.WithCancel(context.WithoutCancel(execution))
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-execution.Done():
		case <-stop:
			return
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel()
		case <-stop:
		}
	}()
	return flush, func() { close(stop); cancel(); <-done }
}

// RunQueued вычитывает готовые задания существующей PostgreSQL-очереди до её
// опустошения или отмены. CheckIn из PropertyCollection не используется: сценарий
// берётся из durable job. Все переданные конфигурации/клиенты переиспользуются.
// HTTP исполняет прежний bounded pool; второй очереди и HTTP retry loop здесь нет.
func RunQueued(ctx context.Context, store *storage.Store, properties []PropertyCollection, options RuntimeOptions) (result RuntimeResult, err error) {
	if store == nil {
		return result, fmt.Errorf("storage is required")
	}
	options, err = options.normalized()
	if err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if options.ExecutionTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.ExecutionTimeout)
		defer cancel()
	}
	ctx, stopExecution := context.WithCancelCause(ctx)
	defer stopExecution(nil)
	flush, stop := shutdownContext(ctx, options.ShutdownGracePeriod)
	defer stop()
	// Прогресс берём только из committed DB state, включая незахваченные jobs.
	defer func() {
		readCtx, cancel := context.WithTimeout(flush, options.ShutdownGracePeriod)
		defer cancel()
		var progressErr error
		result.Progress, progressErr = store.Progress(readCtx, options.Claim.RunID)
		result.ProgressKnown = progressErr == nil
		err = errors.Join(err, progressErr)
	}()
	byID := make(map[string]PropertyCollection, len(properties))
	clients := make(map[string]*bnovo.Client)
	for _, property := range properties {
		if err := property.Hotel.Validate(); err != nil {
			return result, err
		}
		if _, exists := byID[property.Hotel.ID]; exists {
			return result, fmt.Errorf("duplicate runtime property")
		}
		origin, e := SourceKey(property.Hotel.Source.BaseURL)
		if e != nil {
			return result, e
		}
		if property.Client == nil {
			property.Client = clients[origin]
			if property.Client == nil {
				property.Client, e = bnovo.NewClient(property.Hotel.Source.BaseURL, nil)
				if e != nil {
					return result, e
				}
				clients[origin] = property.Client
			}
		}
		if property.Client.SourceOrigin() != origin {
			return result, fmt.Errorf("runtime client does not match property source")
		}
		byID[property.Hotel.ID] = property
	}
	for ctx.Err() == nil {
		jobs, claimErr := store.ClaimJobs(ctx, options.Claim)
		if claimErr != nil {
			return result, claimErr
		}
		if len(jobs) == 0 {
			return result, ctx.Err()
		}
		for _, job := range jobs {
			result.JobIDs = append(result.JobIDs, job.ID)
		}
		if ctx.Err() != nil {
			break
		}
		if err := runLeasedJobs(ctx, flush, stopExecution, store, byID, jobs, options); err != nil {
			return result, err
		}
	}
	return result, ctx.Err()
}

type runtimeLease struct {
	mu       sync.Mutex
	job      storage.Job
	finished bool
}

func runLeasedJobs(ctx, flush context.Context, stopExecution context.CancelCauseFunc, store *storage.Store, properties map[string]PropertyCollection, jobs []storage.Job, options RuntimeOptions) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	states := make([]runtimeLease, len(jobs))
	input := make([]batchJob, len(jobs))
	for i, job := range jobs {
		property, exists := properties[job.Scenario.PropertyID]
		origin, e := SourceKey(property.Hotel.Source.BaseURL)
		if !exists || e != nil || origin != job.Scenario.SourceOrigin || property.Hotel.Source.Type != job.Scenario.Source || property.Hotel.Source.PropertyID != job.Scenario.SourcePropertyID || property.Client.SourceOrigin() != origin {
			return fmt.Errorf("queued job does not match runtime property/source")
		}
		states[i].job = job
		sc := job.Scenario
		input[i] = batchJob{client: property.Client, propertyID: sc.PropertyID, source: sc.Source,
			maxAttempts: job.MaxAttempts - job.Attempts,
			request:     bnovo.SearchRequest{PropertyID: sc.SourcePropertyID, CheckIn: sc.CheckIn, CheckOut: sc.CheckOut, Adults: sc.Adults, ChildrenAges: append([]int(nil), sc.ChildrenAges...), Language: sc.Language}}
	}
	// Единственный heartbeat на ограниченную пачку; не goroutine на задание.
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(options.RenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			for i := range states {
				state := &states[i]
				state.mu.Lock()
				var err error
				if !state.finished && ctx.Err() == nil {
					_, err = store.RenewLease(ctx, state.job.Lease(), options.Claim.LeaseDuration)
				}
				state.mu.Unlock()
				if err != nil {
					stopExecution(err)
					cancel(err)
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-heartbeatDone }()
	observer := &attemptObserver{
		before: func(ctx context.Context, index, number int, started time.Time) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			state := &states[index]
			state.mu.Lock()
			defer state.mu.Unlock()
			_, err := store.RenewLease(ctx, state.job.Lease(), options.Claim.LeaseDuration)
			if err != nil {
				stopExecution(err)
			}
			return err
		},
		after: func(_ context.Context, done completedAttempt, number int, next *time.Time) (writeErr error) {
			defer func() {
				if writeErr != nil {
					stopExecution(writeErr)
				}
			}()
			// Прерванный запрос не является завершением job; lease восстановит его.
			if interruptedAttempt(ctx, done.result) {
				return nil
			}
			writeCtx, end := context.WithTimeout(flush, options.ShutdownGracePeriod)
			defer end()
			if err := writeCtx.Err(); err != nil {
				return err
			}
			state := &states[done.index]
			if ctx.Err() != nil && next == nil && retryable(done.result) && state.job.Attempts+number < state.job.MaxAttempts {
				at := time.Now().Add(retryDelay(options.Batch.Retry, state.job.Attempts+number, done.result.Error.RetryAfter))
				next = &at
			}
			ValidateExpectedCategories(&done.result, properties[state.job.Scenario.PropertyID].Hotel.Inventory)
			attempt, err := storageAttemptContext(writeCtx, done.result, state.job.ID, state.job.Attempts+number, done.startedAt, done.finishedAt, done.durationMS, done.queueWaitMS, next)
			if err != nil {
				return err
			}
			attempt.Lease = ptrLease(state.job.Lease())
			state.mu.Lock()
			defer state.mu.Unlock()
			_, err = store.SaveAttempt(writeCtx, attempt)
			if err == nil && next == nil {
				state.finished = true
			}
			return err
		},
	}
	_, err := collectJobsObserved(ctx, input, options.Batch, observer)
	return errors.Join(err, context.Cause(ctx))
}

func ptrLease(lease storage.Lease) *storage.Lease { return &lease }

func interruptedAttempt(ctx context.Context, result bnovo.SearchResult) bool {
	return ctx.Err() != nil && result.Error != nil && (result.Error.Code == "canceled" || result.Error.Code == "timeout")
}
