# Проверки Catalog Service

## Предыдущий этап: Production Foundation

Проверки выполнены в текущем окружении разработки на Go 1.27.2.

- `go test -race -count=1 ./services/catalog/...` с TEST_DATABASE_URL:
  прошли domain, application, HTTP, bootstrap и PostgreSQL integration tests.
- PostgreSQL 16.15 запускался в изолированном временном cluster в /tmp;
  интеграционные тесты действительно выполнялись, а не пропускались.
- `go vet ./services/catalog/...`: без замечаний.
- `go build -buildvcs=false -o /tmp/food-catalog-api ./services/catalog/cmd/api`:
  успешно. buildvcs=false потребовался из-за недоступной Git-метаинформации
  текущего окружения. GOCACHE был перенаправлен в /tmp.
- Compose CLI 2.40.3: `--env-file .env.example -f deploy/compose.yaml config --quiet`
  успешно проверил конфигурацию.
- migrate CLI 4.18.3 на отдельной временной БД: up, повторный up (no change),
  version, down 1, повторный up — успешно.
- Собранный бинарник с настоящей PostgreSQL: liveness/readiness, POST, PATCH,
  GET списка и по ID, пагинация, 400 и 404 — успешно.
- После остановки PostgreSQL: /health -> 200, /ready -> 503.
- SIGTERM: завершение процесса с exit code 0 и JSON-записью shutdown.
- Отдельные автоматические тесты подтвердили ожидание текущего HTTP-запроса
  при graceful shutdown и отмену запроса при превышении shutdown timeout.

Docker daemon недоступен в WSL (Docker Desktop integration не включена).
Поэтому Dockerfile build и `docker compose up` не выполнялись. Проверка Compose
config не заменяет запуск контейнеров. SQL-миграции, native binary и PostgreSQL
проверены отдельно. Временные процессы Catalog и PostgreSQL остановлены.


## Модуль меню — 2026-10-11

Проверки выполнены на Go 1.27.2 и PostgreSQL 18 в отдельном Docker-контейнере.

- Все domain/application/HTTP/bootstrap/PostgreSQL тесты выполнены с
  `TEST_DATABASE_URL=... go test -race -count=1 ./services/catalog/...` и прошли.
  Интеграционные тесты не пропускались; каждый использовал изолированную схему.
- Проверены создание и изменение категорий/блюд, целочисленная цена, TJS,
  отсутствующие ресурсы, чужой ресторан, false/0/пустая строка в PATCH,
  сортировка, недоступные блюда и сохранение строки при soft delete.
- Прямые repository-записи подтвердили FK/CHECK, включая составную связь
  restaurant/category/item. Конкурентные PATCH сохраняют разные поля;
  конкурентный UPDATE не воскрешает удалённое блюдо.
- pgx QueryTracer подтвердил ровно один SQL-запрос при загрузке полного меню
  с несколькими категориями и блюдами (без N+1).
- Миграция 000002 проверена up/down/up; down сохраняет рестораны.
- `go vet ./services/catalog/...` и native build прошли. Для native build
  использованы `-buildvcs=false` и GOCACHE в /tmp из-за ограничений окружения.
- В отличие от предыдущего этапа Docker доступен: multi-stage Dockerfile
  действительно собран, отдельный Compose-проект запущен с PostgreSQL 18,
  migrate и Catalog. Оба длительно работающих сервиса прошли healthchecks.
- HTTP E2E через собранный контейнер подтвердил весь цикл меню, Unicode,
  целочисленную цену, смену доступности, порядок, ошибки, soft delete и
  работоспособность старого POST/PATCH/GET ресторанов.
- В БД миграций version=2, dirty=false; повторный migrate вернул no change.
- После проверки тестовый Compose-проект, его volume/network и отдельный
  тестовый PostgreSQL-контейнер удалены. Работающее окружение deploy не менялось.

Административные маршруты выделены в отдельную группу регистрации, но пока
не имеют авторизации. Это явно указано в README и коде; разделение маршрутов
само по себе не делает их защищёнными для production.

## Order Service и интеграция с Catalog — 2026-10-17

Проверки выполнены на Go 1.27.2. Существующие тесты Catalog включались в каждый
общий прогон; integration tests действительно запускались, без пропусков.

- `TEST_DATABASE_URL=... GOCACHE=/tmp/food-go-build go test -race -timeout 90s -count=1 ./services/orders/... ./services/catalog/...`
  прошёл сначала с PostgreSQL 16.15 во временном native cluster, затем с
  PostgreSQL 18 в отдельном тестовом Compose-проекте. Для последнего прогона
  создана выделенная database food_integration_test; каждый тест использовал
  отдельную схему и применял реальные SQL-миграции.
