#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# End-to-end check of the deployed web interface and API. Runs against the
# live URLs by default; override with WEB and API. Probes mc.example.org
# (dual-stack Bedrock) and Hypixel (IPv4 Java), so those must be up.
#
#   sh test/live.sh                      # live
#   EXPECT_VERSION=v1.0.1 sh test/live.sh
#   WEB=http://localhost:8000 API=http://localhost:8080 sh test/live.sh
set -u

WEB=${WEB:-https://www.poggensee.it/mc_dualstack_check}
API=${API:-https://mcdscheck-api.poggensee.it}
EXPECT_VERSION=${EXPECT_VERSION:-}

HOST=mc.example.org
V4=192.0.2.10
V6=2001:db8::10
JAVA_HOST=mc.hypixel.net

PY=python3; "$PY" -c pass >/dev/null 2>&1 || PY=python
fails=0
ok()   { echo "ok    $1"; }
fail() { echo "FAIL  $1"; fails=$((fails + 1)); }
check() { # check <label> <command...>: passes when the command exits 0
    label=$1; shift
    if "$@" >/dev/null 2>&1; then ok "$label"; else fail "$label"; fi
}
status() { curl -s -o /dev/null -w '%{http_code}' "$1"; }
is() { [ "$(status "$1")" = "$2" ]; }
# json <url> <python expression over d>: exits 0 when the expression is true
json() { curl -s "$1" | "$PY" -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if ($2) else 1)"; }

echo "== web interface $WEB"
check "page"             is "$WEB/" 200
check "script"           is "$WEB/mc_dualstack_check.js" 200
check "stylesheet"       is "$WEB/mc_dualstack_check.css" 200
check "icon"             is "$WEB/favicon.svg" 200
check "config: own provider"        json "$WEB/api.php?op=config" "d['provider'] == 'own'"
check "health: ok, ipv6"            json "$WEB/api.php?op=health" "d['ok'] and d['ipv6']"
check "resolve: A and AAAA"         json "$WEB/api.php?op=resolve&host=$HOST" "'$V4' in d['a'] and '$V6' in d['aaaa']"
check "resolve: v4-only host"       json "$WEB/api.php?op=resolve&host=$JAVA_HOST" "d['a'] and not d['aaaa'] and 'errors' not in d"
check "ping: bedrock v4 literal"    json "$WEB/api.php?op=ping&ip=$V4&port=19132&edition=bedrock&host=$HOST" "d['state'] == 'online'"
check "ping: bedrock v6 literal"    json "$WEB/api.php?op=ping&ip=$V6&port=19132&edition=bedrock&host=$HOST" "d['state'] == 'online'"
java_ip=$(curl -s "$WEB/api.php?op=resolve&host=$JAVA_HOST" | "$PY" -c 'import sys,json; print(json.load(sys.stdin)["a"][0])')
check "ping: java hostname"         json "$WEB/api.php?op=ping&ip=$java_ip&port=25565&edition=java&host=$JAVA_HOST" "d['state'] == 'online' and d['info']['players_max'] > 0"
check "ping: second call cached"    json "$WEB/api.php?op=ping&ip=$V4&port=19132&edition=bedrock&host=$HOST" "d.get('cached') and d['age_s'] >= 0"
check "ping: invalid ip -> 400"     is "$WEB/api.php?op=ping&ip=nope&port=1" 400
check "ping: invalid port -> 400"   is "$WEB/api.php?op=ping&ip=$V4&port=70000" 400
check "unknown op -> 400"           is "$WEB/api.php?op=nope" 400

echo "== api $API"
check "landing page"     is "$API/" 200
check "icon"             is "$API/favicon.svg" 200
check "healthz: ok, ipv6"           json "$API/healthz" "d['ok'] and d['ipv6']"
check "resolve: A and AAAA"         json "$API/resolve?host=$HOST" "'$V4' in d['a'] and '$V6' in d['aaaa']"
check "ping: v4 literal"            json "$API/ping?ip=$V4&port=19132" "d['state'] == 'online'"
check "ping: v6 literal bare"       json "$API/ping?ip=$V6&port=19132" "d['state'] == 'online'"
check "ping: v6 literal bracketed"  json "$API/ping?ip=%5B$V6%5D&port=19132" "d['state'] == 'online'"
check "ping: offline target"        json "$API/ping?ip=192.0.2.1&port=1&edition=java" "d['state'] == 'offline'"
check "cors header for any origin"  sh -c "curl -s -D - -o /dev/null -H 'Origin: https://example.org' '$API/ping?ip=$V4&port=19132' | grep -qi 'access-control-allow-origin: https://example.org'"
check "landing page shows version"  sh -c "curl -s '$API/' | grep -q 'id=\"api-version\">v'"

echo "== consistency"
webv=$(curl -s "$WEB/api.php?op=config" | "$PY" -c 'import sys,json; print(json.load(sys.stdin)["version"])')
apiv=$(curl -s "$API/healthz" | "$PY" -c 'import sys,json; print(json.load(sys.stdin)["version"])')
pagev=$(curl -s "$API/" | sed -n 's/.*id="api-version">\([^<]*\)<.*/\1/p')
echo "      web $webv, api $apiv, landing page $pagev"
check "web and api versions match"  [ "$webv" = "$apiv" ]
check "landing page version matches" [ "$pagev" = "$apiv" ]
if [ -n "$EXPECT_VERSION" ]; then
    check "deployed version is $EXPECT_VERSION" [ "$apiv" = "$EXPECT_VERSION" ]
fi

echo
if [ "$fails" -eq 0 ]; then echo "all checks passed"; else echo "$fails check(s) failed"; exit 1; fi
