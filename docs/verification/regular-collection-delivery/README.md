# Регулярный LOS=1 → PostgreSQL → CSV → Яндекс Диск — 05.11.2026

**Фактический результат:** 366/366 jobs завершены, failed/retryable/remaining=0; шесть terminal runs, шесть CSV, **шесть реальных файлов с подтверждёнными API size/checksum и ledger `delivered`**. Источники/парсеры и основные конфигурации не изменены. Production не развёрнут и production DB/Диск не затрагивались: использованы отдельная локальная схема и тестовый root Диска.

**Оговорка об автоматичности теста:** сбор шёл через отдельные процессы application service с execution=45s и grace=5s; после инфраструктурного сбоя продолжалась та же очередь. Для одного delivery HTTP 423 назначил штатный 6-часовой backoff. В **одной записи только тестовой схемы** ускорен `next_attempt_at`, чтобы проверить восстановление без шестичасового ожидания. Состояние, attempts, данные сбора и production backoff не переписывались. Поэтому это подтверждение реальной доставки и восстановления механизма, **не непрерывный unattended benchmark без вмешательства**. Mock E2E проверяет автоматический retry отдельно.

## Область

- Реестр — копия актуального `configs/groups.json` с неизменёнными конфигурациями и одним заменённым schema: **`hp_regular_20261105_92f01e86`**. Шесть активных отелей, Lindsay выключен.
- Логический слот **05.11.2026 08:00 МСК**, 61 check-in на отель: **05.11.2026–04.01.2027**, последний checkout **05.01.2027**.
- Все 366 jobs: **1 взрослый, 1 номер, LOS=1, без детей, ru/RUB**. Ни LOS=2/3, ни новые отели не запускались.
- Сам диагностический сбор выполнен позже слота, **16:06:44–16:23:07 МСК**. Это не исторические наблюдения в 08:00: фактический `CollectedAt` сохранён отдельно от логического `run_started_at` и имени файла.
- Штатный путь: **Service.Execute(plan) → PlanRun → Queue → Service.Execute(resume) → RunGroups → Client.Collect → SaveAttempt → FinishRun → CSV → YandexDiskUploader**. Отдельных scheduler/runner/parser/storage не создавалось.
- Каждый resume — новый нативный macOS Go-процесс; PostgreSQL остаётся в Docker. HTTP workers=8, max-jobs=64, стандартные source/property limits и retry не отключались.

## Итог по отелям

`Completed` включает success/partial/no_offers; столбец Partial — его подмножество, не дополнительные jobs. NoOffers/canceled=0, retryable/remaining=0 у всех отелей.

| Отель | Planned | Completed | Partial | Failed | CSV | Delivery |
|---|---:|---:|---:|---:|---|---|
| Arena33 | 61 | 61 | 0 | 0 | [1246 строк](files/arena33.csv) | delivered |
| МонАрх | 61 | 61 | 16 | 0 | [2318 строк](files/monarch.csv) | delivered |
| Бега | 61 | 61 | 3 | 0 | [1940 строк](files/bega.csv) | delivered |
| Аэростар | 61 | 61 | 61 | 0 | [2373 строки](files/azimut_aerostar.csv) | delivered |
| Советский | 61 | 61 | 61 | 0 | [789 строк](files/sovietsky.csv) | delivered |
| Петровский дворец | 61 | 61 | 24 | 0 | [776 строк](files/petroff.csv) | delivered |
| **Всего** | **366** | **366** | **165** | **0** | **9442 строки данных** | **6/6** |

Success=201. Arena33 run=`succeeded`, остальные=`partial` из-за неполных категорий отдельных сценариев; `unknown` не трактуется как физический ноль или дефект парсера.

PostgreSQL: **366 jobs, 367 attempts, 2562 observations, 2527 variants, 9089 offers, 6 delivery records**. Единственная дополнительная сохранённая попытка — `transport_error` Петровского на 06.12, после retry результат сохранён успешно. Это append-only история реальной ошибки, не дубль наблюдения. Финальные stocks: **2225 known, 337 unknown**, conflict=0. [Полный итог с run IDs, hash/size файлов](summary.json).

