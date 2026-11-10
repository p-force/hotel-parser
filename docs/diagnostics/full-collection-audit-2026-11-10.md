# Полный локальный аудит сбора — 10.11.2026

## 1. Executive summary

**Локальный сбор и сохранение завершились успешно; готовность автоматического Production-сбора этим запуском не подтверждена.** Проверены все шесть включённых отелей, оба источника и объединение конфигурационных и регулярных сценариев: **610/610 jobs, 976 HTTP, 13 мин 44,940 с**. Потерянных заданий, failed, 429, retry и конфликтующих остатков не обнаружено. Результаты: **385 succeeded, 224 partial, 1 no_offers**. Из **3721** наблюдения **3173 known**, **548 unknown**, conflict=0. Unknown не означает отсутствие номеров.

Сформированы шесть CSV: **15 721 строка данных, 35 колонок**. Все **550 235 ячеек** независимо сверены с committed PostgreSQL. Повторное выполнение очереди после завершения: **0 Collect, 0 HTTP**, число строк БД не изменилось. Доставка на Диск выключена и в этой диагностике не проверялась.

Исторические «17 минут» установлены: **05.11.2026, 19 отдельных resume**, от ответа plan до terminal runs **16 мин 52,732 с**; до подтверждения всех доставок — **39 мин 05,217 с**. Это не время одного процесса и не чистый benchmark: были восстановление локального PostgreSQL relay, deadlines, ожидания lease и отдельная диагностика доставки. Между source HTTP-стартами есть подтверждённый разрыв **261,048 с**.

Пик RSS нового процесса — **39,52 MiB**, CPU — **13,751 CPU-секунды**, максимальная средняя загрузка за секундный интервал — **9,51% одного ядра**. Рост изолированной схемы с индексами/TOAST — **7 168 000 байт**. Достоверный пик активных PostgreSQL-соединений получить не удалось: ошибка метки в диагностическом sampler описана ниже; нулевой результат не выдаётся за измерение.

## 2. Конфигурация и окружение

- Ветка: `diagnostics/full-collection-audit-2026-11-10`, база `origin/dev` — `b4d6380cab2238b71b028adbeeb1fd23c9d66c37`.
- `origin/main` отсутствует; default branch — `master`, `1a4efb4`. Проверенный `origin/deploy/vercel` — `399d3d004e6b13a315f1d067b9e4c690ce9bccfc`. Эти ветки не изменялись; изменения Vercel в диагностическую ветку не переносились.
- Нативный Go **1.26.0**, macOS/arm64; существующий Docker PostgreSQL **17**, подключение напрямую через опубликованный loopback-порт. Отдельная схема **`hp_audit_20261110_244d372941`**, штатные миграции, начальные бизнес-таблицы пусты.
- `.env`/`.env.local` не загружались. Wrapper получает credentials существующего Docker-контейнера без вывода, удаляет из дочернего окружения `HOTEL_PARSER_*`, `YANDEX_*`, `VERCEL_*`, `CRON_*`, передаёт только локальный DSN. Защита Go-инструментов отклоняет remote DB. Токены, DSN, headers, cookies и исходные HTTP body в публикуемых материалах отсутствуют.
- Один foreground-процесс сбора, один общий pool, переиспользуемые source clients. Во время live-сбора другие collectors и integration-тесты не запускались. PostgreSQL cloud, Vercel, Cron и Яндекс Диск не вызывались и не изменялись.
- Workers/global=**8**, property=**4**; Bnovo source concurrency=**8**, HTTP start interval=**500 ms**; TravelLine source concurrency=**2**, interval=**1000 ms**. Retry: max attempts=3, initial=1s, max=10s, штатные jitter/Retry-After. Лимиты не ослаблялись.
- Один непрерывный `RunGroups`/штатный queued runtime: execution=**55 min**, shutdown grace=**5s**, outer context=65min, max-jobs=1830, lease по умолчанию 1min. Лимиты времени/числа работ не достигнуты. Runner lock освобождён до CSV.

Сбор выполнен **10.11.2026 22:32:55–22:46:40 МСК**. Check-in **10.11.2026–09.01.2027** включительно, 61 дата; максимальный checkout **12.01.2027**. Источник возвращает текущую доступность на эти даты; историческое время логического слота не симулируется.

### Матрица сценариев

