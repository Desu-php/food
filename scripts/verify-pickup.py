#!/usr/bin/env python3
"""Verify pickup handoff and delivery coexistence through real services and Kafka."""
import importlib.util
import json
from pathlib import Path
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location("fulfillment", Path(__file__).with_name("verify-fulfillment.py"))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
f.events.COMPOSE[f.events.COMPOSE.index("food-fulfillment-e2e")] = "food-pickup-e2e"
compose, request, sql, eventually, action = f.compose, f.request, f.sql, f.eventually, f.action
customer = {"X-Dev-Customer-ID": "7"}
manager = {"X-Dev-User-ID": "10"}
dispatcher = {"X-Dev-User-ID": "20"}


def main():
    try:
        compose("up", "--build", "-d", "--wait", "--wait-timeout", "240", "--scale", "outbox=2", "--scale", "orders-saga=2")
        patch = urllib.request.Request("http://localhost:28080/restaurants/1", method="PATCH",
                                       data=json.dumps({"address": "Restaurant-owned location"}).encode(),
                                       headers={"Content-Type": "application/json", "X-Dev-User-ID": "1"})
        with urllib.request.urlopen(patch, timeout=10) as response:
            assert response.status == 200
        category = request(28080, "/restaurants/1/categories", {"name": "Pickup", "sort_order": 0})
        item = request(28080, "/restaurants/1/items", {"category_id": category["id"], "name": "Pickup dish", "price_minor": 6500, "currency": "TJS", "is_available": True})
        body = {"restaurant_id": 1, "fulfillment_type": "PICKUP", "expected_subtotal_minor": 13000,
                "items": [{"menu_item_id": item["id"], "quantity": 2}]}
        headers = {**customer, "Idempotency-Key": "pickup-e2e"}
        created = request(28081, "/orders", body, headers)
        oid, base = created["id"], f"/restaurants/1/orders/{created['id']}"
        assert created["pickup_address"] == "Restaurant-owned location"
        assert created["delivery_fee_minor"] == 0 and created["payment_method"] == "POS_AT_PICKUP"
        action(28081, base + "/accept", manager)
        action(28081, base + "/prepare", manager)
        action(28081, base + "/complete-pickup", manager,
               {"pickup_code": "0" * 32, "payment_confirmed": True}, expected=409)
        action(28081, base + "/ready", manager)
        code = action(28081, f"/orders/{oid}/pickup-code", customer)["pickup_code"]
        verified = action(28081, base + "/verify-pickup", manager, {"pickup_code": code})
        assert verified["status"] == "READY_FOR_PICKUP" and verified["payment_status"] == "UNPAID"
        payload = {"pickup_code": code, "payment_confirmed": True, "receipt_reference": "PICKUP-E2E"}
        action(28081, base + "/complete-pickup", {"X-Dev-User-ID": "11"}, payload, expected=403)
        action(28081, base + "/complete-pickup", manager, {**payload, "pickup_code": "0" * 32}, expected=403)
        action(28081, base + "/complete-pickup", manager, {**payload, "payment_confirmed": False}, expected=409)
        result = action(28081, base + "/complete-pickup", manager, payload)
        assert result["status"] == "PICKED_UP_BY_CUSTOMER" and result["payment_status"] == "REPORTED_COLLECTED"
        compose("restart", "orders")
        def healthy():
            try:
                return request(28081, "/health")["status"] == "ok"
            except (OSError, urllib.error.URLError):
                return False
        eventually(healthy)
        assert action(28081, base + "/complete-pickup", manager, payload) == result
        assert request(28081, "/orders", body, headers) == created
        assert sql("postgres", "orders", "orders_service", f"SELECT count(*) FROM order_sagas WHERE order_id={oid}") == "0"
        assert sql("postgres", "orders", "orders_service", f"SELECT count(*) FROM outbox_events WHERE aggregate_id={oid} AND stream='delivery_commands'") == "0"
        assert sql("postgres", "orders", "orders_service", f"SELECT count(*) FROM pickup_payment_confirmations WHERE order_id={oid}") == "1"
        eventually(lambda: sql("notifications-postgres", "notifications", "notifications_service",
                              f"SELECT count(*) FROM notifications WHERE order_id={oid} AND fulfillment_type='PICKUP'") == "5")
        eventually(lambda: sql("postgres", "delivery", "delivery_service", f"SELECT count(*) FROM processed_events WHERE order_id={oid}") == "5")
        assert sql("postgres", "delivery", "delivery_service", f"SELECT count(*) FROM deliveries WHERE order_id={oid}") == "0"
        assert code not in compose("logs", "--no-color", "orders", "outbox", "notifications", "delivery-consumer", capture=True)
        # Delivery still passes through the existing Saga and collects courier POS.
        delivery_body = {**body, "fulfillment_type": "DELIVERY", "delivery_address": "Customer location"}
        dorder = request(28081, "/orders", delivery_body, {**customer, "Idempotency-Key": "delivery-with-pickup"})
        did, dbase = dorder["id"], f"/restaurants/1/orders/{dorder['id']}"
        action(28081, dbase + "/accept", manager)
        action(28081, dbase + "/prepare", manager, expected=409)
        eventually(lambda: any(d["order_id"] == did for d in request(28085, "/deliveries", headers=dispatcher)))
        delivery = next(d for d in request(28085, "/deliveries", headers=dispatcher) if d["order_id"] == did)
        assert delivery["delivery_address"] == "Customer location"
        courier = request(28085, "/couriers", {"name": "Pickup coexistence courier"}, dispatcher)
        route = f"/deliveries/{delivery['id']}"
        action(28085, route + "/assign", dispatcher, {"courier_id": courier["id"]})
        eventually(lambda: request(28081, f"/orders/{did}", headers=customer).get("courier_id") == courier["id"])
        action(28081, dbase + "/prepare", manager)
        action(28081, dbase + "/ready", manager)
        eventually(lambda: request(28085, route, headers=dispatcher)["ready_for_pickup"])
        courier_headers = {"X-Dev-Courier-ID": str(courier["id"])}
        action(28085, route + "/pickup", courier_headers)
        eventually(lambda: request(28081, f"/orders/{did}", headers=customer)["status"] == "ON_THE_WAY")
        action(28085, route + "/complete", courier_headers, {"payment_confirmed": True, "receipt_reference": "DELIVERY-E2E"})
        eventually(lambda: request(28081, f"/orders/{did}", headers=customer)["status"] == "DELIVERED")
        assert request(28081, f"/orders/{did}", headers=customer)["payment_status"] == "REPORTED_COLLECTED"
        assert sql("postgres", "orders", "orders_service", f"SELECT state FROM order_sagas WHERE order_id={did}") == "COMPLETED"
        print("PASS: pickup without Delivery/Saga, secure POS handoff, immutable replay/restart, Kafka notifications and delivery Saga/POS coexistence.", flush=True)
    except Exception:
        print(compose("logs", "--no-color", "--tail=20", "orders", "notifications", "delivery-consumer", capture=True), flush=True)
        raise
    finally:
        compose("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
