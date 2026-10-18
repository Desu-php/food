# ADR 0007: автоматическое назначение внутри Delivery и Redis GEO

Статус: принято. Дата: 2026-10-18.

## Контекст

Orders уже владеет fulfillment Saga и разрешает готовить DELIVERY только после
CourierAssigned. Delivery имеет ручное назначение, row locks, уникальное ограничение
активной доставки на курьера и transactional Outbox. Нужен автоматический поиск по
текущей геопозиции без SQL-записи каждого GPS и без новой Saga/микросервиса.

## Решение

Catalog хранит необязательную пару координат. Private quote возвращает её вместе
с ценами в одном snapshot; Orders сохраняет неизменяемый снимок только для DELIVERY
и передаёт его в добавочных optional полях CreateDelivery v1. Общий чистый value
object contracts/geo содержит диапазоны и операцию копирования, без I/O и SQL.

Redis GEO — временный пространственный индекс. Sorted set seen и Redis TIME дают
member freshness и общий rate limit в атомарных Lua-операциях. Просроченные записи
удаляются, наличие в индексе не означает AVAILABLE. Географическое расстояние
сортируется по возрастанию, при равенстве — числовой courier ID. Это не ETA.

Worker работает внутри Delivery API. Короткий claim SKIP LOCKED сохраняет lease;
Redis вызывается после commit. Вторая транзакция проверяет token, deadlines,
состояние и TTL, блокирует courier и использует прежний domain.Assign и Outbox.
Partial unique index остаётся последним ограничением одной активной доставки.
Такая схема сериализует ручное/автоматическое назначение и допускает несколько
workers без распределённых Redis locks и долгих SQL-транзакций.

Saga публикует и сохраняет единый dispatch_deadline. Для новых заказов окно
назначения отсчитывается от CreateDelivery; задержка удалённого создания расходует
это окно. Исторические Saga без поля сохраняют прежний расчёт. Повторы ответа и
отказ курьера окно не продлевают. После отказа PostgreSQL cooldown исключает пару
курьер/доставка. После приготовления автоматический поиск отключается.

Worker не отменяет заказ. Отмена/NEEDS_ATTENTION остаются у прежней Saga. Политика
отмены после приготовления, POS и immutable creation responses сохраняются.
Дедлайн не продлевается локальным retry Delivery; authorized Saga retry может
выпустить новую версию CreateDelivery и окно без новой доставки и смены snapshot.
Существующий consumer quarantine fence приостанавливает автоматическое назначение;
shared advisory lock на SQL-фазе согласован с exclusive lock consumers/replay.

Redis не участвует в readiness или запуске обязательных сервисов. При отказе worker
сохраняет причину и выполняет ограниченные повторы; Orders, PICKUP и ручное назначение
работают с PostgreSQL. Потеря Redis требует свежих GPS, не восстановления назначений.

## Последствия

Добавлен небольшой адаптер go-redis/v9 для готового RESP/TLS клиента с deadlines,
без самописного протокола. Redis deployment приватный, ephemeral, с ограничением
памяти и noeviction. PostgreSQL хранит assignments, retries, lease, cooldown и
Outbox; Redis не получает бизнес-права. При старых/полярных координатах доступен
manual fallback. Для больших радиусов/флотов нужны измерения Lua/GEOSEARCH latency;
нет маршрутов, дорожного ETA, GPS verification и автоматического выбора после
приготовления. Часы Redis/PostgreSQL/API должны быть синхронизированы.

Rollout требует consumers-first: миграции → Delivery consumers/API и Catalog →
Orders producers → включение worker. Новые поля omitempty сохраняют исторический
canonical hash; события, topic и schema_version остаются прежними. Повторная
доставка одного command ID не сбрасывает retry budget и не создаёт новый event.

Подробности API, конфигурации и восстановления: [Automatic Dispatch](../automatic-dispatch.md).
