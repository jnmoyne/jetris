#!/usr/bin/env bash
# bench-mk2.sh — crews of golang-mk2 against crews of golang-mk1, and
# against golang-mk2 playing uncoordinated (--coordinate=false), on
# IDENTICAL cooperative line-goal games: the same seed, goal, width and
# neutral host (golang-mk2 --host-only creates the game and takes no seat)
# for every arm, so the only thing that differs between the arms is the crew.
#
#   scripts/bench-mk2.sh [-n games] [-k crew] [-s seed] [-l line-goal] [-d difficulty]
#                        [-x extra-cols] [-p port] [-o outdir] [-a "arms"] [--keep]
#
# Defaults: 3 games, a crew of 2, seed 42 (game i plays seed+i), 40 lines,
# hard, 4 extra columns per seat, a private nats-server on port 4333, the
# logs and records under bench-out/<timestamp>, the arms "mk2 mk2solo mk1".
# Needs go, nats-server, the nats CLI and jq. It kills only the processes it
# started; --keep leaves the server's store directory behind.
set -euo pipefail

N=3 K=2 SEED=42 GOAL=40 DIFF=hard EXTRA=4 PORT=4333 OUT="" KEEP=0 ARMS="mk2 mk2solo mk1"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -n) N=$2; shift 2 ;;
    -k) K=$2; shift 2 ;;
    -s) SEED=$2; shift 2 ;;
    -l) GOAL=$2; shift 2 ;;
    -d) DIFF=$2; shift 2 ;;
    -x) EXTRA=$2; shift 2 ;;
    -p) PORT=$2; shift 2 ;;
    -o) OUT=$2; shift 2 ;;
    -a) ARMS=$2; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n 2,16p "$0"; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done

ROOT=$(cd "$(dirname "$0")/.." && pwd)
STAMP=$(date +%Y%m%d-%H%M%S)
OUT=${OUT:-$ROOT/bench-out/$STAMP}
mkdir -p "$OUT"
TMP=$(mktemp -d -t jetris-bench)
URL="nats://127.0.0.1:$PORT"
TAG=$(printf '%04x' $((RANDOM % 65536)))

echo "building the agents…"
(cd "$ROOT/agents/golang-mk1" && go build -o "$TMP/golang-mk1" .)
(cd "$ROOT/agents/golang-mk2" && go build -o "$TMP/golang-mk2" .)

PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  [[ -n "${SERVER:-}" ]] && kill "$SERVER" 2>/dev/null || true
  [[ $KEEP -eq 0 ]] && rm -rf "$TMP"
}
trap cleanup EXIT

echo "starting a private nats-server on $URL (store $TMP/js)…"
nats-server -js -p "$PORT" -sd "$TMP/js" > "$OUT/server.log" 2>&1 &
SERVER=$!
for i in $(seq 1 50); do
  if nats --server "$URL" pub bench.ping up >/dev/null 2>&1; then break; fi
  sleep 0.2
done

# One game per (arm, round): the neutral host creates it, the crew joins it,
# and the host process ends once the game is over (its listing is gone).
run_game() {
  local arm=$1 round=$2 id="bench-$arm-$round-$TAG"
  local bin="$TMP/golang-mk2" flags=""
  case "$arm" in
    mk2) ;;
    mk2solo) flags="--coordinate=false" ;;
    mk1) bin="$TMP/golang-mk1" ;;
    *) echo "unknown arm $arm" >&2; return 1 ;;
  esac
  "$TMP/golang-mk2" --server "$URL" --create --host-only --mode cooperative --players "$K" --max-agents "$K" \
    --seed $((SEED + round)) --line-goal "$GOAL" --extra-cols "$EXTRA" --game-name "$id" --once \
    > "$OUT/$id-host.log" 2>&1 &
  local host=$!
  PIDS+=("$host")
  sleep 1
  local players=()
  for j in $(seq 1 "$K"); do
    # shellcheck disable=SC2086
    "$bin" --server "$URL" --join "$id" --once --difficulty "$DIFF" $flags > "$OUT/$id-p$j.log" 2>&1 &
    players+=("$!")
    PIDS+=("$!")
    sleep 0.3
  done
  local waited=0
  while kill -0 "$host" 2>/dev/null; do
    sleep 1
    waited=$((waited + 1))
    if [[ $waited -ge 900 ]]; then
      echo "  $id: timed out after ${waited}s — killing it" >&2
      kill "$host" "${players[@]}" 2>/dev/null || true
      break
    fi
  done
  for p in "${players[@]}"; do wait "$p" 2>/dev/null || true; done
  echo "  $id: done in ${waited}s"
}

