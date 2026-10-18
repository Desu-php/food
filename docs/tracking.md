# Real-Time Courier Tracking — этап 12

Delivery владеет текущим GPS. PostgreSQL определяет владельца заказа, статус и
назначенного курьера; Redis хранит только последний замер с TTL. История маршрута,
карта, road ETA и новый сервис не вводятся.

## Слои и интеграция

`domain.Location` проверяет координаты, accuracy и время. Application `Tracking`
определяет порты `TrackingRepository`/`LiveLocations`, принимает GPS и формирует
авторизованный snapshot. PostgreSQL и Redis реализуют порты. HTTP кодирует только
разрешённые поля в SSE. Bootstrap собирает зависимости и отменяет request contexts
при shutdown. Domain и application не импортируют Redis или HTTP.

Orders передаёт `customer_id` в `delivery.commands.v1` (schema_version=1) как
аддитивное поле `omitempty`, вместе с явным `fulfillment_type=DELIVERY`. Это
совместимое расширение существующего контракта, без нового topic/event или изменения
Saga-переходов. Delivery сохраняет привязку в `delivery_requests.customer_id` в
транзакции command Inbox + delivery + Outbox. Изменить известного владельца другой
командой нельзя. Повторные старые сообщения сохраняют прежние canonical fingerprints.
Поле не добавляется в публичные Delivery events.

Миграция `services/delivery/migrations/000007_tracking.up.sql` оставляет исторические
владения NULL. Tracking таких заказов закрыт, пока доверенная новая команда Orders
не передаст владельца. Автоматический backfill, чтение чужой БД или извлечение ID из
JWT subject отсутствуют. PICKUP никогда не создаёт Delivery и tracking для него
возвращает 404.

Rollout: сначала миграция Delivery и обновление всех его consumers/API, затем
Orders producers. До обновления мобильного GPS клиента сохранён старый формат
`latitude`/`longitude`: он обслуживает только прежний Auto Dispatch, без live-location.
Для tracking обязательны **оба** новых поля `accuracy_meters` и `recorded_at`.

## GPS API

`POST /couriers/me/location`, `Content-Type: application/json`, OIDC bearer с Delivery
API audience. Subject должен быть сопоставлен deployment-owned identity registry с
ролью `courier` и CourierID. CourierID в теле, клиентский ReceivedAt и другие
неизвестные поля отклоняются. Курьер должен существовать и быть AVAILABLE или BUSY;
OFFLINE получает 409. Старый dispatch-only формат сохраняет прежнее правило AVAILABLE.

```json
{"latitude":40.28,"longitude":69.62,"accuracy_meters":12,"recorded_at":"2026-10-18T10:00:00Z"}
```

Нужен актуальный timestamp: recorded_at строго новее `now - LOCATION_TTL`, допускается
не более пяти секунд будущего clock skew. Latitude [-90,90], longitude [-180,180],
accuracy конечна, положительна и не превышает `LOCATION_MAX_ACCURACY_METERS`.
Неточные данные отклоняются с 400, предыдущий хороший замер естественно устаревает.
Скорость, маршрут и GPS spoofing не моделируются. Время нормализуется до микросекунд.
Полярные координаты допустимы для tracking, но исключены из Redis GEO dispatch.

Ответы: 204 — сохранено; 400 — некорректные координаты, accuracy или timestamp;
409 — замер не новее последнего либо недоступен courier status; 429 — общий Redis
rate limit; 503 — Redis недоступен. Lua атомарно проверяет Redis TIME, high-water mark
RecordedAt и rate limit, обновляет payload с индивидуальным TTL и существующий GEO
index. Устаревший/равный RecordedAt не перезаписывает данные и не продлевает TTL.
ReceivedAt назначается Redis, а не клиентом. Между API-репликами нет локальной гонки.
При каждом чтении проверяются и RecordedAt, и ReceivedAt, даже если ключ ещё жив.
Auto Dispatch также ограничивает свежесть нового GPS формата временем RecordedAt:
поздно доставленный замер не получает дополнительное окно пригодности для назначения.
Точные координаты и токены не записываются в обычные application logs.

## Customer SSE API

`GET /orders/{orderId}/tracking` на Delivery API (по умолчанию localhost:8085).
Аутентификация через `Authorization: Bearer ...`, отдельный Delivery audience и
серверный principal с ролью `customer` и CustomerID. Параметры URL отклоняются,
включая `access_token`. Чужой, неизвестный, PICKUP или исторический заказ без
доверенной привязки возвращает одинаковый 404. Dispatcher/courier роль не даёт
доступа к customer tracking. HEAD и остальные методы возвращают 405.

```sh
curl -N -H "Authorization: Bearer $DELIVERY_CUSTOMER_ACCESS_TOKEN" \
  http://localhost:8085/orders/42/tracking
```

