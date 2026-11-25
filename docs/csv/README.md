# Аналитический CSV завершённого run — v3

`collection` экспортирует committed результаты завершённого run после остановки
workers и освобождения runner lock. Незавершённые runs пропускаются. Экспорт не
делает HTTP к отелям; ошибка файла не меняет результат сбора. Доставка на Диск
использует тот же потоковый экспорт из PostgreSQL и отдельный delivery ledger.

## Контракт: ровно 35 колонок

UTF-8, запятая, стандартный escaping `encoding/csv`, без BOM. Цены и площади —
точные PostgreSQL NUMERIC text. Timestamp — UTC ISO8601. Пустая ячейка — SQL NULL,
`false` — известный boolean, `0` — известный ноль. Публичные квоты не суммируются.

| Колонки | Значение |
|---|---|
| `group_id`, `property_id`, `source` | Группа, внутренний ID отеля, источник |
| `run_id`, `run_started_at` | ID логического сбора и слот, не время upload |
| `job_status` | Итог конкретного сценария, не агрегированный статус отеля |
| `check_in`, `check_out`, `los`, `adults`, `rooms` | Параметры проживания |
| `collected_at` | Фактическое начало последней сохранённой HTTP-попытки; nullable |
| `http_status`, `error_code` | HTTP и безопасный код ошибки; без исходных сообщений |
| `category_id`, `category_name`, `area_m2` | ID категории источника, название, площадь |
| `stock_status`, `available` | known/unknown/conflict и nullable публичный остаток |
| `variant_id`, `rate_id`, `rate_name`, `price`, `currency` | Вариант, тариф и цена за весь период |
| `reported_quantity` | Нативная квота offer; NULL и 0 различаются |
| `property_physical_total_rooms`, `property_public_observed_max` | Независимые показатели отеля из снимка run |
| `property_physical_source`, `property_public_source` | Источники показателей отеля |
| `category_physical_total_rooms`, `category_public_observed_max` | Показатели категории; без подстановки общего фонда |
| `attempt_warning_codes`, `category_warning_codes` | Раздельные JSON-массивы сохранённых объектов Code/Context |
| `has_offers`, `confirmed_unavailable` | Признаки observation: true/false, пусто без observation |

В v3 удалены `schema`, `source_property_id`, `run_finished_at`, `run_status`,
`job_id`, `attempt_id`, `attempt_no`, `attempt_started_at`, `attempt_finished_at`,
`attempt_outcome`. Все они остаются в PostgreSQL; схема и миграция 0006 не меняются.

Одна строка — offer. Категория без offers получает одну строку с пустыми тарифными
полями. Job без observation тоже получает строку. После `lease_exhausted` без
attempt сохраняются job_status/error_code, HTTP/время попытки пусты. Используется
только последняя committed attempt: промежуточные retry остаются в БД.

### Идентичность при объединении файлов

Ключ регулярного сценария:
`(group_id, run_id, property_id, source, check_in, check_out, los, adults, rooms)`.
Ключ строки предложения дополняется `(category_id, variant_id, rate_id)`.
Одинаковые даты и тарифы разных runs не объединяются без run_id. Один сценарий с
двумя тарифами сохраняет две строки; available нельзя суммировать по этим строкам.
Порядок строк прежний: даты, adults/rooms, внутренний job ID, категория, вариант, тариф.

Это контракт штатного scheduler (язык ru, без детей). Для произвольных ручных jobs,
отличающихся только скрытыми language/children/source_property_id, или разных БД с
одинаковыми логическими ID нужна исходная PostgreSQL-идентичность; CSV не заменяет
полный audit dump. Разные версии CSV объединяются по именам столбцов, не позициям.

### Диагностика и достоверность

Предупреждения читаются непосредственно из JSONB attempt и текущей observation,
без удаления Context и без разворачивания массивов в JOIN. `[]` — существующая
attempt/observation без предупреждений; пустая ячейка — отсутствующая сущность.
Без offer сохраняется диагностика категории, без observation — диагностика attempt.
HTTP=0 отличается от отсутствующего HTTP. Ошибка job может сохраниться без attempt.

confirmed_unavailable означает подтверждённую недоступность выбранного сценария,
а не доказанную полную продажу физического фонда. no_offers, unknown, отсутствие
цены и has_offers=false сами по себе не доказывают sold_out, minimum_stay,
sales_closed или no_rate. Новая классификация причин не добавляется.

Физический фонд и публичный максимум берутся из неизменяемого снимка run (0006).
Для старых run без снимка шесть показателей фонда/источников пусты. Общий фонд не
подставляется категории; разность физического фонда и квоты не является занятостью.

[example.csv](example.csv) — иллюстративные данные, не live evidence. TravelLine:
два положительных значения quota 9/10 дают conflict; категории без offers остаются
unknown с confirmed_unavailable=false, весь сценарий — partial. Пример проверяется
настоящими Parse/NormalizeCategoryStock в unit-тесте без сети. В частности, текущий
TravelLine-парсер не объявляет известный ноль и no_offers по пустому room_stays.
Bnovo может сохранять known=0/no_offers при точном подтверждении недоступности.

## Локальный кеш и восстановление

`HOTEL_PARSER_CSV_DIR` задаёт корень; default — `<os.TempDir()>/hotel-parser-csv`.
Новый путь: `<root>/v3/<schema>/<group>/<property>/<slot_UTC>/<run_id>.csv`.
Старые v1/v2 файлы не переиспользуются как v3, не удаляются и не перезаписываются.
Публикация — временный файл, sync/close и атомарный rename. Незавершённый `.tmp`
не считается готовым файлом. Одинаковый готовый v3 переиспользуется после restart.
Delivered runs пропускаются автоматическим локальным экспортом.

При потере кеша данные воспроизводятся из PostgreSQL без hotel HTTP:

```go
artifact, err := report.Generate(ctx, store, groupID, directory, runID)
rows, err := store.WriteRunCSV(ctx, groupID, runID, writer) // аналитический v3
```

Память WriteRunCSV ограничена одной строкой и буфером csv.Writer. Служебные поля
могут читаться внутри для воспроизведения legacy delivery, но в v3 не публикуются.

## Яндекс Диск: формат фиксируется для доставки

Новые записи ledger получают `/root/<group>/<property>/<YYYY-MM-DD_HH-mm>.v3.csv`.
Минимальный суффикс версии отличает формат без новых каталогов. Старый remote_path
никогда не обновляется (ON CONFLICT DO NOTHING), delivered не получает новый PUT.
Структура таблицы csv_deliveries не меняется.

Для существующих unversioned записей:

- Уже загруженные v1 (35), первоначальный v2 (41) и полный v2 (45) подтверждаются
  по точному размеру/checksum. При потерянном commit следующая invocation отмечает
  delivered без PUT, без переименования и без перехода на v3.
- Если файл ещё отсутствует, pending legacy delivery использует прежний полный v2
  (45 колонок), а не v3. Это детерминированный выбор для старых записей без версии.
- После несовпадения формата fallback только проверяет metadata других legacy
  форматов; fallback PUT запрещён. Посторонние/изменённые bytes остаются конфликтом,
  файл не перезаписывается. Ошибка сети/авторизации не подменяется сменой формата.

`WriteRunCSVFormat` сохраняет старые контракты исключительно для такого recovery.
Новые аналитические экспорты используют WriteRunCSV/v3. Uploader строит байты из
БД, не зависит от сохранности локального кеша, использует leases/backoff и
`overwrite=false`. Парсеры, availability, формат хранимых warnings и миграция 0006
этим изменением не затронуты.
