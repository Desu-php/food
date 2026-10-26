# ADR 0012: Profile API и OIDC public SPA

Статус: принято, Stage 16.

## Контекст

Отдельная Quasar/Vue PWA должна после login узнать внутренний UserID и разрешённые
рестораны. JWT verifier и deployment-owned registry уже существуют, однако
Profile API и CORS отсутствуют. Раздельные audiences требуют resource-specific
токенов, которых простой frontend flow пока не получает. Один JWT verifier без
явной проверки назначения токена не исключает ID-token substitution.

## Решение

Orders предоставляет `GET /v1/auth/me` непосредственно в HTTP adapter: это
проекция уже доверенного Principal, без нового бизнес-состояния или проходных
application слоёв. DTO содержит только user_id, roles, restaurant_ids, permissions;
массивы не бывают null, cache запрещён. Invalid/missing token даёт 401, валидный
access token с неизвестным Subject — 403. Ресурсная авторизация остаётся в API и
application, profile используется только для выбора UI и ресторана.

Для MVP общий audience food-api объединяет четыре публичных API одной доверенной
системы. Существующие service overrides сохранены; Compose наследует общий
OIDC_AUDIENCE. Это уточняет правило отдельных audiences ADR 0006/Stage 9. Public
SPA client food-pwa использует Code + PKCE S256 без client secret; его Client ID
не является API audience. Server registry остаётся источником ролей/business IDs.

Сохраняем coreos/go-oidc и его signature/issuer/audience/expiry verification.
Дополнительно допускаем только signed payload typ=Bearer или защищённый
JOSE typ=at+jwt/application/at+jwt без payload typ. Непомеченные JWT, ID и refresh
tokens отклоняются. Ключи выбираются только из настроенного JWKS, RS256/ES256.
Реальный development IdP также использует HTTPS; HTTP остаётся только test fixture.

CORS — общий HTTP middleware `platform/security`, подключённый bootstrap перед
authentication только для публичных семейств маршрутов. Явный origin allowlist,
без wildcard и cookie credentials, с обработкой preflight и Authorization header.
Внутренние endpoint, metrics и health не получают browser access.
Текущий локальный login описан в ADR 0013 и использует самостоятельный Auth.

## Последствия и ограничения

Одна SPA может обращаться к нескольким API одним токеном; компрометация токена
расширяет область его использования внутри системы. Server grants и ownership
проверяются независимо каждым сервисом. Для более строгой изоляции остаётся
возможность перейти к отдельным resource tokens с поддержкой IdP/frontend.

Ранее выпущенные untyped JWT перестают приниматься: перед rollout требуется
проверка формата действующего IdP. Бизнес-контракты/состояния и миграции не меняются.
Реестр требует согласованного restart всех API при отзыве доступа; централизованное
управление пользователями/грантами и мгновенный отзыв не реализуются этим этапом.
Отзыв ресторанных grants сохраняет требование отзыва push devices Stage 14.

Новые User/Auth Service, регистрация пользователей, social login, gateway и
изменения отдельного frontend исключены. Контракт и запуск описаны в
[auth-profile-oidc.md](../auth-profile-oidc.md).