## Что работало и что исправлено

Уже работали: детерминированные run IDs/hash плана, атомарная публикация jobs, fair общий pool, source dispatch Bnovo/TravelLine, leases/fencing, bounded retry, session advisory lock, FinishRun, 35-колоночный CSV, atomic local publication, immutable PostgreSQL history, upload URL → PUT → confirmation, delivery ledger и восстановление без локального файла. Upload выполняется после остановки workers и освобождения runner lock.

Подтверждённые изменения:

1. **Регулярный planner принимал явный LOS=2 с evidence.** Теперь BuildPlans отклоняет LOS>1 для активного отеля. Старый тест замены на LOS=2 обновлён под новый контракт; timezone-проверка сохранена. Диагностические Collect/LOS API, модели и конфигурации не менялись. Фактический registry проверен для 61/8/8/1 сценариев и исключения Lindsay.
2. **Восстановления пропущенного cron-trigger не было.** Добавлен `collection plan --due`: только наступившие четыре слота текущего московского дня, в том числе после рестарта. Он вызывает существующие BuildPlans/PlanRun; повтор/частичная публикация групп безопасны. Новых таблиц, scheduler или очереди нет. Прошлые дни автоматически не восстанавливаются: получить их историческую доступность нынешним HTTP невозможно. Это явное ограничение истории, не скрытая подмена timestamps.
3. **Delivery step=10s был короче uploader attempt=20s.** На реальных файлах checks директорий + PUT + checksum обрывались внешним deadline; ledger оставался `in_progress` до истечения lease. В service шаг увеличен до **40s для двух существующих попыток**, всё ещё внутри общего execution deadline 45s. Ни HTTP worker, ни runner lock upload не удерживает; при маленьком остатке бюджета следующий вызов восстанавливает delivery. Regression с context budgets **падает на старых 10s** (`9.99s` у PUT) и проходит после исправления, без sleep/реальной задержки.
4. Подготовлены опциональный Compose overlay, crontab/обёртка и SQL контроля. Crontab копируется в образ с owner=root/mode=600, script=755; Compose использует init для дочерних процессов. Реальный VPS/production cron не подключался.

Production-адаптеры Bnovo/TravelLine, CSV contract, SQL schema и rates.enabled не менялись. HTTP 423 не объявлен доказанным дефектом uploader: исходный vendor error body не сохранён, причина не установлена; шестичасовая политика для этой категории 4xx не изменена.

## Resume, сбои и длительность

Очередь и все runs завершены за **19 вызовов resume**. Один диагностический wrapper после первого вызова не смог прочитать итоговый status из-за кратковременного recovery PostgreSQL; **27 committed jobs сохранились**. Сбой по времени совпал с закрытием временного TCP-forwarder. Точный механизм SIGTERM backend не установлен; forwarding заменён отдельным локальным disposable relay, production-код для этого не менялся. Следующие процессы продолжили те же jobs/leases, без перепланирования или очистки данных.

Первое завершение всех runs: **16:23:17 МСК**, через **16 мин 52,7 с** после ответа plan. Source HTTP-интервал — **16 мин 22,1 с**, включая паузу исправления диагностического подключения. Все deliveries подтверждены service к **16:45:29 МСК**, через **39 мин 05,2 с** после plan. Этот wall-clock включает исследование timeout, тесты, ожидание leases и ускорение одного backoff — **не норматив производительности**. [Точные UTC времена](timing.json), [все вызовы и результаты](invocations.json).

После исчерпания execution budget service может вернуть `progress_known=false`; committed attempts остаются в БД. Свежий вызов завершает progress/CSV/delivery. В подготовленном минутном скрипте короткая фаза без HTTP к отелям `plan --due` также выполняет существующую финализацию до следующего resume, поэтому отчёты получают собственный бюджет даже при backlog.