- Domain: один/несколько товаров, пустой заказ, лимиты, чужой ресторан, TJS,
  переполнение int64 при умножении и суммировании, неизменяемый снимок,
  PENDING_PAYMENT → CANCELLED и запрет повторной отмены.
- Application/HTTP: создание, GET/Cancel, изоляция CustomerID, запрет цен и identity
  из JSON, expected subtotal, конфликт ключа и цены, отсутствие ресторана/блюда,
  недоступное/чужое блюдо, временный отказ Catalog, production запрет dev identity.
- Catalog adapter проверен через настоящие httptest servers: один batch request
  на несколько позиций, имена/цены, бизнес-коды, 5xx, redirect, некорректный JSON,
  неполный quote, timeout, отменённый context и закрытый сервер.
- PostgreSQL: атомарная запись заказа/позиций/ключа, принудительная ошибка последней
  записи с полным rollback, CHECK/FK, customer scope, конкурентные одинаковые ключи,
  разные payload с одним ключом, конкурентная отмена, up/down/up миграции Orders.
  Восемь одновременных HTTP-запросов дали один заказ и одинаковые ответы.
  Barrier перед возвратом quote также проверяет отсутствие занятого pool/SQL
  transaction во время HTTP: участников больше, чем соединений в pool.
- Quote Catalog: актуальная цена/имя после PATCH, закрытый ресторан, чужое,
  отсутствующее, недоступное и soft-deleted блюдо, пакет из двух блюд.
- `go vet ./services/catalog/... ./services/orders/...` прошёл. `gofmt -l`
  не вывел файлов; `sh -n deploy/init-databases.sh` прошёл.
- Каждый модуль отдельно собран с `GOWORK=off go build -buildvcs=false ... ./cmd/api`.
  В окружении Git-метаданные недоступны; buildvcs=false использован только для
  native builds. Git commit не выполнялся.
- Native E2E с двумя настоящими бинарниками и отдельными service databases:
  создание нескольких позиций, восемь конкурентных запросов, подтверждение
  изменённой цены, исторические снимки, чужой CustomerID, отмена, настоящий restart
  Orders, replay при остановленном Catalog и 503 для нового заказа.
- `init-databases.sh` проверен дважды подряд, включая старую Catalog database с
  таблицами bootstrap superuser: ownership перенесён, данные сохранены, сервисные
  роли не могут подключиться к базе соседнего сервиса.
- После остановки native PostgreSQL Orders `/health` вернул 200, `/ready` — 503
  с безопасным JSON, SIGTERM завершил процесс с кодом 0. Bootstrap tests отдельно
  подтвердили ожидание текущего запроса и forced close при shutdown timeout.

Linux-команда docker в WSL не работала, но через Windows Docker CLI удалось
обратиться к Docker Desktop 29.8.2. Поэтому проверка контейнеров выполнена:

- Compose config прошёл штатный CLI. Оба multi-stage Dockerfile собраны.
- Изолированный проект food-orders-verify-20261017 использовал собственный volume,
  сети и host ports 15432/18080/18081. PostgreSQL 18, Catalog и Orders healthy;
  init-databases и оба migrate job завершились успешно.
- Docker HTTP E2E подтвердил создание через реальный Catalog quote, конкурентную
  идемпотентность, 409 при смене цены, replay после изменения меню, отмены,
  настоящего restart Orders и отключения Catalog. GET/ready оставались доступны
  при отказе Catalog; новый заказ вернул 503. Проверено отсутствие дубликатов SQL.
- Публичный Catalog endpoint /internal/v1/menu/quote вернул 404. Из отдельного
  контейнера в edge network соединение с Catalog:8082 не удалось, поскольку
  listener привязан к backend interface. Проверена изоляция database roles.
- Повторный up существующего тестового окружения завершился успешно.
  Тестовый Compose-проект, его
  volume, сети и образы сервисов удалены после проверок; native cluster и
  сервисы остановлены. Рабочее пользовательское Compose-окружение не изменялось.

Ограничения функциональности: quote не резервирует продукты; production требует
service-to-service authentication и настоящую identity integration. Dev header
не является публичной аутентификацией. Эти границы описаны в README и ADR 0001.

## Kafka / Outbox / Inbox — 2026-10-17

В этом этапе Docker Engine 29.8.2 доступен через Linux CLI с разрешением на socket.
Использовались настоящие PostgreSQL 18 и Redpanda v26.2.5, Go 1.27.2.

Выполнено успешно:

- `GOCACHE=/tmp/food-go-cache sh scripts/test-integration.sh`: все unit, HTTP,
  PostgreSQL и Kafka tests четырёх модулей с `go test -race -count=1`, затем `go vet`.
  TEST_DATABASE_URL и TEST_KAFKA_BROKERS заданы, интеграционные тесты не пропущены.
