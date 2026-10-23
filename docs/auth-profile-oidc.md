# Stage 16: Auth Profile API и настоящий OIDC

## Граница ответственности

Authentication отвечает на вопрос, кто предъявил токен: backend проверяет подпись,
issuer, API audience, срок действия и назначение JWT. Authorization отвечает,
разрешено ли этому пользователю действие над конкретным рестораном или заказом.
После проверки JWT `platform/security` ищет Subject в серверном реестре и создаёт
Principal. Роли, restaurant grants, customer/courier ID берутся только из реестра.
Даже корректно подписанные одноимённые JWT claims не дают прав.

`sub` — непрозрачный идентификатор внутри конкретного issuer. Он может быть UUID,
строкой или числом и не является внутренним `user_id`. Реестр явно связывает
`(issuer, subject)` с внутренним UserID для аудита и отдельными business IDs.
Не переиспользуйте ID для другого человека. Один issuer на процесс; pairwise
subjects разных SPA clients требуют согласованной административной привязки.

Profile — HTTP-представление уже проверенного Principal, поэтому не требует
нового доменного агрегата, repository или проходного application use case.
Зависимости остаются направлены внутрь: bootstrap собирает middleware и HTTP,
бизнес-правила Orders/Catalog/Delivery не зависят от JWT или OIDC. Отдельный User
Service пока не нужен: паролями и сессиями владеет IdP, а backend хранит только
привязки и выдаёт существующие серверные права.

## Публичный контракт для frontend

Полный маршрут **Order Service**: `GET /v1/auth/me`.
Локальный URL: `http://localhost:8081/v1/auth/me`; production использует HTTPS.
Вариант `/auth/me` без `/v1` не добавляется.

```http
GET /v1/auth/me HTTP/1.1
Authorization: Bearer <access_token>
```

```json
{
  "user_id": 10,
  "roles": ["restaurant_staff"],
  "restaurant_ids": [1, 2],
  "permissions": []
}
```

| Поле | Значение |
|---|---|
| `user_id` | Положительный внутренний int64, стабильный ID из реестра |
| `roles` | Массив серверных ролей: customer, restaurant_staff, courier, dispatcher, admin |
| `restaurant_ids` | Массив разрешённых RestaurantID из Principal |
| `permissions` | Массив отдельных серверных permissions, например delivery:dispatch, saga:retry |

Массивы всегда присутствуют; отсутствие прав представлено `[]`. Ответ содержит
только эти четыре поля. Subject, issuer, customer/courier bindings, имя, email,
телефон, токены и конфигурация безопасности в ответ не включаются. Query, тело
и пользовательские identity headers не определяют текущего пользователя.
`Cache-Control: no-store` применяется также к отказам аутентификации.

| HTTP | Причина |
|---|---|
| 200 | JWT действителен, пользователь найден в реестре; допустим и профиль без ролей |
| 401 | Нет Bearer, токен повреждён/истёк, неверные issuer/audience/signature/type, либо X-Dev-* в OIDC режиме |
| 403 | Валидный access token, но Subject отсутствует в реестре; также отказ CORS для чужого Origin |
| 405 | Метод кроме GET/HEAD; `Allow: GET, HEAD` |

Ошибки — JSON с полем `error`, без деталей JWT/ключей. При 401 присутствует
`WWW-Authenticate: Bearer`. OPTIONS preflight разрешён для настроенного Origin
без токена и возвращает 204. HEAD наследует GET по соглашению Go ServeMux.

Frontend запрашивает профиль после login и повторяет после обновления сессии;
не кэширует его в service worker/PWA offline cache. При одном restaurant_id можно
выбрать его автоматически; при нескольких покажите выбор из возвращённого списка
и храните выбранный ID как UI preference. При пустом списке управление рестораном
недоступно. После нового профиля проверьте, что предыдущий выбор ещё разрешён.
ID в URL бизнес-запроса обозначает выбранный ресурс; backend повторно проверяет
роль и grant для каждого действия независимо от интерфейса или ранее полученного
профиля. Не считайте список ресторанов универсальным разрешением на любое действие:
например, Saga retry требует отдельного permission и роли оператора.

