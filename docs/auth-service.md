# Stage 18: независимый Auth Service

Auth — отдельный Go module `services/auth`, собственная PostgreSQL, API process
и offline `authctl`. JWT issuer не является OAuth2/OIDC Provider: discovery,
authorization redirects, clients и password grant здесь отсутствуют.
Архитектурное решение: [ADR 0013](adr/0013-independent-auth-service.md).

## Структура и доверие

`domain` содержит User и инварианты без зависимостей от HTTP/SQL/криптобиблиотек.
`application` задаёт сценарии и порты repository/passwords/tokens, adapters
реализуют их через net/http, pgx/v5, x/crypto/argon2 и go-jose/v4. Bootstrap
создаёт зависимости, настраивает timeouts, два listener и graceful shutdown.
Auth API независимо собирается: `cd services/auth && GOWORK=off go build ./...`.

UserID — внутренний числовой audit ID; JWT `sub` — отдельный opaque identifier.
CustomerID/CourierID связывают его с бизнес-ресурсами. Restaurant memberships —
явный набор разрешённых RestaurantID, а не владение таблицами Catalog. Заказы,
назначения курьеров и доменные переходы по-прежнему проверяются своими сервисами.
CustomerID не равен UserID по соглашению и не извлекается из числового `sub`.

В новом режиме каждый защищённый запрос проверяется локально по JWT/JWKS и онлайн
через Auth. Последний читает свою БД и возвращает актуальный Principal. Ни один
бизнес-сервис не читает Auth tables. Logout/смена пароля/отзыв grants действуют на
следующий запрос даже до истечения JWT. При недоступности Auth — fail closed/503.
Уже разрешённые in-flight операции не образуют распределённой транзакции с отзывом;
SSE заканчивается не позднее access expiry и ограничений самого tracking API.

## API и frontend-контракт

Все пути ниже относятся к Auth API. Все ответы с учётными данными/профилем имеют
`Cache-Control: no-store`; PWA service worker не должен кешировать `/api/*`, auth
или JWT. Поля, отсутствующие в DTO, отклоняются; client roles/grants не принимаются.
Запросы с JSON требуют `Content-Type: application/json`. Mutation требует точный
`Origin` из allowlist, включая native-инструменты (они также должны указать Origin).

| Endpoint | Запрос | Результат |
|---|---|---|
| POST `/v1/auth/register` | email, password | 202 `{"status":"accepted"}` для нового и существующего email |
| POST `/v1/auth/login` | email, password | 200 access DTO и Set-Cookie refresh |
| GET `/v1/auth/csrf` | credentials cookie, Origin | 200 `{"csrf_token":"..."}`; 401 без cookie |
| POST `/v1/auth/refresh` | `{}`, cookie, X-CSRF-Token | 200 новый access DTO и новая refresh cookie |
| POST `/v1/auth/logout` | `{}`, cookie, X-CSRF-Token | 204, отзыв сессии и удаление cookie; Bearer не нужен |
| POST `/v1/auth/logout-all` | `{}`, Bearer | 204, отзыв всех сессий и удаление cookie |
| GET `/v1/auth/me` | Bearer | 200 текущий профиль |
| POST `/v1/auth/password` | current_password, new_password; Bearer | 204, отзыв всех сессий, нужен новый login |
| GET `/v1/auth/sessions` | Bearer | 200 массив активных session metadata |
| DELETE `/v1/auth/sessions/{id}` | Bearer | 204; чужой ID не отзывает чужую сессию |
| PUT `/v1/auth/admin/users/{id}/bindings` | Bearer admin; полный набор roles, permissions, restaurant_ids, customer_id, courier_id | 204, аудит, отзыв сессий и очередь отзыва устройств |
| POST `/v1/auth/verify-email` | — | 503 email_channel_not_configured |
| POST `/v1/auth/password-recovery` | — | 503 email_channel_not_configured |

Регистрация и вход:

```json
{"email":"customer@example.test","password":"a sufficiently long secret passphrase"}
```

