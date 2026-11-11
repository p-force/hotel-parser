package vercelapp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/p-force/hotel-parser/internal/schedule"
	"github.com/p-force/hotel-parser/internal/storage"
)

// Path Schedules use a shared CRON_SECRET, not a signed identity assertion.
// Only parse metadata after matching that secret. A manual Trigger request cannot
// acquire a Schedule identity by adding ce-* headers.
var scheduleIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)

type scheduleMetadata struct {
	ExecutionID, Name string
	ScheduledAt       time.Time
}

func usableSecret(secret string, minimum int) bool {
	if len(secret) < minimum || strings.TrimSpace(secret) != secret {
		return false
	}
	for _, b := range []byte(secret) {
		if b < 32 || b > 126 {
			return false
		}
	}
	return true
}
func cronBearer(header, secret string) bool {
	if !usableSecret(secret, 16) {
		return false
	}
	return bearerMinimum(header, secret, 16)
}

func readScheduleMetadata(headers http.Header, action string) (*scheduleMetadata, error) {
	present := false
	for k := range headers {
		if strings.HasPrefix(strings.ToLower(k), "ce-") {
			present = true
			break
		}
	}
	if !present {
		return nil, nil
	} // Authorized manual/legacy Cron request.
	name := headers.Get("ce-vssschedulename")
	expected := "morning-" + action
	if name != expected || (action != "start" && action != "resume") || headers.Get("ce-type") != "com.vercel.schedule.v1beta" || headers.Get("ce-specversion") != "1.0" || headers.Get("ce-vssnamespace") != "default" || headers.Get("ce-source") != "/schedule/default/"+name || !scheduleIdentifier.MatchString(headers.Get("ce-id")) || !scheduleIdentifier.MatchString(headers.Get("ce-vssscheduleid")) || headers.Get("ce-vssschedulesource") != "static" {
		return nil, errors.New("invalid schedule metadata")
	}
	raw := headers.Get("ce-vssscheduledat")
	if len(raw) > 64 {
		return nil, errors.New("invalid schedule time")
	}
	at, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil || at.IsZero() {
		return nil, errors.New("invalid schedule time")
	}
	return &scheduleMetadata{ExecutionID: headers.Get("ce-id"), Name: name, ScheduledAt: at.UTC()}, nil
}

type executionLog struct {
	logger       *slog.Logger
	ctx          context.Context
	action       string
	actual       time.Time
	measured     time.Time
	authorized   bool
	metadata     *scheduleMetadata
	status, code string
	httpStatus   int
	responses    []schedule.Response
}

func (x *executionLog) attrs() []any {
	a := []any{"action", x.action, "actual_started_at", x.actual.UTC()}
	if x.metadata != nil {
		a = append(a, "schedule_execution_id", x.metadata.ExecutionID, "schedule_name", x.metadata.Name, "scheduled_at", x.metadata.ScheduledAt, "delay_ms", x.actual.Sub(x.metadata.ScheduledAt).Milliseconds(), "metadata_auth", "cron_secret")
	}
	return a
}
func (x *executionLog) started() {
	if x != nil {
		x.logger.InfoContext(x.ctx, "schedule.execution.started", x.attrs()...)
	}
}
func (x *executionLog) finish() {
	if x == nil {
		return
	}
	a := x.attrs()
	a = append(a, "http_status", x.httpStatus, "duration_ms", time.Since(x.measured).Milliseconds())
	if x.code != "" {
		a = append(a, "error_code", x.code)
	}
	level := slog.LevelInfo
	if x.httpStatus >= 400 {
		level = slog.LevelError
		if x.httpStatus == 401 || x.httpStatus == 409 || x.code == "collection_disabled" {
			level = slog.LevelWarn
		}
	}
	status := x.status
	if status == "" {
		status = "completed"
		if x.httpStatus >= 400 {
			status = "failed"
		}
	}
	if len(x.responses) > 0 {
		last := x.responses[len(x.responses)-1]
		a = append(a, "stage", last.Stage, "progress_known", last.ProgressKnown, "claimed_jobs", last.ClaimedJobs)
		if summary, ok := resultSummary(x.action, x.responses).(map[string]int); ok {
			for _, key := range []string{"planned_jobs", "completed_jobs", "remaining_jobs", "queued", "running", "retryable", "succeeded", "partial", "no_offers", "failed"} {
				a = append(a, key, summary[key])
			}
		}
		if last.ProgressKnown {
			pending, partial, failed := x.progress(last)
			if x.httpStatus < 400 && x.status == "" {
				if pending > 0 {
					status = "incomplete"
				} else if failed > 0 {
					status = "completed_with_failures"
				} else if partial > 0 {
					status = "partial"
				} else if responsePlanned, _ := resultSummary(x.action, x.responses).(map[string]int); responsePlanned["planned_jobs"] == 0 {
					status = "empty"
				}
			}
		}
	}
	a = append(a, "status", status)
	event := "schedule.execution.completed"
	if !x.authorized {
		event = "schedule.request.rejected"
	}
	x.logger.Log(x.ctx, level, event, a...)
}

