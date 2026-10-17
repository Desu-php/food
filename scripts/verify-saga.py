#!/usr/bin/env python3
"""Run persisted Saga recovery, courier timeout/decline and compensation against real services."""
import importlib.util
from pathlib import Path

spec = importlib.util.spec_from_file_location("fulfillment", Path(__file__).with_name("verify-fulfillment.py"))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
f.events.COMPOSE[f.events.COMPOSE.index("food-fulfillment-e2e")] = "food-saga-e2e"
f.events.ENV.update(SAGA_CREATE_TIMEOUT="2m", SAGA_ASSIGNMENT_TIMEOUT="10s",
                    SAGA_PROGRESS_TIMEOUT="2m", SAGA_CANCEL_TIMEOUT="30s",
                    SAGA_RETRY_BASE="500ms", SAGA_RETRY_MAX="2s", SAGA_MAX_ATTEMPTS="100",
                    SAGA_POLL_INTERVAL="100ms", SAGA_CANCEL_BEFORE_PREPARING="true")
compose, request, sql, eventually, action = f.compose, f.request, f.sql, f.eventually, f.action
manager, dispatcher = {"X-Dev-User-ID": "10"}, {"X-Dev-User-ID": "20"}
customer = {"X-Dev-Customer-ID": "7"}


def main():
    try:
        compose("up", "--build", "-d", "--wait", "--wait-timeout", "240", "--scale", "outbox=2", "--scale", "delivery-outbox=2", "--scale", "orders-saga=2")
        category = request(28080, "/restaurants/1/categories", {"name": "Saga", "sort_order": 0})
        item = request(28080, "/restaurants/1/items", {"category_id": category["id"], "name": "Saga dish", "price_minor": 25000, "currency": "TJS", "is_available": True})
        courier = request(28085, "/couriers", {"name": "Saga courier"}, dispatcher)
        courier_headers = {"X-Dev-Courier-ID": str(courier["id"])}
        body = {"restaurant_id": 1, "currency": "TJS", "fulfillment_type": "DELIVERY", "delivery_address": "Test delivery address", "expected_subtotal_minor": 25000,
                "items": [{"menu_item_id": item["id"], "quantity": 1}]}

        def create(key):
            order = request(28081, "/orders", body, {**customer, "Idempotency-Key": key})
            return order["id"], f"/restaurants/1/orders/{order['id']}"

        def order(oid):
            return request(28081, f"/orders/{oid}", headers=customer)

        def saga(base):
            return request(28081, base + "/saga", headers=manager)

        def delivery_for(oid):
            return next((d for d in request(28085, "/deliveries", headers=dispatcher) if d["order_id"] == oid), None)

        # The command and Saga survive unavailability and restart of the Order processes.
        compose("stop", "delivery-consumer", "orders-delivery-consumer")
        oid, base = create("saga-restart")
        action(28081, base + "/accept", manager)
        action(28081, base + "/prepare", manager, expected=409)
        eventually(lambda: int(sql("postgres", "orders", "orders_service", f"SELECT attempts FROM order_sagas WHERE order_id={oid}")) >= 2)
        assert delivery_for(oid) is None
        compose("stop", "orders", "orders-saga")
        compose("up", "-d", "--wait", "orders", "orders-saga", "delivery-consumer", "orders-delivery-consumer")
        eventually(lambda: delivery_for(oid) is not None)
        delivery = delivery_for(oid)
        dbase = f"/deliveries/{delivery['id']}"
        eventually(lambda: order(oid)["status"] == "WAITING_COURIER")
        action(28085, dbase + "/assign", dispatcher, {"courier_id": courier["id"]})
        eventually(lambda: order(oid).get("courier_id") == courier["id"])
        action(28081, base + "/prepare", manager)
        eventually(lambda: sql("postgres", "delivery", "delivery_service", f"SELECT preparation_started FROM deliveries WHERE id={delivery['id']}") == "t")
        action(28085, dbase + "/decline", courier_headers)
        eventually(lambda: order(oid).get("courier_id", 0) == 0)
        eventually(lambda: saga(base)["state"] == "NEEDS_ATTENTION")
        assert saga(base)["reason"] == "courier_missing_after_preparation"
        assert order(oid)["status"] == "PREPARING" and order(oid)["payment_status"] == "UNPAID"
        action(28081, base + "/retry", {"X-Dev-User-ID": "99"}, expected=403)
        action(28081, base + "/retry", manager)
        action(28085, dbase + "/assign", dispatcher, {"courier_id": courier["id"]})
        eventually(lambda: order(oid).get("courier_id") == courier["id"])
        action(28081, base + "/ready", manager)
        eventually(lambda: request(28085, dbase, headers=courier_headers)["ready_for_pickup"])
        action(28085, dbase + "/pickup", courier_headers)
        action(28085, dbase + "/complete", courier_headers, {"payment_confirmed": True, "receipt_reference": "SAGA-POS-42"})
        action(28085, dbase + "/complete", courier_headers, {"payment_confirmed": True, "receipt_reference": "SAGA-POS-42"})
        eventually(lambda: saga(base)["state"] == "COMPLETED")
        assert order(oid)["payment_status"] == "REPORTED_COLLECTED"

        # No courier before cooking: configured timeout sends an idempotent compensation.
        timed_oid, timed_base = create("saga-timeout")
        action(28081, timed_base + "/accept", manager)
        eventually(lambda: delivery_for(timed_oid) is not None)
        eventually(lambda: saga(timed_base)["state"] == "CANCELLED")
        assert order(timed_oid)["status"] == "CANCELLED"
        assert delivery_for(timed_oid)["status"] == "CANCELLED"
        action(28081, timed_base + "/prepare", manager, expected=409)

        # Restaurant refusal after acceptance cancels the remote task before reporting CANCELLED.
        rejected_oid, rejected_base = create("saga-reject")
        action(28081, rejected_base + "/accept", manager)
        eventually(lambda: delivery_for(rejected_oid) is not None)
        action(28081, rejected_base + "/reject", manager)
        eventually(lambda: saga(rejected_base)["state"] == "CANCELLED")
        assert delivery_for(rejected_oid)["status"] == "CANCELLED"

        # Offline and busy couriers are excluded from availability and cannot be assigned.
        action(28085, f"/couriers/{courier['id']}/status", courier_headers, {"status": "OFFLINE"})
        assert request(28085, "/couriers?status=AVAILABLE", headers=dispatcher) == []
        cancelled_oid, cancelled_base = create("saga-customer-cancel")
        action(28081, cancelled_base + "/accept", manager)
        eventually(lambda: delivery_for(cancelled_oid) is not None)
        pending = delivery_for(cancelled_oid)
        action(28085, f"/deliveries/{pending['id']}/assign", dispatcher, {"courier_id": courier["id"]}, expected=409)
        action(28085, f"/couriers/{courier['id']}/status", courier_headers, {"status": "AVAILABLE"})
        action(28085, f"/deliveries/{pending['id']}/assign", dispatcher, {"courier_id": courier["id"]})
        eventually(lambda: order(cancelled_oid).get("courier_id") == courier["id"])
        action(28081, f"/orders/{cancelled_oid}/cancel", customer)
        eventually(lambda: saga(cancelled_base)["state"] == "CANCELLED")
        eventually(lambda: len(request(28085, "/couriers?status=AVAILABLE", headers=dispatcher)) == 1)

        # Replay every directed command. Inbox/tombstones must preserve terminal outcomes.
        eventually(lambda: sql("postgres", "orders", "orders_service", "SELECT count(*) FROM outbox_events WHERE stream='delivery_commands' AND status<>'PUBLISHED'") == "0")
        before = int(sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM command_inbox"))
        sql("postgres", "orders", "orders_service", "UPDATE outbox_events SET status='PENDING',published_at=NULL,available_at=now(),attempts=0 WHERE stream='delivery_commands'")
        eventually(lambda: sql("postgres", "orders", "orders_service", "SELECT count(*) FROM outbox_events WHERE stream='delivery_commands' AND status<>'PUBLISHED'") == "0")
        eventually(lambda: compose("logs", "--no-color", "delivery-consumer", capture=True).count("fulfillment command processed") >= before * 2)
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM deliveries") == "4"
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM payment_confirmations") == "1"
        assert sql("postgres", "orders", "orders_service", "SELECT count(*) FROM order_sagas WHERE state IN ('COMPLETED','CANCELLED')") == "4"
        print("PASS: persisted Saga restart, retry, preparation gate, decline/reassignment, no auto-cancel after cooking, timeout compensation, restaurant/customer cancellation, offline courier, POS and command replay.", flush=True)
    except Exception:
        print(compose("logs", "--no-color", "--tail=35", "orders-saga", "orders-delivery-consumer", "delivery-consumer", capture=True), flush=True)
        raise
    finally:
        compose("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