- `python3 scripts/verify-events.py`: Docker multi-stage builds, startup init jobs,
  миграции, readiness всех сервисов и **двух** экземпляров Outbox Worker.
  Redpanda реально остановлен; POST /orders продолжил работать, HTTP replay
  сохранил единственный Outbox, attempts росли. После восстановления брокера
  появился OrderCreated intent. Отмена создала OrderCancelled intent.
  Оба события повторно опубликованы через Outbox, consumer залогировал два duplicate
  skip, в Notification PostgreSQL осталось ровно два PENDING и сохранены correlation IDs.
  Повторный полный запуск подтвердил работу Notifications с собственной
  несуперпользовательской сервисной ролью и отдельным bootstrap admin.
- `docker compose ... config -q` для обоих Compose-файлов.
- Независимые native builds Catalog API, Orders API/Outbox, Notifications consumer:
  `GOWORK=off go build -buildvcs=false`; shared contracts разрешаются без go.work.
- `gofmt`, `sh -n` для init/test scripts, Python syntax compilation.

Соответствие обязательным сценариям:

| Требование | Выполненная проверка |
|---|---|
| 1–2. Atomic order + Outbox / rollback | OrderAtomicityAndConstraints, OutboxAtomicCreationCancellationAndIdempotency, OutboxCancellationRollback |
| 3. Восстановление Kafka | Compose E2E с реальным stop/start Redpanda и проверкой SQL |
| 4. Повторная публикация | OutboxKafkaAcknowledgementCrashAndRecovery + KafkaInboxCommitAfterPersistence + Compose E2E |
| 5–6. Два claim / восстановление | OutboxConcurrentClaimsRecoveryAndFencing, stale token rejected; два worker в E2E |
| 7–8. Created и дедупликация | ConsumerCreatedDuplicateAndCancelled, InboxConcurrentDuplicatesAndReorderedVersions, KafkaInboxCommitAfterPersistence |
| 9. Ошибка SQL без offset commit | ConsumerDatabaseFailureNeverCommits; реальный Kafka consumer перезапущен после отказа SQL, получил тот же offset |
| 10. Cancelled | Unit, PostgreSQL/Kafka integration и Compose E2E |
| 11. Idempotency-Key без нового события | ConcurrentOrderCreationHTTP с проверкой числа Outbox, Compose HTTP replay при недоступном брокере |
| 12. Конкурентная согласованность | PostgreSQL Inbox (12 конкурентных дублей), Outbox claims, HTTP idempotency/cancel; race detector |

Дополнительно проверены DEAD/requeue, retention только PUBLISHED, up/down/up новых
миграций, rollback Inbox при неудаче Notification INSERT, replay карантина,
конфликт ID/version, перестановка v2 → v1, неизвестная schema и malformed JSON,
сохранение offsets после restart consumer, возврат ошибки offset commit.

Найденная Kafka-интеграционным тестом ошибка потери leader_epoch при ручном commit
исправлена; повторный consumer restart и полный race suite прошли.
Первый native build столкнулся с недоступной Git-метаинформацией, поэтому повторён
с `-buildvcs=false`. Default GOCACHE read-only; использован временный writable cache.
Начальная форма CLI аргумента Redpanda заменена bootstrap YAML; реальный запуск
подтвердил конфигурацию. Невыполненных обязательных проверок не осталось.

Оба тестовых Compose-проекта, их volumes и сети удалены. Пользовательские `.env`
и рабочее окружение не изменялись. Git commit не выполнялся.

## Restaurant Fulfillment + Delivery + POS (2026-10-17)

Выполнено на Go 1.27.2. Использованы отдельные тестовые PostgreSQL 18 и Redpanda
из deploy/compose.test.yaml (проект food-fulfillment-test). Рабочие БД не изменялись.

- Полный `go test -race -count=1 ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/...`
  с TEST_DATABASE_URL и TEST_KAFKA_BROKERS: PASS. PostgreSQL и Kafka integration
  tests выполнялись, а не пропускались.
- `go vet` по тем же модулям: PASS.
- Независимая сборка Catalog, Orders, Notifications и Delivery:
  `GOWORK=off GOCACHE=/tmp/food-go-cache go build -buildvcs=false ./...`: PASS.
  Обычный go build в этом workspace завершался ошибкой получения VCS status;
  исходники предоставлены без доступного Git repository. В Docker сборки прошли
  без этого флага: .git не входит в build context.
- `python3 scripts/verify-fulfillment.py`: PASS, повторён после изменения DTO и
  SQL-чтения. Полная сборка Compose, миграции, два Order Outbox и два Delivery
  Outbox workers, ресторанный цикл, POS, остановка брокера/consumer и восстановление.
