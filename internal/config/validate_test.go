
package config

import (
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	valid := func() Config {
		return Config{
			ID:       "arena33",
			Name:     "Arena33",
			Type:     "apart_hotel",
			Role:     "own",
			Timezone: "Europe/Moscow",
			Source: SourceConfig{
				Type:       "bnovo",
				BaseURL:    "https://reservationsteps.ru",
				PropertyID: "7f3f84ae-1f13-419f-af60-ae782d454666",
			},
			Inventory: InventoryConfig{
				Source:     "manager_confirmed",
				TotalRooms: 19,
				Categories: []RoomCategory{
					{ID: "stadium", Name: "Stadium", SourceRoomID: "561429", TotalRooms: 10},
					{ID: "garden", Name: "Garden", SourceRoomID: "459946", TotalRooms: 4},
					{ID: "premium", Name: "Premium", SourceRoomID: "639687", TotalRooms: 5},
				},
			},
			Collection: CollectionConfig{
				Enabled: true, DaysAhead: 60,
				Adults: 2, Rooms: 1,
				Nights: []int{1, 2, 3},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "valid config",
			mutate:  func(*Config) {},
		},
		{
			name: "missing hotel id",
			mutate: func(c *Config) {
				c.ID = ""
			},
			wantErr: "id: required",
		},
		{
			name: "invalid timezone",
			mutate: func(c *Config) {
				c.Timezone = "Invalid/Timezone"
			},
			wantErr: "timezone:",
		},
		{
			name: "duplicate category id",
			mutate: func(c *Config) {
				c.Inventory.Categories[1].ID = "stadium"
			},
			wantErr: "duplicate",
		},
		{
			name: "duplicate source id",
			mutate: func(c *Config) {
				c.Inventory.Categories[1].SourceRoomID = "561429"
			},
			wantErr: "source_room_id: duplicate",
		},
		{
			name: "incorrect room total",
			mutate: func(c *Config) {
				c.Inventory.TotalRooms = 20
			},
			wantErr: "sum of categories",
		},
		{
			name: "negative category stock",
			mutate: func(c *Config) {
				c.Inventory.Categories[0].TotalRooms = -1
			},
			wantErr: "must be positive",
		},
		{
			name: "invalid horizon",
			mutate: func(c *Config) {
				c.Collection.DaysAhead = 0
			},
			wantErr: "days_ahead",
		},
		{
			name: "unsupported LOS",
			mutate: func(c *Config) {
				c.Collection.Nights = []int{1, 2, 4}
			},
			wantErr: "unsupported LOS",
		},
		{
			name: "duplicate LOS",
			mutate: func(c *Config) {
				c.Collection.Nights = []int{1, 2, 2}
			},
			wantErr: "duplicate LOS",
		},
		{
			name: "invalid adult count",
			mutate: func(c *Config) {
				c.Collection.Adults = 3
			},
			wantErr: "requires 2 adults",
		},
		{
			name: "children not supported",
			mutate: func(c *Config) {
				c.Collection.ChildrenAges = []int{5}
			},
			wantErr: "requires no children",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)

			err := cfg.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf(
					"Validate() error = %v, want substring %q",
					err, tt.wantErr,
				)
			}
		})
	}
}
