#!/usr/bin/env bash
# Builds the browser (WebAssembly) version of Jetris into dist/web/:
#   jetris.wasm     the game, compiled with GOOS=js GOARCH=wasm
#   wasm_exec.js    Go's JS runtime shim, copied from the local Go toolchain
#   index.html      the landing page + loader (from web/), with the version and
#                   the module's size stamped in
#   join.html       the join page (from web/): a link or QR code carrying a
#                   server (?server=…&name=…) opens it, it asks for a player
#                   name and hands all three to index.html — see
#                   scripts/gen-qr.go, which builds those links; a share
#                   link names a replay (&replay=) or an open game (&game=)
#                   as well (internal/webdist/join.go)
#   screenshot.png  the landing page's screenshot (Jetris-screenshot-1.png)
# The release workflow (.github/workflows/release.yml) runs this on every tag,
# attaches the directory to the release as jetris-<tag>-web.tar.gz and
# publishes it to GitHub Pages: https://jnmoyne.github.io/jetris/ — and runs
# it before building each desktop binary, which embeds a copy of the
# directory (internal/webdist) to serve in LAN party mode.
#
# To try it locally, serve the directory over HTTP (wasm cannot be loaded from
# file://), e.g.
#   python3 -m http.server -d dist/web 8080
# and open http://localhost:8080/. The NATS server you connect to must have a
# websocket {} listener; pass ?server=ws://host:port to preselect one.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT=dist/web
mkdir -p "$OUT"

GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/jetris.wasm" ./cmd/jetris
# install(1) rather than cp: the toolchain copy of wasm_exec.js is read-only,
# and a plain cp of it leaves a read-only file that the next build cannot overwrite.
install -m 0644 "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$OUT/"
install -m 0644 Jetris-screenshot-1.png "$OUT/screenshot.png"
install -m 0644 web/touchtest.html "$OUT/" # the plain touch test page (see it for what it isolates)
install -m 0644 web/favicon.ico web/apple-touch-icon.png "$OUT/" # the tab and home-screen icons (scripts/gen-icons.go)

# The page shows the version, uses it to cache-bust wasm_exec.js and
# jetris.wasm (they must come from the same build), and sizes its download
# progress bar from the module's uncompressed byte count — the server may gzip
# it on the wire, so the page cannot trust Content-Length for that.
size=$(wc -c < "$OUT/jetris.wasm" | tr -d ' ')
sed -e "s|__JETRIS_VERSION__|$VERSION|g" -e "s|__JETRIS_WASM_SIZE__|$size|g" web/index.html > "$OUT/index.html"
chmod 0644 "$OUT/index.html"
# The join page loads no wasm of its own — it only needs the version, for the
# icons' cache-bust and the line at its foot.
sed -e "s|__JETRIS_VERSION__|$VERSION|g" web/join.html > "$OUT/join.html"
chmod 0644 "$OUT/join.html"

# The desktop binary carries this same build (internal/webdist embeds
# internal/webdist/dist): the LAN party mode serves it to the phones on the
# network. Build it here, then build the binary, and it is in there.
EMBED=internal/webdist/dist
mkdir -p "$EMBED"
find "$EMBED" -mindepth 1 -not -name .gitkeep -delete
cp "$OUT"/* "$EMBED/"

ls -lh "$OUT"
