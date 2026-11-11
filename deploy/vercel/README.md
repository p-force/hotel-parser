# Временный deployment на Vercel

Только ветка `deploy/vercel`. Не сливать её в `dev`/`main`. Основной CLI, очередь, адаптеры, миграции, CSV и доставка переиспользуются. Для `/start` в существующий service добавлен порядок plan-before-run, а в claim — опциональные фильтр слота и приоритет новых runs; без новых таблиц/очередей.

Фактический ограниченный E2E и оставшиеся ограничения: [проверка 10.11.2026](verification/2026-11-10.md).

## Целевые ресурсы

- Vercel: команда `dianas-projects-a75fd820`, проект `hotel-parser`, ID `prj_8LrZh5Obv8pUw8jpj6YGFzbK5fSz` (предоставлен пользователем).
- Git: `p-force/hotel-parser`, Production Branch **`deploy/vercel`**. Framework Preset: **Go**; entrypoint `cmd/api/main.go`, build command `go build -buildvcs=false -o server ./cmd/api`. Install/Output overrides отсутствуют. Один Go HTTP-server Function обслуживает пять защищённых маршрутов.
- Регион: `fra1`, предел HTTP Function **260 s** (настройка проекта `resourceConfig.functionDefaultTimeout`, проверять также timeout lambda deployment). Адаптер ограничивает запрос/HTTP write 250 s, подключение 8 s, service — 220 s, graceful flush — 5 s. Workers=8, max-jobs по умолчанию64. Source HTTP timeouts, rate limits и retry не меняются.
- Новый Neon: `shiny-grass-79482545`, `hotel-parser`, `aws-eu-central-1`, PostgreSQL 18, `production` / `br-odd-pine-b9f2vemz`, БД `neondb`.
- Direct host: `ep-hidden-breeze-b9oouvds.c-7.eu-central-1.aws.neon.tech`. Runtime отклоняет другой host, другую БД, отсутствие TLS и fallback без TLS, включая transaction pooler.
- В текущем `configs/groups.json` одна группа `arena33` и схема `hotel_parser_arena33`. Будущая `hotel_parser_ni_teta` не создаётся до отдельного подключения группы.

Старые `arena-hotel` и Neon `bold-wildflower-33104468` не изменять. До проверки отсутствия одновременно работающего старого сборщика оставлять collection выключенным.

## Переменные в Dashboard

В Settings → Environment Variables проекта `hotel-parser` добавить вручную для **Production**. После изменения переменных нужен новый deployment. Не переносить эти production-секреты в Preview; Preview для проверки может работать без БД/токенов.

| Переменная | Значение / требование |
|---|---|
| `HOTEL_PARSER_DATABASE_URL` | Direct PostgreSQL URL **нового** Neon: host выше, database `neondb`, `sslmode=require` или `verify-full`. Без `-pooler`; advisory locks используют сессию |
| `HOTEL_PARSER_TRIGGER_SECRET` | Собственный случайный секрет, не менее 32 байт; только Bearer header |
| `CRON_SECRET` | Другой случайный секрет, не менее 32 байт; необходим для path-based Schedules; минимум16 символов |
| `YANDEX_DISK_TOKEN` | OAuth-токен пользователя; вводить только в Dashboard |
| `YANDEX_DISK_DIR` | Например `/hotel-parser`; для smoke отдельная тестовая папка |
| `HOTEL_PARSER_LOG_FORMAT` | `json` |
| `HOTEL_PARSER_LOG_LEVEL` | `INFO` |
| `HOTEL_PARSER_COLLECTION_ENABLED` | **`false`** до проверки секретов, миграций и отсутствия второго сборщика. Только буквальное `true` разрешает plan/start/resume |
| `HOTEL_PARSER_MAX_JOBS` | `1` для ограниченного smoke, `64` штатно; допустимо 1..64 |

Шаблон без значений секретов: [env.example](env.example). Если обязательной настройки нет, plan/resume возвращает 503 до вызова application service. Секреты не принимаются в URL/body и не возвращаются в ответах. Значения ошибок источников и DSN также не возвращаются.

