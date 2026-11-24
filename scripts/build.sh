#!/usr/bin/env bash
# Builds jetris and every Go agent, and puts the binaries in the repository's
# top directory:
#   jetris        the game (cmd/jetris), with the browser version embedded
#   golang-mk1    one binary per Go agent (agents/golang-*/, each its own
#   golang-mk2    module), named after its directory
#
# The browser version (scripts/build-wasm.sh) is built first: the jetris
# binary embeds whatever internal/webdist/dist/ holds when it is built, and
# serves it to phones in LAN party mode.
#
# jetris is a "dev" build (no update check) unless VERSION is set, e.g.
#   VERSION=v0.6.0 ./scripts/build.sh
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$PWD"

echo "==> browser version (scripts/build-wasm.sh)"
./scripts/build-wasm.sh

echo "==> jetris"
ldflags="-s -w"
if [ -n "${VERSION:-}" ]; then
	ldflags="$ldflags -X main.version=$VERSION"
fi
go build -trimpath -ldflags "$ldflags" -o "$ROOT/jetris" ./cmd/jetris

for dir in agents/golang-*/; do
	[ -f "$dir/go.mod" ] || continue
	name="$(basename "$dir")"
	echo "==> $name"
	(cd "$dir" && go build -trimpath -o "$ROOT/$name" .)
done

echo "built:"
ls -l "$ROOT/jetris" "$ROOT"/golang-*