Для локального Compose нужны `APP_ENV=development`/`test` и явный
`DEV_AUTH_ENABLED=true`; вместо bearer используйте `X-Dev-Customer-ID: 7` и
`X-Dev-Courier-ID: 1`. Production отвергает dev identities. Native browser
EventSource не позволяет поставить Authorization header: используйте streaming
fetch/SSE client с поддержкой bearer; передавать токен в URL нельзя. Фронтенда нет.

SSE — однонаправленный HTTP поток: клиенту нужны только серверные обновления,
обратный GPS уже имеет свой HTTP endpoint. WebSocket добавил бы handshake,
двунаправленный протокол и более сложное обслуживание без пользы для этого API.
`Content-Type: text/event-stream`, `Cache-Control: no-store`, `X-Accel-Buffering: no`.
Прокси должен отключать буферизацию/кэширование и иметь read timeout больше heartbeat.

`http.Flusher.Flush()` передаёт накопленный HTTP output в соединение сразу после
полного frame, не дожидаясь возврата handler. Это не подтверждение прочтения клиентом;
прокси всё ещё может буферизовать данные. `ResponseController` задаёт deadline каждой
записи, ограничивая зависание на медленном клиенте.

При подключении отправляется `retry: 3000`, затем текущий snapshot:

```text
event: location
data: {"latitude":40.28,"longitude":69.62,"accuracy_meters":12,"recorded_at":"2026-10-18T10:00:00Z"}

```

Другие события имеют `data: {"status":"..."}`:

| Событие | Условие |
| --- | --- |
| `tracking_pending` | PENDING_ASSIGNMENT/ASSIGNED; координаты не читаются |
| `location` | PICKED_UP + свежий точный замер назначенного курьера |
| `location_unavailable` | PICKED_UP, но GPS отсутствует/устарел, Redis недоступен или назначение сменилось во время чтения |
| `tracking_ended` | DELIVERED/CANCELLED; затем сервер закрывает поток |
| `tracking_unavailable` | Ошибка чтения PostgreSQL/доступа после подключения; `data: {}`, поток закрывается |

Если GPS долго отсутствует, `location_unavailable` отправляется при переходе в это
состояние; далее идут heartbeat comments `: heartbeat`. Изменившиеся замеры отправляются
на следующем poll; одинаковые snapshots не дублируются. CourierID и ReceivedAt в SSE
не включаются. Клиент должен удалять показанную точку при unavailable/ended/disconnect.

## Polling, Kafka и переподключение

Выбран ограниченный периодический опрос: обычно раз в две секунды. На соединение
один handler, два останавливаемых ticker, никаких фоновых goroutine или очередей
GPS. Перед Redis выполняется проверка владельца и PICKED_UP в PostgreSQL; после Redis
повторно проверяются версия, статус и courier_id. Смена назначения отбрасывает старый
замер; следующий poll читает нового курьера. Terminal status проверяется без Redis.

Redis Pub/Sub рассылает сообщения подключённым подписчикам без durable log,
consumer offsets и replay. Kafka хранит бизнес-события с offsets, повторным чтением
и интеграцией Inbox/Outbox. Потерянный GPS полезнее заменить следующим актуальным
замером, чем накапливать журнал и доставлять старые точки через Kafka.

Pub/Sub здесь не используется: polling не требует подписок, fan-out goroutine и
отдельного механизма восстановления пропусков. Компромисс — до poll interval
задержки и нагрузка PostgreSQL/Redis пропорционально числу соединений. При defaults
200 активных PICKED_UP потоков дают примерно 200 SQL reads/s и 100 Redis reads/s,
без учёта начальных подключений и задержек I/O. Это ограниченный MVP, не глобальная
платформа для миллионов соединений.

После обрыва клиент повторяет GET с актуальным bearer; сервер заново проверяет
владельца/статус и читает последнее значение Redis. Last-Event-ID не используется:
нет истории или обещания доставки каждой точки. До истечения TTL пропуски обновлений
и reconnect не теряют актуальный snapshot. Поток ограничен пятью минутами и временем
жизни проверенного OIDC token; после истечения токена нужен новый bearer.

## Приватность, отказоустойчивость и ограничения

После terminal commit следующий poll отправляет ended и закрывает поток. Новый GET
не читает Redis для terminal заказа. Courier payload может оставаться до TTL и
обновляться для других operational задач; старый заказ не даёт к нему доступа.
Между последней SQL проверкой и сетевой записью возможен один уже проверенный frame
в полёте: сетевую доставку ранее отправленных байтов нельзя отозвать. Во время
Redis I/O SQL locks не удерживаются, завершение заказа не блокируется SSE клиентом.
Отмена после физического pickup по-прежнему запрещена доменными правилами.

При отказе Redis PICKED_UP поток показывает unavailable и продолжает попытки с poll
interval; после восстановления снова показывает свежие данные. Заказ, ручное
назначение, POS completion и PICKUP работают через PostgreSQL. Redis не входит в
readiness API. При потере PostgreSQL поток закрывается без выдачи координат.

