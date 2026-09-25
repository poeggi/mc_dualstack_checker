#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# One-time setup of the backend host: any Linux with systemd.
# Run as root from a checkout of the repo:  sudo sh deploy/bootstrap.sh
# Afterwards mcdc-tick keeps Caddy, the backend and the files of deploy/
# current from the GitHub releases. Idempotent.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)

echo "== tools"
missing=
for c in curl tar cmp sha256sum sha512sum install timeout; do
    command -v "$c" >/dev/null 2>&1 || missing="$missing $c"
done
if [ -n "$missing" ]; then
    echo "missing:$missing"
    pkgs="curl tar diffutils coreutils"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq $pkgs
    elif command -v dnf >/dev/null 2>&1; then
        dnf -y -q install $pkgs
    elif command -v yum >/dev/null 2>&1; then
        yum -y -q install $pkgs
    elif command -v zypper >/dev/null 2>&1; then
        zypper -n -q install $pkgs
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Sy --noconfirm --needed $pkgs
    else
        echo "no known package manager; install:$missing" >&2
        exit 1
    fi
fi

echo "== firewall"
if systemctl is-active -q firewalld 2>/dev/null; then
    firewall-cmd -q --permanent --add-service=https
    firewall-cmd -q --reload
elif command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow 443/tcp >/dev/null
else
    echo "no firewalld or ufw active; make sure TCP 443 is open"
fi

echo "== users"
nologin=$(command -v nologin || echo /bin/false)
id mcdc  >/dev/null 2>&1 || useradd --system --no-create-home --shell "$nologin" mcdc
id caddy >/dev/null 2>&1 || useradd --system --home-dir /var/lib/caddy --create-home --shell "$nologin" caddy

echo "== deploy files"
mkdir -p /etc/caddy /var/www/mcdc /var/lib/mcdc
install -m 644 "$HERE/Caddyfile" /etc/caddy/Caddyfile
install -m 644 "$HERE/www/"* /var/www/mcdc/
install -m 755 "$HERE/mcdc-tick" /usr/local/bin/mcdc-tick
install -m 644 "$HERE/mc-dualstack-check.service" "$HERE/mcdc-tick.service" "$HERE/mcdc-tick.timer" "$HERE/caddy.service" /etc/systemd/system/
chown -R caddy:caddy /var/lib/caddy
systemctl daemon-reload

echo "== first tick: Caddy and the latest release"
BUSY_SECONDS=0 /usr/local/bin/mcdc-tick

systemctl enable --now mc-dualstack-check caddy mcdc-tick.timer
systemctl restart caddy

echo "== done"
systemctl --no-pager --lines=0 status mc-dualstack-check caddy mcdc-tick.timer | grep -E 'Active|Loaded' || true
curl -fsS http://127.0.0.1:8080/health && echo
