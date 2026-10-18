# ADR 0008: подтверждение автоматического назначения через CourierOffer

Статус: принято. Дата: 2026-10-18. Уточняет фазу назначения ADR 0007.

## Контекст

Автодиспетч этапа 10 выбирал ближайшего доступного курьера и сразу выполнял
Delivery.Assign. Теперь курьер должен подтвердить предложение. Требуются
30-секундный TTL, восстановление, несколько workers и сохранение ручного fallback,
consumer fence и preparation gate Orders Saga.

## Решение

CourierOffer — отдельная доменная сущность Delivery с PENDING, ACCEPTED, DECLINED,
EXPIRED, CANCELLED. Предложения и аудит хранятся в PostgreSQL, без новой Saga,
микросервиса, Kafka topic или Redis business state. Два partial unique indexes
по PENDING ограничивают одну резервацию доставки и курьера. Срок не входит в
предикат индекса: резервирование снимается сохранением терминального статуса.

Существующий Dispatch Worker после lease и Redis GEO создаёт PENDING вместо
назначения. TTL равен min(30s, остаток Saga dispatch_deadline). Ожидание ответа
асинхронно: SQL-транзакций между созданием предложения и HTTP-решением нет.
Отдельный expiry worker в том же API процессе работает и при выключенном поиске.
Его SKIP LOCKED по Delivery сохраняет общий порядок блокировок с ответами и отменой.
И сроки, и retry/cooldown восстанавливаются из PostgreSQL после рестарта.

Accept блокирует Delivery, Offer, Courier, проверяет clock_timestamp после
ожидания locks и использует прежние domain.Assign/writeDeliveryChange. ACCEPTED,
ASSIGNED, BUSY и CourierAssigned Outbox коммитятся вместе. Повтор возвращает
сохранённое решение; не публикует новое событие и не назначает повторно. Поздний
ответ сохраняет EXPIRED и возвращает конфликт. Отказ/истечение используют прежний
persisted cooldown для пары Delivery/Courier, не сбрасывая бюджет или deadline.

Ручное назначение отменяет PENDING своей Delivery в той же транзакции. Курьер с
предложением другой Delivery защищён от скрытого снятия резервации: такой manual
request получает конфликт. CourierAvailable остаётся операционным статусом;
PENDING резервирует курьера отдельно от BUSY. При переходе OFFLINE принятие
запрещено. CancelDelivery и cancellation events отменяют PENDING под тем же lock.

Любое назначение и создание предложения согласованы с прежним shared advisory
consumer fence. Expiry/decline освобождают резервации независимо от Redis и
quarantine. Orders получает прежний CourierAssigned только после принятия и
успешного назначения (либо разрешённого ручного назначения). Поэтому preparation
gate и существующая Saga не требуют новых состояний или контрактов. PICKUP
по-прежнему не имеет Delivery и не достигает offer use cases.

## Последствия и trade-offs

PostgreSQL остаётся единственным источником права назначения. Redis outage не
мешает принять сохранённое предложение или назначить вручную. Kafka outage
задерживает продолжение Saga; Outbox сохраняет факт принятия атомарно.

Используется HTTP polling собственных живых предложений; push/WebSocket и клиентский
GPS tracking не вводятся. TTL worker имеет обычную задержку до poll interval, но
HTTP проверяет срок самостоятельно. Сохранённый PENDING может удерживать
резервацию во время простоя всех API; после запуска она освобождается автоматически.
История растёт до отдельной политики retention. Миграция additive; переход
требует отключить старые auto workers, иначе они смогут назначать без предложения.
Откат таблицы требует сохранить аудит и убрать активные предложения до downgrade.

Подробности: [Courier Offer API, TTL, конкуренция и rollout](../courier-offers.md).
