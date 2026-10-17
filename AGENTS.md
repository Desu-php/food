# AGENTS.md — Food Delivery

## Project and communication

Build a production-oriented food delivery monorepo on Go. Communicate in Russian;
use English identifiers and code comments. Explain architectural decisions,
trade-offs and complex mechanisms, rather than every line of Go.

## Workflow

One task is one complete functional module, not one file or endpoint.
Read existing code and relevant instructions before making changes. Implement the
entire authorized module through a working result: code, infrastructure,
appropriate tests and documentation. Do not stop for approval after each file or
endpoint. Run available checks, fix failures and report checks that could not run.
Do not implement subsequent large modules without a separate user task.
Do not make Git commits without explicit authorization.

## Architecture

Use Clean Architecture and DDD from the beginning, without excessive abstractions.
Domain owns entities, invariants and domain errors and depends on neither HTTP nor
PostgreSQL. Application owns use cases and repository ports. Adapters implement
HTTP and persistence. Bootstrap loads configuration and wires dependencies through
constructors. Dependencies point inward.
Keep each microservice independently buildable and responsible for its own data.
Do not create empty packages or future services. Avoid generic repositories,
pass-through layers, heavy DI frameworks and abstractions without a concrete need.

## Technology and quality

Use current stable Go compatible with the environment, standard net/http and
ServeMux, PostgreSQL with pgx/v5, SQL migrations, Docker Compose, log/slog and
standard testing. No Gin, GORM or third-party libraries without a clear need.
Do not introduce Kafka, Redis, Kubernetes or other services unless requested.
Write idiomatic, maintainable Go; use gofmt, explicit dependencies and context for
I/O. Handle and wrap errors with useful context; use errors.Is where appropriate.
Avoid global mutable state. Protect shared mutable data against concurrent access.
Use parameterized SQL, validate inputs, keep server-owned fields server-controlled,
and avoid exposing internal failures or credentials in HTTP responses and logs.
Configure timeouts, graceful shutdown and resource cleanup.

## Testing and documentation

Use unit tests for business rules, httptest for HTTP and optional integration tests
for PostgreSQL using a dedicated test database. Test success, validation, missing
resources, updates and relevant concurrency behavior. Avoid unnecessary mocks and
implementation-mirroring tests. Never claim unexecuted tests passed.
Keep README instructions for setup, migrations, API, tests and extension current.
After completing a module, explain its structure, dependency direction, important
mechanisms, validation performed and how to run it. Suggest at most one next module
without implementing it.

## Current scope

Production Foundation, Catalog, Orders, Notifications, Restaurant Order
Fulfillment Saga orchestration, PICKUP and Delivery with operational POS collection. Future modules require
separate tasks. The user's current explicit instructions take precedence over this file.

## MVP payment and fulfillment decision

The MVP has no online payment. Customers pay on receipt using a physical POS terminal. DELIVERY uses
POS_ON_DELIVERY; PICKUP uses POS_AT_PICKUP. New orders require fulfillment_type,
start UNPAID / WAITING_RESTAURANT, and never accept client-owned payment facts. Never add Payment Service, bank APIs, authorization, capture,
refund or payment Sagas without an explicit new business decision.

Orders owns restaurant fulfillment and order/payment states. Delivery owns
couriers, delivery tasks and operational POS collection confirmations in its own
database. Completion, POS confirmation and DeliveryCompleted Outbox must commit
atomically. Orders handles that single event through its own transactional Inbox,
changing DELIVERED and REPORTED_COLLECTED together. REPORTED_COLLECTED records a
courier statement, not bank verification. Retain confirmation data for future
reconciliation; reconciliation is outside the current scope.

Never write another service's tables. Reuse existing Redpanda, Outbox and Inbox;
keep event identities, historical contracts and immutable creation responses
compatible. Dev identities require explicit opt-in, server-owned restaurant and
dispatcher grants, and must be rejected in production.

## Stage 8: persisted Order Fulfillment Saga

Orders owns the fulfillment orchestrator; do not create a Saga service or a
universal workflow framework. Use the existing Redpanda and transactional
Outbox/Inbox. Directed CreateDelivery and CancelDelivery commands use a separate
versioned contract/topic from events. Persist Saga state, deadlines, retries and
outgoing commands atomically. Recover due work from PostgreSQL after restart.

The DELIVERY preparation policy is: restaurant acceptance -> delivery creation ->
courier assignment -> cooking. PICKUP can cook immediately after acceptance and
never creates a Delivery, Saga row, command, courier timeout or compensation. WAITING_COURIER is an order status. Domain methods must
enforce the preparation gate, cancellation and reassignment invariants.

Before cooking, an assignment timeout may request cancellation only under the
configured policy. Cancellation is a separate idempotent business command, not
SQL rollback; report final cancellation after the remote result. After cooking
starts, a missing courier requires another manual assignment or operator action.
Never automatically cancel prepared food, a picked-up order or a completed
physical delivery. Persist NEEDS_ATTENTION with a diagnosable reason and allow an
authorized operator to retry/resume without inventing delivery or payment facts.
Courier refusal before pickup releases assignment and keeps fulfillment active.
Couriers have OFFLINE/AVAILABLE/BUSY and both creation/update timestamps.

POS collection stays exclusively operational: payment_confirmations, delivery
completion and DeliveryCompleted Outbox commit together. Preserve historical
integration contracts and audit records when migrating. Do not add online
payments, banking APIs, Temporal, Event Sourcing, automatic courier selection,
GPS, route planning, Redis or Kubernetes in this stage.


## Stage 8 extension: PICKUP + DELIVERY

Keep both fulfillment types in the same Order aggregate and shared domain rules.
Fulfillment type and location snapshots are immutable from creation. DELIVERY
requires a customer address. PICKUP snapshots the restaurant-owned Catalog address;
restaurants without a configured address cannot accept pickup. Delivery fees stay
zero until a real server-side tariff is introduced by a separate business task.

PICKUP lifecycle: WAITING_RESTAURANT -> ACCEPTED -> PREPARING -> READY_FOR_PICKUP
-> PICKED_UP_BY_CUSTOMER. Local cancellation is allowed only before cooking.
Never apply courier-specific domain transitions or Delivery facts to PICKUP.

Pickup handoff requires server-owned restaurant grants, a customer-presented
server-generated secret and explicit POS confirmation. Keep only token hashes in
PostgreSQL; retain expiry, failed-attempt count and lockout across restarts. Never
log tokens or include them in order lists or integration events. Only the customer
may issue/rotate their ready order's token. Staff can verify it before physical POS;
completion re-verifies under the order lock and never trusts an earlier check. Confirmation audit, completed order
and OrderPickedUpByCustomer Outbox commit atomically; retries must not duplicate
payment or events. REPORTED_COLLECTED is an operational statement, not bank proof.

Notifications preserves fulfillment_type for pickup-ready intents. Delivery
acknowledges pickup event identities/versions through Inbox without delivery
business effects. Preserve optional-field canonical compatibility and historical
creation responses, including exact retries of historical requests without a
fulfillment type; never accept that shape for a new creation. Historical delivery addresses stay explicitly unknown; do not
invent addresses, pickup facts or payment confirmations during migration.
See docs/adr/0005-pickup-and-delivery.md and docs/pickup.md.