Лимиты соединений общие и per-customer **в пределах одного API процесса**, без
очереди ожидающих; превышение даёт 429 и Retry-After. При N репликах общий максимум
N × лимит; для глобальных квот потребуется внешнее admission control отдельной
задачей. Статические OIDC grants перечитываются при рестарте; token expiry и max
stream duration ограничивают жизнь уже открытого потока. Здесь нет мгновенной
глобальной JWT revocation или Redis Cluster deployment.

При disconnect/context cancellation освобождается slot и останавливаются ticker.
Server BaseContext отменяется при shutdown, затем закрываются HTTP, Redis и SQL pool.

## Настройки и запуск

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| LOCATION_TTL | 60s | Возраст замера и TTL courier payload |
| LOCATION_MIN_INTERVAL | 5s | Shared GPS rate limit с Auto Dispatch |
| LOCATION_MAX_ACCURACY_METERS | 100 | Максимальная accuracy, допустимо (0,1000] |
| TRACKING_POLL_INTERVAL | 2s | Snapshot interval, минимум 100ms |
| TRACKING_HEARTBEAT | 15s | SSE heartbeat |
| TRACKING_MAX_DURATION | 5m | Lifetime потока, максимум 15m |
| TRACKING_WRITE_TIMEOUT | 5s | Deadline отдельной сетевой записи |
| TRACKING_OPERATION_TIMEOUT | 2s | Deadline SQL/Redis snapshot |
| TRACKING_MAX_CONNECTIONS | 200 | Соединения на процесс |
| TRACKING_MAX_PER_CUSTOMER | 3 | Соединения клиента на процесс |

Все durations положительны; heartbeat меньше lifetime, per-customer не выше total.
Defaults переданы в Compose и `.env.example`. Используется существующий REDIS_URL.

```sh
cp .env.example .env
docker compose --env-file .env -f deploy/compose.yaml up --build -d --wait
./scripts/check.sh
./scripts/test-integration.sh
python3 scripts/verify-offers.py
```

Compose migration job применяет 000007. Для локальных Go integration tests без
полного запуска: поднимите `deploy/compose.test.yaml`, задайте
`TEST_DATABASE_URL=postgres://test:test@localhost:15432/food_test?sslmode=disable` и
`TEST_REDIS_URL=redis://localhost:16379/0`, затем выполните
`go test -race ./services/delivery/... ./services/orders/... ./contracts/... ./platform/...`.
Только выделенная test DB: тесты создают и удаляют собственные схемы.
Основной integration script также поднимает Redpanda и проверяет migration round trips.

Unit/httptest покрывают validation, identity, ownership, lifecycle, смену курьера
во время Redis read, TTL/unavailable, reconnect, heartbeat, concurrent admission и
освобождение handler/slots после серии disconnect. Redis integration проверяет
атомарное упорядочивание конкурирующих замеров, общий rate limit, TTL, полярные точки
и отказ Redis. Сквозной HTTP тест использует реальные PostgreSQL/Redis и доменные
assignment/pickup/POS completion; Orders integration проверяет customer_id в Outbox
команде. Существующий verify-offers проверяет отсутствие регрессии этапов 10–11.

```mermaid
sequenceDiagram
    participant O as Orders
    participant K as Kafka / Outbox
    participant D as Delivery API / consumer
    participant P as Delivery PostgreSQL
    participant R as Redis
    participant C as Courier
    participant U as Customer
    O->>K: CreateDelivery v1 + customer_id (business command)
    K->>D: Trusted command
    D->>P: Transaction: Inbox + ownership + Delivery + Outbox
    C->>D: POST /couriers/me/location + OIDC + timestamp/accuracy
    D->>D: Validate principal and Location invariants
    D->>P: Check AVAILABLE / BUSY
    D->>R: Atomic monotonic Store + TTL + GEO + rate limit
    R-->>D: Success / stale / rate limited
    D-->>C: 204 / 409 / 429
    U->>D: GET /orders/id/tracking + customer OIDC
    D->>P: Check owner and current delivery state
    alt ASSIGNED
        D-->>U: SSE tracking_pending (no coordinates)
    end
    C->>D: Pickup after restaurant ready
    D->>P: PICKED_UP + business Outbox
    loop Each bounded poll while connection is active
        D->>P: Owner + PICKED_UP + assigned courier
        D->>R: Latest observation with freshness check
        R-->>D: Latest point / absent / unavailable
        D->>P: Recheck version, status and courier
        D-->>U: SSE location / location_unavailable; periodic heartbeat
    end
    C->>D: Complete with operational POS confirmation
    D->>P: Atomic DELIVERED + confirmation + Outbox
    D->>P: Next poll sees terminal status (no Redis read)
    D-->>U: SSE tracking_ended, close connection
    U->>D: Reconnect GET with bearer
    D->>P: Check owner + terminal state
    D-->>U: tracking_ended, no courier location
```
