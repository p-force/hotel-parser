package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func hotelFixture(t *testing.T) config.Config {
	t.Helper()
	hotel, err := config.Load(filepath.Join("..", "..", "configs", "arena33.json"))
	if err != nil {
		t.Fatal(err)
	}
	return hotel
}

func writeConfig(t *testing.T, hotel config.Config) string {
	t.Helper()
	data, err := json.Marshal(hotel)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hotel.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func observedResult(req bnovo.SearchRequest, hotel config.Config) bnovo.SearchResult {
	result := bnovo.SearchResult{Request: req, HTTPStatus: 200, Status: bnovo.SearchStatusSuccess}
	for _, category := range hotel.Inventory.Categories {
		count := category.TotalRooms
		result.Categories = append(result.Categories, bnovo.RoomCategory{ID: category.SourceRoomID, Name: category.Name})
		result.Variants = append(result.Variants, bnovo.AccommodationVariant{ID: category.SourceRoomID, CategoryID: category.SourceRoomID, Adults: 2})
		result.Offers = append(result.Offers, bnovo.RateOffer{CategoryID: category.SourceRoomID, VariantID: category.SourceRoomID, RateID: "1", Visible: true, Price: "100.00", Currency: "RUB", Availability: bnovo.AvailabilityEvidence{DataAvailable: &count, SelectableQuantities: []int{1, count}}})
		result.CategoryAvailability = append(result.CategoryAvailability, bnovo.CategoryAvailability{CategoryID: category.SourceRoomID, HasOffers: true})
	}
	return result
}

func TestInventoryCheckDates(t *testing.T) {
	for _, tc := range []struct {
		name, timezone, now, explicit, wantIn, wantOut string
		offset                                         int
	}{
		{"default_moscow", "Europe/Moscow", "2026-10-26T12:00:00Z", "", "2026-12-25", "2026-12-26", 3 * 3600},
		{"hotel_today_differs_from_utc", "Asia/Tokyo", "2026-10-26T18:00:00Z", "", "2026-12-26", "2026-12-27", 9 * 3600},
		{"year_boundary", "Europe/Moscow", "2026-12-02T12:00:00Z", "", "2027-01-31", "2027-02-01", 3 * 3600},
		{"calendar_days_across_dst", "Europe/Berlin", "2026-02-01T12:00:00Z", "", "2026-04-02", "2026-04-03", 2 * 3600},
		{"explicit_date", "Europe/Moscow", "2026-10-26T12:00:00Z", "2026-12-21", "2026-12-21", "2026-12-22", 3 * 3600},
		{"today_allowed", "Europe/Moscow", "2026-10-26T22:00:00Z", "2026-10-27", "2026-10-27", "2026-10-28", 3 * 3600},
		{"one_calendar_night_dst", "Europe/Berlin", "2026-03-28T12:00:00Z", "2026-03-29", "2026-03-29", "2026-03-30", 3600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hotel := hotelFixture(t)
			hotel.ID = "configured-hotel"
			hotel.Timezone = tc.timezone
			path := writeConfig(t, hotel)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--config=" + path}
			if tc.explicit != "" {
				args = append(args, "--date="+tc.explicit)
			}
			var out, diagnostics, logs bytes.Buffer
			calls := 0
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			code := run(context.Background(), args, &out, &diagnostics, log, now, func(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
				calls++
				if req.PropertyID != hotel.Source.PropertyID || req.CheckIn.Format("2006-01-02") != tc.wantIn || req.CheckOut.Format("2006-01-02") != tc.wantOut {
					t.Errorf("неверный сценарий: %+v", req)
				}
				_, offset := req.CheckIn.Zone()
				if req.CheckIn.Location().String() != tc.timezone || offset != tc.offset || req.CheckIn.Hour() != 0 || req.CheckOut.Hour() != 0 || req.Adults != 2 || len(req.ChildrenAges) != 0 || req.Language != "ru" {
					t.Errorf("неверные параметры сценария: %+v", req)
				}
				if req.CheckOut.Day() == req.CheckIn.Day() {
					t.Error("LOS должен составлять одну календарную ночь")
				}
				return observedResult(req, hotel)
			})
			if code != 0 || calls != 1 {
				t.Fatalf("код=%d, вызовов=%d, диагностика=%s", code, calls, diagnostics.String())
			}
			if !strings.Contains(out.String(), "Hotel: configured-hotel\nDate: "+tc.wantIn+" → "+tc.wantOut) {
				t.Errorf("неверный заголовок: %s", out.String())
			}
			if !strings.Contains(out.String(), "Inventory increases: 0") || logs.Len() != 0 || diagnostics.Len() != 0 {
				t.Errorf("неожиданные события: %s %s %s", out.String(), logs.String(), diagnostics.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("JSON-конфигурация изменена")
			}
		})
	}
}

func TestInventoryCheckDefaultConfigFlag(t *testing.T) {
	hotel := hotelFixture(t)
	data, err := json.Marshal(hotel)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "configs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "configs", "arena33.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	calls := 0
	var out, diagnostics bytes.Buffer
	code := run(context.Background(), nil, &out, &diagnostics, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), func(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
		calls++
		return observedResult(req, hotel)
	})
	if code != 0 || calls != 1 {
		t.Errorf("неверный путь по умолчанию: код=%d calls=%d %s", code, calls, diagnostics.String())
	}
}