func progressKey(group string, p storage.RunProgress) string { return group + "/" + p.Run.ID }
func planned(p storage.RunProgress) int {
	return p.Remaining + p.Succeeded + p.Partial + p.NoOffers + p.Failed + p.Canceled
}
func (x *executionLog) progress(response schedule.Response) (pending, partial, failed int) {
	before := map[string]storage.RunProgress{}
	for _, g := range response.Before {
		for _, p := range g.Runs {
			before[progressKey(g.GroupID, p)] = p
		}
	}
	for _, g := range response.Groups {
		for _, p := range g.Runs {
			if !runSelected(x.action, response, g.GroupID, p) {
				continue
			}
			pending += p.Remaining
			partial += p.Partial
			failed += p.Failed
			old, known := before[progressKey(g.GroupID, p)]
			same := known && planned(old) == planned(p)
			reason := "incomplete"
			switch {
			case x.code == "runner_busy":
				reason = "runner_busy"
			case p.Remaining == 0:
				reason = "complete"
			case same && p.Remaining < old.Remaining:
				reason = "advanced"
			case p.Ready == 0 && p.WaitingBackoff > 0:
				reason = "waiting_backoff"
			case p.Ready == 0 && p.WaitingLease > 0:
				reason = "waiting_lease"
			case response.ClaimedJobs == 0:
				reason = "no_jobs_claimed"
			}
			a := x.attrs()
			a = append(a, "group_id", g.GroupID, "run_id", p.Run.ID, "logical_slot", p.Run.StartedAt.UTC(), "scope", "run", "planned_jobs", planned(p), "queued", p.Queued, "running", p.Running, "retryable", p.Retryable, "succeeded", p.Succeeded, "partial", p.Partial, "no_offers", p.NoOffers, "failed", p.Failed, "canceled", p.Canceled, "remaining_after", p.Remaining, "ready_jobs", p.Ready, "waiting_lease", p.WaitingLease, "waiting_backoff", p.WaitingBackoff, "same_job_set", same, "progress_status", reason)
			if same {
				a = append(a, "remaining_before", old.Remaining)
			}
			x.logger.InfoContext(x.ctx, "collection.progress", a...)
			// Stateless invocations use persisted attempts/job creation, not a process map.
			// A successful retry attempt counts as activity even if remaining stays equal.
			if same && p.Remaining > 0 && p.Remaining >= old.Remaining && !p.ObservedAt.IsZero() && !p.LastActivityAt.IsZero() && p.ObservedAt.Sub(p.LastActivityAt) >= 30*time.Minute {
				x.logger.WarnContext(x.ctx, "collection.no_progress", append(a, "no_progress_ms", p.ObservedAt.Sub(p.LastActivityAt).Milliseconds())...)
			}
		}
	}
	return
}

// A joined database/deadline error must not be hidden behind HTTP409.
func onlyRunnerBusy(err error) bool {
	if err == storage.ErrRunnerBusy {
		return true
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		children := many.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, e := range children {
			if !onlyRunnerBusy(e) {
				return false
			}
		}
		return true
	}
	if child := errors.Unwrap(err); child != nil {
		return onlyRunnerBusy(child)
	}
	return false
}

func runSelected(action string, response schedule.Response, groupID string, p storage.RunProgress) bool {
	if action == "start" {
		for _, r := range response.Planned {
			if r.ID == p.Run.ID {
				return true
			}
		}
		return false
	}
	if action == "resume" {
		if response.Before == nil {
			return p.Remaining > 0 || p.Run.Status == storage.RunRunning
		}
		for _, g := range response.Before {
			if g.GroupID == groupID {
				for _, old := range g.Runs {
					if old.Run.ID == p.Run.ID {
						return old.Remaining > 0 || old.Run.Status == storage.RunRunning
					}
				}
			}
		}
		return false
	}
	return true
}
