package schedule

import (
	"context"
	"fmt"
	"io"

	"github.com/p-force/hotel-parser/internal/collect"
)

// WriteRunDiagnostics только читает завершённую историю выбранной группы.
// Не захватывает runner lock, не планирует задания и не отправляет HTTP.
func (s *Service) WriteRunDiagnostics(ctx context.Context, groupID, runID string, output io.Writer) error {
	for _, group := range s.groups {
		if group.ID == groupID {
			return collect.WriteRunDiagnostics(ctx, group.Store, runID, output)
		}
	}
	return fmt.Errorf("unknown diagnostic group")
}
