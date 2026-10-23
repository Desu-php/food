# Food Delivery — Auth, Catalog, Orders, Notifications и Delivery

В MVP поддерживаются DELIVERY и PICKUP. Клиент платит при получении физическим
POS-терминалом курьера либо ресторана. Самовывоз: [API и запуск](docs/pickup.md),
[ADR 0005](docs/adr/0005-pickup-and-delivery.md).
Онлайн-оплаты и Payment Service нет. Полный цикл и запуск:
[Restaurant Fulfillment + Delivery](docs/fulfillment.md), [ADR 0004 — Fulfillment Saga](docs/adr/0004-fulfillment-saga.md).

Этап 18: независимый `services/auth` со своей PostgreSQL, собственными Login/Register
API, Argon2id, короткими ES256 JWT/JWKS и HttpOnly refresh rotation. Права и связи
хранятся в Auth; бизнес-API проверяют актуальную сессию и сохраняют resource-level
authorization. Login не требует редиректов. Frontend в этом этапе не меняется.
[Контракт Quasar, запуск, ключи и миграция](docs/auth-service.md),
[ADR 0013](docs/adr/0013-independent-auth-service.md).

Новый режим включается overlay `deploy/compose.auth.yaml` после импорта и проверки
привязок. OIDC/Keycloak и старый registry сохранены для контролируемого перехода;
два overlay одновременно не подключаются. Публичная production-регистрация пока
создаёт PENDING_VERIFICATION: без почтового канала подтверждение/recovery недоступны,
а вход такого customer закрыт. Управляемые аккаунты создаются через `authctl`.

```sh
# После подготовки .local/auth/keys.json, csrf.key, service.key и новых DB passwords:
docker compose --env-file .env -f deploy/compose.yaml -f deploy/compose.auth.yaml up -d --build
# Сквозная проверка нового Auth в отдельном disposable deployment:
python3 scripts/verify-auth-service.py
```

Этап 9: OIDC/JWKS, resource authorization, durable consumer quarantine и CI.
Настройка и восстановление: [Security & Reliability](docs/security-reliability.md),
[ADR 0006](docs/adr/0006-security-and-consumer-quarantine.md).

Этап 10: автоматический поиск ближайшего доступного курьера внутри Delivery,
Redis GEO с TTL, PostgreSQL lease/cooldown и ручной fallback.
[API, настройки, rollout и восстановление](docs/automatic-dispatch.md),
[ADR 0007](docs/adr/0007-automatic-dispatch-and-redis-geo.md).

Этап 11: предложения курьерам с подтверждением, TTL 30 секунд, PostgreSQL
резервациями и восстановлением после рестарта. Автоматический CourierAssigned
появляется после принятия; приготовление ждёт продолжения прежней Saga.
[API, запуск и конкурентность](docs/courier-offers.md),
[ADR 0008](docs/adr/0008-courier-offer-and-acceptance.md).

Этап 14: регистрация устройств через OIDC, transactional notification intents,
отдельный Push Worker и Firebase FCM (официальный Go SDK, FID, ADC), retries,
per-device история и TTL предложений. Локально используется fake без credentials.
[API устройств, Firebase, запуск и гарантии](docs/push-notifications.md),
[ADR 0011](docs/adr/0011-push-notifications-and-fcm.md).

Этап 16: `GET /v1/auth/me` в Orders, профиль из серверного Principal, общий
API audience для SPA, явное различение access/ID token и CORS по allowlist.
[Контракт frontend, регистрация staff и запуск Keycloak по HTTPS](docs/auth-profile-oidc.md),
[ADR 0012](docs/adr/0012-auth-profile-and-oidc.md).

В сохранённом legacy режиме Stage 16 frontend выполняет Code + PKCE S256 с public client `food-pwa`
и передаёт `Authorization: Bearer <access_token>`. Profile возвращает
`{"user_id":10,"roles":["restaurant_staff"],"restaurant_ids":[1,2],"permissions":[]}`,
всегда с `Cache-Control: no-store`; 401 означает отсутствующий/недействительный
токен, 403 — отсутствие привязки Subject в серверном реестре. Выбор ресторана
в UI не заменяет серверные grants. Имена/email/токены в профиль не включаются.

Для реального IdP задайте `DEV_AUTH_ENABLED=false`, `OIDC_ISSUER`, `OIDC_JWKS_URL`,
`OIDC_AUDIENCE=food-api`, `AUTH_IDENTITIES_FILE` и точные `CORS_ALLOWED_ORIGINS`.
Compose наследует общий audience во всех четырёх API, если service override пуст.
Issuer/JWKS требуют HTTPS даже в development; роли и business IDs берутся из
deployment registry. Не используйте frontend Client ID вместо API audience.
Локальный Keycloak запускается опциональным `deploy/compose.oidc.yaml`; сначала
подготовьте TLS и привязки по инструкции выше. Frontend `.env` контракт:

```dotenv
OIDC_AUTHORITY=https://identity.food.test:8443/realms/food
OIDC_CLIENT_ID=food-pwa
AUTH_PROFILE_URL=http://localhost:8081/v1/auth/me
```

Способ экспорта этих имён в Quasar следует сверить в отдельном frontend-проекте;
он в этом репозитории отсутствует. Пример public client использует callback
`http://localhost:9000/auth/callback` и post-logout `http://localhost:9000/`.
Автопроверка с настоящим HTTPS Keycloak и временными credentials:
`python3 scripts/verify-auth.py`; browser/PWA smoke выполняется в отдельном frontend.

Monorepo для сервисов доставки еды. Реализованы Catalog с ресторанами, категориями и блюдами и Orders с созданием,
чтением, отменой и постоянной идемпотентностью. У каждого сервиса своя база PostgreSQL,
Clean Architecture, стандартный net/http, pgx/v5, структурированные JSON-логи slog,
таймауты и graceful shutdown. Orders публикует события через Transactional Outbox
в Redpanda; Notifications использует транзакционный Inbox и отдельный push worker.
Go 1.27.2 закреплён в go.work и модулях.

