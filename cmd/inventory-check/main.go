package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/logger"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

type collectFunc func(context.Context, bnovo.SearchRequest) bnovo.SearchResult

func main() {
	log, err := logger.FromEnv(os.Stderr, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid logging configuration")
		os.Exit(2)
	}
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, log, time.Now(), nil)
	stop()
	os.Exit(code)
}

// run выполняет один сценарий. В тестах collectOne заменяет только сетевой сбор.
// Коды завершения: 0 — полная сверка, 1 — неполные данные/сбой, 2 — ошибка аргументов/конфигурации.
func run(ctx context.Context, args []string, out, diagnostics io.Writer, log *slog.Logger, now time.Time, collectOne collectFunc) int {
	flags := flag.NewFlagSet("inventory-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // Не возвращаем произвольные значения аргументов в диагностику.
	configPath := flags.String("config", "configs/arena33.json", "hotel JSON configuration")
	adults := flags.Int("adults", 2, "adults: 1 or 2")
	dateFlag := flags.String("date", "", "check-in date YYYY-MM-DD; default: hotel today + 60 days")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, "Usage: inventory-check [--config=configs/arena33.json] [--date=YYYY-MM-DD] [--adults=1|2]")
			return 0
		}
		fmt.Fprintln(diagnostics, "Invalid arguments; use --help")
		return 2
	}
	if *adults != 1 && *adults != 2 {
		fmt.Fprintln(diagnostics, "Invalid --adults: expected 1 or 2")
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(diagnostics, "Unexpected positional arguments")
		return 2
	}
	hotel, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(diagnostics, "Invalid or unreadable hotel configuration")
		return 2
	}
	loc, err := time.LoadLocation(hotel.Timezone)
	if err != nil {
		fmt.Fprintln(diagnostics, "Invalid hotel timezone")
		return 2
	}
	localNow := now.In(loc)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	checkIn := today.AddDate(0, 0, 60)
	explicitDate := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "date" {
			explicitDate = true
		}
	})
	if explicitDate {
		checkIn, err = time.ParseInLocation("2006-01-02", *dateFlag, loc)
		if err != nil || checkIn.Format("2006-01-02") != *dateFlag || checkIn.Before(today) {
			fmt.Fprintln(diagnostics, "Invalid --date: expected YYYY-MM-DD, today or later in the hotel timezone")
			return 2
		}
	}
	req := bnovo.SearchRequest{PropertyID: hotel.Source.PropertyID, CheckIn: checkIn, CheckOut: checkIn.AddDate(0, 0, 1), Adults: *adults, Language: "ru"}
	if collectOne == nil {
		client, err := bnovo.NewClient(hotel.Source.BaseURL, nil)
		if err != nil {
			fmt.Fprintln(diagnostics, "Invalid Bnovo source configuration")
			return 2
		}
		collectOne = client.Collect
	}
	result := collectOne(ctx, req)
	stocks := collect.NormalizeCategoryStock(result)
	increases, warnings := collect.DetectInventoryIncreases(stocks, hotel.Inventory)
	collect.LogInventoryIncreases(ctx, log, hotel, req, increases)
	if err := writeSummary(out, hotel, req, result, stocks, increases); err != nil {
		fmt.Fprintln(diagnostics, "Failed to write inventory summary")
		return 1
	}
	complete := result.Error == nil && result.HTTPStatus == 200 &&
		(result.Status == bnovo.SearchStatusSuccess || result.Status == bnovo.SearchStatusNoOffers) &&
		len(result.Warnings) == 0 && len(warnings) == 0 && ctx.Err() == nil
	seenWarnings := make(map[string]bool)
	reportWarning := func(code, category string) {
		key := code + "/" + category
		if seenWarnings[key] {
			return
		}
		seenWarnings[key] = true
		if category != "" {
			fmt.Fprintf(diagnostics, "Warning: %s category_id=%s\n", code, category)
		} else {
			fmt.Fprintf(diagnostics, "Warning: %s\n", code)
		}
	}
	byID := make(map[string]collect.CategoryStock, len(stocks))
	for _, stock := range stocks {
		byID[stock.CategoryID] = stock
	}
	for _, category := range hotel.Inventory.Categories {
		stock, exists := byID[category.SourceRoomID]
		if !exists {
			complete = false
			reportWarning("expected_category_missing", category.SourceRoomID)
			continue
		}
		if stock.Status != collect.StockKnown || stock.Available == nil {
			complete = false
		}
		for _, warning := range stock.Warnings {
			reportWarning(warning.Code, category.SourceRoomID)
		}
	}
	// Печатаем только машинные коды, не HTML, тексты ошибок или сырые контексты источника.
	for _, warning := range result.Warnings {
		reportWarning(warning.Code, "")
	}
	for _, warning := range warnings {
		reportWarning(warning.Code, "")
	}
	if result.Error != nil {
		fmt.Fprintf(diagnostics, "Collection error: %s\n", result.Error.Code)
	}
	if !complete {
		fmt.Fprintln(diagnostics, "Inventory check incomplete")
		return 1
	}
	return 0
}

func writeSummary(out io.Writer, hotel config.Config, req bnovo.SearchRequest, result bnovo.SearchResult, stocks []collect.CategoryStock, increases []collect.InventoryIncrease) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Hotel: %s\nDate: %s → %s\nLOS: 1\nAdults: %d\nHTTP: %d\nStatus: %s\n\n", hotel.ID, req.CheckIn.Format("2006-01-02"), req.CheckOut.Format("2006-01-02"), req.Adults, result.HTTPStatus, result.Status)
	byID := make(map[string]collect.CategoryStock, len(stocks))
	for _, stock := range stocks {
		byID[stock.CategoryID] = stock
	}
	increased := make(map[string]bool, len(increases))
	for _, increase := range increases {
		increased[increase.CategoryID] = true
	}
	table := tabwriter.NewWriter(&buf, 0, 4, 3, ' ', 0)
	fmt.Fprintln(table, "CATEGORY\tPUBLIC_MAX\tOBSERVED\tSTOCK\tRESULT")
	for _, category := range hotel.Inventory.Categories {
		stock := byID[category.SourceRoomID]
		observed, outcome := "unknown", "unknown"
		if stock.Status == collect.StockConflict {
			outcome = "conflict"
		}
		if stock.Status == collect.StockKnown && stock.Available != nil {
			observed = strconv.Itoa(*stock.Available)
			if category.PublicObservedMax != nil {
				outcome = "unchanged"
				if increased[category.SourceRoomID] {
					outcome = "increase"
				}
			}
		}
		configured := "unknown"
		if category.PublicObservedMax != nil {
			configured = strconv.Itoa(*category.PublicObservedMax)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", category.Name, configured, observed, stockStatus(stock), outcome)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(&buf, "\nInventory increases: %d\n", len(increases))
	_, err := out.Write(buf.Bytes())
	return err
}

func stockStatus(stock collect.CategoryStock) collect.StockStatus {
	if stock.Status == "" {
		return collect.StockUnknown
	}
	return stock.Status
}
