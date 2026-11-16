// Package vercelapp содержит только HTTP-границу временного Vercel deployment.
package vercelapp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	configassets "github.com/p-force/hotel-parser/configs"
	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/logger"
	"github.com/p-force/hotel-parser/internal/schedule"
	"github.com/p-force/hotel-parser/internal/storage"
)

const databaseHost = "ep-hidden-breeze-b9oouvds.c-7.eu-central-1.aws.neon.tech"

type application interface {
	Execute(context.Context, schedule.Request) (schedule.Response, error)
	Close()
}
type handler struct {
	log      *slog.Logger
	env      func(string) string
	now      func() time.Time
	registry func() (schedule.Registry, error)
	open     func(context.Context, string, schedule.Registry, collect.RuntimeOptions) (application, error)
	status   func(context.Context, string, schedule.Registry) (any, error)
}

var logOnce sync.Once

// Serve не запускает фоновой работы и не выполняет DDL. Все ресурсы закрываются до ответа.
func Serve(action string, w http.ResponseWriter, r *http.Request) {
	logOnce.Do(func() {
		loc, _ := time.LoadLocation("Europe/Moscow")
		log, err := logger.FromEnv(os.Stderr, loc)
		if err == nil {
			slog.SetDefault(log)
		}
	})
	h := handler{env: os.Getenv, now: time.Now,
		registry: loadRegistry,
		open: func(ctx context.Context, dsn string, registry schedule.Registry, options collect.RuntimeOptions) (application, error) {
			return schedule.Open(ctx, dsn, registry, options)
		}, status: readStatus,
	}
	h.serve(action, w, r)
}

// Штатный loader ожидает файлы. Материализуем встроенные публичные JSON в /tmp,
// читаем их тем же loader и сразу удаляем: cwd и постоянный диск не требуются.
func loadRegistry() (schedule.Registry, error) {
	directory, err := os.MkdirTemp("", "hotel-parser-config-")
	if err != nil {
		return schedule.Registry{}, err
	}
	defer os.RemoveAll(directory)
	entries, err := configassets.Files.ReadDir(".")
	if err != nil {
		return schedule.Registry{}, err
	}
	for _, entry := range entries {
		data, err := configassets.Files.ReadFile(entry.Name())
		if err != nil {
			return schedule.Registry{}, err
		}
		if err := os.WriteFile(directory+"/"+entry.Name(), data, 0600); err != nil {
			return schedule.Registry{}, err
		}
	}
	return schedule.LoadRegistry(directory + "/groups.json")
}