Этап 12: клиентское отслеживание курьера через SSE в Delivery API, GPS с accuracy,
RecordedAt/ReceivedAt и TTL, доверенная привязка владельца через CreateDelivery.
Точные координаты доступны только после PICKED_UP, terminal delivery закрывает поток.
[API, запуск, ограничения и sequence diagram](docs/tracking.md),
[ADR 0009](docs/adr/0009-real-time-courier-tracking.md).

## Структура

```text
services/auth/
  cmd/api/                 независимый Auth API
  cmd/authctl/             offline administration, import и key generation
  internal/domain/         User, статусы и binding/password invariants
  internal/application/    auth/session/admin use cases и порты
  internal/adapters/       HTTP, PostgreSQL, Argon2id и JWT
  internal/bootstrap/      конфигурация, DI, listeners и cleanup/revocation workers
  migrations/              отдельная Auth DB schema
services/catalog/
  cmd/api/                 точка входа
  internal/domain/         Restaurant, MenuCategory, MenuItem, инварианты
  internal/application/    use cases и порты RestaurantRepository / MenuRepository
  internal/adapters/http/  маршруты, DTO, валидация HTTP, JSON-ошибки
  internal/adapters/postgres/ SQL-реализация порта
  internal/bootstrap/      env-конфигурация, pool, сборка зависимостей, lifecycle
  migrations/              версионированные up/down SQL
  go.mod, go.sum           независимый Go-модуль
  Dockerfile               multi-stage сборка
services/orders/
  cmd/api/, cmd/outbox/     HTTP API и отдельный Outbox Worker
  cmd/consumer/, cmd/saga/  результаты Delivery и persisted Saga scheduler
  internal/domain/         Order aggregate, OrderItem, суммы, state machine
  internal/application/    CreateOrder / GetOrder / CancelOrder, порты
  internal/adapters/http/  HTTP DTO, dev/test identity, JSON-ошибки
  internal/adapters/catalog/ пакетный HTTP quote client
  internal/adapters/postgres/ транзакции, persistent idempotency, row locks
  internal/bootstrap/      env, DI, lifecycle
  migrations/              orders, order_items, idempotency_keys, outbox_events
  go.mod, go.sum, Dockerfile независимая сборка
services/notifications/
  cmd/consumer/            Kafka consumer, devices/OIDC API, health, replay CLI
  cmd/push-worker/         отдельный процесс отправки FCM
  internal/domain/         Notification, DeviceRegistration, Delivery
  internal/application/    consumer, devices, templates, PushProvider/worker ports
  internal/adapters/       Kafka, PostgreSQL, HTTP, FCM, private Delivery client
  internal/bootstrap/      env, DI, lifecycle
  migrations/              processed_events, notifications, failed_messages
contracts/                 отдельный Go-модуль публичных JSON-контрактов
scripts/                   интеграционные и полные Compose-проверки
services/delivery/        Domain, Application, HTTP/PostgreSQL/Kafka/Redis GEO, Bootstrap; API/consumer/outbox + Dispatch/Offer Expiry workers
contracts/delivery/commands/v1/  CreateDelivery / CancelDelivery, отдельные от events
deploy/compose.yaml       два PostgreSQL, четыре сервисные БД, Redpanda, Redis, миграции и workers
docs/adr/0001-orders-catalog-integration.md  принятые решения и sequence diagram
docs/architecture.md      решения и границы слоёв
```

Сервис можно собирать самостоятельно; go.work связывает модули для разработки в
monorepo. Корневого go.mod нет. Поэтому из корня используйте
`go test ./platform/... ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/...`, а не `go test ./...`.

Пути Go-модулей соответствуют репозиторию `github.com/Desu-php/food`:
общие модули — `github.com/Desu-php/food/contracts` и
`github.com/Desu-php/food/platform`, сервисы —
`github.com/Desu-php/food/services/<service>`. Локальные `replace` в сервисных
`go.mod` позволяют собирать сервисы отдельно с `GOWORK=off` из этого checkout.

## Запуск через Docker Compose

Требуются Docker Engine и Compose v2. Для WSL включите интеграцию дистрибутива в
Docker Desktop. Команды ниже выполняются из корня репозитория:

```bash
# Для нового окружения (не перезаписывайте существующие секреты):
cp .env.example .env
docker compose --env-file .env -f deploy/compose.yaml up --build -d
docker compose --env-file .env -f deploy/compose.yaml ps
docker compose --env-file .env -f deploy/compose.yaml logs -f catalog
```

Для уже настроенного локального Keycloak подключайте также OIDC overlay:

```bash
docker compose --env-file .env -f deploy/compose.yaml -f deploy/compose.oidc.yaml up -d --wait
```

Если в `.env` задан `COMPOSE_FILE=deploy/compose.yaml:deploy/compose.oidc.yaml`,
достаточно `docker compose up -d --wait`. Явный `-f deploy/compose.yaml` перекрывает
`COMPOSE_FILE` и исключает overlay: API теряют монтирование реестра и CA, а Keycloak
показывается как orphan. При `cannot open AUTH_IDENTITIES_FILE` проверьте эту
команду запуска. Подготовка OIDC описана в [инструкции](docs/auth-profile-oidc.md).

Для существующей `.env` добавьте CATALOG_DB_PASSWORD, ORDERS_DB_PASSWORD,
NOTIFICATIONS_POSTGRES_PASSWORD, NOTIFICATIONS_DB_PASSWORD и DELIVERY_DB_PASSWORD из
нового примера, задав собственные URL-safe пароли. Для native Catalog также
обновите DATABASE_URL на роль catalog_service (см. пример). Старый POSTGRES_PASSWORD не
меняйте: он должен соответствовать сохранённому volume. Новые переменные
APP_ENV=development и DEV_AUTH_ENABLED=true нужны для локального Orders API.

