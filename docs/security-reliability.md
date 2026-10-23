# Этап 9: Security & Reliability Hardening

Stage 18 добавляет независимый Auth с JWT/JWKS, PostgreSQL credentials/sessions и
проверкой актуальных полномочий через private authority. Новый режим и миграция:
[auth-service.md](auth-service.md), [ADR 0013](adr/0013-independent-auth-service.md).
Ниже сохранён legacy OIDC/registry runbook; Kafka/Inbox/Outbox/quarantine инварианты
действуют в обоих режимах. Не удаляйте старый IdP до проверки новой конфигурации.

## HTTP identity

Catalog, Orders, Delivery и Notifications проверяют JWT Bearer через `coreos/go-oidc/v3` и
доверенный JWKS. Проверяются подпись, точный issuer, audience конкретного API,
обязательный exp и nbf. Разрешены только RS256 и ES256; HMAC и `none` запрещены.
JWKS URL задаёт оператор, параметры `jku`/`x5u` из токена не определяют источник
ключей. Клиент JWKS имеет timeout 5s и запрещает redirects. KeySet переиспользуется,
кэширует ключи и обновляется при ротации kid. Собственной криптографии нет.

Нужен внешний OIDC/OAuth2 provider, выдающий **JWT access tokens для audience API**.
Не используйте audience frontend/client ID и ID tokens для обращения к API.
Issuer и JWKS задаются явно из доверенной конфигурации provider; discovery не
выполняется. В production оба URL должны быть HTTPS. Аутентификация не требует
доступности provider при запуске, но неизвестный ключ при недоступности JWKS
не даёт доступа. Отсутствие конфигурации приводит к отказу запуска API.

Для native процесса:

```bash
export APP_ENV=production DEV_AUTH_ENABLED=false
export OIDC_ISSUER=https://identity.example/realms/food
export OIDC_JWKS_URL=https://identity.example/realms/food/protocol/openid-connect/certs
export OIDC_AUDIENCE=food-orders
export AUTH_IDENTITIES_FILE=/etc/food/orders-identities.json
go run ./services/orders/cmd/api
```

Для MVP SPA задайте общий `OIDC_AUDIENCE=food-api`: Catalog, Orders, Delivery и
Notifications наследуют его в Compose. Service-specific overrides должны быть
пустыми либо совпадать с общим значением. Раздельные audiences по-прежнему доступны:
`CATALOG_OIDC_AUDIENCE=food-catalog`, `ORDERS_OIDC_AUDIENCE=food-orders`,
`DELIVERY_OIDC_AUDIENCE=food-delivery`, `NOTIFICATIONS_OIDC_AUDIENCE=food-notifications`;
тогда frontend должен получать access tokens для соответствующих ресурсов.
Передайте остальные переменные через отдельный production env и смонтируйте
реестр read-only в каждый API через Compose override, например:

```yaml
services:
  orders:
    volumes:
      - /etc/food/orders-identities.json:/etc/food/identities.json:ro
```

`AUTH_IDENTITIES_FILE=/etc/food/identities.json` — путь **в контейнере**.
Для Catalog, Delivery и Notifications задайте mounts того же актуального реестра аналогично. Пример формата
есть в [auth-identities.example.json](../deploy/auth-identities.example.json).
Файл не содержит токенов и паролей, но позволяет выдавать права: изменять его
должны только администраторы deployment. При отсутствии файла реестр пуст,
любой Bearer получает отказ в доступе. Обновление привязок требует перезапуска API.

Ключ привязки — `(OIDC_ISSUER, subject)`: issuer указан также внутри файла.
`subject` — непрозрачная строка, даже числовой subject не превращается в int64.
`user_id` — внутренний ID для аудита действий сотрудника/оператора;
`customer_id` и `courier_id` задаются отдельно. Повторные subjects/user IDs и
неоднозначные customer/courier bindings отклоняются при запуске. Роли и разрешения
тоже берутся из реестра, claims `roles`, `restaurant_id`, `customer_id` из JWT и
JSON не предоставляют полномочий. Клиенты не могут регистрировать эти привязки.
Курьерский binding создаётся оператором после создания соответствующего courier.
Не переиспользуйте старые ID для других людей.

| Роль | Доступ |
|---|---|
| customer | Создание, чтение, отмена, pickup-code только своего customer_id; проверка владельца остаётся в application/persistence |
| restaurant_staff | Изменения Catalog и действия Orders только для restaurants из серверной привязки; существующие SQL-проверки принадлежности блюда/заказа ресторану сохраняются |
| courier | Только связанный courier_id; чтение ограничено назначениями, pickup/decline/complete повторно проверяют назначение в домене под блокировкой доставки |
| dispatcher | Delivery требует permission `delivery:dispatch`; Saga retry требует `saga:retry` и явный ресторанный grant |
| admin | Может создавать/изменять весь Catalog; в Orders нужны ресторанные grants, в Delivery — `delivery:dispatch`; POS completion требует роли и binding courier |

Restaurant staff не получает права production Saga retry автоматически.
JWT/HTTP остаются в adapters/bootstrap. Application получает внутренние ID и
копии серверных grants; Domain не импортирует security. Общий модуль
`platform/security` нужен для одинаковой проверки JWT в четырёх API.
Он не владеет пользователями, БД или бизнес-состояниями.

