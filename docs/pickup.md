# PICKUP + DELIVERY: API и запуск

Модель и диаграммы: [ADR 0005](adr/0005-pickup-and-delivery.md).
DELIVERY продолжает использовать [Fulfillment Saga](fulfillment.md).

## Обновление стенда

Сделайте backup. Остановите старые writers/consumers, включая Catalog, как описано
в [fulfillment.md](fulfillment.md#запуск), примените Catalog 000003, Orders 000005,
Delivery 000003, Notifications 000004 и пересоберите все процессы. Compose уже
монтирует каталоги миграций; новых сервисов и переменных окружения нет.

```bash
docker compose --env-file .env -f deploy/compose.yaml stop \
  catalog orders outbox orders-delivery-consumer orders-saga \
  delivery delivery-outbox delivery-consumer notifications
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-orders
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-delivery
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-notifications
docker compose --env-file .env -f deploy/compose.yaml up --build -d --wait
```

Для нового окружения сначала скопируйте `.env.example` в `.env`. Существующие
секреты и volumes сохраняйте. Исторические заказы остаются DELIVERY с прежними
статусами/оплатой и явно неизвестным адресом; migration не создаёт им PICKUP-фактов.
Down после появления новых данных блокируется, предпочтителен roll-forward.

Dev identity включается только явным opt-in; APP_ENV=production запрещает её.
Примеры используют server-owned `DEV_RESTAURANT_GRANTS=10:1`. Запрос с произвольным
restaurant_id в заголовке не предоставляет прав. Production identity provider,
как и до расширения, ещё не подключён: без dev/test opt-in API возвращает 401.
Административный Catalog API пока без авторизации и не предназначен для публичного
production-доступа; настройка адреса использует его существующую границу доверия.

## Создание

Сначала настройте настоящий адрес ресторана (старым ресторанам он не выдумывается):

```bash
curl -i -X PATCH http://localhost:8080/restaurants/1 \
  -H 'Content-Type: application/json' \
  -d '{"address":"Душанбе, улица ресторана 1"}'
```

Нужны существующие доступные блюда Catalog и открытый ресторан. Подставьте ID и
актуальную цену блюда:

```bash
curl -i -X POST http://localhost:8081/orders \
  -H 'Content-Type: application/json' \
  -H 'X-Dev-Customer-ID: 7' -H 'Idempotency-Key: pickup-demo-1' \
  -d '{"restaurant_id":1,"fulfillment_type":"PICKUP","expected_total_minor":13000,"items":[{"menu_item_id":100,"quantity":2}]}'
```

201 + Location; `fulfillment_type=PICKUP`, `payment_method=POS_AT_PICKUP`,
`payment_status=UNPAID`, `status=WAITING_RESTAURANT`, `delivery_fee_minor=0`.
`pickup_address` — снимок из Catalog. `currency` можно опустить (TJS), другая валюта
запрещена. Клиентский pickup_address, payment fields и delivery_fee_minor запрещены.

Для DELIVERY то же API требует `"fulfillment_type":"DELIVERY"` и непустой
`"delivery_address":"адрес клиента"`, delivery_latitude и delivery_longitude.
Они сохраняются в Order и задаче Delivery.
Для PICKUP сбор 0 и POS-сумма равна сумме блюд; для DELIVERY применяется
[серверный тариф](pricing.md). Клиент не задаёт стоимость доставки. Для PICKUP непустой delivery_address запрещён.

Idempotency-Key scoped по customer_id и учитывает fulfillment_type/адрес DELIVERY.
Replay возвращает исходный результат создания, даже после выдачи, изменения меню,
адреса ресторана или отказа Catalog. Другой payload с прежним ключом — 409. Точный replay исторического запроса
без fulfillment_type поддерживается только при уже сохранённом старом результате;
создать новый заказ таким запросом нельзя.
Способ получения и адреса неизменяемы с создания; endpoint их изменения отсутствует.

## Приготовление и выдача

Подставляйте реальный order_id вместо 42:

```bash
curl -i -X POST http://localhost:8081/restaurants/1/orders/42/accept -H 'X-Dev-User-ID: 10'
curl -i -X POST http://localhost:8081/restaurants/1/orders/42/prepare -H 'X-Dev-User-ID: 10'
curl -i -X POST http://localhost:8081/restaurants/1/orders/42/ready -H 'X-Dev-User-ID: 10'
```

PICKUP: WAITING_RESTAURANT → ACCEPTED → PREPARING → READY_FOR_PICKUP.
Курьер, Delivery и Saga не нужны. Для DELIVERY сохраняется gate назначения курьера.
`OrderReadyForPickup` содержит fulfillment_type: Notifications сохраняет PENDING
intent готовности самовывоза. Фактической отправки SMS/email в текущем проекте нет.

Клиент готового заказа получает код под своей identity, с пустым телом:

```bash
curl -i -X POST http://localhost:8081/orders/42/pickup-code -H 'X-Dev-Customer-ID: 7'
```

200, `Cache-Control: no-store`, JSON `{"pickup_code":"32 hex-символа","expires_at":"..."}`.
Это 128-битный секрет, пригодный для QR. Покажите его сотруднику при получении.
Повтор этой операции **ротирует** код и сбрасывает лимит попыток; прежний код
перестаёт действовать. Срок — 24 часа. Исходный код сервер не хранит и восстановить
не может. Клиент другого заказа не получает доступ; ресторанный список код не содержит.

Сотрудник сначала проверяет предъявленный код:

```http
POST /restaurants/1/orders/42/verify-pickup
Content-Type: application/json
X-Dev-User-ID: 10

{"pickup_code":"код из ответа клиенту"}
```

200 возвращает готовый заказ с UNPAID; проверка сама не подтверждает оплату,
не меняет статус/версию и не создаёт событие. Она использует тот же сохраняемый
лимит попыток, что и выдача. Затем сотрудник принимает оплату физическим POS и
подтверждает выдачу. Финальная операция повторно проверяет код под блокировкой:
ротация или истечение между двумя запросами не обходят проверку. Передавайте код только в JSON-теле, не в URL:

```http
POST /restaurants/1/orders/42/complete-pickup
Content-Type: application/json
X-Dev-User-ID: 10

{
  "pickup_code": "код из ответа клиенту",
  "payment_confirmed": true,
  "receipt_reference": "необязательный номер POS-чека"
}
```

Успех — 200, `PICKED_UP_BY_CUSTOMER` и `REPORTED_COLLECTED` вместе.
Уполномоченный сотрудник берётся из identity; сумма и валюта — из сохранённого
заказа, время — с сервера. В Orders остаётся POS audit с ID сотрудника, суммой,
валютой, методом, временем и чеком. Это заявление об оплате, не банковская проверка.

Повтор с тем же кодом/сотрудником/чеком и `payment_confirmed=true` возвращает прежний
результат без дубликата POS или Outbox, также после рестарта и истечения кода.
Другой чек/сотрудник — 409. На повторе код всё равно проверяется. Новый код после
завершения выдавать нельзя.

| Операция/ошибка | HTTP |
|---|---|
| POST /orders без fulfillment_type или DELIVERY без адреса | 400 |
| PICKUP при ненастроенном адресе ресторана | 409 |
| POST /orders/{id}/pickup-code для неготового/терминального заказа | 409 |
| Клиент запрашивает код чужого заказа | 404 |
| verify-pickup для неготового/выданного заказа | 409 |
| complete-pickup без прав соответствующего ресторана | 403 |
| complete-pickup для заказа другого ресторана при правах только на свой | 404 |
| Неверный/истёкший код | 403 |
| После 5 неверных кодов: блокировка на 15 минут | 429 |
| Неготовый заказ, DELIVERY вместо PICKUP, payment_confirmed=false | 409 |
| GET /restaurants/{restaurant}/orders/{id}/saga для PICKUP | 404 |
| POST /restaurants/{restaurant}/orders/{id}/retry для PICKUP | 409 |

Неверный формат JSON/кода/чека — 400. Коды не включаются в события и логи.
Успешная выдача, POS и Outbox — одна транзакция; отрицательная проверка кода
коммитит лишь счётчик попыток. READY/PREPARING нельзя отменить. Отмена PICKUP через
POST /orders/{id}/cancel или ресторанный reject до готовки локальна и не публикует
CancelDelivery. Конкурентные операции сериализует Order row lock.

## Проверки

```bash
# PostgreSQL 18 + Redpanda; тестовые схемы создаются и удаляются автоматически:
./scripts/test-integration.sh
# Полный Compose, реальные binaries, Outbox/Inbox и оба процесса получения:
python3 scripts/verify-pickup.py
# Существующая Saga, recovery, deadlines, отмена и POS доставки:
python3 scripts/verify-saga.py
```

Скрипты используют отдельные Compose-проекты и удаляют только их тестовые volumes.
Они занимают одни и те же тестовые порты, поэтому запускайте их последовательно.

Настройки доступности и тарифы этапа 13: [Delivery Zones & Pricing](pricing.md).