PostgreSQL сначала проходит healthcheck. `init-databases` создаёт отдельные роли
и базы, ограничивает межбазовый доступ и переводит старые Catalog tables на
сервисную роль. `migrate` и `migrate-orders` применяют миграции независимо.
Catalog и Orders стартуют после миграций; Orders также ожидает готовность Catalog. В первой
миграции сохраняются два исходных ресторана: Pizza House и Burger Corner.
Данные сохраняются в Docker volume между перезапусками. `is_open` — boolean,
ID генерируется PostgreSQL identity, timestamps хранятся в TIMESTAMPTZ.

Используется PostgreSQL 18 (`postgres:18-alpine`): major-версия закреплена,
исправления ветки 18 доступны при обновлении образа. Для загрузки свежего образа:
`docker compose --env-file .env -f deploy/compose.yaml pull postgres`.
Volume монтируется в `/var/lib/postgresql`; данные PostgreSQL 18 хранятся в
`/var/lib/postgresql/18/docker` согласно настройкам официального образа.

Если окружение уже запускалось с PostgreSQL 16, существующий `catalog_data`
нельзя напрямую использовать как базу PostgreSQL 18. До запуска новой версии
сохраните резервную копию и перенесите данные через dump/restore в новый volume
либо выполните `pg_upgrade`. SQL-миграции приложения не обновляют формат кластера.
Не удаляйте старый volume до проверки переноса данных.

`.env` не коммитится. Пример пароля предназначен для локальной разработки.
Если меняете пароль, согласуйте DATABASE_URL для запуска без Docker. Символы,
значимые для URL, в пароле нужно URL-кодировать при формировании DATABASE_URL;
Compose-пример рассчитан на пароль из URL-безопасных символов.
В Compose PostgreSQL доступен с хоста только на 127.0.0.1.

Остановка с ожиданием текущих HTTP-запросов:

```bash
docker compose --env-file .env -f deploy/compose.yaml stop orders catalog notifications outbox
```

## Миграции

Используется отдельный CLI migrate v4.18.3, не зависимость Go-приложения.
Состояние миграций хранится в PostgreSQL (`schema_migrations`). Приложение само не
меняет схему. Для повторного применения новых миграций (у Orders своя schema_migrations):

```bash
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-orders
```

Добавляйте пары `000003_description.up.sql` / `000003_description.down.sql`.
Не редактируйте уже применённые миграции. Проверяйте up и down на тестовой БД.
При миграции существующего окружения выполняйте изменения схемы совместимо с
работающей версией сервиса и перезапускайте/пересобирайте Catalog после обновления.

Для локального отката последней миграции адреса ресторана (разрешён только пока
адреса не заполнены; сохраняет рестораны):

```bash
docker compose --env-file .env -f deploy/compose.yaml stop catalog
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate \
  -path=/migrations \
  -database='postgres://catalog_service:catalog_service_local_password@postgres:5432/catalog?sslmode=disable' \
  down 1
```

URL в примере отката должен соответствовать вашей `.env`. После отката можно
снова выполнить `run --rm migrate` и `up -d catalog`.

## Запуск Go без Docker

Нужна работающая PostgreSQL с применёнными миграциями. Например, сначала запустите
только БД и миграции в Compose:

```bash
docker compose --env-file .env -f deploy/compose.yaml up -d postgres
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate
set -a
. ./.env
set +a
go run ./services/catalog/cmd/api
# В другом shell экспортируйте .env и запустите:
# DATABASE_URL="$ORDERS_DATABASE_URL" HTTP_ADDR=:8081 go run ./services/orders/cmd/api
```

Либо из `services/catalog`: `go run ./cmd/api`. Не запускайте отдельный `.go`-файл.
Приложение не загружает `.env` автоматически: оно читает переменные окружения.
Не запускайте локальный процесс и Compose Catalog одновременно на одном порту.
Для проверки graceful shutdown соберите бинарник и отправьте ему SIGTERM:
`go build -o /tmp/catalog ./services/catalog/cmd/api`.

Основные настройки (полный пример — `.env.example`):

| Переменная | По умолчанию | Назначение |
|---|---|---|
| DATABASE_URL | обязательна | строка подключения pgx |
| HTTP_ADDR | :8080 (Catalog), :8081 (Orders) | адрес публичного HTTP-сервера |
| INTERNAL_HTTP_ADDR | 127.0.0.1:8082 | приватный quote listener Catalog |
| CATALOG_BASE_URL | http://localhost:8082 | Orders: адрес внутреннего Catalog |
| CATALOG_TIMEOUT | 2s | Orders: HTTP timeout, меньше REQUEST_TIMEOUT |
| APP_ENV | production | Все API: production/development/test |
| DEV_AUTH_ENABLED | false | Все API: dev/test identity; production запрещён |
| LOG_LEVEL | INFO | DEBUG, INFO, WARN или ERROR |
| DB_MAX_CONNS / DB_MIN_CONNS | 10 / 0 | границы pool |
| DB_CONNECT_TIMEOUT | 5s | подключение и проверка БД при старте |
| DB_MAX_CONN_LIFETIME / DB_MAX_CONN_IDLE_TIME | 1h / 5m | жизненный цикл соединений |
| HTTP_READ_HEADER_TIMEOUT / HTTP_READ_TIMEOUT | 5s / 10s | чтение заголовков и запроса |
| HTTP_WRITE_TIMEOUT / HTTP_IDLE_TIMEOUT | 15s / 60s | запись и keep-alive |
| REQUEST_TIMEOUT | 5s | deadline use case и SQL-запросов |
| SHUTDOWN_TIMEOUT | 10s | ожидание текущих запросов |

Положительные duration задаются как `5s`, `1m`. Write timeout должен быть больше
request timeout. SIGINT/SIGTERM прекращают приём новых соединений, ожидают текущие
запросы и затем закрывают pool. Если ожидание превышено, соединения закрываются
принудительно и процесс возвращает ошибку.

## REST API

Все ответы с телом — JSON; успешный DELETE возвращает 204 без тела. Ошибки: `{"error":"message"}`. Внутренние SQL/DB-ошибки не
выдаются клиенту. POST/PATCH требуют `Content-Type: application/json`.
Максимальный размер тела — 64 KiB. Неизвестные поля, server-owned поля, null в
изменяемых полях, неверные типы и несколько JSON-значений отклоняются.