Login/refresh response (refresh token отсутствует в JSON):

```json
{
  "access_token": "<short-lived signed JWT>",
  "token_type": "Bearer",
  "expires_in": 179,
  "expires_at": "2026-10-24T10:03:00Z",
  "csrf_token": "<session-bound CSRF token>",
  "user": {
    "user_id": 1000000000000,
    "email": "customer@example.test",
    "status": "ACTIVE",
    "email_verified_at": null,
    "roles": ["customer"],
    "restaurant_ids": [],
    "permissions": []
  }
}
```

`GET me` возвращает только объект `user` из примера. ID берётся из Bearer и
доверенной БД, query user_id не используется. `roles`, `restaurant_ids`,
`permissions` всегда массивы. Старый Orders `/v1/auth/me` сохранён с прежними
четырьмя полями для плавного обновления PWA; Auth profile содержит также email,
status и email_verified_at. Query не позволяет читать чужой профиль.

Session list возвращает только `id`, `created_at`, `expires_at`, максимум десять
активных сессий. Session ID не является refresh credential. Ни IP, ни user-agent,
ни refresh hashes через этот API не выдаются. Один пользователь имеет до десяти
активных сессий; следующий login отзывает самую старую.

Ошибки JSON: 400 invalid_request, 401 invalid_credentials, 403 access_denied /
origin_denied, 409 identity_conflict, 415 json_required, 429 try_later с Retry-After,
503 unavailable. Неверный пароль, неизвестный email, disabled/pending user дают
одинаковый 401; дубликат регистрации не выдаёт существование аккаунта. Не выводите
пользователю raw backend/FCM/JWT errors. Логи не содержат passwords/tokens/email.

## Cookies, CSRF и браузер

Production cookie: `__Host-food_refresh; HttpOnly; Secure; SameSite=Strict; Path=/`,
без Domain. TTL — абсолютный, по умолчанию 30 дней; refresh не продлевает его.
Refresh secret — 32 случайных байта, в БД только SHA-256 hash. CSRF — стандартный
HMAC-SHA256 с отдельным серверным ключом и текущим refresh token; он меняется при
rotation и не раскрывает refresh. CSRF key должен быть одинаковым на репликах и
сохраняться между рестартами.

Frontend использует `fetch(..., {credentials:'include'})` для login/refresh/logout
и csrf. Access JWT и CSRF token держатся в памяти; никаких localStorage,
sessionStorage, URLs или offline cache для JWT/refresh. После reload:

1. GET `/v1/auth/csrf` с cookie и Origin.
2. POST refresh с JSON `{}` и возвращённым X-CSRF-Token.
3. Сохранить новый access JWT/CSRF в памяти, запросить me и разрешённые рестораны.
4. Во все бизнес-API передавать только `Authorization: Bearer <access_token>`.

Origin проверяется для всех auth mutations. Refresh/logout дополнительно требуют
CSRF, связанный с cookie. Bearer mutations logout-all/password/admin не используют
cookie как полномочие. CORS credentials разрешены только на Auth для точных origins;
wildcard запрещён, service-secret headers и dev identity headers не разрешаются.
Cookie не является авторизацией в Catalog/Orders/Delivery/Notifications.

Refresh обязательно сериализовать между вкладками через Web Locks и согласованный
BroadcastChannel lifecycle. После конкурентного использования одного token вся
семья отзывается, даже если один запрос успел вернуть 200. Не повторять refresh
автоматически после неопределённого сетевого результата: после commit plaintext
successor не хранится и повторный запрос считается replay. Показать новый login.

Рекомендуется один HTTPS origin для PWA и API. Например, reverse proxy отображает
`/api/auth/*` в Auth `/v1/auth/*`, `/api/catalog/*` в Catalog `/*`,
`/api/orders/*` в Orders `/*`, аналогично Delivery/Notifications. Публичный proxy
никогда не маршрутизирует `/internal/*`, health или metrics. Не передавайте API
refresh cookie на внешний домен и не разрешайте cross-site SameSite=None.
Одинаковый origin упрощает модель; отдельные репозитории этому не мешают.
BFF с серверным хранением access JWT — более защищённая эволюция при активном XSS,
но в Stage 18 реализована явно согласованная memory JWT + refresh cookie схема.

