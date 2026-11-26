package collect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
	"github.com/p-force/hotel-parser/internal/storage"
)

var _ BookingRulesStore = (*storage.Store)(nil)
var _ BookingRulesClient = (*travelline.Client)(nil)

type coordinatorStoreMock struct {
	schema       string
	reads, saves atomic.Int32
	get          func(context.Context, string) (storage.RunBookingRules, error)
	save         func(context.Context, storage.RunBookingRulesInput) (storage.RunBookingRules, error)
}

func (s *coordinatorStoreMock) Schema() string { return s.schema }
func (s *coordinatorStoreMock) GetRunBookingRules(ctx context.Context, id string) (storage.RunBookingRules, error) {
	s.reads.Add(1)
	if s.get != nil {
		return s.get(ctx, id)
	}
	return storage.RunBookingRules{}, fmt.Errorf("lookup: %w", storage.ErrNotFound)
}
func (s *coordinatorStoreMock) SaveRunBookingRules(ctx context.Context, in storage.RunBookingRulesInput) (storage.RunBookingRules, error) {
	s.saves.Add(1)
	if s.save != nil {
		return s.save(ctx, in)
	}
	return savedCoordinatorRules(in), nil
}

type coordinatorClientMock struct {
	calls atomic.Int32
	fetch func(context.Context, string, time.Time, time.Time, []string) (*travelline.BookingRulesResponse, error)
}

func (c *coordinatorClientMock) BookingRules(ctx context.Context, hotelID string, from, to time.Time, rates []string) (*travelline.BookingRulesResponse, error) {
	c.calls.Add(1)
	return c.fetch(ctx, hotelID, from, to, rates)
}

func savedCoordinatorRules(in storage.RunBookingRulesInput) storage.RunBookingRules {
	out := storage.RunBookingRules{RunID: in.RunID, RequestedAt: in.RequestedAt, ReceivedAt: in.ReceivedAt,
		StartDate: in.StartDate, EndDate: in.EndDate, RequestedRateIDs: append([]string(nil), in.RequestedRateIDs...),
		Rules: make([]storage.BookingRule, len(in.Rules))}
	for i, rule := range in.Rules {
		out.Rules[i] = storage.BookingRule{BookingRuleInput: rule}
	}
	return out
}

func coordinatorDates() (time.Time, time.Time) {
	from := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 0, 7)
}

func getCoordinatorRules(ctx context.Context, c *BookingRulesCoordinator, s BookingRulesStore, client BookingRulesClient, runID string) (travelline.BookingRulesIndex, error) {
	from, to := coordinatorDates()
	return c.Get(ctx, s, client, runID, "585", from, to, []string{"128299", "128301"})
}

func TestBookingRulesCoordinatorFetchAndCommit(t *testing.T) {
	response := decodeRulesForStorage(t, `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true}]}`)
	store := &coordinatorStoreMock{schema: "group"}
	var committed atomic.Bool
	store.save = func(_ context.Context, in storage.RunBookingRulesInput) (storage.RunBookingRules, error) {
		from, to := coordinatorDates()
		if in.RunID != "run" || !in.StartDate.Equal(from) || !in.EndDate.Equal(to) ||
			in.RequestedAt.IsZero() || in.ReceivedAt.Before(in.RequestedAt) ||
			strings.Join(in.RequestedRateIDs, ",") != "128299,128301" || len(in.Rules) != 1 ||
			in.Rules[0].RoomTypeID == nil || *in.Rules[0].RoomTypeID != "4347" || in.Rules[0].Forbidden == nil || !*in.Rules[0].Forbidden {
			t.Error("incorrect saved request", in)
		}
		committed.Store(true)
		return savedCoordinatorRules(in), nil
	}
	client := &coordinatorClientMock{fetch: func(_ context.Context, hotel string, from, to time.Time, rates []string) (*travelline.BookingRulesResponse, error) {
		wantFrom, wantTo := coordinatorDates()
		if hotel != "585" || !from.Equal(wantFrom) || !to.Equal(wantTo) || strings.Join(rates, ",") != "128299,128301" {
			t.Error("incorrect API request")
		}
		return response, nil
	}}
	c := NewBookingRulesCoordinator()
	for range 2 {
		index, err := getCoordinatorRules(context.Background(), c, store, client, "run")
		if err != nil || !committed.Load() {
			t.Fatal("rules returned without commit", err)
		}
		from, _ := coordinatorDates()
		if !travelline.ConfirmCategoryBookingForbidden(index, from, "4347", []string{"128299"}, 1) {
			t.Fatal("saved forbidden rule missing")
		}
	}
	if store.reads.Load() != 1 || store.saves.Load() != 1 || client.calls.Load() != 1 {
		t.Fatal("cache repeated I/O")
	}
}

func TestBookingRulesCoordinatorParallelCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	response := decodeRulesForStorage(t, `{"booking_rules":[]}`)
	client := &coordinatorClientMock{fetch: func(ctx context.Context, _ string, _, _ time.Time, _ []string) (*travelline.BookingRulesResponse, error) {
		close(entered)
		select {
		case <-release:
			return response, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	store, c := &coordinatorStoreMock{schema: "group"}, NewBookingRulesCoordinator()
	errs := make(chan error, 32)
	go func() { _, err := getCoordinatorRules(ctx, c, store, client, "run"); errs <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var started sync.WaitGroup
	started.Add(31)
	for range 31 {
		go func() {
			started.Done()
			_, err := getCoordinatorRules(ctx, c, store, client, "run")
			errs <- err
		}()
	}
	started.Wait()
	unblock()
	for range 32 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if store.reads.Load() != 1 || store.saves.Load() != 1 || client.calls.Load() != 1 {
		t.Fatal("parallel calls repeated I/O")
	}
}

func TestBookingRulesCoordinatorIndependentKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	from, to := coordinatorDates()
	store := &coordinatorStoreMock{schema: "group"}
	store.get = func(ctx context.Context, id string) (storage.RunBookingRules, error) {
		if id == "blocked" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return storage.RunBookingRules{}, ctx.Err()
			}
		}
		return storage.RunBookingRules{RunID: id, StartDate: from, EndDate: to, RequestedRateIDs: []string{"128299", "128301"}}, nil
	}
	c := NewBookingRulesCoordinator()
	done := make(chan error, 1)
	go func() { _, err := getCoordinatorRules(ctx, c, store, nil, "blocked"); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := getCoordinatorRules(ctx, c, store, nil, "other"); err != nil {
		t.Fatal("another run blocked", err)
	}
	otherSchema := &coordinatorStoreMock{schema: "another_group", get: func(context.Context, string) (storage.RunBookingRules, error) {
		return storage.RunBookingRules{RunID: "blocked", StartDate: from, EndDate: to, RequestedRateIDs: []string{"128301", "128299"}}, nil
	}}
	if _, err := getCoordinatorRules(ctx, c, otherSchema, nil, "blocked"); err != nil {
		t.Fatal("another schema blocked", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if store.reads.Load() != 2 || otherSchema.reads.Load() != 1 || store.saves.Load() != 0 {
		t.Fatal("keys were not isolated")
	}
}

func TestBookingRulesCoordinatorErrorsCached(t *testing.T) {
	failure := errors.New("mock failure")
	for _, stage := range []string{"read", "http", "convert", "save"} {
		t.Run(stage, func(t *testing.T) {
			store := &coordinatorStoreMock{schema: "group"}
			response := decodeRulesForStorage(t, `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true}]}`)
			client := &coordinatorClientMock{fetch: func(context.Context, string, time.Time, time.Time, []string) (*travelline.BookingRulesResponse, error) {
				if stage == "http" {
					return nil, failure
				}
				if stage == "convert" {
					return nil, nil
				}
				return response, nil
			}}
			if stage == "read" {
				store.get = func(context.Context, string) (storage.RunBookingRules, error) {
					return storage.RunBookingRules{}, failure
				}
			}
			if stage == "save" {
				store.save = func(context.Context, storage.RunBookingRulesInput) (storage.RunBookingRules, error) {
					return storage.RunBookingRules{}, failure
				}
			}
			c := NewBookingRulesCoordinator()
			var first error
			for range 2 {
				index, err := getCoordinatorRules(context.Background(), c, store, client, "run")
				if err == nil || (stage != "convert" && !errors.Is(err, failure)) {
					t.Fatal("expected original error", err)
				}
				if first != nil && err != first {
					t.Fatal("error was not cached")
				}
				first = err
				from, _ := coordinatorDates()
				if travelline.ConfirmCategoryBookingForbidden(index, from, "4347", []string{"128299"}, 1) {
					t.Fatal("unsaved rules escaped on error")
				}
			}
			wantHTTP, wantSave := int32(1), int32(0)
			if stage == "read" {
				wantHTTP = 0
			}
			if stage == "save" {
				wantSave = 1
			}
			if store.reads.Load() != 1 || client.calls.Load() != wantHTTP || store.saves.Load() != wantSave {
				t.Fatal("failure repeated I/O")
			}
		})
	}
}

func TestBookingRulesCoordinatorSavedAndCachedApplicability(t *testing.T) {
	from, to := coordinatorDates()
	for _, mismatch := range []string{"rates", "range"} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%v", mismatch, cached), func(t *testing.T) {
				saved := storage.RunBookingRules{RunID: "run", StartDate: from, EndDate: to, RequestedRateIDs: []string{"128299", "128301"},
					Rules: []storage.BookingRule{{BookingRuleInput: storage.BookingRuleInput{Date: from, RoomTypeID: bookingStorageField("4347"), RateID: "128299", Forbidden: bookingStorageField(true)}}}}
				store := &coordinatorStoreMock{schema: "group", get: func(context.Context, string) (storage.RunBookingRules, error) { return saved, nil }}
				c := NewBookingRulesCoordinator()
				if cached {
					if _, err := getCoordinatorRules(context.Background(), c, store, nil, "run"); err != nil {
						t.Fatal(err)
					}
				}
				rates, end := []string{"128299", "128301"}, to
				reason := "rate_policy_mismatch"
				if mismatch == "rates" {
					rates = []string{"128299"}
				} else {
					end, reason = to.AddDate(0, 0, 1), "date_range_not_covered"
				}
				index, err := c.Get(context.Background(), store, nil, "run", "585", from, end, rates)
				if err == nil || !strings.Contains(err.Error(), reason) {
					t.Fatal("incompatible set accepted", err)
				}
				if travelline.ConfirmCategoryBookingForbidden(index, from, "4347", []string{"128299"}, 1) || store.saves.Load() != 0 || store.reads.Load() != 1 {
					t.Fatal("incompatible call returned rules or repeated I/O")
				}
			})
		}
	}
}