| Метод и путь | Назначение | Успех |
|---|---|---|
| GET /health | liveness, без запроса к БД | 200 |
| GET /ready | проверяет доступность БД и схемы ресторанов/меню | 200 или 503 |
| GET /restaurants?limit=20&offset=0 | список по возрастанию ID | 200, JSON-массив |
| GET /restaurants/{id} | ресторан по положительному ID | 200 |
| POST /restaurants | создание | 201 + Location |
| PATCH /restaurants/{id} | частичное обновление | 200 |

Пагинация: `limit` от 1 до 100 (по умолчанию 20), `offset >= 0` (по умолчанию 0).
Пустой список возвращается как `[]`. Формат массива прежний, добавлены timestamps.
Имя обрезается по краям и должно содержать 1–200 Unicode-символов.
При создании отсутствие `is_open` означает false. PATCH меняет только переданные
поля; `false` отличается от отсутствующего поля. Пустой PATCH запрещён.
ID, created_at и updated_at контролируются сервером. В отличие от старого учебного
POST, переданный клиентом ID теперь отклоняется, а не игнорируется.

Примеры:

```bash
curl -i http://localhost:8080/health
curl -i http://localhost:8080/ready
curl -i 'http://localhost:8080/restaurants?limit=10&offset=0'
curl -i http://localhost:8080/restaurants/1
curl -i -X POST http://localhost:8080/restaurants -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"name":"New Cafe","is_open":true}'
curl -i -X PATCH http://localhost:8080/restaurants/3 -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"is_open":false}'
```

Ожидаемый ответ создания на свежей БД:

```json
{"id":3,"name":"New Cafe","is_open":true,"created_at":"...","updated_at":"..."}
```

Ошибки: 400 — валидация/JSON, 404 — нет ресторана, 405 — неверный метод,
413 — слишком большое тело, 415 — неверный Content-Type,
500 — внутренняя ошибка, 504 — превышен deadline.

## Управление меню

Миграция `000002_menu` добавляет `menu_categories` и `menu_items` без изменения
существующих ресторанов. При обновлении уже запущенного окружения сначала
примените миграции, затем пересоберите сервис:

```bash
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate
docker compose --env-file .env -f deploy/compose.yaml up --build -d catalog
```

| Доступ | Метод и путь | Успех |
|---|---|---|
| Публичный | GET /restaurants/{restaurantId}/menu | 200 |
| Административный | POST /restaurants/{restaurantId}/categories | 201 |
| Административный | PATCH /restaurants/{restaurantId}/categories/{categoryId} | 200 |
| Административный | POST /restaurants/{restaurantId}/items | 201 |
| Административный | PATCH /restaurants/{restaurantId}/items/{itemId} | 200 |
| Административный | DELETE /restaurants/{restaurantId}/items/{itemId} | 204 |

Публичное чтение доступно без аутентификации. Каждая мутация проверяет principal:
создание ресторана требует admin, изменения существующего ресторана/меню — admin
либо restaurant_staff с серверным grant именно этого ресторана. В локальном
примере DEV_ADMIN_IDS=1 и X-Dev-User-ID: 1; production использует Bearer JWT.

Категории упорядочены по `(sort_order, id)`, блюда внутри категории — по `id`.
Отдельного sort_order у блюда сейчас нет; ID обеспечивает стабильный порядок.
Имена категорий/блюд не обязаны быть уникальными: их идентичность определяет ID.
Пустые категории присутствуют с `items: []`; пустое меню — с `categories: []`.
Недоступные блюда остаются в ответе с `is_available: false`, удалённые исключены.

Правила входных данных:

- Имя: 1–200 Unicode-символов после удаления пробелов по краям; NUL запрещён.
- `sort_order`: целое int32 от 0 до 2147483647; при создании по умолчанию 0.
- `category_id`: обязательный положительный int64; категория должна принадлежать
  ресторану из URL. При PATCH можно перемещать блюдо только между его категориями.
- `price_minor`: обязательное положительное int64. Только целые минимальные
  денежные единицы: `6500` означает 65.00 TJS. Дробные JSON-числа отклоняются.
- `currency`: обязательная строка `TJS`, другие значения пока не поддерживаются.
- `description`: до 2000 Unicode-символов, NUL запрещён; по умолчанию пустая строка.
- `is_available`: boolean, по умолчанию true при создании.
- ID, restaurant_id, timestamps и deleted_at задаёт сервер; такие входные поля
  отклоняются. `category_id` — редактируемое поле блюда, а `restaurant_id` — нет.

PATCH меняет только переданные поля. `false` отключает доступность, `sort_order: 0`
перемещает категорию, `description: ""` очищает описание. `price_minor: 0` и
`name: ""` запрещены бизнес-правилами. `null`, пустой PATCH, неизвестные поля,
неверные типы и лишние JSON-значения возвращают 400. Ограничения Content-Type и
64 KiB такие же, как у ресторанов.

Примеры на новой БД (подставьте фактические ID из ответов):

```bash
# Создать категорию; ответ 201 содержит её ID и timestamps.
curl -i -X POST http://localhost:8080/restaurants/1/categories -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"name":"Пицца","sort_order":1}'

# Изменить порядок категории.
curl -i -X PATCH http://localhost:8080/restaurants/1/categories/1 -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"sort_order":0}'

# Создать блюдо; ответ 201 содержит все его поля и timestamps.
curl -i -X POST http://localhost:8080/restaurants/1/items -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"category_id":1,"name":"Маргарита","description":"Сыр, томаты","price_minor":6500,"currency":"TJS","is_available":true}'

# Частично изменить цену и доступность, сохранив остальные поля.
curl -i -X PATCH http://localhost:8080/restaurants/1/items/1 -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"price_minor":7000,"is_available":false}'

curl -i http://localhost:8080/restaurants/1/menu

# Soft delete: 204, строка в БД сохраняется, публичное меню её больше не возвращает.
curl -i -X DELETE http://localhost:8080/restaurants/1/items/1 -H 'X-Dev-User-ID: 1'
```

