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

Production Foundation, Catalog, Orders, Notifications, Independent Auth Service, Restaurant Order
Fulfillment Saga orchestration, PICKUP and Delivery with operational POS collection, Security & Reliability Hardening, Automatic Courier Dispatch + Redis GEO, Courier Offer & Acceptance, Real-Time Courier Tracking, Delivery Zones & Pricing, Push Notifications & Firebase FCM, Auth Profile API & Real OIDC Integration. Future modules require
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


## Stage 9: security and consumer reliability

HTTP mutations require an authenticated server-mapped principal. OIDC uses trusted
HTTPS issuer/JWKS and distinct API audiences; allow only RS256/ES256 and require
expiration. Never parse JWT subject as a numeric business ID or authorize from
client-selected roles/restaurant/courier claims. Keep the deployment-owned identity
registry and explicit restaurant/dispatcher permissions; no User Service is needed.
Production must fail closed; dev identities require development/test opt-in.

Preserve transactional Inbox/Outbox and historical canonical contracts. Production
consumers park poison and dependent messages durably behind a service-wide fence,
including both Delivery input streams; offset commit follows business or parking
commit. Never bypass the fence, silently skip a version or invent facts during
recovery. Operational replay requires audit and uses the original domain gates.
Monitor open consumer_quarantine even when Kafka lag is low. See
[security/recovery runbook](docs/security-reliability.md) and
[ADR 0006](docs/adr/0006-security-and-consumer-quarantine.md).

Run scripts/check.sh and scripts/test-integration.sh; CI covers every Go module,
independent builds, PostgreSQL/Redpanda, migration round trips and Compose validation.


## Stage 10: automatic courier dispatch and Redis GEO

Delivery owns automatic dispatch inside its API process; do not create a Dispatch
service or another Saga. Catalog owns optional paired restaurant coordinates;
Orders obtains and validates a private quote and persists an immutable DELIVERY
snapshot. Only the versioned CreateDelivery command carries that snapshot to
Delivery. Preserve historical NULL coordinates, addresses, events and canonical
Inbox fingerprints; new optional contract fields must use omitempty. Roll out
migrations and all consumers before new producers.

Redis GEO is an ephemeral candidate index, never assignment authority. Authenticate
courier GPS through the server-mapped principal; reject body courier IDs. Validate
coordinates, apply Redis member freshness and a shared rate limit, and remove or
exclude stale entries. Never persist each GPS update in PostgreSQL. Redis GEO's
latitude projection limit is explicit; polar restaurants require manual assignment.
Geographic distance is approximate and must never be called road ETA.

Claim due PENDING_ASSIGNMENT with a short PostgreSQL SKIP LOCKED lease and UUID
fencing token. Close the transaction before Redis I/O. Recheck lease, deadline,
preparation/cancellation state, candidate freshness, AVAILABLE and decline cooldown
in a new short transaction. Reuse domain.Assign, courier/delivery row locks, the
active-courier unique index and existing transactional CourierAssigned Outbox.
Manual assignment competes through the same locks. Multiple workers must be safe.
Cooperate with the existing consumer quarantine fence during SQL assignment.

Saga publishes/persists the dispatch deadline. Bounded retries never extend it;
only authorized Saga retry may issue a new command/window for an active order.
Worker never cancels orders. Refusal before cooking restarts selection with a
persisted per-delivery/courier cooldown; after cooking use manual assignment.
PICKUP never creates dispatch work. Redis failure degrades only auto dispatch/GPS;
manual fallback, Order Service and PICKUP remain operational. Keep diagnostic
reasons and retry access for dispatchers under existing OIDC permissions.

See docs/automatic-dispatch.md and docs/adr/0007-automatic-dispatch-and-redis-geo.md.
Stage 10 explicitly permits Redis, GPS updates and automatic courier selection;
it supersedes the Stage 8 prohibition only for this authorized functionality.
No road routing, traffic ETA, online payments or multi-order courier assignment.


## Stage 11: Courier Offer & Acceptance

