# ADR 0002: Orders events, Transactional Outbox и Notification Inbox

> Историческое решение. Актуальный жизненный цикл заказа, POS-оплата и события
> исполнения определены в [ADR 0003](0003-pos-fulfillment.md).


Статус: принято. Дата: 2026-10-17.

## Контекст и границы

Orders синхронно получает quote от Catalog. Эта проверка цен и доступности
сохранена. После создания/отмены заказа требуется независимо создать намерение
уведомить клиента. Реальная отправка, Payments, Saga и Event Sourcing в этап не входят.

Прямой `COMMIT order; Kafka.Publish()` оставляет окно потери: процесс может упасть
после COMMIT до Publish. Обратный порядок оставляет событие о несуществующем заказе
при неудачном COMMIT. Локальная PostgreSQL-транзакция не включает Kafka.

Выбран Transactional Outbox с at-least-once и транзакционным Inbox потребителя.
Распределённая транзакция и глобальное exactly-once не заявляются.

## Компоненты и зависимости

`contracts` — самостоятельный Go-модуль: JSON DTO `orders/v1`, проверка контракта,
UUID для событий, ограниченный correlation ID. В нём нет чужих domain entities,
Kafka или PostgreSQL. Orders и Notifications зависят только от публичного контракта.
`replace` указывает на monorepo; независимая сборка `GOWORK=off` работает из каждого
сервиса при наличии соседнего contracts. Docker build context Orders/Notifications —
корень monorepo, Dockerfile остаётся в сервисе.

Domain определяет Order и версии его состояний / Notification со статусом PENDING.
Application определяет порты OutboxRepository, EventPublisher, InboxRepository,
MessageSource и алгоритмы worker/consumer. SQL и franz-go находятся в adapters.
Bootstrap создаёт зависимости конструкторами, управляет сигналами и ресурсами.
В существующей архитектуре репозиторий Orders уже владел Create/Cancel-транзакциями;
Outbox добавлен в эти транзакции без нового универсального Unit of Work/event bus.

