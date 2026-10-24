#!/usr/bin/env bash
# Self-check for mock_vendor.py. Usage: bash spike/mock-vendor/self_check.sh [port]
set -u

PORT="${1:-18099}"
BASE="http://127.0.0.1:${PORT}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FAILURES=0

python3 "${SCRIPT_DIR}/mock_vendor.py" --host 127.0.0.1 --port "${PORT}" 2>/dev/null &
MOCK_PID=$!
trap 'kill "${MOCK_PID}" 2>/dev/null' EXIT

for _ in $(seq 1 50); do
  curl -sf "${BASE}/" >/dev/null 2>&1 && break
  sleep 0.1
done

check() {
  local name="$1" expected="$2" actual="$3"
  if [ "${expected}" = "${actual}" ]; then
    echo "PASS ${name}"
  else
    echo "FAIL ${name}: expected '${expected}', got '${actual}'"
    FAILURES=$((FAILURES + 1))
  fi
}

code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

# 1. record request
curl -s -X POST "${BASE}/vendor-a/hook" -d '{"a":1}' >/dev/null
check "record-request" "yes" "$(curl -s "${BASE}/_requests/vendor-a" | grep -q '\\"a\\": 1\|\\"a\\":1' && echo yes || echo no)"

# 2. injected status
curl -s -X POST "${BASE}/_control/vendor-a" -d '{"status":503}' >/dev/null
check "inject-status" "503" "$(code -X POST "${BASE}/vendor-a/hook")"

# 3. fail first N
curl -s -X POST "${BASE}/_control/vendor-a" -d '{"status":200,"fail_first_n":2}' >/dev/null
check "fail-first-n" "500,500,200" \
  "$(code -X POST "${BASE}/vendor-a/hook"),$(code -X POST "${BASE}/vendor-a/hook"),$(code -X POST "${BASE}/vendor-a/hook")"

# 4. delay isolation: slow vendor-a must not block vendor-b
curl -s -X POST "${BASE}/_control/vendor-a" -d '{"delay_ms":3000}' >/dev/null
curl -s -X POST "${BASE}/vendor-a/hook" >/dev/null &
SLOW_PID=$!
sleep 0.2
FAST_SECONDS="$(curl -s -o /dev/null -w '%{time_total}' -X POST "${BASE}/vendor-b/hook")"
check "delay-isolation" "fast" "$(awk -v t="${FAST_SECONDS}" 'BEGIN { print (t < 0.1) ? "fast" : "slow(" t "s)" }')"
wait "${SLOW_PID}" 2>/dev/null

# 5. invalid control keeps previous config
check "invalid-control-400" "400" "$(code -X POST "${BASE}/_control/vendor-a" -d '{"status":"abc"}')"
check "invalid-control-keeps-config" "yes" \
  "$(curl -s "${BASE}/_control/vendor-a" | grep -q '"delay_ms": 3000' && echo yes || echo no)"

# 6. clear
curl -s -X DELETE "${BASE}/_requests" >/dev/null
check "clear" "[]" "$(curl -s "${BASE}/_requests/vendor-a")"

# edge: empty body and ~1MB body
curl -s -X POST "${BASE}/_control/vendor-a" -d '{}' >/dev/null
check "empty-body" "200" "$(code -X POST "${BASE}/vendor-a/hook")"
BIG_FILE="$(mktemp)"
head -c 1048576 /dev/zero | tr '\0' 'x' > "${BIG_FILE}"
check "large-body" "200" "$(code -X POST --data-binary "@${BIG_FILE}" "${BASE}/vendor-a/hook")"
rm -f "${BIG_FILE}"

if [ "${FAILURES}" -eq 0 ]; then
  echo "ALL PASS"
  exit 0
fi
echo "${FAILURES} FAILURE(S)"
exit 1
