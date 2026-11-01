package schedule

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/report"
	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/storage"
)

type Service struct {
	registry     Registry
	groups       []collect.RuntimeGroup
	options      collect.RuntimeOptions
	csvDirectory string
}
type Request struct {
	Action string    `json:"action"`
	Slot   time.Time `json:"slot,omitempty"`
}
type GroupProgress struct {
	GroupID string                `json:"group_id"`
	Schema  string                `json:"schema"`
	Runs    []storage.RunProgress `json:"runs"`
}
type Response struct {
	Planned       []storage.Run     `json:"planned,omitempty"`
	Groups        []GroupProgress   `json:"groups"`
	ProgressKnown bool              `json:"progress_known"`
	Exports       []report.Artifact `json:"exports,omitempty"`
}

// Open подключает все группы к одной БД. DDL выполняет только явный Migrate.
func Open(ctx context.Context, dsn string, registry Registry, options collect.RuntimeOptions) (*Service, error) {
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	if options.ExecutionTimeout < 0 || options.ShutdownGracePeriod < 0 || options.MaxJobs < 0 || options.Batch.Workers < 0 || options.Batch.Workers > collect.MaxWorkers || options.RunnerLock != nil {
		return nil, fmt.Errorf("invalid service options")
	}
	directory := os.Getenv("HOTEL_PARSER_CSV_DIR")
	if directory == "" {
		directory = filepath.Join(os.TempDir(), "hotel-parser-csv")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("invalid CSV directory")
	}
	service := &Service{registry: registry, options: options, csvDirectory: directory}
	if service.options.Claim.Owner == "" {
		service.options.Claim.Owner = rand.Text()
	}
	if service.options.ExecutionTimeout == 0 {
		service.options.ExecutionTimeout = 45 * time.Second
	}
	if service.options.ShutdownGracePeriod == 0 {
		service.options.ShutdownGracePeriod = 5 * time.Second
	}
	if service.options.MaxJobs == 0 {
		service.options.MaxJobs = 64
	}
	if options.Claim.RunID != "" || options.Claim.PropertyID != "" {
		return nil, fmt.Errorf("service drains all unfinished runs")
	}
	clients := map[string]source.Client{}
	for _, g := range registry.Groups {
		store, err := storage.Open(ctx, dsn, storage.Options{Schema: g.Schema})
		if err != nil {
			service.Close()
			return nil, err
		}
		group := collect.RuntimeGroup{ID: g.ID, Store: store}
		for _, p := range g.Properties {
			origin, e := collect.SourceKey(p.Hotel.Source.BaseURL)
			if e != nil {
				store.Close()
				service.Close()
				return nil, e
			}
			client := clients[collect.SourceClientKey(p.Hotel, origin)]
			if client == nil {
				client, e = collect.NewSourceClient(p.Hotel)
				if e != nil {
					store.Close()
					service.Close()
					return nil, e
				}
				clients[collect.SourceClientKey(p.Hotel, origin)] = client
			}
			group.Properties = append(group.Properties, collect.PropertyCollection{Hotel: p.Hotel, Client: client})
		}
		service.groups = append(service.groups, group)
	}
	return service, nil
}
func (s *Service) Close() {
	for _, g := range s.groups {
		_ = g.Store.Close()
	}
}
func (s *Service) Migrate(ctx context.Context) error {
	for _, g := range s.groups {
		if err := g.Store.Migrate(ctx); err != nil {
			return err
		}
	}
	return nil
}

func ValidateRequest(req Request) error {
	switch req.Action {
	case "plan", "tick":
		_, err := SlotHorizon(req.Slot)
		return err
	case "resume":
		if !req.Slot.IsZero() {
			return fmt.Errorf("resume does not accept slot")
		}
		return nil
	default:
		return fmt.Errorf("action must be plan, resume or tick")
	}
}