Зарегистрировано **699 source HTTP starts**: 61 Bnovo и 638 TravelLine, HTTP 200=688, transport/read interruptions без HTTP-статуса=11. Часть прерванных попыток не фиксируется как terminal job и восстанавливается по lease; одна сохраняется в истории как retryable transport error. [Аудит](replay-audit.json) проверил 374 availability starts: **нет запроса после последнего успешно сохранённого результата того же сценария**. После завершения повторный resume: **source HTTP=0, upload PUT=0, весь HTTP=0**, локальные CSV не перегенерированы/не перезаписаны.

[HTTP summary](http-summary.json), [source timestamps/параметры/checksums](http-starts.csv). Номинальные limits не изменены: global=8, property=4, Bnovo 500ms, TravelLine 1s. Пик наблюдённого активного HTTP — 4. Минимальные **UTC** gaps: Bnovo 0.501799s, TravelLine 0.998397s. Лог не сохраняет monotonic timestamps: он не позволяет отделить малый clock drift от scheduling между gate и callback, поэтому строгое live-monotonic равенство 1s здесь не заявляется. Существующие monotonic gate-тесты прошли; оснований менять source adapter по этому измерению нет.

## Полнота и восстановимость CSV

Все **9442 × 35 = 330470 ячеек** независимо сопоставлены с committed DB readback, включая IDs, даты, времена, outcome/error, category/area, variant/rate, Decimal prices, RUB, native reported_quantity. Timestamps сравнивались как instants, не строковым timezone-представлением. Все 61 даты каждого отеля проверены полным множеством, а не только count.

**9089 offer-строк + 353 строки категорий без offers**. Пустой available — **337** ячеек; подтверждённый `0` — **16**, они различаются. NULL native quota Bnovo не заменяется нормализованным остатком. Тарифы и варианты не складываются. Пример — первые строки [arena33.csv](files/arena33.csv); формат всех файлов исходный, 35 колонок.

Дополнительно **7859 TravelLine offers** независимо выведены из raw availability/RPH и сверены с PostgreSQL по полному key set, цене/валюте/квоте; **1230 Bnovo offers** и category stocks повторно разобраны текущим неизменённым Parse/Normalize из фактического HTML последней попытки и совпали с PostgreSQL. Это offline replay, без новых HTTP. [CSV-аудит](csv-audit.json), [TravelLine](raw-audit.json), [Bnovo](bnovo-raw-audit.json).

Восстановление после потери локального файла проверяется existing integration-тестом CSV resume/restart. Uploader формирует CSV из PostgreSQL, поэтому будущий сервер не зависит от постоянного локального диска. Ошибки CSV/delivery не меняют collection outcome. Необходимо сохранить PostgreSQL и конфигурации отелей backlog.

Offline-проверка опубликованных файлов:

```bash
python3 docs/verification/regular-collection-delivery/audit.py
# Полное повторное сравнение с исходным локальным committed readback (без сети):
python3 docs/verification/regular-collection-delivery/audit.py --readback /tmp/regular-collection-delivery/readback.json
```

## Реальный Яндекс Диск

Тестовый root: **`/hotel-parser-test/regular-20261105-92f01e86`**. Внутри:

```text
arena33/arena33/2026-11-05_08-00.csv
arena33/monarch/2026-11-05_08-00.csv
arena33/bega/2026-11-05_08-00.csv
arena33/azimut_aerostar/2026-11-05_08-00.csv
arena33/sovietsky/2026-11-05_08-00.csv
arena33/petroff/2026-11-05_08-00.csv
```

Каждый путь имеет group/property/логический слот МСК. Старые remote_path и пользовательские файлы не удалялись/не перемещались. Все шесть файлов отдельно проверены реальными metadata GET: **HTTP 200, точный размер и SHA-256** совпали с CSV PostgreSQL. [Полные пути/size/checksums](disk-verification.json), [финальный ledger](delivery-ledger.json), [идемпотентность](idempotency.json).

Перед подтверждением часть PUT закончилась без ответа: код не объявлял их delivered и не включал overwrite. Следующий вызов подтверждал уже существующий файл по size/checksum. HTTP 423 дал retryable_failed с отложенным retry; для одного тестового run время повтора ускорено явно: [единственное изменение тестового ledger](retry-acceleration.json). Доставку всё равно выполнял существующий uploader после claim, не ручной PUT и не ручная установка delivered. Никаких observations/jobs/runs при этом не менялось. Реальная доставка **подтверждена**, но фактическое шестичасовое ожидание не выполнялось.