func bearer(header, secret string) bool {
	return bearerMinimum(header, secret, 32)
}
func bearerMinimum(header, secret string, minimum int) bool {
	if !usableSecret(secret, minimum) {
		return false
	}
	got, want := sha256.Sum256([]byte(header)), sha256.Sum256([]byte("Bearer "+secret))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func (h handler) serve(action string, w http.ResponseWriter, r *http.Request) {
	actual := h.now()
	var execution *executionLog
	if action == "start" || action == "resume" {
		log := h.log
		if log == nil {
			log = slog.Default()
		}
		execution = &executionLog{logger: log, ctx: r.Context(), action: action, actual: actual, measured: time.Now(), httpStatus: 500}
		defer execution.finish()
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	var closeResources func()
	reply := func(status int, value any) {
		// The Function can freeze as soon as the response is published.
		if closeResources != nil {
			closeResources()
		}
		if execution != nil {
			execution.httpStatus = status
			execution.finish()
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	fail := func(status int, code string) {
		if execution != nil {
			execution.code = code
		}
		reply(status, map[string]string{"error": code})
	}
	trigger := h.env("HOTEL_PARSER_TRIGGER_SECRET")
	if len(trigger) < 32 {
		fail(503, "trigger_secret_missing_or_invalid")
		return
	}
	cronAction := action == "plan" || action == "resume" || action == "start"
	authorized := bearer(r.Header.Get("Authorization"), trigger)
	cronAuthorized := false
	if cronAction && r.Method == http.MethodGet {
		cronAuthorized = cronBearer(r.Header.Get("Authorization"), h.env("CRON_SECRET"))
		authorized = authorized || cronAuthorized
	}
	if !authorized {
		fail(401, "unauthorized")
		return
	}
	if execution != nil {
		execution.authorized = true
		if cronAuthorized {
			metadata, err := readScheduleMetadata(r.Header, action)
			if err != nil {
				fail(400, "schedule_metadata_invalid")
				return
			}
			execution.metadata = metadata
		}
		execution.started()
	}
	if r.URL.RawQuery != "" {
		fail(400, "query_not_allowed")
		return
	}
	if action != "health" && action != "status" && !cronAction {
		fail(404, "not_found")
		return
	}
	if r.Method != http.MethodGet && !(cronAction && r.Method == http.MethodPost) {
		w.Header().Set("Allow", "GET")
		if cronAction {
			w.Header().Set("Allow", "GET, POST")
		}
		fail(405, "method_not_allowed")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 {
		fail(400, "body_not_allowed")
		return
	}
	registry, err := h.registry()
	if err != nil {
		fail(503, "registry_unavailable")
		return
	}
	if action == "health" {
		reply(200, map[string]any{"status": "ok", "database_configured": h.env("HOTEL_PARSER_DATABASE_URL") != "", "delivery_configured": h.env("YANDEX_DISK_TOKEN") != "", "collection_enabled": h.env("HOTEL_PARSER_COLLECTION_ENABLED") == "true", "groups": len(registry.Groups)})
		return
	}
	dsn := h.env("HOTEL_PARSER_DATABASE_URL")
	if !validDatabase(dsn) {
		fail(503, "database_target_invalid_or_missing")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 250*time.Second)
	defer cancel()
	if action == "status" {
		result, err := h.status(ctx, dsn, registry)
		if err != nil {
			fail(503, "status_unavailable")
			return
		}
		reply(200, result)
		return
	}
	// Разрешение старта отделено от наличия секретов: их добавление не включает сбор.
	if h.env("HOTEL_PARSER_COLLECTION_ENABLED") != "true" {
		fail(503, "collection_disabled")
		return
	}
	if h.env("YANDEX_DISK_TOKEN") == "" {
		fail(503, "disk_token_missing")
		return
	}
	maxJobs := 96
	if value := h.env("HOTEL_PARSER_MAX_JOBS"); value != "" {
		maxJobs, err = strconv.Atoi(value)
		if err != nil || maxJobs < 1 || maxJobs > 96 {
			fail(503, "max_jobs_invalid")
			return
		}
	}
	requests := []schedule.Request{{Action: "resume"}}
	if execution != nil && execution.metadata != nil && action == "resume" {
		loc, _ := time.LoadLocation("Europe/Moscow")
		at := execution.metadata.ScheduledAt.In(loc)
		if at.Hour() == 8 && at.Minute() == 0 {
			execution.status = "initial_resume_skipped"
			reply(200, map[string]any{"results": []schedule.Response{}, "status": "initial_resume_skipped"})
			return
		}
	}
	if action == "start" {
		loc, _ := time.LoadLocation("Europe/Moscow")
		now := actual.In(loc)
		slot := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, loc)
		if slot.After(now) {
			if execution != nil {
				execution.status = "slot_not_due"
			}
			reply(200, map[string]any{"results": []schedule.Response{}, "status": "slot_not_due"})
			return
		}
		requests = []schedule.Request{{Action: "start", Slot: slot}}
	}
	openCtx, stopOpen := context.WithTimeout(ctx, 8*time.Second)
	service, err := h.open(openCtx, dsn, registry, collect.RuntimeOptions{Batch: collect.BatchOptions{Workers: 8}, Claim: storage.ClaimOptions{NewestRunFirst: true}, ExecutionTimeout: 220 * time.Second, ShutdownGracePeriod: 5 * time.Second, MaxJobs: maxJobs})
	stopOpen()
	if err != nil {
		fail(503, "database_open_failed")
		return
	}
	closeResources = sync.OnceFunc(service.Close)
	defer closeResources()
	if action == "plan" {
		slots, err := schedule.DueSlots(h.now())
		if err != nil {
			fail(503, "slots_unavailable")
			return
		}
		requests = nil
		for _, slot := range slots {
			requests = append(requests, schedule.Request{Action: "plan", Slot: slot})
		}
	}
	responses := make([]schedule.Response, 0, len(requests))
	for _, request := range requests {
		response, err := service.Execute(ctx, request)
		responses = append(responses, response)
		if execution != nil {
			execution.responses = responses
		}
		if err != nil {
			code, status := "collection_incomplete", http.StatusServiceUnavailable
			if onlyRunnerBusy(err) {
				code, status = "runner_busy", http.StatusConflict
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = "timeout"
			} else if errors.Is(err, context.Canceled) {
				code = "canceled"
			} else if response.Stage == "planning" {
				code = "planning_failed"
			}
			if execution != nil {
				execution.code = code
			}
			reply(status, map[string]any{"results": responses, "summary": resultSummary(action, responses), "error": code})
			return
		}
	}
	reply(200, map[string]any{"results": responses, "summary": resultSummary(action, responses)})
}

func resultSummary(action string, responses []schedule.Response) any {
	// Счётчики относятся к durable состоянию слота (start) или очереди (resume),
	// а не выдают claim за подтверждённый commit текущего вызова.
	var summary any
	if len(responses) > 0 {
		last := responses[len(responses)-1]
		if last.ProgressKnown {
			planned, completed, remaining := 0, 0, 0
			counts := map[string]int{}
			for _, group := range last.Groups {
				for _, run := range group.Runs {
					if !runSelected(action, last, group.GroupID, run) {
						continue
					}
					terminal := run.Succeeded + run.Partial + run.NoOffers + run.Failed + run.Canceled
					completed += terminal
					remaining += run.Remaining
					planned += terminal + run.Remaining
					counts["queued"] += run.Queued
					counts["running"] += run.Running
					counts["retryable"] += run.Retryable
					counts["succeeded"] += run.Succeeded
					counts["partial"] += run.Partial
					counts["no_offers"] += run.NoOffers
					counts["failed"] += run.Failed
					counts["canceled"] += run.Canceled
				}
			}
			counts["planned_jobs"], counts["completed_jobs"], counts["remaining_jobs"] = planned, completed, remaining
			summary = counts
		}
	}
	return summary
}

// Фиксируем разрешённый новый Neon. Ошибочная/старая БД и transaction pooler
// отклоняются до соединения; advisory locks требуют одной PostgreSQL-сессии.
func validDatabase(dsn string) bool {
	if dsn == "" {
		return false
	}
	c, err := pgx.ParseConfig(dsn)
	if err != nil || c.Host != databaseHost || c.Database != "neondb" || c.TLSConfig == nil {
		return false
	}
	for _, fallback := range c.Fallbacks {
		if fallback.Host != databaseHost || fallback.TLSConfig == nil {
			return false
		}
	}
	return true
}

// Status только читает: не FinishRun, не экспортирует CSV, не создаёт delivery.
func readStatus(ctx context.Context, dsn string, registry schedule.Registry) (any, error) {
	type group struct {
		schedule.GroupProgress
		Deliveries []storage.Delivery `json:"deliveries"`
	}
	groups := make([]group, 0, len(registry.Groups))
	for _, g := range registry.Groups {
		store, err := storage.Open(ctx, dsn, storage.Options{Schema: g.Schema})
		if err != nil {
			return nil, err
		}
		runs, err := store.RunProgress(ctx)
		if err != nil {
			store.Close()
			return nil, err
		}
		deliveries, err := store.Deliveries(ctx, g.ID, 100)
		store.Close()
		if err != nil {
			return nil, err
		}
		groups = append(groups, group{GroupProgress: schedule.GroupProgress{GroupID: g.ID, Schema: g.Schema, Runs: runs}, Deliveries: deliveries})
	}
	return map[string]any{"groups": groups, "delivery_limit_per_group": 100}, nil
}