## Пароли и эксплуатационные ограничения

Argon2id: по умолчанию m=19456 KiB, t=2, p=1, 16-byte random salt и 32-byte hash.
Параметры в PHC строке; проверка ограничивает допустимые ресурсы. Настройки
AUTH_ARGON_MEMORY_KIB / AUTH_ARGON_ITERATIONS / AUTH_ARGON_PARALLELISM проверяются
при старте. AUTH_HASH_CONCURRENCY (по умолчанию 2) ограничивает память; перегрузка
не создаёт неограниченную очередь Argon2. Параметры нужно измерить на production CPU.
Минимум 15 Unicode символов, максимум 1024 UTF-8 байта; пробелы и длинные фразы
разрешены, control chars/пустые/простейшие повторения и известные слабые значения
запрещены. Автоматический rehash старых PHC пока не выполняется.

Общие PostgreSQL fixed-window limits: login email 10/15 минут и peer 30/минуту;
register email 3/час и peer 10/час; refresh peer 120/минуту; auth HTTP peer 600/5 минут;
смена пароля 5/15 минут. Ошибки хранения limiter дают отказ, не bypass. Proxy headers
не считаются доверенным IP: за reverse proxy peer — сам proxy, поэтому нужна
дополнительная edge-защита и оценка общих лимитов для нагрузки/NAT.

**Production-блокер публичного onboarding:** почтовый канал не подключён. Register
создаёт PENDING_VERIFICATION с EmailVerifiedAt=NULL, login такого аккаунта запрещён.
Verify/recovery явно возвращают 503. Нельзя запускать production self-registration
как завершённый продукт, пока не добавлены реальная доставка, одноразовые hashed
verification/recovery secrets, TTL/attempt limits и проверка владения email.
`AUTH_ALLOW_UNVERIFIED_LOGIN=true` допустим только development/test, production
с ним не стартует. Управляемый account активируется администратором после внешней
проверки личности; это не подтверждает email, поэтому EmailVerifiedAt остаётся NULL.
Password recovery управляемого аккаунта — только контролируемое повторное
provisioning с новым секретом после внешней проверки, с отзывом всех старых сессий.

## Запуск и секреты

Для localhost основной Compose и Auth overlay подключаются через `.env`:

```sh
python3 scripts/setup-local.py
docker compose up -d --build --wait
```

`setup-local.py` создаёт `.env` из `.env.example`, если его нет; сохраняет существующие
пароли и ключи, генерирует недостающие AUTH_POSTGRES_PASSWORD/AUTH_DB_PASSWORD,
ES256 ring `.local/auth/keys.json`, CSRF и service keys. Требуется Go из go.work.
`.env` имеет mode 0600, каталог secrets — 0700, key files — 0640.
AUTH_SECRET_GID задаётся по группе хоста и добавляется контейнерам как supplementary
GID: ключи не становятся общедоступными. Auth получает все три файла, бизнес-API
и push worker — только service key. На другом хосте повторите setup для его GID.

`.env.example` выбирает APP_ENV=development, AUTH_ISSUER=http://localhost:8084,
AUTH_COOKIE_SECURE=false, AUTH_ALLOW_UNVERIFIED_LOGIN=true и
CORS_ALLOWED_ORIGINS=http://localhost:9000. Это позволяет проверять регистрацию и
вход без почтового канала в development. В production эти исключения запрещены.

Публичный Auth — `http://localhost:8084`; JWKS для контейнеров —
`http://auth:8084/.well-known/jwks.json`; authority — `http://auth:8086`.
API ожидают healthcheck Auth при запуске. Не заменяйте внутренние адреса на localhost:
в контейнере это адрес самого контейнера. Для явных Compose файлов используйте оба:
`docker compose --env-file .env -f deploy/compose.yaml -f deploy/compose.auth.yaml up -d`.

