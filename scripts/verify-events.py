#!/usr/bin/env python3
"""Exercise the full Compose deployment, including a real broker outage."""
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
ENV = dict(os.environ, APP_ENV="test", DEV_AUTH_ENABLED="true",
           OIDC_ISSUER="", OIDC_JWKS_URL="", AUTH_IDENTITIES_FILE="", POSTGRES_PORT="25432", CATALOG_PORT="28080",
           DEV_ADMIN_IDS="1", ORDERS_PORT="28081", NOTIFICATIONS_PORT="28083", DELIVERY_PORT="28085",
           NOTIFICATIONS_DB_PORT="25433", KAFKA_EXTERNAL_PORT="39092",
           KAFKA_PUBLISH_TIMEOUT="3s", OUTBOX_RETRY_BASE="100ms",
           OUTBOX_RETRY_MAX="1s", OUTBOX_MAX_ATTEMPTS="100")
COMPOSE = ["docker", "compose", "-p", "food-events-e2e", "--env-file",
           str(ROOT / ".env.example"), "-f", str(ROOT / "deploy/compose.yaml")]


def compose(*args, capture=False):
    return subprocess.run(COMPOSE + list(args), cwd=ROOT, env=ENV,
                          check=True, text=True, capture_output=capture).stdout


def request(port, path, body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    if port == 28080 and body is not None:
        headers = {"X-Dev-User-ID": "1", **(headers or {})}
    req = urllib.request.Request(f"http://localhost:{port}{path}", data=data,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    with urllib.request.urlopen(req, timeout=10) as response:
        return json.load(response)


def sql(service, database, user, query):
    return compose("exec", "-T", service, "psql", "-U", user, "-d", database,
                   "-At", "-v", "ON_ERROR_STOP=1", "-c", query, capture=True).strip()


def eventually(check, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.5)
    raise AssertionError("Timed out waiting for event delivery")


def main():
    try:
        compose("up", "--build", "-d", "--wait", "--wait-timeout", "180", "--scale", "outbox=2")
        category = request(28080, "/restaurants/1/categories", {"name": "Events test", "sort_order": 0})
        item = request(28080, "/restaurants/1/items", {"category_id": category["id"], "name": "Test dish", "price_minor": 25000, "currency": "TJS", "is_available": True})
        compose("stop", "redpanda")
        headers = {"X-Dev-Customer-ID": "7", "Idempotency-Key": "events-e2e", "X-Request-ID": "events-e2e-request"}
        body = {"restaurant_id": 1, "currency": "TJS", "fulfillment_type": "DELIVERY", "delivery_address": "Test delivery address", "expected_subtotal_minor": 25000, "items": [{"menu_item_id": item["id"], "quantity": 1}]}
        order = request(28081, "/orders", body, headers)
        assert request(28081, "/orders", body, headers)["id"] == order["id"]
        assert sql("postgres", "orders", "orders_service", "SELECT count(*) FROM outbox_events") == "1"
        assert sql("postgres", "orders", "orders_service", "SELECT count(*) FROM outbox_events WHERE status='PUBLISHED'") == "0"
        eventually(lambda: int(sql("postgres", "orders", "orders_service", "SELECT max(attempts) FROM outbox_events")) >= 2)
        print("Order committed while Kafka is down; retries preserve the event.", flush=True)
        compose("up", "-d", "--wait", "redpanda")
        eventually(lambda: sql("notifications-postgres", "notifications", "notifications_service", "SELECT count(*) FROM notifications WHERE kind='OrderCreated' AND status='PENDING'") == "1")
        req = urllib.request.Request(f"http://localhost:28081/orders/{order['id']}/cancel", data=b"", method="POST", headers={"X-Dev-Customer-ID": "7", "X-Request-ID": "events-e2e-cancel"})
        with urllib.request.urlopen(req, timeout=10) as response:
            assert json.load(response)["status"] == "CANCELLED"
        eventually(lambda: sql("notifications-postgres", "notifications", "notifications_service", "SELECT count(*) FROM notifications WHERE status='PENDING'") == "2")
        eventually(lambda: sql("postgres", "orders", "orders_service", "SELECT count(*) FROM outbox_events WHERE status='PUBLISHED'") == "2")
        # Re-deliver both immutable events through the actual Outbox Worker.
        sql("postgres", "orders", "orders_service", "UPDATE outbox_events SET status='PENDING',published_at=NULL,available_at=now(),attempts=0")
        eventually(lambda: sql("postgres", "orders", "orders_service", "SELECT count(*) FROM outbox_events WHERE status='PUBLISHED'") == "2")
        # Wait for actual duplicate handling, not just producer acknowledgements.
        eventually(lambda: compose("logs", "--no-color", "notifications", capture=True).count("duplicate event skipped") >= 2)
        assert sql("notifications-postgres", "notifications", "notifications_service", "SELECT count(*) FROM notifications") == "2"
        assert sql("notifications-postgres", "notifications", "notifications_service", "SELECT count(*) FROM notifications WHERE correlation_id IN ('events-e2e-request','events-e2e-cancel')") == "2"
        print("PASS: recovery, two workers, HTTP idempotency, cancellation, duplicate publication, correlation IDs, PENDING intents.", flush=True)
    finally:
        compose("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