Используется **franz-go v1.22.2**: Go без CGO/librdkafka, producer idempotence,
acks=all, синхронное ожидание результата публикации, ручной commit offsets,
контроль rebalancing. Это одна Kafka-библиотека; её `kmsg` используется только
для управления тестовыми topics. Документация:
[franz-go](https://github.com/twmb/franz-go),
[consumer/producer guidance](https://github.com/twmb/franz-go/blob/master/docs/producing-and-consuming.md),
[kgo API](https://pkg.go.dev/github.com/twmb/franz-go/pkg/kgo).

## Контракт v1

Topic по умолчанию `orders.events.v1`, Kafka key — десятичная строка `order_id`.
Типы: `OrderCreated` (aggregate_version=1), `OrderCancelled` (version=2).
Оба payload содержат order/customer/restaurant IDs, subtotal_minor и currency.
event_id — UUID v4, идентичный при всех повторах. Бизнес-ID остаются **положительными
int64**, как в существующих API/БД. Миграции на UUID этот этап не делает.

```json
{
  "event_id": "c6c070d1-f115-4ccc-9e29-60f7db34495c",
  "event_type": "OrderCreated",
  "schema_version": 1,
  "aggregate_id": 42,
  "aggregate_version": 1,
  "correlation_id": "checkout-request-42",
  "occurred_at": "2026-10-17T12:00:00Z",
  "payload": {
    "order_id": 42,
    "customer_id": 7,
    "restaurant_id": 1,
    "subtotal_minor": 25000,
    "currency": "TJS"
  }
}
```

В рамках v1 разрешены новые **необязательные** поля с безопасным отсутствующим
значением. Consumers игнорируют неизвестные поля; существующие значения, типы и
семантику не меняем. Добавление обязательного поля, смена типов или смысла требует
v2, нового DTO/topic и поэтапного обновления consumers (при необходимости временного
dual publishing). Удаление v1 — только после миграции всех consumers и истечения
срока replay. Contract tests фиксируют терпимость к дополнительным полям и отказ
от неподдерживаемых версий. Новые типы событий требуют явного обновления consumer.

Для browser/JavaScript-клиентов числа выше 2^53 требуют lossless JSON parser;
самостоятельно менять int64 на string внутри v1 нельзя.

`X-Request-ID` в Orders принимается только как 1–128 печатных ASCII-символов без
пробелов; иначе генерируется UUID. ID возвращается в HTTP response, хранится в
Outbox/envelope, дублируется в Kafka header и сохраняется в Notification/logs.
HTTP Idempotency-Key и correlation_id имеют разные назначения: replay возвращает
исходный заказ и не порождает новое событие.

## Outbox

Создание: BEGIN → order + items → OrderCreated → idempotency response → COMMIT.
Отмена: BEGIN → SELECT order FOR UPDATE → domain.Cancel (version++) → UPDATE order
→ OrderCancelled → COMMIT. Сбой любой записи откатывает бизнес-данные и событие.
Проигравший конкурентный Idempotency-Key откатывает свой order и Outbox.
Повторная отмена возвращает существующую доменную ошибку, нового события нет.

Существующим заказам миграция присваивает версии 1/2 по текущему статусу.
Исторические OrderCreated не публикуются задним числом: для них прежняя система
не давала событийной гарантии. Отмена старого заказа публикует самостоятельный факт v2.
Старые immutable idempotency responses без Version читаются с восстановленной версией.

Worker — отдельный `cmd/outbox`, масштабируется независимо от HTTP API:

1. Одной короткой SQL-командой выбирает запись через `FOR UPDATE SKIP LOCKED` и
   ставит PROCESSING, случайный claim_token, lease_until, attempts++. PostgreSQL
   фиксирует claim до сетевого вызова. Берётся одна запись, чтобы lease не истекал
   в очереди локального batch. Разные заказы обрабатываются разными workers.
2. Публикует с ограниченным timeout и ждёт broker ACK (`acks=all`).
3. Обновляет PUBLISHED только при совпадении claim_token и ещё активном lease.
   Старый worker не может изменить состояние нового claim: это fencing в БД.
4. При ошибке возвращает PENDING с exponential backoff и equal jitter; после
   OUTBOX_MAX_ATTEMPTS сохраняет DEAD. Записи не теряются. Зависимые версии того
   же заказа остаются заблокированными до ручного requeue. Остальные заказы идут дальше.
5. PROCESSING с истёкшим lease снова доступен для claim. Восстановление логируется.
   При shutdown неопределённый результат оставляется для этого механизма.
6. Периодически удаляет до 1000 старых PUBLISHED по OUTBOX_RETENTION. PENDING,
   PROCESSING и DEAD никогда не очищаются этой политикой.

OUTBOX_LEASE должен превышать publish timeout + два DB timeout. PostgreSQL-транзакция
не держится во время Kafka I/O. Длительная остановка процесса/сети всё равно может
пережить lease: lease не может отменить уже отправленный broker request.

Если Kafka подтвердила запись, но worker упал до PUBLISHED, после lease происходит
**повторная публикация того же event_id**. Idempotent producer защищает от части
повторов внутри producer session, но не заменяет Inbox после рестарта/нового claim.

## Inbox, версии и ошибки

Notification владеет отдельным PostgreSQL-процессом, database и миграциями.
Consumer читает по одному record, autocommit выключен, rebalance блокируется на
время обработки. В PostgreSQL транзакционно выполняются INSERT processed_events
и INSERT notifications(PENDING). Уникальны event_id и (order_id,aggregate_version).
Конкурентные дубли ждут уникальный constraint и затем становятся no-op.
При ошибке Notification INSERT processed_events также откатывается.

Дубликат сверяется по fingerprint известных полей: тот же ID с другим содержимым
или другой ID той же версии считается конфликтом и попадает в карантин.
Оба события создают самостоятельные факты-намерения; никакое уведомление не
перезаписывает другое. Поэтому даже v2 → v1 даёт два согласованных PENDING,
а поздний повтор v1 не меняет эффект v2. Это **не** механизм выбора актуального
состояния заказа. Реальная отправка позже потребует отдельной политики для
устаревших намерений; сейчас отправки нет и порядок отправки не обещается.

Offset подтверждается синхронно только после COMMIT. Адаптер сохраняет и offset,
и leader_epoch исходного record, что необходимо для корректного возобновления
чтения после смены consumer. Ошибка offset commit останавливает цикл до нового
poll; повтор после рестарта безопасен. На shutdown rebalance разрешается перед Close.

Transient ошибки обрабатываются ограниченными retry с backoff/jitter. Неверный JSON,
неизвестный тип/schema, неверный Kafka key, конфликт ID/version идут без бесполезных
повторов в таблицу failed_messages. После исчерпания retry туда же попадает исходный
record с reason/attempts/topic/partition/offset. Это долговечный PostgreSQL-карантин
вместо отдельного DLQ topic. Содержимое не пишется в логи; доступ к таблице ограничен
доступом к сервисной БД. После COMMIT карантина offset можно подтвердить и идти дальше.

Если БД/карантин недоступны, consumer выходит **без commit**; supervisor его
перезапускает. Пропуск сообщения без долговечной записи невозможен. Это осознанная
остановка при недоступности собственного storage, а не бесконечное блокирование
partition одним некорректным сообщением. После исправления причины отдельная CLI
команда повторяет quarantined record. Если обработка успела COMMIT, а Resolve упал,
следующий replay дедуплицируется. Исходные сообщения неизвестной версии можно
переобработать после добавления её поддержки; исправлять сохранённые bytes вручную
не требуется и не рекомендуется.

## Kafka и порядок

Topic — именованный поток, разбитый на partitions. Offset — позиция в одной
partition; это не глобальный номер и не business ID. Consumer group хранит offsets
и распределяет partitions между участниками: в один момент одна partition назначена
одному участнику группы. Другая группа независимо читает тот же поток.

Одинаковый order_id при неизменном количестве partitions и partitioner попадает в
одну partition. Kafka хранит порядок **добавления записей внутри неё**. Это не порядок
начала PostgreSQL-транзакций. Outbox не claim-ит следующую версию, пока предыдущая
не PUBLISHED, что упорядочивает нормальный путь. Поздние повторы после lease и
параллельные producer sessions всё равно могут прийти после более новой версии;
поэтому consumers используют event_id и aggregate_version, как описано выше.

Между partitions и разными topics общего порядка нет. Увеличение числа partitions
может переместить ключ на другую partition: изменять количество на существующем
потоке без плана миграции нельзя. init-topics сохраняет существующий topic.
Retention Kafka (по умолчанию 7 дней) ограничивает срок восстановления consumer;
оператор должен следить за lag. Удаление PUBLISHED Outbox ограничивает возможность
ручного повторного экспорта. Inbox не очищается автоматически, иначе старый replay
мог бы повторить бизнес-эффект.

## Последовательность

```mermaid
sequenceDiagram
    participant C as Client
    participant O as Orders API
    participant A as Catalog
    participant D as Orders PostgreSQL
    participant W as Outbox Worker
    participant K as Redpanda
    participant N as Notification Consumer
    participant I as Notification PostgreSQL
    C->>O: POST /orders + Idempotency-Key + X-Request-ID
    O->>A: synchronous quote
    A-->>O: prices and availability
    O->>D: BEGIN; order + items + Outbox(v1) + idempotency
    D-->>O: COMMIT
    O-->>C: 201 + order
    W->>D: SKIP LOCKED; claim with lease; COMMIT
    D-->>W: event + claim_token
    W->>K: Publish(key=order_id, event_id)
    K-->>W: ACK
    W->>D: PUBLISHED WHERE token matches and lease active
    K->>N: OrderCreated (possibly repeated)
    N->>I: BEGIN; processed_events + Notification(PENDING)
    I-->>N: COMMIT (duplicate becomes no-op)
    N->>K: Commit offset + leader_epoch
    C->>O: POST /orders/{id}/cancel
    O->>D: BEGIN; lock order; CANCELLED(v2) + Outbox; COMMIT
    Note over W,I: The same publication and Inbox path creates OrderCancelled intent
```

## Эксплуатация и ограничения

Структурированные логи связываются по event_id, order_id, aggregate_version,
correlation_id; Kafka failures/retries, claim recovery, duplicate skip, quarantine
и cleanup имеют отдельные записи. Отдельной системы метрик в проекте не было —
новый observability stack не добавлен. Для диагностики доступны SQL-запросы README,
health/readiness для API, consumer и worker, `rpk group describe` для lag.

Compose — локальный single-broker стенд с persistent volume и fsync. Уведомления
PENDING не означают отправку клиенту. Нет end-to-end exactly-once: гарантируется
атомарный commit локальных данных и намерения, повторяемая доставка, один
зафиксированный эффект на event ID/версию у этого consumer. Прогресс после DEAD
или карантина требует исправления причины/requeue, а долговременная потеря дисков
или lag за retention требуют backup/recovery. Для боевого Kafka нужны отдельно
спланированные репликация, min.insync.replicas, TLS/ACL и эксплуатация; одиночный
broker не выдерживает потерю узла. Это не меняет границы текущего этапа.

Конфигурация listeners и bootstrap соответствует
[документации Redpanda](https://docs.redpanda.com/streaming/current/get-started/quick-start/).
