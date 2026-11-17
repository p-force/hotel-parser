package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCronOptInAndQueueRecovery(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mock-collection")
	log := filepath.Join(dir, "calls")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$*\" >> \"$CALLS_FILE\"\nif [ \"$1\" = plan ]; then exit 1; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COLLECTION_BIN", bin)
	t.Setenv("CALLS_FILE", log)
	t.Setenv("COLLECTION_GROUPS", "configs/groups.json")
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("HOTEL_PARSER_COLLECTION_ENABLED", enabled)
		cmd := exec.Command("sh", "../../deploy/collection-cron.sh", "minute")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
		data, err := os.ReadFile(log)
		if enabled == "false" {
			if !os.IsNotExist(err) {
				t.Fatal("disabled cron executed", string(data), err)
			}
			continue
		}
		if err != nil || !strings.Contains(string(data), "plan --due") || !strings.Contains(string(data), "resume --groups") {
			t.Fatal(string(data), err)
		}
	}
	if err := os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "../../deploy/collection-cron.sh", "tick", "08")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.HasPrefix(string(data), "start --groups") || !strings.Contains(string(data), "T08:00:00+03:00") {
		t.Fatal(string(data), err)
	}
}
