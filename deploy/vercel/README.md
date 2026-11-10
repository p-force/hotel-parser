# Временный deployment на Vercel

Только ветка `deploy/vercel`. Не сливать её в `dev`/`main`. Основной CLI, scheduler, очередь, адаптеры, миграции, CSV и доставка переиспользуются без изменения их реализации.

Фактический ограниченный E2E и оставшиеся ограничения: [проверка 10.11.2026](verification/2026-11-10.md).

## Целевые ресурсы

- Vercel: команда `dianas-projects-a75fd820`, проект `hotel-parser`, ID `prj_8LrZh5Obv8pUw8jpj6YGFzbK5fSz` (предоставлен пользователем).
- Git: `p-force/hotel-parser`, Production Branch **`deploy/vercel`**. Framework Preset: **Go**; entrypoint `cmd/api/main.go`, build command `go build -buildvcs=false -o server ./cmd/api`. Install/Output overrides отсутствуют. Один Go HTTP-server Function обслуживает четыре защищённых маршрута.
- Регион: `fra1`, предел HTTP Function 60 s (настройка проекта `resourceConfig.functionDefaultTimeout`). Адаптер ограничивает запрос 55 s, подключение 8 s, существующий service — 40 s, graceful flush — 5 s. Рабочих потоков 8, максимум jobs на invocation по умолчанию 8.
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
| `CRON_SECRET` | Другой случайный секрет, не менее 32 байт; необходим при включении Vercel Cron |
| `YANDEX_DISK_TOKEN` | OAuth-токен пользователя; вводить только в Dashboard |
| `YANDEX_DISK_DIR` | Например `/hotel-parser`; для smoke отдельная тестовая папка |
| `HOTEL_PARSER_LOG_FORMAT` | `json` |
| `HOTEL_PARSER_LOG_LEVEL` | `INFO` |
| `HOTEL_PARSER_COLLECTION_ENABLED` | **`false`** до проверки секретов, миграций и отсутствия второго сборщика. Только буквальное `true` разрешает plan/resume |
| `HOTEL_PARSER_MAX_JOBS` | `1` для ограниченного smoke, `8` штатно; допустимо 1..64 |

Шаблон без значений секретов: [env.example](env.example). Если обязательной настройки нет, plan/resume возвращает 503 до вызова application service. Секреты не принимаются в URL/body и не возвращаются в ответах. Значения ошибок источников и DSN также не возвращаются.

## HTTP

| Endpoint | Метод | Авторизация | Действие |
|---|---|---|---|
| `/api/health` | GET | Trigger Bearer | Liveness, загрузка встроенных конфигураций, флаги наличия настроек. **Не проверяет соединение с БД или доставку** |
| `/api/status` | GET | Trigger Bearer | Только чтение run progress и до 100 delivery records на группу; не завершает runs, не собирает и не загружает CSV |
| `/api/plan` | GET или POST без body | Trigger Bearer; Cron Bearer разрешён только для GET | Существующие `DueSlots(now)` и `Service.Execute(plan)` для наступивших слотов текущего московского дня |
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

## Расписание и ограничения плана

Времена Europe/Moscow: 08:00 → 61 job на отель; 12:00 и 16:00 → по 8; 20:00 → 1. Регулярный сценарий остаётся adults=1, rooms=1, LOS=1, без детей, `ru`. LOS=2/3 не добавляются. UTC cron для plan: `0 5,9,13,17 * * *`; resume: `* * * * *`. Повторное планирование использует существующую идентичность слота и PlanRun.

По [документации Vercel Cron](https://vercel.com/docs/cron-jobs/usage-and-pricing) на 10.11.2026: Hobby — не чаще одного запуска в день для каждого cron, точность ±59 минут; Pro/Enterprise — раз в минуту; до 100 cron на проект. **Hobby не обеспечивает требуемый контракт.** Не заменять ежеминутный resume более редким. Корректные варианты: подтверждённый Pro/Enterprise либо отдельный согласованный внешний HTTP trigger каждую минуту, либо перенос на VPS. Новый [Schedules beta](https://vercel.com/docs/schedules/limits) на Hobby разрешает минимум 15 минут с 15-минутным jitter, поэтому также не обеспечивает ежеминутный resume. Внешний trigger сейчас не создан; ему потребуется штатный доступ через Deployment Protection и Bearer-авторизация. Не менять тариф аккаунта автоматически.

[Go runtime](https://vercel.com/docs/functions/runtimes/go) поддерживает Go Functions и выбор версии из `go.mod`; [duration](https://vercel.com/docs/functions/configuring-functions/duration) для выбранного Go server preset задан в настройке проекта. `functions` glob на `cmd/api/main.go` платформой не принимается; в `vercel.json` его нет. Здесь выбран консервативный предел 60 s; облачная применимость и фактический план должны быть подтверждены deployment. Наличие production URL само по себе не подтверждает обработку очереди.

## Удаление адаптера при переходе на VPS

Основная ветка `dev` не содержит этих изменений: для VPS использовать её штатные `cmd/collection` и `deploy/README.md`. Остановить Vercel Cron/внешний trigger перед запуском VPS; оставить один active runner. Перенести нужные секреты защищённым способом, использовать тот же direct PostgreSQL и существующие миграции/ledger — повторно собирать завершённые runs не нужно. Адаптер можно оставить только в `deploy/vercel` либо удалить `cmd/api/`, `internal/vercelapp/`, `configs/vercel_assets.go`, `vercel.json`, `.vercelignore` и эти инструкции; вернуть добавления к Makefile/test script. Не удалять PostgreSQL history или delivery ledger.