Регулярный `BuildPlans` использует **один взрослый, один номер, LOS=1**. Конфиги дополнительно содержат Arena33 adults=2/LOS=1,2,3 и Аэростар adults=2/LOS=1. Диагностический generator объединяет эти множества и устраняет совпадения; бизнес-planner не изменён. Все сценарии — без детей, ru/RUB, rooms=1.

| Множество | Jobs | Bnovo HTTP | TravelLine HTTP | Всего HTTP без retry |
|---|---:|---:|---:|---:|
| Только конфигурационные сочетания | 488 | 183 | 610 | 793 |
| Только регулярный LOS=1/adults=1 | 366 | 61 | 610 | 671 |
| Проверенное объединение без дублей | **610** | **244** | **732** | **976** |

Включены Arena33, МонАрх, Бега, Аэростар, Советский, Петровский дворец. Lindsay выключен и не запрашивался. Два источника: `reservationsteps.ru` и `ru-ibe.tlintegration.ru`. Публичные property IDs, категории, параметры и неизменённые `rates[].enabled` сохранены в [run-manifest.json](full-collection-audit-2026-11-10/results/run-manifest.json).

**Граница покрытия:** проверены все 10 сочетаний, явно заданных конфигами или регулярным контрактом, а не произвольное декартово произведение API. TravelLine LOS=2/3, Arena33 adults=1/LOS=2/3, дети, >1 номера, disabled-тарифы и disabled-отели не согласованы текущими конфигами и здесь не проверялись. Исторические исследования LOS=2 не являются включённым регулярным сценарием. Регулярный scheduler и LOS не менялись.

Штатный путь: `PlanRun → PostgreSQL queue → RunGroups/RunQueued runtime → Client.Collect → ValidateExpectedCategories/Normalize → SaveAttempt → FinishRun → report.Generate`. Diagnostic wrapper создаёт расширенный план через существующий `PlanRun`, не альтернативную очередь или storage. Сервисные `plan/tick/resume` и доставка дополнительно покрыты существующими integration-тестами.

## 3. История 17-минутного прогона

