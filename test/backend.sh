#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Backend checks against local instances: endpoints, validation, name
# lookups, the internal-address filter, the result cache and the per-client
# limits. Starts two backends itself: one with the filter off, so probes can
# target closed local ports, and one with the default filter.
#
#   sh test/backend.sh
set -u
cd "$(dirname "$0")/../backend" || exit 1

PORT=${PORT:-8089}
B="http://127.0.0.1:$PORT"
F="http://127.0.0.1:$((PORT + 1))"
STATS=$(mktemp -d)
PORT=$PORT FILTER_INTERNAL_TARGETS=false STATE_DIRECTORY=$STATS go run . >/dev/null 2>&1 &
pid=$!
PORT=$((PORT + 1)) go run . >/dev/null 2>&1 &
fpid=$!
trap 'kill $pid $fpid 2>/dev/null; rm -rf "$STATS"' EXIT
for u in "$B" "$F"; do
    i=0
    while [ "$i" -lt 60 ] && ! curl -fsS "$u/health" >/dev/null 2>&1; do sleep 1; i=$((i + 1)); done
done

PY=python3; "$PY" -c pass >/dev/null 2>&1 || PY=python
fails=0
ok()   { echo "ok    $1"; }
fail() { echo "FAIL  $1"; fails=$((fails + 1)); }
check() {
    label=$1; shift
    if "$@" >/dev/null 2>&1; then ok "$label"; else fail "$label"; fi
}
# CLIENT is the forwarded client address. Each section uses its own, so
# the budget and the systems limit of one never reach into another.
status() { curl -s -o /dev/null -w '%{http_code}' -H "X-Forwarded-For: ${CLIENT:-198.51.100.1}" "$1"; }
is() { [ "$(status "$1")" = "$2" ]; }
json() { curl -s -H "X-Forwarded-For: ${CLIENT:-198.51.100.1}" "$1" | "$PY" -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if ($2) else 1)"; }

echo "== endpoints"
CLIENT=198.51.100.1
check "health"                    json "$B/health" "d['ok'] and 'version' in d and 'ipv6' in d"
check "usage numbers written at start" "$PY" -c "import json,sys; sys.exit(0 if all(isinstance(json.load(open(sys.argv[1] + '/stats/' + k + '.json'))['periods'], list) for k in ('minutes','hours','days','months')) else 1)" "$STATS"
check "ping: closed port offline"   json "$B/ping?ip=127.0.0.1&port=9&edition=java" "d['state'] == 'offline' and d['error'] and d['ip'] == '127.0.0.1'"
check "ping: errors per scheme"     json "$B/ping?ip=127.0.0.1&port=9&edition=java" "d['errors'] == {'slp': d['error']}"
check "ping: invalid ip -> 400"     is "$B/ping?ip=nope&port=1" 400
check "ping: invalid port -> 400"   is "$B/ping?ip=127.0.0.1&port=0" 400
check "ping: invalid edition -> 400" is "$B/ping?ip=127.0.0.1&port=1&edition=pocket" 400
check "no resolve endpoint -> 404"  is "$B/resolve?host=localhost" 404
check "unknown endpoint -> 404"     is "$B/nope" 404
check "unknown endpoint: JSON error" json "$B/nope" "d['error']"
check "POST -> 405 with JSON"       sh -c "curl -s -X POST -w '%{http_code}' '$B/ping' | tr -d '\n' | grep -q '\"error\".*405\$'"

echo "== connection IDs"
CLIENT=198.51.100.5
check "id with bedrock -> 400"      is "$B/ping?ip=127.0.0.1&port=9&edition=bedrock&id=x" 400
check "id with a comma -> 400"      is "$B/ping?ip=127.0.0.1&port=9&edition=java&id=a,b" 400
check "id of 65 characters -> 400"  is "$B/ping?ip=127.0.0.1&port=9&edition=java&id=$(printf '%065d' 0)" 400
check "id with a space inside ok"   json "$B/ping?ip=127.0.0.1&port=9&edition=java&id=team%20a" "d['state'] == 'offline'"
status "$B/ping?ip=127.0.0.1&port=9&edition=java" >/dev/null
check "cache keeps IDs apart"       json "$B/ping?ip=127.0.0.1&port=9&edition=java&id=other" "not d.get('cached')"
check "same ID is cached"           json "$B/ping?ip=127.0.0.1&port=9&edition=java&id=other" "d.get('cached')"

