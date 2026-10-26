package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/logger"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func TestRunOutput(t *testing.T) {
	// Запускаем настоящий run с локальным TLS-источником и временной конфигурацией.
	// Даты и параметры сценария в CLI не подменяются.
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "arena33.json"))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/rooms/index/"+cfg.Source.PropertyID || r.URL.Query().Get("dfrom") != "04-12-2026" || r.URL.Query().Get("dto") != "07-12-2026" || r.URL.Query().Get("adults") != "2" {
			t.Errorf("изменён сценарий: %s", r.URL)
		}
		fmt.Fprint(w, `<!doctype html><html><head><script>
const $enabledRoomTypes = {"561429":{"id":561429,"name":"Stadium"}};
const $plansForJs = {"1":{"id":1,"name":"Основной","roomTypeIds":["561429"]}};
const account_currency_iso4217 = "RUB";
</script></head><body><div class="room" data-roomtype-id="561429">
<div class="plan_row" data-plan-id="1"><table><tr>
<td><span class="guest-list"><span class="guest-list__item"><i class="bnovobook-icon--adult"></i><span class="guest-list__counter">x2</span></span></span></td>
<td data-tariff-price="4910.50"></td>
<td><select class="selectAvailable" data-room-id="561429" data-real-room-id="561429" data-plan-id="1"><option value="1">1</option></select></td>
</tr></table></div></div></body></html>`)
	}))
	defer server.Close()
	cfg.Source.BaseURL = server.URL
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "configs"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "configs", "arena33.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(logger.New(&logs, time.FixedZone("UTC+3", 3*60*60)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	output, err := captureStdout(run)
	if err != nil {
		t.Fatal(err)
	}
	const summary = "HTTP 200 | Status: success | Categories: 1 | Variants: 1 | Offers: 1 | Warnings: 0\n"
	if !strings.HasPrefix(output, summary) {
		t.Fatalf("неверная сводка CLI: %s", output)
	}
	if strings.Contains(output, "%!") {
		t.Fatalf("ошибка форматирования fmt: %s", output)
	}
	jsonOutput := strings.TrimPrefix(output, summary)
	if !json.Valid([]byte(jsonOutput)) {
		t.Fatalf("после сводки должен идти полный JSON: %s", jsonOutput)
	}
	var result bnovo.SearchResult
	decoder := json.NewDecoder(strings.NewReader(jsonOutput))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("лишние данные после результата: %v", err)
	}
	if !strings.HasPrefix(jsonOutput, "{\n  \"Request\": {") {
		t.Error("изменено форматирование JSON")
	}
	if result.HTTPStatus != 200 || result.Status != bnovo.SearchStatusSuccess || result.Error != nil || result.CollectedAt.IsZero() {
		t.Fatalf("неверные метаданные: %+v", result)
	}
	if len(result.Categories) != 1 || len(result.Variants) != 1 || len(result.Offers) != 1 || len(result.Warnings) != 0 || result.Offers[0].Price != "4910.50" || result.Request.PropertyID != cfg.Source.PropertyID {
		t.Fatalf("результат потерял данные: %+v", result)
	}
	if requests.Load() != 1 {
		t.Errorf("запросов = %d, ожидался один", requests.Load())
	}
	if strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), "bnovo.collect.completed") {
		t.Fatalf("ожидался один итоговый лог: %s", logs.String())
	}
	if strings.Contains(output, "bnovo.collect.completed") {
		t.Error("логи попали в пользовательский stdout")
	}
	t.Logf("INFO example:\n%s", logs.String())
	t.Logf("CLI example:\n%s", output)
}

func captureStdout(run func() error) (string, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	defer writer.Close()
	previous := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = previous }()
	var output bytes.Buffer
	copied := make(chan error, 1)
	go func() { _, err := io.Copy(&output, reader); copied <- err }()
	runErr := run()
	writer.Close()
	if err := <-copied; err != nil {
		return "", err
	}
	return output.String(), runErr
}
