#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# End-to-end check of a deployed web interface and API. Each part runs only
# when its setting is given:
#   WEB                page URL
#   API                API URL
#   LIVE_BEDROCK_HOST  a dual-stack Bedrock server that is always up
#   LIVE_JAVA_HOST     a Java server that is always up
#   EXPECT_VERSION     the release the API must report
# The Bedrock server's addresses come from the API's answers and are masked
# in CI logs.
#
#   WEB=https://example.org/mcdc API=https://api.example.org sh test/live.sh
#   WEB=http://localhost:8000 API=http://localhost:8080 sh test/live.sh
set -u

WEB=${WEB:-}
WEB=${WEB%/}
API=${API:-}
API=${API%/}
HOST=${LIVE_BEDROCK_HOST:-}
JAVA_HOST=${LIVE_JAVA_HOST:-}
EXPECT_VERSION=${EXPECT_VERSION:-}

PY=python3; "$PY" -c pass >/dev/null 2>&1 || PY=python
fails=0
ok()   { echo "ok    $1"; }
fail() { echo "FAIL  $1"; fails=$((fails + 1)); }
skip() { echo "skip  $1"; }
check() { # check <label> <command...>: passes when the command exits 0
    label=$1; shift
    if "$@" >/dev/null 2>&1; then ok "$label"; else fail "$label"; fi
}
status() { curl -s -o /dev/null -w '%{http_code}' "$1"; }
is() { [ "$(status "$1")" = "$2" ]; }
# header <headers> <pattern>: exits 0 when a header line matches pattern
header() { printf '%s\n' "$1" | tr -d '\r' | grep -qi "$2"; }
# json <url> <python expression over d>: exits 0 when the expression is true
json() { curl -s "$1" | "$PY" -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if ($2) else 1)"; }
# has <json> <python expression over d>: exits 0 when the expression is true
has() { printf '%s' "$1" | "$PY" -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if ($2) else 1)"; }
# host_health: the body of the web host's health copy. A copy that is not
# current (no Age, or older than 7 s) is asked for again after the web
# host has refreshed it.
host_health() {
    h=$(curl -s -D - "$WEB/api/health" | tr -d '\r')
    age=$(printf '%s\n' "$h" | sed -n 's/^[Aa]ge: *//p')
    if [ -z "$age" ] || [ "$age" -gt 7 ]; then
        sleep 7
        h=$(curl -s -D - "$WEB/api/health" | tr -d '\r')
    fi
    printf '%s\n' "$h" | sed '1,/^$/d'
}
# address <family>: the address the API probes for HOST in that family
address() {
    curl -s "$API/ping?host=$HOST&family=$1&port=19132" |
        "$PY" -c "import sys,json; d=json.load(sys.stdin); print(d['ip'] if d['state'] == 'online' else '')" 2>/dev/null
}

if [ -n "$WEB" ]; then
    echo "== web interface $WEB"
    check "page"             is "$WEB/" 200
    check "script"           is "$WEB/mc_dualstack_check.js" 200
    check "stylesheet"       is "$WEB/mc_dualstack_check.css" 200
    check "icon"             is "$WEB/favicon.svg" 200
    if [ -n "$API" ]; then
        check "config names the API"    json "$WEB/api/config" "d['backend'] == '$API' and d['version']"
    else
        check "config names an API"     json "$WEB/api/config" "d['backend'] and d['version']"
    fi
    check "health copy: ok, ipv6"   has "$(host_health)" "d['ok'] and d['ipv6']"
    check "no probe relay -> 404"   is "$WEB/api/ping?ip=192.0.2.1&port=1" 404
    check "unknown endpoint -> 404" is "$WEB/api/nope" 404
    check "scripts hidden by name"  is "$WEB/api.php" 404
    check "config hidden by name"   is "$WEB/config.php" 404
else
    skip "web interface: WEB is not set"
fi

