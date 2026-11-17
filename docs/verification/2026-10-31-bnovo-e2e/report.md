# Bnovo E2E verification — 31.10.2026

## Executive summary

**NO-GO для регулярного production-сбора до исправления [#36](https://github.com/p-force/hotel-parser/issues/36).**
Цепочка Planner → PostgreSQL jobs → claim → pool → настоящий Bnovo → parser →
normalization → attempts/observations/offers → recovery фактически выполнена.
Однако требование ≥500 мс между HTTP-стартами нарушается внутри одного runner.
Issue #35 остаётся открытой; успешное завершение jobs не означает прохождения приёмки.

- Выполнены **122/122 сценария**, по 61 для Arena33 и Lindsay; `success` — 120,
  `partial` — 2, `no_offers`/`failed`/remaining — 0.
- **125 вызовов RoundTrip**: 122 завершённых HTTP 200 и 3 отмены до получения
  заголовков при настоящем SIGTERM. Доставка этих трёх запросов источнику не доказана.
- Сохранены **122 attempts, 793 observations, 1 163 variants, 2 784 offers**.
- Сверка сохранённых фактов с захваченными ответами: 122 attempts, 793 observations,
  2 784 offers, 0 выявленных расхождений. Это проверка соответствия публичному HTML,
  а не независимый эталон физического фонда/занятости или гарантия полноты сайта.
- Минимальный интервал HTTP-старта — **455,947542 мс**; 24 интервала <500 мс;
  9 последовательностей из трёх стартов укладываются в <1 секунды. 429/5xx — 0.
- Полный live-проход с остановкой, возобновлением и диагностикой: **86,180 с**.

## Окружение и ограничения эксперимента

- Production-код: `dev`, SHA `74c3492d26a05af98d88c7f2cb7ac02c554489d5` (после PR #34).
  Диагностическая ветка: `test/35-bnovo-e2e-validation`. Production Go-код и
  конфигурации отелей в этой проверке не менялись.
- Go `go1.26.0 darwin/arm64`; тестовые binaries — Linux/arm64, `CGO_ENABLED=0`.
- Существующий Docker PostgreSQL **17.10**, случайные схемы `hp_test_*`;
  production Neon и старая `hotel_parser` не использовались. Схемы удалены
  штатным test cleanup; результаты SELECT сохранены до удаления.
- Live: 31.10.2026 **04:00:58,713–04:02:24,894 МСК**
  (`01:00:58.713–01:02:24.894 UTC`). Даты заезда: 31.10–30.12.2026 включительно;
  выезд последнего сценария — 31.12.2026. Adults=1, rooms=1, LOS=1, lang=ru.
- В рабочем `configs/lindsay.json` **enabled=false**: штатный planner создаёт
  только 61 job Arena33. Lindsay явно включена **только в тестовой копии Registry**;
  на production-конфиг разрешение этим отчётом не распространяется.
- Worker pool=8, property cap=4, origin cap=8, source interval=500 мс,
  session singleton guard включён. HTTP retry policy — штатная, максимум 3 попытки;
  фактических HTTP retry не было. Для ускорения recovery в live-тесте lease=5 с,
  renewal=1 с; исходный production default lease=1 минута не менялся.
  В smoke MaxAttempts=1 для fail-fast; в full — штатные 3.
- В штатный GET входят `/rooms/index/<public UID>`, `dfrom`, `dto`, `adults=1`,
  `lang=ru`. `rooms` в URL не передаётся: клиент поддерживает только один номер.
  UID и даты сверялись с `scenario_jobs`, варианты/тарифы — с ключами категорий.

## Smoke и TLS

1. Первый запуск не получил HTTP: 1 run/job/attempt, `transport_error`, HTTPStatus=0,
   0 observations/offers, 1,138 с. В PostgreSQL-образе отсутствуют
   `/etc/ssl/cert.pem` и `/etc/ssl/certs/ca-certificates.crt`.
   Низкоуровневый тип ошибки первой версии probe не сохранялся; вывод о CA основан
   на состоянии контейнера и успешном повторе после предоставления trust store.
2. Передан доверенный `/etc/ssl/cert.pem` через `SSL_CERT_FILE` только тестовому
   процессу; TLS verification **не отключалась**, пакеты БД не устанавливались.
3. Успешный smoke Arena33, 21–22.12.2026: **1 job, 1 HTTP 200, success, 1 attempt,
   3 observations, 7 variants, 28 offers**, все три остатка known. 0 предупреждений.
   Весь проход — **3,526 с**, HTTP=2 935 мс, parse=10 мс.

Подтверждения: [первый запуск](smoke-untrusted-ca.json), [успешный smoke](smoke.json).
Полный сбор был разрешён только после второго результата; первый запуск не выдан за PASS.

## Фактический полный сбор

| Объект | Planned/jobs | HTTP attempts¹ | Saved attempts | Success | Partial | No offers | Failed | Retry | Remaining | Observations | Offers |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Arena33 | 61 | 63 | 61 | 60 | 1 | 0 | 0 | 0 | 0 | 183 | 1 228 |
| Lindsay | 61 | 62 | 61 | 60 | 1 | 0 | 0 | 0 | 0 | 610 | 1 556 |
| **Всего** | **122** | **125** | **122** | **120** | **2** | **0** | **0** | **0** | **0** | **793** | **2 784** |

¹ Измерены на границе `RoundTripper.RoundTrip`; 2/1 запроса соответственно отменены
до заголовков. Это не серверный access log и не доказательство доставки всех 125.
Counts offers/observations по отелю дополнительно подтверждены offline SQL replay;
их общие live-итоги и все прочитанные факты совпадают.

Каждый объект получил 61 завершённый ответ HTTP 200. Доля `success` — **98,36%**,
доля обработанных сценариев с учётом `partial` — **100%**. Время HTTP-интервала
Arena33 — 83,722 с, Lindsay — 83,345 с; они перекрываются и не складываются.
Оба run завершились со статусом `partial`, а не `succeeded`.

`stock_status`: **780 known (98,36%)**, **13 unknown (1,64%)**, conflict=0.
Из known: 668 положительных остатков, 112 подтверждённых публичных нулей.
Unknown сохраняется как NULL, а не 0. Площадь опубликована для всех 793 наблюдений.

## Сверка данных и границы доверия

Прямо после сбора `GetAttempt` прочитал сохранённые категории/варианты/предложения.
Каждая завершённая попытка сопоставлена с её UID, календарными датами, adults,
моментом начала и SHA-256 **того же декодированного HTML**, без повторного обращения
к Bnovo. Проверены category/variant/rate ID, имя, площадь, цены как точные decimal,
валюта, meal plan, Visible, SelectDisabled, DataAvailable, RowAvailable, options,
category availability и результат нормализации.

Parser/normalizer replay использует ту же реализацию и доказывает сохранность
преобразования, а не независимую корректность алгоритма. Дополнительно отдельный
DOM walker сопоставил 2 784 offer keys с raw `<select>`, ценовыми атрибутами,
options/disabled и валютой. Независимая ручная выборка:

| Заезд | Объект/category | Площадь | Rate | Цена RUB | Raw data-available | Остаток DB |
|---|---|---:|---|---:|---:|---:|
| 31.10 | Arena33 / Garden `459946` | 12 | `554752` | 6 060 | 1 | 1 known |
| 31.10 | Lindsay / Economy `188497` | 12 | `772150` | 3 200 | 1 | 1 known |
| 30.11 | Lindsay / Single `188505` | 8 | `772150` | 3 200 | 1 | 1 known |
| 30.12 | Lindsay / все 10 категорий | 8–35 | тарифов нет | — | точное сообщение nodates | NULL unknown |

Первые два raw-примера: `tr[data-available="1"]`, select enabled,
`data-room-id` совпадает с category ID, option разрешает 1 номер.
Body SHA соответственно `bc73078c94df4f1e5746ca600c839b6b8dc9d8ca74b2d3569ee49e8d68c260d0`
и `0fa7a9f50887d80c13f9eaef9d949c6f62caedd3eb45c97f47111c5df25ad2c8`.
Garden имеет несколько тарифов, но физический публичный остаток остаётся 1,
не суммой тарифов. Hidden/disabled предложения сохраняются как признаки, не
автоматическое доказательство bookability; это HTML-видимость, не browser screenshot.

На 30.12 оба объекта явно подтверждают недоступность на 30–31.12, но отсутствие
`$plansForJs` даёт `plans_metadata_missing`: 2 partial и 13 unknown. Это [#37](https://github.com/p-force/hotel-parser/issues/37),
не доказательство продаж всех физических номеров или ограничений minimum stay.

Cancellation и prepayment присутствуют у всех 2 784 распарсенных offers, но **текущая
storage-модель их не сохраняет** (исключены по scope #25). MealPlan сохраняется.
Поэтому полную историю тарифных условий подтвердить нельзя. Нет PMS-эталона
физического фонда, independent browser ground truth и доказательства наличия
всех возможных тарифов сайта. Проверенная доля сохранённых фактов — 793/793 и
2 784/2 784; 100% бизнес-полноты этим не заявляется.

Подтверждения: [full.json](full.json), поля `Verification`, `Samples`, `Requests`.
Сырые HTML (~57 MiB) хранились отдельно от Git, не публикуются; в артефактах только
хеши, публичные поля, коды и синтетические test IDs. Cookies/DSN/платёжных URL нет.

## PostgreSQL и SQL evidence

В live-схеме до cleanup выполнены SELECT по статусам, HTTP-кодам, stock status и
counts всех таблиц. Вся цепочка связей реально пройдена `GetAttempt`: 122 attempts,
793 observations и 2 784 offers; это совпадает с SQL totals, фактов вне обхода нет.
Live schema затем автоматически удалена `storagetest`. Попытка позднего отдельного
SQL lock-аудита получила «relation missing» — она **не** считается успешной проверкой.

Для расширенных запросов захваченные 122 ответа **offline переиграны** через тот же
Planner/Service/pool/parser/storage в новой изолированной PostgreSQL-схеме. Network
fallback запрещён; hashes проверены. Это не второй live-сбор. Singleton и интервал
500 мс сохранены. Counts и все поля повторно совпали с live; SQL replay дал:

| Запрос/проверка | Результат |
|---|---|
| `table_counts` | runs=2, jobs=122, attempts=122, observations=793, variants=1163, offers=2784 |
| `duplicates` | jobs=0, attempts=0 |
| `orphans` | jobs=0, attempts=0, observations=0, offers=0 |
| `attempt_count_mismatches` | 0: job.attempts=count=max(attempt_no) |
| `scenario_shape` | adults=1, rooms=1, LOS=1, ru, 31.10–30.12, 122 jobs |
| `category_counts` | каждая из 13 category ID — 61 наблюдение; known=60, unknown=1 |
| `timestamps` | bad_attempt_order=0, future_attempts=0, future_run_finished=2 |

Последние два future run timestamps **не скрыты**: тест вручную планировал
сегодняшний слот 08:00 до его наступления; `Run.StartedAt` равен логическому slot,
`FinishRun` ставит `max(now, StartedAt)`. В smoke slot вообще 21.12. Метрики реального
времени взяты из wall clock/HTTP/attempts, не этих run timestamps. Физическое время
завершения run с будущим logical slot текущая модель точно не отражает.

Полные результаты: [replay-sql.json](replay-sql.json). Повторяемые SELECT:
[diagnostics.sql](diagnostics.sql); генератор — `live_sql_test.go`.
Расширенные replay-результаты не выданы за SELECT исходной live-схемы.

## Recovery, конкурентность, idempotency

- **Реальный SIGTERM:** первый отдельный Go-процесс остановлен после 12 completed
  HTTP-событий. Подтверждено в БД: 12 saved attempts, 78 observations, 299 offers;
  106 queued, 4 running, 0 terminal canceled. Второй процесс обработал оставшиеся
  110 jobs. После завершения remaining=0 и попытка №1 на каждом job без дублей.
  Истечение короткого lease позволило забрать незавершённые задания; 3 лишних
  RoundTrip — прерванные запросы, **не HTTP retries**.
- Новый `TestPostgresVerificationIndependentProcessFencing`: два настоящих
  subprocess одновременно claim одного job → один победитель; после expiration
  новый процесс получает token+1, stale completion отвергается, commit ровно один.
- Существующие PostgreSQL tests проверяют append-only UPDATE/DELETE/TRUNCATE,
  rollback, replay completion, два connection pool, retry 429/5xx на mock-server,
  renewal, canceled SQL waits, grace/flush timeout и отсутствие ложного success.
- Две схемы и совпадающие property ID, несколько отелей в одном pool, singleton
  refusal второму runner и HTTP-trigger resume проверены управляемыми тестами #27.
  Публичному Bnovo для этих сценариев дополнительная нагрузка не создавалась.
- Слоты 61/8/8/1 и 78/сутки проверены существующими planner/PG-тестами.
  Добавлены границы года, месяца и переход через полночь в timezone объекта.
- Один промежуточный запуск integration совпал с offline replay и получил
  `another Bnovo runner is active`. Это рабочий guard всей БД, не изоляция по
  test schema. Финальный последовательный запуск выполнен отдельно; guard не обходился.

## Производительность

| Метрика live | Значение |
|---|---:|
| Wall time, включая оба процесса/recovery/сверку | 86 180 мс |
| RoundTrip starts / завершённые HTTP 200 | 125 / 122 |
| Peak HTTP / peak active scenarios | 5 / 5 при лимите 8 |
| Peak per-property / per-origin | 3 / 5 при лимитах 4 / 8 |
| HTTP duration, mean / max (125, включая отмены) | 1 761,416 / 2 501 мс |
| Parser duration, mean / max (122 ответа) | 13 / 66 мс |
| Collect duration, mean / max (125) | 1 774,904 / 2 513 мс |
| Arena33 completed collect mean / max | 1 921,246 / 2 513 мс |
| Lindsay completed collect mean / max | 1 676,082 / 2 184 мс |
| Pool queue wait mean / max | 814,128 / 1 016 мс |
| Limiter wait mean / max | 814,128 / 1 016 мс |
| Rate wait events | 110 |
| HTTP retries / 429 / 5xx | 0 / 0 / 0 |
| Декодированный HTML | 59 405 333 байта |
| Commit/flush latency PostgreSQL | **не измеряется текущими метриками** |

Часы duration — `time.Now/Since`. Queue/limiter wait — ожидание внутри текущей
пачки, не полное ожидание всех 61 jobs в durable queue. Совпадение двух метрик в
этом прогоне не означает их универсальную эквивалентность. Суммы параллельных
длительностей не складываются в wall time. SQL `attempt_timing` в replay относится
к replay, **не** к публичному HTTP.

Главные наблюдаемые ограничения: HTTP (~1,7–1,9 с) и интервал source; parser на
порядки короче. Дополнительно между waves из восьми jobs есть барьеры: следующая
пачка ждёт окончания предыдущей, поэтому wall time больше идеальных ~62,5 с
плюс tail. Отдельную стоимость DB commit и долю instrumentation надёжно выделить
нельзя. Тест локальный; ограничений бесплатного хостинга здесь не измеряли.
Данные и формулы: [metrics.json](metrics.json), исходные события — `full.json.Events`.
Wall time не включает компиляцию, подготовку контейнера и финальный schema cleanup.

## Найденные проблемы и проверки

| Severity | Проблема | Доказательство | Issue |
|---|---|---|---|
| P1, blocker | admission gate не гарантирует интервал фактического HTTP после pre-HTTP SQL | live min 455,947542 мс; локальный gate repro даёт 2,583 µs при требуемых 250 мс | [#36](https://github.com/p-force/hotel-parser/issues/36) |
| P2 | точная полная недоступность теряет no_offers/known=0 из-за отсутствующего plans JS | 2 partial / 13 unknown; минимальный contract repro | [#37](https://github.com/p-force/hotel-parser/issues/37) |
| Test environment | в postgres image нет CA roots | первая неуспешная попытка, успешный повтор с trust bundle | исправлен только diagnostic launcher |

Opt-in contract reproductions **падают ожидаемо**, не считаются PASS и не маскируются
изменением production-кода. Они пропускаются обычным набором до исправления bugs:

```bash
HOTEL_PARSER_REPRO_KNOWN_DEFECTS=1 go test ./internal/collect -run '^TestKnownDefectAdmissionDoesNotLimitActualHTTPStarts$' -count=1
HOTEL_PARSER_REPRO_KNOWN_DEFECTS=1 go test ./internal/source/bnovo -run '^TestKnownDefectConfirmedUnavailableWithoutPlans$' -count=1
```

Логи с ненулевыми exit: [rate-repro.log](rate-repro.log), [nooffers-repro.log](nooffers-repro.log).
Обычные unit/race/make/Docker integration проверки запускаются отдельно; их PASS
не выдаётся за соответствие source rate contract. #33 остаётся отдельной
задачей горизонтального scaling; она не исправляет баг внутри одного процесса.

Финальные обычные проверки: `go test ./...`, `go test -race ./...`, `make check`,
`make test-integration-docker`, `git diff --check` — PASS. Известные contract repro
запускались отдельно и дали ожидаемый **exit 1**; они не засчитаны в PASS. Новый
process-fencing test на Docker PostgreSQL также PASS. Сводка — [checks.json](checks.json).

## Повтор проверки

```bash
# Каждый live запуск создаёт только случайные локальные hp_test_* schemas.
python3 scripts/verify-bnovo-docker.py --phase smoke --output /tmp/bnovo-smoke
python3 scripts/verify-bnovo-docker.py --phase full \
  --smoke-evidence /tmp/bnovo-smoke/evidence.json --output /tmp/bnovo-full \
  --private-raw-dir /tmp/bnovo-private-raw
# Без новых обращений к Bnovo: воспроизведение точно захваченных ответов.
python3 scripts/verify-bnovo-docker.py --phase replay \
  --replay-evidence /tmp/bnovo-full/evidence.json --output /tmp/bnovo-replay \
  --private-raw-dir /tmp/bnovo-private-raw
```

Нужны существующий compose PostgreSQL, Go и доверенный PEM bundle (`--ca-bundle`
при отсутствии стандартного файла на host). В этой фиксации smoke жёстко использует
21–22.12.2026; перед более поздним повтором дату нужно осознанно обновить в harness.
Public phases требуют явного флага, успешного smoke того же SHA; обычный
`make test-integration-docker` **не отправляет публичные запросы**. Новый launcher
также возвращает ненулевой exit при измеренном rate violation, даже если jobs завершены.
HTML остаётся только в приватном `/tmp`, не добавляется в Git. Перед повтором не
запускать другие runner/integration одновременно: singleton общий на БД.

## Заключение

Сбор и восстановление данных подтверждены фактически; два production-дефекта
вынесены отдельно, без большого рефакторинга. **NO-GO до #36**, затем повторить
rate conformance и оценить #37. Lindsay остаётся отключённой в рабочем конфиге;
согласование её регулярного запуска и объёма сохраняемых тарифных условий — отдельно.
Issue #35 не закрывать по результатам только зелёного обычного CI.
