# Hotel Parser

Сервис мониторинга цен, доступности и занятости номеров отелей.

Проект находится на этапе первоначальной разработки. Реализованы сбор и разбор
публичных предложений Bnovo и TravelLine, PostgreSQL-история и очередь, планирование регулярного
сбора групп отелей, логгер и CI. Аналитика занятости и Excel-отчёты планируются.

## Локальный запуск

**Требования:** Go 1.26+, Make; Docker с Docker Compose для PostgreSQL.

Запуск приложения:

```bash
go mod download
make run
```

Сейчас приложение выводит сообщение о старте с московским временем и завершает
работу. Подключение конфигурации и базы данных к точке входа ещё не реализовано.

Локальная база PostgreSQL 17:

```bash
docker compose up -d
```

База `hotel_parser` доступна на `localhost:5432`, пользователь и пароль —
`postgres`. Данные сохраняются в Docker volume.

Запуск приложения в Docker:

```bash
docker build -t hotel-parser .
docker run --rm hotel-parser
```

## Проверка Bnovo

Из корня проекта:

```bash
go run ./cmd/bnovo-check
```

CLI использует источник из `configs/arena33.json` и выполняет одну попытку
`Client.Collect(ctx, req)`: HTTP-запрос, разбор HTML и заполнение метаданных.
Даты и гости пока заданы в `cmd/bnovo-check/main.go`: 4–7 декабря 2026 года,
два взрослых, язык `ru`.

Вывод содержит сводку и JSON с категориями, вариантами размещения, тарифами,
ценами за весь период, признаками доступности и диагностикой. Статусы:
`success`, `partial`, `no_offers`, `failed`. Ошибка сбора записывается в `Error`;
статус `failed` пока не меняет код завершения CLI.

Таймаут запроса — 20 секунд, лимит тела — 8 МиБ, до трёх редиректов внутри
исходного origin. Повторных запросов и записи в БД нет.
Публичные остатки не считаются подтверждённой занятостью физических номеров.

## TravelLine

В группе `arena33` включены МонАрх (`17249`), Бега (`61`), AZIMUT Аэростар (`585`),
Советский (`333`) и Петровский Путевой Дворец (`5787`).
Анонимные GET BookingForm `hotel_info` / `hotel_availability` собирают категории,
публичные тарифы, цены и квоты через существующий PostgreSQL runtime.
Адаптер выбирается по `source.type=travelline`; очередь, leases/fencing,
retry и shutdown общие с Bnovo. Неизвестный номерной фонд остаётся `null`.
[Проверка пяти отелей, отсутствующих категорий и PostgreSQL/CSV](docs/verification/2026-11-02-travelline-competitors/README.md).
[Первый полный диагностический сбор: 305 сценариев, 03.11–02.01](docs/verification/travelline-full-collection/README.md).

`rates[].enabled` для TravelLine задаёт allowlist обычных публичных тарифов:
BONUS и персональные цены исключаются. Пустые/неоднозначные ответы сохраняют
`unknown`, квоты разных тарифов и вариантов не складываются. Максимальный фонд
не определяется по одному наблюдению. Поддержаны 1/2 взрослых без детей, язык `ru`.
По умолчанию — 2 активных сценария на TravelLine origin и интервал реальных HTTP
стартов 1 с в пределах процесса; `collect.ConfigureLimits` позволяет переопределить их.
Базовые глобальный/отельный лимиты 8/4 сохраняются.
Кеш клиента учитывает source property ID и набор разрешённых тарифов независимо
от порядка; разные политики групп изолированы. Некорректная валюта исключает offer
с диагностикой до записи в БД. Нативная квота каждого предложения сохраняется в
`observation_offers.reported_quantity` (NULL отличается от 0); требуется миграция 0004.

[Контракт, сравнение с публичной страницей и PostgreSQL smoke](docs/verification/2026-11-01-travelline-aerostar/README.md).

## Физический фонд и публичные максимумы

`inventory.physical_total_rooms` и `public_observed_max` — независимые nullable
показатели отеля и категорий. Источники — `physical_source` / `public_source`.
Публичный максимум не подменяет физический фонд и не определяет занятость.
Сумма физических категорий проверяется только при явном
`categories_form_complete_partition=true` и известных значениях полного разбиения.
Снимок конфигурации сохраняется вместе с новым run; исторические данные не
пересчитываются по текущим конфигам. [Контракт и миграция](docs/inventory.md).

## CSV завершённого сбора

После завершения всех jobs одного run `collection` автоматически создаёт CSV из
PostgreSQL. Один run может завершиться за несколько resume. Каталог задаётся
`HOTEL_PARSER_CSV_DIR`, по умолчанию — временный `hotel-parser-csv`.
Готовые артефакты и ошибки экспорта возвращаются в `exports`; ошибка файла не
меняет результат сбора. Потерянный файл восстанавливается на следующем вызове
из БД без запросов к отелям. Локальная готовность не означает доставку на Диск.