Пример публичного ответа:

```json
{
  "restaurant_id": 1,
  "categories": [
    {
      "id": 1,
      "name": "Пицца",
      "sort_order": 0,
      "items": [
        {
          "id": 1,
          "name": "Маргарита",
          "description": "Сыр, томаты",
          "price_minor": 7000,
          "currency": "TJS",
          "is_available": false
        }
      ]
    }
  ]
}
```

Ошибка принадлежности категории другому ресторану — 400. Отсутствие ресторана,
категории или блюда — 404. PATCH/DELETE ресурса через URL чужого ресторана тоже
возвращают 404. Повторный DELETE и PATCH удалённого блюда возвращают 404.
Полное меню загружается одним SQL-запросом, независимо от количества категорий:
LEFT JOIN, один MVCC snapshot и группировка результата в Go. GET не выполняет
отдельный запрос на каждую категорию.

## Тестирование

Unit- и HTTP-тесты не требуют базы:

```bash
go test -race ./services/catalog/... ./services/orders/...
go vet ./services/catalog/... ./services/orders/...
go build ./services/catalog/cmd/api
```

Последняя команда создаёт бинарник `api` в текущем каталоге; для временной сборки
используйте `go build -o /tmp/catalog ./services/catalog/cmd/api`.

В окружении с недоступной Git-метаинформацией добавьте к сборке `-buildvcs=false`.
Если стандартный кеш Go недоступен для записи, задайте `GOCACHE=/tmp/food-go-build`.

Интеграционные тесты пропускаются без TEST_DATABASE_URL. Создайте отдельную БД:

```bash
docker compose --env-file .env -f deploy/compose.yaml exec postgres \
  createdb -U catalog catalog_test
TEST_DATABASE_URL='postgres://catalog:catalog_local_password@localhost:5432/catalog_test?sslmode=disable' \
  go test -race -count=1 -v ./services/catalog/... ./services/orders/...
```

Пользователь тестовой БД должен иметь право создавать схемы. Каждый тест создаёт
изолированную схему, применяет необходимые настоящие SQL-миграции и удаляет схему после теста.
Не указывайте production database. Проверяются CRUD, пагинация, ограничения БД,
параметризованные запросы, context cancellation, откат миграции, graceful shutdown (ожидание текущего запроса и принудительная остановка), конкурентная
генерация ID и обновление разных полей одной строки. Для меню дополнительно
проверяются составной FK при прямых SQL-записях, soft delete, порядок, PATCH с
нулевыми значениями и лимит в один SQL-запрос для полного меню.

## Добавление функциональности

Начинайте с доменных правил и use case. Если требуется persistence, расширяйте
application-порт, PostgreSQL-адаптер и SQL-миграцию. Затем добавляйте HTTP DTO и
маршрут. Зависимости соединяются конструкторами в bootstrap. Дополняйте unit,
HTTP и интеграционные тесты и документацию. Не добавляйте слои, которые лишь
пересылают вызовы, и не переносите бизнес-правила в handlers или SQL-адаптеры.

Production использует проверенные JWT и серверные resource grants; новый режим
получает их из Auth, legacy — из OIDC registry. Compose предназначен для
локальной разработки; боевое окружение отдельно задаёт identity registry, secrets,
TLS и доступ к БД. См. [настройку OIDC](docs/security-reliability.md).


## Order Service: запуск и API

`docker compose --env-file .env -f deploy/compose.yaml up --build -d` запускает
весь стек. Catalog: http://localhost:8080, Orders: http://localhost:8081.
На свежей базе ресторан 1 открыт. Создайте категорию и блюдо, затем заказ:

```bash
curl -sS -X POST http://localhost:8080/restaurants/1/categories -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' -d '{"name":"Основное","sort_order":0}'
# Подставьте фактический category_id из ответа (на свежей базе — 1).
curl -sS -X POST http://localhost:8080/restaurants/1/items -H 'X-Dev-User-ID: 1' \
  -H 'Content-Type: application/json' \
  -d '{"category_id":1,"name":"Плов","description":"","price_minor":6500,"currency":"TJS","is_available":true}'
# До оформления явно настройте координаты, DELIVERY и тариф (см. docs/pricing.md).
curl -X PATCH http://localhost:8080/restaurants/1 -H 'Content-Type: application/json' -H 'X-Dev-User-ID: 1' -d '{"latitude":38.56,"longitude":68.77}'
curl -X PUT http://localhost:8080/restaurants/1/delivery-settings -H 'Content-Type: application/json' -H 'X-Dev-User-ID: 1' -d '{"delivery_enabled":true,"pickup_enabled":true,"minimum_delivery_subtotal_minor":0}'
curl -X POST http://localhost:8080/restaurants/1/delivery-tariffs -H 'Content-Type: application/json' -H 'X-Dev-User-ID: 1' -d '{"max_distance_meters":2000,"delivery_fee_minor":1000}'
# Подставьте menu_item_id из ответа (на свежей базе — 1).
curl -i -X POST http://localhost:8081/orders \
  -H 'Content-Type: application/json' \
  -H 'X-Dev-Customer-ID: 7' -H 'Idempotency-Key: order-demo-1' \
  -d '{"restaurant_id":1,"fulfillment_type":"DELIVERY","delivery_address":"Душанбе, улица 1","currency":"TJS","delivery_latitude":38.56,"delivery_longitude":68.77,"expected_total_minor":14000,"items":[{"menu_item_id":1,"quantity":2}]}'
curl -i http://localhost:8081/orders/1 -H 'X-Dev-Customer-ID: 7'
curl -i -X POST http://localhost:8081/orders/1/cancel -H 'X-Dev-Customer-ID: 7'
curl -i http://localhost:8081/health
curl -i http://localhost:8081/ready
```

