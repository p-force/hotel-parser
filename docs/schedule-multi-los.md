# Регулярный LOS=1/2/3

Для шести активных отелей collection.nights=[1,2,3]. Остальные поля конфигов,
Lindsay (выключен), groups.json, расписание, concurrency/rate/retry не меняются.
Legacy-конфиг [1] продолжает работать. В registry los=0/отсутствие использует весь
список; явный los=1/2/3 ограничивает до одного разрешённого значения. Противоречие
с collection.nights отвергается. los2_evidence необязателен при los=2; для другого
los непарное evidence по-прежнему отвергается. Evidence не классифицирует причины.

| Слот Europe/Moscow | LOS=1 | LOS=2 | LOS=3 | На отель | Шесть отелей |
|---|---:|---:|---:|---:|---:|
| 08:00 | 61 | 8 | 8 | 77 | 462 |
| 12:00 | 8 | 8 | 8 | 24 | 144 |
| 16:00 | 8 | 8 | 8 | 24 | 144 |
| 20:00 | 1 | 1 | 1 | 3 | 18 |
| Всего | 78 | 25 | 25 | 128 | 768 |

LOS=1 использует прежние горизонты 60/7/7/0 дней; LOS=2/3 — min(горизонт,7),
включая сегодня. Константа `extendedLOSDaysAhead=7` — явная точка расширения.
Nights сортируются в копии, конфиг не меняется. CheckIn — календарная полночь в
timezone отеля; CheckOut=CheckIn.AddDate(0,0,LOS). PeriodEnd покрывает максимальный
check_out: обычно сегодня +61 на 08:00, +10 в 12/16, +3 в 20. Если LOS=1 выключен,
утренний run не получает искусственного 60-дневного period_end. DST проверяется
календарными датами, не умножением часов на число ночей.

## Переход без изменения старых планов

UUID слота остаётся прежним: второй run на тот же group/property/slot не создаётся.
Сначала service вызывает обычный PlanRun с новым набором. Только при ErrConflict
проверяется **точный** прежний регулярный кандидат LOS=1 с прежними горизонтом,
датами, источником и параметрами. Он должен пройти тот же PlanRun/hash. Старый
план тогда возвращается неизменным: никаких добавлений LOS=2/3 задним числом.
Новые слоты получают collection.nights при первой публикации. 768 jobs — для
полностью нового дня; день перехода может содержать меньше jobs. Например, уже
опубликованное утро LOS=1 и новые остальные слоты дадут 672 jobs на шесть отелей.
Отдельного времени
cutover нет: граница — уже опубликованный план, а не часы процесса.

Hash ledger и storage/plans.go не менялись. Новые multi-LOS-планы также неизменяемы:
смена nights после публикации или смена source не превращаются в молчаливый replan.
При необходимости добавить пропущенный LOS уже запланированному слоту требуется
отдельное решение; обычный resume выполняет только исходные jobs.

В job identity уже включены даты/LOS, поэтому одинаковый check-in с разными LOS
даёт независимые jobs, attempts, observations и offers. Run завершается только
после всех своих jobs. Незавершённые jobs восстанавливаются по lease/fencing.
Повтор resume не выполняет завершённые jobs и не повторяет доставленные CSV.

## Нагрузка

Jobs: 468 → 768/сутки, **+300 / +64,1%**, без retry. Никакие лимиты не повышены:
VPS workers=4, property=2, Bnovo=2/500ms, TravelLine=2/1s, MaxJobs=64.
Теоретический минимум при полных пачках за сутки: ceil(768/64)=12 invocations.
Для отдельных слотов без переноса backlog: минимум 8/3/3/1; в реальности execution
budget, RTT, rate limiting и retry увеличивают число вызовов. Минутный cron resume
продолжает очередь штатно; тест 77 jobs подтверждает 64+13 после restart.

Без ошибок Bnovo делает один HTTP на сценарий, TravelLine — hotel_info + availability.
При одном Bnovo и пяти TravelLine это около **1408 HTTP/сутки** вместо 858 (+64,1%).
Это расчёт, не live-замер. Только source interval=1s даёт для 1280 TravelLine HTTP
около 21 минуты минимального суммарного времени стартов в сутки; latency/source
concurrency, разрывы invocation и retry увеличивают его. В утреннем слоте 770 TL HTTP
требуют примерно 13 минут стартов даже без сетевых задержек. SLA здесь не утверждается.

## Данные и ограничения

CSV v3 остаётся ровно 35 колонок, parser/normalization/миграция 0006 не меняются.
Цены — за весь период, не автоматически за ночь; квоты LOS не складываются.
Случай «LOS=1 отсутствует, LOS=2/3 есть» не доказывает minimum stay. Bnovo может
подтвердить точную недоступность, TravelLine при пустых room_stays остаётся unknown.
Partial одного LOS не изменяет результат другого; все исходные статусы/diagnostics
сохраняются. Доставка использует прежний v3 ledger/version recovery без изменений.

Проверки выполняются на локальном PostgreSQL в изолированных схемах и mock HTTP.
Реальных запросов к отелям, deployment, merge и включения production не выполнялось.

## Результаты проверок — 25.11.2026

- go fmt, go vet, go test, go test -race и go build ./... — PASS.
- make test-integration-docker — PASS, 81 PostgreSQL integration-тест.
- Новые MultiLOS integration-тесты с race и итоговые unit-тесты schedule с race — PASS.
- git diff --check — PASS.

Подтверждены: 77/24/24/3 на отель, 768 на шесть; максимальный check_out, DST,
уникальность и стабильность nights order; legacy plan replay и отклонение
изменённого нового плана/источника; два настоящих адаптера через mock transport,
LOS-specific статусы/цены/квоты; cancel → expired lease → новый service; MaxJobs=1
и 64; завершение run/CSV только после всех jobs; повторный resume без HTTP/PUT.

Старые tests с сохранёнными однодневными HTTP-ответами получили явный nights=[1],
не новые фиктивные ответы для LOS=2/3. Их прежние assertions сохранены; отдельные
MultiLOS mocks проверяют новые периоды. Ошибка компактной HTML-фикстуры исправлена
в тесте (пробел в существующем JS-маркере), production-парсер не менялся.

PostgreSQL-пакеты проверялись последовательно: общий DB advisory lock намеренно
исключает параллельные Runner даже в разных тестовых схемах. Ранний одновременный
запуск двух integration-команд дал тестовую коллизию; финальный полный прогон
выполнен отдельно и успешен.

Изменённые файлы:

```text
CHANGELOG.md
README.md
configs/aerostar.json
configs/arena33.json
configs/bega.json
configs/monarch.json
configs/petroff.json
configs/sovietsky.json
deploy/README.md
deploy/VPS.md
docs/schedule-multi-los.md
internal/schedule/csv_integration_test.go
internal/schedule/due_test.go
internal/schedule/multi_los_integration_test.go
internal/schedule/multi_los_test.go
internal/schedule/planner.go
internal/schedule/planner_test.go
internal/schedule/registry.go
internal/schedule/regular_delivery_integration_test.go
internal/schedule/service.go
internal/schedule/travelline_integration_test.go
internal/schedule/travelline_regression_integration_test.go
internal/schedule/vps_integration_test.go
```

Конфиги сравнены с исходными: меняется только collection.nights у шести
включённых отелей. Числа фонда/квот, source, rates, enabled и Lindsay сохранены.
Storage PlanRun, hash, миграция 0006, CSV v3, парсеры, stock algorithms и лимиты
не изменены. Commit/PR/deployment/merge не выполнялись.