echo "== name lookups (localtest.me is public DNS for 127.0.0.1)"
CLIENT=198.51.100.2
check "name resolved by the backend" json "$B/ping?host=localtest.me&family=4&port=9&edition=java" "d['ip'] == '127.0.0.1' and d['state'] == 'offline'"
check "trailing dot accepted"       json "$B/ping?host=LocalTest.me.&family=4&port=9&edition=java" "d['ip'] == '127.0.0.1'"
check "name without family -> 400"  is "$B/ping?host=localtest.me&port=9" 400
check "neither ip nor host -> 400"  is "$B/ping?port=9" 400
check "invalid name -> 400"         is "$B/ping?host=bad!name.example.com&family=4&port=9" 400
check "underscore in a name accepted" json "$B/ping?host=bad_name.example&family=4&port=9" "d['state'] == 'no_dns'"
check "missing record -> no_dns"    json "$B/ping?host=does-not-exist-7f3a.poggensee.it&family=4&port=9" "d['state'] == 'no_dns'"
CLIENT=198.51.100.3
check "single label, no lookup"     json "$B/ping?host=localhost&family=4&port=9" "d['state'] == 'no_dns'"
check "local domain, no lookup"     json "$B/ping?host=printer.local&family=4&port=9" "d['state'] == 'no_dns'"
check "internal domain, no lookup"  json "$B/ping?host=db.internal&family=6&port=9" "d['state'] == 'no_dns'"

echo "== internal-address filter (default on)"
CLIENT=198.51.100.4
for t in 127.0.0.1 10.1.2.3 100.64.0.1 169.254.169.254 172.16.0.1 192.168.1.1 0.0.0.1 \
         224.0.0.1 255.255.255.255 ::1 :: fd00::1 fe80::1 fec0::1 ff02::1 \
         ::ffff:127.0.0.1 64:ff9b::a00:1; do
    check "$t -> 400" is "$F/ping?ip=$t&port=9&edition=java" 400
done
check "name of an internal address -> no_dns" json "$F/ping?host=localtest.me&family=4&port=9&edition=java" "d['state'] == 'no_dns' and 'ip' not in d"
check "public address passes"       is "$F/ping?ip=192.0.2.1&port=9&edition=java" 200
check "IPv4-mapped address -> 400"  is "$F/ping?ip=::ffff:192.0.2.1&port=9&edition=java" 400

echo "== cache"
CLIENT=198.51.100.1
check "second identical probe is cached" json "$B/ping?ip=127.0.0.1&port=9&edition=java" "d.get('cached') and d['age_s'] >= 0"

echo "== limits"
CLIENT=203.0.113.6
i=0
while [ "$i" -lt 16 ]; do status "$B/ping?ip=127.0.0.1&port=9&edition=java" >/dev/null; i=$((i + 1)); done
check "budget: 17th request at once -> 429" is "$B/ping?ip=127.0.0.1&port=9&edition=java" 429
check "429 names the budget"        json "$B/health" "'slow down' in d['error']"
check "429 says when the batch comes" sh -c "curl -s -D - -o /dev/null -H 'X-Forwarded-For: $CLIENT' '$B/health' | tr -d '\r' | grep -qiE '^Retry-After: [1-7]\$'"
sleep 8
for i in 1 2 3; do status "$B/ping?ip=127.0.0.1&port=9&edition=java" >/dev/null; done
check "budget: 4 tokens after 7 s"  is "$B/ping?ip=127.0.0.1&port=9&edition=java" 200
check "budget: 5th before the next batch -> 429" is "$B/ping?ip=127.0.0.1&port=9&edition=java" 429
CLIENT=203.0.113.7
for i in 1 2 3 4 5 6 7 8; do status "$B/ping?ip=127.0.0.1&port=9&edition=java&host=sys$i.example" >/dev/null; done
check "8 systems allowed, same system again ok" is "$B/ping?ip=127.0.0.1&port=9&edition=java&host=sys1.example" 200
check "9th system -> 429"           is "$B/ping?ip=127.0.0.1&port=9&edition=java&host=sys9.example" 429
check "429 names the systems limit" json "$B/health" "'systems' in d['error']"
check "block lasts until the oldest system leaves" sh -c "curl -s -D - -o /dev/null -H 'X-Forwarded-For: $CLIENT' '$B/health' | tr -d '\r' | grep -qiE '^Retry-After: (5[0-9]|60|61)\$'"
CLIENT=203.0.113.8
for i in 1 2 3; do status "$B/health" >/dev/null; done
check "4 health requests within 7 s ok" is "$B/health" 200
check "5th health request -> 429"   is "$B/health" 429
check "health cooldown covers probes" is "$B/ping?ip=127.0.0.1&port=9&edition=java" 429
check "429 names the health limit"  json "$B/health" "'health' in d['error']"
CLIENT=203.0.113.9
check "other client unaffected"     is "$B/health" 200
CLIENT=2001:db8:1:2::1
for i in 1 2 3; do status "$B/health" >/dev/null; done
CLIENT=2001:db8:1:2::ffff
check "IPv6 /64 shares limits: 4th ok" is "$B/health" 200
check "IPv6 /64 shares limits: 5th -> 429" is "$B/health" 429
CLIENT=2001:db8:1:3::1
check "next /64 unaffected"         is "$B/health" 200
unset CLIENT

echo
if [ "$fails" -eq 0 ]; then echo "all checks passed"; else echo "$fails check(s) failed"; exit 1; fi
