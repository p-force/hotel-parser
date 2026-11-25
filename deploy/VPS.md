# RUVDS / Debian 13

Подготовленный runtime, **не активированное развёртывание**. Используется тот же
`cmd/collection → queue → RunQueued → SaveAttempt → CSV → Yandex Disk`.
Ни HTTP endpoint, ни Vercel для запуска не нужны. Production Vercel не меняется.

## Установка без включения сбора

1. На Debian 13 установите Docker Engine и Compose plugin по
   [официальной инструкции Docker](https://docs.docker.com/engine/install/debian/).
   Пакеты: `docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin`.
   Проверьте `docker version`, `docker compose version`. Требуются Git, CA certificates,
   синхронизация часов (NTP), исходящий HTTPS/DNS к источникам и Яндекс Диску.
   Host cron не нужен: Alpine crond с tzdata запускается внутри collector.
2. Клонируйте репозиторий в `/opt/hotel-parser`, используйте проверенный SHA после merge.
   Файл `compose.vps.yml` **самостоятельный**: не объединять с `docker-compose.yml`.
   VPS stack имеет отдельное имя `hotel-parser-vps` и постоянные named volumes.
3. Скопируйте `deploy/collection.env.example` в `.env.collection`, `chmod 600`.
   Задайте новый пароль, соответствующий URL-encoded DSN (host `postgres`), OAuth и
   `YANDEX_DISK_DIR`. DSN direct/session, без transaction pooler.
   `HOTEL_PARSER_COLLECTION_ENABLED=false` оставить до отдельного разрешения.
   `.env.collection` исключён из Git и Docker build context. Не печатать `compose config`
   с реальными credentials. Изменение env не меняет пароль уже созданного volume.

```sh
cd /opt/hotel-parser
docker compose --env-file .env.collection -f compose.vps.yml build collector
docker compose --env-file .env.collection -f compose.vps.yml up -d postgres
docker compose --env-file .env.collection -f compose.vps.yml run --rm --entrypoint /app/collection collector migrate --execution-timeout=60s
```

`migrate` — только штатные versioned migrations; существующие volumes не удалять.
До миграции существующей БД сделать backup. PostgreSQL 17 не публикует **никаких**
портов host: 5432 доступен только внутри сети Compose. На VPS снаружи нужен только
SSH с ограниченным доступом. Не поднимать HTTP `serve`, reverse proxy не требуется.
Docker logs обоих сервисов: `json-file`, 10 MB × 5. CSV cache — постоянный `csv_cache`.
PostgreSQL остаётся источником истины. Следить за свободным местом и размером history:
эта задача не вводит retention и не удаляет старые наблюдения.

## Лимиты и расписание

CLI читает env до создания клиентов/Runner; явные CLI-флаги имеют приоритет.
Недопустимые значения отклоняются до подключения к БД. Глобальный допуск равен
`--workers` / `HOTEL_PARSER_WORKERS`, а не только размеру локального pool.

| Переменная | VPS |
|---|---:|
| `HOTEL_PARSER_WORKERS` | 4 |
| `HOTEL_PARSER_PROPERTY_CONCURRENCY` | 2 |
| `HOTEL_PARSER_TRAVELLINE_CONCURRENCY` | 2 |
| `HOTEL_PARSER_TRAVELLINE_START_INTERVAL` | 1s |
| `HOTEL_PARSER_BNOVO_CONCURRENCY` | 2 |
| `HOTEL_PARSER_BNOVO_START_INTERVAL` | 500ms |
| `HOTEL_PARSER_MAX_JOBS` | 64 |
| `HOTEL_PARSER_EXECUTION_TIMEOUT` | 45s |
| `HOTEL_PARSER_SHUTDOWN_GRACE_PERIOD` | 5s |

Env template задаёт эти значения; без env сохраняются прежние defaults CLI.
Допуски/интервалы действуют на весь процесс, в том числе retry и фактические HTTP
старты; все отели используют общую очередь. Прямые вызовы адаптера вне runtime
и независимые БД не делят process-wide лимиты.

Europe/Moscow: **08:00 / 12:00 / 16:00 / 20:00**, соответственно 61/8/8/1 job на
включённый отель. Шесть отелей × 78 = **468 jobs/сутки** без retry. Один взрослый,
один номер, LOS=1. Lindsay выключен. Четыре cron trigger вызывают `start`: сначала
публикуются все планы слота, затем claim только его jobs. Каждую минуту `plan --due`
восстанавливает пропущенные слоты текущего дня, затем `resume` продолжает всю очередь.
Старые задания не теряются; исторические snapshots за дни простоя не выдумываются.

Пересечения допустимы: session advisory lock в PostgreSQL разрешает только один
Runner на БД. Busy invocation не запускает HTTP, следующая минута повторит resume.
Fair claim использует persisted history отелей. Retryable jobs ждут `next_retry_at`,
terminal jobs не выполняются повторно. Lease renewal/fencing защищает от старого
worker; после остановки незавершённые jobs восстанавливаются по истечении lease.
При недоступности БД invocation завершится ошибкой, новый cron повторит операцию.

Для shutdown entrypoint останавливает crond, запрещает следующие команды и явно
посылает SIGTERM активным Go `collection` (cron jobs имеют отдельные sessions).
Go отменяет HTTP/backoff, делает bounded flush. Внешний Docker grace — 75 секунд,
ожидание entrypoint — до 60 секунд. Если grace опции увеличены, согласовать эти
границы; SIGKILL не гарантирует flush, но lease обеспечивает последующее восстановление.

**Включать только после отдельного handover:** действующий Vercel collector сначала
выключает оператор. VPS и Vercel с разными БД не защищаются общим advisory lock.
После разрешения сменить `HOTEL_PARSER_COLLECTION_ENABLED=true` и выполнить:

```sh
docker compose --env-file .env.collection -f compose.vps.yml --profile collection up -d collector
```

Эта команда в рамках подготовки не выполнялась. Для паузы: `stop collector`;
для изменения env: `up -d --force-recreate collector`. Не использовать `down -v`.

## CSV / delivery

Готовый файл кешируется, существующий CSV не переписывается при resume.
Уже `delivered` run пропускается автоматическим локальным экспортом, даже если
его cache удалён: частые resume не восстанавливают весь доставленный архив.
Недоставленные файлы восстанавливаются из committed PostgreSQL при исчезновении
cache; сбой записи повторяется в следующем вызове и не меняет collection status.
Для явного восстановления любого архивного run остаются существующие Go API
`report.Generate(ctx, store, groupID, directory, runID)` и `store.WriteRunCSV(...)`:
оба работают без HTTP к отелям, включая delivered run.

Uploader сам воспроизводит CSV из БД, не требует постоянного локального диска.
Путь `/root/<group_id>/<property_id>/<YYYY-MM-DD_HH-mm>.v3.csv` использует логический
слот МСК. Старые remote_path не меняются. Доставка имеет отдельные lease/retry
в PostgreSQL; API outage/timeout не меняет результат сбора. После успешного PUT и
потери commit проверяются размер/checksum существующего файла; overwrite запрещён.

## Backup / restore

Регулярно копировать зашифрованные backups **за пределы VPS**, проверять restore.
Локальная копия на том же volume не защищает от потери сервера. Установка расписания
backup и политика хранения — действия оператора, автоматически ничего не удаляется.

```sh
sudo install -d -m 700 /var/backups/hotel-parser
# Из корня репозитория; pg_dump читает credentials внутри postgres, не печатает их.
sh deploy/backup.sh
```

Скрипт создаёт custom-format consistent snapshot всех схем БД, проверяет чтение
архива, атомарно публикует `.dump` и `.sha256`. Роли не входят в dump: владелец и
credentials создаются отдельно через защищённый env. Проверка восстановления
**только в новую пустую БД**, без DROP / `--clean`:

```sh
# Указать конкретный backup; проверить его checksum.
sha256sum -c /var/backups/hotel-parser/FILE.dump.sha256
docker compose --env-file .env.collection -f compose.vps.yml exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" createdb -U "$POSTGRES_USER" -T template0 hotel_parser_restore_check'
docker compose --env-file .env.collection -f compose.vps.yml exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" pg_restore -U "$POSTGRES_USER" -d hotel_parser_restore_check --exit-on-error --no-owner' < /var/backups/hotel-parser/FILE.dump
docker compose --env-file .env.collection -f compose.vps.yml exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -U "$POSTGRES_USER" -d hotel_parser_restore_check -c "SELECT count(*) FROM hotel_parser_arena33.scenario_jobs"'
```

Для аварийного переключения: остановить collector, восстановить в новую БД,
проверить counts/run progress/delivery ledger, изменить DSN защищённо, выполнить
штатный migrate, возобновить после разрешения. Сохранить старую БД и volume.
Не держать одновременно два collector с разными копиями восстановленной очереди.
Cache CSV восстанавливаем из PostgreSQL; OAuth/прочие секреты — отдельный защищённый backup.

## Контроль

```sh
docker compose --env-file .env.collection -f compose.vps.yml ps
docker compose --env-file .env.collection -f compose.vps.yml logs --tail=100 collector
docker compose --env-file .env.collection -f compose.vps.yml exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -X -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v schema=hotel_parser_arena33' < deploy/status.sql
```

Проверять missing slots, истёкшие leases, backlog, failed jobs, delivery state /
`next_attempt_at`. `partial`/`unknown` — диагностируемая неопределённость источника,
не подтверждённый ноль. Ошибки доставки не означают провал сохранённого сбора.
