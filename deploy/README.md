# Регулярный сбор на Docker/VPS

Это конфигурация запуска, не развёрнутый production. Нужны согласованный сервер,
постоянный PostgreSQL volume, доступ к источникам и OAuth Яндекс Диска. Все активные
отели берутся из `configs/groups.json`/`collection.enabled`; Lindsay не включается.
Сценарий регулярного planner: 1 взрослый, 1 номер, LOS=1, ru/RUB, без детей.

## Окружение и первый запуск

1. Скопируйте `deploy/collection.env.example` в **`.env.collection`**, задайте
   собственный пароль PostgreSQL, соответствующий URL-encoded DSN и OAuth-токен.
   Файл игнорируется Git/Docker build context. `chmod 600 .env.collection`.
   DSN — direct/session connection, host `postgres` внутри Compose. Не используйте
   transaction pooling: advisory lock требует постоянной сессии.
2. Изменение POSTGRES_PASSWORD не меняет пароль существующей БД. Для уже созданного
   volume используйте его реальные credentials; не удаляйте volume ради запуска.
3. Соберите и мигрируйте. Эти команды **не содержат значений секретов**:

```bash
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml build collector
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml up -d postgres
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml run --rm --entrypoint /app/collection collector migrate
# Только после проверки окружения и разрешения регулярного live-сбора:
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml up -d collector
```

Переменные: `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`,
`HOTEL_PARSER_DATABASE_URL`, `YANDEX_DISK_TOKEN`, `YANDEX_DISK_DIR=/hotel-parser`,
`TZ=Europe/Moscow`, `HOTEL_PARSER_LOG_FORMAT=json`, `HOTEL_PARSER_LOG_LEVEL=info`.
Опционально `HOTEL_PARSER_CSV_DIR` — локальный cache, а не источник истины.
`COLLECTION_GROUPS`/`COLLECTION_BIN` позволяют переиспользовать cron-скрипт на VPS;
по умолчанию `/app/configs/groups.json` и `/app/collection`. HTTP `serve` не нужен
для локального cron; при его использовании отдельно задайте trigger secret и TLS.

Не запускайте `docker compose config` с реальными env в публичный лог:
рендер конфигурации раскрывает environment. Не используйте `set -x`.
PostgreSQL в основном локальном compose опубликован на host port; на реальном
VPS ограничьте доступ firewall/loopback либо уберите публикацию порта. Перенос,
backups и эксплуатационная защита PostgreSQL требуют отдельной настройки сервера.

## Расписание и восстановление

Cron-процесс работает в **Europe/Moscow** (`TZ`), четыре `tick`: 08:00/12:00/16:00/20:00,
горизонты 61/8/8/1 job на отель. Каждую минуту скрипт выполняет:

1. `collection plan --due --execution-timeout=45s`: публикует все наступившие слоты
   текущего московского дня через те же BuildPlans/PlanRun. UUID/hash детерминированы;
   повтор trigger или частичное планирование групп после сбоя безопасны.
2. `collection resume`: продолжает всю готовую очередь, включая старые runs,
   execution=45s, grace=5s, максимум 64 claims, общий pool=8. Один process runner
   на PostgreSQL database обеспечивается существующим session advisory lock.

Пересечение минутного запуска с tick допустимо: конкурирующий runner получает
busy, не запускает второй collection pool; следующая минута продолжит работу.
Вывод/ошибки cron направлены в stdout/stderr контейнера. Не добавляйте второй
сборщик в обход этого service. Несколько серверов не делят in-memory limiter:
нужны общая PostgreSQL и этот же guard; источники отдельно не запускаются.

При execution deadline успешно committed attempts остаются в БД, незавершённые
jobs восстанавливаются по lease с fencing. Просроченный владелец не может записать
результат. Следующий свежий вызов service завершает runs/CSV/delivery. Короткий
`plan --due` также выполняет штатный progress/export/delivery без HTTP к отелям:
это даёт отчётам время даже при непрерывном collection backlog. Шаг доставки ограничен 40 с и общим execution deadline. Минутный script
может пересечься со следующим: lock и ledger сохраняют безопасность. После исчерпания
его бюджета следующий минутный вызов повторяет идемпотентную проверку.

**Граница catch-up:** автоматически восстанавливаются только слоты **сегодня**.
После многодневного простоя нельзя достоверно восстановить вчерашний snapshot
нынешним HTTP. Этот пробел истории отмечается и разбирается оператором;
`--due` не выдаёт новый сбор за утраченное историческое наблюдение. Уже созданные
старые jobs не удаляются и обрабатываются обычным resume. Перезапуск не теряет
очередь: PostgreSQL volume сохраняется. `down -v` для восстановления запрещён.

Для host cron установите TZ/CRON_TZ согласно реализации своего cron, передайте
защищённое environment процессу (без значений секретов в crontab), используйте те же
`deploy/collection-cron.sh tick 08|12|16|20` и `minute`. Не предполагается наличие
фоновых goroutines после завершения процесса. Для пропущенных предыдущих дней
автоматического восстановления исторических цен нет.

## CSV и Диск

Run становится terminal только после всех jobs. CSV содержит существующие 35 колонок,
категории без offers и NULL. Источник — committed PostgreSQL, локальный файл можно
удалить и восстановить следующим вызовом без HTTP к отелям. Атомарная публикация
и детерминированный run ID исключают частичный готовый файл.

Путь Диска: `/hotel-parser/<group_id>/<property_id>/<YYYY-MM-DD_HH-mm>.csv`,
время — логический слот МСК. Для теста задайте отдельный root, не production-каталог.
Старые remote_path не меняются. Ledger в PostgreSQL хранит lease, retry time,
state, error code; повтор delivered не делает PUT. После потери локального диска
uploader сам строит CSV из PostgreSQL. Ошибка доставки не меняет collection status.
Потеря commit после успешного PUT разрешается проверкой размера/checksum существующего
файла, без overwrite; разные bytes по занятому пути не перезаписываются.

## Контроль

```bash
docker compose --env-file .env.collection -f docker-compose.yml -f compose.collection.yml logs --tail=100 collector
# Credentials берутся из окружения уже работающего PostgreSQL; значения не печатаются.
docker compose exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -X -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v schema=hotel_parser_arena33' < deploy/status.sql
```

SQL проверяется отдельно для каждой configured schema. Список `expected` содержит
текущие шесть активных отелей; при изменении registry обновите его.
`missing_current_slot=true` после небольшого допуска cron — нет нового плана;
`backlog_without_recent_claim=true` — есть очередь без claim более пяти минут:
проверьте контейнер cron, соединение, runner busy и leases. `expired_leases` показывает
восстановимые jobs. Смотрите также старые недоставленные runs во втором запросе,
`retryable_failed`, `error_code`, `next_attempt_at`, OAuth и сетевой доступ.

`csv_state=reconstructable_from_postgres` означает готовность восстановить CSV,
**не проверку наличия локального cache-файла**. Delivery `delivered` — committed
подтверждение uploader; неизвестные остатки `unknown` не являются ошибкой парсера.
Дальнейшая production-приёмка: сервер/доступы, backup/restore, защищённый network,
мониторинг этих признаков, проверка первого реального расписания и ротация OAuth.
