# Автоматическое назначение курьера — этап 10

Delivery API запускает Dispatch Worker внутри своего процесса. Каждая реплика API
может запускать worker; отдельного сервиса, брокера или Saga нет. Отключить подбор
можно через `DISPATCH_ENABLED=false`, сохранив API и ручное назначение.

```mermaid
flowchart LR
    Staff[Сотрудник ресторана] -->|PATCH coordinates / OIDC grant| Catalog
    Catalog -->|private quote: цены + координаты| Orders
    Orders -->|Order + snapshot + Saga + Outbox| OP[(Orders PostgreSQL)]
    OP -->|CreateDelivery / delivery.commands.v1| Kafka[Redpanda]
    Kafka -->|transactional Inbox| DP[(Delivery PostgreSQL)]
    Courier[Аутентифицированный курьер] -->|POST own location| API[Delivery API]
    API -->|rate limit + GEOADD + freshness| Redis[(Redis GEO)]
    Worker[Dispatch Worker в Delivery] -->|короткая claim lease| DP
    Worker -->|GEOSEARCH: distance ASC, courier ID ASC| Redis
    Worker -->|проверки + assignment + Outbox в транзакции| DP
    Dispatcher[Диспетчер / OIDC permission] -->|ручное назначение| API
    API -->|тот же domain.Assign и SQL locks| DP
    DP -->|CourierAssigned / Outbox| Kafka
    Kafka -->|Orders Inbox → существующая Saga| Gate[Разрешение приготовления]
```

## Координаты и API

Авторизованный admin или restaurant_staff с серверным grant ресторана обновляет
**обе** координаты через существующий `PATCH /restaurants/{id}`:

```json
{"latitude":38.5598,"longitude":68.7870}
```

Допустимы latitude от −90 до 90 и longitude от −180 до 180; NaN/Infinity и
неполная пара запрещены. Адрес ресторана сохраняется. У исторических ресторанов
координаты NULL. Создание ресторана сохраняет прежний API; координаты задаются PATCH.

Private `POST /internal/v1/menu/quote` дополнительно возвращает необязательное поле
`restaurant_location: {latitude, longitude}` из того же SQL snapshot, что цены,
адрес и доступность. Orders проверяет ответ и сохраняет координаты только для
DELIVERY. Клиент заказа не выбирает координаты ресторана. Изменение Catalog
после создания не меняет Order/Delivery snapshot и исходный idempotency response.

`POST /couriers/me/location` принимает только latitude/longitude. Courier ID
берётся из серверной привязки OIDC subject, dev headers разрешены лишь в прежнем
явно включённом development/test режиме. Поле `courier_id` в JSON запрещено.
Успех — 204, неверная/неполная пара — 400, чужая роль — 403, недоступный курьер —
409, слишком частое обновление — 429, недоступный Redis — 503.