// Done сообщает тесту, что ожидающий уже дошёл до select общего результата.
type coordinatorWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *coordinatorWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestBookingRulesCoordinatorWaiterCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	response := decodeRulesForStorage(t, `{"booking_rules":[]}`)
	client := &coordinatorClientMock{fetch: func(ctx context.Context, _ string, _, _ time.Time, _ []string) (*travelline.BookingRulesResponse, error) {
		close(entered)
		select {
		case <-release:
			return response, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	store, c := &coordinatorStoreMock{schema: "group"}, NewBookingRulesCoordinator()
	owner := make(chan error, 1)
	go func() { _, err := getCoordinatorRules(ctx, c, store, client, "run"); owner <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	waiterCtx := &coordinatorWaitingContext{Context: waitCtx, waiting: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() { _, err := getCoordinatorRules(waiterCtx, c, store, client, "run"); waiter <- err }()
	select {
	case <-waiterCtx.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelWait()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatal("waiter did not cancel", err)
	}
	unblock()
	if err := <-owner; err != nil {
		t.Fatal("waiter canceled owner", err)
	}
	if _, err := getCoordinatorRules(ctx, c, store, client, "run"); err != nil || client.calls.Load() != 1 || store.reads.Load() != 1 {
		t.Fatal("waiter changed shared result", err)
	}
}

func TestBookingRulesCoordinatorRestart(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%v", empty), func(t *testing.T) {
			body := `{"booking_rules":[{"room_types":["4347"],"code":"128299","date":"2026-11-25","forbidden":true}]}`
			if empty {
				body = `{"booking_rules":[]}`
			}
			response := decodeRulesForStorage(t, body)
			var saved storage.RunBookingRules
			store := &coordinatorStoreMock{schema: "group"}
			store.get = func(context.Context, string) (storage.RunBookingRules, error) {
				if saved.RunID == "" {
					return storage.RunBookingRules{}, storage.ErrNotFound
				}
				return saved, nil
			}
			store.save = func(_ context.Context, in storage.RunBookingRulesInput) (storage.RunBookingRules, error) {
				saved = savedCoordinatorRules(in)
				return saved, nil
			}
			client := &coordinatorClientMock{fetch: func(context.Context, string, time.Time, time.Time, []string) (*travelline.BookingRulesResponse, error) {
				return response, nil
			}}
			for range 2 {
				c := NewBookingRulesCoordinator()
				for range 2 {
					index, err := getCoordinatorRules(context.Background(), c, store, client, "run")
					if err != nil {
						t.Fatal(err)
					}
					from, _ := coordinatorDates()
					if got := travelline.ConfirmCategoryBookingForbidden(index, from, "4347", []string{"128299"}, 1); got == empty {
						t.Fatal("restored index differs")
					}
				}
			}
			if store.reads.Load() != 2 || store.saves.Load() != 1 || client.calls.Load() != 1 {
				t.Fatal("restart repeated HTTP or save")
			}
		})
	}
}

func TestBookingRulesCoordinatorNewInstanceRetriesFailure(t *testing.T) {
	response := decodeRulesForStorage(t, `{"booking_rules":[]}`)
	var fail atomic.Bool
	fail.Store(true)
	client := &coordinatorClientMock{fetch: func(context.Context, string, time.Time, time.Time, []string) (*travelline.BookingRulesResponse, error) {
		if fail.Load() {
			return nil, context.DeadlineExceeded
		}
		return response, nil
	}}
	store, c := &coordinatorStoreMock{schema: "group"}, NewBookingRulesCoordinator()
	if _, err := getCoordinatorRules(context.Background(), c, store, client, "run"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expected failure", err)
	}
	fail.Store(false)
	if _, err := getCoordinatorRules(context.Background(), c, store, client, "run"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("old coordinator retried", err)
	}
	if _, err := getCoordinatorRules(context.Background(), NewBookingRulesCoordinator(), store, client, "run"); err != nil {
		t.Fatal("new coordinator could not retry", err)
	}
	if client.calls.Load() != 2 || store.reads.Load() != 2 || store.saves.Load() != 1 {
		t.Fatal("unexpected retries")
	}
}

func TestBookingRulesCoordinatorWaitsForCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	response := decodeRulesForStorage(t, `{"booking_rules":[]}`)
	client := &coordinatorClientMock{fetch: func(context.Context, string, time.Time, time.Time, []string) (*travelline.BookingRulesResponse, error) {
		return response, nil
	}}
	store := &coordinatorStoreMock{schema: "group", save: func(ctx context.Context, in storage.RunBookingRulesInput) (storage.RunBookingRules, error) {
		close(entered)
		select {
		case <-release:
			return savedCoordinatorRules(in), nil
		case <-ctx.Done():
			return storage.RunBookingRules{}, ctx.Err()
		}
	}}
	c := NewBookingRulesCoordinator()
	owner := make(chan error, 1)
	go func() { _, err := getCoordinatorRules(ctx, c, store, client, "run"); owner <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitCtx := &coordinatorWaitingContext{Context: ctx, waiting: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() { _, err := getCoordinatorRules(waitCtx, c, store, client, "run"); waiter <- err }()
	select {
	case <-waitCtx.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-owner:
		t.Fatal("owner returned before commit", err)
	case err := <-waiter:
		t.Fatal("waiter returned before commit", err)
	default:
	}
	unblock()
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
	if err := <-waiter; err != nil {
		t.Fatal(err)
	}
}

func TestBookingRulesCoordinatorOwnerCancellationCached(t *testing.T) {
	parent, end := context.WithTimeout(context.Background(), 5*time.Second)
	defer end()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	entered := make(chan struct{})
	client := &coordinatorClientMock{fetch: func(ctx context.Context, _ string, _, _ time.Time, _ []string) (*travelline.BookingRulesResponse, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	store, c := &coordinatorStoreMock{schema: "group"}, NewBookingRulesCoordinator()
	done := make(chan error, 1)
	go func() { _, err := getCoordinatorRules(ctx, c, store, client, "run"); done <- err }()
	select {
	case <-entered:
	case <-parent.Done():
		t.Fatal(parent.Err())
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("owner did not cancel", err)
	}
	if _, err := getCoordinatorRules(parent, c, store, client, "run"); !errors.Is(err, context.Canceled) {
		t.Fatal("owner error was not cached", err)
	}
	if store.reads.Load() != 1 || store.saves.Load() != 0 || client.calls.Load() != 1 {
		t.Fatal("canceled owner repeated I/O or saved data")
	}
}
