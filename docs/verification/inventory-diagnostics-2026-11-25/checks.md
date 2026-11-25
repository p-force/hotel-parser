# Проверки

25.11.2026, локально. Production/Neon не использовались; PostgreSQL 17 в существующем
Docker, тесты создавали и удаляли только случайные изолированные `hp_test_*` схемы.
HTTP-запросов к Bnovo/TravelLine и загрузок на Яндекс Диск не было. Официальные сайты
использовались только для чтения опубликованных материалов физического фонда.

| Команда | Результат |
| --- | --- |
| `go test ./...` | PASS |
| `go test -race -count=1 ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `make test-integration-docker` | PASS; финальный полный прогон — 85 top-level PostgreSQL-тестов |
| Целевые новые PostgreSQL-тесты с `-race`, `-p 1`, `-count=1` | PASS (storage + collect) |
| Повтор unit/race изменённых collect/storage/schedule/collection после self-review | PASS |
| `gofmt -l` изменённых Go-файлов | Пустой вывод |
| `git diff --check` | PASS |

Первый полный PostgreSQL-прогон также прошёл. После дополнительных regression-тестов
выполнен финальный полный прогон. После исключения lease credentials из JSON отдельно
повторены затронутые unit/race и целевые PostgreSQL race-тесты; vet/build также повторены.

Целевой PostgreSQL набор (локальный DSN задаётся через окружение, здесь не публикуется):

```sh
go test -race -tags=integration -count=1 -p 1 \
  ./internal/storage ./internal/collect \
  -run '^TestPostgres(AvailabilityDiagnosticsRecovery|DiagnosticsUnknownConflictAndMissingFacts|RuntimeCapturesDiagnosticPolicy|SelectionPolicyUsesAttemptFencing)$'
```

## Новые проверки

- Bnovo и TravelLine: отсутствие на LOS=1 и предложение LOS=2/3 дают диагностическое
  наблюдение, а не minimum_stay; сохранены ссылки на обе попытки и конкретный тариф.
- Отсутствующая/изменившаяся политика, другой source/origin/property/run, даты,
  guests/rooms/children/language, неподходящий вариант и исключённый тариф не сравниваются.
- Partial с допустимыми warning не повышает достоверность количества. Неизвестная
  диагностика и конфликты блокируют вывод о различиях LOS.
- Hidden/disabled, ошибочная/нулевая цена, явная нулевая квота не подтверждают длинный
  сценарий. Bnovo использует существующее MatchesAdults, включая RowAvailable.
- Нулевой остаток/explicit unavailability и warning с названием бизнес-причины не
  классифицируются как sold_out/minimum_stay/sales_closed/no_rate.
- Известные технические коды подтверждаются только на уровне сценария; source_error
  остаётся unknown. Сохраняются category/attempt предупреждения и context.
- No offers, missing observation, missing attempt, NULL/false/0 и конфликт evidence.
- Неизменность входных структур; lease owner/token не выводятся в JSON.
- Незавершённый run не выдаёт отчёт; отменённое чтение не выдаёт JSON.
- Повтор SaveAttempt не создаёт дубликатов; другая политика даёт ErrConflict.
- Политика сохраняется в штатном RunQueued; stale fencing token не может её записать.
- UPDATE/DELETE/TRUNCATE политики запрещены; независимый Store после рестарта выдаёт
  тот же JSON без сборщика/HTTP.
- Существующие integration-тесты продолжают проверять CSV v3 (ровно 35 колонок),
  legacy upgrade/replay, inventory snapshots, LOS, recovery и delivery.

## Ограничения результата

Нет live-подтверждения новых restriction-кодов; классификатор намеренно не распознаёт
их по свободному тексту. Отсутствие подтверждённых бизнес-причин — ограничение
доступных evidence, а не подтверждение отсутствия ограничений у отеля.

JSON формируется по запросу; он не включён в автоматическую доставку CSV. Политика
новых попыток требует миграции 0007. Старые попытки не backfill: сравнение LOS для них
не подтверждается сегодняшним allowlist. Исторические inventory snapshots не изменены.