Существующие business ID перед переключением сохраняются через импорт registry,
описанный ниже. Подготовка файлов сама не изменяет аккаунты и бизнес-данные.

Production использует HTTPS issuer/JWKS, HTTPS Auth authority и HTTPS Notifications
revocation URL; platform/security отвергает HTTP endpoints в production.
Auth может завершать TLS через AUTH_TLS_CERT_FILE/AUTH_TLS_KEY_FILE (для обоих
listener) либо стоять за доверенным TLS proxy. Compose overlay описывает development
и нуждается в отдельной production конфигурации TLS/healthchecks/secret mounts.
Не публиковать internal port 8086. Его service key — отдельный минимум 32-byte
secret, передаётся как AUTH_SERVICE_KEY_FILE; клиент не выбирает authority URL.

AUTH_SIGNING_KEYS_FILE — read-only JSON JWKS ring с private active ES256 key.
Только Auth получает этот файл; бизнес-API получают public JWKS и service key.
Никаких ключей в Git, image layers, логах или frontend env. В production нужны
secret manager/минимальные file ACL, зашифрованные backups и контроль операторов.
GET `/.well-known/jwks.json` содержит только public keys и Cache-Control max-age=60.

Ротация ключей: сначала опубликовать новый public key вместе с прежним, сохраняя
старый active signer на всех репликах; затем выбрать новый AUTH_ACTIVE_KID и
перезапустить/rolling restart Auth с тем же ring. После максимального access TTL,
clock/cache overlap удалить старый private/public key и перезапустить реплики.
Нельзя одновременно удалить старый verifier key и продолжать выпуск старых JWT.
Аварийный компромисс требует отзыва сессий/auth versions и обновления API JWKS
кешей/перезапуска, а не ожидания обычного TTL. Доступ к старым backups контролируется.

## Управляемые пользователи и миграция

`authctl` доступен только операторам с доступом к Auth DB/контейнеру. Это отдельный
защищённый механизм bootstrap/provisioning, не публичный API. Он требует `-operator`
для аудита. JSON/password поступают через stdin; не помещайте секрет в CLI flags,
shell history, process args, CI output или Compose environment.

Пример безопасного интерактивного создания первоначального admin:

```sh
python3 -c 'import getpass,json,sys; print("Email: ",file=sys.stderr,end="",flush=True); email=input(); print(json.dumps({"email":email,"password":getpass.getpass(),"roles":["admin"]}))' \
  | docker compose exec -T auth authctl provision -operator deployment-admin
```

Для staff указываются roles=[restaurant_staff], restaurant_ids=[...]; для courier
roles=[courier] и фактический courier_id из Delivery. Для dispatcher нужны явные
permissions delivery:dispatch/saga:retry и restaurant_ids для Saga. Новые ресурсы
Delivery/Catalog не создаются этим инструментом. Повторное provisioning существующего
аккаунта требует user_id, сохраняет subject/CustomerID/CourierID, меняет пароль и
отзывает сессии/устройства. Нельзя переиспользовать ID для другого человека.
Admin HTTP update получает полный набор связей; customer_id/courier_id существующих
пользователей нужно сохранить. Последнего активного admin нельзя демотировать.
Отключение аккаунта с немедленным отзывом сессий/очередью отзыва устройств:
`docker compose exec -T auth authctl disable -user-id 10 -operator deployment-admin`.
Последнего активного admin также нельзя отключить. Для повторного включения нужно
контролируемое provisioning с новым паролем; подтверждение email при смене адреса
сбрасывается, а не переносится на новый адрес.

Порядок перехода со старого registry:

