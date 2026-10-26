package logger

import (
	"fmt"
	"log/slog"
	"strings"
)

// writeBnovoAttrs меняет только текстовое представление известных событий Bnovo.
// Исходные записи и атрибуты остаются полными для JSONHandler.
func (h *PrettyHandler) writeBnovoAttrs(out *strings.Builder, record slog.Record) bool {
	switch record.Message {
	case "bnovo.collect.started", "bnovo.http.started", "bnovo.http.completed", "bnovo.parse.completed", "bnovo.collect.completed":
	default:
		return false
	}
	// Группированные сообщения сохраняют общее представление PrettyHandler.
	if len(h.groups) != 0 {
		return false
	}
	values := make(map[string]slog.Value, len(h.attrs)+record.NumAttrs())
	for _, attr := range h.attrs {
		values[attr.Key] = attr.Value.Resolve()
	}
	record.Attrs(func(attr slog.Attr) bool { values[attr.Key] = attr.Value.Resolve(); return true })
	for _, field := range []struct {
		key, label string
		duration   bool
	}{
		{"attempt_id", "attempt_id", false},
		{"status", "status", false},
		{"http_status", "http_status", false},
		{"categories_count", "categories", false},
		{"offers_count", "offers", false},
		{"warnings_count", "warnings", false},
		{"error_code", "error_code", false},
		{"collection_duration_ms", "total", true},
		{"http_duration_ms", "http", true},
		{"parse_duration_ms", "parse", true},
	} {
		value, exists := values[field.key]
		if !exists || (value.Kind() == slog.KindString && value.String() == "") || (value.Kind() == slog.KindAny && value.Any() == nil) {
			continue
		}
		if field.key == "warnings_count" && value.Kind() == slog.KindInt64 && value.Int64() == 0 {
			continue
		}
		fmt.Fprintf(out, " %s=%s", field.label, value.String())
		if field.duration {
			out.WriteString("ms")
		}
	}
	return true
}