## HTTP

| Endpoint | Метод | Авторизация | Действие |
|---|---|---|---|
| `/api/health` | GET | Trigger Bearer | Liveness, загрузка встроенных конфигураций, флаги наличия настроек. **Не проверяет соединение с БД или доставку** |
| `/api/status` | GET | Trigger Bearer | Только чтение run progress и до 100 delivery records на группу; не завершает runs, не собирает и не загружает CSV |
| `/api/plan` | GET или POST без body | Trigger Bearer; Cron Bearer разрешён только для GET | Существующие `DueSlots(now)` и `Service.Execute(plan)` для наступивших слотов текущего московского дня |
| `/api/start` | GET или POST без body | Trigger Bearer; Cron Bearer только для GET | Только наступивший слот08:00 МСК текущего дня: commit всех планов, затем Runner только этого слота |
| `/api/resume` | GET или POST без body | Trigger Bearer; Cron Bearer разрешён только для GET | Существующий `Service.Execute(resume)`, ограниченная пачка очереди, существующий экспорт и delivery |

Query-параметры и body запрещены. GET Cron преобразуется во внутренний типизированный запрос, а не пересылается в POST Handler с ослабленной авторизацией. Ошибки: 401 unauthorized, 409 runner_busy, 503 configuration/collection_incomplete. Все ответы `Cache-Control: no-store`.

`resume` синхронно завершает service и закрывает соединения до HTTP-ответа. Нет фонового вызова после ответа, self-invocation или нового scheduler. При истечении бюджета сохраняется существующая семантика lease/fencing и восстановления. `Service` освобождает runner lock до CSV/upload; ledger доставки и исходные записи находятся в PostgreSQL. CSV можно заново сформировать на другом инстансе: `/tmp` не является источником истины.

Публичные JSON-конфигурации встроены через `configs/vercel_assets.go`. Для штатного `LoadRegistry()` они кратковременно материализуются в `/tmp`, после чтения удаляются. Дублирования конфигураций и зависимости от runtime cwd нет. Секреты не встраиваются. `.git` исключён из deployment; `-buildvcs=false` предотвращает ошибку Go VCS stamping после Git checkout и обработки `.vercelignore`. Файловые `/api/*.go` Functions переписывают имя модуля и ломают существующие Go internal-импорты; HTTP-server preset выбран после воспроизведения этой ошибки в облачной сборке. Ни `go.mod`, ни версия Go не меняются: Vercel читает `go 1.26.0`; официальный builder `@vercel/go@28.0.1` проверяется отдельно от облачного deployment.

## Контролируемые миграции

HTTP никогда не выполняет DDL. Перед миграциями проверить именно новую БД: существующие схемы, таблицы, ledger. При первичной проверке 10.11.2026 в новом `neondb` была только `public`; миграции ещё не применялись.

После безопасной настройки direct DSN в локальном окружении запустить существующий runner отдельно. Следующий preflight не печатает строку подключения и не позволяет случайно выполнить команду на старом host:

```bash
python3 - <<'PY'
import os, subprocess
from urllib.parse import urlparse, parse_qs
u = urlparse(os.environ.get('HOTEL_PARSER_DATABASE_URL', ''))
assert u.scheme in ('postgres', 'postgresql')
assert u.hostname == 'ep-hidden-breeze-b9oouvds.c-7.eu-central-1.aws.neon.tech'
assert u.path == '/neondb'
assert parse_qs(u.query).get('sslmode') in (['require'], ['verify-full'])
subprocess.run(['go', 'run', './cmd/collection', 'migrate',
                '--groups', 'configs/groups.json', '--execution-timeout', '60s'], check=True)
PY
```

Повторить контролируемую миграцию для проверки идемпотентности. Runner применяет встроенные `0001`–`0005`, проверяет checksum, использует advisory migration lock и атомарные транзакции. Не создавать таблицы вручную. После запуска проверить `hotel_parser_arena33.schema_migrations`, индексы/ограничения и два конкурентных session runner lock. Не создавать будущую схему ни-тета только ради deployment.