CSV v4 содержит 36 аналитических колонок, включая фонд, квоты, диагностику и nullable `unavailability_reason`; v1–v3 воспроизводятся без изменения файлов и checksum.
Служебные ID/тайминги исключены; старые доставки воспроизводятся в прежнем формате.
[Колонки, восстановление и потоковый API](docs/csv/README.md),
[пример CSV](docs/csv/example.csv).

[Исходные room_type_quotas TravelLine](docs/travelline-room-type-quotas.md) сохраняются
отдельно от offers (миграция 0008) и доступны в JSON-диагностике; CSV не расширяется.

Отдельная диагностика причин и сравнение LOS: `collection diagnostics --group-id arena33 --run-id <UUID>`.
Читает завершённый run из БД без HTTP. [Контракт, аудит фонда и ограничения](docs/verification/inventory-diagnostics-2026-11-25/README.md).

## Логирование

Логи пишутся в stderr через `log/slog`: локально — исходный `PrettyHandler`, в Docker — JSON.
Текстовый формат: `[INFO] 2026-10-26 00:47:04 hotel-parser started`;
атрибуты добавляются в конце как `key=value`.
Настройки: `HOTEL_PARSER_LOG_FORMAT=text|json` и
`HOTEL_PARSER_LOG_LEVEL=DEBUG|INFO|WARN|ERROR` (по умолчанию `text` и `INFO`).

На INFO выводится одна итоговая запись на попытку: `success` / `no_offers` — INFO,
`partial` — WARN, `failed` — ERROR. Этапы сбора доступны на DEBUG с общим `attempt_id`.
Параметры сценария и `collected_at` записываются только в итоге; `error_code` —
только при ошибке. JSON сохраняет полный контекст итоговой записи. HTML, URL,
cookies и текст условий бронирования в логи не попадают.
Текстовые события Bnovo показывают короткие счётчики и длительности
(`categories`, `offers`, `total`, `http`, `parse`), без длинных параметров сценария.
Формат остальных сообщений приложения сохраняется.

В итоговом событии измеряются `http_duration_ms` (запрос, чтение и закрытие ответа),
`parse_duration_ms` (вызов `Parse`) и `collection_duration_ms` (вся попытка до
записи итогового лога). Невыполненные этапы не содержат метрики; выполненные
могут занять 0 мс после округления. `collected_at` — начало попытки в UTC,
длительности считаются по монотонным часам. Универсальный `duration_ms` не выводится.

## Пакетный сбор

`internal/collect.CollectBatch` вызывает `Client.Collect` общим клиентом через
ограниченный worker pool и возвращает результаты в порядке входных сценариев:

```go
results, summary, err := collect.CollectBatch(ctx, client, requests, collect.BatchOptions{
    Workers:       3,
    StartInterval: 200 * time.Millisecond,
})
```

Сценарии из конфигурации для одной даты заезда выполняются через
`collect.CollectConfiguredBatch(ctx, client, hotel, checkIn, options)`:
каждое значение `collection.adults` комбинируется с LOS 1/2/3.
Поддерживаются `"adults": 2` и `"adults": [1, 2]`; каждый результат сохраняет
свои даты и число взрослых в `Request`.

`collect.AggregateCategoryStock(results)` сохраняет отдельные наблюдения и
объединяет подтверждённые остатки только для одного отеля и одинаковых дат.
Равные количества не суммируются; разные дают `conflict` без общего количества.
Неподтверждённый сценарий остаётся `unknown`. Хранилище истории пока не реализовано.

`Workers=0` означает 3 workers, допустимы 1–32. `StartInterval` ограничивает
интервал между допусками к `Collect` внутри пакета; 0 отключает ограничение частоты.
Ошибки сценариев остаются в `SearchResult`; `err` сообщает о неверных настройках.

При отмене незапущенные сценарии сохраняются как `failed` с кодом `canceled`
(или `timeout` при истечении deadline), нулевыми `CollectedAt` и `HTTPStatus`.
`summary` содержит время пакета и счётчики исходов, отдельно — отменённые начатые
(`CanceledCount`) и незапущенные (`NotStartedCount`) сценарии; оба входят в `FailedCount`.
На DEBUG `queue_wait_ms` считается от момента перед попыткой отправки конкретного
задания в очередь до допуска к `Collect`: включает ожидание свободного worker,
`StartInterval` и глобального допуска, без времени предыдущих заданий до постановки
этого задания. Событие содержит `batch_id` и индекс сценария. Собственные
`attempt_id` и метрики каждой попытки формирует существующий `Collect`.

## Сбор нескольких отелей

```go
// Один раз при запуске, до начала любых пакетов; по умолчанию 8, допустимы 1–32.
if err := collect.ConfigureGlobalConcurrency(8); err != nil {
    return err
}
results, summary, err := collect.CollectProperties(ctx, []collect.PropertyCollection{
    {Hotel: arena33, CheckIn: checkIn},
    {Hotel: lindsay, CheckIn: checkIn},
}, collect.BatchOptions{StartInterval: 200 * time.Millisecond})
```