Создание возвращает 201 + Location и объект с id, customer_id, restaurant_id,
status=WAITING_RESTAURANT, fulfillment_type, payment_method (POS_ON_DELIVERY либо
POS_AT_PICKUP), payment_status=UNPAID, серверный delivery_fee_minor, total_minor, pricing_snapshot, снимки адресов,
currency, subtotal_minor, created_at, updated_at, items.
Каждая позиция содержит id, order_id, menu_item_id, name, quantity,
unit_price_minor и total_price_minor. `subtotal_minor=13000` — **стоимость блюд**
в минимальных единицах TJS, без доставки, налогов и скидок.

| Метод | Успех | Поведение |
|---|---|---|
| POST /orders | 201 | обязательны fulfillment_type, Idempotency-Key, подтверждённая сумма и identity; для DELIVERY — delivery_address |
| GET /orders/{id} | 200 | текущее состояние своего заказа |
| POST /orders/{id}/cancel | 200 | пустое тело; до готовки по политике способа получения |
| POST /orders/{id}/pickup-code | 200 | владелец готового PICKUP получает новый код; пустое тело |
| POST /restaurants/{restaurant}/orders/{id}/verify-pickup | 200 | проверка кода до POS; без изменения состояния |
| POST /restaurants/{restaurant}/orders/{id}/complete-pickup | 200 | выдача с кодом и подтверждением POS; права ресторана |
| GET /health | 200 | liveness без I/O |
| GET /ready | 200/503 | собственная БД и схема Orders |

Ошибки согласованы с Catalog: `{"error":"message"}`. 400 — невалидные поля,
повтор MenuItemID, количество вне 1–100, пустой заказ, больше 100 позиций,
переполнение или неверный/отсутствующий ключ. 401 — отсутствующая identity.
404 — нет ресторана/блюда/своего заказа (включая чужой заказ). 409 — изменилась
сумма, ресторан закрыт, блюдо недоступно/чужое, ключ уже использован для другого
запроса, повторная отмена. 503 — временный отказ/timeout/неверный ответ Catalog.
405/413/415/500/504 — как у Catalog. Цены и CustomerID из JSON отклоняются как
неизвестные поля. expected_total_minor обязателен и должен быть положительным.
Для DELIVERY обязательны delivery_latitude и delivery_longitude.
При 409 цены клиент обновляет меню, подтверждает актуальную сумму и отправляет
новый запрос с новым ключом. Ошибки не закрепляют ключ, успешные ответы закрепляют.

Повторите исходный POST с тем же ключом: вернутся те же ID, цены и исходный статус,
даже после отмены, изменения меню или перезапуска Orders. GET вернёт текущее
состояние. Ключ привязан к CustomerID; порядок позиций и JSON whitespace не
меняют содержание запроса. Другая сумма/количество/ресторан с сохранённым ключом —
409. Ключи сохраняются без TTL, поэтому гарантия повторов не истекает.

### Внутренний quote API

Вызов идёт **Orders → Catalog**, Catalog возвращает снимок. Единственный batch
POST /internal/v2/menu/quote принимает restaurant_id, fulfillment_type,
items[{menu_item_id,quantity}], для DELIVERY — delivery_location{latitude,longitude}.
Возвращает также items_subtotal_minor, delivery_fee_minor, total_minor,
delivery_available, unavailable_reason, distance_meters и pricing_snapshot.
V1 сохранён для переходного deployment.
Возвращает restaurant_id, currency=TJS и items с menu_item_id, restaurant_id, name,
quantity, unit_price_minor, currency. Бизнес-ошибки имеют `error` и дополнительный
машинный `code`: restaurant_not_found/item_not_found (404),
restaurant_unavailable/item_unavailable/item_restaurant_mismatch (409),
invalid_quote (400). Проверки читают ресторан и все блюда одним SQL statement.

Публичный listener Catalog на 8080 не имеет этого маршрута (404). В Compose
8082 привязан к адресу `catalog-internal` в `backend` с internal:true; порт не
публикуется. За пределами Compose оставляйте INTERNAL_HTTP_ADDR loopback или
привязывайте к приватному интерфейсу и ограничивайте сеть. В production нужны
TLS и service-to-service authentication, например mTLS, и запрет проксирования
/internal наружу. Разделение сетей само по себе не аутентифицирует сервис.

Quote не резервирует блюда: доступность и цена могут измениться сразу после
проверки. Заказ сохраняет исторический снимок, но не гарантирует наличие для
дальнейшего исполнения. Это ограничение текущего бизнес-процесса.

### Identity и границы production

Dev headers работают только при явном DEV_AUTH_ENABLED=true и
APP_ENV=development/test. Production с dev mode не запускается. При false нужны
OIDC_ISSUER, OIDC_AUDIENCE, OIDC_JWKS_URL и доверенный источник Principal: либо
AUTH_PRINCIPAL_URL/AUTH_SERVICE_KEY_FILE нового Auth, либо legacy AUTH_IDENTITIES_FILE.
Роли и ownership определяются серверной привязкой issuer/subject к внутренним ID.
Подробности: [identity и resource authorization](docs/security-reliability.md).

### Проверки Orders и миграции

```bash
go test -race -count=1 ./services/catalog/... ./services/orders/...
go vet ./services/catalog/... ./services/orders/...
(cd services/orders && GOWORK=off go build -o /tmp/orders ./cmd/api)
# Только выделенная тестовая БД; каждый тест создаёт и удаляет собственную схему:
TEST_DATABASE_URL='postgres://catalog:catalog_local_password@localhost:5432/catalog_test?sslmode=disable' \
  go test -race -count=1 ./services/catalog/... ./services/orders/...
```

Без TEST_DATABASE_URL integration tests явно пропускаются. Проверяются domain,
HTTP DTO и ошибки, реальный HTTP Catalog adapter, атомарность заказа, items, idempotency и Outbox,
конкурентное создание/отмена, CHECK/FK, изменение цены и снимки, replay после
перезапуска/отказа Catalog, изоляция пользователей и up/down/up миграции Orders.
Для Orders schema_migrations независима от Catalog; текущая миграция Orders —
000008, Catalog — 000005, Notifications — 000005, Delivery — 000008. Миграции не применяются из Go-приложения.