## Client ID, issuer и API audience

| Настройка | Назначение | Пример |
|---|---|---|
| frontend `OIDC_AUTHORITY` | Issuer IdP для discovery/login | https://identity.food.test:8443/realms/food |
| frontend `OIDC_CLIENT_ID` | Публичный OAuth client приложения | food-pwa |
| backend `OIDC_ISSUER` | Точный доверенный issuer токена | https://identity.food.test:8443/realms/food |
| backend `OIDC_JWKS_URL` | Явно настроенный HTTPS endpoint ключей | …/protocol/openid-connect/certs |
| backend `OIDC_AUDIENCE` | Ресурс, которому адресован access token | food-api |

Backend не выполняет discovery и не выбирает ключи по `jku`/`x5u` из токена.
Frontend использует Authorization Code + PKCE **S256**, проверяет state и nonce
средствами OIDC SDK, обменивает code с тем же redirect URI и code_verifier.
SPA является public client и не хранит client secret. К API отправляется
`access_token`, а `id_token` используется OIDC SDK для сессии приложения.
[Описание browser flow и PKCE в Keycloak](https://www.keycloak.org/securing-apps/javascript-adapter).

Для MVP у Catalog, Orders, Delivery и Notifications один audience `food-api`.
Это позволяет одной SPA обращаться к публичным API одним access token.
Issuer/signature/expiration/audience остаются обязательными; API независимо
проверяют серверные роли, ownership и grants. Цена решения — более широкая область
применения украденного access token внутри доверенного backend. Используйте HTTPS,
короткий TTL и не сохраняйте токены в URL, логах или PWA cache.

Compose передаёт `${SERVICE_OIDC_AUDIENCE:-${OIDC_AUDIENCE:-}}`. Доступны прежние
`CATALOG_OIDC_AUDIENCE`, `ORDERS_OIDC_AUDIENCE`, `DELIVERY_OIDC_AUDIENCE`,
`NOTIFICATIONS_OIDC_AUDIENCE`. Пустой override наследует общий audience. При переходе
со старой настройки очистите все service overrides либо установите их в `food-api`.
Для отдельных resource tokens можно сохранить разные audiences, если IdP и frontend
умеют получать соответствующие access tokens; нельзя отправлять Orders token с
единственным audience `food-orders` в Catalog, ожидающий `food-catalog`.
Native процессы всегда читают только `OIDC_AUDIENCE`.

Поддерживаются подписанные RS256/ES256 JWT с явным назначением access token:
Keycloak payload `typ: "Bearer"` (JOSE typ отсутствует либо JWT/at+jwt) или
защищённый JOSE `typ: "at+jwt"` / `"application/at+jwt"` без payload typ.
ID tokens, refresh tokens и JWT без маркера access token отклоняются даже при
совпадающем API audience. Другой IdP должен выпускать JWT с поддерживаемым маркером;
opaque tokens/introspection и произвольные provider-specific profiles не реализованы.
Это устраняет принятие ID token, которое одна проверка `aud` сама по себе не исключает.
Маркеры RFC описаны в [RFC 9068, §4](https://www.rfc-editor.org/rfc/rfc9068.html#section-4).
Перед обновлением проверьте формат токенов действующего IdP; старые JWT без маркера
больше не принимаются. Бизнес-контракты, Inbox/Outbox и historical retries не меняются.

## CORS

`CORS_ALLOWED_ORIGINS` — список точных origins через запятую:

```dotenv
CORS_ALLOWED_ORIGINS=https://food.example,https://staff.example
```

Origin включает scheme, hostname и при необходимости port, без пути и завершающего
слеша. Wildcard, null origin, userinfo, query и fragment запрещены. В production
разрешён только HTTPS; development/test допускают HTTP для localhost SPA. Пустая
настройка запрещает cross-origin запросы к публичным API. Запросы без Origin
(native/service clients) продолжают работать с обычной аутентификацией.

CORS расположен перед authentication; OPTIONS с Access-Control-Request-Method не
требует Bearer. Разрешены GET, HEAD, POST, PUT, PATCH, DELETE; конкретный маршрут
проверяет собственные методы. Headers: Authorization, Content-Type, Idempotency-Key,
X-Request-ID. Exposed headers: X-Request-ID, WWW-Authenticate, Allow, Retry-After.
Cookie credentials и X-Dev-* через CORS не разрешаются. Возвращаются Vary и no-store.
Чужой Origin/метод/header preflight даёт 403 без Access-Control-Allow-Origin.

CORS действует только на публичные семейства маршрутов: Catalog `/restaurants`,
Orders `/v1/auth/me`, `/orders`, `/restaurants`, Delivery `/orders`, `/deliveries`,
`/couriers`, `/courier-offers`, Notifications `/v1/devices`. Health/readiness,
metrics, приватный Catalog listener и Delivery `/internal/push/...` не получают CORS.
Сетевая изоляция внутренних API по-прежнему обязательна: CORS контролирует браузер,
а не заменяет service authentication или firewall.

## Локальный Keycloak по HTTPS

Опциональный [Compose overlay](../deploy/compose.oidc.yaml) поднимает настоящий
Keycloak и включает OIDC в четырёх API. Это development setup с H2 volume;
production требует отдельного управляемого IdP, его БД, TLS и backup policy.
`start-dev` Keycloak не означает DEV_AUTH_ENABLED в backend: там он выключен.

Для браузера на той же машине можно использовать `KEYCLOAK_PUBLIC_URL=https://localhost:8443`.
Overlay применяет этот URL к `KC_HOSTNAME` и backend issuer; frontend authority и
`issuer` в identities.json должны быть `https://localhost:8443/realms/food`.
В сертификат включите оба DNS SAN: `localhost` и `identity.food.test`.
Backend получает JWKS по внутреннему alias `identity.food.test`, поэтому менять
hosts Windows/WSL не требуется. Это два доверенных адреса одного локального IdP;
проверка TLS действует для обоих. В браузере необходимо доверить CA сертификата.
В Windows его можно импортировать в «Доверенные корневые центры сертификации»
**текущего пользователя**; секретный ключ CA импортировать нельзя.
Без `KEYCLOAK_PUBLIC_URL` сохраняется описанный ниже вариант с `identity.food.test`.

Чтобы обычный `docker compose up -d` из корня проекта включал OIDC overlay,
добавьте в локальный `.env`:

```dotenv
COMPOSE_FILE=deploy/compose.yaml:deploy/compose.oidc.yaml
KEYCLOAK_PUBLIC_URL=https://localhost:8443
```

`COMPOSE_FILE` использует разделитель `:` в Linux/WSL. Явные `-f` имеют приоритет:
команда только с `-f deploy/compose.yaml` запустит базовую конфигурацию без overlay.
Пароли, сертификаты, identities.json и локальные настройки храните в игнорируемой
`.local/oidc/` и `.env`, не в realm import или frontend environment.
Настройки hostname и TLS соответствуют
[документации Keycloak](https://www.keycloak.org/server/hostname).

1. Скопируйте `.env.example` в локальный `.env`, если его ещё нет. Сохраните
   существующие DB settings. Добавьте `KEYCLOAK_ADMIN_USER` и случайный
   `KEYCLOAK_ADMIN_PASSWORD`; они используются только локальным IdP и не хранятся
   в Git. Установите `CORS_ALLOWED_ORIGINS=http://localhost:9000` и `PUSH_PROVIDER=fake`.
2. Настройте локальный DNS: `identity.food.test` должен разрешаться в 127.0.0.1
   в браузере/WSL (например, hosts соответствующей ОС). В Compose это backend
   network alias Keycloak. Один issuer доступен браузеру и всем API по одному URL;
   `localhost` внутри контейнера не подходит для адреса IdP.
3. Создайте локальный доверенный сертификат, например установленным `mkcert`:

   ```bash
   mkdir -p .local/oidc/tls
   mkcert -install
   mkcert -cert-file .local/oidc/tls/server.pem \
     -key-file .local/oidc/tls/server-key.pem identity.food.test
   cp "$(mkcert -CAROOT)/rootCA.pem" .local/oidc/ca.pem
   chmod 644 .local/oidc/tls/server.pem .local/oidc/ca.pem
   # Keycloak runs as UID 1000; permit that user to read the development TLS key.
   # Use a group/ACL appropriate to your host, without making the key world-readable.
   setfacl -m u:1000:r .local/oidc/tls/server-key.pem
   ```

   Доверие CA должно быть настроено и в браузере, если он работает в другой ОС.
   API получают только публичный CA через SSL_CERT_FILE; private CA key никогда
   не монтируется. Проверка TLS не отключается.
   [Настройка TLS Keycloak](https://www.keycloak.org/server/enabletls).
4. Создайте `.local/oidc/identities.json` с пустым реестром:

   ```json
   {"issuer":"https://identity.food.test:8443/realms/food","identities":[]}
   ```

5. Запустите только IdP:

   ```bash
   docker compose --env-file .env -f deploy/compose.yaml \
     -f deploy/compose.oidc.yaml up -d keycloak
   ```

   Дождитесь `https://identity.food.test:8443/realms/food/.well-known/openid-configuration`.
   Realm `food` создаётся импортом [food-realm.json](../deploy/oidc/food-realm.json).
   Повторный старт сохраняет существующий realm: изменение import JSON само по
   себе его не обновляет. Меняйте настройки в Admin Console либо используйте
   новый выделенный development volume.
6. Откройте `https://identity.food.test:8443/admin/`, войдите локальным bootstrap
   admin и выберите realm `food`. В Users создайте тестового сотрудника, задайте
   локальный пароль в Credentials. Скопируйте его **ID** (OIDC Subject).
   Не используйте username, email или ID из master realm.
7. Привяжите сотрудника в `.local/oidc/identities.json`:

   ```json
   {
     "issuer": "https://identity.food.test:8443/realms/food",
     "identities": [
       {"subject":"<user-ID-from-food-realm>","user_id":10,
        "roles":["restaurant_staff"],"restaurants":[1,2],"permissions":[]}
     ]
   }
   ```

   Выдавайте только реальные RestaurantID, которыми сотрудник должен управлять.
   `restaurants` — имя поля файла; публичный DTO использует `restaurant_ids`.
   UserID назначает администратор backend, не IdP/клиент. Keycloak role с именем
   restaurant_staff не требуется и сама по себе никаких прав backend не даёт.
8. Запустите backend с overlay (реестр уже смонтирован read-only):

   ```bash
   docker compose --env-file .env -f deploy/compose.yaml \
     -f deploy/compose.oidc.yaml up --build -d
   ```

   После изменения registry перезапустите catalog, orders, delivery, notifications
   той же Compose-командой с `restart`. Для удаления доступа обновите все реплики;
   выполняющиеся SSE sessions ограничены token TTL и максимальной длительностью.
   Если пользователь регистрировал push device, отзыв grants также требует
   отзыва device registration по существующему operational порядку.

При ручной настройке IdP вместо импорта создайте realm `food` и OpenID Connect
client `food-pwa`: Client authentication OFF (public), Standard flow ON,
Implicit flow OFF, Direct access grants OFF, PKCE method S256 обязательна.
Valid redirect URI: `http://localhost:9000/auth/callback`; post-logout URI:
`http://localhost:9000/`; Web origins: `http://localhost:9000`. Если SPA использует
другой callback, задайте его точный URI и в IdP, и в OIDC SDK. Не добавляйте wildcard
redirects. В dedicated client scope добавьте Audience mapper с Included Custom
Audience `food-api`, Add to access token ON, Add to ID token OFF.
[Keycloak audience configuration](https://www.keycloak.org/docs/latest/server_admin/index.html#_audience).

Для native backend задайте переменные каждого процесса (overlay не требуется):

```dotenv
APP_ENV=development
DEV_AUTH_ENABLED=false
OIDC_ISSUER=https://identity.food.test:8443/realms/food
OIDC_JWKS_URL=https://identity.food.test:8443/realms/food/protocol/openid-connect/certs
OIDC_AUDIENCE=food-api
AUTH_IDENTITIES_FILE=/absolute/path/to/.local/oidc/identities.json
SSL_CERT_FILE=/absolute/path/to/.local/oidc/ca.pem
CORS_ALLOWED_ORIGINS=http://localhost:9000
```

Укажите также DATABASE_URL нужного сервиса и остальные настройки из README.
HTTP IdP разрешён только в автоматических `APP_ENV=test` fixtures; реальный
development issuer/JWKS также требует HTTPS.

## Frontend environment и проверка

```dotenv
OIDC_AUTHORITY=https://identity.food.test:8443/realms/food
OIDC_CLIENT_ID=food-pwa
AUTH_PROFILE_URL=http://localhost:8081/v1/auth/me
```

Это имена из входного frontend-контракта задачи. Исходники отдельного Quasar/Vue
проекта здесь отсутствуют: фактический способ экспорта env (например, через
quasar.config и build.env) необходимо сверить в нём. Этот backend не вводит
непроверенные VITE_/VUE_APP_ aliases и не изменяет frontend. Audience выдаётся
серверным mapper Keycloak, отдельный `audience` query parameter для него не нужен.
SPA SDK должен запросить openid scope и code flow с S256; сохранять code_verifier,
state/nonce на время redirect, обрабатывать callback и logout.

В DevTools после login проверьте: token endpoint вернул access token, в нём issuer
realm food, `aud` содержит food-api, payload typ Bearer. Не публикуйте сам токен.
Profile должен вернуть UserID 10 и [1,2], без имени/email. `/restaurants/1/orders`
доступен этому staff, `/restaurants/999/orders` возвращает 403. Пользователь IdP
без binding получает 403 на profile; logout/expired token требует обновления
сессии или login. Проверить preflight без раскрытия токенов можно так:

```bash
curl -i -X OPTIONS http://localhost:8081/v1/auth/me \
  -H 'Origin: http://localhost:9000' \
  -H 'Access-Control-Request-Method: GET' \
  -H 'Access-Control-Request-Headers: Authorization'
```

## Ограничения и проверки

Файловый реестр читается при старте; нет hot reload, self-registration, UI,
центрального аудита выдачи прав или немедленного отзыва во всех процессах.
Доступ к записи равнозначен выдаче административных прав. Используйте защищённый
deployment pipeline, read-only mounts, одинаковую версию файла во всех API и
контролируемый restart. Пароли остаются в IdP. В дальнейшем потребуется управляемое
хранилище identity mappings/grants с аудитом, согласованным отзывом и lifecycle
пользователей; эта задача его не создаёт. Browser profile помогает UI и не заменяет
серверную авторизацию. Не разрешайте self-registration административных ролей.

Автотесты используют настоящий JWT signer и локальный OIDC/JWKS HTTP server:
valid/expired/forged/wrong issuer/wrong audience/unmapped subject/ID-token,
public DTO, no-store, CORS, отсутствие утечек, dev-header rejection. PostgreSQL
integration test соединяет OIDC boundary, profile, server grants и разные gates
DELIVERY/PICKUP. Существующие E2E сохраняют полный POS lifecycle.

```bash
./scripts/check.sh
./scripts/test-integration.sh
python3 scripts/verify-push.py
python3 scripts/verify-pricing.py
python3 scripts/verify-offers.py
python3 scripts/verify-pickup.py
python3 scripts/verify-auth.py
```

check включает go test, go test -race, go vet и независимые сборки; integration
поднимает изолированные PostgreSQL/Redpanda/Redis и проверяет migration round trips.
`verify-auth.py` поднимает настоящий Keycloak из overlay, генерирует однодневный
TLS certificate и временные пароли, выполняет Code + PKCE S256 через login form,
проверяет обязательность PKCE, registry 403/restart, Profile, отказ ID/refresh
tokens, CORS и один audience во всех четырёх API. Использует отдельный Compose
project и удаляет его volumes, credentials и сертификаты после проверки. Требуются
Docker, Python 3 и openssl; доверие CA и hosts ОС не изменяются. Порт 8443 и
выделенные E2E ports должны быть свободны. Не запускайте несколько E2E scripts
одновременно, если их host ports совпадают.

Проверка реального browser redirect в отдельной PWA требует её запуска и настроенного
IdP; Go tests и headless HTTP flow не заменяют эту последнюю проверку frontend.
