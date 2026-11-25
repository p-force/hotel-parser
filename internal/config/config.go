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

// InventoryConfig хранит независимые физический фонд и наблюдаемый публичный максимум.
// nil означает неизвестное значение; один показатель не подменяет другой.
type InventoryConfig struct {
	PhysicalTotalRooms *int   `json:"physical_total_rooms"`
	PublicObservedMax  *int   `json:"public_observed_max"`
	PhysicalSource     string `json:"physical_source"`
	PublicSource       string `json:"public_source"`
	// Проверка суммы физических категорий разрешена только для подтверждённого разбиения.
	CategoriesFormCompletePartition bool           `json:"categories_form_complete_partition,omitempty"`
	Categories                      []RoomCategory `json:"categories"`
}

type RoomCategory struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	SourceRoomID       string `json:"source_room_id"`
	PhysicalTotalRooms *int   `json:"physical_total_rooms"`
	PublicObservedMax  *int   `json:"public_observed_max"`
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
