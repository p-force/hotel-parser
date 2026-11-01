# CSV конкретного завершённого run, формат v1

`collection plan/tick/resume` и HTTP-trigger автоматически проверяют CSV после
commit `FinishRun`. Экспорт выполняется после завершения workers и освобождения
runner lock. Незавершённые runs пропускаются, даже если часть jobs уже успешна.
Расписание, LOS и запросы к отелям экспорт не меняет.

## Файл и восстановление

`HOTEL_PARSER_CSV_DIR` задаёт каталог; по умолчанию —
`<os.TempDir()>/hotel-parser-csv`. Пример запуска:

```bash
HOTEL_PARSER_CSV_DIR=/tmp/hotel-parser-csv go run ./cmd/collection resume
```

Путь: `<root>/v1/<schema>/<group>/<property>/<slot_UTC>/<run_id>.csv`.
Slot — `run.started_at` в UTC (`20060102T150405Z`). Для небезопасного имени
компонента пути используется детерминированный SHA-256 вместо исходной строки.
Schema предотвращает смешивание независимых групп; run ID различает сборы.

Сначала создаётся `.tmp` в каталоге конечного файла, затем выполняются запись,
проверка ошибок, sync/close и атомарный rename на том же filesystem.
Ошибка или отмена до публикации не оставляют готовый частичный CSV. После crash
возможен оставшийся скрытый `.tmp`; он не считается готовым артефактом.

Отдельного durable-флага «экспортировано» нет: завершённый неизменяемый run в
PostgreSQL — основание для генерации, наличие опубликованного файла в выделенном
каталоге — локальный кеш. На каждом последующем вызове проверяются terminal runs
(в каждой группе сначала свежие). Отсутствующие файлы создаются снова; существующие
готовые файлы переиспользуются. Это восстанавливает экспорт после restart,
ошибки диска или потери ephemeral filesystem без повторного hotel HTTP.

Готовый путь возвращается в `Response.exports` вместе с `group_id`, `schema`,
`property_id`, `run_id`, `ready`, `generated`, `error_code` и `retry_pending`.
Ошибки CSV логируются как `collection.csv.failed` безопасным кодом; результат
collection и состояния jobs/runs не меняются. Следующий вызов повторяет генерацию.
Время экспорта ограничено оставшимся execution context; фоновые задачи после
возврата запроса не используются. Восстановление требует следующего вызова
plan/tick/resume либо явного вызова экспорта, самостоятельного cron для этого нет.

`ready=true` означает наличие полного локального файла, а не доставку на Диск.
Выделенный каталог принадлежит приложению: внешнее редактирование готовых файлов
не поддерживается. Локальный диск может быть полностью утрачен без потери данных.

## Строки и колонки

UTF-8, разделитель — запятая, escaping — стандартный `encoding/csv`, без BOM.
Пустая ячейка означает отсутствующее значение/SQL NULL; явный ноль — `0`.
Цены и площади читаются как точные PostgreSQL NUMERIC text, без float-конверсии.
Timestamp — UTC ISO8601. `run_started_at` — логический слот; фактическое время
HTTP-попытки хранится отдельно в `collected_at` и `attempt_*`.

В CSV включена **только последняя committed attempt каждого job данного run**.
Промежуточные retry остаются в БД и не дублируют итоговые цены/остатки в отчёте.
Порядок: даты, adults, rooms, job ID, category ID, variant ID, rate ID.

Одна строка соответствует одному offer. Категория без offers получает одну строку
с пустыми тарифными полями. Job без observation тоже получает строку. Если job
завершён без сохранённой attempt (например, `lease_exhausted`), поля попытки пусты,
а `job_status` и `error_code` объясняют исход; фиктивная attempt не создаётся.

`available` — атрибут категории, повторяемый в её тарифных строках. Его нельзя
суммировать по строкам. Ключ категории — `(job_id, category_id)`, ключ предложения —
`(job_id, category_id, variant_id, rate_id)`. Агрегированные counts/occupancy не
вычисляются. Пустые offers не становятся подтверждённым нулевым остатком.

| Колонки | Значение |
|---|---|
| `group_id`, `schema` | Группа вызывающего сервиса и изолированная PostgreSQL-схема |
| `property_id`, `source`, `source_property_id` | ID отеля, тип источника, его публичный ID из сохранённого job |
| `run_id`, `run_started_at`, `run_finished_at`, `run_status` | Идентификатор, времена и итог логического сбора |
| `job_id`, `job_status` | Сценарий и его terminal status |
| `check_in`, `check_out`, `los`, `adults`, `rooms` | Точные параметры проживания |
| `attempt_id`, `attempt_no` | Последняя committed attempt; пусто, если её нет |
| `collected_at`, `attempt_started_at`, `attempt_finished_at` | Времена запроса/попытки; nullable |
| `attempt_outcome`, `http_status`, `error_code` | success/partial/no_offers/failed, HTTP и безопасный код; без исходных ошибок |
| `category_id`, `category_name`, `area_m2` | Категория и её площадь; nullable при отсутствии observation |
| `stock_status`, `available` | known/unknown/conflict и nullable публичный остаток |
| `variant_id`, `rate_id`, `rate_name` | Вариант и тариф; пусто при отсутствии offer |
| `price`, `currency` | Цена за весь период и валюта; nullable |
| `reported_quantity` | Нативная квота конкретного offer; NULL и 0 различаются |

У Bnovo `reported_quantity` остаётся пустым: HTML `DataAvailable` не подменяется
API-квотой. `no_offers` присутствует в job/attempt; агрегированный run при этом
может быть `succeeded`. `unknown`/`conflict` имеют пустое available, подтверждённый
`known=0` сохраняет ноль.

[example.csv](example.csv) — иллюстративные данные, не live evidence: две
противоречивые квоты 9/10, unknown без offers, known=0/no_offers и failed без category.

## Следующий этап: доставка

Готовый артефакт возвращает `report.Generate(ctx, store, groupID, directory, runID)`.
Для передачи без постоянного диска можно заново сформировать тот же CSV прямо
в writer загрузчика:

```go
rows, err := store.WriteRunCSV(ctx, groupID, runID, writer)
```

Чтение ограничено SQL `WHERE j.run_id = $1`; terminal run и его результаты
неизменяемы. `sql.Rows.Next` и `csv.Writer` держат одну строку и небольшой буфер,
память O(максимальной строки), а не O(истории/run). Частичный вывод writer при
ошибке нельзя считать доставленным. Идемпотентность удалённой доставки и её
подтверждение должны появиться в следующей задаче; здесь upload/OAuth отсутствуют.
