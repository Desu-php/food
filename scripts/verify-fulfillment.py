#!/usr/bin/env python3
"""Verify restaurant fulfillment, Kafka replay/outage and operational POS collection."""
import importlib.util
import json
from pathlib import Path
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location("events", Path(__file__).with_name("verify-events.py"))
events = importlib.util.module_from_spec(spec)
spec.loader.exec_module(events)
events.COMPOSE[events.COMPOSE.index("food-events-e2e")] = "food-fulfillment-e2e"
compose, request, sql, eventually = events.compose, events.request, events.sql, events.eventually


def action(port, path, headers, body=None, expected=200):
    data = b"" if body is None else json.dumps(body).encode()
    req = urllib.request.Request(f"http://localhost:{port}{path}", data=data, method="POST",
                                 headers={"Content-Type": "application/json", **headers})
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            assert response.status == expected
            return json.load(response)
    except urllib.error.HTTPError as error:
        assert error.code == expected, (error.code, error.read())


def main():
    try:
        compose("up", "--build", "-d", "--wait", "--wait-timeout", "240", "--scale", "outbox=2", "--scale", "delivery-outbox=2", "--scale", "orders-saga=2")
        category = request(28080, "/restaurants/1/categories", {"name": "Fulfillment", "sort_order": 0})
        item = request(28080, "/restaurants/1/items", {"category_id": category["id"], "name": "Test dish", "price_minor": 25000, "currency": "TJS", "is_available": True})
        customer = {"X-Dev-Customer-ID": "7", "Idempotency-Key": "fulfillment-1"}
        manager = {"X-Dev-User-ID": "10"}
        dispatcher = {"X-Dev-User-ID": "20"}
        body = {"restaurant_id": 1, "currency": "TJS", "fulfillment_type": "DELIVERY", "delivery_address": "Test delivery address", "expected_subtotal_minor": 25000, "items": [{"menu_item_id": item["id"], "quantity": 1}]}
        order = request(28081, "/orders", body, customer)
        assert order["status"] == "WAITING_RESTAURANT" and order["payment_status"] == "UNPAID" and order["payment_method"] == "POS_ON_DELIVERY"
        base = f"/restaurants/1/orders/{order['id']}"
        action(28081, base + "/accept", {}, expected=401)
        action(28081, base + "/accept", {"X-Dev-User-ID": "99"}, expected=403)
        action(28081, base + "/prepare", manager, expected=409)
        action(28081, base + "/accept", manager)
        eventually(lambda: len(request(28085, "/deliveries", headers=dispatcher)) == 1)
        delivery = request(28085, "/deliveries", headers=dispatcher)[0]
        courier = request(28085, "/couriers", {"name": "Courier"}, dispatcher)
        courier_headers = {"X-Dev-Courier-ID": str(courier["id"])}
        delivery_base = f"/deliveries/{delivery['id']}"
        action(28085, delivery_base + "/assign", dispatcher, {"courier_id": courier["id"]})
        action(28085, delivery_base + "/assign", dispatcher, {"courier_id": courier["id"]}, expected=409)
        action(28085, delivery_base + "/pickup", courier_headers, expected=409)
        eventually(lambda: request(28081, f"/orders/{order['id']}", headers=customer).get("courier_id") == courier["id"])
        action(28081, base + "/prepare", manager)
        action(28081, base + "/ready", manager)
        eventually(lambda: request(28085, delivery_base, headers=courier_headers)["ready_for_pickup"])
        action(28085, delivery_base + "/pickup", courier_headers)
        eventually(lambda: request(28081, f"/orders/{order['id']}", headers=customer)["status"] == "ON_THE_WAY")
        action(28085, delivery_base + "/complete", {}, {"payment_confirmed": True}, expected=401)
        action(28085, delivery_base + "/complete", courier_headers, {"payment_confirmed": False}, expected=400)
        action(28085, delivery_base + "/complete", courier_headers, {"payment_confirmed": True, "amount_minor": 1}, expected=400)
        # Completion remains durable with both broker and downstream consumer unavailable.
        compose("stop", "orders-delivery-consumer", "redpanda")
        confirmation = {"payment_confirmed": True, "pos_reference": "POS-E2E-42"}
        result = action(28085, delivery_base + "/complete", courier_headers, confirmation)
        assert result["status"] == "DELIVERED" and result["confirmation"]["amount_minor"] == 25000
        action(28085, delivery_base + "/complete", courier_headers, confirmation)
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM payment_confirmations") == "1"
        assert request(28081, f"/orders/{order['id']}", headers=customer)["payment_status"] == "UNPAID"
        compose("up", "-d", "--wait", "redpanda", "orders-delivery-consumer")
        eventually(lambda: request(28081, f"/orders/{order['id']}", headers=customer)["status"] == "DELIVERED")
        assert request(28081, f"/orders/{order['id']}", headers=customer)["payment_status"] == "REPORTED_COLLECTED"
        assert request(28081, base + "/saga", headers=manager)["state"] == "COMPLETED"
        # Duplicate publication of both streams must leave all business effects unchanged.
        for db, role in [("orders", "orders_service"), ("delivery", "delivery_service")]:
            eventually(lambda db=db, role=role: sql("postgres", db, role, "SELECT count(*) FROM outbox_events WHERE status<>'PUBLISHED'") == "0")
            sql("postgres", db, role, "UPDATE outbox_events SET status='PENDING',published_at=NULL,available_at=now(),attempts=0")
            eventually(lambda db=db, role=role: sql("postgres", db, role, "SELECT count(*) FROM outbox_events WHERE status<>'PUBLISHED'") == "0")
        eventually(lambda: sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM processed_events") == "8")
        eventually(lambda: sql("postgres", "orders", "orders_service", "SELECT count(*) FROM delivery_inbox") == "4")
        # A rejected order never gets a delivery and cannot enter preparation.
        customer["Idempotency-Key"] = "fulfillment-reject"
        rejected = request(28081, "/orders", body, customer)
        rejection_base = f"/restaurants/1/orders/{rejected['id']}"
        action(28081, rejection_base + "/reject", manager)
        action(28081, rejection_base + "/prepare", manager, expected=409)
        eventually(lambda: sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM processed_events") == "10")
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM deliveries") == "1"
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM payment_confirmations") == "1"
        eventually(lambda: sql("notifications-postgres", "notifications", "notifications_service", "SELECT count(*) FROM notifications") == "10")
        assert request(28081, "/orders", body, {**customer, "Idempotency-Key": "fulfillment-1"})["status"] == "WAITING_RESTAURANT"
        print("PASS: restaurant ACL/transitions, ready pickup, assignment, atomic POS, broker outage, consumer restart, duplicate streams, rejection and Notifications.", flush=True)
    finally:
        compose("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
