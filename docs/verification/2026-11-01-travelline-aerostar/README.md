# TravelLine: AZIMUT Аэростар

Проверка 01.11.2026 (MSK). TravelLine property ID — `585`, селектор AZIMUT — `101`.
Выбран отель с воспроизводимым анонимным JSON API и независимым публичным контролем.

## Подтверждённый контракт

Origin: `https://ru-ibe.tlintegration.ru`. Оба вызова — GET с URL-encoded query,
`Accept: application/json`, без body, cookies и токенов. Специальные headers не потребовались.

`/ApiWebDistribution/BookingForm/hotel_info`:

```text
hotels[0].code=585
language=ru-ru
audience=BookingForm
```

`/ApiWebDistribution/BookingForm/hotel_availability`:

```text
criterions[0].hotels[0].code=585
criterions[0].adults=2
criterions[0].dates=2026-11-17;2026-11-18
include_all_placements=false
include_promo_restricted=true
include_rates=true
include_transfers=true
language=ru-ru
audience=BookingForm
```

`hotel_info`: `hotels[].code/currency/room_types/rate_plans`; категории содержат
`code/name/kind/size`. `hotel_availability`: `room_stays[]` с `hotel_ref`, `guests`,
`stay_dates`, `room_types`, `rate_plans`, `total.price_after_tax/currency`.
`room_types[].room_type_quota_rph` связывает предложение с
`room_type_quotas[].rph/quantity`. RPH действует внутри ответа, это не ID категории.
Цена — за запрошенный период, валюта — RUB. Количества разных тарифов не складываются.

Проверяются HTTP status и семантические `errors`, а также идентичность отеля,
категории, дат и гостей. Непустые `availability_result`/`warnings` сохраняются
без догадок о кодах minimum stay; неоднозначность не становится нулевым остатком.
Протокол календарного `room_type_availability_2` не подтверждался и адаптером не
используется: даты, предложения и квоты получены из основного endpoint.

## Контроль публичных цен

Страница: <https://azimuthotels.com/ru/tl/availability?hotel=101>.
Её публичный JS запрашивает GET `/api/tl/availability` на том же домене с
`hotelId=101`, `checkIn=2026-11-17`, `checkOut=2026-11-18`, `adults=2`,
`lang=ru`, пустыми `children` и `promo`. Один номер, LOS=1, без авторизации.
Контроль запрашивался с `Accept: application/json`, `X-Requested-With: XMLHttpRequest`.
В production адаптер обращается только к прямому TravelLine API.

В исследовательском GET TravelLine: HTTP 200, 80 исходных предложений,
8 квот, 10 категорий метаданных, семантических ошибок нет. После исключения BONUS —
40 предложений. Все 40 найдены в контроле с совпадающими ценами, валютой и датами.
У 35 совпали количества; пять тарифов категории `4347` дали API=10, контроль=9.
Запросы сделаны в разное время; причина расхождения не установлена. Оно не
скрывается и не используется для корректировки API-остатка. В позднем smoke API
для `4347` вернул 9, но это отдельный замер для одного взрослого.
Сравнение: [public-comparison.json](public-comparison.json).

`promo=false` встречается и у BONUS. Публичный allowlist в конфигурации установлен
по `ratesDb.lp=0`, `azimutBonusRate=0`, `available=true` контрольного источника и
совпадению ID с метаданными TravelLine. Из него в LOS=1 получены тарифы `128299`,
`128301`, `128311`, `128337`, `917131`. Персональные/BONUS-тарифы исключаются;
название с BONUS дополнительно блокируется даже при ошибочном включении в allowlist.

## Smoke через очередь

Штатный путь: `Service.Execute(plan) → PostgreSQL → RunGroups/RunQueued runtime →
source.Client → NormalizeCategoryStock → fenced CompleteJob → observations`.
Логический слот 17.11.2026 20:00 MSK использован для единственного будущего сценария
17–18 ноября, 1 взрослый, 1 номер, LOS=1; политика planner не изменялась.
Тестовая схема PostgreSQL изолирована и удалена после проверки.
Существующий planner записывает плановое время будущего слота в run.started_at;
run.finished_at в этом искусственном smoke также равен будущему слоту. Реальные
времена HTTP/attempt и измеренные duration сохранены отдельно в evidence.
Поведение времён run не менялось в рамках адаптера.

- 2 HTTP-запроса, оба 200; ошибок и retry — 0; batch от планирования до сохранения — 2446 мс.
- Metadata: 80018 байт / 1058 мс; availability: 200695 байт / 810 мс.
- Интервал между фактическими стартами transport — 1061,941 мс.
- 1 job, 1 committed attempt, 10 observations, 40 offers; результат `partial`.
- Повторные plan/resume не создают HTTP-запросы, jobs, attempts или observations.
- Цены, валюта и нормализованные остатки прочитаны из PostgreSQL и сверены с ответом.

| Категория | Публичный остаток | Статус |
|---|---:|---|
| 103578 | 8 | known |
| 103580 | 10 | known |
| 133917 | — | unknown |
| 250899 | — | unknown |
| 4347 | 9 | known |
| 4348 | 1 | known |
| 4349 | 6 | known |
| 4350 | 10 | known |
| 8875 | 3 | known |
| 8876 | 3 | known |

У `133917` и `250899` нет публичных предложений; предупреждения
`category_availability_unknown`, без утверждения распродажи.
Безопасные результаты и SHA-256 HTTP-ответов: [smoke.json](smoke.json).
Исходные ответы с платёжными ссылками/контактами не публикуются. Testdata содержит
только поля разбора, без контактов, UUID гостей и платёжных данных.

## Ограничения

Публичная квота не доказывает полный физический фонд. Максимумы конфигурации — null.
Семантика подтверждённой полной распродажи и специальных minimum-stay кодов пока
не установлена: пустой ответ/нулевая квота дают unknown, а не known=0/no_offers.
Не поддерживаются дети, более двух взрослых, промокоды, персональные цены и другие отели.
Нативная квота сохраняется как нормализованный observation.available; transient RPH
и квота каждого тарифа отдельно в текущую схему истории не добавляются.
Лимиты 2 активных сценария и 1 старт HTTP/с — консервативная политика приложения,
переопределяемая через ConfigureLimits, действует внутри процесса. Межпроцессный
singleton TravelLine не добавлялся. Lease/fencing защищают запись результата.
Конфигурация Aerostar выключена и не включена в рабочий groups.json.
Массового прогона по 61 дате не выполнялось. Блокеров для проверенного сценария нет.

Повторный ограниченный smoke (два публичных HTTP-запроса, без retry):

```bash
python3 scripts/verify-travelline-docker.py --output /tmp/travelline-smoke.json
```