- Проверены accept/reject и server-owned grants, запрещённые переходы, создание
  Delivery, конкурентный Inbox, невозможность двойного назначения одного курьера
  и повторного назначения доставки, pickup только готового заказа назначенным
  курьером, отказ complete без подтверждения и отказ клиентской суммы.
- Проверены атомарность Delivery/POS/Outbox и Order/Inbox/payment status/Outbox,
  rollback при отказе последней записи, конкурентные повторные complete с одной
  POS-записью и одним DeliveryCompleted, повтор обеих Kafka streams и отсутствие
  дубликатов бизнес-эффектов.
- Реальный Kafka test проверил ошибку SQL без ACK, рестарт consumer, повтор после
  DB COMMIT до offset commit и повторную публикацию OrderAccepted.
- Миграции Delivery и Orders fulfillment: up/down/up; legacy PENDING_PAYMENT
  переводится в WAITING_RESTAURANT, исторический idempotency response читается.
  Отдельный тест подтвердил, что down с принятым заказом отклоняется и сохраняет
  заказ и его OrderAccepted.
- `sh -n` для init-databases.sh, init-topics.sh и test-integration.sh, Python
  compilation для обоих e2e scripts: PASS.

В production identity adapter не добавлен: dev identities доступны только при
явном opt-in в development/test; production с включённым dev auth запрещён.
POS-подтверждение остаётся операционным, банковская сверка не выполнялась.
- Регрессионный `python3 scripts/verify-events.py`: PASS на итоговых образах;
  прежний сценарий создания/отмены, недоступности брокера, двух workers, повторной
  публикации, correlation_id и Notifications сохранён. Его тестовый Compose-проект
  также удалён автоматически.

## Этап 8: persisted Fulfillment Saga (2026-10-18)

Выполнено на Go 1.27.2, выделенных PostgreSQL 18 и Redpanda v26.2.5.
Рабочие БД/volumes не использовались. Новая messaging-инфраструктура не добавлялась.

- Полный `go test -race -count=1 ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/...`
  с TEST_DATABASE_URL и TEST_KAFKA_BROKERS: PASS. Реальные SQL/Kafka tests выполнялись.
- `go vet` по всем пяти модулям: PASS.
- Независимые сборки четырёх сервисов с `GOWORK=off go build -buildvcs=false ./...`:
  PASS. Флаг отключает VCS stamping для этого workspace без доступного Git repository.
- `verify-fulfillment.py`: PASS — полный POS-цикл через Saga, остановка брокера,
  восстановление consumer, два Saga workers, два workers каждого Outbox, повтор
  потоков и Notifications.
- `verify-saga.py`: PASS, повторён после отделения Application-решений от SQL и
  усиления startup/readiness проверок. Недоступный Delivery consumer, сохранённый
  retry, остановка/перезапуск Order процессов посреди Saga, продолжение из БД,
  запрет приготовления до назначения, отказ курьера после готовки, NEEDS_ATTENTION
  без отмены еды, операторский resume, повторное назначение и успешный POS.
- В том же e2e проверены timeout с компенсацией до готовки, отказ ресторана после
  принятия, OFFLINE/AVAILABLE, запрет назначения недоступного курьера, отмена
  клиентом и идемпотентный replay всех CreateDelivery/CancelDelivery.
- Unit/SQL tests проверили атомарный accept + Saga + command Outbox, retry с тем же
  MessageID/body, восстановление attempts/deadline после создания нового repository,
  escalation и авторизованный retry, запрет автоматической отмены после готовки.
- Реальный Kafka test проверил Delivery COMMIT до ACK, закрытие consumer и повтор
  той же команды новым consumer без второй Delivery/Outbox-эффекта. Сохранились
  проверки повторного DeliveryCompleted, конкурентного complete и отказа последней
  Outbox-записи с rollback Delivery/payment/courier или Order/Inbox/Saga.
- SQL constraints/row locks проверены для двух конкурентных назначений, повторного
  назначения одной Delivery, недоступного курьера, чужого курьера и POS без явного
  подтверждения. Сумма от HTTP отклоняется; receipt/ID подтверждения сохраняются.
- Проверены неправильный порядок Delivery events без ACK/Inbox partial write,
  stale duplicate без регресса Order, конфликт MessageID/version/payload и
  cancellation tombstone: Cancel раньше Create и поздний новый Create не
  восстанавливают отменённую задачу.
- Миграции Orders 000004 и Delivery 000002: пустой up/down/up и сохранение старого
  POS receipt/суммы/подтверждения. Legacy READY_FOR_PICKUP без courier_id корректно
  принимает уже опубликованный pickup после миграции. PENDING_PAYMENT и immutable
  creation responses продолжают проверяться существующими тестами.
