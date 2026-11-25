# Физический фонд и публичные квоты

`physical_total_rooms` — подтверждённое физическое количество номеров;
`public_observed_max` — установленный ранее максимум публичной квоты.
Оба показателя независимы на уровне отеля и категории. JSON-поля чисел обязательны,
`null` означает неизвестное значение, а не ноль. Известное значение положительно;
публичный максимум не превышает соответствующий известный физический фонд.

Для известных чисел нужен непустой источник соответствующего показателя:
`physical_source` / `public_source`. Значения источников не ограничены enum.
Источники задаются на уровне отеля; отдельных источников категории текущий JSON
не содержит. В CSV источники поэтому явно называются `property_*_source`.

`categories_form_complete_partition` по умолчанию false. Только явное true,
известный общий физический фонд и известные физические значения **всех** категорий
разрешают проверку равенства суммы. Идентификаторы категорий/source_room_id уникальны;
фактическую непересекаемость подтверждает автор конфигурации, код её не выдумывает.
Неизвестные значения категории при известном общем фонде допустимы. Сумма публичных
максимумов не обязана совпадать с физическим фондом или публичным максимумом отеля.

`DetectInventoryIncreases` сравнивает доступный публичный остаток только с
`PublicObservedMax` категории. При неизвестном максимуме сравнение пропускается,
физический фонд не используется вместо него. Сам конфиг автоматически не меняется.
`available`, `reported_quantity`, `stock_status`, confirmed unavailable и partial
сохраняют прежнюю семантику. Физический фонд минус квота не является занятостью.

## PostgreSQL, миграция 0006

В схеме до 0006 **не было** таблиц справочников/конфигурации фонда и колонок
`total_rooms` / `inventory.source`. Сохранялись только сценарии, попытки, наблюдения,
варианты и предложения. Поэтому миграция не может восстановить прежние конфиги из
истории и не делает backfill по `available`/квотам или сегодняшним JSON.

Новые append-only таблицы:

- `run_inventory`: run_id, два числа отеля, два источника, признак полного разбиения.
- `run_category_inventory`: run_id, config category_id/name, source_room_id,
  два числа категории и исходный порядок категорий.

Снимок создаётся атомарно с **новым** run. Штатный planner передаёт конфигурацию
в `PlanRun`; очереди, даты и количество сценариев не меняются. Для прямого API:

```go
run, err := store.CreateRun(ctx, input, hotel.Inventory)
// Либо атомарный план:
run, err = store.PlanRun(ctx, input, scenarios, hotel.Inventory)
snapshot, err := store.GetRunInventory(ctx, run.ID)
```

Аргумент Inventory необязателен для совместимости существующего storage API.
Без него снимок неизвестен. Повтор CreateRun/PlanRun не переписывает снимок и не
добавляет его задним числом старому run. Результаты из GetRunInventory независимы
от последующего редактирования конфига. UPDATE/DELETE/TRUNCATE снимков запрещены.
Не удаляются jobs, runs, attempts, observations, variants, offers или delivery ledger.
Старые миграции и checksum не меняются, повтор Migrate идемпотентен.

### Старые значения и неоднозначности

Шесть активных конфигов уже переведены автором на новые показатели и не изменены
реализацией. Lindsay по разрешению переведён только структурно: прежние числа
`total_rooms` с `source=public_observed_max` перенесены в публичные максимумы;
физический фонд оставлен null. Старый provenance label `public_observed_max`
сохранён в public_source дословно, без выдумывания подтверждённого нового источника.
Lindsay остаётся выключенным; все остальные поля и количества сохранены.

Для старых run, где снимки не существовали, GetRunInventory возвращает nil,
шесть новых CSV-ячеек пусты. История сама по себе не доказывает ни физический фонд,
ни конфигурационный публичный максимум. Это известный пробел provenance, а не ноль.
Если отдельно будут предоставлены исторические подтверждённые конфиги, их импорт
нужно решать отдельно; эта миграция не угадывает неоднозначную семантику.

CSV v2: [колонки и совместимость](csv/README.md), [пример](csv/example.csv).
Production не мигрируется автоматически в рамках реализации; Migrate запускает
оператор штатным механизмом после ревью. Проверки выполняются в изолированных
тестовых схемах локального PostgreSQL Docker, без запросов к отелям/Диску.