GET/HEAD каталога, `/health` и `/ready` остаются анонимными. Любая мутация Catalog
проверяет principal и ресурс в своём handler до вызова application.
Notifications также предоставляет защищённый API `/v1/devices`; его `/metrics`
требует admin. Технические worker listeners имеют только health/readiness.
Внутренний Catalog quote — чтение
каталога через прежний отдельный приватный listener; сетевое ограничение сохраняется.
Сервисная mTLS/OAuth2 client credentials для этого соединения не добавлялись.

В development/test требуется явное `DEV_AUTH_ENABLED=true`; вместе с OIDC режим
запрещён. Catalog использует `X-Dev-User-ID` и серверные `DEV_ADMIN_IDS` либо
`DEV_RESTAURANT_GRANTS`. Orders/Delivery сохраняют существующие dev headers и
grants. В production dev mode запрещён, любые `X-Dev-*` на API отклоняются.
Dev mode предназначен исключительно для доверенного локального окружения.

### Profile и браузерный OIDC (Stage 16)

Orders `GET /v1/auth/me` возвращает только публичный DTO из Principal:
`user_id`, `roles`, `restaurant_ids`, `permissions`, всегда с no-store и массивами
вместо null. Authentication проверяет, кто предъявил access token; authorization
по серверному реестру и resource gates определяет, какие действия разрешены.
Файловые привязки требуют административной выдачи и restart всех API: self-service
выдачи ролей нет. В будущем потребуется управляемое хранилище grants с аудитом и
согласованным отзывом, отдельный User Service сейчас не создаётся.

Frontend public Client ID идентифицирует приложение в Authorization Code + PKCE
S256; API audience обозначает получателя access token. Они различаются. Проверка
JWT дополнена обязательным маркером access token: подписанный Keycloak payload
typ=Bearer либо protected JOSE typ=at+jwt/application/at+jwt без payload typ.
ID/refresh token и untyped JWT не принимаются даже с API audience. Убедитесь в
совместимости текущего IdP до rollout. Claims не становятся источником прав.

CORS_ALLOWED_ORIGINS разрешает только точные origins публичных API, без wildcard;
production требует HTTPS. Authorization/Content-Type/Idempotency-Key/X-Request-ID
разрешены, OPTIONS preflight обрабатывается до authentication. Cookies и dev
identity headers через CORS не включаются. Internal API, health/readiness и metrics
не получают CORS. CORS не заменяет сетевую изоляцию и серверную авторизацию.

[Полный контракт Profile, identity mapping, ограничения реестра и HTTPS Keycloak
runbook](auth-profile-oidc.md), [ADR 0012](adr/0012-auth-profile-and-oidc.md).

## Kafka: фиксация и карантин

Публичные event/command JSON и canonical fingerprints Inbox не изменены.
Delivery Decode дополнительно различает unsupported schema/type, сохраняя
`errors.Is(err, ErrInvalid)` для старых вызывающих сторон.

Порядок обработки production consumers:

1. Взять сервисную PostgreSQL advisory lock, общую для всех экземпляров consumer
   и operational replay этого сервиса. Session lock удерживается до завершения
   business transaction или сохранения карантина; соединение освобождается всегда.
2. Проверить координаты уже сохранённого сообщения и наличие открытого карантина.
3. Если fence отсутствует, выполнить прежнюю business transaction с Inbox.
   Временные/неизвестные внутренние ошибки повторяются ограниченно с exponential
   backoff и equal jitter. По умолчанию 5 попыток, timeout операции 5s, задержки
   0.5–1s, 1–2s, 2–4s, 4–8s. Notifications сохраняет свои CONSUMER_* настройки.
4. Invalid/unsupported contract, identity conflict, event gap и нарушение доменной
   инварианты сразу сохраняются в `consumer_quarantine`. Исчерпавшие retry ошибки
   сохраняются как `retries_exhausted`. SQL/credentials/payload в логи не попадают.
5. Пока есть хотя бы одна открытая строка карантина, **все** последующие сообщения
   данного сервиса сохраняются как `blocked_by_quarantine`, без business effects.
   Это охватывает другие partitions и оба входящих топика Delivery.
6. Только после business commit или надёжного INSERT очереди подтверждается
   конкретный Kafka offset. Ошибка offset commit завершает worker; при повторном
   чтении Inbox/координаты карантина защищают от повторных эффектов.

При недоступности PostgreSQL offsets не подтверждаются. Карантин не является
Inbox: успешные business effects всё так же атомарны с прежним Inbox/Outbox.
Сообщения не выбрасываются, bytes, key, topic, partition, offset, metadata,
число попыток и причина сохраняются в собственной БД сервиса. Структурированные
логи содержат correlation_id, event_id (для команды — message_id), topic,
partition, offset и error_type. Для повреждённого JSON неизвестные ID пусты.

