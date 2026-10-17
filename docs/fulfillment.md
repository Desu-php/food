# Этап 8: Order Fulfillment Saga, Delivery, Pickup и POS

Самовывоз без распределённой Saga: [PICKUP API](pickup.md),
[ADR 0005](adr/0005-pickup-and-delivery.md). Ниже описан процесс DELIVERY.

Архитектура, переходы, компенсации и две Mermaid-диаграммы:
[ADR 0004](adr/0004-fulfillment-saga.md). POS-модель из ADR 0003 сохраняется.

## Запуск

Для нового окружения:

```bash
cp .env.example .env
docker compose --env-file .env -f deploy/compose.yaml up --build -d --wait
```

Все команды выполняются из корня. Catalog :8080, Orders :8081, Delivery :8085,
Notifications health :8083. PostgreSQL и Redpanda остаются прежними; новая
инфраструктура хранения/брокеров не требуется. У сервисов отдельные логические
БД и login roles. Приложения не применяют миграции самостоятельно.

При обновлении существующего окружения добавьте недостающие переменные из
`.env.example`, сохранив прежние секреты. Сначала backup и остановка старых
writers/consumers, затем согласованное обновление:

```bash
docker compose --env-file .env -f deploy/compose.yaml stop \
  catalog orders outbox orders-delivery-consumer orders-saga \
  delivery delivery-outbox delivery-consumer notifications
docker compose --env-file .env -f deploy/compose.yaml run --rm init-databases
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-orders
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-delivery
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-notifications
docker compose --env-file .env -f deploy/compose.yaml up --build -d --wait
```

Текущие миграции: Catalog 000003, Orders 000005, Delivery 000003, Notifications
000004. Старые PENDING_PAYMENT преобразует Orders 000003; immutable creation
responses и исторические events не переписываются. Orders 000004 создаёт Saga
для существующих заказов, активные reconciles через Kafka. Delivery переименовывает
pos_confirmations в payment_confirmations с сохранением данных и добавлением ID.
Откат новой бизнес-истории запрещён защитными проверками; не удаляйте volumes.

## Настройки Saga

| Переменная | Default | Назначение |
|---|---|---|
| DELIVERY_COMMANDS_TOPIC | delivery.commands.v1 | topic направленных команд; одинаковый для producer/consumer |
| SAGA_CREATE_TIMEOUT | 5m | ожидание результата CreateDelivery |
| SAGA_ASSIGNMENT_TIMEOUT | 10m | ожидание ручного назначения / замены курьера |
| SAGA_PROGRESS_TIMEOUT | 30m | ожидание следующего шага исполнения |
| SAGA_CANCEL_TIMEOUT | 5m | ожидание результата компенсации |
| SAGA_RETRY_BASE / SAGA_RETRY_MAX | 1s / 30s | capped exponential backoff с jitter |
| SAGA_MAX_ATTEMPTS | 20 | предел отправок команды до NEEDS_ATTENTION |
| SAGA_POLL_INTERVAL | 1s | интервал чтения due Saga |
| SAGA_CANCEL_BEFORE_PREPARING | false | при true timeout без курьера до готовки запрашивает отмену |
| SAGA_HTTP_ADDR | :8089 | health/readiness отдельного Orders cmd/saga |

Политика одинакова в Orders API, orders-delivery-consumer и orders-saga. API/SQL
таймауты, KAFKA_BROKERS и Outbox lease/retry/retention — как в README. Длительности
Saga положительные от 1ms до 7 дней; max attempts 1–1000; retry max >= base.
После начала приготовления автоматической отмены нет при любом значении флага.

Identity пока только локальная: APP_ENV=development/test и DEV_AUTH_ENABLED=true.
Production с dev auth не стартует, при отключённом dev auth business endpoints
отвечают 401. DEV_RESTAURANT_GRANTS=10:1,10:2 означает user 10 управляет ресторанами
1/2; DEV_DISPATCHER_IDS=20,21 — серверный список диспетчеров. Заголовки не задают
принадлежность ресторана. В dev/test управляющий ресторана также может выполнить
операторский retry; полноценные production authentication/RBAC ещё не подключены.
Порты Compose опубликованы на 127.0.0.1.

## REST API

POST ресторанных actions, pickup и decline требуют пустое тело. JSON endpoints
требуют Content-Type: application/json, неизвестные server-owned поля отклоняются.
Пагинация after >= 0, limit 1–100, default 20. Следующая страница — after=последний ID.