// Execute — общий application service для CLI и HTTP. Tick сначала продолжает
// ограниченную пачку старой очереди, затем публикует слот и продолжает исполнение.
func (s *Service) Execute(parent context.Context, req Request) (response Response, err error) {
	if err := ValidateRequest(req); err != nil {
		return response, err
	}
	ctx, cancel := context.WithTimeout(parent, s.options.ExecutionTimeout)
	defer cancel()
	defer func() {
		// Runtime уже использовал весь общий grace budget на HTTP/flush. Не начинаем
		// ещё один grace period для отчёта после deadline/отмены.
		response.Groups, err = s.progress(ctx, err)
		response.ProgressKnown = response.Groups != nil
		response.Exports = s.exportCSV(ctx, response.Groups)
	}()
	options := s.options
	options.ExecutionTimeout = 0
	var guard *storage.RunnerLock
	if req.Action != "plan" {
		guard, err = s.groups[0].Store.TryRunnerLock(ctx)
		if err != nil {
			return response, err
		}
		defer guard.Close()
		options.RunnerLock = guard
		// Между разными процессами in-memory lastStart не сохраняется. Тихий
		// интервал перед новым runner исключает burst на обычной передаче lock.
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return response, ctx.Err()
		}
	}
	if req.Action == "tick" {
		first := options
		workers := first.Batch.Workers
		if workers == 0 {
			workers = collect.DefaultGlobalConcurrency
		}
		first.MaxJobs = min(options.MaxJobs, workers)
		// Медленный старый HTTP не должен съесть всё время публикации нового слота.
		deadline, _ := ctx.Deadline()
		reserve := time.Until(deadline) / 4
		if reserve <= 0 {
			return response, context.DeadlineExceeded
		}
		first.ExecutionTimeout = reserve
		first.ShutdownGracePeriod = min(options.ShutdownGracePeriod, reserve)
		results, e := collect.RunGroups(ctx, s.groups, first)
		if e != nil && !(errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil) {
			return response, e
		}
		// Не скрываем timeout: сначала публикуем слот в зарезервированное время,
		// затем возвращаем исходную ошибку вместе с durable progress.
		err = e
		for _, r := range results {
			options.MaxJobs -= len(r.JobIDs)
		}
	}
	if req.Action != "resume" {
		plans, e := BuildPlans(s.registry, req.Slot)
		if e != nil {
			return response, e
		}
		stores := map[string]*storage.Store{}
		for _, g := range s.groups {
			stores[g.ID] = g.Store
		}
		for _, p := range plans {
			run, e := stores[p.GroupID].PlanRun(ctx, p.Run, p.Scenarios)
			if e != nil {
				return response, e
			}
			response.Planned = append(response.Planned, run)
		}
	}
	if req.Action != "plan" && options.MaxJobs > 0 {
		_, drainErr := collect.RunGroups(ctx, s.groups, options)
		err = errors.Join(err, drainErr)
	}
	return response, err
}

func (s *Service) progress(ctx context.Context, prior error) ([]GroupProgress, error) {
	groups := make([]GroupProgress, 0, len(s.groups))
	for _, g := range s.groups {
		runs, err := g.Store.RunProgress(ctx)
		if err != nil {
			return nil, errors.Join(prior, err)
		}
		for i := range runs {
			p := &runs[i]
			finishedNow := false
			if p.Remaining == 0 && p.Run.Status == storage.RunRunning {
				run, e := g.Store.FinishRun(ctx, p.Run.ID)
				if e != nil && !errors.Is(e, storage.ErrIncomplete) {
					return nil, errors.Join(prior, e)
				}
				if e == nil {
					p.Run = run
					finishedNow = true
				}
			}
			// Старые закрытые run остаются в ответе/истории, но не повторяются в INFO каждую минуту.
			if p.Remaining > 0 || finishedNow {
				attrs := []any{"group_id", g.ID, "schema", g.Store.Schema(), "run_id", p.Run.ID, "property_id", p.Run.PropertyID, "queued", p.Queued, "running", p.Running, "retryable", p.Retryable, "succeeded", p.Succeeded, "partial", p.Partial, "no_offers", p.NoOffers, "failed", p.Failed, "remaining", p.Remaining}
				if p.Run.FinishedAt != nil {
					attrs = append(attrs, "finished_at", *p.Run.FinishedAt)
				}
				slog.InfoContext(ctx, "collection.run.progress", attrs...)
			}
		}
		groups = append(groups, GroupProgress{GroupID: g.ID, Schema: g.Store.Schema(), Runs: runs})
	}
	return groups, prior
}

// Экспорт запускается после остановки workers и освобождения runner lock (LIFO
// defer в Execute). Ошибка CSV возвращается отдельно от результата collection.
// Terminal runs остаются в PostgreSQL и на следующем вызове проверяются снова:
// поэтому сбой диска/процесса или исчезновение ephemeral filesystem восстановимы.
func (s *Service) exportCSV(ctx context.Context, groups []GroupProgress) []report.Artifact {
	var artifacts []report.Artifact
	stores := make(map[string]*storage.Store, len(s.groups))
	for _, g := range s.groups {
		stores[g.ID] = g.Store
	}
	for _, g := range groups {
		// Сначала свежие завершённые runs, чтобы старый архив не задерживал новые.
		for i := len(g.Runs) - 1; i >= 0; i-- {
			run := g.Runs[i].Run
			if run.Status == storage.RunRunning || run.FinishedAt == nil {
				continue
			}
			artifact, err := report.Generate(ctx, stores[g.GroupID], g.GroupID, s.csvDirectory, run.ID)
			artifacts = append(artifacts, artifact)
			if err != nil {
				slog.WarnContext(ctx, "collection.csv.failed", "run_id", run.ID, "group_id", g.GroupID, "path", artifact.Path, "error_code", artifact.ErrorCode, "retry_pending", artifact.RetryPending)
			} else if artifact.Generated {
				slog.InfoContext(ctx, "collection.csv.generated", "run_id", run.ID, "group_id", g.GroupID, "path", artifact.Path, "ready", true)
			}
			if ctx.Err() != nil {
				return artifacts
			}
		}
	}
	return artifacts
}
