package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load("configs/arena33.json")
	if err != nil {
		return fmt.Errorf("load hotel config: %w", err)
	}

	// Часовой пояс отеля, не локальный часовой пояс компьютера.
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return fmt.Errorf("load hotel timezone: %w", err)
	}

	nights := 3

	checkIn := time.Date(2026, 12, 4, 0, 0, 0, 0, loc)

	// Настройки одного тестового поискового сценария.
	request := bnovo.SearchRequest{
		PropertyID: cfg.Source.PropertyID,
		CheckIn:    checkIn,
		CheckOut:   checkIn.AddDate(0, 0, nights),
		Adults:     2,
		Language:   "ru",
	}

	// Клиент для публичного модуля Bnovo.
	client, err := bnovo.NewClient(
		cfg.Source.BaseURL,
		nil,
	)
	if err != nil {
		return fmt.Errorf("create Bnovo client: %w", err)
	}

	// Выполняем одну попытку публичного сбора Bnovo.
	result := client.Collect(context.Background(), request)

	// Выводим краткую сводку.
	fmt.Printf(
		"HTTP %d | Status: %s | Categories: %d | Variants: %d | Offers: %d | Warnings: %d\n",
		result.HTTPStatus,
		result.Status,
		len(result.Categories),
		len(result.Variants),
		len(result.Offers),
		len(result.Warnings),
	)

	// Выводим полный результат в JSON, включая диагностику неудачной попытки.
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")

	return encoder.Encode(result)
}
