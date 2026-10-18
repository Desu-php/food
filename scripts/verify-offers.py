#!/usr/bin/env python3
"""Verify offer acceptance through real HTTP, Redis, PostgreSQL, Kafka and Orders Saga."""
import concurrent.futures
import importlib.util
import json
from pathlib import Path
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location("fulfillment", Path(__file__).with_name("verify-fulfillment.py"))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
f.events.COMPOSE[f.events.COMPOSE.index("food-fulfillment-e2e")] = "food-offers-e2e"
f.events.ENV.update(SAGA_ASSIGNMENT_TIMEOUT="5m", SAGA_CREATE_TIMEOUT="2m",
                    DISPATCH_ENABLED="true", DISPATCH_RETRY_INTERVAL="500ms",
                    LOCATION_TTL="5m", LOCATION_MIN_INTERVAL="1s")
compose, request, sql, eventually, action = f.compose, f.request, f.sql, f.eventually, f.action
manager, dispatcher = {"X-Dev-User-ID": "10"}, {"X-Dev-User-ID": "20"}
customer = {"X-Dev-Customer-ID": "7"}


def send(port, path, method, headers, body=None, expected=200):
    req = urllib.request.Request(f"http://localhost:{port}{path}",
                                 data=b"" if body is None else json.dumps(body).encode(),
                                 method=method, headers={"Content-Type": "application/json", **headers})
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            assert response.status == expected
            raw = response.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        assert error.code == expected, (error.code, error.read())


def main():
    try:
        compose("up", "--build", "-d", "--wait", "--wait-timeout", "240",
                "--scale", "delivery-outbox=2", "--scale", "orders-saga=2")
        send(28080, "/restaurants/1", "PATCH", {"X-Dev-User-ID": "1"},
             {"latitude": 38.56, "longitude": 68.77})
        f.events.configure_delivery()
        tariff = send(28080, "/restaurants/1/delivery-settings", "GET", manager)["tariffs"][0]
        send(28080, f"/restaurants/1/delivery-tariffs/{tariff['id']}", "PUT", manager,
             {"max_distance_meters": 8000, "delivery_fee_minor": 1000})
        category = request(28080, "/restaurants/1/categories", {"name": "Offers", "sort_order": 0})
        item = request(28080, "/restaurants/1/items", {"category_id": category["id"], "name": "Dish", "price_minor": 25000, "currency": "TJS", "is_available": True})

        def courier(name, latitude):
            c = request(28085, "/couriers", {"name": name}, dispatcher)
            headers = {"X-Dev-Courier-ID": str(c["id"])}
            send(28085, "/couriers/me/location", "POST", headers,
                 {"latitude": latitude, "longitude": 68.77}, 204)
            return c, headers

        c1, h1 = courier("Nearest", 38.56)
        c2, h2 = courier("Next", 38.561)
        body = {"restaurant_id": 1, "currency": "TJS", "fulfillment_type": "DELIVERY", "delivery_latitude": 38.56, "delivery_longitude": 68.77,
                "delivery_address": "Customer address", "expected_total_minor": 26000,
                "items": [{"menu_item_id": item["id"], "quantity": 1}]}

        def create(key):
            order = request(28081, "/orders", body, {**customer, "Idempotency-Key": key})
            assert order["subtotal_minor"] == 25000 and order["delivery_fee_minor"] == 1000 and order["total_minor"] == 26000
            base = f"/restaurants/1/orders/{order['id']}"
            action(28081, base + "/accept", manager)
            return order["id"], base

        def offers(headers):
            return request(28085, "/couriers/me/offers", headers=headers)

        oid, base = create("offer-acceptance")
        eventually(lambda: len(offers(h1)) == 1)
        first = offers(h1)[0]
        assert offers(h2) == []
        action(28081, base + "/prepare", manager, expected=409)
        action(28085, f"/courier-offers/{first['id']}/accept", h2, expected=404)
        action(28085, f"/courier-offers/{first['id']}/decline", h1)
        action(28085, f"/courier-offers/{first['id']}/decline", h1)
        eventually(lambda: len(offers(h2)) == 1)
        second = offers(h2)[0]
        assert second["delivery_id"] == first["delivery_id"]
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM outbox_events WHERE event_type='CourierAssigned'") == "0"
        # Acceptance commits even during a broker outage; cooking remains gated.
        compose("stop", "orders-delivery-consumer", "redpanda")
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
            results = list(executor.map(lambda _: action(28085, f"/courier-offers/{second['id']}/accept", h2), range(2)))
        assert all(o["status"] == "ACCEPTED" for o in results)
        assert sql("postgres", "delivery", "delivery_service", "SELECT count(*) FROM outbox_events WHERE event_type='CourierAssigned'") == "1"
        action(28081, base + "/prepare", manager, expected=409)
        compose("up", "-d", "--wait", "redpanda", "orders-delivery-consumer")
        eventually(lambda: request(28081, base + "/saga", headers=manager)["state"] == "COURIER_ASSIGNED")
        action(28081, base + "/prepare", manager)

        cancelled, _ = create("offer-cancel")
        eventually(lambda: len(offers(h1)) == 1)
        cancelled_offer = offers(h1)[0]
        action(28081, f"/orders/{cancelled}/cancel", customer)
        eventually(lambda: sql("postgres", "delivery", "delivery_service", f"SELECT status FROM courier_offers WHERE id={cancelled_offer['id']}") == "CANCELLED")
        action(28085, f"/courier-offers/{cancelled_offer['id']}/accept", h1, expected=409)

        # Persisted TTL recovery: API process is absent when the offer becomes due.
        c3, h3 = courier("Recovery fallback", 38.562)
        create("offer-restart")
        eventually(lambda: len(offers(h1)) == 1)
        expired = offers(h1)[0]
        compose("stop", "delivery")
        sql("postgres", "delivery", "delivery_service", f"UPDATE courier_offers SET created_at=now()-interval '31 seconds',expires_at=now()-interval '1 second' WHERE id={expired['id']}")
        compose("up", "-d", "--wait", "delivery")
        eventually(lambda: sql("postgres", "delivery", "delivery_service", f"SELECT status FROM courier_offers WHERE id={expired['id']}") == "EXPIRED")
        eventually(lambda: len(offers(h3)) == 1)
        assert offers(h3)[0]["delivery_id"] == expired["delivery_id"]
        print("PASS: nearest offer, ownership, decline/cooldown, concurrent acceptance, Kafka outage/recovery, Saga cooking gate, cancellation and persisted TTL restart.", flush=True)
    except Exception:
        print(compose("logs", "--no-color", "--tail=30", "delivery", "delivery-consumer", "orders-saga", "orders-delivery-consumer", capture=True), flush=True)
        raise
    finally:
        compose("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