Каждый отель разворачивается в adults × LOS. Задания идут через существующий пул
с небуферизованной очередью; отдельных пулов на отель нет. `Workers=0` здесь
означает 8; у прежних `CollectBatch` / `CollectConfiguredBatch` остаётся 3.
Все три API разделяют процессные ограничения, включая параллельные batch:
по умолчанию глобально 8, на физический отель 4, на origin источника 8.
Готовые задания чередуются round-robin между отелями. Занятый отель/источник
пропускается; другие готовые задания не ждут его освобождения. Между batch
очерёдность допусков защищает ожидающий отель от обгона готовыми новыми заданиями.
Итоговый порядок результатов при этом сохраняется.

Допуски резервируются атомарно: проверяются отель, источник, rate limiter и
глобальный предел. Частичных резервирований нет; глобальный слот не удерживается
при ожидании других лимитов. Все разрешения освобождаются после каждой попытки,
в том числе при ошибке и панике; паника не скрывается. Слот охватывает весь
`Collect`, включая чтение тела и парсинг. Настройки меняются только между batch.

Результаты упорядочены по входным отелям, затем adults и LOS из конфигурации.
`PropertyResult` содержит ID отеля из конфигурации, источник, LOS, `QueueWaitMS`
и прежний `SearchResult` с датами и adults. При отмене незапущенные задания остаются
в списке как `failed` с `canceled`/`timeout`, без HTTP-метаданных и с `QueueWaitMS=nil`.
Ошибка отдельного сценария не прерывает остальные. Это явный запуск независимо
от `collection.enabled`, как `CollectConfiguredBatch`.

Клиент создаётся один раз на BaseURL внутри пакета; можно передать готовый `Client`
для повторного использования. DEBUG-события показывают `queue_wait_ms`,
`active_scenarios` всего процесса и `concurrency_limit`. Сводка содержит пик
сценариев пакета и исходы по отелям; события `bnovo.batch.property.completed`
связаны общим `batch_id`. Добавлены `active_property`, `active_source`, безопасный
`source_origin`, `retry_count`, `throttled_count` (ответы HTTP 429),
`rate_wait_count` (попытки, ожидавшие source interval) и `limiter_wait_ms`.
Последняя метрика суммирует ожидание допуска после доступности worker и может
пересекаться с `queue_wait_ms`; их нельзя складывать как длительности этапов.
Измерения #13 сохраняются без дублирования.

Настройка лимитов до запуска пакетов:

```go
if err := collect.ConfigureLimits(collect.Limits{
    PropertyConcurrency: 4,
    SourceConcurrency:   8,
    Sources: map[string]collect.SourceLimit{
        "https://reservationsteps.ru": {
            Concurrency:   8,
            StartInterval: 500 * time.Millisecond,
        },
    },
}); err != nil {
    return err
}
```

Допустимая конкурентность — 1–32; ноль выбирает значения по умолчанию.
Origin нормализуется как схема + host + port, с удалением стандартного порта,
пути, query и credentials. Физический отель определяется по origin + UID,
поэтому конфигурационный ID и UID прежнего `CollectBatch` делят один допуск.
Для `https://reservationsteps.ru` автоматически действует встроенное правило:
8 одновременных попыток и минимум 500 мс между стартами (2 старта в секунду).
Вызов `ConfigureLimits` не обязателен; `ConfigureLimits(Limits{})` сохраняет это
правило. Явная запись этого origin в `Sources` переопределяет его, в том числе
`StartInterval: 0` отключает паузу. `SourceStartInterval` задаёт интервал для
источников без отдельного правила; для них ноль означает отсутствие пауз.
`BatchOptions.StartInterval` остаётся дополнительным интервалом этого batch:
выполняются оба условия.
Source interval дополнительно проверяется непосредственно перед базовым
`http.RoundTripper.RoundTrip`: общий для всех Bnovo-клиентов процесса limiter
учитывает прямые вызовы, retry и разрешённые redirects. Admission сохраняет
pacing/fairness очереди, но сам по себе не гарантирует интервал: проверки lease
и SQL между допуском и HTTP могут сблизить старты. Финальный gate ждёт только
оставшуюся часть интервала, учитывает context и не сериализует in-flight запросы.
Гарантия 500 мс относится к вызовам базового transport одного нормализованного
origin, а не ко времени получения пакетов сервером; сетевые RTT не контролируются.
Ожидание transport входит в существующий `http_duration_ms`; `limiter_wait_ms`
по-прежнему измеряет ожидание admission.

Все пакетные API по умолчанию делают до 3 попыток. Политика настраивается через
`BatchOptions.Retry`; явное `MaxAttempts: 1` отключает retry, верхний предел — 5.
По умолчанию backoff начинается с 1 с, удваивается до 10 с и использует
equal jitter: 50–100% текущей задержки.
`InitialBackoff` / `MaxBackoff` настраиваются; максимальный backoff — 1 минута.
Корректный `Retry-After` (секунды или HTTP-date) задаёт минимальную задержку и
может превышать MaxBackoff; ожидание остаётся отменяемым.

