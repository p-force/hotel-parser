// Package report публикует локальные CSV-артефакты, воспроизводимые из PostgreSQL.
package report

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/p-force/hotel-parser/internal/storage"
)

type Artifact struct {
	GroupID      string `json:"group_id"`
	Schema       string `json:"schema"`
	PropertyID   string `json:"property_id"`
	RunID        string `json:"run_id"`
	Path         string `json:"path,omitempty"`
	Ready        bool   `json:"ready"`
	Generated    bool   `json:"generated"`
	ErrorCode    string `json:"error_code,omitempty"`
	RetryPending bool   `json:"retry_pending"`
}

var component = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func pathComponent(s string) string {
	if component.MatchString(s) {
		return s
	}
	return fmt.Sprintf("id-%x", sha256.Sum256([]byte(s)))
}

// Generate использует только committed terminal run. Готовый файл неизменяем и
// локален: отсутствие на другом инстансе приводит к повторной генерации из БД.
// Существование файла не означает доставку внешнему сервису.
func Generate(ctx context.Context, store *storage.Store, groupID, directory, runID string) (artifact Artifact, err error) {
	artifact = Artifact{GroupID: groupID, Schema: store.Schema(), RunID: runID}
	defer func() {
		if err != nil {
			artifact.Ready = false
			artifact.RetryPending = true
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				artifact.ErrorCode = "csv_context_ended"
			} else if artifact.ErrorCode == "" {
				artifact.ErrorCode = "csv_file_error"
			}
		}
	}()
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		artifact.ErrorCode = "csv_read_error"
		return artifact, err
	}
	artifact.PropertyID = run.PropertyID
	if run.Status == storage.RunRunning || run.FinishedAt == nil {
		artifact.ErrorCode = "csv_run_incomplete"
		return artifact, storage.ErrIncomplete
	}
	artifact.Path = filepath.Join(directory, "v2", pathComponent(store.Schema()), pathComponent(groupID), pathComponent(run.PropertyID), run.StartedAt.UTC().Format("20060102T150405Z"), run.ID+".csv")
	if err := ctx.Err(); err != nil {
		return artifact, err
	}
	info, err := os.Stat(artifact.Path)
	if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		artifact.Ready = true
		return artifact, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return artifact, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return artifact, fmt.Errorf("CSV target is not a regular file")
	}

	err = publish(ctx, artifact.Path, func(output io.Writer) error {
		_, err := store.WriteRunCSV(ctx, groupID, run.ID, output)
		if err != nil {
			artifact.ErrorCode = "csv_write_error"
		}
		return err
	})
	if err != nil {
		return artifact, err
	}
	artifact.Ready = true
	artifact.Generated = true
	return artifact, nil
}

// Публикуем только закрытый файл на том же filesystem. Незавершённые .tmp
// никогда не возвращаются как готовые и не мешают восстановлению после crash.
func publish(ctx context.Context, path string, write func(io.Writer) error) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0750); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	if err := write(file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
