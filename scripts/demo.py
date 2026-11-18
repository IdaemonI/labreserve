#!/usr/bin/env python3
"""Check the running synthetic demo through HTTP; only release our own bookings."""
import concurrent.futures
import json
import os
import threading
import urllib.error
import urllib.request

BASE = os.environ.get("BASE_URL", "http://127.0.0.1:8080").rstrip("/")


def call(method, path, data=None):
    payload = None if data is None else json.dumps(data).encode()
    req = urllib.request.Request(BASE + path, data=payload, method=method)
    if payload is not None:
        req.add_header("Content-Type", "application/json")
    try:
        response = urllib.request.urlopen(req, timeout=8)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        return response.status, json.loads(body) if body else None


def main():
    status, devices = call("GET", "/devices")
    assert status == 200, (status, devices)
    sensor = next(device for device in devices if device["id"] == "sensor-a1")
    assert not sensor["occupied"], "sensor-a1 must be free before demo; existing bookings are preserved"
    owned_ids = []
    try:
        barrier = threading.Barrier(16)

        def book(number):
            barrier.wait()
            return call("POST", "/reservations", {"device_id": "sensor-a1", "owner": f"demo-{number}"})

        with concurrent.futures.ThreadPoolExecutor(max_workers=16) as executor:
            results = list(executor.map(book, range(16)))
        owned_ids.extend(body["id"] for status, body in results if status == 201)
        assert sum(status == 201 for status, _ in results) == 1, results
        assert sum(status == 409 for status, _ in results) == 15, results
        print("Concurrent HTTP bookings: 1 created, 15 conflicts (SQL invariant is checked by integration tests).")
        first = owned_ids[0]
        assert call("DELETE", f"/reservations/{first}")[0] == 204
        status, history = call("GET", "/devices/sensor-a1/reservations?limit=100")
        assert status == 200, history
        released_at = next(item["released_at"] for item in history["items"] if item["id"] == first)
        status, second = call("POST", "/reservations", {"device_id": "sensor-a1", "owner": "demo-next"})
        assert status == 201, (status, second)
        owned_ids.append(second["id"])
        assert call("DELETE", f"/reservations/{first}")[0] == 204
        status, history = call("GET", "/devices/sensor-a1/reservations?limit=100")
        assert status == 200, history
        records = {item["id"]: item for item in history["items"]}
        assert records[first]["released_at"] == released_at
        assert records[second["id"]]["released_at"] is None
        print("Repeated old release: original timestamp preserved, new reservation remains active.")
        for device_id, expected, health in [("sensor-a1", 200, "OK"), ("adapter-b1", 200, "FAULT"), ("meter-c1", 504, None)]:
            status, body = call("GET", f"/devices/{device_id}/diagnostics")
            assert status == expected, (device_id, status, body)
            if health:
                assert body["health"] == health, body
            print(f"Diagnostics {device_id}: HTTP {status}" + (f", {health}" if health else " (bounded deadline)"))
        assert call("GET", "/readyz")[0] == 200
        print("Readiness still succeeds after a diagnostics timeout.")
    finally:
        for reservation_id in owned_ids:
            status, body = call("DELETE", f"/reservations/{reservation_id}")
            if status != 204:
                raise RuntimeError(f"Could not release demo reservation {reservation_id}: {status} {body}")


if __name__ == "__main__":
    main()
