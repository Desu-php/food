# ADR 0009: Customer courier tracking через SSE

Статус: принято. Дата: 2026-10-18.

## Контекст

Delivery уже владеет couriers, assignment, Redis GEO и GPS endpoint. Orders владеет
customer identity заказа и Saga. Нужен live snapshot клиента после PICKED_UP без
новой истории GPS, микросервиса или high-frequency Kafka событий.

## Решение

Расширяем существующий GPS запрос accuracy/recorded timestamp, вводим domain Location
и application Tracking с портами. В Redis один latest payload с courier TTL,
атомарными Redis TIME, проверкой RecordedAt, rate limit и обновлением GEO. Плохая
accuracy/время отклоняются; старый замер не перезаписывает новый. GPS не пишется в SQL.
Legacy GPS остаётся dispatch-only, чтобы не придумывать timestamp/accuracy старым
клиентам. BUSY принимает новый формат, OFFLINE отклоняется.

CustomerID добавляется опционально в существующий версионированный CreateDelivery
contract (omitempty); известное владение неизменно и сохраняется в transaction
Inbox/Delivery/Outbox. Старые canonical messages и historical NULL не меняются.
Rollout consumers/migration перед producer. Прямого доступа к Orders DB нет.

SSE endpoint внутри Delivery проверяет server-mapped customer principal и owner
из SQL. Нет роли администратора, обходящей customer ownership. Перед pickup
координаты не читаются; terminal delivery возвращает ended и закрывает stream.
Чтение Redis обрамлено двумя SQL проверками assignment/status/version без удержания
SQL locks на Redis/сети. Старое назначение отбрасывается.

Используем bounded polling (default 2s), а не Pub/Sub: один handler и ticker,
восстановление из latest Redis при каждом reconnect, без подписок/очередей и потерь
из-за Pub/Sub disconnect. Достаточно однонаправленного SSE; WebSocket не нужен.
http.Flusher выпускает frame немедленно. Write deadlines, operation deadlines,
request cancellation, heartbeat и process/per-customer admission ограничивают
ресурсы. OIDC request context заканчивается не позднее token expiry; BaseContext
отменяет streams на shutdown.

Kafka сохраняет durable business events и команды с Inbox/Outbox. Redis Pub/Sub
эпhemeral и не имеет replay; для GPS ценен последний snapshot, а не каждый замер.
Ни Kafka GPS producer, ни новый брокер не вводятся.

## Последствия

Polling создаёт ограниченную задержку и SQL reads пропорционально числу открытых
потоков. Лимиты локальны реплике. Поток может потребовать reconnect через пять минут
или при истечении OIDC token. Last-Event-ID/history не обещаются. Прокси должен
пропускать SSE без buffering и bearer нужен в HTTP header (streaming fetch вместо
native EventSource с URL token).

Redis outage даёт location_unavailable с автоматическим повторным чтением; SQL
lifecycle и ручная работа продолжаются. GPS courier key живёт до TTL независимо
от завершённого заказа, но terminal customer authorization больше не читает его.
Сетевой frame между последней проверкой SQL и terminal commit может быть уже в
полёте; SQL locks не удерживаются при записи медленному клиенту. Антиспуфинга,
road routing, ETA и мгновенной глобальной token revocation нет.

API, sequence diagram, конфигурация, запуск и тесты: [Tracking](../tracking.md).