## Последовательность запуска

1. Проверить точные team/project/repository и Production Branch. CLI API подтвердил Hobby: встроенный ежеминутный Cron недоступен. Не делать вывод о плане по одному названию команды.
2. Развернуть код ветки с **отсутствующим `crons`** в `vercel.json`; секреты для сборки не нужны. Проверить облачный build, Go version, регион и URL deployment. Deployment Protection проверять штатным доступом Vercel, не отключать её ради обхода авторизации.
3. Добавить секреты самостоятельно, оставив `COLLECTION_ENABLED=false`. Redeploy. Проверить health с Trigger Bearer и отказ без него; затем соединение/миграции отдельным действием и GET status. Передача заголовков через переменные окружения допустима, печатать их или включать curl trace нельзя.
4. Согласовать отсутствие второго производственного collector. Только затем временно включить collection, `MAX_JOBS=1`, отдельный `YANDEX_DISK_DIR` и провести ограниченный smoke. `plan` планирует **всех включённых отелей группы**: не вызывать его ради одного fixture-job. Для одного smoke подготовить явный изолированный fixture-run штатным storage API, без изменения scheduler; resume должен видеть только его очередь.
5. Проверить `SaveAttempt`, observations/offers и CSV: `NULL` не равно 0, `unknown/conflict` сохранены, исходные квоты не складываются по тарифам. Для `failed` без offers должна остаться диагностическая CSV-строка. Повторный resume и новый ephemeral filesystem не должны увеличивать jobs/attempts/offers или создавать второй PUT для delivered run.
6. Зафиксировать API checksum/размер и `state=delivered` для тестовой доставки. До фактической проверки БД и загрузки не объявлять этап успешным.
7. Только после проверки подготовить включение cron, не активируя параллельно старый collector. Готовая, **неактивная** секция: [cron.example.json](cron.example.json). Она не входит в корневой `vercel.json`.

## Утренний запуск и продолжение

`GET /api/start` выбирает **08:00 Europe/Moscow текущего дня**. До08:00 отвечает `slot_not_due`, не открывая БД. Даже при ручном вызове вечером не создаёт12:00/16:00/20:00. В `Service.Execute(start)` сначала берётся session runner lock, затем последовательно и идемпотентно публикуются все `PlanRun`, после их commit вызывается прежний `RunGroups`. При ошибке любого плана HTTP-сбор не начинается; частично опубликованный план восстанавливается повтором.

Claim `/start` ограничен точным `collection_runs.started_at=slot`, поэтому старая очередь не занимает его64jobs. `/resume` обрабатывает общий backlog с приоритетом новых slots **внутри каждого отеля**, сохраняя round-robin групп/отелей. Старый уже активный runner не прерывается: новый запрос получает409 `runner_busy`, его нужно повторить. Лишних source-запросов или выполнения terminal jobs нет. Без опций slot/latest штатный FIFO storage сохранён; миграция не требуется.

Ответ содержит `results` существующего service и `summary`: `planned_jobs`, `completed_jobs`, `remaining_jobs`. Для `/start` это durable итоги текущего слота, для `/resume` — всей очереди, **не число HTTP/claim текущего invocation**. `summary=null` при недостоверном progress; это не нулевой остаток очереди. Ошибка/deadline возвращает безопасный код и доступный progress. CSV/delivery выполняются существующим service после workers/lock, в оставшемся бюджете; при его исчерпании восстановятся следующим вызовом.

## Vercel Schedules

Корневой `vercel.json` содержит только два path-based Schedules, без legacy `crons`:

- `morning-start`: `0 8 * * *`, Europe/Moscow, GET `/api/start`.
- `morning-resume`: `*/15 8-11 * * *`, Europe/Moscow, GET `/api/resume`.

