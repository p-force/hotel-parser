# Исходные room_type_quotas TravelLine

`SearchResult.RoomTypeQuotas` сохраняет типизированные элементы **исходного** массива
до отбора тарифов, проверки room_stays и построения offers. Декодирование evidence отделено от
полей цены/гостей: даже их ошибка типа не отбрасывает корректные квоты. HTTP-ответ целиком не
сохраняется. Bnovo это поле не заполняет.

## Контракт

Оболочка `RoomTypeQuotaSet{Items: []RoomTypeQuota}`:

- `nil` — массив отсутствовал/null либо попытка историческая, без такого evidence.
- `Items: []` — источник явно вернул пустой массив.
- Элементы сохраняются в исходном порядке, `Ordinal` начинается с 0. Сортировки,
  объединения по RPH и удаления дубликатов нет.
- `RPH` сохраняется без подмены идентификатором категории, включая пустую строку.
- `Quantity` — исходное nullable целое. NULL, 0, положительные и отрицательные
  значения сохраняются раздельно; отрицательное число не объявляется доступностью.
- `SourceRoomID` — nullable нативный ID категории; не ID из локального конфига.
- `MappingStatus`: `mapped`, `unmapped`, `conflict`.

Типы представлены в source, collect и storage. Source использует существующий
подход общих alias-моделей Bnovo; это не добавляет TravelLine-логику в Bnovo-парсер.
Collect переносит evidence в независимую storage-модель с копированием указателей.

## Сопоставление

Проверяются **все** связи `room_stays[].room_types[]` независимо от allowlist,
promo, цен, дат, гостей, числа room_types и наличия валидного offer.

- Один непустой `code` для RPH и правильного `hotel_ref.code` → `mapped`.
- Нет достоверных связей → `unmapped`, `SourceRoomID=NULL`.
- Несколько разных категорий либо достоверная связь вместе с неполной/чужой
  привязкой → `conflict`, `SourceRoomID=NULL`.
- Повтор одной и той же связи не создаёт конфликт.
- Повреждённая структура связей оставляет все категории несопоставленными; квоты сохраняются.
- Пустая RPH не используется как идентификатор для сопоставления.
- Категория может отсутствовать в hotel_info: явная связь room_stay сохраняется
  как исходное evidence, не создавая нормализованную категорию или offer.

`mapping_status` описывает **связь с категорией**, а не достоверность количества.
Два элемента одинаковой RPH с разными quantity могут иметь одну подтверждённую
категорию и `mapped`; оба количества остаются отдельными строками. Из этого не
следует ни exact, ни at_least. Существующее разрешение конфликтов квот для offers
и нормализации не менялось.

## PostgreSQL и идемпотентность

Append-only миграция `0008_attempt_room_type_quotas.sql` добавляет:

- `attempt_room_type_quota_sets(attempt_id)` — признак присутствия массива,
  в том числе пустого.
- `attempt_room_type_quotas(attempt_id, ordinal, rph, quantity, source_room_id,
  mapping_status)` — PK `(attempt_id, ordinal)`, quantity `bigint`, nullable.

Обе таблицы запрещают UPDATE/DELETE/TRUNCATE. Запись происходит в транзакции
SaveAttempt вместе с attempts/observations/offers и существующими fencing-проверками.
Новая оболочка включена в payload hash; повтор той же попытки — no-op, другой
массив, порядок, mapping или количество — ErrConflict. При ошибке вставки квоты
вся попытка откатывается, частичных записей нет.

Пустой набор и отсутствие набора имеют разные hash. Отсутствующее поле помечено
`omitempty`, поэтому canonical payload старых попыток не изменяется. Миграции
0001–0007 не менялись. Миграция не делает backfill старой истории из текущих
конфигов/ответов. Перед запуском новой версии нужен штатный `collection migrate`.

## JSON-диагностика и CSV

`GetAttempt` читает полную последовательность квот по ordinal. Отчёт
`collection diagnostics --group-id <group> --run-id <UUID>` включает её в
`data.scenarios[].attempt.RoomTypeQuotas.Items`, даже когда нет observation/offer.
Как и прежде, JSON run-диагностики содержит финальные попытки; квоты остальных
попыток доступны через GetAttempt из append-only истории.

