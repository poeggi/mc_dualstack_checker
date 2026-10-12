#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Publishes the backend image, pages included (stamp them first):
#   1. Builds it for linux/amd64 and linux/arm64 and runs it once: /health
#      and the landing page must carry the version, -health must pass.
#   2. Pushes it signed (sigstore, key in SIGNING_KEY) as <repo>:<x.y.z>.
#   3. Pulls it back per architecture through a policy that takes only
#      images signed with the key in cosign.pub, as the hosts do; a policy
#      with another key must refuse it.
#   4. Points <repo>:<x> at the same digest and pulls that tag back too.
# A failed step stops before the major tag moves, so hosts keep the image
# they run.
#   sh .github/image.sh <repo> <tag, e.g. v1.2.3>
# Env: SIGNING_KEY and SIGNING_PASSPHRASE (files), PUBLIC_KEY (default
# cosign.pub), TLS_VERIFY (default true; false for a local test registry).
set -eu

repo=$1
tag=$2
case "$tag" in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "tag must look like v1.2.3" >&2; exit 1 ;; esac
full=${tag#v}
major=${full%%.*}
tls=${TLS_VERIFY:-true}
cd "$(dirname "$0")/.."
pub=$(cd "$(dirname "${PUBLIC_KEY:-cosign.pub}")" && pwd)/$(basename "${PUBLIC_KEY:-cosign.pub}")
img="localhost/mc_dualstack_check:$full"

tmp=$(mktemp -d)
cid=
trap '[ -z "$cid" ] || podman rm -f "$cid" >/dev/null 2>&1; rm -rf "$tmp"' EXIT

# Sigstore signatures are stored next to the image in the registry.
conf="$HOME/.config/containers/registries.d"
mkdir -p "$conf"
printf 'docker:\n  %s:\n    use-sigstore-attachments: true\n' "$repo" > "$conf/mc_dualstack_check.yaml"

echo "== build"
podman build --platform linux/amd64,linux/arm64 --build-arg "VERSION=$tag" --manifest "$img" backend

echo "== run"
port=18089
cid=$(podman run -d --network host -e PORT=$port "$img")
i=0
until curl -fsS "http://127.0.0.1:$port/health" > "$tmp/health" 2>/dev/null; do
    i=$((i + 1))
    [ "$i" -lt 30 ] || { podman logs "$cid"; echo "no /health answer" >&2; exit 1; }
    sleep 1
done
grep -q -F "\"version\":\"$tag\"" "$tmp/health" || { cat "$tmp/health"; echo "wrong version" >&2; exit 1; }
curl -fsS "http://127.0.0.1:$port/" | grep -q -F "id=\"api-version\">$tag<"
curl -fsS -o /dev/null --retry 5 --retry-all-errors --retry-delay 1 "http://127.0.0.1:$port/stats/minutes.json"
podman exec "$cid" /mc_dualstack_check -health
podman rm -f "$cid" >/dev/null
cid=

echo "== push $repo:$full"
podman manifest push --all --tls-verify="$tls" \
    --sign-by-sigstore-private-key "$SIGNING_KEY" --sign-passphrase-file "$SIGNING_PASSPHRASE" \
    "$img" "docker://$repo:$full"

# policy <key>: accepts only images of $repo signed with key, under any of
# its tags; refuses everything else.
policy() {
    printf '{"default":[{"type":"reject"}],"transports":{"docker":{"%s":[{"type":"sigstoreSigned","keyPath":"%s","signedIdentity":{"type":"matchRepository"}}]}}}\n' \
        "$repo" "$1" > "$tmp/policy.json"
}
# pull <ref> <arch>: copies the image through the policy.
pull() {
    rm -rf "$tmp/pulled"
    skopeo --policy "$tmp/policy.json" --override-os linux --override-arch "$2" \
        copy --quiet --src-tls-verify="$tls" "docker://$1" "dir:$tmp/pulled"
}
openssl ecparam -name prime256v1 -genkey -noout | openssl ec -pubout -out "$tmp/other.pub" 2>/dev/null

echo "== verify $repo:$full"
for arch in amd64 arm64; do
    policy "$pub"
    pull "$repo:$full" $arch
    policy "$tmp/other.pub"
    if pull "$repo:$full" $arch 2>/dev/null; then
        echo "$arch: accepted under another key" >&2; exit 1
    fi
done

echo "== move $repo:$major"
skopeo copy --all --preserve-digests --remove-signatures --quiet \
    --src-tls-verify="$tls" --dest-tls-verify="$tls" "docker://$repo:$full" "docker://$repo:$major"
policy "$pub"
for arch in amd64 arm64; do
    pull "$repo:$major" $arch
done
a=$(skopeo inspect --raw --tls-verify="$tls" "docker://$repo:$full" | sha256sum)
b=$(skopeo inspect --raw --tls-verify="$tls" "docker://$repo:$major" | sha256sum)
[ "$a" = "$b" ] || { echo "$major and $full differ" >&2; exit 1; }
echo "published $repo:$full and $repo:$major, signed and verified"
