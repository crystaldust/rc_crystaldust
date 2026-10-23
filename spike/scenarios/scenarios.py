#!/usr/bin/env python3
"""Run PoC scenarios S1-S8 against a candidate (outpost | svix) using the mock vendor.

Usage:
    python3 spike/scenarios/scenarios.py outpost S1 S2 ...   (or "all")

Prerequisites: candidate stack up (see run_scenarios.md), mock vendor on :18080,
candidate setup done (two destinations vendor-a / vendor-b subscribed to order.paid).
Results are printed and written to .spike/results/<candidate>-<scenario>.json.
Standard library only; local HTTP bypasses any configured proxy.
"""

import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

MOCK = "http://localhost:18080"
RESULTS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", ".spike", "results")
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
TOPIC = "order.paid"
VENDORS = ("vendor-a", "vendor-b")


def http(method, url, body=None, headers=None, timeout=30):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with OPENER.open(req, timeout=timeout) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw)
        except ValueError:
            return err.code, raw.decode(errors="replace")


# ---------------------------------------------------------------- mock vendor
def mock_reset():
    http("DELETE", f"{MOCK}/_requests")
    for vendor in VENDORS:
        mock_set(vendor)


def mock_set(vendor, **behaviour):
    status, body = http("POST", f"{MOCK}/_control/{vendor}", behaviour)
    if status != 200:
        raise RuntimeError(f"mock control failed: {status} {body}")


def mock_requests(vendor):
    return http("GET", f"{MOCK}/_requests/{vendor}")[1]


# ---------------------------------------------------------------- adapters
class Outpost:
    name = "outpost"
    base = "http://localhost:3333/api/v1"
    auth = {"Authorization": "Bearer spike-api-key"}

    def __init__(self):
        _, dests = http("GET", f"{self.base}/tenants/hub/destinations", headers=self.auth)
        self.dest = {d["config"]["url"].split("/")[3]: d for d in dests}

    def publish(self, event_id, data, retry=True):
        body = {"id": event_id, "tenant_id": "hub", "topic": TOPIC, "eligible_for_retry": retry, "data": data}
        return http("POST", f"{self.base}/publish", body, self.auth)

    def set_custom_headers(self, vendor, headers):
        dest = self.dest[vendor]
        config = {**dest["config"], "custom_headers": json.dumps(headers)}
        return http("PATCH", f"{self.base}/tenants/hub/destinations/{dest['id']}", {"config": config}, self.auth)

    def attempts(self, event_id, vendor=None):
        url = f"{self.base}/attempts?tenant_id=hub&event_id={event_id}&limit=100"
        if vendor:
            url += f"&destination_id={self.dest[vendor]['id']}"
        _, body = http("GET", url, headers=self.auth)
        models = body.get("models", []) if isinstance(body, dict) else []
        return [{"status": a.get("status"), "code": a.get("code"), "time": a.get("time")} for a in models]

    def replay(self, event_id, vendor):
        return http("POST", f"{self.base}/retry",
                    {"event_id": event_id, "destination_id": self.dest[vendor]["id"]}, self.auth)


class Svix:
    name = "svix"
    base = os.environ.get("SVIX_BASE", "http://localhost:8071/api/v1")

    def __init__(self):
        self.auth = {"Authorization": f"Bearer {os.environ['SVIX_TOKEN']}"}
        _, eps = http("GET", f"{self.base}/app/hub/endpoint/?limit=50", headers=self.auth)
        self.dest = {e["url"].split("/")[3]: e for e in eps["data"]}
        self.msg_ids = {}

    def publish(self, event_id, data, retry=True):
        body = {"eventType": TOPIC, "eventId": event_id, "payload": data}
        status, resp = http("POST", f"{self.base}/app/hub/msg/", body, self.auth)
        if isinstance(resp, dict) and "id" in resp:
            self.msg_ids = {**self.msg_ids, event_id: resp["id"]}
        return status, resp

    def set_custom_headers(self, vendor, headers):
        return http("PATCH", f"{self.base}/app/hub/endpoint/{self.dest[vendor]['id']}/headers/",
                    {"headers": headers}, self.auth)

    def attempts(self, event_id, vendor=None):
        msg_id = self.msg_ids.get(event_id)
        if not msg_id:
            return []
        out = []
        for name, ep in self.dest.items():
            if vendor and name != vendor:
                continue
            _, body = http("GET", f"{self.base}/app/hub/attempt/endpoint/{ep['id']}/?limit=100", headers=self.auth)
            data = body.get("data", []) if isinstance(body, dict) else []
            out += [{"status": a.get("status"), "code": a.get("responseStatusCode"), "time": a.get("timestamp")}
                    for a in data if a.get("msgId") == msg_id]
        return out

    def replay(self, event_id, vendor):
        return http("POST", f"{self.base}/app/hub/msg/{self.msg_ids[event_id]}/endpoint/{self.dest[vendor]['id']}/resend/",
                    None, self.auth)


