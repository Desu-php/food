# ADR 0010: радиальные зоны и Pricing в Catalog

Статус: принято. Этап 13.

## Контекст

DELIVERY и PICKUP используют POS при получении. Раньше доставка была бесплатной,
поэтому subtotal ошибочно подходил и как сумма взыскания. Нужны тарифы по радиусу,
минимум блюд и сохранение согласованной итоговой цены без изменения истории.

## Решение

Catalog владеет delivery/pickup settings и DeliveryTariff. Он уже владеет меню и
координатами ресторана, поэтому один batch quote читает их одним MVCC snapshot.
Отдельный Pricing Microservice для этой модели не имеет собственной ответственности.

Зоны концентрические: выбираем минимальный max_distance_meters >= distance_meters.
Расстояние Haversine по сфере округляется вверх до целого метра. PostGIS и внешние
routing engines не нужны. Цены и итог — int64 minor units TJS с overflow checks.

Orders сверяет expected_total_minor и хранит immutable snapshot меню, адресов,
координат, выбранного тарифа/версии, subtotal, fee, total. Текущие настройки никогда
не пересчитывают существующий заказ. Между quote и commit нет распределённой
транзакции: согласованность привязана к моменту quote, а не commit Orders.

Delivery получает полную POS-сумму через существующую CreateDelivery v1. Новые
опциональные поля с omitempty сохраняют historical canonical Inbox fingerprints.
Orders events несут опциональный total; fallback для исторических событий — subtotal.
Расширенные consumers развёртываются до producers. POS confirmation, completion и
Outbox остаются одной транзакцией; Orders Inbox завершает заказ и Saga атомарно.

Новая миграция выключает DELIVERY, сохраняет прежний PICKUP и не придумывает старым
данным координаты. У исторических заказов fee=0 и total=согласованный subtotal;
idempotency JSON не переписывается. Downgrade Orders запрещён после новых snapshots.

## Последствия и ограничения

Поддерживаются несколько диапазонов, бесплатная доставка и ручное/автоматическое
назначение с прежними механизмами блокировок. Тарифы можно менять, не затрагивая
историю. Version относится к настройкам/тарифам, цены блюд сохраняются явно.

Круг не отражает дороги, природные препятствия и реальные сроки поездки. Валидация
координат не доказывает соответствие адресу. Нет карт, geocoding, ETA, динамических
коэффициентов, промокодов или онлайн-оплаты. Stored generated total переписывает
таблицу при миграции; для большой production БД требуется maintenance window.

API, deployment, rollback и тесты: [Delivery Zones & Pricing](../pricing.md).