Первый resume с **scheduled_at08:00** авторизуется, но возвращает `initial_resume_skipped` до открытия service. Таким образом рабочие resume —08:15…11:45 без удаления09:00/10:00/11:00. Это не исключает overlap из-за15минутного Hobby jitter:409 `runner_busy` является ожидаемым безопасным результатом, а не причиной скрывать SQL/timeout. Изменять cron expression на `15,30,45 8-11` не стали: он потерял бы ещё три запуска в начале часов.

[Path Schedules](https://vercel.com/docs/schedules/handle-runs) посылают Bearer `CRON_SECRET` из env текущего deployment. У секрета минимум16 printable ASCII символов без краевых пробелов/переводов строк; ручной Trigger сохраняет минимум32. Секреты нельзя передавать в query/body. Заголовки `ce-id`, `ce-vssschedulename`, `ce-vssscheduledat` и остальные CloudEvent поля проверяются только после Cron Bearer. Trigger-запрос не может выдать себя за Schedule через заголовки. Общий секрет не является криптографической аттестацией платформы: окончательный факт доставки проверяется по execution history Vercel.

`CRON_SECRET` фиксируется при сборке deployment: изменение секрета без redeploy не обновит отправителя. `HOTEL_PARSER_COLLECTION_ENABLED` остаётся обязательным выключателем, Deployment Protection не отключается.

[Shedules на Hobby](https://vercel.com/docs/schedules/limits): минимальный интервал15минут, jitter всегда15минут, до5000запусков команды за billing period. Наши17вызовов/день включают один пропущенный начальный resume; лимит общий с другими проектами. Точное08:00 и завершение всех366jobs за утреннее окно не гарантируются. Path targets **не повторяются автоматически при409/5xx**. Если start не опубликовал слот, пустой resume не заменит Planner: нужны диагностика и отдельный повтор start, а не вывод об успехе по HTTP200.

### Логи и прогресс

- `schedule.execution.started/completed`: execution ID, проверенное имя, scheduled_at без jitter, actual_started_at, delay_ms, action, duration, stage, HTTP/status/error_code и durable counters.
- `schedule.request.rejected`: отказ авторизации без доверия к клиентским `ce-*` и без секретов.
- `collection.progress`: group/run ID, логический слот, все job statuses, remaining_before/after для того же immutable набора jobs, ready/lease/backoff.
- `collection.no_progress`: нет подтверждённой активности30минут и remaining не уменьшился при том же наборе. Время берётся из PostgreSQL attempts/job creation, поэтому диагностика работает после рестарта без новой таблицы или глобальной карты.

Summary start относится к опубликованному слоту. Summary resume — к runs, которые были незавершены в снимке до Runner (включая завершившиеся в этом вызове), без старого закрытого архива или нового слота, появившегося параллельно. Полная история остаётся в results/groups. При неизвестном progress —null, а не фиктивные нули. Completed/claimed различаются; сохранённые attempts являются активностью даже без уменьшения remaining при retry.

Расширенные локальные проверки, облачная конфигурация и фактический статус миграции: [Schedules verification](verification/2026-11-11-schedules.md). Историческая проверка260s: [duration evidence](verification/2026-11-11.md). Root config сохраняет framework Go, build command, fra1; project default и фактическая lambda —260s, HTTP250s, service220s, jobs64/workers8/grace5s. Старый cron.example.json — только исторический пример, **не применять его поверх Schedules**.

## Удаление адаптера при переходе на VPS

Основная ветка `dev` не содержит этих изменений: для VPS использовать её штатные `cmd/collection` и `deploy/README.md`. Остановить Vercel Cron/внешний trigger перед запуском VPS; оставить один active runner. Перенести нужные секреты защищённым способом, использовать тот же direct PostgreSQL и существующие миграции/ledger — повторно собирать завершённые runs не нужно. Адаптер можно оставить только в `deploy/vercel` либо удалить `cmd/api/`, `internal/vercelapp/`, `configs/vercel_assets.go`, `vercel.json`, `.vercelignore` и эти инструкции; вернуть добавления к Makefile/test script. Не удалять PostgreSQL history или delivery ledger.
