#!/usr/bin/env bash
# Builds the browser (WebAssembly) version of Jetris into dist/web/:
#   jetris.wasm   the game, compiled with GOOS=js GOARCH=wasm
#   wasm_exec.js  Go's JS runtime shim, copied from the local Go toolchain
#   index.html    the page that loads the two (from web/)
# Serve that directory over HTTP (wasm cannot be loaded from file://), e.g.
#   python3 -m http.server -d dist/web 8080
# and open http://localhost:8080/. The NATS server you connect to must have a
# websocket {} listener; pass ?server=wss://host:port to preselect one.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT=dist/web
mkdir -p "$OUT"

GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/jetris.wasm" ./cmd/jetris
# install(1) rather than cp: the toolchain copy of wasm_exec.js is read-only,
# and a plain cp of it leaves a read-only file that the next build cannot overwrite.
install -m 0644 "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$OUT/"
install -m 0644 web/index.html "$OUT/"

ls -lh "$OUT"
