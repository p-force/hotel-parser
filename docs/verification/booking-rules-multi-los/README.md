# Booking forbidden: реализация и тестовый контракт LOS=1/2/3

Дата: 27.11.2026. База: `dev`, `a452b2b` (после PR #71).
Ветка: `test/travelline-booking-rules-los2-los3`.

**Поддержка LOS=2/3 реализована поверх test-first коммита `035d020`.
Все 29 прежних unit FAIL и 2 integration FAIL перешли в PASS. Тестовые ожидания
и фикстуры `035d020` не изменены. PR #73 остаётся draft.**

Изменены только два production-файла:

- `internal/collect/booking_rules_need.go`: eligibility для LOS=1/2/3 при прежних
  фильтрах source, HTTP, error, outcome и состояния observations;
- `internal/source/travelline/booking_rules_forbidden.go`: для каждого тарифа
  обход максимум трёх календарных ночей через `AddDate`; недостаточные и
  конфликтующие записи пропускаются, а не прерывают проверку следующих ночей.

## Контракт

Проверяются календарные ночи `[check_in, check_out)`. Для каждого применимого
тарифа нужна хотя бы одна дата с непротиворечивым `forbidden=true` для той же
категории. Даты разных тарифов могут различаться. NULL, false, отсутствующий
ключ и конфликт на конкретную дату доказательством не являются; конфликт не
блокирует другое однозначное правило того же тарифа на другой ночи.

Наличие offers исключает обогащение. При подтверждении меняются только
`ConfirmedUnavailable` и `UnavailabilityReason=booking_forbidden`.
`StockUnknown`, `Available=nil`, outcome, предупреждения, варианты и offers
сохраняются. Без достаточных доказательств причина остаётся NULL.

Календарные границы строятся через `AddDate`, не через длительность местных суток.
Контроль DST использует Europe/Berlin (переходы 25.10.2026 и 28.03.2027);
timezone data включены в test binary через `time/tzdata`.

## Фикстуры Советского

Нативный отель `333`, категория `29344` («Сталинские апартаменты» /
«Апартаменты Яръ»), тарифы `38564`, `49218`.

- `internal/source/travelline/testdata/sovietsky-booking-rules-2026-12.json`:
  по два правила на 03, 04 и 05 декабря. Первые два дня — NULL, 05 декабря — true.
- `internal/source/travelline/testdata/sovietsky-multi-los-cases.json`:
  три контрольных сценария из задания.

| Заезд → выезд | LOS | Offers по заданию | Ожидаемый запрет |
|---|---:|---|---|
| 03 → 05.12.2026 | 2 | Есть | Нет; 05 декабря — дата выезда |
| 03 → 06.12.2026 | 3 | Нет | Да, третья ночь |
| 04 → 06.12.2026 | 2 | Нет | Да, вторая ночь |

Это воспроизводимая запись сообщённых пользователем фактов, **не архив live
HTTP-ответов**. Интеграционный mock моделирует лишь необходимые поля основного
API. Цена 100 RUB за весь период, один взрослый и один номер — синтетические параметры теста,
не подтверждённые значения ручной проверки. Публичная квота не задана (NULL).
Дополнительный LOS=1 на 05–06.12 — синтетический контроль общего календаря.
Ручная надпись «Распродано» не используется как самостоятельное доказательство
sold_out или физического нулевого остатка.

## Покрытие

Ранее были покрыты LOS=1, одинаковые/конфликтующие дубликаты, отсутствие ключей,
source/HTTP/outcome-фильтры, неизменность попытки, координатор и PostgreSQL/CSV
для LOS=1. Существующие ожидания LOS=1 сохранены. На test-first этапе `035d020`
старые проверки безусловного отклонения LOS=2/3 заменены ожиданиями нового
контракта, тогда без правки production. На этапе реализации тесты не менялись.

Изменённые тестовые файлы:

- `internal/source/travelline/booking_rules_forbidden_test.go`;
- `internal/source/travelline/booking_rules_multi_los_test.go`;
- `internal/collect/booking_rules_need_test.go`;
- `internal/collect/booking_rules_apply_test.go`;
- `internal/collect/booking_rules_multi_los_test.go`;
- `internal/schedule/booking_rules_multi_los_integration_test.go`.

Новые cases:

- LOS=1/2/3; запрет на заезд, вторую/третью ночь; исключение checkout;
- разные даты запрета двух тарифов; только один подтверждённый тариф;
- false, NULL, отсутствие записей, одинаковые дубликаты и конфликты;
- конфликт на одной дате и однозначный запрет на другой;
- пустой список тарифов, пустой/неизвестный некорректный ID, другая категория;
- границы месяца/года, весенний и осенний DST;
- eligibility LOS=2/3 для success/partial/no_offers и отрицательные проверки
  source, HTTP 0/403/429/500, error, outcome, known/conflict, Available, offers,
  ранее подтверждённой недоступности и отсутствующих observations;
- применение правил: только два поля, сохранность всех остальных данных,
  отсутствие изменений при offers/недостаточных доказательствах и идемпотентность;
- PostgreSQL: четыре независимых jobs одного run с LOS=1/2/3, общий диапазон
  календаря, один дополнительный GET, несколько волн, restart/resume из БД,
  ошибка endpoint без повторного основного сбора, сохранение причин и CSV v4.

Для PostgreSQL используются существующие `storagetest.Open`, `testService`,
TLS mock, очередь, `Service.Execute`, `SaveAttempt` и CSV-экспортер. Feature flag
включается только в тесте. Изолированные `hp_test_*` схемы создаются и удаляются
на локальном PostgreSQL 17 в Docker. Production-БД не используется.

## Результаты после реализации

| Запуск | PASS | FAIL | SKIP |
|---|---:|---:|---:|
| `go test -json -race ./... -count=1` | 1083 | 0 | 0 |
| `go test -json -race -tags=integration ./internal/schedule/... -run '^TestPostgresBookingRules' -count=1 -timeout=120s` | 13 | 0 | 0 |

Обе команды завершились с exit code 0. Все прежние 29 unit FAIL и 2 integration
FAIL теперь PASS; регрессий LOS=1 не обнаружено. Ошибок race detector, panic и
ошибок компиляции нет. `-json` используется только для подсчёта конечных cases.
`go vet ./...`, `go build ./...` и `git diff --check` — PASS.

Подтверждено через PostgreSQL и mock HTTP:

- 03–05.12, LOS=2: offers сохраняются, запрет не подтверждён;
- 03–06.12, LOS=3, и 04–06.12, LOS=2: сохраняются
  `ConfirmedUnavailable=true`, `UnavailabilityReason=booking_forbidden`;
- в контрольных сценариях сохраняются stock unknown/Available NULL, исходный partial,
  предложения и отдельные jobs/attempts разных LOS;
- причина присутствует в CSV v4; формат остаётся из 36 колонок;
- один календарь на run используется всеми LOS и волнами, а после restart
  сохранённый календарь читается без нового HTTP-запроса;
- ошибка дополнительного endpoint не вызывает retry основного запроса и
  оставляет причину NULL.

## Исходный test-first результат (`035d020`, до реализации)

Подсчёт по конечным test cases; родительские suites повторно не учитываются.

| Запуск | PASS | FAIL (expected) | FAIL (unexpected) | SKIP |
|---|---:|---:|---:|---:|
| `go test -json -race ./... -count=1` | 1054 | 29 | 0 | 0 |
| `go test -json -race -tags=integration ./internal/schedule/... -run '^TestPostgresBookingRules' -count=1 -timeout=120s` | 11 | 2 | 0 | 0 |

Обе команды возвращают exit code 1 из-за ожидаемых FAIL. Ошибок компиляции,
panic и сообщений race detector нет. `go vet -tags=integration` для затронутых
пакетов и `git diff --check` — PASS. Интеграционный запуск выполнен штатным Go
процессом на macOS, PostgreSQL — локальный Docker. HTTP — только mock.

Причины исходных ожидаемых FAIL (устранены реализацией):

- `ConfirmCategoryBookingForbidden`: ранний `los != 1` отклоняет все положительные
  LOS=2/3, включая вторую/третью ночь, разные даты тарифов, обход конфликта и DST.
  17 конечных cases FAIL (2 старых ожидания LOS=2/3, 13 календарных,
  2 контрольных сценария Советского).
- `NeedsTravelLineBookingRules`: ранний `scenario.LOS != 1`. 8 cases FAIL
  (2 обновлённых и 6 eligibility-проверок success/partial/no_offers).
- `ApplyTravelLineBookingRules`: не проходит существующие проверки выше;
  4 положительных cases LOS=2/3 FAIL. Негативные проверки проходят.
- `TestPostgresBookingRulesMultiLOS/calendar_once` и `/resume_saved`:
  для 03–06.12 (LOS=3) и 04–06.12 (LOS=2) сохраняются false/NULL вместо
  true/booking_forbidden; причина пуста и в CSV. Проверки количества основных
  и дополнительных запросов, независимости jobs, stock unknown/NULL и offers
  проходят. `/endpoint_error` — PASS, повторных основных запросов нет.

Отсутствие регрессий LOS=1 подтверждается неизменёнными ожиданиями существующих
тестов и успешными контрольными integration-тестами booking rules.

## Реализация и ограничения

1. `ConfirmCategoryBookingForbidden` разрешает LOS=1/2/3, обходит ночи календарно
   и ищет однозначный запрет отдельно для каждого тарифа. Checkout не включается.
2. LOS-фильтр `NeedsTravelLineBookingRules` расширен при сохранении остальных
   ограничений. `ApplyTravelLineBookingRules` без изменений делегирует этим двум
   функциям и меняет только два поля подтверждения.
3. Текущие hook/coordinator прошли integration-тесты без изменений. Они используют
   полный диапазон run и сохраняют правила до подтверждения. Новые scheduler,
   API, миграции и колонки CSV для расширения не понадобились.

Модель совместима: nullable Forbidden сохраняется в индексе и БД; SQL 0001/0009
разрешает confirmed=true с unknown/NULL и reason=booking_forbidden при отсутствии
offers независимо от LOS. Stock и причина остаются разными измерениями.

Неоднозначности и риски:

- Семантика «достаточно одной запрещённой ночи для каждого тарифа» задана этим
  контрактом и контрольным примером. Тесты не подтверждают её живым API для всех
  отелей и условий гостей; параметры ручной проверки не предоставлены полностью.
- `BookingRulesIndex.Conflict` учитывает все сохранённые поля ограничений, не
  только Forbidden. Используется существующее значение Conflict; min/max LOS и
  advance booking не интерпретируются.
- Чистый confirmer получает check_in и LOS, а не check_out. Тесты используют
  согласованные сценарии; нужно сохранить равенство check_out = AddDate(LOS).
  Storage сначала нормализует даты в civil UTC; его проверка длительности после
  этой нормализации сама по себе не является ошибкой DST и здесь не менялась.
- Неоднозначные или отсутствующие правила не доказывают разрешение бронирования:
  результат остаётся unknown, без новых предположительных причин.
- Feature flag `HOTEL_PARSER_BOOKING_RULES_ENABLED` по умолчанию остаётся false;
  реализация не активирует сбор правил автоматически.

Помимо двух перечисленных функций, production-код не менялся. Конфиги, planner,
scheduler, HTTP-клиент, retry/resume/coordinator, SQL-миграции, CSV schema и настройки
VPS не изменены. Deployment, production-миграции и live HTTP не выполнялись.