| Сервис | Метод / путь | Назначение |
|---|---|---|
| Orders | GET /restaurants/{restaurant}/orders | входящие активные заказы своего ресторана |
| Orders | POST /restaurants/{restaurant}/orders/{id}/accept | принять, создать Saga-команду |
| Orders | POST /restaurants/{restaurant}/orders/{id}/reject | отказ; до готовки компенсировать существующую задачу |
| Orders | POST /restaurants/{restaurant}/orders/{id}/prepare | только после CourierAssigned |
| Orders | POST /restaurants/{restaurant}/orders/{id}/ready | приготовление завершено |
| Orders | GET /restaurants/{restaurant}/orders/{id}/saga | фаза, reason, deadline, attempts, версии |
| Orders | POST /restaurants/{restaurant}/orders/{id}/retry | разрешённый retry/resume из NEEDS_ATTENTION |
| Orders | POST /orders/{id}/cancel | клиент отменяет до принятия либо запрашивает отмену до готовки |
| Orders | GET /orders/{id} | состояние заказа и оплаты, courier_id, cancellation_requested |
| Delivery | POST /couriers | диспетчер создаёт {"name":"Courier"}, ответ 201 |
| Delivery | GET /couriers?status=AVAILABLE | диспетчер получает доступных курьеров; без status — все |
| Delivery | POST /couriers/{id}/status | сам курьер: {"status":"OFFLINE"} или AVAILABLE; BUSY менять нельзя |
| Delivery | GET /deliveries | диспетчер — все, курьер — только назначенные ему |
| Delivery | GET /deliveries/{id} | состояние и, после завершения, POS-подтверждение |
| Delivery | POST /deliveries/{id}/assign | диспетчер: {"courier_id":1} |
| Delivery | POST /deliveries/{id}/decline | назначенный курьер освобождает назначение до pickup |
| Delivery | POST /deliveries/{id}/pickup | назначенный курьер забирает ready заказ |
| Delivery | POST /deliveries/{id}/complete | назначенный курьер подтверждает POS при завершении |

Отмена Delivery выполняется командой оркестратора, а не прямым публичным изменением
её статуса. После принятия HTTP cancel/reject возвращает текущий Order с
cancellation_requested=true; конечный CANCELLED асинхронно появляется после
DeliveryCancelled. Приготовление в этом промежутке запрещено.

Стандартные ошибки: 400 — ввод/нет payment_confirmed; 401 — нет identity;
403 — нет прав/чужой курьер; 404 — ресурс отсутствует или недоступен текущему
читателю; 409 — недопустимый переход, недоступный курьер, конфликт повторной оплаты.
5xx не раскрывают внутренние SQL errors/credentials.

## Полный цикл

Создайте блюдо через Catalog API из README. Ниже пример для меню с блюдом ID 1
и ценой 25000 TJS minor; фактические Order/Delivery/Courier IDs берите из ответов.
Они независимы и не обязаны совпадать.

```bash
curl -sS http://localhost:8081/orders -H 'X-Dev-Customer-ID: 7' \
  -H 'Idempotency-Key: saga-demo-1' -H 'Content-Type: application/json' \
  -d '{"restaurant_id":1,"fulfillment_type":"DELIVERY","delivery_address":"Душанбе, улица 1","currency":"TJS","expected_subtotal_minor":25000,"items":[{"menu_item_id":1,"quantity":1}]}'
# WAITING_RESTAURANT / POS_ON_DELIVERY / UNPAID
curl -sS -X POST http://localhost:8081/restaurants/1/orders/1/accept -H 'X-Dev-User-ID: 10'
curl -sS http://localhost:8081/restaurants/1/orders/1/saga -H 'X-Dev-User-ID: 10'
# Дождитесь WAITING_COURIER после DeliveryCreated:
curl -sS http://localhost:8085/deliveries -H 'X-Dev-User-ID: 20'
curl -sS http://localhost:8085/couriers -H 'X-Dev-User-ID: 20' \
  -H 'Content-Type: application/json' -d '{"name":"Курьер"}'
curl -sS 'http://localhost:8085/couriers?status=AVAILABLE' -H 'X-Dev-User-ID: 20'
curl -sS http://localhost:8085/deliveries/1/assign -H 'X-Dev-User-ID: 20' \
  -H 'Content-Type: application/json' -d '{"courier_id":1}'
# Дождитесь courier_id=1 и COURIER_ASSIGNED в Order/Saga:
curl -sS -X POST http://localhost:8081/restaurants/1/orders/1/prepare -H 'X-Dev-User-ID: 10'
curl -sS -X POST http://localhost:8081/restaurants/1/orders/1/ready -H 'X-Dev-User-ID: 10'
# Дождитесь ready_for_pickup=true в Delivery GET:
curl -sS http://localhost:8085/deliveries/1 -H 'X-Dev-Courier-ID: 1'
curl -sS -X POST http://localhost:8085/deliveries/1/pickup -H 'X-Dev-Courier-ID: 1'
# После фактической оплаты на физическом POS:
curl -sS http://localhost:8085/deliveries/1/complete -H 'X-Dev-Courier-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"payment_confirmed":true,"receipt_reference":"POS-42"}'
curl -sS http://localhost:8081/orders/1 -H 'X-Dev-Customer-ID: 7'
# После обработки DeliveryCompleted: DELIVERED / REPORTED_COLLECTED; Saga COMPLETED.
```