## Проверки

- `make check` — PASS (fmt, vet, unit/race, build).
- `make test-integration-docker` на финальном коде — PASS; все три пакета. Live collector и integration-набор не запускались одновременно из-за database-wide runner lock.
- Новые PostgreSQL tests: mixed Bnovo/TravelLine → несколько resume/перезапуск → CSV → mock 503 → retry → delivered → повтор без HTTP/PUT; due-slot recovery двух групп; committed result после expired invocation; PUT context budget.
- Existing tests: concurrent planning/runner busy, old queue + new tick, HTTP/retry/cancellation, lease expiry, независимые процессы/fencing/stale owner, CSV восстановление, uncertain PUT, delivery без runner lock.
- Directed regression на старом 10s step — ожидаемый FAIL, после исправления PASS. Это подтверждение причины, не скрытый сбой итогового набора.
- Docker build, Compose render с placeholder env, owner/mode/timezone образа, запуск его cron **с /bin/echo вместо collection и network=none**, диагностический SQL на отдельной схеме — PASS. Реальные production cron/serve не стартовали.
- Offline raw/DB/CSV/checksum audits и `git diff --check` — PASS. [Машиночитаемые checks](checks.json).

## Запуск Docker/VPS и оставшиеся ограничения

Полная инструкция, расписание, stop/recovery и SQL: **[deploy/README.md](../../../deploy/README.md)**. Требуемые переменные: `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `HOTEL_PARSER_DATABASE_URL` (session/direct DSN), `YANDEX_DISK_TOKEN`, `YANDEX_DISK_DIR=/hotel-parser`, `TZ=Europe/Moscow`; опционально `HOTEL_PARSER_CSV_DIR`, logging env и свой groups registry. Секреты — только в защищённом `.env.collection`/окружении, не в crontab/CLI args/Git/logs.

```bash
# Сначала заполнить .env.collection из deploy/collection.env.example; chmod 600.
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml build collector
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml up -d postgres
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml run --rm --entrypoint /app/collection collector migrate
# Только после согласования сервера/окружения:
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml up -d collector
```

Cron: четыре tick 08/12/16/20 МСК; каждую минуту `plan --due` и `resume`. Частично опубликованный/повторный слот безопасен; уже созданная старая очередь продолжается после restart. При abrupt kill незавершённые jobs/deliveries дождутся истечения leases. Docker cron не является гарантией завершения работы после убийства контейнера; истина — подтверждённые транзакции PostgreSQL.

[deploy/status.sql](../../../deploy/status.sql) показывает последний план/успех/завершение, backlog всех runs, failed/partial/expired leases, CSV reconstructable-from-DB, текущую и старые недоставленные записи, error/retry time. `missing_current_slot` или backlog без claim >5 минут — проверить cron/container/DSN/lock. SQL-готовность CSV не означает наличие локального cache-файла.

До production нужны согласованные сервер и доступы, persistent PostgreSQL/backup+restore, закрытый network (локальный compose публикует PG port), безопасное окружение/OAuth, мониторинг и проверка настоящих clock-trigger. После многодневного простоя прошлые отсутствующие snapshots не восстанавливаются автоматически; это пробел истории, а не цены задним числом. Выключенный Lindsay остаётся выключенным. **Merge и production deployment не выполнялись.**

## Безопасность evidence

Опубликованы только публичные CSV, агрегаты/параметры source-запросов, hash/size и обезличенные технические результаты. Полные HTTP bodies/DOM и DB readback остаются локально, в Git не добавлены: в TravelLine bodies есть transient guest refs. Cookies, OAuth, credentials, signed upload URLs и персональные данные не публикуются. Схема исследования сохранена локально для проверки; production-схемы не изменены. Временные диагностические контейнеры остановлены, регулярный сервис не запущен.
