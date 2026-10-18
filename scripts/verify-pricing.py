#!/usr/bin/env python3
"""Verify server pricing, immutable orders, POS and Saga against the full deployment."""
import concurrent.futures
import importlib.util
import json
from pathlib import Path
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location("fulfillment", Path(__file__).with_name("verify-fulfillment.py"))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
f.events.COMPOSE[f.events.COMPOSE.index("food-fulfillment-e2e")] = "food-pricing-e2e"
compose, request, sql, eventually, action = f.compose, f.request, f.sql, f.eventually, f.action
customer = {"X-Dev-Customer-ID": "7"}
manager = {"X-Dev-User-ID": "10"}
dispatcher = {"X-Dev-User-ID": "20"}


def send(port, path, method, body=None, headers=None, expected=200):
    req = urllib.request.Request(f"http://localhost:{port}{path}", method=method,
                                 data=None if body is None else json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            assert response.status in ((expected,) if isinstance(expected, int) else expected), response.status
            data = response.read()
            return json.loads(data) if data else None
    except urllib.error.HTTPError as error:
        data = error.read()
        assert error.code in ((expected,) if isinstance(expected, int) else expected), (error.code, data)
        return json.loads(data)


def main():
    try:
        compose("up", "--build", "-d", "--wait", "--wait-timeout", "240",
                "--scale", "outbox=2", "--scale", "delivery-outbox=2", "--scale", "orders-saga=2")
        settings = {"delivery_enabled": True, "pickup_enabled": True, "minimum_delivery_subtotal_minor": 13000}
        path = "/restaurants/1/delivery-settings"
        send(28080, path, "PUT", settings, {}, 401)
        send(28080, path, "PUT", settings, {"X-Dev-User-ID": "11"}, 403)
        initial = send(28080, path, "GET", headers=manager)
        assert initial["settings"]["delivery_enabled"] is False
        send(28080, "/restaurants/1", "PATCH", {"address": "Restaurant address", "latitude": 38.56, "longitude": 68.77}, manager)
        send(28080, path, "PUT", settings, manager)
        tariff = send(28080, "/restaurants/1/delivery-tariffs", "POST",
                      {"max_distance_meters": 2000, "delivery_fee_minor": 1000}, manager, 201)
        send(28080, "/restaurants/1/delivery-tariffs", "POST",
             {"max_distance_meters": 2000, "delivery_fee_minor": 1500}, manager, 409)
        category = request(28080, "/restaurants/1/categories", {"name": "Pricing"})
        item = request(28080, "/restaurants/1/items", {"category_id": category["id"], "name": "Dish", "price_minor": 6500, "currency": "TJS", "is_available": True})
        body = {"restaurant_id": 1, "fulfillment_type": "DELIVERY", "delivery_address": "Declared delivery point",
                "delivery_latitude": 38.56, "delivery_longitude": 68.77, "expected_total_minor": 13000,
                "items": [{"menu_item_id": item["id"], "quantity": 2}]}
        headers = {**customer, "Idempotency-Key": "pricing-original"}
        conflict = send(28081, "/orders", "POST", body, headers, 409)
        assert conflict["code"] == "price_changed" and conflict["current_quote"]["total_minor"] == 14000
        body["expected_total_minor"] = 14000
        created = send(28081, "/orders", "POST", body, headers, 201)
        assert (created["subtotal_minor"], created["delivery_fee_minor"], created["total_minor"]) == (13000, 1000, 14000)
        tariff_path = f"/restaurants/1/delivery-tariffs/{tariff['id']}"
        send(28080, tariff_path, "PUT", {"max_distance_meters": 2000, "delivery_fee_minor": 2000}, manager)
        assert send(28081, "/orders", "POST", body, headers, 201) == created
        conflict = send(28081, "/orders", "POST", body, {**customer, "Idempotency-Key": "pricing-new"}, 409)
        assert conflict["current_quote"]["total_minor"] == 15000
        outside = {**body, "delivery_latitude": 0, "delivery_longitude": 0}
        send(28081, "/orders", "POST", outside, {**customer, "Idempotency-Key": "outside"}, 409)
        minimum = {**body, "items": [{"menu_item_id": item["id"], "quantity": 1}]}
        send(28081, "/orders", "POST", minimum, {**customer, "Idempotency-Key": "minimum"}, 409)
        pickup = {"restaurant_id": 1, "fulfillment_type": "PICKUP", "expected_total_minor": 6500, "items": minimum["items"]}
        picked = send(28081, "/orders", "POST", pickup, {**customer, "Idempotency-Key": "pickup"}, 201)
        assert picked["total_minor"] == 6500 and picked["delivery_fee_minor"] == 0
        # Tariff edits and actual order commits run concurrently across service boundaries.
        def concurrent_edit(index):
            send(28080, tariff_path, "PUT", {"max_distance_meters": 2000, "delivery_fee_minor": 1000 + (index % 2) * 1000}, manager)

        def concurrent_create(index):
            result = send(28081, "/orders", "POST", body,
                          {**customer, "Idempotency-Key": f"concurrent-price-{index}"}, (201, 409))
            if result.get("code") == "price_changed":
                assert result["current_quote"]["total_minor"] == 15000
            else:
                assert result["total_minor"] == 14000
                assert result["pricing_snapshot"]["delivery_fee_minor"] == result["delivery_fee_minor"] == 1000
                assert result["total_minor"] == result["subtotal_minor"] + result["delivery_fee_minor"]
                assert send(28081, "/orders", "POST", body,
                            {**customer, "Idempotency-Key": f"concurrent-price-{index}"}, 201) == result

        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
            futures = []
            for i in range(10):
                futures.append(executor.submit(concurrent_edit, i))
                futures.append(executor.submit(concurrent_create, i * 2))
                futures.append(executor.submit(concurrent_create, i * 2 + 1))
            for future in futures:
                future.result()
        send(28080, path, "PUT", {**settings, "delivery_enabled": False, "pickup_enabled": False}, manager)
        send(28081, "/orders", "POST", body, {**customer, "Idempotency-Key": "disabled-delivery"}, 409)
        send(28081, "/orders", "POST", pickup, {**customer, "Idempotency-Key": "disabled-pickup"}, 409)
        # Existing financial/fulfillment snapshots survive disabling the restaurant's options.
        oid = created["id"]
        base = f"/restaurants/1/orders/{oid}"
        action(28081, base + "/accept", manager)
        eventually(lambda: any(d["order_id"] == oid for d in request(28085, "/deliveries", headers=dispatcher)))
        delivery = next(d for d in request(28085, "/deliveries", headers=dispatcher) if d["order_id"] == oid)
        assert delivery["amount_to_collect_minor"] == 14000 and delivery["delivery_location"] == {"latitude": 38.56, "longitude": 68.77}
        courier = request(28085, "/couriers", {"name": "Pricing courier"}, dispatcher)
        courier_headers = {"X-Dev-Courier-ID": str(courier["id"])}
        dbase = f"/deliveries/{delivery['id']}"
        action(28085, dbase + "/assign", dispatcher, {"courier_id": courier["id"]})
        eventually(lambda: request(28081, f"/orders/{oid}", headers=customer).get("courier_id") == courier["id"])
        action(28081, base + "/prepare", manager)
        action(28081, base + "/ready", manager)
        eventually(lambda: request(28085, dbase, headers=courier_headers)["ready_for_pickup"])
        action(28085, dbase + "/pickup", courier_headers)
        eventually(lambda: request(28081, f"/orders/{oid}", headers=customer)["status"] == "ON_THE_WAY")
        result = action(28085, dbase + "/complete", courier_headers, {"payment_confirmed": True, "receipt_reference": "PRICING-POS"})
        assert result["confirmation"]["amount_minor"] == 14000
        action(28085, dbase + "/complete", courier_headers, {"payment_confirmed": True, "receipt_reference": "PRICING-POS"})
        eventually(lambda: request(28081, f"/orders/{oid}", headers=customer)["status"] == "DELIVERED")
        assert request(28081, f"/orders/{oid}", headers=customer)["payment_status"] == "REPORTED_COLLECTED"
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM payment_confirmations WHERE amount_minor=14000") == "1"
        assert sql("postgres", "orders", "orders_service", f"SELECT state FROM order_sagas WHERE order_id={oid}") == "COMPLETED"
        assert send(28081, "/orders", "POST", body, headers, 201) == created
        send(28080, tariff_path, "DELETE", headers=manager, expected=204)
        print("PASS: pricing ACL, zones, minimum, pickup, 409 totals, immutable retries, full POS and completed Saga.", flush=True)
    finally:
        compose("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