Подробные объяснения Aggregate Root, собственного хранилища, транзакций,
идемпотентности, направлений зависимостей и схема последовательности:
[ADR 0001](docs/adr/0001-orders-catalog-integration.md).
Фактически выполненные проверки и ограничения окружения —
[verification](docs/verification.md).

## Redpanda, Outbox и Notification Inbox

Подробное решение, контракты, гарантии, сбои и sequence diagram:
[ADR 0002](docs/adr/0002-outbox-inbox.md).

В Compose добавлены Redpanda, автоматический `init-topics`, отдельный `outbox`
process, Notifications и **независимый PostgreSQL** для Notifications. Сервисная
роль notifications_service не является superuser; bootstrap использует отдельный
NOTIFICATIONS_POSTGRES_PASSWORD. Дополните существующую `.env` новыми переменными
из `.env.example`, сохраняя прежние секреты. Для чистого окружения:

```bash
cp .env.example .env
docker compose --env-file .env -f deploy/compose.yaml up --build -d --wait
docker compose --env-file .env -f deploy/compose.yaml ps
docker compose --env-file .env -f deploy/compose.yaml logs -f outbox notifications
```

Контейнеры подключаются к `redpanda:9092` через KAFKA_INTERNAL_BROKERS.
Native Go-процессы используют `KAFKA_BROKERS=localhost:19092`; внешний advertised
listener задаётся KAFKA_EXTERNAL_HOST/PORT. Нельзя подставлять localhost в адрес
внутреннего broker listener. Notifications readiness: http://localhost:8083/ready.
Outbox health/readiness слушают :8084 внутри контейнера; host port не публикуется,
что позволяет запускать несколько workers:

```bash
docker compose --env-file .env -f deploy/compose.yaml up -d --scale outbox=2
docker compose --env-file .env -f deploy/compose.yaml run --rm migrate-notifications
docker compose --env-file .env -f deploy/compose.yaml exec redpanda \
  rpk topic describe orders.events.v1 -X brokers=redpanda:9092
docker compose --env-file .env -f deploy/compose.yaml exec redpanda \
  rpk group describe notifications-v1 -X brokers=redpanda:9092
```

Настройки и значения по умолчанию:

| Переменные | Значение / назначение |
|---|---|
| REDPANDA_IMAGE / SMP / MEMORY | v26.2.5 / 1 / 512M |
| KAFKA_INTERNAL_BROKERS / KAFKA_BROKERS | redpanda:9092 в Compose / localhost:19092 native |
| ORDERS_EVENTS_TOPIC | orders.events.v1; общий для producer и consumer |
| ORDERS_EVENTS_PARTITIONS / REPLICAS | 3 / 1 для локального стенда |
| ORDERS_EVENTS_RETENTION_MS | 604800000 (7 дней Kafka retention) |
| KAFKA_CONSUMER_GROUP | notifications-v1 |
| KAFKA_PUBLISH_TIMEOUT / DB_OPERATION_TIMEOUT | 10s / 5s |
| OUTBOX_POLL_INTERVAL / OUTBOX_LEASE | 500ms / 30s |
| OUTBOX_RETRY_BASE / RETRY_MAX / MAX_ATTEMPTS | 1s / 5m / 20 |
| OUTBOX_RETENTION / CLEANUP_INTERVAL | 168h / 1h; только PUBLISHED, до 1000 за проход |
| CONSUMER_RETRY_BASE / RETRY_MAX / MAX_ATTEMPTS | 1s / 10s / 5 |
| NOTIFICATIONS_DB_PORT / NOTIFICATIONS_PORT | 5433 / 8083 на 127.0.0.1 |

Укажите полные имена переменных с префиксами OUTBOX_/CONSUMER_, как в `.env.example`.
Lease проверяется при старте: он должен быть больше publish timeout + 2 DB timeout.
Redpanda использует постоянный volume и fsync; этот single-broker Compose не даёт
отказоустойчивости к потере узла. На существующем topic init не меняет число
partitions/retention. Менять число partitions без плана переноса потока нельзя.

### Полный сценарий и диагностика

Создайте заказ и отмените его командами из раздела Order Service. Передайте,
например, `X-Request-ID: checkout-demo-1`. После каждого успешного HTTP COMMIT
в Orders появится Outbox, а затем в Notifications — intent с тем же correlation_id.
Worker асинхронно создаст доставки активным устройствам; без устройств intent
завершится CANCELLED. [Регистрация устройств и Firebase](docs/push-notifications.md).
При повторном Idempotency-Key новое событие не создаётся. Проверить записи:

```bash
docker compose --env-file .env -f deploy/compose.yaml exec postgres \
  psql -U orders_service -d orders -c \
  'SELECT event_id,aggregate_id,aggregate_version,status,attempts,lease_until,last_error FROM outbox_events ORDER BY created_at;'
docker compose --env-file .env -f deploy/compose.yaml exec notifications-postgres \
  psql -U notifications_service -d notifications -c \
  'SELECT event_id,order_id,aggregate_version,kind,status,correlation_id FROM notifications ORDER BY id;'
docker compose --env-file .env -f deploy/compose.yaml exec notifications-postgres \
  psql -U notifications_service -d notifications -c \
  'SELECT id,topic,partition,offset_value,reason,attempts,resolved_at FROM consumer_quarantine ORDER BY id;'
```

Запись DEAD сохраняется вместе с исходным событием и блокирует последующие версии
этого заказа. После устранения причины повторите её, подставив настоящий UUID:

```bash
docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps \
  outbox -requeue EVENT_UUID
# После исправления причины/добавления поддержки схемы повторить карантин ID=1:
docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps \
  notifications -replay-parked 1 -operator alice -resolution-note 'Contract support deployed; incident INC-123'
```

