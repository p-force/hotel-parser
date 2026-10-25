
package logger

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPrettyHandler_WithAttrsAndGroups(t *testing.T) {
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
