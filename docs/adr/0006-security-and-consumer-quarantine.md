# ADR 0006: OIDC identity mapping и устойчивый consumer quarantine

Статус: принято. Дата: 2026-10-18.

## Контекст

Catalog допускал anonymous mutations, Orders/Delivery имели только dev identity.
Orders/Delivery consumer завершались на неисправимом сообщении, Notification DLQ
не блокировал зависимые сообщения. Без отдельного решения последовательности
offset commit после DLQ мог скрыть пропущенную версию Order/Saga.

## Решение

JWT проверяет поддерживаемая OIDC-библиотека coreos/go-oidc с переиспользуемым
RemoteKeySet. Issuer, API audience, HTTPS JWKS URL и `(issuer, subject)` bindings
задаются сервером. Разрешены RS256/ES256. Роли, permissions, customer/courier IDs
и ресторанные grants берутся из неизменяемого на время процесса deployment registry.
Это небольшая foundation без User Service, самостоятельного IdP и своего JWT verifier.
Используется [документированный verifier/JWKS API](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc).

Adapters переводят principal в прежние внутренние ID и grants; application/domain
сохраняют ownership, assignment и transition checks. Catalog проверяет каждую
мутацию. Production fail-closed, dev mode только при opt-in в development/test.

Каждый consumer сохраняет прежний транзакционный Inbox. Новые локальные PostgreSQL
адаптеры дают bounded retry, durable quarantine и сервисный fence, общий также для
commands/events разных топиков Delivery. Session advisory lock сериализует решение
process/park/replay между экземплярами; business effects остаются в прежних транзакциях.
Offsets подтверждаются только после business commit или durable parking. Commit
failure не превращается в business failure. Все последующие сообщения сервиса
паркуются, пока карантин не разобран. Ошибочный key не позволяет обойти fence.

Replay — явная operational CLI-команда с аудитом, без Kafka offset commit и без
bypass доменных version gates. Исправленные bytes допустимы только как восстановление
upstream-оригинала, сохраняются отдельно от неизменяемого исходного сообщения.
Сбой между replay business commit и resolution покрывается Inbox. Автоматического
skip/discard нет. Старый Notification quarantine мигрирует с сохранением audit.

## Последствия и альтернативы

Консервативный fence жертвует throughput и доступностью business consumption
всего сервиса. Нужны alert на очередь, operational replay и контроль места в БД.
Per-aggregate fencing мог бы изолировать неисправность, но для неизвестных контрактов,
повреждённых keys и нескольких streams потребовал бы дополнительного протокола
идентификации зависимостей. Выбран более простой безопасный механизм текущего этапа.
Публичные JSON contracts, fingerprints, historical creation responses и POS
атомарность не меняются. Новые SQL-таблицы относятся только к инфраструктуре доставки
сообщений; сервисы не обращаются к чужим таблицам. Общего messaging framework нет.

Registry требует перезапуска при изменении/отзыве grant. Это осознанная цена
небольшой foundation; динамический IAM/admin UI не входит в этап. OIDC provider
и production transport credentials задаёт оператор deployment.

Запуск и recovery: [security-reliability.md](../security-reliability.md).

CI использует официальные [checkout](https://github.com/actions/checkout) и
[setup-go](https://github.com/actions/setup-go), read-only contents permission и
отключённое сохранение checkout credentials для выполняемого кода Pull Request.
