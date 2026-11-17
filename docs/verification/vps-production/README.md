# Подготовка VPS runtime — 17.11.2026

Ветка: `feature/vps-production`, base `dev` (`d1bd709`).
Сравнена `deploy/vercel` (`58d863d`), 28 отличающихся файлов.
Никакого развёртывания на VPS / изменения Vercel Production / live-сбора в этой проверке.

## Аудит переноса

| Изменение | Решение |
|---|---|
| `schedule/service.go`: `start`, plan-before-HTTP, фильтр слота, Before/ClaimedJobs/Stage | Перенесено; cron использует CLI `start` |
| `storage/leases.go`: RunStartedAt, NewestRunFirst | Перенесено; обычный resume сохраняет FIFO и fair claim |
| `storage/schedule_progress.go`: Ready / WaitingLease / WaitingBackoff / timestamps | Перенесено |
| Start, slot claim и concurrent start/resume/recovery tests | Перенесены; PostgreSQL + mock HTTP |
| `cmd/api`, `internal/vercelapp`, embedded assets, vercel.json, Schedules, платформенные build/config dependencies | Не перенесены |

Новое: CLI env-лимиты до создания клиентов; самостоятельный `compose.vps.yml`,
закрытый PostgreSQL 17, volumes, log rotation, явное opt-in cron, shutdown forwarding,
backup/restore. Автоматический экспорт пропускает delivered history; явный экспорт
любого run и восстановление pending CSV из PostgreSQL сохранены.

## Покрытие

| Инвариант | Проверка |
|---|---|
| 468 jobs/сутки, шесть отелей, повтор плана без дубликатов | `TestProductionRegistryDaily468Jobs`, `TestPostgresProductionRegistryDaily468Jobs` |
| Start публикует все планы до HTTP, claim только текущего слота | `TestPostgresStartPublishesAllGroupsBeforeHTTPAndSkipsOldQueue`, `TestPostgresStartPlanFailureDoesNotRunAnyHTTP` |
| Runner exclusion, отмена, восстановление, fencing | `TestPostgresScheduleConcurrentStartResumeCancelAndRecovery`, существующие runtime/lease/process tests |
| Retryable / terminal, persistent fairness | `TestPostgresLeaseBudgetAndRetryScheduling`, `TestPostgresScheduleStopRecoverAndPersistentFairness` |
| Готовые jobs / lease / backoff в диагностике | `TestPostgresRunProgressReadyLeaseBackoff` |
| CSV после рестарта, delivered archive без повторной генерации | `TestPostgresDeliveredArchiveSkippedAndExplicitlyRecoverable`, существующие CSV tests |
| Недоступность PostgreSQL, отсутствие source HTTP | `TestPostgresUnavailableFailsWithoutSourceTraffic` |
| Недоступность Диска / restart / независимость от lock | `TestPostgresAutomaticDiskDeliveryRecovery`, `TestPostgresDeliveryDoesNotHoldRunnerLock`, mixed-source regular delivery tests |
| Env-лимиты, fail-fast, opt-in cron, resume после plan failure | `cmd/collection/options_test.go`, `cron_test.go`; существующие collect limits/concurrency/HTTP-gate tests |

Результаты:

- `go test ./...` — PASS.
- `make check` (format/vet, `go test -race -count=1 ./...`, build) — PASS.
- PostgreSQL integration с race: `go test -race -p=1 -tags=integration ./internal/storage ./internal/collect ./internal/schedule -run '^TestPostgres' -count=1` — PASS.
- Добавленный затем progress test отдельно с race — PASS.
- Docker image build — PASS; Compose с dummy env: нет host ports, cron disabled,
  log rotation и named volumes присутствуют.
- Docker shutdown: изолированный Go mock с именем `collection` получил SIGTERM,
  записал flush marker; контейнер завершился с exit 0. Источники не вызывались.
- Backup: `deploy/backup.sh`, SHA256, pg_restore в новую локальную БД — PASS:
  контрольная строка восстановлена, все пять migrations на месте; volumes не удалялись.
- Финальный `make test-integration-docker` — PASS; `git diff --check` — PASS.

Первый Docker integration выявил старую фикстуру TravelLine: после усечения списка
категорий её общий фонд не соответствовал сумме. Исправлена только фикстура.
Первый дополнительный race-запуск пакетов параллельно конфликтовал за общий
DB advisory lock; integration-пакеты запускаются последовательно (`-p=1`), как в
штатном Docker script. В новой progress-фикстуре исправлено истечение lease только
claimed job вместо нарушения constraint queued job; production constraint не менялся.

## Границы приёмки

Инструкция: [deploy/VPS.md](../../../deploy/VPS.md). Реальные RUVDS/сеть/ресурсы ещё
не проверены. Regular collection не активирован. Перед handover оператор выключает
действующий Vercel collector, чтобы разные БД не создавали независимые live-сборы.
Retentions / удаление history не вводились: нужны мониторинг диска и внешние backups.
