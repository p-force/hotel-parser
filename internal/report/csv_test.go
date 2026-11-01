package report

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAtomicPublication(t *testing.T) {
	for _, kind := range []string{"success", "write_error", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nested", "run.csv")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := publish(ctx, path, func(w io.Writer) error {
				if _, err := io.WriteString(w, "header\nrow\n"); err != nil {
					return err
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("partial file became visible", err)
				}
				if kind == "write_error" {
					return io.ErrClosedPipe
				}
				if kind == "canceled" {
					cancel()
				}
				return nil
			})
			if kind == "success" {
				data, e := os.ReadFile(path)
				if err != nil || e != nil || string(data) != "header\nrow\n" {
					t.Fatal(err, e, string(data))
				}
			} else {
				want := io.ErrClosedPipe
				if kind == "canceled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
				if _, e := os.Stat(path); !os.IsNotExist(e) {
					t.Fatal("failed publication left a ready file", e)
				}
			}
			files, e := os.ReadDir(filepath.Dir(path))
			if e != nil {
				t.Fatal(e)
			}
			for _, f := range files {
				if strings.HasSuffix(f.Name(), ".tmp") {
					t.Fatal("temporary file leaked", f.Name())
				}
			}
		})
	}
}
func TestConcurrentPublicationIsComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.csv")
	content := strings.Repeat("quoted,row\n", 2000)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- publish(context.Background(), path, func(w io.Writer) error { _, err := io.WriteString(w, content); return err })
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatal("incomplete concurrent publication", err)
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
}
func TestPathComponent(t *testing.T) {
	for _, s := range []string{"arena33", "../escape", "..", "/tmp/escape", "отель"} {
		got := pathComponent(s)
		if !component.MatchString(got) || got == ".." || strings.ContainsAny(got, "/\\") {
			t.Fatal(s, got)
		}
		if got != pathComponent(s) {
			t.Fatal("unstable path")
		}
	}
}