- Отдельно проверена защита отката Saga после retention-очистки Outbox: command
  sequence хранится в Saga и не позволяет удалить её историю.
- После проверки позднего ответа добавлен regression test: DeliveryCreated после
  timeout компенсации не стирает CancelDelivery/NEEDS_ATTENTION; business cause
  restaurant_rejected сохраняется отдельно от технической причины timeout.
  Полный SQL/Kafka/race прогон после исправления: PASS.
- Shell syntax, Python compilation и gofmt: PASS.

Ограничения: production identity adapter не реализован; dev identity запрещена в
production. Подтверждение POS остаётся заявлением курьера. Retention и действия
оператора при DEAD/poison messages описаны в ADR 0004; глобальный exactly-once не
обещается. Автоматического выбора курьера, банковской сверки и отката физических
действий нет.

Дополнительные заключительные проверки этапа 8:

- `python3 scripts/verify-events.py`: PASS на итоговых образах — сохранён прежний
  цикл Catalog/Orders/Notifications, broker outage/recovery, два Outbox workers,
  повторная публикация, correlation_id и HTTP idempotency.
- `GOCACHE=/tmp/food-go-cache sh scripts/test-integration.sh`: PASS после последнего
  изменения migration reconciliation. Runner выполнил полный race-прогон и vet
  на реальных PostgreSQL/Redpanda и автоматически удалил свои контейнеры.
- Legacy migration test дополнительно проводит Saga через timeout/NEEDS_ATTENTION
  перед уже опубликованным pickup. Отдельный persisted legacy flag сохраняет
  возможность связать старый courier_id независимо от технической причины сбоя.
- Все проекты проверки (food-saga-test, food-events-test, food-fulfillment-e2e,
  food-saga-e2e и food-events-e2e) остановлены и их тестовые данные удалены.

## Расширение этапа 8: PICKUP + DELIVERY — 2026-10-18

Проверено на Go 1.27.2, PostgreSQL 18 и настоящей Redpanda в изолированных
Compose-проектах. Рабочее окружение и его volumes не изменялись.

- Полный `go test -race -count=1 ./contracts/... ./services/catalog/... ./services/orders/... ./services/delivery/... ./services/notifications/...`
  с TEST_DATABASE_URL и TEST_KAFKA_BROKERS: PASS, SQL и Kafka integration tests
  выполнялись. После добавления предварительной проверки кода, точного legacy
  replay и нормализации времени повторно выполнены все Orders tests с PostgreSQL
  18 и race detector: PASS.
- Domain/Application/HTTP: обязательный тип нового заказа, DELIVERY без адреса,
  PICKUP без клиентского адреса, нулевой сбор, POS_AT_PICKUP/UNPAID, Catalog quote,
  приготовление сразу после принятия, запрет выдачи неготового/неоплаченного заказа,
  запрет courier transitions и отмены готовой еды. PICKUP не создаёт Saga/commands;
  отмена до готовки локальна. Исторический request hash проверен по точному JSON
  прежнего формата; отсутствующий тип разрешает только сохранённый legacy replay.
- PostgreSQL/HTTP: чужие grants и customer ID, неверный/истёкший/ротированный код,
  пять попыток, сохраняемый cooldown, ротация, проверка до POS без бизнес-эффектов,
  12 конкурентных выдач и один POS/Outbox, повтор после рестарта/истечения кода,
  конфликт сотрудника/чека, отсутствие кода в логах, immutable creation result.
  Искусственный отказ OrderPickedUpByCustomer Outbox откатывает оплату и статус.
- Orders 000005: up/down/up и отказ down при наличии PICKUP, сохранение всех старых
  полей завершённого DELIVERY, неизвестный legacy address без выдуманных фактов.
  Существующие тесты старых миграций и сохранения POS-аудита тоже прошли.
- Catalog: хранение/валидация адреса, snapshot адреса и цен в batch quote;
  Delivery: адрес команды сохраняется, изменение адреса отвергается; PICKUP events
  сохраняют Inbox/версии без Delivery, POS, Outbox или preparation projection.
  Notifications: готовность/выдача PICKUP сохраняют по одному PENDING intent с
  fulfillment_type, повторные события дедуплицируются.
- Canonical JSON исторического order event остаётся побайтово прежним;
  новые optional поля отсутствуют при чтении старых envelopes.
- `go vet` по всем пяти модулям, gofmt, Compose config и синтаксис Python-скриптов:
  PASS. Независимые `GOWORK=off go build -buildvcs=false ./...` для четырёх сервисов
  и contracts: PASS. Multi-stage Docker builds всех сервисов выполнены через E2E.
