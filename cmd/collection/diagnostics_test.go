package main

import (
	"context"
	"strings"
	"testing"
)

func TestDiagnosticsRequiresExplicitRunAndGroup(t *testing.T) {
	for _, args := range [][]string{{"diagnostics"}, {"diagnostics", "--group-id", "arena33"}, {"diagnostics", "--run-id", "run"}, {"diagnostics", "--group-id", "arena33", "--run-id", "run", "--due"}, {"resume", "--run-id", "run"}} {
		err := run(context.Background(), args)
		if err == nil || (!strings.Contains(err.Error(), "diagnostics requires") && !strings.Contains(err.Error(), "require diagnostics")) {
			t.Fatalf("unexpected validation for %v: %v", args, err)
		}
	}
}
