# Integration stability — 05.11.2026

Проверка в существующей ветке `feat/travelline-competitors`, PR #54. Использованы локальная Docker PostgreSQL и mock HTTP; реальных запросов к отелям/Диску не выполнялось.

| Полный прогон | Код | Результат | Подробности |
|---|---|---|---|
| 1 | до исправления | PASS | packages=3/3; tests passed=55; ошибок нет |
| 2 | до исправления | FAIL | packages=2/3; tests passed=54; TestPostgresAutomaticCSVAcrossResumeAndRestart (0.75s) |
| 3 | после исправления | PASS | packages=3/3; tests passed=56; ошибок нет |
| 4 | после исправления | PASS | packages=3/3; tests passed=56; ошибок нет |
| 5 | после исправления | PASS | packages=3/3; tests passed=56; ошибок нет |

После исправлений — **три последовательных полных PASS**. Каждый запуск заново собирает и запускает все PostgreSQL-тесты storage, collect и schedule в изолированных схемах. Новая regression увеличила число top-level тестов с 55 до 56.

## CSV resume/restart: подтверждённая причина

Во втором полном запуске `TestPostgresAutomaticCSVAcrossResumeAndRestart` упал за 0.75 с. Сообщение `unfinished run exported` было общим текстом assertion; фактическая ошибка — `check runner lock failed`, четыре jobs остались running и ни одно не завершилось. CSV ещё не экспортировался. Это не таймаут записи файла и не особенность TLS mock server.

`RunnerLock.Check` вызывался несколькими workers на одном pinned `sql.Conn`. `database/sql` сериализует отдельные driver-вызовы, но не всю последовательность QueryRowContext → Scan/закрытие rows. Другой Check мог открыть query до завершения предыдущего Scan. Пересечение работы с состоянием pgx приводило к SQL-ошибкам, а в направленной regression (8 goroutines × 100 Check) — к panic `index out of range` в `pgx/v5/stdlib.(*Rows).Next`. Увеличение нагрузки лишь повышает вероятность неблагоприятного порядка goroutines; зависимость от конкретной внешней нагрузки не требуется.

Минимальный fix в `internal/storage/runner_lock.go`: общий mutex на весь Check query/Scan и Close. Guard использует то же pinned соединение и по-прежнему отклоняет потерянный lock; reconnect/повторный acquire не добавлялись. Под mutex нет HTTP, парсинга или записи observation. CSV-тест сохранён с четырьмя workers и прежними assertions, лимиты и таймауты не увеличены. Regression `TestPostgresRunnerLockConcurrentChecks` должна выполнить 800 успешных проверок одного действующего lock.

## Прежний Bnovo warm-up timeout: подтверждённый механизм

Limiter намеренно сохраняет последнюю отметку старта origin между ConfigureLimits. В старом тесте часовой интервал включался **перед** warm-up, при предположении, что адрес только что созданного httptest server не имеет истории. Это предположение неверно при повторном использовании origin/порта в общем процессе; отдельный запуск имеет другую историю.

Воспроизведение сделано без случайного ожидания переиспользования порта: предварительный успешный mock-запрос создаёт историю того же origin, затем прежний порядок warm-up стабильно получает timeout через 20 с. Исправлена только подготовка теста: seed history → warm-up с нулевым интервалом → включение часового интервала → queued runtime с исходным deadline 200 мс. После deadline проверяются сохранность job и lease/fencing recovery прежним способом. Счётчик теперь учитывает seed/warm-up/recovery (2/3 HTTP), а не скрывает seed сбросом counter. Production-код Bnovo/TravelLine и политика хранения rate-limit history не менялись.

## Проверки

- Два воспроизводящих случая зафиксированы до исправления; безопасные выдержки: [stability-evidence-2026-11-05.txt](stability-evidence-2026-11-05.txt).
- После исправления направленные PostgreSQL regressions runner lock и warm-up — PASS.
- `make check` — PASS (fmt, vet, unit/race, build).
- `git diff --check` — PASS.
- PostgreSQL stress с race detector и `-count=10` — **PASS** в storage, collect и schedule: 10 × 800 конкурентных Check, 10 deadline/recovery и 10 CSV resume/restart. Без data races и panic.

Машиночитаемые результаты полных прогонов и SHA-256 логов: [integration-stability-2026-11-05.json](integration-stability-2026-11-05.json). Прежние FAIL сохранены в истории отчёта; они не заменяются одиночным успешным повтором. Данные полного TravelLine-сбора 03.11 и его CSV не менялись. Merge не выполнялся.
