#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Backend checks against a local instance: endpoints, validation, the
# result cache and the per-client limits. Starts the backend itself.
#
#   sh test/backend.sh
set -u
cd "$(dirname "$0")/../backend" || exit 1

PORT=${PORT:-8089}
B="http://localhost:$PORT"
PORT=$PORT go run . >/dev/null 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null' EXIT
for i in $(seq 1 60); do curl -fsS "$B/health" >/dev/null 2>&1 && break; sleep 1; done

PY=python3; "$PY" -c pass >/dev/null 2>&1 || PY=python
fails=0
ok()   { echo "ok    $1"; }
fail() { echo "FAIL  $1"; fails=$((fails + 1)); }
check() {
    label=$1; shift
    if "$@" >/dev/null 2>&1; then ok "$label"; else fail "$label"; fi
}
status() { curl -s -o /dev/null -w '%{http_code}' -H "X-Forwarded-For: ${CLIENT:-198.51.100.1}" "$1"; }
is() { [ "$(status "$1")" = "$2" ]; }
json() { curl -s -H "X-Forwarded-For: ${CLIENT:-198.51.100.1}" "$1" | "$PY" -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if ($2) else 1)"; }

echo "== endpoints"
check "health"                      json "$B/health" "d['ok'] and 'version' in d"
check "resolve localhost"           json "$B/resolve?host=localhost" "'127.0.0.1' in d['a']"
check "resolve: invalid host -> 400" is "$B/resolve?host=bad%20host" 400
check "ping: closed port offline"   json "$B/ping?ip=127.0.0.1&port=9&edition=java" "d['state'] == 'offline' and d['error']"
check "ping: invalid ip -> 400"     is "$B/ping?ip=nope&port=1" 400
check "ping: invalid port -> 400"   is "$B/ping?ip=127.0.0.1&port=0" 400
check "ping: invalid edition -> 400" is "$B/ping?ip=127.0.0.1&port=1&edition=pocket" 400
check "unknown endpoint -> 404"     json "$B/nope" "d['error']"
check "POST -> 405 with JSON"       sh -c "curl -s -X POST '$B/ping' | grep -q '\"error\"'"

echo "== cache"
check "second identical probe is cached" json "$B/ping?ip=127.0.0.1&port=9&edition=java" "d.get('cached') and d['age_s'] >= 0"

echo "== limits"
CLIENT=203.0.113.7
for i in 1 2 3 4 5 6 7 8 9 10; do status "$B/ping?ip=127.0.0.1&port=9&edition=java&host=sys$i.example" >/dev/null; done
check "10 systems allowed, same system again ok" is "$B/ping?ip=127.0.0.1&port=9&edition=java&host=sys1.example" 200
check "11th system -> 429"          is "$B/ping?ip=127.0.0.1&port=9&edition=java&host=sys11.example" 429
check "cooldown carries Retry-After" sh -c "curl -s -D - -o /dev/null -H 'X-Forwarded-For: $CLIENT' '$B/resolve?host=localhost' | grep -qi '^Retry-After: '"
CLIENT=203.0.113.8
check "other client unaffected"     is "$B/resolve?host=localhost" 200
unset CLIENT

echo
if [ "$fails" -eq 0 ]; then echo "all checks passed"; else echo "$fails check(s) failed"; exit 1; fi