В complete нет суммы, валюты, payment method, ConfirmedBy или timestamp от клиента.
Reference необязателен, до 200 символов без NUL/CR/LF. Старый pos_reference
принимается как alias; если оба поля переданы с разными значениями, ответ 400.
Повтор с тем же reference возвращает прежнее подтверждение/ID и не создаёт
вторую оплату. Это операционное сообщение, не банковская верификация.

При отказе курьера до pickup выполните POST decline и вручную назначьте другого.
Если приготовление уже началось и новый курьер не появился за deadline, GET Saga
покажет NEEDS_ATTENTION/courier_missing_after_preparation. После проверки ситуации:

```bash
curl -sS -X POST http://localhost:8081/restaurants/1/orders/1/retry -H 'X-Dev-User-ID: 10'
# Затем назначение через Delivery API; Order продолжит готовиться без отката еды.
```

## Native запуск

Экспортируйте нужные переменные `.env`; PostgreSQL, Redpanda и миграции должны быть
готовы. Отдельные процессы запускаются в разных shell (Catalog/Notifications — по README):

```bash
DATABASE_URL="$ORDERS_DATABASE_URL" HTTP_ADDR=:8081 go run ./services/orders/cmd/api
DATABASE_URL="$ORDERS_DATABASE_URL" go run ./services/orders/cmd/outbox
DATABASE_URL="$ORDERS_DATABASE_URL" go run ./services/orders/cmd/consumer
DATABASE_URL="$ORDERS_DATABASE_URL" go run ./services/orders/cmd/saga
DATABASE_URL="$DELIVERY_DATABASE_URL" HTTP_ADDR=:8085 go run ./services/delivery/cmd/api
DATABASE_URL="$DELIVERY_DATABASE_URL" OUTBOX_HTTP_ADDR=:8088 go run ./services/delivery/cmd/outbox
DATABASE_URL="$DELIVERY_DATABASE_URL" go run ./services/delivery/cmd/consumer
```

В monorepo сохраняются существующие cmd/consumer и cmd/outbox вместо дублирующего
cmd/worker. У Delivery application остаётся плоская структура с конкретными use
cases/ports; пустые пакеты usecases/ports не создаются. GOWORK=off go build ./...
из каждого модуля проверяет независимую сборку. Для архива без Git metadata можно
добавить -buildvcs=false. Native consumers нужно перезапускать supervisor'ом после
фатальной ошибки; Compose делает это через restart: unless-stopped.

## Тесты

```bash
go test -race -count=1 ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/...
go vet ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/...
sh scripts/test-integration.sh
python3 scripts/verify-fulfillment.py
python3 scripts/verify-saga.py
```

Без TEST_DATABASE_URL PostgreSQL tests SKIP; без TEST_KAFKA_BROKERS Kafka tests
SKIP. test-integration.sh поднимает выделенные PostgreSQL/Redpanda автоматически.
Compose e2e используют проекты food-fulfillment-e2e и food-saga-e2e, общие тестовые
порты 25432/25433/28080/28081/28083/28085/39092; запускайте их последовательно.
Они удаляют только собственные тестовые volumes в finally. Не используйте эти
имена для рабочих проектов.

verify-fulfillment проверяет полный POS-цикл с остановкой брокера, повторами и
двумя Outbox/Saga workers. verify-saga дополнительно проверяет недоступность
Delivery consumer, перезапуск Orders посреди Saga, сохранённый retry, запрет
готовки без назначения, decline после готовки, эскалацию/resume, timeout с
компенсацией до готовки, отказ ресторана после принятия, offline курьера,
отмену клиентом и повтор команд. Unit/SQL/Kafka tests также проверяют сбой после
COMMIT до ACK, неправильный порядок, stale/conflicting messages, конкуренцию,
rollback при отказе Outbox и миграции исторических данных.

## Диагностика

```bash
docker compose --env-file .env -f deploy/compose.yaml logs -f \
  orders-saga orders-delivery-consumer delivery-consumer outbox delivery-outbox
docker compose --env-file .env -f deploy/compose.yaml exec postgres \
  psql -U orders_service -d orders -c \
  'SELECT order_id,state,version,delivery_id,delivery_version,attempts,deadline,next_attempt_at,reason FROM order_sagas ORDER BY order_id;'
docker compose --env-file .env -f deploy/compose.yaml exec postgres \
  psql -U orders_service -d orders -c \
  'SELECT event_id,stream,aggregate_id,aggregate_version,status,attempts,last_error FROM outbox_events ORDER BY created_at;'
# После устранения причины DEAD (нужен настоящий UUID):
docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps outbox -requeue MESSAGE_UUID
docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps delivery-outbox -requeue EVENT_UUID
```

Не исправляйте соседнюю БД SQL UPDATE и не пропускайте offset ради продвижения
partition. Исправьте причину, затем выполните replay/retry и перезапустите consumer.
NEEDS_ATTENTION хранит причину; API/worker не скрывают её автоматической отменой.
