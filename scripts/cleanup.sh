#!/usr/bin/env bash
# Deletes all NATS streams and KV buckets created by Jetricks.
# Uses the nats CLI with the currently selected context (or pass --context <name>).
#
# Coverage notes:
#   - Invitations (invites.<player>.<gameID>) live in the JETRICKS_LOBBY KV
#     bucket deleted below — no separate resource.
#   - Lobby events (jetricks.lobby.event.>) are transient core NATS messages
#     captured by no stream — nothing to clean up.

set -euo pipefail

NATS_ARGS=""
if [[ "${1:-}" == "--context" && -n "${2:-}" ]]; then
  NATS_ARGS="--context $2"
fi

echo "Deleting Jetricks game streams..."
nats stream ls $NATS_ARGS -j 2>/dev/null \
  | jq -r '.[] | select(startswith("JETRICKS_GAME_"))' \
  | while read -r stream; do
      echo "  Deleting stream: $stream"
      nats stream rm "$stream" $NATS_ARGS -f
    done

echo "Deleting lobby chat stream..."
nats stream rm JETRICKS_LOBBY_CHAT $NATS_ARGS -f 2>/dev/null && echo "  Deleted JETRICKS_LOBBY_CHAT" || echo "  JETRICKS_LOBBY_CHAT not found (ok)"

echo "Deleting lobby KV bucket..."
nats kv rm JETRICKS_LOBBY $NATS_ARGS -f 2>/dev/null && echo "  Deleted JETRICKS_LOBBY" || echo "  JETRICKS_LOBBY not found (ok)"

echo "Deleting archive stream..."
nats stream rm JETRICKS_ARCHIVE $NATS_ARGS -f 2>/dev/null && echo "  Deleted JETRICKS_ARCHIVE" || echo "  JETRICKS_ARCHIVE not found (ok)"

echo "Done."
