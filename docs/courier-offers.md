# Courier Offer & Acceptance

Этап 11 заменяет непосредственное **автоматическое** назначение предложением на
30 секунд. Поиск по Redis GEO, SQL lease/fencing, ограничения попыток и Saga
этапа 10 переиспользуются. Ручное назначение диспетчером сохраняется.

## Архитектура и жизненный цикл

Delivery владеет `CourierOffer` и таблицей `courier_offers` в своей БД. Domain
определяет TTL, статусы, допустимые решения и идемпотентность. Application задаёт
порт `OfferRepository`, use cases и отдельный expiry worker; PostgreSQL adapter
реализует транзакции, HTTP adapter — аутентификацию, DTO и маршруты. Bootstrap
запускает поиск и expiry независимо друг от друга в процессе Delivery API и
останавливает оба перед закрытием pool. Нового сервиса или Saga нет.

```mermaid
sequenceDiagram
    participant W as Dispatch Worker
    participant R as Redis GEO
    participant DB as Delivery PostgreSQL
    participant C as Courier HTTP
    participant K as Outbox / Kafka
    participant O as Orders Inbox / Saga
    W->>DB: Claim SKIP LOCKED + lease token; commit
    W->>R: Nearest fresh candidates
    W->>DB: Lock Delivery/Courier; PENDING offer; commit
    C->>DB: GET own live offers
    Note over DB,O: Delivery PENDING_ASSIGNMENT, Order WAITING_COURIER; cooking blocked
    C->>DB: Accept under Delivery/Offer/Courier locks
    Note over DB: ACCEPTED + ASSIGNED + BUSY + CourierAssigned Outbox, one commit
    DB->>K: Existing Outbox publisher
    K->>O: Existing CourierAssigned event
    Note over O: COURIER_ASSIGNED; restaurant may start cooking
```

`CourierOffer`: ID, DeliveryID, CourierID, Status, ExpiresAt, CreatedAt,
RespondedAt. Статусы: PENDING → ACCEPTED / DECLINED / EXPIRED / CANCELLED.
Терминальная запись и время решения сохраняются для аудита; завершённые предложения
не удаляются автоматически. `RespondedAt` — время терминального решения, включая
автоматическое истечение или отмену. PENDING не переводит курьера в BUSY и не меняет
версию Delivery; он резервирует курьера через уникальный индекс.

Подбор повторно проверяет доступность, свежесть GEO member, cooldown, lease,
приготовление, deadline и cancellation/consumer fences. Его SQL-фаза создаёт
предложение и освобождает lease. Пока есть PENDING, новые search claims этой
доставке запрещены. Курьер с PENDING другой доставки исключён из подбора.
SQL-транзакций во время Redis I/O, polling или ожидания решения курьера нет.

## TTL и восстановление

`ExpiresAt = min(PostgreSQL clock_timestamp() + 30s, dispatch_deadline)`.
Deadline принадлежит Orders Saga; предложение, отказ и локальный retry его не
продлевают. Настройки TTL нет: 30 секунд — бизнес-правило.

Отдельный expiry worker стартует сразу и проверяет due offers примерно раз в
секунду, даже при `DISPATCH_ENABLED=false` и недоступном Redis. Он выбирает Delivery
через `FOR UPDATE OF d SKIP LOCKED`, блокирует PENDING offer и атомарно сохраняет
EXPIRED, cooldown и следующий dispatch run. Несколько реплик безопасны. Состояние
и расписание находятся в PostgreSQL: после рестарта нет таймеров, которые нужно
восстанавливать из памяти. Пока API остановлен, запись может оставаться PENDING,
но принять её после ExpiresAt нельзя. GET скрывает истёкшие записи ещё до expiry run.

Accept/decline также проверяют время БД **после** получения всех нужных row locks.
Поздний ответ фиксирует EXPIRED и возвращает 409, даже если expiry worker ещё не
работал. Отказ и истечение обновляют существующий `dispatch_declines` для пары
Delivery/Courier, чтобы следующая попытка выбрала другого курьера в пределах
`DISPATCH_COOLDOWN`. Курьер может получить другой заказ. Попытки поиска остаются
ограниченными `DISPATCH_MAX_ATTEMPTS`; ответы не сбрасывают бюджет. Исчерпание после
ответа получает `attempts_exhausted:offer_declined` / `offer_expired`; потерянная
последняя lease — прежний `attempts_exhausted:lease_expired`. Новое окно после
истечения deadline выдаёт только авторизованный retry существующей Orders Saga.

## PostgreSQL и конкурентность

Два partial unique indexes по `delivery_id` и `courier_id` с `WHERE status='PENDING'`
запрещают две активные резервации. Предикат не зависит от часов: просроченная запись
освобождает резервацию только после сохранения EXPIRED. CHECK ограничивает статусы,
TTL и согласованность RespondedAt. Прежний unique index активных Delivery на
курьера продолжает защищать от двойного назначения.

Общий порядок блокировок ответа: Delivery → CourierOffer → Courier. Отмена,
expiry и ручное назначение сначала блокируют ту же Delivery. Поиск блокирует
Delivery и доступного Courier с SKIP LOCKED, затем повторно проверяет резервацию
в новом SQL snapshot. Назначение использует прежние `Delivery.Assign` и
`writeDeliveryChange`, а не отдельную копию перехода. Ошибка записи Outbox
откатывает ACCEPTED, Delivery и BUSY вместе.

Ручное назначение своей доставки атомарно отменяет её PENDING-предложение, в том
числе при выборе другого курьера. Гонка accept/manual сериализуется на Delivery;
побеждает первая успешная транзакция, вторая получает 409. Резервация курьера для
**другой** доставки даёт конфликт: диспетчер не отменяет чужое предложение скрыто.
Если курьер перешёл OFFLINE, принять предложение нельзя; попытка принятия отменяет
его и возвращает 409. Курьер может отклонить предложение, оставаясь OFFLINE.