# ---------------------------------------------------------------- helpers
def wait_for(predicate, timeout, interval=0.5):
    deadline = time.time() + timeout
    while time.time() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(interval)
    return predicate()


def percentile(values, pct):
    ordered = sorted(values)
    if not ordered:
        return None
    return ordered[min(len(ordered) - 1, int(round(pct / 100 * (len(ordered) - 1))))]


def uid(prefix):
    return f"{prefix}-{int(time.time() * 1000)}"


# ---------------------------------------------------------------- scenarios
def s1(c):
    event_id = uid("s1")
    first = c.publish(event_id, {"uid": 1, "amount": 99})
    second = c.publish(event_id, {"uid": 1, "amount": 99})
    wait_for(lambda: all(mock_requests(v) for v in VENDORS), 10)
    time.sleep(2)
    received = {v: mock_requests(v) for v in VENDORS}
    first_a = received["vendor-a"][0] if received["vendor-a"] else {}
    return {
        "event_id": event_id,
        "publish_first": first, "publish_duplicate": second,
        "received_count": {v: len(r) for v, r in received.items()},
        "headers_vendor_a": first_a.get("headers"), "body_vendor_a": first_a.get("body"),
    }


def s2(c):
    patch = c.set_custom_headers("vendor-b", {"X-Api-Key": "spike-key"})
    time.sleep(1)
    c.publish(uid("s2"), {"uid": 2, "amount": 10})
    wait_for(lambda: all(mock_requests(v) for v in VENDORS), 10)
    reqs = {v: mock_requests(v) for v in VENDORS}
    headers_b = reqs["vendor-b"][0]["headers"] if reqs["vendor-b"] else {}
    return {
        "patch_response": patch,
        "vendor_b_has_x_api_key": any(k.lower() == "x-api-key" for k in headers_b),
        "vendor_a_body": reqs["vendor-a"][0]["body"] if reqs["vendor-a"] else None,
        "note": "no configuration exists to reshape the body to {uid, evt}; body is sent exactly as published",
    }


def s3(c):
    mock_set("vendor-a", status=200, body={"code": 1001, "msg": "business failure"})
    event_id = uid("s3")
    c.publish(event_id, {"uid": 3})
    wait_for(lambda: c.attempts(event_id, "vendor-a"), 20)
    time.sleep(5)
    return {"vendor_a_attempts": c.attempts(event_id, "vendor-a"), "vendor_a_requests": len(mock_requests("vendor-a"))}


def s4(c):
    mock_set("vendor-a", fail_first_n=3)
    event_id = uid("s4")
    c.publish(event_id, {"uid": 4})
    wait_for(lambda: len(mock_requests("vendor-a")) >= 4, 90)
    time.sleep(3)
    times = [r["received_at"] for r in mock_requests("vendor-a")]
    return {
        "requests": len(times),
        "intervals_s": [round(b - a, 2) for a, b in zip(times, times[1:])],
        "attempts": c.attempts(event_id, "vendor-a"),
    }


def s5(c, exhaust_wait_s=None):
    mock_set("vendor-a", status=500)
    event_id = uid("s5")
    c.publish(event_id, {"uid": 5})
    time.sleep(exhaust_wait_s or int(os.environ.get("S5_EXHAUST_WAIT_S", "25")))
    exhausted = len(mock_requests("vendor-a"))
    failed = c.attempts(event_id, "vendor-a")
    mock_set("vendor-a", status=200)
    replay = c.replay(event_id, "vendor-a")
    wait_for(lambda: len(mock_requests("vendor-a")) > exhausted, 15)
    time.sleep(5)
    return {
        "requests_until_exhausted": exhausted,
        "attempts_before_replay": failed,
        "replay_response": replay,
        "requests_after_replay": len(mock_requests("vendor-a")),
        "attempts_after_replay": c.attempts(event_id, "vendor-a"),
    }


