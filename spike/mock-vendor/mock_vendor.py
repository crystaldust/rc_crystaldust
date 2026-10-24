#!/usr/bin/env python3
"""Controllable mock vendor HTTP server for Message Hub spike scenarios.

Any path whose first segment is not reserved is treated as a vendor endpoint:
    POST /vendor-a/hook   -> vendor "vendor-a"

Control plane (reserved prefixes):
    POST   /_control/<vendor>   JSON: {"status", "delay_ms", "body", "fail_first_n"}
    GET    /_control/<vendor>   current behaviour
    GET    /_requests/<vendor>  received requests (JSON list)
    DELETE /_requests           clear all recorded requests and failure counters

Standard library only, so it runs anywhere python3 exists.
"""

import argparse
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DEFAULT_BEHAVIOUR = {"status": 200, "delay_ms": 0, "body": {"ok": True}, "fail_first_n": 0}
FAILURE_STATUS = 500
MIN_STATUS, MAX_STATUS = 100, 599
MAX_DELAY_MS = 120_000
MAX_BODY_BYTES = 10 * 1024 * 1024
RESERVED_PREFIXES = ("_control", "_requests")


class ValidationError(ValueError):
    pass


def validate_behaviour(raw):
    """Return a new behaviour dict built from raw input, or raise ValidationError."""
    if not isinstance(raw, dict):
        raise ValidationError("control body must be a JSON object")
    unknown = set(raw) - set(DEFAULT_BEHAVIOUR)
    if unknown:
        raise ValidationError(f"unknown fields: {sorted(unknown)}")
    status = raw.get("status", DEFAULT_BEHAVIOUR["status"])
    delay_ms = raw.get("delay_ms", DEFAULT_BEHAVIOUR["delay_ms"])
    fail_first_n = raw.get("fail_first_n", DEFAULT_BEHAVIOUR["fail_first_n"])
    if not _is_int(status) or not MIN_STATUS <= status <= MAX_STATUS:
        raise ValidationError(f"status must be an integer in [{MIN_STATUS}, {MAX_STATUS}]")
    if not _is_int(delay_ms) or not 0 <= delay_ms <= MAX_DELAY_MS:
        raise ValidationError(f"delay_ms must be an integer in [0, {MAX_DELAY_MS}]")
    if not _is_int(fail_first_n) or fail_first_n < 0:
        raise ValidationError("fail_first_n must be a non-negative integer")
    return {
        "status": status,
        "delay_ms": delay_ms,
        "body": raw.get("body", DEFAULT_BEHAVIOUR["body"]),
        "fail_first_n": fail_first_n,
    }


def _is_int(value):
    return isinstance(value, int) and not isinstance(value, bool)


class VendorState:
    """Thread-safe store; maps are replaced, never mutated in place."""

    def __init__(self, log_path):
        self._lock = threading.Lock()
        self._behaviours = {}
        self._served = {}
        self._requests = {}
        self._log_path = log_path

    def behaviour(self, vendor):
        with self._lock:
            return self._behaviours.get(vendor, DEFAULT_BEHAVIOUR)

    def set_behaviour(self, vendor, behaviour):
        with self._lock:
            self._behaviours = {**self._behaviours, vendor: behaviour}
            self._served = {**self._served, vendor: 0}

    def record(self, vendor, entry):
        """Record a request; return (status, behaviour) to answer it with."""
        with self._lock:
            behaviour = self._behaviours.get(vendor, DEFAULT_BEHAVIOUR)
            served = self._served.get(vendor, 0)
            self._served = {**self._served, vendor: served + 1}
            self._requests = {**self._requests, vendor: [*self._requests.get(vendor, []), entry]}
            if self._log_path:
                with open(self._log_path, "a", encoding="utf-8") as log_file:
                    log_file.write(json.dumps({"vendor": vendor, **entry}) + "\n")
        injected = served < behaviour["fail_first_n"]
        return (FAILURE_STATUS if injected else behaviour["status"]), injected, behaviour

    def requests(self, vendor):
        with self._lock:
            return list(self._requests.get(vendor, []))

    def clear(self):
        with self._lock:
            self._requests = {}
            self._served = {}


def make_handler(state):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt, *args):
            sys.stderr.write("[mock-vendor] " + (fmt % args) + "\n")

        def _segments(self):
            return [s for s in self.path.split("?", 1)[0].split("/") if s]

        def _send_json(self, status, payload):
            data = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def _read_body(self):
            length = int(self.headers.get("Content-Length") or 0)
            if length > MAX_BODY_BYTES:
                raise ValidationError(f"body exceeds {MAX_BODY_BYTES} bytes")
            return self.rfile.read(length) if length else b""

        def _handle_control(self, method, segments):
            if segments[0] == "_requests":
                if method == "DELETE" and len(segments) == 1:
                    state.clear()
                    return self._send_json(200, {"cleared": True})
                if method == "GET" and len(segments) == 2:
                    return self._send_json(200, state.requests(segments[1]))
            if segments[0] == "_control" and len(segments) == 2:
                vendor = segments[1]
                if method == "GET":
                    return self._send_json(200, state.behaviour(vendor))
                if method == "POST":
                    try:
                        behaviour = validate_behaviour(json.loads(self._read_body() or b"{}"))
                    except (ValidationError, json.JSONDecodeError) as err:
                        return self._send_json(400, {"error": str(err)})
                    state.set_behaviour(vendor, behaviour)
                    return self._send_json(200, behaviour)
            return self._send_json(404, {"error": "unknown control endpoint"})

        def _handle_vendor(self, method, segments):
            try:
                raw_body = self._read_body()
            except ValidationError as err:
                return self._send_json(413, {"error": str(err)})
            entry = {
                "received_at": time.time(),
                "method": method,
                "path": self.path,
                "headers": dict(self.headers.items()),
                "body": raw_body.decode("utf-8", errors="replace"),
            }
            status, injected, behaviour = state.record(segments[0], entry)
            if behaviour["delay_ms"]:
                time.sleep(behaviour["delay_ms"] / 1000)
            body = {"error": "injected failure"} if injected else behaviour["body"]
            return self._send_json(status, body)

        def _dispatch(self, method):
            segments = self._segments()
            if not segments:
                return self._send_json(200, {"service": "mock-vendor"})
            if segments[0] in RESERVED_PREFIXES:
                return self._handle_control(method, segments)
            return self._handle_vendor(method, segments)

        def do_GET(self):
            self._dispatch("GET")

        def do_POST(self):
            self._dispatch("POST")

        def do_PUT(self):
            self._dispatch("PUT")

        def do_PATCH(self):
            self._dispatch("PATCH")

        def do_DELETE(self):
            self._dispatch("DELETE")

    return Handler


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=18080)
    parser.add_argument("--log", default=None, help="append received requests to this JSONL file")
    args = parser.parse_args()

    server = ThreadingHTTPServer((args.host, args.port), make_handler(VendorState(args.log)))
    server.daemon_threads = True
    sys.stderr.write(f"[mock-vendor] listening on {args.host}:{args.port}\n")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