Создание предложения и любое назначение используют shared transaction advisory
lock прежнего consumer fence, совместимый с exclusive lock consumer/replay.
Открытый consumer_quarantine блокирует новые предложения и принятие/ручное
назначение (409). Expiry и decline могут освобождать резервации: они не создают
фактов назначения и не обходят fence. Отслеживайте открытый карантин даже при
нулевом Kafka lag.

CancelDelivery отменяет PENDING в той же транзакции, что Delivery/Inbox/Outbox.
OrderCancellationRequested, OrderCancelled и OrderRejected отменяют предложение
под Delivery lock и сохраняют прежний локальный dispatch fence. Если acceptance
уже committed до получения отмены, дальнейшая компенсация остаётся у Saga.
Предложение не даёт права отменять приготовленную или физически завершённую еду.
PICKUP не создаёт Delivery, CourierOffer, dispatch work или cooldown.

## HTTP API

Все маршруты требуют аутентифицированного courier principal с **серверным** CourierID.
Сохраняются OIDC/JWKS и прежний явно включённый development/test identity режим.
Диспетчерская роль сама по себе не даёт доступа к предложениям курьера.

| Метод и путь | Результат |
| --- | --- |
| `GET /couriers/me/offers` | 200, массив собственных живых PENDING; максимум одна запись |
| `POST /courier-offers/{id}/accept` | 200, ACCEPTED после атомарного назначения |
| `POST /courier-offers/{id}/decline` | 200, DECLINED и повторный поиск |

POST принимает пустое тело. Courier ID, статус и время клиент не задаёт.
Пример предложения:

```json
{"id":12,"delivery_id":5,"courier_id":7,"status":"PENDING","expires_at":"2026-10-18T12:00:30Z","created_at":"2026-10-18T12:00:00Z","responded_at":null}
```

Повтор того же принятия/отказа возвращает 200 с первоначальным временем решения,
включая повтор после последующего освобождения Delivery. Повтор не назначает
курьера заново и не создаёт Outbox. Противоположное решение, истечение или отмена —
409. Чужой или отсутствующий ID — 404; без identity — 401, неподходящая роль — 403;
неверный ID или непустое тело — 400. Во время quarantine принятие возвращает 409,
в том числе если нужно повторить запрос после восстановления.

```bash
# Только при явно включённом dev auth; production использует Bearer OIDC token.
curl -H 'X-Dev-Courier-ID: 7' localhost:8085/couriers/me/offers
curl -X POST -H 'X-Dev-Courier-ID: 7' localhost:8085/courier-offers/12/accept
curl -X POST -H 'X-Dev-Courier-ID: 7' localhost:8085/courier-offers/12/decline
```

## Kafka, деградация и эксплуатация

Новых topics, типов событий, optional полей и версий контрактов нет. PENDING,
DECLINED, EXPIRED и CANCELLED не публикуют CourierAssigned. При недоступном Kafka
acceptance всё равно коммитится с Outbox; Orders остаётся WAITING_COURIER до
доставки события через существующий publisher и transactional Inbox. Gate на
приготовление не ослаблен. REPORTED_COLLECTED и POS audit не меняются.

Redis нужен только для поиска новых кандидатов и GPS. Принятие уже сохранённого
предложения, decline, TTL, ручное назначение и PICKUP работают без него.
Новые dispatch reasons: `awaiting_courier_acceptance`, `offer_declined`,
`offer_expired`, `offer_cancelled`, `offer_creation_failed`; остальные прежние.
Мониторьте число/возраст PENDING, просроченные offers, dispatch deadline/reasons,
Outbox backlog и consumer_quarantine. При остановленных Delivery API expiry не
работает; после запуска он обрабатывает backlog, а проверки HTTP не дают принять
просроченное предложение.

## Миграция, запуск и проверки

Delivery migration `000006_courier_offers` добавляет таблицу и индексы, сохраняя
существующие назначения, события, Inbox fingerprints и audit. Исторические
назначения не превращаются в предложения. Сначала примените миграцию и обновите
**все** Delivery API/consumers. Для перехода со старого автодиспетча сначала
отключите `DISPATCH_ENABLED` на старых API, затем замените их и включите поиск:
старая версия worker умеет назначать без принятия. Orders не требует новой схемы
или контрактов. Перед downgrade остановите producers/workers и сохраните таблицу
courier_offers: down удаляет её историю; не откатывайте рабочий процесс с активными
предложениями к версии, которая не знает резерваций.

```bash
cp .env.example .env # для нового окружения
docker compose --env-file .env -f deploy/compose.yaml up --build -d
./scripts/check.sh
./scripts/test-integration.sh
python3 scripts/verify-offers.py
```

Integration runner поднимает отдельные PostgreSQL/Redpanda/Redis, проверяет
migration round trips и все Go-модули с race/vet. Тесты CourierOffer покрывают
принятие, отказ, TTL/late response, точные повторы, конкурентное принятие и
предложения одному курьеру, SQL constraints, отмену событием/командой, гонку с
диспетчером, рестарт, ownership, quarantine, OFFLINE и откат при ошибке Outbox.
Orders integration проверяет gate до CourierAssigned. `verify-offers.py`
дополнительно проверяет весь Compose-поток до продолжения Saga, включая Kafka
outage и перезапуск Delivery с due offer (время fixture сдвигается SQL).

[ADR 0008](adr/0008-courier-offer-and-acceptance.md),
[конфигурация Redis GEO и dispatch](automatic-dispatch.md).