Automatic dispatch creates a persisted CourierOffer before assignment. Delivery
owns offers and the separate expiry worker inside its API process. Preserve the
existing lease, Redis GEO candidate lookup, retry budget, cooldown and Saga window;
never wait for a courier or Redis inside a SQL transaction. Offer TTL is 30 seconds
capped by the Saga dispatch deadline. PostgreSQL partial unique indexes permit
only one PENDING offer per delivery and per courier. Persist terminal decisions
and RespondedAt; do not delete history or create offers for historical assignments.

Accept locks Delivery, Offer, Courier and checks PostgreSQL time after lock waits.
ACCEPTED, domain.Assign, BUSY and the existing CourierAssigned Outbox commit together.
Same-decision retries return the saved result without another assignment/event.
Decline/expiry persist the existing per-delivery/courier cooldown and resume bounded
search. Expiry must run without Redis and even when automatic search is disabled;
recover due offers from PostgreSQL after restart. Never accept an expired,
cancelled, foreign or unavailable courier's offer. Only server-mapped courier
principals can list/respond to their own offers; POST bodies stay empty.

Manual dispatcher assignment cancels the PENDING offer of its own delivery
atomically. A courier reserved for another delivery causes a conflict rather than
silently cancelling that other reservation. Cancellation commands/events cancel
pending offers under the delivery lock. Creation/assignment cooperate with the
existing consumer quarantine fence. Orders cooking remains gated on the original
CourierAssigned event after acceptance or authorized manual assignment. PICKUP
never creates or interacts with offers. No new event contracts, topics, Dispatch
service, customer GPS tracking, maps or road ETA are introduced.

See docs/courier-offers.md and docs/adr/0008-courier-offer-and-acceptance.md.
Run scripts/check.sh, scripts/test-integration.sh and scripts/verify-offers.py.


## Stage 12: Real-Time Courier Tracking

Delivery owns latest courier Location (accuracy, RecordedAt, server ReceivedAt),
Redis TTL and GPS validation. New GPS payloads atomically enforce timestamp order,
shared rate limiting and GEO indexing; accept AVAILABLE/BUSY and reject OFFLINE.
Legacy GPS remains dispatch-only. Never store movement history in PostgreSQL,
log exact coordinates or publish each GPS update to Kafka.

Customer GET /orders/{orderId}/tracking uses SSE and bounded polling inside Delivery.
Authenticate a server-mapped customer principal; never accept URL tokens. Bind
ownership only from trusted CreateDelivery v1 optional customer_id, transactionally
with Inbox/Delivery/Outbox. Preserve historical fingerprints and unknown NULL owners;
roll out migration/consumers before producers. No cross-service database reads.

Only PICKED_UP permits coordinates. Check owner/status/courier in PostgreSQL before
and after Redis reads, discard changed assignments, stop terminal streams without
Redis. PICKUP never has tracking. Missing/stale/inaccurate GPS or Redis outage must
not invent points; report location_unavailable and keep order operations independent.
Use Flusher, heartbeat, operation/write deadlines, token-expiry-bound contexts,
bounded lifetime, per-process/per-customer connection caps and cancellation cleanup.
No extra tracking service, maps, routes, ETA, GPS history or frontend.

See docs/tracking.md and docs/adr/0009-real-time-courier-tracking.md.
Run scripts/check.sh, scripts/test-integration.sh and scripts/verify-offers.py.


## Stage 13: Delivery Zones & Pricing

Catalog owns delivery/pickup settings, minimum DELIVERY food subtotal and radial
DeliveryTariff tiers. Use one MVCC statement for the batch quote: menu, settings,
coordinates, tariffs and pricing version. Haversine distance is rounded upward to
integer meters; select the first maximum >= distance. Validate finite coordinates;
never describe straight-line distance as road distance/ETA or geocode verification.
Money stays int64 minor units (100 dirams per TJS); guard multiplication/addition.

