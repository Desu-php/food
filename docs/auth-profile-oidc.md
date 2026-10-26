# Auth Profile API и CORS

## Граница ответственности

Authentication отвечает на вопрос, кто предъявил токен: backend проверяет подпись,
issuer, API audience, срок действия и назначение JWT. Authorization отвечает,
разрешено ли этому пользователю действие над конкретным рестораном или заказом.
После проверки JWT `platform/security` запрашивает актуальный Principal через
приватный Auth API. Роли, restaurant grants, customer/courier ID принадлежат Auth.
Одноимённые JWT claims не дают прав. Статический реестр сохраняется только как
совместимый режим проверки/импорта, без отдельного локального login deployment.

`sub` — непрозрачный идентификатор внутри конкретного issuer. Он может быть UUID,
строкой или числом и не является внутренним `user_id`. Реестр явно связывает
`(issuer, subject)` с внутренним UserID для аудита и отдельными business IDs.
Не переиспользуйте ID для другого человека. Один issuer на процесс; pairwise
subjects разных SPA clients требуют согласованной административной привязки.

Profile — HTTP-представление уже проверенного Principal, поэтому не требует
нового доменного агрегата, repository или проходного application use case.
Зависимости остаются направлены внутрь: bootstrap собирает middleware и HTTP,
бизнес-правила Orders/Catalog/Delivery не зависят от JWT или OIDC. Отдельный User
Service не требуется: паролями, сессиями и привязками владеет Auth Service.

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
| `user_id` | Положительный внутренний int64, стабильный UserID |
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

## Токены и локальный запуск

В localhost используется самостоятельный Auth Service, issuer `http://localhost:8084`,
общий audience `food-api` и ES256 access JWT с protected `typ=at+jwt`.
API проверяют подпись/issuer/audience/exp/nbf и актуальную сессию через приватный
Auth API. Права из клиентских JWT claims не используются. При недоступности Auth
защищённые операции закрыты. ID/refresh tokens и JWT без access marker отклоняются.
Общий JWT verifier сохраняет совместимость с подписанным payload `typ=Bearer`;
это не добавляет внешний login provider или discovery.

`GET /v1/auth/me` в Orders сохраняет узкий DTO выше. В Auth этот же путь возвращает
его собственный DTO с customer/courier bindings: [контракт](auth-service.md).
В текущем режиме grants обновляются через Auth, без ручного реестра/restart API.
Описанные выше ошибки реестра относятся к совместимому статическому JWT режиму.

```sh
python3 scripts/setup-local.py
docker compose up -d --build --wait
python3 scripts/verify-auth-service.py
```

## CORS

`CORS_ALLOWED_ORIGINS=http://localhost:9000` разрешает точный frontend origin.
Добавляйте другие origins явно, через запятую, без пути и wildcard; production
требует HTTPS. `localhost` и `127.0.0.1` — разные origins: используйте согласованный
hostname в frontend и Auth, иначе refresh cookie не будет работать.

Бизнес-API обрабатывают Bearer preflight до authentication и не разрешают dev
identity headers. Доступны Authorization, Content-Type, Idempotency-Key, X-Request-ID.
Auth дополнительно разрешает credentials и X-CSRF-Token для refresh cookie.
Для login/register/cookie mutations обязателен точный Origin; refresh/logout
требуют CSRF token. Access token хранится в памяти frontend; refresh — HttpOnly
cookie. Запросы frontend к Auth используют credentials: include.

Health, metrics и внутренние listeners не получают CORS. Сервисные ключи никогда
не передаются в браузер. CORS не заменяет серверные roles/grants/ownership checks.

## Проверки

JWT/JWKS, профиль, маркеры токенов, CORS и grants проверяются Go unit/HTTP tests.
`verify-auth-service.py` проверяет самостоятельный Auth и все четыре API в отдельном
Compose project, включая refresh/reuse/concurrency, logout и отзыв grants/devices.
Frontend находится в отдельном репозитории и здесь не изменяется.