for round in $(seq 1 "$N"); do
  for arm in $ARMS; do
    echo "round $round, $arm crew of $K (seed $((SEED + round)), $GOAL lines)…"
    run_game "$arm" "$round"
  done
done

echo "collecting the archive records…"
games=$((N * $(echo "$ARMS" | wc -w)))
nats --server "$URL" sub jetris.archive --stream "JETRIS_ARCHIVE" --all --count "$games" 2>/dev/null \
  | grep '^{' | grep "\"bench-.*-$TAG\"" > "$OUT/archive.jsonl" || true
grep -h "stats:" "$OUT"/bench-*-p*.log > "$OUT/stats.txt" || true

echo
echo "== $N game(s) per arm, crews of $K at $DIFF, $GOAL lines, seeds $((SEED + 1))..$((SEED + N)), $EXTRA extra columns =="
printf "%-8s %6s %8s %7s %8s %7s | %8s %8s %8s %8s %8s\n" arm games "seconds" pieces "pcs/line" score cas waits "deferred" replans yields
for arm in $ARMS; do
  jq -r --arg arm "$arm" '
    select(.game_id | startswith("bench-" + $arm + "-")) |
    ((.finished_at | sub("\\.[0-9]+Z$"; "Z") | fromdate) - (.started_at | sub("\\.[0-9]+Z$"; "Z") | fromdate)) as $secs |
    ([.players[].piece_count] | add) as $pieces |
    ([.players[].lines] | add) as $lines |
    "\($secs) \($pieces) \($lines) \(.total_score)"' "$OUT/archive.jsonl" 2>/dev/null \
  | awk -v arm="$arm" -v statsfile="$OUT/stats.txt" '
      { n++; secs += $1; pieces += $2; lines += $3; score += $4 }
      END {
        if (n == 0) { printf "%-8s %6d\n", arm, 0; exit }
        cas = waits = def = rep = yld = 0
        while ((getline line < statsfile) > 0) {
          if (line !~ "game=bench-" arm "-") continue
          if (match(line, /cas_losses=[0-9]+/)) cas += substr(line, RSTART + 11, RLENGTH - 11)
          if (match(line, /walk_waits=[0-9]+/)) waits += substr(line, RSTART + 11, RLENGTH - 11)
          if (match(line, /deferred_spawns=[0-9]+/)) def += substr(line, RSTART + 16, RLENGTH - 16)
          if (match(line, /claims_yielded=[0-9]+/)) yld += substr(line, RSTART + 15, RLENGTH - 15)
          if (match(line, /replans=[a-z0-9:,]+/)) { s = substr(line, RSTART + 8, RLENGTH - 8); m = split(s, parts, ","); for (i = 1; i <= m; i++) { split(parts[i], kv, ":"); rep += kv[2] } }
        }
        printf "%-8s %6d %8.1f %7.1f %8.2f %7.0f | %8.1f %8.1f %8.1f %8.1f %8.1f\n", arm, n, secs / n, pieces / n, pieces / (lines > 0 ? lines : 1), score / n, cas / n, waits / n, def / n, rep / n, yld / n
      }'
done
echo
echo "(seconds: start to finish; pieces and score: the crew's per game; cas, waits, deferred, replans, yields: the crew's per game, off the stats: lines — golang-mk1 prints none)"
echo "logs and records: $OUT"
