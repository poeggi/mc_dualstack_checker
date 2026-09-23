#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# One-time VM setup for the backend on a Linux VM.
# Run as root from a checkout of the repo:  sudo sh deploy/bootstrap.sh
# Idempotent: rerunning updates the unit files and Caddy config.
set -eu

CADDY_VERSION="2.11.4"
HERE=$(cd "$(dirname "$0")" && pwd)

case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

echo "== packages and firewall"
dnf -y -q install firewalld tar curl
systemctl enable --now firewalld
firewall-cmd -q --permanent --add-service=http
firewall-cmd -q --permanent --add-service=https
firewall-cmd -q --reload

echo "== users"
id mcdc  >/dev/null 2>&1 || useradd --system --no-create-home --shell /sbin/nologin mcdc
id caddy >/dev/null 2>&1 || useradd --system --home-dir /var/lib/caddy --create-home --shell /sbin/nologin caddy

echo "== caddy $CADDY_VERSION"
if [ "$(/usr/local/bin/caddy version 2>/dev/null | cut -d' ' -f1)" != "v$CADDY_VERSION" ]; then
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/caddy.tar.gz" \
        "https://github.com/caddyserver/caddy/releases/download/v$CADDY_VERSION/caddy_${CADDY_VERSION}_linux_$ARCH.tar.gz"
    tar -xzf "$tmp/caddy.tar.gz" -C "$tmp" caddy
    install -m 755 "$tmp/caddy" /usr/local/bin/caddy
    rm -rf "$tmp"
fi
mkdir -p /etc/caddy /var/www/mcdc
install -m 644 "$HERE/Caddyfile" /etc/caddy/Caddyfile
install -m 644 "$HERE/www/"* /var/www/mcdc/
chown -R caddy:caddy /var/lib/caddy

echo "== backend units"
install -m 755 "$HERE/mcdc-tick" /usr/local/bin/mcdc-tick
install -m 644 "$HERE/mc-dualstack-check.service" "$HERE/mcdc-tick.service" "$HERE/mcdc-tick.timer" "$HERE/caddy.service" /etc/systemd/system/
mkdir -p /var/lib/mcdc
systemctl daemon-reload

echo "== first release pull"
BUSY_SECONDS=0 /usr/local/bin/mcdc-tick

systemctl enable --now mc-dualstack-check caddy mcdc-tick.timer
systemctl restart caddy

echo "== done"
systemctl --no-pager --lines=0 status mc-dualstack-check caddy mcdc-tick.timer | grep -E 'Active|Loaded' || true
curl -fsS http://127.0.0.1:8080/health && echo
