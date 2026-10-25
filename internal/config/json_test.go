package config

import (
	"path/filepath"
	"testing"
)

func TestLoadArena33(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "arena33.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.ID != "arena33" {
		t.Errorf("ID = %q, want arena33", cfg.ID)
	}

	if cfg.Inventory.TotalRooms != 19 {
		t.Errorf(
			"TotalRooms = %d, want 19",
			cfg.Inventory.TotalRooms,
		)
	}

	if len(cfg.Inventory.Categories) != 3 {
		t.Errorf(
			"categories = %d, want 3",
			len(cfg.Inventory.Categories),
		)
	}
}
