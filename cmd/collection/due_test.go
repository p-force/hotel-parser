package main

import (
	"context"
	"strings"
	"testing"
)

func TestDueFlagValidation(t *testing.T) {
	for _, args := range [][]string{{"resume", "--due"}, {"migrate", "--due"}, {"serve", "--due"}, {"plan", "--due", "--slot=2026-11-05T08:00:00+03:00"}} {
		if err := run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "--due requires") {
			t.Fatal(args, err)
		}
	}
}
