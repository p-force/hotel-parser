package report

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"path"
	"strings"
	"time"

	"github.com/p-force/hotel-parser/internal/storage"
)

// Deliver выполняет ограниченный delivery step, отдельно от collection status.
// Общий context step ограничивает время; SQL lease (2 min) длиннее всего step.
func (u *YandexDiskUploader) Deliver(ctx context.Context, store *storage.Store, groupID, root string, limit int) []storage.Delivery {
	root = strings.TrimPrefix(root, "disk:")
	if root == "" {
		root = "/hotel-parser"
	}
	if !strings.HasPrefix(root, "/") || strings.Contains(root, "\x00") || path.Clean(root) != root {
		slog.WarnContext(ctx, "collection.delivery.failed", "group_id", groupID, "error_code", "disk_directory_invalid")
		return nil
	}
	if err := store.EnsureDeliveries(ctx, groupID, root); err != nil {
		slog.WarnContext(ctx, "collection.delivery.failed", "group_id", groupID, "error_code", "delivery_enqueue_failed")
		return nil
	}
	if u.token == "" {
		ds, err := store.Deliveries(ctx, groupID, 20)
		if err != nil {
			return nil
		}
		for i := range ds {
			if ds[i].State != "delivered" {
				ds[i].ErrorCode = "disk_token_missing"
			}
		}
		return ds
	}
	var result []storage.Delivery
	for range limit {
		if ctx.Err() != nil {
			break
		}
		d, err := store.ClaimDelivery(ctx, groupID)
		if errors.Is(err, storage.ErrNotFound) {
			break
		}
		if err != nil {
			slog.WarnContext(ctx, "collection.delivery.failed", "group_id", groupID, "error_code", "delivery_claim_failed")
			break
		}
		// Защищаем lease даже при использовании Deliver вне Service.
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = u.upload(attemptCtx, d.RemotePath, func(ctx context.Context, w io.Writer) error {
			_, err := store.WriteRunCSV(ctx, groupID, d.RunID, w)
			return err
		})
		cancel()
		code := ""
		delay := time.Duration(0)
		if err != nil {
			code = "disk_internal_error"
			var diskErr *diskError
			// bounded exponential backoff с jitter, без sleep внутри invocation.
			base := min(5*time.Second*time.Duration(1<<min(d.Attempts-1, 7)), 10*time.Minute)
			delay = base + time.Duration(rand.Int64N(int64(base/4)+1))
			if errors.As(err, &diskErr) {
				code = diskErr.code
				delay = max(delay, diskErr.retryAfter)
				if diskErr.permanent {
					delay = max(delay, 6*time.Hour)
				}
			}
		}
		// Если context закончился, claim истечёт сам. Не объявляем delivered без commit.
		if e := store.CompleteDelivery(ctx, d, code, delay); e != nil {
			d.ErrorCode = "delivery_commit_unconfirmed"
			result = append(result, d)
			break
		}
		d.ErrorCode = code
		d.State = "delivered"
		if code != "" {
			d.State = "retryable_failed"
		}
		d.NextAttemptAt = time.Now().Add(delay)
		result = append(result, d)
		attrs := []any{"group_id", groupID, "run_id", d.RunID, "state", d.State}
		level := slog.LevelInfo
		if code != "" {
			attrs = append(attrs, "error_code", code)
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "collection.delivery.completed", attrs...)
	}
	return result
}