1. Сделать защищённые backups, экспортировать полный registry, сопоставить всех
   legacy issuer/subject с UserID/CustomerID/CourierID и restaurant grants. Проверить
   реальные максимумы бизнес-ID, включая исторические аккаунты вне registry.
   Зарезервированный диапазон новых ID начинается с 10^12; подтвердить отсутствие
   пересечений. Если существующие ID уже в нём, импортировать полный набор до
   открытия registration: allocator поднимется выше max импортированного ID.
2. Применить только Auth migrations в его БД. Старые бизнес-миграции/данные,
   idempotency responses, Outbox/Inbox и события не переписываются.
3. Передать исходный JSON registry в контейнер read-only и выполнить:

   ```sh
   docker compose run --rm --no-deps --entrypoint authctl \
     -v "$PWD/.local/auth/import-identities.json:/registry.json:ro" auth \
     import-registry -registry /registry.json -operator migration-admin
   ```

4. Импорт в одной транзакции сохраняет все исходные IDs, memberships и отдельную
   legacy issuer mapping. Conflict откатывает весь импорт. Повтор того же импорта
   идемпотентен по идентичности и не перезаписывает уже изменённые права/пароли.
   Legacy пароли не импортируются: это не Argon2id credential database Auth.
5. Импортированные записи DISABLED, email/password NULL, email verification NULL.
   После проверки личности назначить явный email и новый пароль через authctl
   provision с исходным user_id и исходными business IDs. Ничего не выводить в логи.
6. Проверить новый login/profile/JWT в изолированном стенде. Затем согласованно
   переключить все четыре API и push worker на новые issuer/JWKS/authority, убрать
   AUTH_IDENTITIES_FILE только в новой конфигурации. Смешанная authority запрещена.
7. Обновить frontend по контракту выше в отдельной задаче. После импорта удалить
   временный registry с хоста. Сохранение business IDs проверяется до открытия API.

Новый режим не зависит от ручного registry: актуальные roles/grants лежат в Auth DB.
Он не принимает старые JWT, а legacy режим не принимает новые. Откат требует
синхронизировать изменённые grants с прежним registry и согласованно переключить
API; logout в Auth не отзывает токены старого issuer. Новые public accounts не
существуют в старом IdP автоматически. Схема не обещает бесшовного dual-issuer SSO.

## Отзыв устройств, восстановление и проверки

Grant changes и очередь auth_device_revocations фиксируются атомарно. Worker
использует короткую PostgreSQL lease с fencing token, без HTTP внутри транзакции.
Notifications принимает идемпотентный secret-auth command, отзывает прежние devices
и очищает installation_id в своей БД. Pending work переживает restart и повторяется
с ограниченной задержкой до 15 минут; контролируйте backlog/возраст таблицы и
логи `device revocation pending`. Перед каждым Send проверяются актуальные права
через Auth, поэтому задержка доставки команды не даёт отправлять новым владельцам
старые confidential payloads. Уже начавшийся/принятый FCM Send отменить нельзя.

Consumed refresh hashes хранятся всю абсолютную жизнь session; cleanup удаляет
истёкшие sessions/tokens через семь дней, rate limits — через день после reset.
Audit не чистится автоматически. Для больших deployments нужно планировать
retention/архивирование audit, мониторинг latency и реплики Auth/DB.

```sh
scripts/check.sh
scripts/test-integration.sh
python3 scripts/verify-auth-service.py
python3 scripts/verify-push.py
python3 scripts/verify-pricing.py
python3 scripts/verify-offers.py
python3 scripts/verify-pickup.py
```

Интеграционные Go-тесты используют только выделенную TEST_DATABASE_URL и временные
schemas; без неё явно SKIP. Новый E2E создаёт отдельный Compose project с настоящими
БД и всеми API, временными ключами/паролями и fake FCM; production/реальную почту/
Firebase он не проверяет. Результаты запусков фиксируются в verification.md.

Рекомендации, использованные при выборе механизмов:
[OWASP Password Storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html),
[OWASP Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html),
[JWT BCP RFC 8725](https://www.rfc-editor.org/rfc/rfc8725.html).