Redis GEO поддерживает широты только ±85.05112878. Поэтому GPS endpoint принимает
этот диапазон; ресторан вне него допустим в Catalog, но требует ручного назначения.
Это ограничение [проекции Redis GEO](https://redis.io/docs/latest/commands/geoadd/).
Курьер сначала переводит себя в AVAILABLE через прежний endpoint статуса.

GPS не записывается в PostgreSQL. Lua атомарно делает GEOADD и сохраняет время
**Redis TIME**, а не время клиента, в sorted set `seen`. Там же действует общий для
реплик rate limit. Оба ключа имеют TTL, один hash tag и исчезают при отсутствии
обновлений. На обновлении/поиске удаляется до 1000 просроченных участников из обоих
наборов; поиск дополнительно проверяет свежесть каждого результата, удаляя
просроченные записи в радиусе. Поэтому оставшийся stale GEO member не становится
кандидатом. Отсутствующий `seen` также исключает участника.

OFFLINE/BUSY могут кратковременно оставаться в индексе после смены SQL-статуса.
Это безопасно: Redis определяет кандидатов, а не право назначения. Новый GPS для
них не принимается, и PostgreSQL повторно проверяет AVAILABLE перед назначением.
После потери Redis курьерам нужно снова отправить свежие координаты.

## Назначение и конкуренция

1. Короткая транзакция выбирает due PENDING_ASSIGNMENT через `FOR UPDATE SKIP
   LOCKED`, записывает UUID lease, срок и попытку; сразу коммитится.
2. Без SQL-транзакции worker ищет все результаты в радиусе через
   [GEOSEARCH](https://redis.io/docs/latest/commands/geosearch/), сортирует по
   приблизительному расстоянию в метрах, затем по числовому courier ID. COUNT/ANY
   не используются: недоступные ближайшие участники не должны скрыть следующих.
3. Новая короткая транзакция блокирует Delivery, проверяет lease token и expiry,
   PENDING_ASSIGNMENT, отсутствие приготовления, cancellation fence и deadline.
   В порядке расстояния берётся доступный кандидат через courier row lock с
   SKIP LOCKED; проверяются TTL результата и cooldown в PostgreSQL.
4. Общий `Delivery.Assign` меняет состояние; прежний persistence-механизм ставит
   курьеру BUSY и пишет CourierAssigned в Outbox в той же транзакции. Существующий
   partial unique index запрещает две активные доставки одному курьеру.

Ручное назначение блокирует те же строки и использует тот же доменный переход.
Побеждает первый успешный commit; проигравшая попытка не пишет событие. Worker
повторяет работу по состоянию PostgreSQL, а не по памяти. При потере ответа после
commit старый lease не создаёт повторный event. После падения worker просроченную
lease получает другая реплика; UUID защищает от прежнего владельца. После ожидания
коротких SQL-блокировок кандидат перепроверяется. SKIP LOCKED может пропустить
занятую другой транзакцией ближайшую строку: ближайший означает ближайший
доступный и доступный для блокировки кандидат на момент попытки.

Отказ до pickup атомарно освобождает курьера, сохраняет `declined_at` для пары
Delivery/Courier, сбрасывает локальные попытки и возвращает PENDING_ASSIGNMENT.
Worker исключает эту пару на `DISPATCH_COOLDOWN`; другие заказы для курьера
разрешены. После приготовления worker не работает: повторное назначение делает
диспетчер. POS collection, confirmation audit и завершение не меняются.

## Saga, остановка и восстановление

CreateDelivery v1 получает **добавочные необязательные** restaurant_location и
dispatch_deadline. Новая Saga сохраняет этот deadline и команду атомарно с Order.
Окно равно `SAGA_ASSIGNMENT_TIMEOUT` от отправки CreateDelivery: задержка создания
Delivery расходует часть окна. DeliveryCreated и отказ не продлевают его.
Исторические Saga без этого поля сохраняют прежние таймеры.

После DeliveryCreated Order ждёт CourierAssigned, и только затем разрешает
prepare. Dispatch не готовит еду и не принимает решения об отмене. Истечение
окна/лимита попыток оставляет PENDING_ASSIGNMENT; существующая Saga применяет
`SAGA_CANCEL_BEFORE_PREPARING` или NEEDS_ATTENTION. Приготовленная еда, pickup и
физическое завершение никогда не отменяются worker.

CancelDelivery под delivery lock останавливает назначение. Также обработанный
OrderCancellationRequested/OrderCancelled/OrderRejected ставит локальный fence,
даже если команда ещё в пути. OrderResumed снимает его. При одновременной отмене
и назначении SQL сериализует локальные изменения; уже совершённое назначение
корректно компенсируется существующей Saga, а не SQL rollback между сервисами.

Worker приостанавливается при открытом consumer_quarantine. Перед SQL-назначением
он берёт shared transaction advisory lock с прежним ключом consumer fence и
перепроверяет карантин. Consumer/replay используют существующий exclusive lock.
Redis вызывается без этой блокировки; разные workers могут назначать параллельно.
После recovery подбор возобновляется в пределах сохранённых deadline/попыток.
PICKUP не создаёт Delivery, команд, lease или записи dispatch cooldown.

Диспетчер с существующей OIDC ролью dispatcher/admin, permission
`delivery:dispatch` и серверным ID grant использует:

| API | Назначение |
| --- | --- |
| `GET /deliveries?after=0&limit=100` | Ожидающие записи имеют status PENDING_ASSIGNMENT; результаты пагинированы |
| `GET /deliveries/{id}` | dispatch_paused, dispatch_attempts, dispatch_reason, dispatch_next_at, dispatch_deadline |
| `POST /deliveries/{id}/assign` с courier_id | Прежний ручной fallback, без зависимости от Redis/GPS |
| `POST /deliveries/{id}/dispatch/retry` без body | Сбросить локальные попытки до истечения окна, 204; неподходящий статус — 409 |

`dispatch_paused=true` означает завершение автоматических попыток до явного retry.
Счётчик хранит фактическое число попыток, а не фиктивное значение лимита.
Причины: `restaurant_coordinates_missing`, `restaurant_outside_geo_projection`,
`deadline_expired`, `redis_unavailable`, `no_available_courier`,
`attempts_exhausted:<reason>`, `manual_assignment_required_after_preparation`.
`searching` означает текущую lease; её истечение позволяет восстановить работу
в пределах бюджета. Исчерпавшая бюджет lease после падения помечается
`attempts_exhausted:lease_expired`; SQL-ошибка назначения — `assignment_failed`.
После истечения окна оператор возобновляет **существующую** Saga через прежний
`POST /restaurants/{restaurant_id}/orders/{id}/saga/retry`. Для ожидающего заказа до
приготовления она выдаёт новый CreateDelivery с новым окном; Delivery сохраняет
тот же ID и snapshot и публикует DeliveryStateReported. Это не возрождает отмену
и не продлевает окно локальным HTTP-запросом к Delivery. Ручное назначение остаётся
доступно по прежним доменным правилам, в том числе после приготовления.

## Настройки и эксплуатация

Compose добавляет приватный Redis без persistent volume: геопозиции — временный
индекс. Нет зависимости запуска API от Redis healthcheck. `/ready` Delivery
проверяет PostgreSQL/схему; Redis outage не делает неготовым весь Delivery API.
Redis-клиент имеет секундные сетевые таймауты, context deadlines и отключённые
скрытые retries. Redis error сохраняет диагностическую причину и переносит
попытку, не останавливая процесс. Orders/PICKUP/ручное назначение не обращаются
к Redis. После исчерпания попыток нужен явный retry.

| Переменная | По умолчанию |
| --- | --- |
| REDIS_URL | redis://redis:6379/0; также поддерживается rediss:// |
| DISPATCH_ENABLED | true |
| DISPATCH_RADIUS_METERS | 5000; допустимо (0, 100000] |
| DISPATCH_POLL_INTERVAL | 1s при отсутствии работы |
| DISPATCH_LEASE / DISPATCH_OPERATION_TIMEOUT | 15s / 3s; lease должна быть больше timeout |
| DISPATCH_RETRY_INTERVAL / DISPATCH_MAX_ATTEMPTS | 5s / 60 |
| DISPATCH_COOLDOWN | 2m |
| LOCATION_TTL / LOCATION_MIN_INTERVAL | 60s / 5s; interval меньше TTL |

Реплики должны иметь одинаковые dispatch/GPS настройки. Настройте синхронизацию
часов PostgreSQL, Redis и Delivery: TTL рассчитывается Redis, повторная SQL-проверка
использует PostgreSQL clock. Следите за pending age, dispatch_reason/attempts,
просроченными lease/deadline, Outbox backlog и **open consumer_quarantine**, а также
за памятью/latency Redis. Redis использует noeviction и ограничение памяти 128 MB:
при заполнении новые GPS отклоняются, старые позиции истекают, ручной fallback
сохраняется. Размер радиуса/флота определяет объём GEOSEARCH; нагрузочное тестирование
большого флота и capacity planning остаются эксплуатационной задачей.

Расстояние Redis — приближённое географическое расстояние, не маршрут по дорогам
и не ETA. GPS принимается как сообщение аутентифицированного курьера, без внешней
верификации физической позиции. Нет дорог, traffic, навигации, Google Maps,
мультизаказов или онлайн-платежей.

## Миграции, обновление и проверки

Миграции: Catalog 000004, Orders 000007, Delivery 000005. Существующие адреса,
Inbox/Outbox, event IDs, версии и POS audit не переписываются. Старые координаты и
deadline остаются NULL; worker безопасно оставляет такие доставки для диспетчера.
Отсутствующие новые contract fields имеют omitempty, поэтому canonical encoding и
fingerprint исторических сообщений не меняются. Обычные order/delivery events
не меняются: снимок передаётся направленной командой через прежний topic.

Безопасный rollout: сначала миграции; затем **все Delivery consumers/API** с
поддержкой новых optional fields и Catalog; затем Orders API/consumer/Saga
producer. До обновления всех consumers новые поля публиковать нельзя: старый
consumer их не сохранит. Worker можно включить последним. Обратное обновление
сначала отключает producer/worker; удаление snapshot колонок down-миграциями
требует резервной копии. Это обычный additive v1 rollout, без нового topic и
изменения версии уже существующих сообщений.

```bash
cp .env.example .env # только для нового окружения
docker compose --env-file .env -f deploy/compose.yaml up --build -d
./scripts/check.sh
./scripts/test-integration.sh
```

Второй скрипт поднимает PostgreSQL 18, Redpanda и Redis, проверяет migration round
trips, все Go-модули с race и vet. Для уже запущенных отдельных test dependencies:

```bash
export TEST_DATABASE_URL='postgres://test:test@localhost:15432/food_test?sslmode=disable'
export TEST_REDIS_URL='redis://localhost:16379/0'
export TEST_KAFKA_BROKERS='localhost:29092'
go test -race -count=1 ./platform/... ./contracts/... ./services/catalog/... ./services/orders/... ./services/delivery/... ./services/notifications/...
```

Без соответствующих переменных PostgreSQL/Redis/Kafka integration tests явно
пропускаются. Проверяются расстояние/радиус/tie-break, TTL, rate limit/identity,
OFFLINE/BUSY, гонки workers/диспетчера, Outbox, lease recovery, cooldown, retry
limit, cancellation fence, старые сообщения/доставки, продолжение Saga и PICKUP.