- `python3 scripts/verify-pickup.py`: PASS. Реальный internal Catalog quote,
  предварительная проверка кода, POS/выдача, рестарт Orders, одинаковый ответ
  повтора, отсутствие Saga/Delivery у PICKUP, пять Kafka intents, delivery Inbox
  без бизнес-эффектов и полный успешный DELIVERY/Saga/POS в том же окружении.
  Выявленное расхождение nanos/micros первого ответа и replay устранено;
  временный connection refused после рестарта корректно ожидается тестом.
- `python3 scripts/verify-saga.py`: PASS на новых контрактах. Restart/retry,
  preparation gate, courier decline/reassignment, отсутствие автоматической
  отмены приготовленной еды, timeout compensation, restaurant/customer cancel,
  offline courier, POS и повтор всех направленных команд.

Тестовые контейнеры, сети и volumes удалены. Исходные SQL-миграции и исторические
Outbox/Inbox/creation responses не переписаны. Существующие ограничения сохранены:
production identity provider ещё не подключён; Notifications сохраняет PENDING
intents без фактического SMS/email provider. POS — операционное заявление,
банковская проверка и сверка не добавлены.


## Этап 9: Security & Reliability Hardening — 2026-10-18

Финальный локальный эквивалент обоих CI jobs выполнен успешно:

- `./scripts/check.sh`: PASS — gofmt, vet, unit/HTTP tests, race, GOWORK=off build
  всех шести модулей, validation полного и тестового Compose без вывода конфигурации.
- `./scripts/test-integration.sh`: PASS — реальные PostgreSQL 18 и Redpanda,
  up/down миграций четырёх сервисов, все модули с race, vet и очистка стенда.
- `python3 scripts/verify-pickup.py`: PASS — PICKUP без Delivery/Saga, POS handoff,
  токены, immutable creation retries после restart, Kafka и DELIVERY/Saga/POS.
- `python3 scripts/verify-saga.py`: PASS — restart/retry, preparation gate,
  отказ/переназначение курьера, timeout compensation, запрет автоотмены после готовки,
  отмены ресторана/клиента, offline courier, POS и command replay.
- `python3 scripts/verify-events.py`: PASS — остановка/восстановление брокера,
  два Outbox workers, HTTP idempotency, cancellation, повторная публикация,
  Inbox deduplication, correlation IDs и PENDING intents.
- Новый `TestKafkaPoisonParkingRestartAndBusinessRecovery`: PASS с race на
  настоящем broker/PostgreSQL — crash после durable parking до offset commit,
  повтор того же offset после restart, fence зависимой команды и audit replay
  с единственными Delivery/command Inbox.
- Security tests: PASS — подпись/issuer/audience/exp/nbf, unsigned/malformed JWT,
  HS256 rejection, wrong RSA signature, JWKS kid rotation, opaque subject mapping,
  игнорирование spoofed roles/customer_id, запрет всех dev headers в production,
  anonymous Catalog mutation/чужой ресторан/чужая доставка, scoped dispatcher grants.
- Quarantine tests трёх сервисов: PASS — durable fence после restart, replay audit,
  неизменность original bytes, сбой parking без разрешения offset commit,
  восстановление temporary failure, concurrent workers, отмена lock context,
  запрет downgrade с audit/парковкой. Проверены прежние бизнес-регрессии всех модулей.
- `sh -n`, Python AST parsing тестовых scripts и `git diff --check`: PASS.

Один дополнительный общий прогон на фоне Docker E2E встретил временный producer
record timeout в прежнем `TestOutboxKafkaAcknowledgementCrashAndRecovery`.
Изолированный запуск этого теста `-race -count=3` прошёл три раза, последующий
финальный `scripts/test-integration.sh` прошёл полностью. Producer timeout и
бизнес-код Outbox ради обхода этой ошибки не менялись. Первичные sandbox-отказы
loopback/Docker были устранены запуском проверок с разрешённым доступом.

GitHub-hosted Actions ещё не выполнялся: workflow добавлен, Git commit/push
не выполнялись. Не проверялось подключение конкретного production IdP:
проверка JWT/JWKS использовала локальный настоящий HTTP JWKS server с RSA keys.
Временные Compose проекты удалены. Production registry, TLS и мониторинг очереди
должен настроить оператор. Service-wide fence намеренно ограничивает throughput
и блокирует business consumption всего сервиса до audited recovery; grants
обновляются перезапуском API. Детали и команды — [runbook](security-reliability.md).

### Изменённые файлы этапа 9