Orders requires expected_total_minor on new creations and paired customer delivery
coordinates for DELIVERY. Preserve exact historical retries before calling Catalog;
new requests with only expected_subtotal_minor are invalid. Persist immutable menu,
pricing snapshot, subtotal, fee, total and locations. Do not reprice existing orders.
Quote and Orders commit are separate snapshots, without distributed transactions.
PICKUP ignores delivery minimum, has zero fee, and creates no Delivery/Saga.

Delivery receives full subtotal + delivery fee through CreateDelivery, persists the
customer's declared coordinates and retains atomic full POS confirmation/completion/
Outbox. Orders completes through its transactional Inbox/Saga and compares total.
REPORTED_COLLECTED remains operational. No online payment or Payment Service.

Optional v1 fields use omitempty and preserve historical canonical fingerprints.
Migrate first, upgrade all consumers before producers, then explicitly configure
and enable restaurant DELIVERY. Historical fees stay zero; coordinates remain NULL;
never rewrite stored idempotency JSON. Orders downgrade must refuse new pricing
snapshots. See docs/pricing.md and docs/adr/0010-delivery-zones-and-pricing.md.
Run scripts/check.sh, scripts/test-integration.sh, scripts/verify-pricing.py,
scripts/verify-offers.py and scripts/verify-pickup.py. No commits without approval.


## Stage 14: Push Notifications & Firebase FCM

Notifications owns device registrations, intents, per-device deliveries and attempt
history. Run its Kafka/API process and separate Push Worker; never call Firebase
from a business service or inside an Inbox transaction. Official Firebase Admin Go
v4.22.0 supports Message.Fid: use registered Firebase Installation IDs and ADC.
Fake is explicit development/test only. Domain must not depend on Firebase.

OIDC maps customer/courier IDs and restaurant grants from the server-owned registry.
One installation has one active owner/recipient; transfer revokes the old record and
uses a new registration ID. Recheck ownership/activity before Send. Never expose or
log installation IDs, tokens, Firebase errors or confidential payloads. Scrub the
identifier on revocation; retain attempt audit. Grant removal also requires device
revocation. Historical notifications must never become a backfill campaign.

Use PostgreSQL SKIP LOCKED leases with fencing, bounded retries/jitter, Retry-After,
shared send-start rate limiting and recovery after restart. FCM accepted does not
mean received/read. Crash after Send may duplicate push; retain UNKNOWN attempts.
UNREGISTERED revokes only its registration; INVALID_ARGUMENT alone never does.
Preserve existing Inbox fingerprints and service-wide quarantine across all topics.

CourierOfferCreated/Closed and OrderNeedsAttention use separate versioned business
contracts and Outbox streams; preserve old aggregate versions/topics. Pending offer
push expires at the original offer deadline, terminal facts cancel unsent intents,
and worker checks Delivery's service-auth private relevance API before Send.
Never invent facts, revoke already accepted FCM messages, publish GPS or rely on
push as the source of truth. No reminder for the existing 30-second offers.

See docs/push-notifications.md and docs/adr/0011-push-notifications-and-fcm.md.
Run scripts/check.sh, scripts/test-integration.sh, scripts/verify-push.py,
scripts/verify-pricing.py, scripts/verify-offers.py and scripts/verify-pickup.py.
Real FCM tests require explicit opt-in and a dedicated installation. No commits.

## Stage 16: Auth Profile API & Real OIDC Integration

Orders exposes GET /v1/auth/me as an explicit HTTP DTO derived only from the
authenticated server-mapped Principal: user_id, roles, restaurant_ids, permissions.
Always return arrays and Cache-Control: no-store. Missing/invalid access tokens
return 401; verified subjects absent from the registry return 403. Never expose
Subject, tokens, issuer/JWKS configuration or invented personal data. Profile does
not grant authority; each API still checks roles, resource ownership and grants.

MVP SPA may use one explicit food-api audience across Catalog, Orders, Delivery
and Notifications. This supersedes Stage 9's distinct-audience requirement only
for this shared trusted backend; per-service audience overrides remain supported.
Never use the public SPA client ID as API audience or accept ID/refresh tokens.
Require RS256/ES256, trusted HTTPS issuer/JWKS, aud, exp and an access-token marker
(Keycloak signed typ=Bearer or RFC 9068 protected typ=at+jwt/application/at+jwt).
Authorization Code + PKCE S256 has no frontend client secret.

