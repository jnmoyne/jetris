#!/usr/bin/env bash
# Deletes everything one Jetris game left on the server: its stream, its
# lobby listing (and replay pin), its archive record, its replay copy and
# its chat. For a game that got stranded — listed and joinable long after it
# ended, or archived by a client that went away mid-way — or simply one you
# want gone from the history.
#
#   scripts/delete-game.sh [--context <name>] <game-id>
#
# Uses the nats CLI with the currently selected context unless --context is
# given. Every step is best-effort: what is not there is reported and skipped.
#
# Coverage notes (config.go):
#   - Stream JETRIS_GAME_<id> holds the meta, the board, the events and the
#     roster entries — one deletion covers them all.
#   - Lobby KV JETRIS_LOBBY: games.<id> is the listing, pins.<id> the replay
#     pin. Invitations (invites.<player>.<id>) are TTL'd and expire on their
#     own.
#   - JETRIS_ARCHIVE: one jetris.archive message per game, found by its
#     game_id — the stream has no per-game subject, so it is scanned.
#   - JETRIS_REPLAY: jetris.replay.<id>.> (the copy and its .done marker).
#   - JETRIS_CHAT: jetris.chat.<id>.

set -euo pipefail

usage() {
  echo "usage: $0 [--context <name>] <game-id>" >&2
  exit 2
}

NATS_ARGS=()
if [[ "${1:-}" == "--context" ]]; then
  [[ -n "${2:-}" ]] || usage
  NATS_ARGS=(--context "$2")
  shift 2
fi
[[ $# -eq 1 && -n "$1" ]] || usage
ID="$1"
if [[ ! "$ID" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "game id $ID: letters, digits, dots, dashes and underscores only" >&2
  exit 2
fi

# (The empty-array expansion idiom keeps bash 3.2, macOS's, happy under set -u.)
n() { nats ${NATS_ARGS[@]+"${NATS_ARGS[@]}"} "$@"; }

echo "Deleting game $ID"

echo "Game stream JETRIS_GAME_$ID..."
if n stream rm "JETRIS_GAME_$ID" -f 2>/dev/null; then
  echo "  deleted"
else
  echo "  not found (ok)"
fi

echo "Lobby listing games.$ID..."
if n kv get JETRIS_LOBBY "games.$ID" >/dev/null 2>&1; then
  n kv del JETRIS_LOBBY "games.$ID" -f >/dev/null
  echo "  deleted"
else
  echo "  not found (ok)"
fi

echo "Replay pin pins.$ID..."
if n kv get JETRIS_LOBBY "pins.$ID" >/dev/null 2>&1; then
  n kv del JETRIS_LOBBY "pins.$ID" -f >/dev/null
  echo "  deleted"
else
  echo "  not found (ok)"
fi

echo "Archive record..."
# The archive stream keeps one record per game on a single subject, so the
# record is found by scanning: a subscription replays exactly the stream's
# current message count (nats sub never returns on its own otherwise), and
# every message's header line carries its sequence, its payload the game_id.
count=$(n stream info JETRIS_ARCHIVE --json 2>/dev/null | jq -r '.state.messages // 0' || echo 0)
seqs=""
if [[ "$count" -gt 0 ]]; then
  seqs=$(n sub jetris.archive --stream JETRIS_ARCHIVE --all --count "$count" 2>/dev/null \
    | awk -v id="\"game_id\":\"$ID\"" '
        /^\[#/ { if (match($0, /seq: [0-9]+/)) seq = substr($0, RSTART + 5, RLENGTH - 5) }
        index($0, id) && seq != "" { print seq; seq = "" }' || true)
fi
if [[ -z "$seqs" ]]; then
  echo "  not found (ok)"
else
  for seq in $seqs; do
    if n stream rmm JETRIS_ARCHIVE "$seq" -f >/dev/null 2>&1; then
      echo "  deleted message $seq"
    else
      echo "  message $seq: could not delete" >&2
    fi
  done
fi

echo "Replay copy jetris.replay.$ID.>..."
if n stream purge JETRIS_REPLAY --subject "jetris.replay.$ID.>" -f >/dev/null 2>&1; then
  echo "  purged"
else
  echo "  not found (ok)"
fi

echo "Chat jetris.chat.$ID..."
if n stream purge JETRIS_CHAT --subject "jetris.chat.$ID" -f >/dev/null 2>&1; then
  echo "  purged"
else
  echo "  not found (ok)"
fi

echo "Done."
