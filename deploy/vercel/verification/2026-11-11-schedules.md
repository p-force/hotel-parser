# Миграция Cron Jobs → Schedules, 11.11.2026

Ветка `deploy/vercel`, PR #61, исходный HEAD `a591bd6`. Только проект `hotel-parser` команды `dianas-projects-a75fd820`; старый `arena-hotel` и другие ветки не изменяются. Пользователь подтвердил единственный collector и разрешил активацию после проверок. Ручные Schedule invocations и полный ручной live-сбор запрещены и не выполняются.

## Исходное состояние и безопасность

Read-only Vercel API и защищённые health/status подтвердили: Hobby, Fluid, Production Branch deploy/vercel, Function260s, сбор включён, CRON_SECRET присутствует. Один закрытый fixture-run, remaining=0, нет running/retryable backlog. Ранее был один legacy Cron `/api/start`, `0 5 * * *`; новых Schedules в production namespace default —0. Secrets/DSN/headers с авторизацией не публикуются. Проверка auth с реальным секретом не выполнялась вручную: локально CRON_SECRET отсутствует, Sensitive env из Vercel не извлекался.

## Авторизация и execution metadata

По [официальной схеме path targets](https://vercel.com/docs/schedules/handle-runs): GET указан явно (defaultPOST), Bearer берётся из CRON_SECRET текущего deployment. Исправлена совместимость: Cron minimum16 вместо прежних32; Trigger остаётся32. Пустые/короткие/некорректные secrets не авторизуют; query/body запрещены; collection_enabled=false блокирует service. Deployment Protection сохраняется.

Метаданные: `ce-specversion=1.0`, `ce-type=com.vercel.schedule.v1beta`, `ce-source=/schedule/default/{name}`, `ce-id`, `ce-vssscheduleid`, `ce-vssschedulename`, `ce-vssnamespace=default`, `ce-vssscheduledat` (ISO8601), `ce-vssschedulesource=static`. ID и размеры ограничены; имена соответствуют action. Парсинг — только после Cron Bearer, заголовки ручного Trigger-запроса не дают Schedule identity. Shared secret подтверждает доверенного вызывающего, не подписанное происхождение; фактический вызов доказывается также execution history платформы.

Логический slot=08:00МСК дня фактического start, scheduled_at=время выражения без jitter, actual_started_at=поступление HTTP. Это разные поля; задержка определяется как actual−scheduled. Raw headers/body/secret values не логируются.

## Queue reliability и изменения

Переиспользуются PlanRun, bounded RunGroups, session runner lock, source gates/retry, leases/fencing, CSV и delivery ledger. Добавлены только read-only progress fields и снимок до Runner. Схема/история не меняются.

Новые события `schedule.execution.started/completed`, `schedule.request.rejected`, `collection.progress`, `collection.no_progress`. Достоверные job statuses/ready/waiting_lease/waiting_backoff получаются SQL. Для start scope=конкретный слот; resume scope=незавершённые runs из before-snapshot. Сравнение remaining выполняется только для того же run/числа jobs; неизвестные значения не заменяются нулями.

Порог no_progress=30минут без подтверждённой активности attempts/создания jobs и без уменьшения remaining. Он работает после нового процесса без in-memory истории. Lease/backoff отмечаются отдельно; retry может быть активностью без уменьшения remaining. Нулевой/неизвестный progress не объявляется успехом сбора.

Исправлен подтверждённый дефект обработки ошибок: `errors.Is(err,runner_busy)` скрывал присоединённую PostgreSQL/deadline ошибку как409. Теперь409 только для чистого runner_busy; timeout/canceled/SQL/planning failures остаются503 с безопасными кодом/stage.

## Конфигурация

```json
"schedules": {
  "morning-start": {"schedule":"0 8 * * *","timezone":"Europe/Moscow","path":"/api/start","requestMethod":"GET"},
  "morning-resume": {"schedule":"*/15 8-11 * * *","timezone":"Europe/Moscow","path":"/api/resume","requestMethod":"GET"}
}
```

Root `crons` удалён; framework/buildCommand/fra1/Go version сохранены. Минимальный интервал15минут и timezone поддержаны [configuration](https://vercel.com/docs/schedules/configuration), [Hobby limits](https://vercel.com/docs/schedules/limits).17executions/день, включая один no-op, не обещают гарантированное завершение всех jobs.

08:00resume с проверенными metadata возвращает `initial_resume_skipped` без service/lock. Рабочие resume начинаются08:15, сохраняя09:00/10:00/11:00. Jitter до15минут всё ещё допускает overlap; это409 без потери jobs. Path Schedules не retry при409/5xx — следующий слот продолжает очередь. При провале единственного morning-start и отсутствии плана resume не создаёт слот автоматически; требуется диагностика/согласованный повтор.

## Проверки до активации

- make check: PASS (fmt/vet/unit/race/build), native API build PASS.
- make test-integration-docker: PASS, все четыре пакета на изолированных схемах, источники/доставка только mock.
- Новый PostgreSQL regression: два конкурентных start/resume получают busy, активный запрос отменяется, fixture lease истекает, новый service восстанавливает оба задания; before2→after0; ровно2jobs/2attempts/2offers/2delivery records, replay terminal без HTTP.
- Unit regressions: Cron16/missing/wrong/invalid; Trigger не подделывает identity; CloudEvent/time/delay; skipped08:00resume; query/body; joined busy+SQL/timeout; no_progress после рестарта/advance/change-set; backoff/lease/unknown; root config безcrons, два GET schedules.
- Existing PostgreSQL deadline/flush/recovery/stale fencing/CSV across resume/restart tests PASS.
- Фактические registration, deployment и execution history будут дополнены после разрешённого автоматического deployment; ручной invoke не используется.
