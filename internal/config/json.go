package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	cfg, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}

	return cfg, nil
}

func Parse(data []byte) (Config, error) {
	var cfg Config

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config JSON: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("decode config JSON: unexpected trailing value")
		}
		return Config{}, fmt.Errorf("decode config JSON: trailing data: %w", err)
	}

	if err := validateJSONFields(data); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// Проверяем обязательность nullable-полей отдельно от их значения.
// Основной decoder выше продолжает отклонять неизвестные поля на всех уровнях.
func validateJSONFields(data []byte) error {
	var fields struct {
		Inventory struct {
			TotalRooms json.RawMessage `json:"total_rooms"`
			Categories []struct {
				TotalRooms json.RawMessage `json:"total_rooms"`
			} `json:"categories"`
		} `json:"inventory"`
		Rates []struct {
			Enabled json.RawMessage `json:"enabled"`
		} `json:"rates"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode config fields: %w", err)
	}
	if len(fields.Inventory.TotalRooms) == 0 {
		return fmt.Errorf("inventory.total_rooms: required field (use null for unknown)")
	}
	for index, category := range fields.Inventory.Categories {
		if len(category.TotalRooms) == 0 {
			return fmt.Errorf("inventory.categories[%d].total_rooms: required field (use null for unknown)", index)
		}
	}
	// encoding/json принимает null для bool как нулевое значение; тариф требует boolean.
	for index, rate := range fields.Rates {
		if bytes.Equal(bytes.TrimSpace(rate.Enabled), []byte("null")) {
			return fmt.Errorf("rates[%d].enabled: must be boolean", index)
		}
	}
	return nil
}