Найдены [PR #57](https://github.com/p-force/hotel-parser/pull/57), [первичный отчёт](../verification/regular-collection-delivery/README.md), timing, invocations, source HTTP timestamps и сохранённая локальная схема `hp_regular_20261105_92f01e86`. Последняя прочитана только read-only/repeatable-read и подтвердила **366 jobs, 367 attempts, 2562 observations, 2527 variants, 9089 offers**, active leases=0. Производственная БД для восстановления истории не использовалась.

База измеренного кода — **`0f4e8417554ae191582de0c854458699641e84de`**; публикация отчёта — `089b2f875391d83eba3274fd5df5fb0b59a0a949`, merge в dev — `b4d6380…`. Группа/шесть отелей те же; слот 05.11.2026 08:00 МСК, check-in 05.11–04.01, LOS=1/adults=1/rooms=1. Workers=8, max-jobs=64, execution=45s/grace=5s, стандартные source limits.

| Событие 05.11.2026 | Время МСК | Измеренный интервал |
|---|---|---|
| Ответ `plan` | 16:06:24,566962 | Точка отсчёта отчёта; точного начала plan нет |
| Первый source HTTP | 16:06:44,924226 | +20,357 с после ответа plan |
| Последний source HTTP завершён | 16:23:07,005635 | HTTP wall 982,081 с |
| Все jobs/runs terminal | 16:23:17,298942 | **1012,732 с после plan** |
| Все доставки подтверждены service | 16:45:29,784067 | **2345,217 с после plan** |

**17 минут — завершение одной логической очереди через 19 последовательных процессов resume.** Сумма продолжительностей этих 19 invocation — **705,631 с**; wall-clock включает паузы между ними. Всего журнал содержит 54 invocation вместе с последующей диагностикой/доставкой, сумма их durations=919,424с. Это разные величины, складывать их с HTTP wall нельзя.

Точное argv каждого старого subprocess не сохранилось. Подтверждённый эквивалент порядка действий: `collection plan` для копии registry с тестовой schema и слотом 08:00 → повторные `collection resume`, workers=8/max-jobs=64/execution=45s/grace=5s. Литерал полной команды, прежние CPU/RSS/goroutines/активные PG connections и раздельные normalization/SaveAttempt timings восстановить нельзя.

### Что заняло время

1. Между HTTP-стартами **16:07:29,029016 и 16:11:50,077205 МСК** — **261,048 с**; предыдущий ответ закончился 16:07:29,282728. Первичный отчёт связывает паузу с recovery локального диагностического TCP-forwarder/PostgreSQL после первого resume. **27 committed jobs сохранились**. Точный механизм SIGTERM backend не установлен; это не доказательство дефекта Bnovo/TravelLine.
2. Из 19 source invocations первые 14 исчерпывали 45s execution и возвращали `progress_known=false`. Короткие процессы, lease recovery и паузы возобновления добавляли wall time. Отдельные бюджеты нельзя считать сетевой latency.
3. Источники: **699 starts = 61 Bnovo + 638 TravelLine**, 688 HTTP 200, 11 transport/read interruptions без статуса, **429=0**. Номинально для 366 jobs ожидалось 671 HTTP: **20 дополнительных hotel_info + 8 availability starts**. Это не 28 сохранённых retry: в БД только **одна дополнительная попытка**, Petroff 06.12 `transport_error`, затем success. Прерванные незавершённые попытки могли восстановиться через lease без сохранённой ошибки. Replay-аудит не обнаружил HTTP после финального успешно сохранённого результата того же сценария.
4. После завершения сбора исследовались delivery timeout и uncertain PUT. Прежний 10s delivery step исправлен на 40s в PR #57. В одном тестовом ledger `next_attempt_at` для шестичасового backoff был явно ускорен; это зафиксировано старым отчётом. Поэтому 39 минут не являются unattended SLA и не объясняют «17 минут сбора».

Read-only восстановленные queue_wait p50/p95/p99 — **2001/8431/9740 ms**, durable age — **592,394/901,073/942,622 s**. HTTP p50/p95/p99: Bnovo **1584,507/2847,197/2886,912 ms**, TravelLine **471,462/1110,744/2403,576 ms**. Старые HTTP времена — UTC без monotonic-компоненты; по минимальному UTC gap=998,397ms нельзя доказать точную миллисекундную семантику limiter. [Сводная реконструкция](full-collection-audit-2026-11-10/results/history-analysis.json).

## 4. Покрытие и результаты нового прогона

Во всех строках ниже **61/61 дата**, rooms=1, failed/canceled=0, 429=0, одна сохранённая попытка на job. `S/P/N` — succeeded/partial/no_offers. Stocks считаются на уровне наблюдения категории, не тарифа.

| Отель | Adults | LOS | Jobs | HTTP | S/P/N | Known/unknown | Offers |
|---|---:|---:|---:|---:|---|---|---:|
| Arena33 | 1 | 1 | 61 | 61 | 61/0/0 | 183/0 | 1217 |
| Arena33 | 2 | 1 | 61 | 61 | 61/0/0 | 183/0 | 1217 |
| Arena33 | 2 | 2 | 61 | 61 | 61/0/0 | 183/0 | 1095 |
| Arena33 | 2 | 3 | 61 | 61 | 60/0/1 | 183/0 | 1659 |
| Аэростар | 1 | 1 | 61 | 122 | 0/61/0 | 418/192 | 2103 |
| Аэростар | 2 | 1 | 61 | 122 | 0/61/0 | 418/192 | 2103 |
| Бега | 1 | 1 | 61 | 122 | 59/2/0 | 486/2 | 1944 |
| МонАрх | 1 | 1 | 61 | 122 | 40/21/0 | 443/45 | 2289 |
| Петровский дворец | 1 | 1 | 61 | 122 | 43/18/0 | 322/44 | 739 |
| Советский | 1 | 1 | 61 | 122 | 0/61/0 | 354/73 | 708 |
| **Всего** | | | **610** | **976** | **385/224/1** | **3173/548** | **15074** |

Полное множество ключей `(hotel, check_in, check_out, adults)` сопоставлено с запланированным; все LOS/rooms/даты сохранены. **Пропущено=0, дублей jobs=0, незавершено=0, active leases после завершения=0.** Фактические source result `success` отображаются в job status `succeeded`.

Наблюдения охватывают **42 категории** конфигурации, 4007 записей вариантов. Уникальных rate IDs внутри отеля: Arena33=6, Аэростар=7, Бега=4, МонАрх=7, Петровский=3, Советский=2; всего 29 пар hotel/rate. Варианты, тарифы и разные даты не суммируются в физический остаток.

### Полнота данных

- Все **15 074 offers** имеют цену и **RUB**. Это сохранённые цены за запрошенное проживание, без деления LOS и без суммирования квот; сверка CSV проверяет точное Decimal-значение БД. Нового независимого UI/API-аудита каждой цены в задаче не было.
- **3477/3721** наблюдений имеют площадь; 244 NULL: Бега `342440`, `342441` и Советский `24386`, `29344` по 61 дате. NULL не заменяется нулём. Площадь может происходить из названия категории согласно существующему адаптеру; внешний обмер не проверялся.
- Native `reported_quantity`: **9886 заполненных TravelLine offers**, **5188 NULL у Bnovo**, нулевых native quotas среди offers не встретилось. Нормализованный `known=0` есть у **99 наблюдений**, он не подставляется в native quota.
- В DTO Bnovo `MealPlan`, `Cancellation`, `Prepayment` непусты у **5188/5188** offers; у TravelLine — **0/9886** по каждому полю. Наличие текстов не доказывает эквивалентность условий. Новые `rate_plan_booking_rules` запросы не выполнялись; полноту условий у источника по этим данным установить нельзя.
- Все 548 предупреждений наблюдений — **`category_availability_unknown`**. Это отсутствие достаточного положительного/отрицательного сигнала по конкретной категории; не подтверждение распродажи и не ошибка HTTP. Параметры тарифной allowlist не менялись; отсутствие offers под действующей политикой также не доказывает физический ноль.
- Аэростар `250899`: unknown **122/122** сценария; Советский `2992`: **61/61**. Другие unknown распределены по датам/категориям: [categories.csv](full-collection-audit-2026-11-10/results/categories.csv). У Аэростара 384 unknown суммарно при двух adults-сценариях, у Советского 73, МонАрха 45, Петровского 44, Беги 2. Причины minimum stay, закрытого канала или физического отсутствия номеров не приписываются без evidence.
- Единственный `no_offers`: Arena33 **27–30.11.2026, adults=2, LOS=3**, все три категории подтверждённо недоступны. Значение known=0 сохранено отдельно от unknown.

### PostgreSQL и CSV

| Таблица | До | После | После повторного resume |
|---|---:|---:|---:|
| collection_plans | 0 | 6 | 6 |
| collection_runs | 0 | 6 | 6 |
| scenario_jobs | 0 | 610 | 610 |
| collection_attempts | 0 | 610 | 610 |
| observations | 0 | 3721 | 3721 |
| observation_variants | 0 | 4007 | 4007 |
| observation_offers | 0 | 15074 | 15074 |
| csv_deliveries | 0 | 0 | 0 |

Размер всей локальной БД: **35 231 411 → 42 391 219 байт**, прирост **7 159 808**. Измеренный размер relations именно тестовой схемы: **286 720 → 7 454 720**, прирост **7 168 000 байт**. Разница объясняет, почему database size и сумма relations не взаимозаменяемы; это не оценка объёма на сутки/месяц. Размер схемы включает индексы/TOAST, не WAL или Docker image.

CSV создавались только после terminal jobs, после снятия runner lock. Строки данных без header:

| Отель | Строки × колонки | Байты | CSV generation + read/hash, ms |
|---|---:|---:|---:|
| Arena33 | 5287 × 35 | 2852914 | 92,058 |
| МонАрх | 2334 × 35 | 1226346 | 34,180 |
| Бега | 1946 × 35 | 927550 | 23,991 |
| Аэростар | 4590 × 35 | 2576086 | 65,685 |
| Советский | 781 × 35 | 378014 | 12,796 |
| Петровский дворец | 783 × 35 | 427307 | 13,207 |
| **Всего** | **15721 × 35** | **8388217** | **241,917** |

**15 074 offer-строки + 647 строк категорий без offers**. Проверены все 35 колонок, key sets и отсутствие дублей; NULL и 0 различаются, Decimal и timestamps сравниваются по значениям. SHA-256/size каждого файла — [results.json](full-collection-audit-2026-11-10/results/results.json). Полные CSV остаются локально в `/tmp/hotel-parser-full-audit-20261110/csv/`; публикуются метрики, а не повтор всей истории цен.

Повторный queued runtime завершился за **7,462 ms**, Collect=0, HTTP=0, данные и DB size неизменны. Эта проверка подтверждает идемпотентность очереди; не выдаётся за повторную доставку или повторный запуск CSV generator. Recovery CSV/доставки проверен существующим mock integration-набором. **Live delivery, delivered-status, повторный PUT и длительность загрузки: N/A**, намеренно не выполнялись по запрету менять Диск; upload clients не создавались, ledger=0, whitelist транспорта разрешал только два источника.

## 5. Подробные метрики

Все длительности нового процесса — `time.Now/time.Since` с monotonic-компонентой; квантили nearest-rank без интерполяции. HTTP latency — от входа в базовый RoundTripper после start gate до закрытия прочитанного body; включает DNS/TLS/сеть/body. Это **фактические transport attempts**, а не число `Collect`. Старт пакета в kernel на уровне TCP отдельно не трассировался.

| Фаза | Измерение |
|---|---|
| Wall процесса измерения | **824,940 s** |
| Планирование шести runs/610 jobs | 0,274 s |
| RunGroups, включая claim, limits, collect, normalize и commit | **824,219 s** |
| FinishRun + шесть CSV + проверка файлов | 0,255 s |
| HTTP wall, первый start → последний close | 824,141 s |
| HTTP сумма всех запросов | 796,637 s; перекрывается, не прибавлять к wall |
| Parser сумма | Bnovo 2,600 s + TravelLine 0,868 s |
| Normalize/DTO conversion сумма | 62,125 ms на 610 попыток |
| SaveAttempt сумма | 13,569 s на 610 вызовов, включая DB wait/транзакции/commit |

Фазы не образуют точное разбиение: HTTP/SQL разных workers перекрываются, runtime включает ожидания, finalization входит в wall; instrumentation и открытие schema/соединений также занимают время. Измеренный TravelLine `http_duration_ms` у адаптера включает ожидание двух запросов и gate, поэтому не равен сумме чистых HTTP latency.

| Latency | n | p50 | p95 | p99 | max | Единица |
|---|---:|---:|---:|---:|---:|---|
| Bnovo HTTP | 244 | 1604,433 | 2608,625 | 3534,179 | 5016,744 | ms |
| TravelLine HTTP | 732 | 457,986 | 970,916 | 1854,657 | 4656,048 | ms |
| Bnovo Collect | 244 | 1616,657 | 2620,810 | 3543,730 | 5028,744 | ms |
| TravelLine Collect | 366 | 3158,460 | 6925,559 | 8843,810 | 9273,947 | ms |
| Bnovo parser | 244 | 11 | 14 | 15 | 19 | ms |
| TravelLine parser | 366 | 2 | 4 | 5 | 14 | ms |
| Normalize/DTO | 610 | 0,090 | 0,171 | 0,377 | 0,588 | ms |
| SaveAttempt | 610 | 21,439 | 33,646 | 40,965 | 54,222 | ms |
| Batch queue_wait | 610 | 878 | 8192 | 9061 | 11170 | ms |
| Durable queue age | 610 | 510,699 | 801,477 | 817,317 | 822,258 | s |

`queue_wait_ms` — ожидание внутри текущей bounded batch до допуска; **не** возраст PostgreSQL backlog. Durable age отдельно вычислен как `attempt.started_at - job.created_at`, отрицательных значений нет. SQL server CPU, отдельно чистые normalization и DTO conversion, а также чистый claim latency не профилировались.

HTTP codes: **976×200**, прочие=0. Bnovo endpoints=244; TravelLine `hotel_info`=366, `hotel_availability`=366. Ошибки сети/body, timeout, parse_error, persisted retry, backoff и Retry-After=**0**. Следовательно, устойчивость при 429/отмене здесь подтверждена только тестами, не возникшими live-событиями.

Минимальный monotonic start gap: Bnovo **500,026 ms**, TravelLine **1000,044 ms**. Превышений согласованной частоты не найдено. Пик активных HTTP **5**, активных Collect **6**, лимит8 не превышен. В данной матрице один Bnovo-отель допускает4 Collect, весь TravelLine источник —2; достигать8 одновременно эти конфиги не обязаны.

77 bounded batches, сумма batch_duration_ms=**821 947 ms**. `RunGroups` ждёт окончания текущей wave перед следующим claim; моментальная загрузка global workers этим ограничена. Это существующее поведение, не потеря задач. Подробные timestamps каждой попытки/очереди — [attempt-metrics.csv](full-collection-audit-2026-11-10/results/attempt-metrics.csv).

### Ресурсы и пределы измерения

- `getrusage` CPU: **13,75114 s** user+system процесса, в среднем **1,667% одного ядра** на весь wall.
- 824 секундных sample: peak CPU **9,5066%** — максимум усреднённых интервалов около1s, не мгновенный пик и не суммарная нагрузка macOS/Docker. **CPU сервера PostgreSQL отдельно не измерен.**
- `getrusage` max RSS: **41 435 136 байт = 39,52 MiB**; heap sampled max **10 549 992 байта**. Это процесс Go без PostgreSQL/Docker и без родительского Python wrapper.
- Goroutines sampled peak **24**, первый sample23, последний12. Процесс завершился exit0; остаточные HTTP/PG служебные goroutines до закрытия соединений не считаются доказательством утечки. Мгновенные пики между samples могли не попасть в выборку.
- **Active PostgreSQL connections: неизвестно.** В измеренном binary `application_name` изменялся в pgx RuntimeParams, но `ConnConfig.ConnString()` возвращал исходный DSN. Sampler фильтровал отсутствующую метку и получал нули. Эти sample поля исключены из опубликованного CSV; в JSON — `null`. После обнаружения исправлен только диагностический harness и добавлен regression-тест effective DSN. Повторные 976 live-запросов не выполнялись. Хеш исходного измеренного binary и исходников до исправления sampler сохранён в manifest; опубликованная версия harness отличается этим исправлением.

## 6. Сравнение со старым прогоном

Матрицы и даты различаются. Новый прогон содержит **244 Bnovo jobs против61**, два состава гостей Аэростара и непрерывный55min budget вместо отдельных45s resume. Оба запуска используют одинаковые source intervals; старый имел relay recovery, новый — прямой loopback PostgreSQL. Поэтому проценты «ускорения кода» из wall time не выводятся.

| Метрика | Старый прогон | Новый полный | Разница / оговорка |
|---|---:|---:|---|
| Jobs | 366 | 610 | +244 |
| HTTP | 699 | 976 | +277; состав источников иной |
| Succeeded | 201 | 385 | +184 |
| Partial | 165 | 224 | +59 |
| No offers / failed | 0 / 0 | 1 / 0 | +1 / 0 |
| Unknown observations | 337 | 548 | +211; разные даты/состав |
| HTTP 429 | 0 | 0 | 0 |
| Дополнительные persisted attempts | 1 | 0 | −1 |
| HTTP сверх номинального | 28 | 0 | −28; interrupted HTTP ≠ persisted retry |
| Plan response → terminal / новый wall | 1012,732 s | 824,940 s | −187,792 s; разные границы/режимы |
| Source HTTP wall | 982,081 s | 824,141 s | −157,940 s |
| Peak CPU | нет измерения | 9,51% /1s | Сравнение невозможно |
| Peak RSS | нет измерения | 39,52 MiB | Сравнение невозможно |
| Attempts / observations / offers | 367 / 2562 / 9089 | 610 / 3721 / 15074 | Append-only readback |
| Рост DB | нет baseline | 7 159 808 B | Сравнение невозможно |

Нормализация описания нагрузки, **не скорости последовательной job**:

- Старый source wall/job = **2,683 s**, HTTP/job=**1,910**, source wall/HTTP=**1,405 s**.
- Новый source wall/job = **1,351 s**, HTTP/job=**1,600**, source wall/HTTP=**0,844 s**. Эти отношения включают параллельность и паузы; это не HTTP latency.
- Сопоставимое регулярное подмножество внутри нового прогона: **366 jobs, 671 HTTP, 203 succeeded, 163 partial, 0 failed/no_offers, 2206 known/356 unknown, 9000 offers**. Для него нет самостоятельного wall/RSS: оно выполнялось вместе с дополнительными сценариями. Даты сдвинуты на5дней, сравнивать числа offers/unknown как regression нельзя.

Объяснение17мин подтверждается историческими19resume, 261s паузой, бюджетами и11прерванными HTTP; приписывать всё время медленному HTML-парсеру или PostgreSQL нет оснований. Сколько именно старого времени ушло на SQL, CPU, чистый idle или lease, первичные метрики не позволяют разложить.

## 7. Проблемы и приоритеты

**P0:** наблюдаемых потерь заданий/данных, ложного known=0, превышения лимитов или повреждения CSV в проверенном пути не обнаружено. Независимое сравнение всего live body с каждым сохранённым offer здесь не выполнялось: ответы хешировались без сохранения; это не аудит всех возможных source semantics.

**P1 — ограничения доказательств перед unattended Production:**

1. **Неполная доступность:** 548/3721 unknown, в том числе устойчивые Аэростар250899 и Советский2992. Автоматизация обязана сохранять их как unknown; полнота физического остатка всех категорий не подтверждена. Менять0/allowlist по предположению нельзя.
2. **Сопоставимость тарифов:** у всех9886TravelLine offers DTO не содержит отдельных meals/cancellation/prepayment. Конфиги допускают разные публичные пакеты. Минимальные цены нельзя объявлять эквивалентными условиями проживания без отдельной политики; текущая диагностика только сохраняет действующую политику и отмечает пробел.
3. **Production SLA не измерен:** локальный непрерывный55min runtime не доказывает своевременное завершение при коротких serverless invocations, сбоях trigger и реальной доставке. Старый тест требовал recovery и ускорения одного тестового delivery backoff. Текущий запрет на Vercel/Cron/Диск соблюдён; их работоспособность здесь не переобъявляется подтверждённой.

Это ограничения готовности, а не новые подтверждённые дефекты парсера. Production-компоненты ради них не менялись.

**P2 — диагностические ограничения/производительность:**

- Peak активных PG connections не измерен из-за ошибки sampler; исправление harness проверено тестом, production DB/client не менялись.
- **366 hotel_info запросов**, один на каждый TravelLine Collect. Они передали **27 514 782 байта**. При этом разные SHA-256 встречаются у каждого отеля: AZ28, остальные22. Нельзя обещать безопасное кеширование до выяснения, какие поля меняются; слепо заменить366запросов пятью нельзя.
- Синхронная wave до8jobs ждёт самый медленный сценарий; 77waves занимают почти весьruntime. Bnovo p95 gap между стартами14,119s при разрешённых500ms показывает промежутки без непрерывной Bnovo-нагрузки; это не нарушение лимита. Потенциальный выигрыш overlap/replenishment без изменения fairness нужно измерять отдельно.
- Площадь отсутствует у четырёх категорий/244наблюдений. Не заполнять её искусственно; это пробел данных, не доказанный дефект чтения.

## 8. Рекомендации

Три основных ограничения скорости в порядке практической важности:

1. **TravelLine gate 1s и два HTTP на каждый job.** Для732стартов требуется минимум731интервал между первым и последним start; наблюдённый TL start-span737,984s. Лимит согласован, обходить его нельзя.
2. **Повтор metadata в каждом Collect.** Отдельно исследовать неизменяемую часть hotel_info/корректный TTL и только после этого сравнивать с baseline; динамические body hashes не позволяют считать все ответы одинаковыми.
3. **Wave barrier + короткие restart budgets в старом запуске.** Сначала проверить адекватный execution/grace и надёжный direct/session PG transport. Отдельная оптимизация пополнения pool должна сохранять leases/fencing/fairness и source gates. В рамках аудита runtime не переписан.

Ускорение parser не приоритет: суммарно3,468s; Normalize/DTO0,062s; SaveAttempt13,569s суммарных клиентских вызовов при wall824,940s. Переносить эти суммы напрямую в возможный выигрыш wall некорректно из-за перекрытия.

## 9. Готовность и проверки

**Вердикт: PARTIAL для включения автоматического Production; PASS для проверенного локального queued collection → PostgreSQL → CSV.** Все активные конфигурационные/регулярные сценарии выполнены и восстановимость завершённой очереди подтверждена. Стабильная Production-автоматизация, доставка и полнота тарифных условий/неизвестных категорий остаются отдельными критериями; новых сервисов, Cron, постоянных фоновых процессов или конфигурационных изменений не внесено.

На диагностическом коде:

- `make check` — **PASS**: fmt, vet, все unit/race, native builds.
- `make test-integration-docker` — **PASS**: storage, collect, schedule; изолированные схемы, mock источники/Диск. Покрыты mixed sources, retry/cancellation, deadline/gates, graceful flush, lease recovery/fencing независимых процессов, concurrent claims/runner locks, append-only/idempotency, CSV across resume/restart, mock delivery recovery.
- Новые harness unit-тесты — **PASS**: точная матрица610/976 без дублей, параметры сценария, local-only DB, nearest-rank/nonmutation, передача application_name в effective DSN.
- Offline анализ live committed readback и полная сверка610ключей/550235CSVячеек — **PASS**.
- `git diff --check` — **PASS** перед коммитом.

Перед настоящим live-сбором диагностический preflight был остановлен из-за отсутствующего Claim.Owner: **0HTTP**, создана только тестовая очередь. Ошибка wrapper исправлена, отдельная preflight schema удалена; финальный прогон начат с новой пустой схемы. Это не ошибка источника и не дополнительный hidden collection.

## 10. Артефакты и воспроизведение

- [results.json](full-collection-audit-2026-11-10/results/results.json) — метрики, before/after counts/sizes, CSV SHA-256/size, batch events.
- [scenarios.csv](full-collection-audit-2026-11-10/results/scenarios.csv) — отель/источник/adults/LOS, latency, coverage и полнота.
- [categories.csv](full-collection-audit-2026-11-10/results/categories.csv) — каждая категория/сценарий, known/unknown/known_zero/area/offers.
- [attempt-metrics.csv](full-collection-audit-2026-11-10/results/attempt-metrics.csv) —610attempts, очередь, collect/normalize/save и timestamps.
- [http-metrics.csv](full-collection-audit-2026-11-10/results/http-metrics.csv) —976безопасных transport records: время, origin type/endpoint, status, bytes, SHA-256; без URL/query/headers/body.
- [process-samples.csv](full-collection-audit-2026-11-10/results/process-samples.csv) —824sampleCPU/RSS/heap/goroutines/concurrency; неверные DBsampleполя исключены.
- [run-manifest.json](full-collection-audit-2026-11-10/results/run-manifest.json) — базовый commit, original measurement binary/source hashes, параметры/конфиги и run IDs. SHA итогового документационного коммита указывается в PR.
- [history-analysis.json](full-collection-audit-2026-11-10/results/history-analysis.json) — реконструкция17мин с ссылками на первичные артефакты и read-only readback старой локальной схемы.
- [checks.json](full-collection-audit-2026-11-10/results/checks.json) — выполненные проверки и явная отметка недостоверной PG-метрики.
- [harness](full-collection-audit-2026-11-10/harness/main.go), [readonly inspector](full-collection-audit-2026-11-10/inspect/main.go), [local wrapper](full-collection-audit-2026-11-10/run_local.py), [offline analyzer](full-collection-audit-2026-11-10/analyze.py).

Исходные приватные JSON/readback, CSV и binary остаются в **`/tmp/hotel-parser-full-audit-20261110/`**; временный каталог не является постоянным хранилищем. Опубликованные метрики и hashes сохранены в Git. Raw HTML/JSON source body не сохранялись, повторное независимое сравнение body→DB для нового запуска без повторных HTTP невозможно. SQL-снимок выполнен после всех commit и содержит только явные выбранные поля; без session/lease owner/fencing credentials.

Повторный анализ **без HTTP и без записи в БД**:

```bash
python3 docs/diagnostics/full-collection-audit-2026-11-10/analyze.py \
  --input /tmp/hotel-parser-full-audit-20261110 \
  --output /tmp/hotel-parser-audit-reanalysis
```

Read-only восстановление JSON локальной сохранённой schema:

```bash
python3 docs/diagnostics/full-collection-audit-2026-11-10/run_local.py \
  --inspect hp_audit_20261110_244d372941 \
  --out /tmp/hotel-parser-audit-readback
```

Фактическая команда измеренного binary приведена в manifest; для нового live-прогона требуется явный `run_local.py --live --out <новый каталог>`. **Это повторит610jobs/976HTTP**, поэтому не является командой проверки отчёта. Единственные изменения рабочего кода — два DEBUG-замера Normalize/DTO и SaveAttempt; бизнес-логика, source clients/parsers, scheduler, лимиты, CSV, storage schema и delivery не изменены.