Identity registry remains deployment-owned and issuer-namespaced. Staff bindings
require explicit UserID, restaurant_staff and restaurant grants; never authorize
from JWT role/ID claims. Registry changes require coordinated API restart and
push device revocation where applicable. No User Service or self-registration.

CORS_ALLOWED_ORIGINS lists exact origins; production requires HTTPS, never wildcard.
Wrap authentication with CORS only for selected public API routes; allow Bearer
preflights without authenticating OPTIONS. Never enable browser access to internal
listeners, health, metrics or service-secret headers. Dev identity headers remain
explicit development/test only and are not allowed through CORS.

See docs/auth-profile-oidc.md, docs/security-reliability.md and ADR 0012. The optional
deploy/compose.oidc.yaml is local Keycloak with HTTPS and public PKCE client;
never deploy its start-dev/H2 setup to production. Do not modify the separate
frontend repository. Run scripts/check.sh, scripts/test-integration.sh,
scripts/verify-auth.py (real isolated Keycloak with temporary credentials) and existing
verify-push/pricing/offers/pickup scripts. No commits without authorization.

## Stage 18: Independent Auth Service

Stage 18 supersedes the earlier prohibition on a separate Auth service and the
requirement for external OIDC login. Auth owns its PostgreSQL database, users,
Argon2id password hashes, roles, restaurant memberships, courier/customer bindings,
sessions, refresh hashes, signing keys and administration audit. Keep domain and
application independent of HTTP, SQL and crypto libraries through concrete ports.

Public registration creates customer only. With no email channel, production
registrations remain PENDING_VERIFICATION and cannot log in; verification/recovery
must fail explicitly rather than invent email ownership. Unverified login is an
explicit development/test option, forbidden in production. Managed accounts are
provisioned by deployment administrators through authctl with audited operator and
password input on stdin. Never add public role assignment or log credentials.

Use short-lived ES256 at+jwt access tokens in frontend memory and rotated opaque
refresh tokens in HttpOnly Secure SameSite=Strict host-only cookies. Persist only
SHA-256 refresh hashes; rotation and replay detection must use PostgreSQL locks.
Reuse revokes the entire session, including a concurrent successful rotation.
Preserve consumed hashes for the full session lifetime. Frontend must serialize
refresh across tabs. Logout/password/grant changes revoke access immediately via
online Auth authority checks, not just expiry. Check issuer/audience/signature/exp/
nbf/algorithm before resolving the current Principal through Auth's private API.

Never read Auth tables from other services. Preserve resource ownership checks in
Catalog, Orders and Delivery. Inject current authorization into application gates;
never replace it with client roles or stale startup grants. Notifications rechecks
device authorization before sending; permission changes queue durable, fenced
device revocations in Auth and Notifications scrubs identifiers in its own DB.

Preserve old OIDC, registry, optional Compose and tests during migration. Import
registry identities transactionally using original UserID/CustomerID/CourierID/
RestaurantID; conflicting imports abort. Do not import passwords or invent emails,
verification or new business facts. Imported accounts start disabled and require
controlled provisioning. New IDs use a reserved high range; audit existing maxima
before rollout. Switch all APIs to Auth only after validating imports and tests.

Exact Origin/CORS allowlists and CSRF tokens protect cookie endpoints. Internal
service keys must never be CORS-enabled or exposed by the public reverse proxy.
Production requires HTTPS endpoints, secure cookies, private signing-key storage,
key overlap during rotation and secret distribution. Auth is a JWT issuer, not an
OIDC Provider. Do not modify the separate frontend repository or commit changes.

See docs/auth-service.md and ADR 0013. Run scripts/check.sh,
scripts/test-integration.sh, scripts/verify-auth-service.py and the existing
verify-auth/push/pricing/offers/pickup scripts. Keep email verification/recovery
production limitations and migration/rollback consequences explicit.
