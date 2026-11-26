package collect

import (
	"context"
	"errors"
	"log/slog"

	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/storage"
)

// BookingRulesWorkerHook уточняет только подходящие наблюдения. Сбой уточнения
// не меняет основной результат и не запускает retry основного HTTP-запроса.
// Один coordinator должен обслуживать все волны текущего Service.Execute.
func BookingRulesWorkerHook(coordinator *BookingRulesCoordinator) WorkerHook {
	return func(ctx context.Context, store *storage.Store, client source.Client, job storage.Job, attempt *storage.AttemptInput) error {
		if !NeedsTravelLineBookingRules(job.Scenario, *attempt) {
			return nil
		}
		attrs := []any{"run_id", job.RunID, "job_id", job.ID, "property_id", job.Scenario.PropertyID, "source", job.Scenario.Source}
		failed := func(code string) {
			slog.WarnContext(ctx, "travelline.booking_rules.failed", append(attrs, "error_code", code)...)
		}
		bookingClient, ok := client.(BookingRulesClient)
		if !ok {
			failed("booking_rules_client_missing")
			return nil
		}
		if attempt.SelectionPolicy == nil || attempt.SelectionPolicy.Mode != "allowlist" || len(attempt.SelectionPolicy.RateIDs) == 0 {
			failed("booking_rules_policy_invalid")
			return nil
		}
		// PeriodEnd — максимальный check_out всего run, а не текущего job.
		run, err := store.GetRun(ctx, job.RunID)
		if err != nil {
			failed("booking_rules_run_read_error")
			return nil
		}
		rates := attempt.SelectionPolicy.RateIDs
		index, err := coordinator.Get(ctx, store, bookingClient, job.RunID, job.Scenario.SourcePropertyID, run.PeriodStart, run.PeriodEnd, rates)
		if err != nil {
			code := "booking_rules_unavailable"
			var collectionErr *source.CollectionError
			if errors.As(err, &collectionErr) && diagnosticCode.MatchString(collectionErr.Code) {
				code = collectionErr.Code
			}
			// Произвольный текст ошибки может содержать URL или ответ источника.
			failed(code)
			return nil
		}
		confirmed := ApplyTravelLineBookingRules(job.Scenario, attempt, index, rates)
		level := slog.LevelDebug
		if confirmed > 0 {
			level = slog.LevelInfo
		}
		slog.Log(ctx, level, "travelline.booking_rules.applied", append(attrs, "confirmed_count", confirmed)...)
		return nil
	}
}
