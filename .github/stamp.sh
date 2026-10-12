#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Stamps the API host's pages (backend/www) with a release: the version
# field and the ?v= stamps of their includes. An unstamped include or a
# missed stamp stops the release.
#   sh .github/stamp.sh <tag>
set -eu

TAG=$1
case "$TAG" in v[0-9]*) ;; *) echo "tag must start with v and a digit" >&2; exit 1 ;; esac
case "$TAG" in *[!A-Za-z0-9._+-]*) echo "unexpected character in tag" >&2; exit 1 ;; esac
cd "$(dirname "$0")/../backend/www"

if grep -n -E '<(link|script|img)[^>]*(href|src)="[^"?:]+"' ./*.html; then
    echo "local include without a version stamp" >&2; exit 1
fi
for f in ./*.html; do
    sed -e "s|id=\"api-version\">[^<]*<|id=\"api-version\">$TAG<|" \
        -e "s|?v=[^\"]*\"|?v=$TAG\"|g" "$f" > "$f.new"
    mv "$f.new" "$f"
done
n=$(awk '{ while (match($0, /[?]v=[^"]*"/)) { n++; $0 = substr($0, RSTART + RLENGTH) } } END { print n + 0 }' ./*.html)
m=$(awk -v s="?v=$TAG\"" '{ while ((i = index($0, s)) > 0) { m++; $0 = substr($0, i + length(s)) } } END { print m + 0 }' ./*.html)
[ "$n" -gt 0 ] && [ "$n" -eq "$m" ] || { echo "$m of $n stamps carry $TAG" >&2; exit 1; }
grep -q -F "id=\"api-version\">$TAG<" index.html
grep -q -F "id=\"api-version\">$TAG<" stats.html