- [.env.example](../.env.example)
- [.github/workflows/ci.yml](../.github/workflows/ci.yml)
- [AGENTS.md](../AGENTS.md)
- [README.md](../README.md)
- [contracts/delivery/commands/v1/command.go](../contracts/delivery/commands/v1/command.go)
- [contracts/delivery/v1/event.go](../contracts/delivery/v1/event.go)
- [deploy/auth-identities.example.json](../deploy/auth-identities.example.json)
- [deploy/compose.yaml](../deploy/compose.yaml)
- [docs/adr/0006-security-and-consumer-quarantine.md](../docs/adr/0006-security-and-consumer-quarantine.md)
- [docs/architecture.md](../docs/architecture.md)
- [docs/security-reliability.md](../docs/security-reliability.md)
- [docs/verification.md](../docs/verification.md)
- [go.work](../go.work)
- [go.work.sum](../go.work.sum)
- [platform/go.mod](../platform/go.mod)
- [platform/go.sum](../platform/go.sum)
- [platform/security/security.go](../platform/security/security.go)
- [platform/security/security_test.go](../platform/security/security_test.go)
- [scripts/check-migrations.sh](../scripts/check-migrations.sh)
- [scripts/check.sh](../scripts/check.sh)
- [scripts/test-integration.sh](../scripts/test-integration.sh)
- [scripts/verify-events.py](../scripts/verify-events.py)
- [scripts/verify-pickup.py](../scripts/verify-pickup.py)
- [services/catalog/Dockerfile](../services/catalog/Dockerfile)
- [services/catalog/go.mod](../services/catalog/go.mod)
- [services/catalog/go.sum](../services/catalog/go.sum)
- [services/catalog/internal/adapters/http/handler.go](../services/catalog/internal/adapters/http/handler.go)
- [services/catalog/internal/adapters/http/handler_test.go](../services/catalog/internal/adapters/http/handler_test.go)
- [services/catalog/internal/adapters/http/menu.go](../services/catalog/internal/adapters/http/menu.go)
- [services/catalog/internal/adapters/http/menu_test.go](../services/catalog/internal/adapters/http/menu_test.go)
- [services/catalog/internal/adapters/http/security_test.go](../services/catalog/internal/adapters/http/security_test.go)
- [services/catalog/internal/bootstrap/config.go](../services/catalog/internal/bootstrap/config.go)
- [services/catalog/internal/bootstrap/run.go](../services/catalog/internal/bootstrap/run.go)
- [services/delivery/Dockerfile](../services/delivery/Dockerfile)
- [services/delivery/README.md](../services/delivery/README.md)
- [services/delivery/cmd/consumer/main.go](../services/delivery/cmd/consumer/main.go)
- [services/delivery/go.mod](../services/delivery/go.mod)
- [services/delivery/go.sum](../services/delivery/go.sum)
- [services/delivery/internal/adapters/http/handler.go](../services/delivery/internal/adapters/http/handler.go)
- [services/delivery/internal/adapters/http/security_test.go](../services/delivery/internal/adapters/http/security_test.go)
- [services/delivery/internal/adapters/postgres/consumer_quarantine.go](../services/delivery/internal/adapters/postgres/consumer_quarantine.go)
- [services/delivery/internal/adapters/postgres/consumer_quarantine_integration_test.go](../services/delivery/internal/adapters/postgres/consumer_quarantine_integration_test.go)
- [services/delivery/internal/adapters/postgres/delivery_integration_test.go](../services/delivery/internal/adapters/postgres/delivery_integration_test.go)
- [services/delivery/internal/adapters/postgres/kafka_integration_test.go](../services/delivery/internal/adapters/postgres/kafka_integration_test.go)
- [services/delivery/internal/adapters/postgres/security_integration_test.go](../services/delivery/internal/adapters/postgres/security_integration_test.go)
- [services/delivery/internal/application/inbox.go](../services/delivery/internal/application/inbox.go)
- [services/delivery/internal/bootstrap/config.go](../services/delivery/internal/bootstrap/config.go)
- [services/delivery/internal/bootstrap/consumer.go](../services/delivery/internal/bootstrap/consumer.go)
- [services/delivery/internal/bootstrap/run.go](../services/delivery/internal/bootstrap/run.go)
- [services/delivery/migrations/000004_consumer_quarantine.down.sql](../services/delivery/migrations/000004_consumer_quarantine.down.sql)
- [services/delivery/migrations/000004_consumer_quarantine.up.sql](../services/delivery/migrations/000004_consumer_quarantine.up.sql)
- [services/notifications/cmd/consumer/main.go](../services/notifications/cmd/consumer/main.go)
- [services/notifications/internal/adapters/postgres/consumer_quarantine.go](../services/notifications/internal/adapters/postgres/consumer_quarantine.go)
- [services/notifications/internal/adapters/postgres/consumer_quarantine_integration_test.go](../services/notifications/internal/adapters/postgres/consumer_quarantine_integration_test.go)
- [services/notifications/internal/adapters/postgres/inbox_integration_test.go](../services/notifications/internal/adapters/postgres/inbox_integration_test.go)
- [services/notifications/internal/application/consumer.go](../services/notifications/internal/application/consumer.go)
- [services/notifications/internal/bootstrap/run.go](../services/notifications/internal/bootstrap/run.go)
- [services/notifications/migrations/000005_consumer_quarantine.down.sql](../services/notifications/migrations/000005_consumer_quarantine.down.sql)
- [services/notifications/migrations/000005_consumer_quarantine.up.sql](../services/notifications/migrations/000005_consumer_quarantine.up.sql)
- [services/orders/Dockerfile](../services/orders/Dockerfile)
- [services/orders/cmd/consumer/main.go](../services/orders/cmd/consumer/main.go)
- [services/orders/go.mod](../services/orders/go.mod)
- [services/orders/go.sum](../services/orders/go.sum)
- [services/orders/internal/adapters/http/handler.go](../services/orders/internal/adapters/http/handler.go)
- [services/orders/internal/adapters/http/restaurants.go](../services/orders/internal/adapters/http/restaurants.go)
- [services/orders/internal/adapters/http/security_test.go](../services/orders/internal/adapters/http/security_test.go)
- [services/orders/internal/adapters/postgres/consumer_quarantine.go](../services/orders/internal/adapters/postgres/consumer_quarantine.go)
- [services/orders/internal/adapters/postgres/consumer_quarantine_integration_test.go](../services/orders/internal/adapters/postgres/consumer_quarantine_integration_test.go)
- [services/orders/internal/adapters/postgres/orders_integration_test.go](../services/orders/internal/adapters/postgres/orders_integration_test.go)
- [services/orders/internal/application/delivery_consumer.go](../services/orders/internal/application/delivery_consumer.go)
- [services/orders/internal/bootstrap/config.go](../services/orders/internal/bootstrap/config.go)
- [services/orders/internal/bootstrap/consumer.go](../services/orders/internal/bootstrap/consumer.go)
- [services/orders/internal/bootstrap/run.go](../services/orders/internal/bootstrap/run.go)
- [services/orders/migrations/000006_consumer_quarantine.down.sql](../services/orders/migrations/000006_consumer_quarantine.down.sql)
- [services/orders/migrations/000006_consumer_quarantine.up.sql](../services/orders/migrations/000006_consumer_quarantine.up.sql)

