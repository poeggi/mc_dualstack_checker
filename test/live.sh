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
check "config: backend configured"  json "$WEB/api/config" "d['backend'] and d['version']"
check "health: ok, ipv6"            json "$WEB/api/health" "d['ok'] and d['ipv6']"
check "ping: name, IPv4"            json "$WEB/api/ping?host=$HOST&family=4&port=19132&edition=bedrock" "d['state'] == 'online' and d['ip'] == '$V4'"
check "ping: name, IPv6"            json "$WEB/api/ping?host=$HOST&family=6&port=19132&edition=bedrock" "d['state'] == 'online' and d['ip'] == '$V6'"
check "ping: bedrock v4 literal"    json "$WEB/api/ping?ip=$V4&port=19132&edition=bedrock&host=$HOST" "d['state'] == 'online'"
check "ping: bedrock v6 literal"    json "$WEB/api/ping?ip=$V6&port=19132&edition=bedrock&host=$HOST" "d['state'] == 'online'"
check "ping: java hostname"         json "$WEB/api/ping?host=$JAVA_HOST&family=4&port=25565&edition=java" "d['state'] == 'online' and d['info']['players_max'] > 0"
check "ping: v4-only host, IPv6"    json "$WEB/api/ping?host=$JAVA_HOST&family=6&port=25565&edition=java" "d['state'] == 'no_dns'"
check "ping: second call cached"    json "$WEB/api/ping?ip=$V4&port=19132&edition=bedrock&host=$HOST" "d.get('cached') and d['age_s'] >= 0"
check "ping: invalid ip -> 400"     is "$WEB/api/ping?ip=nope&port=1" 400
check "ping: invalid port -> 400"   is "$WEB/api/ping?ip=$V4&port=70000" 400
check "unknown endpoint -> 404"     is "$WEB/api/nope" 404
check "no resolve endpoint -> 404"  is "$WEB/api/resolve?host=$HOST" 404
check "scripts hidden by name"      is "$WEB/api.php" 404
check "config hidden by name"       is "$WEB/config.php" 404

echo "== api $API"
check "landing page"     is "$API/" 200
check "icon"             is "$API/favicon.svg" 200
check "health: ok, ipv6"            json "$API/health" "d['ok'] and d['ipv6']"
check "ping: name, IPv4"            json "$API/ping?host=$HOST&family=4&port=19132" "d['state'] == 'online' and d['ip'] == '$V4'"
check "ping: name, IPv6"            json "$API/ping?host=$HOST&family=6&port=19132" "d['state'] == 'online' and d['ip'] == '$V6'"
check "ping: v4 literal"            json "$API/ping?ip=$V4&port=19132" "d['state'] == 'online'"
check "ping: v6 literal bare"       json "$API/ping?ip=$V6&port=19132" "d['state'] == 'online'"
check "ping: v6 literal bracketed"  json "$API/ping?ip=%5B$V6%5D&port=19132" "d['state'] == 'online'"
check "ping: offline target"        json "$API/ping?ip=192.0.2.1&port=1&edition=java" "d['state'] == 'offline'"
check "cors header for any origin"  sh -c "curl -s -D - -o /dev/null -H 'Origin: https://example.org' '$API/ping?ip=$V4&port=19132' | grep -qi 'access-control-allow-origin: https://example.org'"
check "internal target -> 400"     is "$API/ping?ip=127.0.0.1&port=22&edition=java" 400
check "internal name -> no_dns"     json "$API/ping?host=localtest.me&family=4&port=22&edition=java" "d['state'] == 'no_dns'"
check "local name, no lookup"       json "$API/ping?host=localhost&family=4&port=22&edition=java" "d['state'] == 'no_dns'"
check "no resolve endpoint -> 404"  is "$API/resolve?host=$HOST" 404
check "unknown endpoint -> 404"     is "$API/nope" 404
check "no server banner"            sh -c "! curl -s -D - -o /dev/null '$API/ping?ip=nope&port=1' | grep -qiE '^(server|via):'"
check "landing page shows version"  sh -c "curl -s '$API/' | grep -q 'id=\"api-version\">v'"

echo "== consistency"
webv=$(curl -s "$WEB/api/config" | "$PY" -c 'import sys,json; print(json.load(sys.stdin)["version"])')
apiv=$(curl -s "$API/health" | "$PY" -c 'import sys,json; print(json.load(sys.stdin)["version"])')
pagev=$(curl -s "$API/" | sed -n 's/.*id="api-version">\([^<]*\)<.*/\1/p')
echo "      web $webv, api $apiv, landing page $pagev"
check "web and api versions match"  [ "$webv" = "$apiv" ]
check "landing page version matches" [ "$pagev" = "$apiv" ]
if [ -n "$EXPECT_VERSION" ]; then
    check "deployed version is $EXPECT_VERSION" [ "$apiv" = "$EXPECT_VERSION" ]
fi

echo
if [ "$fails" -eq 0 ]; then echo "all checks passed"; else echo "$fails check(s) failed"; exit 1; fi
