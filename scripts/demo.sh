#!/usr/bin/env bash
# End-to-end demo of the hub on a laptop (docs/testing/local-testing.md).
#
# Starts PostgreSQL (Docker), spike/mock-vendor and `messagehub serve --role=all`
# with config/examples/local-demo.yaml, submits one event fanned out to three
# destinations, shows the outcome of each (success / retried / DEAD), replays
# the DEAD one and shows the masked attempt record. Everything is torn down at
# the end unless KEEP=1.
#
# Usage:  scripts/demo.sh            (from the repo root, or via `make demo`)
#         KEEP=1 scripts/demo.sh     keep everything running to explore by hand
#
# Requirements: docker (Colima works), python3, go, curl.
# The API keys and the vendor token are throwaway values generated for this run.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PG_PORT="${PG_PORT:-55432}"
HUB_ADDR="${HUB_ADDR:-127.0.0.1:18090}"
MOCK_PORT=18080 # fixed: the URLs in local-demo.yaml use it
PG_CONTAINER="mh-demo-pg"
WAIT_SECONDS=30
KEEP="${KEEP:-0}"

WORK="$(mktemp -d)"
chmod 700 "$WORK"
HUB_PID=""
MOCK_PID=""

# The shell may route localhost through an HTTP proxy; never do that here.
export NO_PROXY='*' no_proxy='*'
if [[ -z "${DOCKER_HOST:-}" && -S "$HOME/.colima/default/docker.sock" ]]; then
  export DOCKER_HOST="unix://$HOME/.colima/default/docker.sock"
fi