Карантин хранит исходные bytes и координаты Kafka, а не логи с payload.
При недоступности и business storage, и quarantine offset не подтверждается.
processed_events автоматически не очищается: это сохраняет дедупликацию старых replay.
PENDING означает ожидание обработки worker. SENT означает принятие FCM,
а не гарантированное получение; попытки каждого устройства хранятся отдельно.

Чтобы проверить отказ вручную, остановите только `redpanda`, создайте заказ и
проверьте рост attempts/наличие неопубликованного Outbox. Верните брокер командой
`docker compose --env-file .env -f deploy/compose.yaml up -d redpanda` и дождитесь
PUBLISHED и сохранённого intent. При длительном отказе проверьте DEAD и выполните requeue.

Для native запуска после миграций (загрузите нужные переменные из `.env`):

```bash
DATABASE_URL="$ORDERS_DATABASE_URL" go run ./services/orders/cmd/outbox
DATABASE_URL="$NOTIFICATIONS_DATABASE_URL" HTTP_ADDR=:8083 \
  go run ./services/notifications/cmd/consumer
# Отдельный процесс; локально PUSH_PROVIDER=fake и APP_ENV=development:
DATABASE_URL="$NOTIFICATIONS_DATABASE_URL" go run ./services/notifications/cmd/push-worker
# Независимая сборка из модулей:
(cd services/orders && GOWORK=off go build -o /tmp/outbox ./cmd/outbox)
(cd services/notifications && GOWORK=off go build -o /tmp/notifications ./cmd/consumer)
```

### Автоматические проверки событийного этапа

```bash
# Unit/HTTP; PostgreSQL/Kafka tests явно SKIP без TEST_*:
go test -race ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/...
go vet ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/...

# Docker PostgreSQL + Redpanda, реальные integration tests и race/vet:
sh scripts/test-integration.sh

# Собрать весь Compose, запустить два Outbox Worker, остановить/вернуть брокер,
# создать и отменить заказ, проверить HTTP idempotency, дубли и correlation_id:
python3 scripts/verify-events.py
```

Последние две команды используют отдельные проекты food-events-test/food-events-e2e,
свои порты и удаляют **их тестовые** containers/volumes при завершении. Не используйте
эти имена для рабочего окружения. Python-сценарию нужен только стандартный Python 3.
Если default GOCACHE недоступен, добавьте `GOCACHE=/tmp/food-go-cache` перед `sh`.
Можно отдельно задать TEST_DATABASE_URL и TEST_KAFKA_BROKERS и запускать Go-тесты;
каждый тест создаёт собственную PostgreSQL-схему и Kafka topic.

Покрыты атомарность и rollback заказа/Outbox и Notification/Inbox, конкурентный
Idempotency-Key без нового события, два claim, восстановление/fencing lease,
DEAD/requeue/retention, реальный ACK с падением перед PUBLISHED, повторная доставка,
OrderCreated/OrderCancelled, перестановка версий, уникальность бизнес-эффекта,
неподтверждённый offset при отказе SQL, restart consumer, poison messages и карантин.


## Restaurant Fulfillment, Delivery и POS

Для DELIVERY ресторан принимает/отклоняет заказ, начинает приготовление **после назначения
курьера** и отмечает готовность. WAITING_COURIER отделяет ожидание назначения;
состояние и дедлайны Saga сохраняются в PostgreSQL.
Orders сохраняет Saga и CreateDelivery в Outbox. Delivery создаёт задачу по команде,
назначает свободного курьера вручную и
подтверждает получение оплаты при завершении доставки. Order обновляет DELIVERED
и REPORTED_COLLECTED одной Inbox-транзакцией по DeliveryCompleted.

Полный API, конфигурация прав, обновление существующего стенда, curl-сценарий и
команды тестов: [руководство](docs/fulfillment.md).
Обоснование и гарантии: [ADR 0004 — Fulfillment Saga](docs/adr/0004-fulfillment-saga.md).

```bash
python3 scripts/verify-fulfillment.py
python3 scripts/verify-saga.py
```

Сценарий использует отдельный Compose-проект и очищает его тестовые volumes.
Интеграция с банковским POS API и сверка не реализованы; сохранённое подтверждение
фиксирует заявление назначенного курьера либо сотрудника ресторана. Для PICKUP
Orders завершает выдачу, сохраняет POS и Outbox локально, без Saga/Delivery.

Saga находится внутри Orders. По timeout назначения до готовки флаг
SAGA_CANCEL_BEFORE_PREPARING разрешает запрос компенсации (default false).
После готовки автоматической отмены нет: NEEDS_ATTENTION, диагностика и ручной
retry/resume. Компенсация — идемпотентная Kafka-команда с ответом, а не SQL rollback.
В `.env.example` добавлены DELIVERY_COMMANDS_TOPIC и SAGA_*; полный порядок
обновления существующих данных и API диагностики — в руководстве выше.


## Security и проверки CI

[Полный runbook этапа 9](docs/security-reliability.md) описывает OIDC, роли,
серверные bindings, quarantine/fence и operational replay. Production consumers
сохраняют зависимые сообщения до ручного восстановления; простой DLQ-and-continue
не используется. При открытом карантине `/ready` consumer возвращает 503.

```bash
./scripts/check.sh
./scripts/test-integration.sh
```

GitHub Actions запускает эти же проверки на Pull Request и push в main.


### Delivery Zones & Pricing — этап 13

Catalog владеет настройками DELIVERY/PICKUP, минимумом блюд и радиальными тарифами.
Orders сверяет `expected_total_minor` с private quote v2 и сохраняет неизменяемые
`subtotal_minor + delivery_fee_minor = total_minor`. Delivery взыскивает полную
сумму через физический POS; PICKUP сохраняет нулевую стоимость доставки.
Исторические суммы/ответы не пересчитываются, DELIVERY ресторанов включается явно.

[API, запуск, Haversine, ограничения и deployment](docs/pricing.md) ·
[ADR 0010](docs/adr/0010-delivery-zones-and-pricing.md).
Миграции: Catalog 000005, Orders 000008, Delivery 000008.
Дополнительный E2E: `python3 scripts/verify-pricing.py`.