CSV v3 остаётся ровно из 35 колонок. Новые таблицы не участвуют в CSV JOIN.
`reported_quantity` по-прежнему относится к конкретному валидному offer; квота
исключённого тарифа не переносится в эту колонку. `stock_status`, `available`,
NormalizeCategoryStock и остальные правила нормализации не изменены.

## Пример до/после (синтетический)

Исходные поля одного ответа:

```json
{
  "room_type_quotas": [
    {"rph":"A","quantity":10},
    {"rph":"A","quantity":0},
    {"rph":"orphan","quantity":null},
    {"rph":"ambiguous","quantity":2}
  ]
}
```

Пусть room_stays связывают A с категорией 4347, ambiguous с двумя категориями,
а все тарифы исключены allowlist. Раньше у попытки не было offers и исходный
массив из PostgreSQL восстановить было нельзя. Теперь сохраняется:

| ordinal | rph | quantity | source_room_id | mapping_status |
| ---: | --- | ---: | --- | --- |
| 0 | A | 10 | 4347 | mapped |
| 1 | A | 0 | 4347 | mapped |
| 2 | orphan | NULL | NULL | unmapped |
| 3 | ambiguous | 2 | NULL | conflict |

Новых offers, нулевых остатков и утверждений о физическом фонде это не создаёт.

## Проверки

Unit-тесты проверяют сохранение всех элементов, фильтрацию тарифов/цен/дат,
невалидные offers и ошибки их типов, несколько категорий по RPH, повторные связи, несколько room_types,
пустой/отсутствующий массив, NULL/0/отрицательные значения, порядок, hash и копирование.

PostgreSQL-тесты проверяют запись без observations/offers, bigint, повтор SaveAttempt,
конфликт payload, fencing, полный откат ошибки, запрет изменения истории, переход с
0007, чтение после открытия нового Store и включение в JSON без изменения CSV.
Fixtures и локальные mocks; реальные гостиницы не опрашиваются.

## Изменённые файлы

- `CHANGELOG.md`
- `README.md`
- `docs/travelline-room-type-quotas.md`
- `internal/collect/persistence.go`
- `internal/collect/room_type_quotas.go`
- `internal/collect/room_type_quotas_integration_test.go`
- `internal/collect/room_type_quotas_test.go`
- `internal/source/bnovo/models.go`
- `internal/source/source.go`
- `internal/source/travelline/parser.go`
- `internal/source/travelline/quotas.go`
- `internal/source/travelline/quotas_test.go`
- `internal/storage/attempts.go`
- `internal/storage/inventory_upgrade_integration_test.go`
- `internal/storage/migrations/0008_attempt_room_type_quotas.sql`
- `internal/storage/models.go`
- `internal/storage/read.go`
- `internal/storage/reported_quantity_upgrade_integration_test.go`
- `internal/storage/room_type_quotas.go`
- `internal/storage/room_type_quotas_integration_test.go`
- `internal/storage/room_type_quotas_test.go`
- `internal/storage/room_type_quotas_upgrade_integration_test.go`
- `internal/storage/store_integration_test.go`
- `internal/storage/validate.go`

## Результаты локальных проверок

- `go test ./...` — PASS.
- `go test -race -count=1 ./...` — PASS.
- `go vet ./...`, `go build ./...` — PASS.
- Финальный `make check` — PASS (форматирование, vet, полный race и сборка CLI).
- Финальный `make test-integration-docker` — PASS, 89 top-level PostgreSQL-тестов.
- Новые PostgreSQL-тесты с `-race -tags=integration -count=1 -p 1` — PASS.
- `git diff --check` — PASS.

Проверки выполнялись на локальной PostgreSQL 17 в Docker, в изолированных схемах.
Источник TravelLine проверялся сохранёнными fixtures и локальным TLS mock.
Миграция 0008 проверена на чистой схеме и при переходе с 0007; production не менялась.