def s6(c, events=20):
    mock_set("vendor-a", delay_ms=10000)
    for i in range(events):
        c.publish(uid(f"s6-{i}"), {"uid": i, "sent_at": time.time()}, retry=False)
    wait_for(lambda: len(mock_requests("vendor-b")) >= events, 180, interval=1)
    lat = [r["received_at"] - json.loads(r["body"])["sent_at"] for r in mock_requests("vendor-b")]
    return {
        "note": "vendor-a delays 10s; vendor-b responds immediately",
        "events": events, "vendor_b_received": len(lat),
        "vendor_b_latency_p50_s": round(percentile(lat, 50), 3) if lat else None,
        "vendor_b_latency_max_s": round(max(lat), 3) if lat else None,
    }


def s7(c):
    mock_set("vendor-a", fail_first_n=1)
    order = ["created", "paid", "cancelled"]
    for step in order:
        c.publish(uid(f"s7-{step}"), {"order": 7, "step": step})
        time.sleep(0.1)
    wait_for(lambda: len(mock_requests("vendor-a")) >= 4, 60)
    time.sleep(2)
    arrivals = [json.loads(r["body"])["step"] for r in mock_requests("vendor-a")]
    successful = arrivals[1:]  # the first request received the injected failure
    return {"publish_order": order, "arrival_order_incl_failed_first": arrivals,
            "success_order": successful, "ordered": successful == order}


def s8(c, rate=50, seconds=60):
    total = rate * seconds
    publish_ms = []
    lock = threading.Lock()

    def send(i):
        sent_at = time.time()
        status, _ = c.publish(uid(f"s8-{i}"), {"i": i, "sent_at": sent_at}, retry=False)
        with lock:
            publish_ms.append(((time.time() - sent_at) * 1000, status))

    start = time.time()
    with ThreadPoolExecutor(max_workers=20) as pool:
        for i in range(total):
            pool.submit(send, i)
            time.sleep(max(0, start + (i + 1) / rate - time.time()))
    wait_for(lambda: all(len(mock_requests(v)) >= total for v in VENDORS), 180, interval=2)
    result = {"events": total, "rate_per_s": rate,
              "publish_errors": sum(1 for _, s in publish_ms if s >= 300),
              "publish_api_p99_ms": round(percentile([m for m, _ in publish_ms], 99), 1)}
    for vendor in VENDORS:
        lat = [(r["received_at"] - json.loads(r["body"])["sent_at"]) * 1000 for r in mock_requests(vendor)]
        result[vendor] = {"received": len(lat), "p50_ms": round(percentile(lat, 50), 1),
                          "p99_ms": round(percentile(lat, 99), 1), "max_ms": round(max(lat), 1)} if lat else {"received": 0}
    return result


SCENARIOS = {"S1": s1, "S2": s2, "S3": s3, "S4": s4, "S5": s5, "S6": s6, "S7": s7, "S8": s8}
ADAPTERS = {"outpost": Outpost, "svix": Svix}


def main():
    if len(sys.argv) < 3 or sys.argv[1] not in ADAPTERS:
        sys.exit(__doc__)
    candidate = ADAPTERS[sys.argv[1]]()
    names = list(SCENARIOS) if sys.argv[2:] == ["all"] else sys.argv[2:]
    unknown = [n for n in names if n not in SCENARIOS]
    if unknown:
        sys.exit(f"unknown scenarios: {unknown}")
    suffix = os.environ.get("RESULT_SUFFIX", "")
    os.makedirs(RESULTS_DIR, exist_ok=True)
    for name in names:
        mock_reset()
        print(f"== {candidate.name} {name}{suffix}", flush=True)
        result = SCENARIOS[name](candidate)
        print(json.dumps(result, indent=2, ensure_ascii=False), flush=True)
        with open(os.path.join(RESULTS_DIR, f"{candidate.name}-{name}{suffix}.json"), "w", encoding="utf-8") as out:
            json.dump(result, out, indent=2, ensure_ascii=False)


if __name__ == "__main__":
    main()