log() { printf '\n== %s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [[ "$KEEP" == "1" ]]; then
    return
  fi
  [[ -n "$HUB_PID" ]] && kill "$HUB_PID" 2>/dev/null && wait "$HUB_PID" 2>/dev/null || true
  [[ -n "$MOCK_PID" ]] && kill "$MOCK_PID" 2>/dev/null && wait "$MOCK_PID" 2>/dev/null || true
  docker stop "$PG_CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

for cmd in docker python3 go curl; do
  command -v "$cmd" >/dev/null || fail "$cmd is required"
done
docker info >/dev/null 2>&1 || fail "docker is not running (Colima: colima start)"
if curl -s "localhost:$MOCK_PORT" >/dev/null 2>&1; then
  fail "port $MOCK_PORT is in use; stop whatever listens there (an old mock-vendor?)"
fi

# json_get FILE EXPR evaluates a Python expression on the parsed JSON (d).
json_get() { python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print($2)" "$1"; }

hub() { # hub METHOD PATH KEY [BODY]
  local args=(-s -X "$1" "http://$HUB_ADDR$2" -H "Authorization: Bearer $3")
  [[ $# -ge 4 ]] && args+=(-H 'Content-Type: application/json' -d "$4")
  curl "${args[@]}"
}

wait_for() { # wait_for DESCRIPTION COMMAND...
  local what="$1"; shift
  for _ in $(seq "$WAIT_SECONDS"); do
    "$@" >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "timed out waiting for $what"
}

log "build"
make build >/dev/null

log "start PostgreSQL ($PG_CONTAINER on port $PG_PORT)"
docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
docker run -d --rm --name "$PG_CONTAINER" -e POSTGRES_USER=hub -e POSTGRES_PASSWORD=hub \
  -e POSTGRES_DB=messagehub -p "$PG_PORT:5432" postgres:16-alpine >/dev/null
wait_for "PostgreSQL" docker exec "$PG_CONTAINER" pg_isready -U hub -d messagehub

log "start mock vendor on 127.0.0.1:$MOCK_PORT"
python3 spike/mock-vendor/mock_vendor.py --host 127.0.0.1 --port "$MOCK_PORT" \
  --log "$WORK/mock-requests.jsonl" >"$WORK/mock.log" 2>&1 &
MOCK_PID=$!
wait_for "mock vendor" curl -sf "localhost:$MOCK_PORT/_control/vendor-a"

log "generate throwaway API keys"
bin/messagehub keygen >"$WORK/source.key" 2>/dev/null
bin/messagehub keygen >"$WORK/operator.key" 2>/dev/null
SOURCE_KEY="$(awk '/^key:/ {print $2}' "$WORK/source.key")"
OPERATOR_KEY="$(awk '/^key:/ {print $2}' "$WORK/operator.key")"

cat >"$WORK/demo.env" <<EOF
export NO_PROXY='*' no_proxy='*'
export HUB_CONFIG_PATH='$ROOT/config/examples/local-demo.yaml'
export HUB_DATABASE_URL='postgres://hub:hub@127.0.0.1:$PG_PORT/messagehub?sslmode=disable'
export HUB_HTTP_ADDR='$HUB_ADDR'
export HUB_KEY_DEMO_SOURCE_SHA256='$(awk '/^sha256:/ {print $2}' "$WORK/source.key")'
export HUB_KEY_DEMO_OPERATOR_SHA256='$(awk '/^sha256:/ {print $2}' "$WORK/operator.key")'
export DEMO_VENDOR_TOKEN='demo-vendor-token-abc123'
export SOURCE_KEY='$SOURCE_KEY'
export OPERATOR_KEY='$OPERATOR_KEY'
EOF
chmod 600 "$WORK/demo.env"
# shellcheck disable=SC1091
. "$WORK/demo.env"

log "check config and migrate"
bin/messagehub config check
bin/messagehub migrate up

log "start hub (serve --role=all on $HUB_ADDR)"
bin/messagehub serve --role=all >"$WORK/serve.log" 2>&1 &
HUB_PID=$!
wait_for "hub /readyz" curl -sf "http://$HUB_ADDR/readyz"

log "set mock behaviours: vendor-a ok, vendor-b fails twice, vendor-c business error"
curl -s -X POST "localhost:$MOCK_PORT/_control/vendor-a" -d '{"status":200}' >/dev/null
curl -s -X POST "localhost:$MOCK_PORT/_control/vendor-b" -d '{"status":200,"fail_first_n":2}' >/dev/null
curl -s -X POST "localhost:$MOCK_PORT/_control/vendor-c" -d '{"status":200,"body":{"code":1001}}' >/dev/null

log "submit one order.paid event"
hub POST /v1/events "$SOURCE_KEY" \
  '{"source_event_id":"demo-'"$(date +%s)"'","event_type":"order.paid","occurred_at":"2026-10-24T08:00:00Z","payload":{"order_id":"o-1001","amount":199.5}}' \
  | tee "$WORK/ingest.json"
echo
EVENT_ID="$(json_get "$WORK/ingest.json" 'd["data"]["event_id"]')"

show_event() {
  hub GET "/v1/events/$EVENT_ID" "$SOURCE_KEY" >"$WORK/event.json"
  json_get "$WORK/event.json" '"\n".join("  %-9s %-16s %s" % (x["destination_id"], x["status"], x["id"]) for x in d["data"]["deliveries"])'
}
settled() {
  hub GET "/v1/events/$EVENT_ID" "$SOURCE_KEY" >"$WORK/event.json"
  json_get "$WORK/event.json" 'all(x["status"] in ("SUCCEEDED","DEAD") for x in d["data"]["deliveries"]) or sys.exit(1)'
}
delivery_of() { json_get "$WORK/event.json" "[x['id'] for x in d['data']['deliveries'] if x['destination_id']=='$1'][0]"; }
show_attempts() {
  hub GET "/v1/deliveries/$1" "$SOURCE_KEY" >"$WORK/delivery.json"
  json_get "$WORK/delivery.json" '"\n".join("  #%s %-10s status=%s error=%s" % (a["number"], a["outcome"], a.get("status_code"), a.get("error")) for a in reversed(d["data"]["attempts"]))'
}

log "wait until every delivery is SUCCEEDED or DEAD (vendor-b retries after 1s and 2s)"
wait_for "deliveries to settle" settled
show_event
for dest in vendor-a vendor-b vendor-c; do
  echo "  attempts of $dest:"
  show_attempts "$(delivery_of "$dest")"
done

log "attempt record of vendor-a (secrets, Authorization and signature are masked)"
hub GET "/v1/deliveries/$(delivery_of vendor-a)" "$SOURCE_KEY" >"$WORK/delivery.json"
json_get "$WORK/delivery.json" 'json.dumps(d["data"]["attempts"][0]["request"], indent=2)'

log "fix vendor-c and replay its DEAD delivery as the operator"
curl -s -X POST "localhost:$MOCK_PORT/_control/vendor-c" -d '{"status":200,"body":{"code":0}}' >/dev/null
VENDOR_C="$(delivery_of vendor-c)"
hub POST "/v1/deliveries/$VENDOR_C/replay" "$OPERATOR_KEY" '{"reason":"vendor fixed the business error"}'
echo
wait_for "replayed delivery to succeed" bash -c \
  "curl -s 'http://$HUB_ADDR/v1/deliveries/$VENDOR_C' -H 'Authorization: Bearer $SOURCE_KEY' | grep -q '\"status\":\"SUCCEEDED\"'"
show_event
echo "  attempts of vendor-c after replay:"
show_attempts "$VENDOR_C"

log "done"
if [[ "$KEEP" == "1" ]]; then
  cat <<EOF
Everything is still running (KEEP=1):
  hub          http://$HUB_ADDR   (pid $HUB_PID, log $WORK/serve.log)
  mock vendor  http://127.0.0.1:$MOCK_PORT   (pid $MOCK_PID)
  postgres     127.0.0.1:$PG_PORT  (container $PG_CONTAINER)
Load the environment (bash/zsh):  . $WORK/demo.env
                           (fish):  bash -c '. $WORK/demo.env; exec fish'
Stop: kill $HUB_PID $MOCK_PID; docker stop $PG_CONTAINER; rm -rf $WORK
EOF
fi
