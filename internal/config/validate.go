package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (c Config) Validate() error {
	required := map[string]string{
		"id":                 c.ID,
		"name":               c.Name,
		"type":               c.Type,
		"role":               c.Role,
		"timezone":           c.Timezone,
		"source.type":        c.Source.Type,
		"source.base_url":    c.Source.BaseURL,
		"source.property_id": c.Source.PropertyID,
		"inventory.source":   c.Inventory.Source,
	}

	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s: required field", field)
		}
	}

	if c.Type != "apart_hotel" && c.Type != "hotel" {
		return fmt.Errorf("type: unsupported value %q", c.Type)
	}

	if c.Role != "own" && c.Role != "competitor" {
		return fmt.Errorf("role: unsupported value %q", c.Role)
	}

	if c.Source.Type != "bnovo" {
		return fmt.Errorf("source.type: unsupported value %q", c.Source.Type)
	}

	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}

	u, err := url.Parse(c.Source.BaseURL)
	if err != nil || u.Host == "" || u.Scheme != "https" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return fmt.Errorf("source.base_url: invalid HTTPS origin")
	}

	if err := c.Inventory.Validate(); err != nil {
		return fmt.Errorf("inventory: %w", err)
	}

	rateIDs := make(map[string]struct{}, len(c.Rates))
	for index, rate := range c.Rates {
		if strings.TrimSpace(rate.ID) == "" {
			return fmt.Errorf("rates[%d].id: required field", index)
		}
		if strings.TrimSpace(rate.Name) == "" {
			return fmt.Errorf("rates[%d].name: required field", index)
		}
		if _, exists := rateIDs[rate.ID]; exists {
			return fmt.Errorf("rates[%d].id: duplicate %q", index, rate.ID)
		}
		rateIDs[rate.ID] = struct{}{}
	}

	if err := c.Collection.Validate(); err != nil {
		return fmt.Errorf("collection: %w", err)
	}

	return nil
}

func (i InventoryConfig) Validate() error {
	if i.TotalRooms != nil && *i.TotalRooms <= 0 {
		return fmt.Errorf("total_rooms: must be positive")
	}

	if len(i.Categories) == 0 {
		return fmt.Errorf("categories: must not be empty")
	}

	ids := make(map[string]struct{})
	sourceIDs := make(map[string]struct{})

	var total int64
	allKnown := true

	for index, category := range i.Categories {
		field := fmt.Sprintf("categories[%d]", index)

		if strings.TrimSpace(category.ID) == "" {
			return fmt.Errorf("%s.id: required field", field)
		}
		if strings.TrimSpace(category.Name) == "" {
			return fmt.Errorf("%s.name: required field", field)
		}
		if strings.TrimSpace(category.SourceRoomID) == "" {
			return fmt.Errorf("%s.source_room_id: required field", field)
		}
		if category.TotalRooms != nil && *category.TotalRooms <= 0 {
			return fmt.Errorf("%s.total_rooms: must be positive", field)
		}

		if _, exists := ids[category.ID]; exists {
			return fmt.Errorf("%s.id: duplicate %q", field, category.ID)
		}
		ids[category.ID] = struct{}{}

		if _, exists := sourceIDs[category.SourceRoomID]; exists {
			return fmt.Errorf(
				"%s.source_room_id: duplicate %q",
				field, category.SourceRoomID,
			)
		}
		sourceIDs[category.SourceRoomID] = struct{}{}

		if category.TotalRooms == nil {
			allKnown = false
		} else {
			total += int64(*category.TotalRooms)
		}
	}

	if i.TotalRooms != nil && allKnown && total != int64(*i.TotalRooms) {
		return fmt.Errorf(
			"total_rooms: expected %d, sum of categories is %d",
			*i.TotalRooms, total,
		)
	}

	return nil
}

func (c CollectionConfig) Validate() error {
	if c.DaysAhead <= 0 {
		return fmt.Errorf("days_ahead: must be positive")
	}

	if len(c.Adults) == 0 {
		return fmt.Errorf("adults: at least one scenario is required")
	}
	seenAdults := make(map[int]bool)
	for _, adults := range c.Adults {
		if adults != 1 && adults != 2 {
			return fmt.Errorf("adults: expected 1 or 2")
		}
		if seenAdults[adults] {
			return fmt.Errorf("adults: duplicate scenario %d", adults)
		}
		seenAdults[adults] = true
	}

	if c.Rooms != 1 {
		return fmt.Errorf("rooms: MVP requires 1 room per search")
	}

	if len(c.ChildrenAges) != 0 {
		return fmt.Errorf("children_ages: MVP requires no children")
	}

	if len(c.Nights) != 3 {
		return fmt.Errorf("nights: expected exactly [1, 2, 3]")
	}

	seen := make(map[int]bool)

	for _, nights := range c.Nights {
		if nights < 1 || nights > 3 {
			return fmt.Errorf("nights: unsupported LOS %d", nights)
		}

		if seen[nights] {
			return fmt.Errorf("nights: duplicate LOS %d", nights)
		}

		seen[nights] = true
	}

	return nil
}
