package config

type Config struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Timezone   string           `json:"timezone"`
	Source     SourceConfig     `json:"source"`
	Inventory  InventoryConfig  `json:"inventory"`
	Collection CollectionConfig `json:"collection"`
	Rates      []RateConfig     `json:"rates,omitempty"`
}

type SourceConfig struct {
	Type       string `json:"type"`
	BaseURL    string `json:"base_url"`
	PropertyID string `json:"property_id"`
}

type InventoryConfig struct {
	// TotalRooms обязателен в JSON; nil означает неизвестный максимум, а не ноль.
	TotalRooms *int           `json:"total_rooms"`
	Source     string         `json:"source"`
	Categories []RoomCategory `json:"categories"`
}

type RoomCategory struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SourceRoomID string `json:"source_room_id"`
	// nil означает неизвестный максимум категории; JSON-поле обязательно.
	TotalRooms *int `json:"total_rooms"`
}

type CollectionConfig struct {
	Enabled      bool        `json:"enabled"`
	DaysAhead    int         `json:"days_ahead"`
	Adults       AdultCounts `json:"adults"`
	ChildrenAges []int       `json:"children_ages"`
	Rooms        int         `json:"rooms"`
	Nights       []int       `json:"nights"`
}

// RateConfig — справочник тарифов; TravelLine использует Enabled как allowlist публичных тарифов.
type RateConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}