func TestInventoryCheckBadArgumentsAndConfig(t *testing.T) {
	hotel := hotelFixture(t)
	path := writeConfig(t, hotel)
	malformed := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(malformed, []byte(`{"token":"secret-token","broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	invalidHotel := hotel
	invalidHotel.Timezone = "invalid-secret-timezone"
	invalidConfig := writeConfig(t, invalidHotel)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"invalid_day", []string{"--config=" + path, "--date=2026-02-31"}},
		{"wrong_date_format", []string{"--config=" + path, "--date=21-12-2026"}},
		{"empty_explicit_date", []string{"--config=" + path, "--date="}},
		{"past_hotel_date", []string{"--config=" + path, "--date=2026-10-26"}},
		{"unknown_flag", []string{"--secret-token=value"}},
		{"unexpected_arg", []string{"secret-token"}},
		{"missing_file", []string{"--config=" + filepath.Join(t.TempDir(), "secret-token.json")}},
		{"malformed_json", []string{"--config=" + malformed}},
		{"invalid_configuration", []string{"--config=" + invalidConfig}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var out, diagnostics bytes.Buffer
			code := run(context.Background(), tc.args, &out, &diagnostics, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Date(2026, 10, 26, 22, 0, 0, 0, time.UTC), func(context.Context, bnovo.SearchRequest) bnovo.SearchResult { calls++; return bnovo.SearchResult{} })
			if code != 2 || calls != 0 || diagnostics.Len() == 0 || out.Len() != 0 {
				t.Errorf("неверная обработка: код=%d calls=%d stdout=%s stderr=%s", code, calls, out.String(), diagnostics.String())
			}
			if strings.Contains(diagnostics.String(), "secret") {
				t.Errorf("утечка значения аргумента или конфигурации: %s", diagnostics.String())
			}
		})
	}
}

func TestInventoryCheckResults(t *testing.T) {
	hotel := hotelFixture(t)
	path := writeConfig(t, hotel)
	for _, tc := range []struct {
		name            string
		change          func(*bnovo.SearchResult)
		code, increases int
		rows            []string
		diagnostic      string
	}{
		{"unchanged", func(r *bnovo.SearchResult) {}, 0, 0, []string{"Stadium 10 10 unchanged", "Garden 4 4 unchanged", "Premium 5 5 unchanged"}, ""},
		{"increase", func(r *bnovo.SearchResult) {
			*r.Offers[0].Availability.DataAvailable = 12
			r.Offers[0].Availability.SelectableQuantities = []int{1, 12}
		}, 0, 1, []string{"Stadium 10 12 increase"}, ""},
		{"several_increases", func(r *bnovo.SearchResult) {
			*r.Offers[0].Availability.DataAvailable = 12
			r.Offers[0].Availability.SelectableQuantities = []int{1, 12}
			*r.Offers[1].Availability.DataAvailable = 6
			r.Offers[1].Availability.SelectableQuantities = []int{1, 6}
		}, 0, 2, []string{"Stadium 10 12 increase", "Garden 4 6 increase"}, ""},
		{"unknown", func(r *bnovo.SearchResult) { r.Offers[0].Availability.DataAvailable = nil }, 1, 0, []string{"Stadium 10 unknown unknown"}, "stock_evidence_incomplete"},
		{"conflict", func(r *bnovo.SearchResult) { r.Offers[0].Availability.SelectableQuantities = []int{1, 11} }, 1, 0, []string{"Stadium 10 unknown conflict"}, "stock_quantity_conflict"},
		{"partial", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusPartial
			r.Warnings = []bnovo.ParseWarning{{Code: "offer_price_invalid", Context: "1/561429", Message: "secret HTML token"}}
		}, 1, 0, []string{"Stadium 10 10 unchanged"}, "offer_price_invalid"},
		{"partial_increase", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusPartial
			*r.Offers[0].Availability.DataAvailable = 12
			r.Offers[0].Availability.SelectableQuantities = []int{1, 12}
		}, 1, 1, []string{"Stadium 10 12 increase"}, "Inventory check incomplete"},
		{"failed", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusFailed
			r.HTTPStatus = 403
			r.Error = &bnovo.CollectionError{Code: "http_error", Message: "secret HTML token", HTTPStatus: 403}
		}, 1, 0, []string{"Stadium 10 unknown unknown", "Garden 4 unknown unknown", "Premium 5 unknown unknown"}, "http_error"},
		{"timeout", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusFailed
			r.HTTPStatus = 0
			r.Error = &bnovo.CollectionError{Code: "timeout", Message: "secret URL token", Retryable: true}
		}, 1, 0, []string{"Stadium 10 unknown unknown"}, "timeout"},
		{"missing_configured_category", func(r *bnovo.SearchResult) { r.Categories = r.Categories[:2] }, 1, 0, []string{"Premium 5 unknown unknown"}, "expected_category_missing"},
		{"confirmed_zero", func(r *bnovo.SearchResult) {
			r.Status = bnovo.SearchStatusNoOffers
			r.Offers = nil
			for i := range r.CategoryAvailability {
				r.CategoryAvailability[i].HasOffers = false
				r.CategoryAvailability[i].ConfirmedUnavailable = true
			}
		}, 0, 0, []string{"Stadium 10 0 unchanged", "Garden 4 0 unchanged", "Premium 5 0 unchanged"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out, diagnostics, logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			calls := 0
			now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
			code := run(context.Background(), []string{"--config=" + path, "--date=2026-12-21"}, &out, &diagnostics, log, now, func(ctx context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
				calls++
				result := observedResult(req, hotel)
				tc.change(&result)
				return result
			})
			if code != tc.code || calls != 1 {
				t.Fatalf("код=%d, calls=%d, ожидалось %d: %s", code, calls, tc.code, diagnostics.String())
			}
			compact := strings.Join(strings.Fields(out.String()), " ")
			for _, row := range tc.rows {
				if !strings.Contains(compact, row) {
					t.Errorf("нет строки %q: %s", row, out.String())
				}
			}
			if !strings.Contains(out.String(), "Inventory increases: "+string(rune('0'+tc.increases))) {
				t.Errorf("неверное число увеличений: %s", out.String())
			}
			if tc.diagnostic != "" && !strings.Contains(diagnostics.String(), tc.diagnostic) {
				t.Errorf("нет диагностики %s: %s", tc.diagnostic, diagnostics.String())
			}
			if tc.code != 0 && strings.Contains(strings.ToLower(out.String()), "all confirmed") {
				t.Error("неполная проверка объявлена подтверждённой")
			}
			for _, data := range []string{out.String(), diagnostics.String(), logs.String()} {
				if strings.Contains(data, "secret") || strings.Contains(data, "<html>") {
					t.Errorf("выведены чувствительные данные: %s", data)
				}
			}
			decoder := json.NewDecoder(&logs)
			events := 0
			for decoder.More() {
				var event map[string]any
				if err := decoder.Decode(&event); err != nil {
					t.Fatal(err)
				}
				if event["level"] != "WARN" || event["msg"] != "bnovo.inventory.max_increased" || event["property_id"] != "arena33" || event["check_in"] != "2026-12-21" || event["check_out"] != "2026-12-22" || event["config_updated"] != false {
					t.Errorf("неверное событие: %+v", event)
				}
				switch event["category_id"] {
				case "561429":
					if event["previous_max"] != float64(10) || event["new_max"] != float64(12) || event["observed_available"] != float64(12) {
						t.Errorf("неверные числа Stadium: %v", event)
					}
				case "459946":
					if event["previous_max"] != float64(4) || event["new_max"] != float64(6) {
						t.Errorf("неверные числа Garden: %v", event)
					}
				default:
					t.Errorf("лишнее увеличение: %v", event)
				}
				events++
			}
			if events != tc.increases {
				t.Errorf("WARN-событий=%d, ожидалось %d", events, tc.increases)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("конфигурация перезаписана")
			}
		})
	}
}

func TestInventoryCheckContext(t *testing.T) {
	hotel := hotelFixture(t)
	path := writeConfig(t, hotel)
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "marker"))
	cancel()
	var out, diagnostics bytes.Buffer
	calls := 0
	code := run(ctx, []string{"--config=" + path}, &out, &diagnostics, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), func(received context.Context, req bnovo.SearchRequest) bnovo.SearchResult {
		calls++
		if received.Err() != context.Canceled || received.Value(key{}) != "marker" {
			t.Error("context не передан в Collect")
		}
		return bnovo.SearchResult{Request: req, Status: bnovo.SearchStatusFailed, Error: &bnovo.CollectionError{Code: "canceled", Message: "request canceled"}}
	})
	if code != 1 || calls != 1 || !strings.Contains(diagnostics.String(), "canceled") {
		t.Errorf("неверная отмена: code=%d calls=%d %s", code, calls, diagnostics.String())
	}
}

func TestInventoryCheckHelp(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &out, &diagnostics, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Time{}, func(context.Context, bnovo.SearchRequest) bnovo.SearchResult {
		t.Error("Collect не должен вызываться")
		return bnovo.SearchResult{}
	})
	if code != 0 || !strings.Contains(out.String(), "--date") || diagnostics.Len() != 0 {
		t.Errorf("неверный help: %d %s %s", code, out.String(), diagnostics.String())
	}
}