Fence намеренно шире отдельного агрегата. Это простой безопасный вариант для
текущего объёма: испорченный key/JSON может не позволять достоверно определить
агрегат, а CreateDelivery/CancelDelivery и order events идут по разным топикам.
Цена решения — сериализация consumer processing, остановка бизнес-обработки
всего входящего потока сервиса и рост SQL-очереди до вмешательства оператора.
Работа HTTP и других сервисов продолжается. `/health` остаётся 200, `/ready`
consumer возвращает 503, пока есть открытые строки. Kafka lag в этот момент
может быть низким: мониторьте также размер/возраст `consumer_quarantine`.

Это локальные адаптеры каждого сервиса, а не общий messaging/workflow framework.
Не запускайте дополнительные consumer groups для тех же business Inbox и не
меняйте DB search_path: namespace lock — текущие database/schema.

## Ручное восстановление

Перед обновлением остановите старые consumers, примените миграции Orders 000006,
Delivery 000004, Notifications 000005 и запустите новую версию. Неразрешённые
исторические `failed_messages` Notifications копируются в новую очередь, старые
строки и audit сохраняются. Down migration нового карантина запрещена при наличии
строк или replay audit: сначала требуется отдельное архивирование оператором. Старый CLI `-replay-failed` Notifications больше не
обходит fence: он сообщает о необходимости использовать новую очередь.

В БД **соответствующего** сервиса:

```sql
SELECT id,topic,partition,offset_value,reason,attempts,event_id,correlation_id,created_at
FROM consumer_quarantine WHERE resolved_at IS NULL ORDER BY id;
```

1. Найдите первопричину: поддержку версии, недостающую зависимость/версию,
   конфликт ID или временный отказ. Исправьте причину и разверните исправление.
2. Обычный replay выполняйте по id. При version gap сначала найдите и обработайте
   подлинное недостающее событие из очереди: выбор отдельного id разрешён,
   доменные version gates и Inbox продолжают действовать. Не перескакивайте версии
   ручными UPDATE бизнес-таблиц.
3. Используйте CLI из доверенного operational окружения с доступом к БД сервиса.
   HTTP-доступ к replay не предоставляется. Имя оператора и основание обязательны,
   audit записывается до попытки. Это атрибуция оператора с доступом к deployment,
   а не самостоятельная проверка его OIDC-токена.

```bash
# Orders: внутри правильного Compose project/env.
docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps \
  orders-delivery-consumer -replay-failed 1 -operator alice \
  -resolution-note 'Upstream dependency restored; incident INC-123'
# Delivery: тот же набор флагов у delivery-consumer.
# Notifications: имя флага отличается от исторической таблицы failed_messages.
docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps \
  notifications -replay-parked 1 -operator alice \
  -resolution-note 'Contract support deployed; incident INC-123'
```

Если транспорт повредил JSON/key, можно восстановить **подлинное** сообщение из
upstream Outbox/audit с прежними event ID, aggregate version и фактами. Передайте
`-recovery-file /mounted/recovered.json` и при необходимости `-recovery-key 42`.
Файл ограничен 64 KiB. Прежние bytes не изменяются; recovery body/key сохраняются
в `consumer_replay_audit`. Не придумывайте payment/courier/delivery facts и не
отмечайте строку разрешённой ручным SQL. Автоматического discard/skip нет.

После успешного business commit запись помечается resolved в транзакции с audit.
Сбой между business commit и resolution безопасен: повторный replay проходит
через прежний Inbox. Ошибочный replay не снимает fence. Обработайте зависимые
строки, убедитесь, что открытая очередь пуста и `/ready` вернулся в 200.
После этого новые Kafka records снова обрабатываются автоматически. Повтор
исходного offset после replay не воскрешает исправленный poison message.
Исторические notification failed_messages синхронно отмечаются RESOLVED.

## CI и локальные проверки

```bash
./scripts/check.sh
./scripts/test-integration.sh
python3 scripts/verify-pickup.py
python3 scripts/verify-saga.py
python3 scripts/verify-events.py
```

`check.sh`: gofmt, vet, unit/HTTP tests, race, независимый GOWORK=off build всех
шести Go-модулей, config validation двух Compose файлов без печати конфигурации.
`test-integration.sh`: временные PostgreSQL/Redpanda, up/down всех миграций в
отдельных схемах, тесты всех модулей с race и реальным broker, vet, cleanup.
Интеграционные тесты без TEST_DATABASE_URL/TEST_KAFKA_BROKERS пропускаются;
CI integration job задаёт их скриптом. Не направляйте TEST_DATABASE_URL на рабочую БД.
GitHub Actions запускает эти скрипты на PR и push в main, permissions `contents: read`.

## Эксплуатационные границы

Реальный production IdP, домены/audiences, registries, TLS ingress, защита Kafka/БД,
backup/retention карантина и мониторинг должны задаваться deployment. Local
Compose остаётся одноброкерным стендом. Ключи JWKS кэшируются библиотекой:
при аварийном отзыве ключа перезапустите API и отзовите tokens у provider;
обычная ротация через новый kid поддерживается. Runtime-отзыв grants сейчас
требует перезапуска. Внешний provider в тестах заменён настоящим локальным JWKS
HTTP server; интеграция с конкретным production IdP требует его конфигурации.