Повторы разрешены только для 429, 500/502/503/504, таймаутов отдельной попытки и
временных сетевых сбоев. Постоянные ошибки, отмена контекста, `partial` и
`no_offers` не повторяются. Во время backoff workers и допуски свободны;
таймер ожидания один на диспетчер, goroutine на каждый сценарий не создаётся.
`Client.Collect` по-прежнему выполняет одну попытку: вложенного retry нет.
`PropertyResult.Attempts` содержит фактическое число начатых попыток, а `Result`
— последнюю попытку; `QueueWaitMS` сохраняет ожидание исходной попытки.
Промежуточные попытки доступны в логах, durable history пока не реализована.

Лимиты конкурентности действуют для пакетных API в одном процессе. Прямые вызовы
Bnovo тоже используют source start interval. Для нескольких процессов / Vercel
Functions нужна распределённая координация (#33); до её реализации обязателен
существующий singleton guard Bnovo (#27). Материализация входных заданий и результатов занимает O(N) памяти;
очередь и число workers ограничены. Состояние rate limiter хранится в памяти
по origin и не переживает перезапуск. Durable queue, история и восстановление
незавершённых сценариев остаются для следующих задач (#24–27).

## Регулярный сбор групп отелей

`internal/schedule` разделяет planner (`BuildPlans`), runner (`collect.RunGroups`)
и application service (`Service.Execute`). CLI и HTTP используют один service.
Это единственный production-путь сбора с PostgreSQL-очередью, проверкой категорий,
leases и fenced-записью наблюдений. `RunQueued` — обёртка этого runtime для одной группы.
Внешний cron инициирует запуск; фонового планировщика после HTTP-ответа нет.

В [`configs/groups.json`](configs/groups.json) указываются `id`, отдельная `schema`
и пути конфигураций относительно файла реестра. Вторая группа добавляется вторым
элементом `groups` с другим ID и схемой, например `hotel_parser_hotel_b`.
Одинаковые property ID между группами допустимы. Схемы `hotel_parser`, `public`,
`pg_*` и повторное использование схемы двумя группами запрещены.

Регулярный сценарий: **1 взрослый, 1 номер, без детей**, LOS берётся из
`collection.nights` (1/2/3). Все шесть активных отелей используют `[1,2,3]`;
прежний `[1]` продолжает давать только LOS=1. Поле `collection.adults` остаётся
диагностическим, регулярный сбор использует одного взрослого.

| Слот МСК | Cron UTC | LOS=1: даты заезда | LOS=2/3: даты заезда | Jobs/отель | Jobs/6 отелей |
|---|---|---|---|---:|---:|
| 08:00 | `0 5 * * *` | сегодня … +60 | сегодня … +7 | 77 | 462 |
| 12:00 | `0 9 * * *` | сегодня … +7 | сегодня … +7 | 24 | 144 |
| 16:00 | `0 13 * * *` | сегодня … +7 | сегодня … +7 | 24 | 144 |
| 20:00 | `0 17 * * *` | сегодня | сегодня | 3 | 18 |

Границы включительные. Итого **128 сценариев на отель, 768/сутки на шесть отелей**,
без retry. Короткий горизонт LOS=2/3 задан константой `extendedLOSDaysAhead=7`.
Дата заезда определяется timezone отеля; CheckOut вычисляется AddDate(0,0,LOS).
Run.PeriodEnd — максимальная дата выезда всех сценариев, а не последний check-in.

UUID по-прежнему включает group/property/логический слот. Уже опубликованный
LOS=1-план не расширяется: service проверяет точный legacy-кандидат прежним строгим
PlanRun/hash и продолжает его исходные jobs. Новый, ещё не опубликованный слот
использует collection.nights. Изменения нового multi-LOS-плана или источника остаются
ErrConflict; проверка hash не отключается. Повтор plan/start/resume не создаёт копий
run/jobs, история и inventory snapshot не переписываются.

Устаревший registry `los` — явный single-LOS override, который обязан присутствовать
в collection.nights; 0/отсутствие использует весь список. `los2_evidence` остаётся
совместимой необязательной метаинформацией только при los=2, не разрешением на сбор
и не доказательством minimum stay. Обычному LOS=2/3 evidence не требуется.

Отсутствие предложения при LOS=1 и наличие при LOS=2/3 сохраняются отдельно;
minimum_stay автоматически не устанавливается. CSV v4 содержит 36 колонок,
разные check_out/los и полную цену за период. Квоты между LOS не суммируются;
для последующего расчёта средней цены за ночь используется фактический LOS.
[Проверки перехода и оценка нагрузки](docs/schedule-multi-los.md).

CLI (DSN берётся только из `HOTEL_PARSER_DATABASE_URL`, `.env` не исполняется):

```bash
make build
./bin/collection migrate                     # Явный DDL во всех настроенных схемах.
./bin/collection plan --slot=2026-10-31T08:00:00+03:00
./bin/collection plan --due                  # Пропущенные слоты текущего дня МСК.
./bin/collection resume --execution-timeout=45s --shutdown-grace-period=5s --max-jobs=64
./bin/collection tick --slot=2026-10-31T12:00:00+03:00
./bin/collection serve --listen=127.0.0.1:8080
```

`plan` не обращается к Bnovo. `resume` продолжает все готовые незавершённые run,
включая предыдущие дни; `tick` сначала продолжает ограниченную пачку старых jobs,
резервируя время для публикации слота, затем планирует и продолжает очередь.
По умолчанию: 8 workers, 64 claim за вызов, execution 45 секунд, grace 5 секунд.
Очередь отбирается по кругу между группами/отелями; история claim сохраняет
fairness между короткими вызовами. Pool и лимиты процесса общие для всех групп.
Прогресс по group/schema/run читается из БД; завершённые run получают finished_at.
После исчерпания бюджета отчёта возвращается `progress_known=false` и ошибка.

Для Docker используется тот же binary:
`docker run --rm --network=<сеть_БД> --env HOTEL_PARSER_DATABASE_URL --entrypoint /app/collection hotel-parser resume`.
Образ содержит реестр и конфигурации; другой реестр можно передать read-only томом
и `--groups`. В Docker DSN указывает на compose-хост `postgres`, а не `localhost`.
Production Neon автоматически не мигрируется и не настраивается.

HTTP: задайте `HOTEL_PARSER_TRIGGER_SECRET` (случайный секрет от 32 байт) в окружении
сервера. Внешний TLS/reverse proxy должен передавать заголовок Authorization.

```bash
curl --fail-with-body -X POST http://127.0.0.1:8080/trigger \
  -H "Authorization: Bearer $HOTEL_PARSER_TRIGGER_SECRET" \
  -H 'Content-Type: application/json' \
  --data '{"action":"plan","slot":"2026-10-31T08:00:00+03:00"}'
# Для продолжения: --data '{"action":"resume"}'
```

Секрет не принимается в URL. Actions: `plan`, `resume`, `tick`; HTTP 409 означает,
что другой runner уже работает, 503 — незавершённую обработку/ошибку. Повтор
доставки безопасен. Обработчик дожидается runtime и flush до ответа.

На Docker/VPS настройте **внешний cron** (ниже timezone crontab — UTC, DSN передан
через окружение процесса; путь `/opt/hotel-parser` замените своим):

```cron
0 5 * * * cd /opt/hotel-parser && ./bin/collection plan --slot="$(TZ=Europe/Moscow date +\%Y-\%m-\%dT08:00:00+03:00)"
0 9 * * * cd /opt/hotel-parser && ./bin/collection plan --slot="$(TZ=Europe/Moscow date +\%Y-\%m-\%dT12:00:00+03:00)"
0 13 * * * cd /opt/hotel-parser && ./bin/collection plan --slot="$(TZ=Europe/Moscow date +\%Y-\%m-\%dT16:00:00+03:00)"
0 17 * * * cd /opt/hotel-parser && ./bin/collection plan --slot="$(TZ=Europe/Moscow date +\%Y-\%m-\%dT20:00:00+03:00)"
* * * * * cd /opt/hotel-parser && (./bin/collection plan --due --execution-timeout=45s; ./bin/collection resume --execution-timeout=45s --shutdown-grace-period=5s)
```

Для HTTP/serverless внешний scheduler отправляет `plan` в те же четыре слота
и **`resume` каждую минуту** до опустошения очереди и далее для будущих retry/leases.
При ошибке plan повторяется с исходным slot timestamp. Это обязательный механизм
продолжения; одного следующего суточного запуска недостаточно. На сигнал/отмену
новые операции прекращаются, готовые ответы сохраняются с fencing, остальные jobs
восстановит следующий вызов после истечения lease. Конфигурации отелей с backlog
нужно сохранять в реестре. Лимит среды должен покрывать execution + grace.

**Bnovo: один активный application runner на одну PostgreSQL database.** Session
advisory lock общий для всех групп и процессов, guard проверяется перед HTTP;
второй runner не отправляет запросы. Нужен direct/session DSN, transaction-pooling
DSN не поддерживается. При передаче lock выдерживается пауза 500 мс, затем действуют
лимиты #23: глобальный 8, отель 4, origin 8 и 500 мс между стартами публичного Bnovo.
Не запускайте другие Bnovo-клиенты в обход service одновременно с scheduled runner.
Это singleton-ограничение, **не распределённый rate limiter**. До горизонтального
исполнения нескольких runner обязательна [задача #33](https://github.com/p-force/hotel-parser/issues/33).

## PostgreSQL: задания и история

`internal/storage` не зависит от Bnovo. Драйвер — pgx через `database/sql`.
По умолчанию используется отдельная схема `hotel_parser`; миграции запускаются
явно через `Store.Migrate(ctx)`, а не при открытии соединения.

```go
store, err := storage.Open(ctx, os.Getenv("HOTEL_PARSER_DATABASE_URL"), storage.Options{})
if err != nil {
    return err
}
defer store.Close()
if err := store.Migrate(ctx); err != nil {
    return err
}
```

Схема содержит:

- `schema_migrations`: версии и SHA-256 миграций, транзакционный advisory lock;
- `collection_runs`: один запуск отеля с периодом, временем и итоговым статусом;
- `scenario_jobs`: источник/origin/UID, даты, LOS, adults, rooms, children, язык,
  состояние, число попыток и `next_retry_at`;
- `collection_attempts`: **каждая** выполненная попытка, включая промежуточные
  retry, HTTP-статус, безопасный код ошибки, retryable, время и длительность;
- `observations`: один нормализованный публичный остаток и площадь категории на попытку;
- `observation_variants`, `observation_offers`: варианты, тарифы, точная цена
  `NUMERIC`, валюта и исходные признаки доступности.

`unknown`/`conflict` имеют `available=NULL`. Подтверждённая недоступность хранится
отдельным флагом; при достоверном нуле остаток равен 0.
Для полного ответа с точными `.tariff__nodates` отсутствие `$plansForJs` не мешает
`no_offers` и `known=0`. Это публичная недоступность по сценарию, не физическая
занятость. При тарифной структуре `plans_metadata_missing` сохраняется с контекстом
категории; неопределённая привязка остаётся глобальной. Неверные/неоднозначные даты,
ограничения проживания и пропущенные ожидаемые категории не подтверждают ноль.
Полноту относительно конфигурации проверяет `ValidateExpectedCategories` перед
нормализацией; `RunQueued` выполняет эту проверку перед сохранением. Тарифы и варианты не
суммируются: публичный остаток берётся только из `observations.available`.
Сохраняются коды/контексты предупреждений и данные `partial`. HTML, cookies,
свободные тексты ошибок, условия оплаты/отмены и платёжные URL не сохраняются.

`SaveAttempt` одной транзакцией вставляет попытку и наблюдения, затем меняет статус
задания. Уникальность `(job_id, attempt_no)` и hash канонического payload дают
идемпотентность: одинаковая запись — no-op, изменённая — `ErrConflict`. Записи
одного задания сериализуются row lock; порядок номеров проверяется. Задание
идемпотентно создаётся по `(run_id, scenario_key)`, ключ включает все параметры
сценария. Новый сбор создаёт новые run/job и не перезаписывает старые значения.

UPDATE/DELETE/TRUNCATE фактов и изменение идентичности run/job запрещены
триггерами; внешние ключи, UNIQUE и CHECK защищают связи и значения. Состояние
незавершённого задания меняется отдельно от неизменяемых фактов.

### Конкурентные исполнители и аренда задания

Миграция `0002_job_leases.sql` добавляет владельца, монотонный fencing token,
срок аренды и бюджеты повторов. `ClaimJobs` атомарно получает готовые задания
через `FOR UPDATE SKIP LOCKED` и `UPDATE RETURNING`; транзакция завершается до
HTTP. Время готовности и срок аренды проверяются по часам PostgreSQL.

```go
jobs, err := store.ClaimJobs(ctx, storage.ClaimOptions{
    Owner: "worker-1", BatchSize: 3, LeaseDuration: time.Minute,
    MaxClaims: 3, MaxAttempts: 3,
})
// Для каждого job: выполнить сбор вне транзакции, подготовить AttemptInput.
// lease := job.Lease()
// store.RenewLease(ctx, lease, time.Minute) // До истечения аренды.
// store.CompleteJob(ctx, lease, input)     // success/partial/no_offers.
// store.FailJob(ctx, lease, input, storage.FailPolicy{})
```

По умолчанию batch равен 1, lease — минуте, оба бюджета — 3. Размер batch
должен соответствовать доступным workers; исполнитель сам продлевает аренды
до истечения. После истечения задание доступно другому процессу с новым token;
старый владелец получает `ErrLeaseLost` при продлении или записи. Повторный
`CompleteJob` с тем же payload и действующей генерацией не дублирует историю.
Гарантия — **at-least-once execution**, а не exactly-once HTTP: ответ потерянного
worker может потребовать нового запроса.

`Attempts` считает сохранённые HTTP-попытки, token — захваты, включая восстановление
после crash. Бюджеты фиксируются первым claim. Промежуточный HTTP-retry из #23
сохраняется через `SaveAttempt` с `AttemptInput.Lease`, удерживая аренду; оставшееся
число HTTP-попыток ограничивается `MaxAttempts - Attempts`. `FailJob` сохраняет
ошибку и освобождает аренду: retryable получает `next_retry_at` с exponential
backoff и jitter (по умолчанию 1–10 секунд), с учётом переданного `NextRetryAt`
как нижней границы, например для Retry-After. Неповторяемые ошибки и исчерпанные
бюджеты завершают задание. Crash без сохранённого ответа не создаёт фиктивной
HTTP-попытки. Эти методы не выполняют HTTP и не создают второй цикл HTTP-retry.

Длительность попытки измеряется монотонными часами вокруг её выполнения;
`collected_at` сохраняет отдельную метку источника.

### Runtime: deadline и graceful shutdown

`RunQueued` получает готовые задания из той же PostgreSQL-очереди и выполняет их
через существующий pool и HTTP-retry. Сценарии берутся из jobs; `CheckIn` у
`PropertyCollection` не используется. Передайте конфигурации всех отелей очереди
либо ограничьте выборку через `Claim.RunID`. Клиенты переиспользуются по origin.

```go
result, err := collect.RunQueued(ctx, store, properties, collect.RuntimeOptions{
    Batch: collect.BatchOptions{Workers: 8},
    Claim: storage.ClaimOptions{Owner: "worker-1", LeaseDuration: time.Minute},
    ExecutionTimeout: 45 * time.Second,
    ShutdownGracePeriod: 5 * time.Second,
})
```

Пачка не превышает число workers. Heartbeat продлевает аренды, включая ожидание
допуска и backoff; при отмене прекращаются claim, renew и новые HTTP-попытки.
Активные запросы отменяются, все goroutine завершаются до возврата. Завершённые
ответы проходят нормализацию и fenced-запись с отдельным ограниченным context.
Один grace period действует на весь shutdown, включая чтение прогресса; ошибка
записи останавливает execution. Прерванные задания не становятся terminal canceled:
они восстанавливаются после истечения lease, с сохранением уже записанных retry.

`result.Progress` читается из committed PostgreSQL state: completed, failed,
canceled, queued, running, retryable и remaining. Если бюджет чтения истёк,
`ProgressKnown=false` и возвращается ошибка; локальный HTTP success не означает
сохранённый результат. Runtime возвращается, когда готовых заданий больше нет;
ожидание будущих retry/lease и повторный запуск задаёт вызывающая сторона.

Нулевой execution timeout использует deadline родителя; grace по умолчанию —
5 секунд, renew interval — треть lease. Бюджет среды должен покрывать execution
и grace: после принудительного kill сохранение не гарантируется. Отмена
кооперативная; синхронные разбор HTML и расчёт остатков не прерываются посередине.
Распределённые лимиты запросов и платформенные адаптеры не добавлены.

Для чтения есть `GetRun`, `GetJob`, `GetAttempt`; обычный SELECT также сохраняет
измерения сценария без удвоения остатков:

```sql
SELECT r.property_id, j.check_in, j.check_out, j.los, j.adults,
       a.attempt_no, o.category_id, o.available, o.stock_status,
       o.confirmed_unavailable
FROM hotel_parser.collection_runs r
JOIN hotel_parser.scenario_jobs j ON j.run_id = r.id
JOIN hotel_parser.collection_attempts a ON a.job_id = j.id
JOIN hotel_parser.observations o ON o.attempt_id = a.id;
```

Интеграционные тесты запускаются отдельно:

```bash
# Рекомендуется для уже работающего PostgreSQL из docker-compose проекта:
make test-integration-docker

# Альтернатива: явно экспортированный корректный локальный DSN:
make test-integration
```

Первый вариант берёт настройки из существующего compose-контейнера `postgres`,
компилирует Linux test binaries и выполняет их внутри контейнера. Он не создаёт
новый PostgreSQL, не зависит от сервера на порту хоста и не выводит credentials.
`.env` автоматически не исполняется. Каждый тест использует случайную схему
`hp_test_*` и удаляет только её; существующие таблицы не изменяются.
Integration-tag тесты отказываются от нелокального DSN. Обычные `go test ./...`
и `make check` проверяют unit-тесты без подключения к БД.

Будущее подключение к существующей Neon БД:

1. Проверить миграции на отдельной тестовой БД/ветке и сделать backup.
2. Выделить владельца миграций и схему `hotel_parser`; не использовать таблицы
   и историю миграций предыдущего проекта.
3. Передать DSN через секрет `HOTEL_PARSER_DATABASE_URL`; открыть `storage.Open`
   с `Schema: "hotel_parser"` и применить `Migrate` контролируемым отдельным шагом.
4. Проверить ledger и SELECT; выдать приложению минимальные права на новую схему
   (история INSERT/SELECT, прогресс run/job UPDATE), без доступа к старым таблицам.

Neon не подключается. Runtime-адаптеры, Vercel runtime budget, cron и горизонт
60 дней остаются для следующих задач.

## Ручная сверка публичного фонда

```bash
go run ./cmd/inventory-check --config=configs/arena33.json
go run ./cmd/inventory-check --config=configs/arena33.json --date=2026-12-21
go run ./cmd/inventory-check --config=configs/lindsay.json --date=2027-02-19 --adults=1
```

По умолчанию заезд выбирается через 60 календарных дней от сегодняшней даты
в часовом поясе отеля. Выполняется один сценарий: 1 ночь, без детей; `--adults=1|2` (по умолчанию 2).
Вывод содержит даты, LOS, число взрослых и достоверность остатка.
Явная дата не может быть в прошлом относительно даты отеля.

Свёрнутые через `d-none` предложения учитываются при согласованных сигналах
количества и активном селекторе; `Visible` сохраняет исходный признак отображения
HTML. Число взрослых из `.guest-list` описывает вариант: для меньшего запроса
требуется явный `tr[data-available="1"]` в ответе этого сценария. Без этого флага
сохраняется точное сопоставление; вариант на одного гостя не подходит двум.
Цены хранятся отдельно по запросу, варианту и тарифу.
Проверка Lindsay с исходными признаками и ценами —
[в отчёте](docs/lindsay-guest-matching-2027-02-19.md).

В stdout выводится таблица всех настроенных категорий; неподтверждённые остатки —
`unknown`, противоречия — `conflict`. При росте формируется WARN
`bnovo.inventory.max_increased` с `config_updated=false`; JSON-конфигурация не меняется.
Код завершения: 0 — полная сверка, 1 — `partial`, неизвестные остатки или сбой,
2 — неверные аргументы/конфигурация. Предупреждения и логи идут в stderr.

## Конфигурация

Пример конфигурации отеля — [`configs/arena33.json`](configs/arena33.json):

- источник Bnovo и часовой пояс `Europe/Moscow`;
- 19 номеров в категориях Stadium, Garden и Premium;
- поиск на 60 дней вперёд, два взрослых без детей, один номер, 1–3 ночи.

Пакет `internal/config` загружает JSON через `config.Load(path)` и проверяет
обязательные поля, адрес источника, номерной фонд и параметры поиска.
Неизвестные поля и лишние данные после JSON отклоняются.

В [`.env.example`](.env.example) подготовлены переменные для PostgreSQL,
таймаута и Bnovo. Автоматическое чтение `.env` пока не реализовано.

## Разработка

```bash
make run        # Запустить приложение
make build      # Собрать bin/hotel-parser
make test       # Запустить тесты
make test-race  # Запустить тесты с детектором гонок
make fmt        # Отформатировать Go-файлы
make fmt-check  # Проверить форматирование отслеживаемых Go-файлов
make vet        # Выполнить статический анализ
make tidy       # Обновить зависимости модуля
make check      # Проверить форматирование, выполнить vet, test-race и build
```

GitHub Actions выполняет проверки форматирования, статический анализ, тесты
с детектором гонок и сборку при push и pull request в `dev` и `master`.

История изменений — в [CHANGELOG.md](CHANGELOG.md).

### Доставка CSV на Яндекс Диск

После terminal run `plan`/`tick`/`resume` сверяют очередь доставок в PostgreSQL.
Отправка выполняется после collection workers и освобождения runner lock:
не более двух доставок и 10 секунд на вызов, в пределах execution deadline.
Незавершённые runs не отправляются; ошибка Диска не меняет результат сбора.

- `YANDEX_DISK_TOKEN` — OAuth из окружения с правами `cloud_api:disk.read`
  и `cloud_api:disk.write` (чтение metadata для проверки, запись CSV).
- `YANDEX_DISK_DIR` — корень, по умолчанию `/hotel-parser`.
- Перед использованием примените миграцию `0005` штатной командой `migrate`.

Пример пути: `/hotel-parser/arena33/arena33/2026-11-01_08-00.csv`.
Имя — логический слот run в `Europe/Moscow`, а не время загрузки. Группа и отель
изолируют файлы; один файл на слот. Путь фиксируется при первой постановке доставки;
изменение root и формата не меняет старые `remote_path` и не переносит файлы.
OAuth передаётся только `cloud-api.yandex.net`; upload href и redirects проверяются,
на PUT токен не отправляется. Файлы не публикуются в общий доступ.

`deliveries` в ответе содержит состояние доставки отдельно от результата collection.
Без токена: `pending`, диагностика `disk_token_missing`, без HTTP-запросов к Диску.
Полное состояние хранится в `<schema>.csv_deliveries`:

```sql
SELECT group_id, run_id, remote_path, state, attempts, next_attempt_at, error_code
FROM hotel_parser.csv_deliveries ORDER BY next_attempt_at;
```

Повторы выполняются очередным `resume`/`tick`: exponential backoff от 5 секунд
до 10 минут плюс jitter до 25%, с учётом `Retry-After` (до суток).
401/403 и конфигурационные ошибки откладываются минимум на 6 часов.
После исправления токена перезапустите процесс. Для немедленного повтора конкретного run:

```sql
UPDATE hotel_parser.csv_deliveries SET next_attempt_at=now()
WHERE group_id='<group>' AND run_id='<run_id>' AND state='retryable_failed';
```

Затем выполните обычный `resume`. Активные leases не сбрасывайте.
Прерванный delivery claim восстанавливается через 2 минуты; устаревший token
не может подтвердить доставку. Вне транзакции CSV потоково генерируется из PostgreSQL
для checksum, затем для PUT; постоянный локальный файл не нужен.
После PUT или потерянного ответа проверяются размер и SHA-256/MD5 удалённого файла.
`delivered` фиксируется только после подтверждения файла и commit; при повторе
существующий совпадающий файл не загружается заново, отличающийся не перезаписывается.
Реализация не обещает distributed exactly-once. Для повторов нужен следующий вызов
`resume`/`tick`; фонового процесса отправки нет.

### Docker/VPS: регулярный запуск и контроль

Готовые crontab/скрипт и опциональный Compose overlay: [deploy](deploy/README.md).
`plan --due` идемпотентно восстанавливает наступившие слоты **текущего** московского дня;
вчерашние пропущенные snapshots автоматически не запрашиваются задним числом.
Очередь уже опубликованных старых runs продолжает обычный `resume`.
Состояние по отелям/CSV/delivery: [deploy/status.sql](deploy/status.sql).

VPS RUVDS / Debian 13: [инструкция развёртывания](deploy/VPS.md).
Отдельный Compose без публичного PostgreSQL, env-лимиты, backup/restore; cron выключен до явного включения.
