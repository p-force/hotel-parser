# Bnovo E2E revalidation — 31.10.2026

**CONDITIONAL GO для регулярного сбора публичных цен/остатков Arena33 и Lindsay
через `schedule.Service → RunQueued`, с одним активным runner и существующими
лимитами.** Исправления #36/#37 подтверждены. Безусловный GO для всех API и
горизонтального масштабирования не заявляется: старый `CollectPropertiesStored`
имеет отдельный дефект [#41](https://github.com/p-force/hotel-parser/issues/41),
распределённые лимиты остаются в #33. Рабочая конфигурация Lindsay всё ещё disabled;
её включение для регулярного сбора требует отдельного решения владельца.

[Первоначальный отчёт](../2026-10-31-bnovo-e2e/report.md) сохранён без изменений,
вместе с исходными артефактами и их проверенными SHA-256. Draft PR #38 не мержился
и не перезаписывался. Issue #35 этим отчётом автоматически не закрывается.

## 1. Исходный код и условия

- Production-код: `dev`, **`4b8a92444553401d4c640a443d5f46cafd5a8ccf`**.
  Проверены merged PR #39 (#36) и #40 (#37). Ветка доказательств —
  `test/35-bnovo-e2e-revalidation`; изменения только в test/diagnostic harness и документации.
- Live smoke: 31.10.2026 **10:55:40–10:55:43 UTC**; полный прогон:
  **10:56:27.096078468–10:57:51.255457465 UTC** (13:56:27–13:57:51 МСК).
- Реальные BaseURL/UID взяты из существующих Arena33/Lindsay конфигураций.
  Заезды полного плана — 31.10–30.12.2026, последний выезд — 31.12.2026.
  Adults=1, rooms=1, LOS=1, ru; утром 61 job на каждый отель.
- Go **1.26.0 darwin/arm64**; тестовые binaries — Linux/arm64, CGO_ENABLED=0.
  PostgreSQL **17.10**, существующий Docker container проекта;
  только случайные схемы `hp_test_*`. Production Neon, старые/пользовательские
  схемы и конфигурационные файлы не изменялись. Тестовые схемы штатно удалены
  после сохранения SQL evidence.
- Global cap=8, property cap=4, source cap=8, source interval=500 мс;
  singleton guard включён. TLS проверяется по доверенному PEM, без proxy/IP rotation.
- Только тестовая копия Registry включает Lindsay. Схемы Registry заменены на
  изолированные тестовые; source/scenario/worker/lease/persistence реализации не заменены.
- В live full для управляемого recovery lease=5 с, renewal=1 с; production defaults
  не менялись. До 3 completed HTTP attempts на job, реально retry не потребовались.
  Smoke использовал MaxAttempts=1. Эти условия соответствуют первому эксперименту.
- Сырые HTML хранятся приватно вне checkout, не включены в PR. В отчёте — hashes,
  публичные признаки и обезличенная диагностика; DSN, cookies, платёжные ссылки отсутствуют.

## 2. Smoke перед полным сбором

| Объект | Jobs/HTTP 200 | Status | Observations | Offers |
|---|---:|---|---:|---:|
| Arena33 | 1/1 | success | 3 | 28 |
| Lindsay | 1/1 | success | 10 | 28 |
| Всего | 2/2 | 2 success | 13 | 56 |

Даты 21–22.12.2026, adults=1. Сохранён 21 variant. Все остатки known, предупреждений
и расхождений нет. Wall time **2,918 с**, peak HTTP=2, gap=**506,847251 мс**.
Planner → queue → claim → real HTTP → parser → stock → PostgreSQL → run completion
пройден для обоих объектов. Только после PASS запущен full.

Smoke использует будущий логический вечерний slot 21.12 для одного сценария на отель.
По текущей модели run timestamps привязаны к slot, поэтому у двух smoke runs
`finished_at` в будущем. Это не время завершения HTTP и не измерение длительности.
В полном утреннем прогоне slot уже наступил; future run/attempt timestamps — 0.
Подробности: [smoke.json](smoke.json).

## 3. Полный реальный прогон и сравнение с baseline

| Метрика | Первый прогон | Повторный прогон |
|---|---:|---:|
| Завершённые jobs | 122/122 | **122/122** |
| Success / partial / no_offers / failed | 120 / 2 / 0 / 0 | **120 / 0 / 2 / 0** |
| Фактические RoundTrip starts | 125 | **124** |
| HTTP 200 / отмены до заголовков | 122 / 3 | **122 / 2** |
| Минимальный интервал | 455,947542 мс | **500,016126 мс** |
| Интервалов <500 мс | 24 из 124 | **0 из 123** |
| Saved attempts | 122 | **122** |
| Observations / variants / offers | 793 / 1 163 / 2 784 | **793 / 1 164 / 2 786** |
| Unknown / conflict observations | 13 / 0 | **0 / 0** |
| Confirmed public zero observations | 112 | **124** |
| Wall time, включая остановку/recovery/audit | 86,180 с | **84,159 с** |
| Remaining | 0 | **0** |

| Объект | Jobs | HTTP starts | Success | No offers | Partial/failed | Observations | Offers | Remaining |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Arena33 | 61 | 62 | 60 | 1 | 0/0 | 183 | 1 228 | 0 |
| Lindsay | 61 | 62 | 60 | 1 | 0/0 | 610 | 1 558 | 0 |

Два run завершились `succeeded`. По 61 сохранённой попытке на отель. Фактическая
доставка двух отменённых запросов удалённому серверу не подтверждена: 124 — число
начатых базовых RoundTrip, не access log Bnovo. Отменённые попытки не записаны
как завершённые observations; восстановление даёт at-least-once HTTP execution.

Вместе со smoke сделано **126 live RoundTrip starts**, из них 124 HTTP 200 и 2
отмены. Offline/mock проверки в это число не входят. HTTP 403/429/5xx — 0;
HTTP retry/backoff в live не срабатывали, они проверены контролируемыми тестами.

Источник может менять цены, категории с предложениями и варианты. У Lindsay
+1 сохранённый variant и +2 offers; они сверены с текущим raw, это не объявляется
регрессией. Разницу wall time нельзя приписать исправлению: изменились ответы,
число отмен и длительности HTTP; на хосте также выполнялись unit/race проверки.
Данные: [full.json](full.json), [metrics.json](metrics.json).

## 4. #36: реальные HTTP-старты

Измерение — первая операция базового transport, переданного в Bnovo NewClient;
production limiter оборачивает этот transport. Это граница под transport gate,
а не admission/observer/SQL и не момент прихода пакетов на сервер.

Все **123 последовательных интервала >=500 000 000 нс**, минимум **500 016 126 нс**.
Допуск к порогу не добавлялся; точность при анализе сохраняется до наносекунд,
без усечения Python datetime до микросекунд. Timestamps и интервалы:
[http-starts.csv](http-starts.csv).

Peak in-flight HTTP=**6**, peak logged active scenarios/source=**6**, property=**4**:
Фактические HTTP peaks по отелям: Arena33=**4**, Lindsay=**3**.
Долгие запросы перекрывались, source rate limiting не превратилось в последовательный
HTTP. Первые 12 сохранённых attempts распределены 6/6 между отелями; оба завершили
61 сценарий. Вытеснения одного объекта не обнаружено.

Контролируемые regression tests дополнительно подтверждают общий gate разных
клиентов/алиасов origin, несколько workers/отелей, задержку observer.before,
redirects, retry/backoff/Retry-After, отмену/deadline в ожидании и отсутствие HTTP
после отмены. Долгие запросы остаются конкурентными. Live не провоцировал 429
и не подменяет эти тесты реальным испытанием перегрузки источника.

## 5. #37: точная недоступность и изменение внешних данных

Fixtures для 30–31.12.2026, adults=1: Arena33 **3 known=0**, Lindsay **10 known=0**,
оба `no_offers` без plans metadata. Все негативные cases из #37 проходят:
другие/некорректные даты, пропущенные категории, минимум ночей (включая дополнение
к точной фразе), общий текст, неполные блоки, потенциальные тарифы без plans,
противоречия и смешанная доступность.

Текущий live на 30–31.12:

- Arena33 — `success`: Garden=4, Stadium=9, Premium=5, **known**; источник уже
  предлагает тарифы. Требовать нули как в первом прогоне было бы неверно.
- Lindsay — `no_offers`, **все 10 категорий known=0** с точными сообщениями.
- Для Arena33 полностью подтверждённая недоступность получена на **02–03.11.2026**:
  3 known=0. Эти данные также прошли весь путь до PostgreSQL.

Независимый HTMLParser проверил карточки и полный текст nodates для этих двух
no_offers ответов, сопоставил календарные даты с запросами. В обоих HTML нет
селекторов; опубликовано **`$plansForJs = []`**: маркер есть, пригодного объекта
тарифных метаданных нет. Это уточнение текущих raw данных, не утверждение о полном
отсутствии маркера. Parser до #37 считал такой plans недоступным.

Парный offline replay **одних и тех же текущих захваченных HTML**, без сети:

| Production implementation | Lindsay 30.12 (10) | Arena33 02.11 (3) |
|---|---|---|
| 74c3492, до исправлений | partial + plans_metadata_missing, unknown | partial + plans_metadata_missing, unknown |
| 4b8a924, текущий dev | no_offers, known=0 | no_offers, known=0 |

Таким образом #37 подтверждён реальными данными обоих объектов, а не только
отсутствием unknown в итоговой статистике. Публичный ноль означает отсутствие
доступных номеров **по конкретному сценарию**, не физическую занятость всего фонда.
Артефакты: [raw extraction](public-unavailability-evidence.json),
[baseline replay](nooffers-baseline-replay.json), [current replay](nooffers-current-replay.json).

## 6. Достоверность и границы сверки

Прочитанные прямо из live PostgreSQL **122 attempts / 793 observations / 2 786 offers**
сопоставлены с захваченными ответами; **0 mismatches**. Проверены UID/отель, даты,
adults, категории, площадь, варианты, тарифы, decimal цены и валюты, stock/status,
nodates flags, warning codes/contexts, Visible, DataAvailable, RowAvailable,
SelectableQuantities, SelectDisabled, DataAvailableForGuests.

DOM walker отдельно проверил исходные selector/price/options для **2 786 offers**.
Сравнение нормализации использует тот же parser и доказывает сохранность
преобразования, а не независимую бизнес-полноту. Для no_offers добавлена независимая
проверка точных сообщений raw HTML. В выборке — начало/середина/конец горизонта,
оба отеля; hashes и RawSamples находятся в full.json.

Одинаковые физические номера по тарифам не суммируются. 669 observations имеют
положительный known остаток, 124 — подтверждённый ноль; NULL/unknown/conflict — 0,
площадь присутствует у всех 793. PMS/независимого эталона физического фонда нет.
Cancellation/prepayment parser видит у всех 2 786 offers, но storage-модель их
не сохраняет; отсутствие истории этих условий не скрывается этим отчётом.

Дополнительный **offline** replay всех 122 захваченных ответов в новой PostgreSQL
схеме проверил полноту variants и получил те же **793 / 1 164 / 2 786** без сети.
Его SQL отдельно от live: [offline-replay.json](offline-replay.json).

## 7. SIGTERM, leases и идемпотентность

Настоящий дочерний процесс получил SIGTERM в **10:56:35.973554333 UTC** после 12
HTTP completions. `signal.NotifyContext` зафиксировал interruption. На остановке:

- 12 committed attempts, 78 observations, 112 variants, 266 offers;
- 106 queued, 4 running, **0 terminal canceled jobs**;
- 14 HTTP starts: 12 завершённых, 2 отменённых; ещё 2 claimed jobs до HTTP не дошли.

Другой процесс восстановил всю очередь через ту же схему/lease API. Незавершённые
jobs не потеряны; завершённые attempts не перезаписаны. После full повторены
`plan` того же slot и `resume`: новых HTTP/attempts/jobs нет. SQL показывает 0
дублей по всем ключам и 0 расхождений attempts count/max number.

Без публичного HTTP отдельный PostgreSQL-тест с **двумя независимыми процессами**
подтвердил одного claim winner, token increment после expiry, отказ stale write
и один committed attempt. Существующие тесты renewal, fencing, graceful flush,
rollback при deadline, singleton exclusion и межпроцессного handoff тоже PASS.
Это контролируемое доказательство, не crash-испытание против публичного Bnovo.

## 8. SQL evidence

[diagnostics.sql](diagnostics.sql) содержит 19 фактически использованных read-only
запросов; [sql-evidence.json](sql-evidence.json) — результаты из live схемы до cleanup.

| Проверка | Фактический результат |
|---|---|
| table_counts | runs=2, jobs=122, attempts=122, observations=793, variants=1164, offers=2786 |
| jobs | Arena33: succeeded=60, no_offers=1; Lindsay: succeeded=60, no_offers=1 |
| http_codes сохранённых attempts | HTTP 200: 122; ошибок нет |
| duplicates jobs/attempts | 0 / 0 |
| duplicate observations/variants/offers | 0 / 0 / 0 |
| attempt_count_mismatches | 0 |
| orphans jobs/attempts/observations/offers | 0 / 0 / 0 / 0 |
| known zero без подтверждения / unknown с количеством | 0 / 0 |
| stock | positive known=669; confirmed known=0: 124; unknown/conflict=0 |
| scenario_shape | adults=1, rooms=1, los=1, ru, 31.10–30.12, jobs=122 |
| future run timestamps / bad attempt order / future attempts | 0 / 0 / 0 |
| warnings / remaining | 0 / 0 |

Сценарии последних дат с source IDs/категориями представлены SQL-запросом
`exact_date_unavailability`, распределение всех категорий — `category_counts`.
Сырые HTML в SQL-артефактах отсутствуют.

## 9. Оставшиеся ограничения и вердикт

Новых блокеров **штатного Service/RunQueued пути** не обнаружено. Предыдущий NO-GO
по rate limiting снят измерениями; излишне консервативный ноль исправлен и
подтверждён на тех же текущих payload при сравнении двух implementations.

[Issue #41](https://github.com/p-force/hotel-parser/issues/41) — подтверждённый
дефект старого `CollectPropertiesStored`: без одной ожидаемой карточки он сохраняет
полный `no_offers`, 2 observations, warnings пусты. Opt-in contract test **FAIL**
не засчитан как PASS; [отдельный лог](legacy-stored-repro.log). Этот API исключён
из production GO до исправления. `RunQueued` проверяет конфигурацию и проходит
соответствующие негативные тесты.

CONDITIONAL GO означает: регулярный сбор только проверенных Arena33/Lindsay,
через существующий schedule service с актуальным dev, singleton, прямым/session
PostgreSQL connection для advisory lock, caps 8/4/8 и interval 500 мс. Lindsay
требует отдельного включения в рабочей конфигурации; она не была включена этим PR.
Для нескольких активных runner-процессов нужна #33. Production deploy, Neon,
serverless budget и другие отели этой локальной проверкой не сертифицированы.
Нельзя интерпретировать этот вывод как подтверждение физической занятости/PMS.

## 10. Команды, проверки и артефакты

Команды диагностического запуска (RAW_* — приватные каталоги вне checkout):

```bash
python3 scripts/verify-bnovo-docker.py --phase smoke --output "$SMOKE_OUT" --private-raw-dir "$RAW_SMOKE"
python3 scripts/verify-bnovo-docker.py --phase full --output "$FULL_OUT" --smoke-evidence "$SMOKE_OUT/evidence.json" --private-raw-dir "$RAW_FULL"
# Только replay, без network fallback:
python3 scripts/verify-bnovo-docker.py --phase replay --output "$REPLAY_OUT" --replay-evidence "$FULL_OUT/evidence.json" --private-raw-dir "$RAW_FULL"

go test ./...
go test -race ./...
make check
make test-integration-docker
git diff --check
```

Все обычные проверки — **PASS**. Исправленные contract tests #36/#37 выполняются
без env gate и не пропущены. Live harness по умолчанию opt-in; обычные integration
checks публичные источники не вызывают. Отдельно:

```bash
HOTEL_PARSER_REPRO_KNOWN_DEFECTS=1 make test-integration-docker
```

Эта команда воспроизводит **FAIL только нового contract #41**:
`missing configured category persisted as complete: outcome=no_offers observations=2 warnings=[]`.
Она не считается успешной E2E-проверкой. Причина/границы описаны выше.

Парный исторический replay запускался только offline в отдельном detached worktree
74c3492 и на текущем dev. Исходник процедуры сохранён как
[offline-parser-comparison.go.txt](offline-parser-comparison.go.txt); временная
команда и worktree удалены. [audit-public-capture.py](audit-public-capture.py)
проверяет приватный capture по `--evidence-dir` и `--private-raw-dir`.

Self-review: сверены hashes baseline, отсутствие production/config diff, разделение
live/offline/expected-failing evidence, точность HTTP intervals, сохранность jobs,
claims/leases и отсутствие секретов/raw HTML в публикуемых артефактах.
[checks.json](checks.json) фиксирует результаты команд; SHA256SUMS.json — hashes
артефактов повторной проверки. #35 остаётся открытой до согласования результата.