## Этап 13 — Delivery Zones & Pricing (2026-10-18)

Выполнено локально на итоговом Go-коде:

- `GOCACHE=/tmp/food-go-cache ./scripts/check.sh`: PASS. gofmt, go vet,
  go test, go test -race для всех шести модулей, независимые GOWORK=off builds,
  обе Compose configurations.
- `GOCACHE=/tmp/food-go-cache ./scripts/test-integration.sh`: PASS.
  PostgreSQL 18, Redpanda и Redis; полный round trip миграций Catalog, Orders,
  Delivery, Notifications; integration suite с race, включая новые pricing tests.
- `python3 scripts/verify-pricing.py`: PASS. Настоящий quote v2, grants/CRUD,
  minimum/zone/disabled fulfillment, PICKUP с нулевым fee, 409 с новой суммой,
  конкурентное изменение тарифов и создание заказов, immutable retries,
  ручное назначение и полный POS/DeliveryCompleted/Orders Saga.
- `python3 scripts/verify-offers.py`: PASS с платной доставкой 1000 minor units:
  subtotal=25000, total=26000. Сохранены nearest offer, ownership, cooldown,
  concurrent acceptance, Kafka outage/recovery, preparation gate,
  cancellation, TTL recovery после рестарта.
- `python3 scripts/verify-pickup.py`: PASS. Выдача по секрету, POS, повтор после
  рестарта, отсутствие Delivery/Saga у PICKUP, Notifications и совместный DELIVERY.
- `python3 -m py_compile` обновлённых E2E scripts и `git diff --check`: PASS.

Первый запуск внутри sandbox не мог использовать httptest TCP listeners и Docker;
последующие проверки выполнены с разрешённым доступом. Во время одного полного
integration run прежний TestDispatchRespectsConsumerQuarantine не получил ожидаемую
lease (false, nil). Повторный полный прогон прошёл без изменения механизма dispatch;
ошибка не воспроизвелась. Начальные ошибки адаптации fixtures/порядка rollback и
времени тестового DeliveryCompleted исправлены; они не считаются успешными прогонами.

Результаты проверяют локальный тестовый deployment. Production OIDC providers,
нагрузочный предел, geocoding и road routing этим модулем не проверяются.
