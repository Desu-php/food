#!/usr/bin/env python3
"""Verify devices and Kafka/Inbox/push fanout using the full deployment and fake FCM."""
import importlib.util
import json
from pathlib import Path
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location("events", Path(__file__).with_name("verify-events.py"))
e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e)
e.COMPOSE[e.COMPOSE.index("food-events-e2e")] = "food-push-e2e"
e.ENV.update(PUSH_PROVIDER="fake", PUSH_SEND_INTERVAL="10ms", PUSH_POLL_INTERVAL="50ms")
customer = {"X-Dev-Customer-ID": "7"}
other = {"X-Dev-Customer-ID": "8"}
manager = {"X-Dev-User-ID": "10"}


def send(path, method="GET", body=None, headers=None, expected=200):
    req = urllib.request.Request("http://localhost:28083" + path, method=method,
                                 data=None if body is None else json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            assert response.status == expected
            raw = response.read()
            assert b"private-installation" not in raw and b"installation_id" not in raw
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        assert error.code == expected, (error.code, error.read())


def main():
    try:
        e.compose("up", "--build", "-d", "--wait", "--wait-timeout", "360",
                  "--scale", "notification-push-worker=3")
        e.configure_delivery()
        patch = urllib.request.Request("http://localhost:28080/restaurants/1", method="PATCH",
                                       data=json.dumps({"address": "Restaurant-owned location"}).encode(),
                                       headers={"Content-Type": "application/json", "X-Dev-User-ID": "1"})
        with urllib.request.urlopen(patch, timeout=10) as response:
            assert response.status == 200
        send("/v1/devices", expected=401)
        device = {"owner_type": "CUSTOMER", "platform": "ANDROID", "installation_id": "private-installation-one"}
        first = send("/v1/devices", "POST", device, customer)
        assert send("/v1/devices", "POST", device, customer)["id"] == first["id"]
        send("/v1/devices", "POST", {**device, "owner_id": 999}, customer, 400)
        send("/v1/devices", "POST", {**device, "owner_type": "COURIER"}, customer, 403)
        send("/v1/devices", "POST", {**device, "installation_id": "private-installation-two"}, customer)
        staff = {**device, "owner_type": "RESTAURANT", "restaurant_id": 1, "installation_id": "private-installation-staff"}
        send("/v1/devices", "POST", staff, manager)
        send("/v1/devices", "POST", {**staff, "restaurant_id": 2}, manager, 403)
        assert len(send("/v1/devices", headers=customer)) == 2
        category = e.request(28080, "/restaurants/1/categories", {"name": "Push"})
        item = e.request(28080, "/restaurants/1/items", {"category_id": category["id"], "name": "Dish", "price_minor": 25000, "currency": "TJS", "is_available": True})
        body = {"restaurant_id": 1, "fulfillment_type": "PICKUP", "expected_total_minor": 25000,
                "items": [{"menu_item_id": item["id"], "quantity": 1}]}
        order = e.request(28081, "/orders", body, {**customer, "Idempotency-Key": "push-pickup"})
        base = f"/restaurants/1/orders/{order['id']}"
        for action in ("accept", "prepare", "ready"):
            req = urllib.request.Request("http://localhost:28081" + base + "/" + action,
                                         data=b"", method="POST", headers=manager)
            with urllib.request.urlopen(req, timeout=10) as response:
                assert response.status == 200
        sql = lambda query: e.sql("notifications-postgres", "notifications", "notifications_service", query)
        e.eventually(lambda: sql("SELECT count(*) FROM notifications WHERE kind='OrderReadyForPickup' AND status='SENT'") == "1")
        assert sql("SELECT count(*) FROM push_deliveries d JOIN notifications n ON n.id=d.notification_id WHERE n.kind='OrderReadyForPickup' AND d.status='SENT'") == "2"
        assert sql("SELECT count(*) FROM notifications WHERE kind='OrderCreated' AND recipient_type='RESTAURANT' AND status='SENT'") == "1"
        e.eventually(lambda: e.sql("postgres", "orders", "orders_service", "SELECT count(*) FROM outbox_events WHERE status<>'PUBLISHED'") == "0")
        e.eventually(lambda: sql("SELECT count(*) FROM notifications WHERE status IN ('PENDING','PROCESSING')") == "0")
        before = sql("SELECT count(*) FROM push_attempts")
        e.sql("postgres", "orders", "orders_service", "UPDATE outbox_events SET status='PENDING',published_at=NULL,available_at=now(),attempts=0")
        e.eventually(lambda: e.compose("logs", "--no-color", "notifications", capture=True).count("duplicate event skipped") >= 4)
        assert sql("SELECT count(*) FROM push_attempts") == before
        # Exercise the real Offer Outbox/topic and private Delivery relevance API.
        dispatcher = {"X-Dev-User-ID": "20"}
        courier = e.request(28085, "/couriers", {"name": "Push courier"}, dispatcher)
        courier_headers = {"X-Dev-Courier-ID": str(courier["id"])}
        send("/v1/devices", "POST", {**device, "owner_type": "COURIER", "installation_id": "private-installation-courier"}, courier_headers)
        location = urllib.request.Request("http://localhost:28085/couriers/me/location", method="POST",
                                          data=json.dumps({"latitude": 38.56, "longitude": 68.77}).encode(),
                                          headers={"Content-Type": "application/json", **courier_headers})
        with urllib.request.urlopen(location, timeout=10) as response:
            assert response.status == 204
        delivery_body = {**body, "fulfillment_type": "DELIVERY", "delivery_address": "Declared point",
                         "delivery_latitude": 38.56, "delivery_longitude": 68.77}
        delivery_order = e.request(28081, "/orders", delivery_body, {**customer, "Idempotency-Key": "push-delivery-offer"})
        accept = urllib.request.Request(f"http://localhost:28081/restaurants/1/orders/{delivery_order['id']}/accept",
                                        data=b"", method="POST", headers=manager)
        with urllib.request.urlopen(accept, timeout=10) as response:
            assert response.status == 200
        e.eventually(lambda: sql("SELECT count(*) FROM notifications WHERE kind='CourierOfferCreated' AND recipient_type='COURIER' AND status='SENT'") == "1", timeout=25)
        assert len(e.request(28085, "/couriers/me/offers", headers=courier_headers)) == 1
        moved = send("/v1/devices", "POST", device, other)
        assert moved["id"] != first["id"]
        send(f"/v1/devices/{moved['id']}", "DELETE", headers=customer, expected=404)
        send(f"/v1/devices/{first['id']}", "DELETE", headers=customer, expected=204)
        send(f"/v1/devices/{moved['id']}", "DELETE", headers=other, expected=204)
        assert sql("SELECT count(*) FROM consumer_quarantine WHERE resolved_at IS NULL") == "0"
        assert "private-installation" not in e.compose("logs", "--no-color", "notifications", "notification-push-worker", capture=True)
        print("PASS: device ownership, grants, multi-device push, three workers, Kafka replay, PICKUP readiness, courier offer relevance and identifier redaction.", flush=True)
    finally:
        e.compose("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