## Проверки для ревью — 25.11.2026

- `go fmt ./...` — PASS.
- `go vet ./...` — PASS.
- `go test ./...` — PASS.
- `go test -race ./...` — PASS.
- `go build ./...` — PASS.
- `make test-integration-docker` — PASS, 75 PostgreSQL integration-тестов.
- Новые inventory/upgrade/CSV integration-тесты с race на локальном PostgreSQL — PASS.
- Дополнительная проверка NULL/независимости config с race — PASS.
- `git diff --check` — PASS.

Ни production, ни облачная БД не затронуты. Сбор гостиниц не запускался;
HTTP в integration-тестах — mock. Новая миграция применялась только в изолированных
тестовых схемах локального Docker PostgreSQL. Ветка `feat/inventory-metrics`;
коммиты, PR и merge не создавались.

Изменённые реализацией файлы (включая тесты и документацию):

```text
CHANGELOG.md
README.md
cmd/inventory-check/main.go
cmd/inventory-check/main_test.go
configs/lindsay.json
deploy/README.md
docs/csv/README.md
docs/csv/example.csv
docs/inventory.md
internal/collect/inventory.go
internal/collect/inventory_test.go
internal/collect/runtime_history_integration_test.go
internal/collect/runtime_integration_test.go
internal/config/config.go
internal/config/inventory_metrics_test.go
internal/config/json.go
internal/config/json_test.go
internal/config/nullable_inventory_test.go
internal/config/validate.go
internal/config/validate_test.go
internal/report/csv.go
internal/schedule/csv_integration_test.go
internal/schedule/inventory_integration_test.go
internal/schedule/planner.go
internal/schedule/regular_delivery_integration_test.go
internal/schedule/schedules_reliability_integration_test.go
internal/schedule/service.go
internal/schedule/service_integration_test.go
internal/schedule/travelline_regression_integration_test.go
internal/storage/inventory.go
internal/storage/inventory_integration_test.go
internal/storage/inventory_upgrade_integration_test.go
internal/storage/jobs.go
internal/storage/migrations/0006_inventory_metrics.sql
internal/storage/plans.go
internal/storage/reported_quantity_upgrade_integration_test.go
internal/storage/run_csv.go
internal/storage/run_csv_columns_test.go
internal/storage/run_csv_diagnostics_integration_test.go
internal/storage/store_integration_test.go
```

Шесть ранее изменённых пользователем конфигов проверены, но не редактировались
реализацией: Arena33, Aerostar, Bega, Monarch, Sovietsky, Petroff. Контрольные SHA-256
до/после совпали. Только структура Lindsay изменена по отдельному разрешению;
числа, имена/ID, тарифы, параметры сбора и старый provenance label сохранены.

## Дополнение: диагностика отсутствия предложений

CSV v2 расширен до 45 колонок: после показателей фонда добавлены
`attempt_warning_codes`, `category_warning_codes`, `has_offers`,
`confirmed_unavailable`. Существующие `error_code`, `http_status`, `job_status`,
`stock_status` сохранены. JSONB предупреждений выгружается без преобразования,
включая Code/Context. Отсутствующая строка — пустая ячейка; существующий пустой
массив — `[]`; booleans — `true`/`false`, не 0 и не NULL.

Проверены partial/no_offers/failed/unknown, две строки offers одной категории,
отсутствие offer/observation/attempt, HTTP=0 против отсутствующего HTTP, NULL против
reported_quantity=0/available=0 и точное совпадение предупреждений с PostgreSQL.
Число строк совпадает с прежними JOIN; диагностические массивы не разворачиваются.

`go test ./...`, `go test -race ./...`, `make test-integration-docker` (75 тестов),
целевой PostgreSQL diagnostics test с race и `git diff --check` — PASS.
Парсеры, нормализация availability и запись warnings в БД не менялись.

Уже можно сообщить заказчику технический error/HTTP, код и контекст диагностики,
наличие извлечённых предложений и подтверждённую источником недоступность выбранного
сценария. Причины sold_out / minimum_stay / sales_closed / no_rate из этих признаков
в общем случае установить нельзя: новая классификация не добавлялась. Подтверждённая
публичная недоступность не равна доказательству полной продажи физического фонда.