if [ -n "$API" ]; then
    echo "== api $API"
    check "landing page"     is "$API/" 200
    check "icon"             is "$API/favicon.svg" 200
    check "stylesheet"       is "$API/style.css" 200
    check "usage page"       is "$API/stats" 200
    check "usage numbers"    json "$API/stats.json" "all(d[k] and d[k][0].get('current') for k in ('minutes', 'hours', 'days', 'months'))"
    check "landing page CSP" header "$(curl -s -D - -o /dev/null "$API/")" "^content-security-policy: default-src 'none'"
    check "health: ok, ipv6"            json "$API/health" "d['ok'] and d['ipv6']"
    if [ -n "$HOST" ]; then
        V4=$(address 4)
        V6=$(address 6)
        if [ -n "${GITHUB_ACTIONS:-}" ]; then
            for a in $V4 $V6; do echo "::add-mask::$a"; done
        fi
        check "bedrock: name, IPv4 online"  [ -n "$V4" ]
        check "bedrock: name, IPv6 online"  [ -n "$V6" ]
        check "bedrock: v4 literal"         json "$API/ping?ip=$V4&port=19132&host=$HOST" "d['state'] == 'online'"
        check "bedrock: v6 literal bare"    json "$API/ping?ip=$V6&port=19132&host=$HOST" "d['state'] == 'online'"
        check "bedrock: v6 literal bracketed" json "$API/ping?ip=%5B$V6%5D&port=19132&host=$HOST" "d['state'] == 'online'"
        check "bedrock: second call cached" json "$API/ping?ip=$V4&port=19132&host=$HOST" "d.get('cached') and d['age_s'] >= 0"
    else
        skip "bedrock: LIVE_BEDROCK_HOST is not set"
    fi
    if [ -n "$JAVA_HOST" ]; then
        check "java: name, IPv4 online"     json "$API/ping?host=$JAVA_HOST&family=4&port=25565&edition=java" "d['state'] == 'online'"
        check "java: name, IPv6 online or no record" json "$API/ping?host=$JAVA_HOST&family=6&port=25565&edition=java" "d['state'] in ('online', 'no_dns')"
    else
        skip "java: LIVE_JAVA_HOST is not set"
    fi
    check "ping: offline target"        json "$API/ping?ip=192.0.2.1&port=1&edition=java" "d['state'] == 'offline'"
    check "ping: invalid ip -> 400"     is "$API/ping?ip=nope&port=1" 400
    check "ping: invalid port -> 400"   is "$API/ping?ip=192.0.2.1&port=70000" 400
    if [ -n "$WEB" ]; then
        origin=$(echo "$WEB" | sed 's|^\([a-z]*://[^/]*\).*|\1|')
        headers=$(curl -s -D - -o /dev/null -H "Origin: $origin" "$API/ping?ip=nope&port=1")
        check "cors: page origin allowed"   header "$headers" "^access-control-allow-origin: $origin\$"
        check "cors: Retry-After readable"  header "$headers" "^access-control-expose-headers: retry-after\$"
    fi
    check "internal target -> 400"      is "$API/ping?ip=127.0.0.1&port=22&edition=java" 400
    check "internal name -> no_dns"     json "$API/ping?host=localtest.me&family=4&port=22&edition=java" "d['state'] == 'no_dns'"
    check "local name, no lookup"       json "$API/ping?host=localhost&family=4&port=22&edition=java" "d['state'] == 'no_dns'"
    check "no resolve endpoint -> 404"  is "$API/resolve?host=example.org" 404
    check "unknown endpoint -> 404"     is "$API/nope" 404
    check "no server banner"            sh -c "! curl -s -D - -o /dev/null '$API/ping?ip=nope&port=1' | grep -qiE '^(server|via):'"
    check "landing page shows version"  sh -c "curl -s '$API/' | grep -q 'id=\"api-version\">v'"
    apiv=$(curl -s "$API/health" | "$PY" -c 'import sys,json; print(json.load(sys.stdin)["version"])')
    if [ -n "$EXPECT_VERSION" ]; then
        check "deployed version is $EXPECT_VERSION" [ "$apiv" = "$EXPECT_VERSION" ]
    fi
else
    skip "api: API is not set"
fi

if [ -n "$WEB" ] && [ -n "$API" ]; then
    echo "== consistency"
    webv=$(curl -s "$WEB/api/config" | "$PY" -c 'import sys,json; print(json.load(sys.stdin)["version"])')
    pagev=$(curl -s "$API/" | sed -n 's/.*id="api-version">\([^<]*\)<.*/\1/p')
    echo "      web $webv, api $apiv, landing page $pagev"
    check "web and api versions match"  [ "$webv" = "$apiv" ]
    check "landing page version matches" [ "$pagev" = "$apiv" ]
fi

echo
if [ "$fails" -eq 0 ]; then echo "all checks passed"; else echo "$fails check(s) failed"; exit 1; fi
