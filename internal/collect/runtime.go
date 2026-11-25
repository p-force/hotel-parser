package collect

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
)

type RuntimeOptions struct {
	MaxJobs             int                 // 0: ограничение только execution deadline.
	RunnerLock          *storage.RunnerLock // Guard application-service для Bnovo.
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
	if o.MaxJobs < 0 || o.Batch.Workers < 1 || o.Batch.Workers > MaxWorkers || o.Batch.StartInterval < 0 || o.ExecutionTimeout < 0 || o.ShutdownGracePeriod < 0 || o.Claim.BatchSize < 1 || o.Claim.BatchSize > o.Batch.Workers || o.RenewInterval <= 0 || o.RenewInterval >= o.Claim.LeaseDuration {
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

// RuntimeGroup привязывает конфигурации к явному Store; ID может повторяться только
// у отелей разных групп, но не у самих групп или их схем.
type RuntimeGroup struct {
	ID         string
	Store      *storage.Store
	Properties []PropertyCollection
}
type RuntimeGroupResult struct {
	GroupID, Schema string
	RuntimeResult
}

func RunQueued(ctx context.Context, store *storage.Store, properties []PropertyCollection, options RuntimeOptions) (RuntimeResult, error) {
	groups, err := RunGroups(ctx, []RuntimeGroup{{ID: "single", Store: store, Properties: properties}}, options)
	if len(groups) == 0 {
		return RuntimeResult{}, err
	}
	return groups[0].RuntimeResult, err
}

// RunGroups использует один pool. Claim идёт по кругу между группами и отелями;
// большая очередь не захватывает всю пачку раньше небольшой.
func RunGroups(ctx context.Context, groups []RuntimeGroup, options RuntimeOptions) (results []RuntimeGroupResult, err error) {
	options, err = options.normalized()
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
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
	groups = append([]RuntimeGroup(nil), groups...)
	seenIDs, seenSchemas := map[string]bool{}, map[string]bool{}
	clients := map[string]source.Client{}
	for i := range groups {
		g := &groups[i]
		if g.ID == "" || g.Store == nil || seenIDs[g.ID] || seenSchemas[g.Store.Schema()] {
			return nil, fmt.Errorf("invalid or duplicate runtime group/schema")
		}
		seenIDs[g.ID], seenSchemas[g.Store.Schema()] = true, true
		seenProperties := map[string]bool{}
		g.Properties = append([]PropertyCollection(nil), g.Properties...)
		for j := range g.Properties {
			property := &g.Properties[j]
			if err := property.Hotel.Validate(); err != nil {
				return nil, err
			}
			if seenProperties[property.Hotel.ID] {
				return nil, fmt.Errorf("duplicate runtime property")
			}
			seenProperties[property.Hotel.ID] = true
			origin, e := SourceKey(property.Hotel.Source.BaseURL)
			if e != nil {
				return nil, e
			}
			if property.Client == nil {
				property.Client = clients[SourceClientKey(property.Hotel, origin)]
				if property.Client == nil {
					property.Client, e = NewSourceClient(property.Hotel)
					if e != nil {
						return nil, e
					}
					clients[SourceClientKey(property.Hotel, origin)] = property.Client
				}
			}
			if property.Client.SourceOrigin() != origin || property.Client.SourceType() != property.Hotel.Source.Type {
				return nil, fmt.Errorf("runtime client does not match property source")
			}
		}
		results = append(results, RuntimeGroupResult{GroupID: g.ID, Schema: g.Store.Schema()})
	}
	defer func() {
		for i, g := range groups {
			readCtx, cancel := context.WithTimeout(flush, options.ShutdownGracePeriod)
			var e error
			results[i].Progress, e = g.Store.Progress(readCtx, options.Claim.RunID)
			results[i].ProgressKnown = e == nil
			cancel()
			err = errors.Join(err, e)
		}
	}()
	groupOrder := make([]int, len(groups))
	groupLast := make([]time.Time, len(groups))
	for i := range groups {
		history, e := groups[i].Store.ClaimHistory(ctx)
		if e != nil {
			return results, e
		}
		groupOrder[i] = i
		for _, at := range history {
			if at.After(groupLast[i]) {
				groupLast[i] = at
			}
		}
		sort.SliceStable(groups[i].Properties, func(a, b int) bool {
			return history[groups[i].Properties[a].Hotel.ID].Before(history[groups[i].Properties[b].Hotel.ID])
		})
	}
	sort.SliceStable(groupOrder, func(a, b int) bool { return groupLast[groupOrder[a]].Before(groupLast[groupOrder[b]]) })
	cursor := 0
	propertyCursor := make([]int, len(groups))
	claimedCount := 0
	for ctx.Err() == nil && (options.MaxJobs == 0 || claimedCount < options.MaxJobs) {
		wave := make([]leasedJob, 0, options.Claim.BatchSize)
		empty := 0
		for len(wave) < options.Claim.BatchSize && empty < len(groups) && ctx.Err() == nil && (options.MaxJobs == 0 || claimedCount < options.MaxJobs) {
			index := groupOrder[cursor]
			cursor = (cursor + 1) % len(groups)
			g := groups[index]
			found := false
			for tries := 0; tries < len(g.Properties); tries++ {
				p := g.Properties[propertyCursor[index]%len(g.Properties)]
				propertyCursor[index]++
				claim := options.Claim
				claim.BatchSize = 1
				claim.PropertyID = p.Hotel.ID
				jobs, e := g.Store.ClaimJobs(ctx, claim)
				if e != nil {
					return results, e
				}
				if len(jobs) == 0 {
					continue
				}
				wave = append(wave, leasedJob{groupID: g.ID, store: g.Store, property: p, job: jobs[0]})
				results[index].JobIDs = append(results[index].JobIDs, jobs[0].ID)
				claimedCount++
				found = true
				break
			}
			if found {
				empty = 0
			} else {
				empty++
			}
		}
		if len(wave) == 0 {
			return results, ctx.Err()
		}
		if ctx.Err() != nil {
			break
		}
		if err := runLeasedJobs(ctx, flush, stopExecution, wave, options); err != nil {
			return results, err
		}
	}
	return results, ctx.Err()
}

type leasedJob struct {
	groupID  string
	store    *storage.Store
	property PropertyCollection
	job      storage.Job
}

type runtimeLease struct {
	mu sync.Mutex
	leasedJob
	finished bool
}

func runLeasedJobs(ctx, flush context.Context, stopExecution context.CancelCauseFunc, jobs []leasedJob, options RuntimeOptions) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	states := make([]runtimeLease, len(jobs))
	input := make([]batchJob, len(jobs))
	for i, item := range jobs {
		job, property := item.job, item.property
		origin, e := SourceKey(property.Hotel.Source.BaseURL)
		if e != nil || origin != job.Scenario.SourceOrigin || property.Hotel.Source.Type != job.Scenario.Source || property.Hotel.Source.PropertyID != job.Scenario.SourcePropertyID || (property.Client.SourceOrigin() != origin || property.Client.SourceType() != property.Hotel.Source.Type) {
			return fmt.Errorf("queued job does not match runtime property/source")
		}
		states[i].leasedJob = item
		sc := job.Scenario
		input[i] = batchJob{groupID: item.groupID, client: property.Client, propertyID: sc.PropertyID, source: sc.Source, maxAttempts: job.MaxAttempts - job.Attempts,
			request: bnovo.SearchRequest{PropertyID: sc.SourcePropertyID, CheckIn: sc.CheckIn, CheckOut: sc.CheckOut, Adults: sc.Adults, ChildrenAges: append([]int(nil), sc.ChildrenAges...), Language: sc.Language}}
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
					_, err = state.store.RenewLease(ctx, state.job.Lease(), options.Claim.LeaseDuration)
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
			if options.RunnerLock != nil {
				if err := options.RunnerLock.Check(ctx); err != nil {
					stopExecution(err)
					return err
				}
			}
			_, err := state.store.RenewLease(ctx, state.job.Lease(), options.Claim.LeaseDuration)
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
			ValidateExpectedCategories(&done.result, state.property.Hotel.Inventory)
			attempt, err := storageAttemptContext(writeCtx, done.result, state.job.ID, state.job.Attempts+number, done.startedAt, done.finishedAt, done.durationMS, done.queueWaitMS, next)
			if err != nil {
				return err
			}
			attempt.SelectionPolicy = selectionPolicy(state.property.Hotel)
			attempt.Lease = ptrLease(state.job.Lease())
			state.mu.Lock()
			defer state.mu.Unlock()
			_, err = state.store.SaveAttempt(writeCtx, attempt)
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
