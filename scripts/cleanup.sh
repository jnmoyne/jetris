#!/usr/bin/env bash
# Deletes all NATS streams and KV buckets created by Jetris.
# Uses the nats CLI with the currently selected context (or pass --context <name>).
#
# Coverage notes:
#   - Invitations (invites.<player>.<gameID>) live in the JETRIS_LOBBY KV
#     bucket deleted below — no separate resource.
#   - Lobby events (jetris.lobby.event.>) are transient core NATS messages
#     captured by no stream — nothing to clean up.

set -euo pipefail

NATS_ARGS=""
if [[ "${1:-}" == "--context" && -n "${2:-}" ]]; then
  NATS_ARGS="--context $2"
fi

echo "Deleting Jetris game streams..."
nats stream ls $NATS_ARGS -j 2>/dev/null \
  | jq -r '.[] | select(startswith("JETRIS_GAME_"))' \
  | while read -r stream; do
      echo "  Deleting stream: $stream"
      nats stream rm "$stream" $NATS_ARGS -f
    done

echo "Deleting chat stream..."
nats stream rm JETRIS_CHAT $NATS_ARGS -f 2>/dev/null && echo "  Deleted JETRIS_CHAT" || echo "  JETRIS_CHAT not found (ok)"
# Legacy name from before the rename to JETRIS_CHAT.
nats stream rm JETRIS_LOBBY_CHAT $NATS_ARGS -f 2>/dev/null && echo "  Deleted JETRIS_LOBBY_CHAT (legacy)" || true

echo "Deleting lobby KV bucket..."
nats kv rm JETRIS_LOBBY $NATS_ARGS -f 2>/dev/null && echo "  Deleted JETRIS_LOBBY" || echo "  JETRIS_LOBBY not found (ok)"

echo "Deleting archive stream..."
nats stream rm JETRIS_ARCHIVE $NATS_ARGS -f 2>/dev/null && echo "  Deleted JETRIS_ARCHIVE" || echo "  JETRIS_ARCHIVE not found (ok)"

echo "Done."
