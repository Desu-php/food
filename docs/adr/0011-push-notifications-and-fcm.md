# ADR 0011: Push Notifications и Firebase FCM

Статус: принято. Этап 14.

## Контекст

Notifications уже потребляет события Orders через transactional Inbox, сохраняет
намерения и использует service-wide consumer quarantine. Нужна асинхронная отправка
клиентам, ресторанным сотрудникам и курьерам без зависимости fulfillment от Firebase.
Предложения курьерам живут 30 секунд и могут завершиться раньше доставки Kafka.

## Решение

Оставить Notification Service владельцем устройств, намерений, истории и provider
интеграции. Добавить отдельный процесс cmd/push-worker в тот же Go-модуль и образ.
Порт PushProvider принадлежит application; официальный Firebase Admin SDK v4.22.0
подключён только в adapter. Использовать Message.Fid и зарегистрированный клиентом
Firebase Installation ID согласно поддержке закреплённого SDK. Google ADC и ProjectID
передаются окружением/secret mount, fake разрешён только в development/test.

Расширить существующие notifications: один intent на event/recipient, immutable
безопасный текст/data и TTL. Отдельные push_deliveries и push_attempts хранят fanout,
lease/retry и все результаты. Inbox и намерения коммитятся атомарно, Send всегда
выполняется после commit, без SQL-транзакции. SKIP LOCKED и UUID fencing защищают
фиксацию результатов нескольких workers. Общий PostgreSQL limiter ограничивает
Send starts. Bounded exponential retry с jitter учитывает Retry-After; ошибки
классифицируются без raw provider error strings. SENT значит accepted by FCM.

Использовать прежние Orders/Delivery факты для lifecycle. Для отсутствующих фактов
добавить отдельные versioned business topics courier-offers.events.v1 и
fulfillment.events.v1. Outbox streams отделены от прежних aggregate versions и
команд Saga. Offer Created/Closed фиксируются при реальных изменениях, NeedsAttention
— при переходе сохранённой Saga. Нет backfill и событий из предполагаемого состояния.

Регистрация требует OIDC principal из серверного registry. Recipient определяется
customer/courier business ID либо проверенным restaurant grant; UserID владеет
регистрацией. Установка имеет одну активную привязку. Transfer отзывает старую и
создаёт новую запись; очередь старого владельца не перенаправляется новому.
InstallationID не возвращается API, не логируется и стирается при отзыве.

Закрытие offer отменяет ожидающие intents, включая переупорядоченную доставку фактов.
Перед критическим push нужна свежая проверка через закрытую service-auth операцию
Delivery API. TTL задаётся на всех платформах. Эта проверка не создаёт распределённой
транзакции с Firebase: гонки и уже принятые внешние сообщения остаются возможными.
API заказа/offer — источник истины. Клиент проверяет актуальность и account binding.

## Последствия и альтернативы

Firebase объединяет Android/APNs/Web транспорт с официальным SDK, но добавляет Google
зависимости, квоты и ADC. Уведомления могут повториться после неоднозначного результата;
UNKNOWN сохраняет эту неопределённость. Нет обещания exactly-once, receipt или read.
Collapse и event_id помогают дедупликации, но не заменяют бизнес-проверки.

Отправка в Kafka transaction отвергнута: сетевой вызов не атомарен с PostgreSQL,
удерживает locks и связывает consumer lag с Firebase. Новый микросервис/gateway,
универсальный workflow и Redis queue не нужны: PostgreSQL уже обеспечивает durable
Inbox, fanout и recovery. Для коротких offers отдельное «скоро истечёт» напоминание
не даёт оправданной гарантии и не реализуется.

Есть дополнительный service credential и небольшой private Delivery endpoint для
relevance. Он не меняет статусы, не читает чужую БД из Notifications и при outage
запрещает отправку предложения. Успешный push не заменяет polling API курьера/ресторана.
Неактивные устройства и устаревшая очередь не получают будущих сообщений; история
попыток сохраняется, поэтому нужна эксплуатационная политика хранения audit.

Запуск, API, rollout, ограничения и проверки: [runbook](../push-notifications.md).
