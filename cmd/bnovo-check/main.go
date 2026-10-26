package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Часовой пояс отеля, не локальный часовой пояс компьютера.
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return fmt.Errorf("load hotel timezone: %w", err)
	}

	nights := 3

	checkIn := time.Date(2026, 12, 4, 0, 0, 0, 0, loc)

	// Настройки одного тестового поискового сценария.
	request := bnovo.SearchRequest{
		PropertyID: "7f3f84ae-1f13-419f-af60-ae782d454666",
		CheckIn:    checkIn,
		CheckOut:   checkIn.AddDate(0, 0, nights),
		Adults:     2,
		Language:   "ru",
	}

	// Клиент для публичного модуля Bnovo.
	client, err := bnovo.NewClient(
		"https://reservationsteps.ru",
		nil,
	)
	if err != nil {
		return fmt.Errorf("create Bnovo client: %w", err)
	}

	// Передаём контекст. Клиент самостоятельно ограничивает
	// длительность HTTP-запроса до 20 секунд.
	response, err := client.Search(context.Background(), request)
	if err != nil {
		return fmt.Errorf(
			"search Bnovo (HTTP %d, duration %s): %w",
			response.StatusCode,
			response.Duration,
			err,
		)
	}

	fmt.Printf(
		"HTTP %d | HTML: %d bytes | Duration: %s\n",
		response.StatusCode,
		len(response.Body),
		response.Duration,
	)

	// Разбираем HTML на физические категории и варианты.
	// categories, variants, warnings, err :=
	// 	bnovo.ParseCategories(response.Body)
	// if err != nil {
	// 	return fmt.Errorf("parse Bnovo response: %w", err)
	// }

	// Выводим структурированный результат.
	// result := struct {
	// 	Categories []bnovo.RoomCategory         `json:"categories"`
	// 	Variants   []bnovo.AccommodationVariant `json:"variants"`
	// 	Warnings   []bnovo.ParseWarning         `json:"warnings"`
	// }{
	// 	Categories: categories,
	// 	Variants:   variants,
	// 	Warnings:   warnings,
	// }

	// offers, offerWarnings, err := bnovo.ParseOffers(response.Body)
	// if err != nil {
	// 	return fmt.Errorf("parse offers: %w", err)
	// }

	// fmt.Printf(
	// 	"Offers: %d, warnings: %d\n",
	// 	len(offers),
	// 	len(offerWarnings),
	// )

	// encoder := json.NewEncoder(os.Stdout)
	// encoder.SetIndent("", "  ")

	// if err := encoder.Encode(offers); err != nil {
	// 	return err
	// }
	// if err := encoder.Encode(result); err != nil {
	// 	return fmt.Errorf("encode JSON: %w", err)
	// }

	// Выполняем единый разбор HTML.
	result, err := bnovo.Parse(response.Body, request)
	if err != nil {
		return fmt.Errorf("parse Bnovo HTML: %w", err)
	}

	// Дополняем результат метаданными HTTP-запроса.
	result.CollectedAt = response.RequestedAt
	result.HTTPStatus = response.StatusCode

	// Выводим краткую сводку.
	fmt.Printf(
		"Status: %s | Categories: %d | Variants: %d | Offers: %d | Warnings: %d\n",
		result.Status,
		len(result.Categories),
		len(result.Variants),
		len(result.Offers),
		len(result.Warnings),
	)

	// Выводим полный результат в JSON.
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")

	return encoder.Encode(result)

	// return nil
}
