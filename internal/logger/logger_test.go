package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoggerWithAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer

	log := New(&buf, time.UTC)

	dbLog := log.
		WithGroup("database").
		With("host", "localhost").
		With("port", 5432).
		WithGroup("query")

	dbLog.Info("query executed", "duration_ms", 10)

	got := buf.String()

	expected := []string{
		"database.host=localhost",
		"database.port=5432",
		"database.query.duration_ms=10",
	}

	for _, attr := range expected {
		if !strings.Contains(got, attr) {
			t.Errorf("expected %q in log, got: %s", attr, got)
		}
	}

	unexpected := []string{
		"database.query.host=",
		"database.query.port=",
	}

	for _, attr := range unexpected {
		if strings.Contains(got, attr) {
			t.Errorf("unexpected %q in log: %s", attr, got)
		}
	}
}

func TestFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name, format, level    string
		json, debug, wantError bool
	}{
		{name: "default_text"},
		{name: "text_debug", format: "text", level: "DEBUG", debug: true},
		{name: "json", format: "json", level: "INFO", json: true},
		{name: "json_debug", format: "json", level: "DEBUG", json: true, debug: true},
		{name: "invalid_format", format: "secret-invalid-format", wantError: true},
		{name: "invalid_level", level: "secret-invalid-level", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOTEL_PARSER_LOG_FORMAT", tc.format)
			t.Setenv("HOTEL_PARSER_LOG_LEVEL", tc.level)
			var buf bytes.Buffer
			log, err := FromEnv(&buf, time.FixedZone("UTC+3", 3*60*60))
			if tc.wantError {
				if err == nil {
					t.Fatal("ожидалась ошибка конфигурации")
				}
				if strings.Contains(err.Error(), "secret-") {
					t.Errorf("в ошибку попало значение настройки: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			log.Debug("debug event")
			log.Info("info event", "count", 2)
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			wantLines := 1
			if tc.debug {
				wantLines = 2
			}
			if len(lines) != wantLines {
				t.Fatalf("неверная фильтрация уровня: %s", buf.String())
			}
			line := lines[len(lines)-1]
			if tc.json {
				var fields map[string]any
				if err := json.Unmarshal([]byte(line), &fields); err != nil {
					t.Fatal(err)
				}
				if fields["level"] != "INFO" || fields["msg"] != "info event" || fields["count"] != float64(2) {
					t.Errorf("неверный JSON: %v", fields)
				}
				timestamp, err := time.Parse(time.RFC3339Nano, fields["time"].(string))
				if err != nil {
					t.Fatal(err)
				}
				_, offset := timestamp.Zone()
				if offset != 3*60*60 {
					t.Errorf("неверный часовой пояс: %s", fields["time"])
				}
			} else if !strings.HasPrefix(line, "[INFO] ") || !strings.HasSuffix(line, " info event count=2") {
				t.Errorf("ожидался исходный текстовый формат: %s", line)
			}
		})
	}
}

func TestPrettyHandlerExactFormat(t *testing.T) {
	var buf bytes.Buffer
	loc := time.FixedZone("UTC+3", 3*60*60)
	log := New(&buf, loc)
	// Проверяем и формат времени, и преобразование из UTC в часовой пояс логгера.
	timestamp := time.Date(2026, 10, 25, 21, 47, 4, 0, time.UTC)
	for _, tc := range []struct {
		level   slog.Level
		message string
		attrs   []slog.Attr
	}{
		{slog.LevelInfo, "hotel-parser started", nil},
		{slog.LevelWarn, "failed to load timezone", nil},
		{slog.LevelError, "database connection failed", []slog.Attr{slog.Any("error", errors.New("connection refused"))}},
	} {
		record := slog.NewRecord(timestamp, tc.level, tc.message, 0)
		record.AddAttrs(tc.attrs...)
		if err := log.Handler().Handle(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	const want = "[INFO] 2026-10-26 00:47:04 hotel-parser started\n" +
		"[WARN] 2026-10-26 00:47:04 failed to load timezone\n" +
		"[ERROR] 2026-10-26 00:47:04 database connection failed error=connection refused\n"
	if got := buf.String(); got != want {
		t.Errorf("неверный формат:\nполучено: %q\nожидалось: %q", got, want)
	}
}
