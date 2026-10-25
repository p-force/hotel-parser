package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type PrettyHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	loc   *time.Location
	level slog.Leveler

	attrs  []slog.Attr
	groups []string
}

func New(w io.Writer, loc *time.Location) *slog.Logger {
	if loc == nil {
		loc = time.Local
	}

	return slog.New(&PrettyHandler{
		w:     w,
		mu:    &sync.Mutex{},
		loc:   loc,
		level: slog.LevelInfo,
	})
}

func (h *PrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	fmt.Fprintf(
		&b,
		"[%s] %s %s",
		r.Level.String(),
		r.Time.In(h.loc).Format("2006-01-02 15:04:05"),
		r.Message,
	)

	writeAttr := func(a slog.Attr, groups []string) {
		if a.Equal(slog.Attr{}) {
			return
		}

		key := strings.Join(append(append([]string(nil), groups...), a.Key), ".")
		fmt.Fprintf(&b, " %s=%s", key, a.Value.String())
	}

	for _, a := range h.attrs {
		writeAttr(a, nil)
	}

	r.Attrs(func(a slog.Attr) bool {
		writeAttr(a, h.groups)
		return true
	})

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append([]slog.Attr(nil), h.attrs...)

	for _, a := range attrs {
		key := strings.Join(
			append(append([]string(nil), h.groups...), a.Key),
			".",
		)

		clone.attrs = append(clone.attrs, slog.Attr{
			Key:   key,
			Value: a.Value,
		})
	}

	return &clone
}

func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}
