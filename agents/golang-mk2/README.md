# golang-mk2 — the Jetris agent whose teammates talk

`golang-mk1`'s next generation: the same self-contained Go module (its own `go.mod`;
its only dependencies are [`nats.go`](https://github.com/nats-io/nats.go) and two
[orbit](https://github.com/synadia-io/orbit.go) helpers, `natscontext` and
`jetstreamext` — **nothing from the jetris repository**), the same wire protocol from
[`../../jetris-agent-guide.md`](../../jetris-agent-guide.md) and game rules from
[`../../jetris-gameplays.md`](../../jetris-gameplays.md), the same fair-play rules — and
an answer to the README's challenge, *"how well do your agents work together?"*: on a
shared board, `golang-mk2` agents on one team **coordinate through a blackboard**.

## What it adds to golang-mk1

- **The blackboard** (`blackboard.go`, `coord.go`; the open protocol is the guide's
  §1.4). The `JETRIS_BLACKBOARD` KV bucket holds one key per seat of a playfield,
  `<gameID>.<board>.<seat>`, whose value is that seat's **claim**: the piece it holds
  and where it means to put it, written with a 10 s TTL the moment the piece spawns,
  and rewritten `locked` when it lands. Every agent plans on the **projection** of its
  teammates' claims — their landings stamped onto its board as settled cells — posts
  its own, and once its echo is in (the bucket is one ordered stream: seeing your own
  claim means having seen every earlier one) checks it against the earlier claims:
  overlapped, it **yields** (the next placement clear of them, claimed in turn);
  ordered after one — resting on it, or in its way — it **waits** for that claim to
  land before it drops, within reason. Later claims yield to earlier ones and nothing
  ever moves an earlier claim, so a crew converges without a leader. Teammates who
  say nothing — humans, `golang-mk1` — are played around as before, their piece's
  straight drop a nudge away from where they are probably going.
- **A path search instead of a walk** (`path.go`). Dijkstra over the piece's
  positions with the game's moves (shift, drop a row, quarter turn), the stack and
  the other pieces as obstacles, and a surcharge for a teammate's spawn box while
  their spawn is imminent — so a piece ducks under a box it would otherwise cross,
  routes round a crewmate's piece where it can, and waits *outside* everyone's spawn
  box where it cannot. Only the diff goes on the wire (a batch may carry a whole
  validated path, guide §4.3), and when the way is clear the walk, the hard drop and
  the **lock ride one batch**: a piece is on the board one round trip less.
- **The shared-board rules golang-mk1 left out** (`sweep.go`, `game.go`): a crewmate's
  piece nobody has moved for 10 s, or whose seat has left, is **vacated** (a CAS batch
  on the crew's board, the txn-gated `vacate` on a team's or a survival board); a piece
  parked on OUR spawn box for 3 s is vacated whole and we spawn; a running open game
  is **left cleanly** on Ctrl-C — own piece vacated, then the seat freed; **locks on a
  shared board carry CAS** with a bounded merge-retry (the GUI's rule), so a stale view
  never buries a crewmate's piece; a walk blocked by the stack no longer locks onto a
  teammate's falling piece; and a piece resting on the stack away from its column gets
  the lock delay's grace (500 ms) for the crewmate in its way to fall away.
- **A collapse moves the plan, not the piece's mind**: a crewmate's clear that shifts
  our piece down shifts its planned landing with it; only a rising stack re-plans.
- **`--publish auto`** (the default): `sync` on a shared board — every cell there may
  have another writer, so every write of ours carries its exact expectation — and
  `async` on a private competitive board, where `golang-mk1`'s pipelining is safe.
- **A planner nineteen times faster** (`engine.go`, `planner.go`): the same Dellacherie
  evaluator and beam lookahead over the game's revealed preview, its features computed
  a row at a time on bit masks and the lookahead run for the six best placements
  rather than every one — 90 ms a plan at the full six-piece preview on a two-seat
  board, where `golang-mk1` took 1.7 s. Play is otherwise unchanged (`--coordinate=false`
  plays a shared board exactly as `golang-mk1` does, `preview_test.go` still pins the
  fair-visibility cap, `--selftest` still checks the RNG's parity with the game).
- **An account of every game** (`stats.go`): one `stats:` log line at the end — pieces,
  lines, score, CAS losses, waits on crewmates' pieces, deferred spawns, stale pieces
  vacated, re-plans by reason, claims yielded, dependency waits — which
  `../../scripts/bench-mk2.sh` reads to compare crews; `--trace` logs every piece's
  lifecycle with microsecond stamps.

## Fair visibility

It decides only on what a human sees (guide §1): its own committed board, the crewmates'
pieces on it, and the pieces the game reveals (`GameMeta.next_count`, trimmed by the
difficulty, never extended — `revealedPieces` in `planner.go` is the planner's one source
of lookahead, and `preview_test.go` checks every difficulty against every preview size).
The blackboard adds what a human teammate could say over the team's voice room and no
more: a claim carries the piece in play and an intention about it — never a preview,
the seed's future, or a stream sequence — and an agent reads only its own board's keys.
Coordination happens where a result is shared: the crew of a cooperative game (survival
and line-goal games included) or one team of a teams game; a competitive board and a
crew scored per seat (`scoring: "individual"`) get none.

## Build & run

```sh
go build -o golang-mk2 .

# a resident that waits for invitations (any mode), coordinating on shared boards
./golang-mk2 --server nats://localhost:4222
./golang-mk2 --context my-context             # ...or like the nats CLI: a context, or (bare) the selected one

# a crew: one hosts a co-op game, two more join it, and the blackboard shows their claims
./golang-mk2 --server nats://localhost:4222 --create --mode cooperative --players 3 --once &
./golang-mk2 --server nats://localhost:4222 --auto-join --once &
./golang-mk2 --server nats://localhost:4222 --auto-join --once &
nats kv watch JETRIS_BLACKBOARD

# uncoordinated, as golang-mk1 plays; a fixed seed and a seatless host, for benchmarks
./golang-mk2 --auto-join --coordinate=false
./golang-mk2 --create --mode cooperative --players 2 --seed 42 --line-goal 40 --host-only --once

# offline conformance checks (RNG, split-deal and bag parity with the game, planner sanity)
./golang-mk2 --selftest
```

Flags are `golang-mk1`'s (`--server`/`--context`/`--user`/`--password`, `--name`,
`--difficulty`, `--join`, `--create` with `--mode`, `--players`, `--teams`,
`--max-agents`, `--pause-alone`, `--extra-cols`, `--extra-rows`, `--next`, `--holes`,
`--random-holes`, `--guideline-garbage`, `--hold`, `--split-pieces`, `--line-goal`,
`--individual`, `--survival`, `--bag`, `--guideline`, `--game-name`, `--auto-join`,
`--wait`, `--once`, `--selftest`) plus:

- `--coordinate` (default true) — use the blackboard on shared boards; `false` plays them
  as `golang-mk1` does.
- `--publish` now defaults to `auto` (sync on a shared board, async on a private one);
  `sync`, `async` and `optimistic` are still there.
- `--seed <n>` with `--create` — the game's piece seed, so benchmark runs deal every
  crew the same pieces.
- `--host-only` with `--create` — create the game and stay in the lobby without a seat
  until it is over: a benchmark's neutral host.
- `--trace` — every piece's lifecycle in the log.
- `--soft-drop` — never hard-drop: the walk goes alone, then the piece soft-drops a row per
  move step to its landing and locks there (a point a row instead of two).

## Measuring it

`../../scripts/bench-mk2.sh` starts a private `nats-server`, has a seatless
`golang-mk2` host create identical cooperative line-goal games (same seed, goal and
width) for three crews in turn — `golang-mk2`, `golang-mk2 --coordinate=false`, and
`golang-mk1` — and tallies the archive records and the `stats:` lines: seconds to the
goal, pieces, pieces per line, score, and the crew's CAS losses, waits, deferred spawns,
re-plans and yields. `-n` games, `-k` crew size, `-l` goal, `-d` difficulty, `-a` arms.

Three 40-line games per arm, crews of two at `hard` on a 14-column board, over a LAN
(`scripts/bench-mk2.sh -n 3 -l 40`, 2026-10-01):

| crew | reached the goal | pieces per line | score | seconds | CAS losses | waits | yields |
|---|---|---|---|---|---|---|---|
| `golang-mk2` | 3 of 3 | 3.70 | 17,461 | 16.7 | 0 | 9.7 | 11.3 |
| `golang-mk2 --coordinate=false` | 2 of 3 (one top-out, at 12 lines) | 4.39 | 14,134 | 11.3 | 0 | 0 | 0 |
| `golang-mk1` | 0 of 3 (top-outs at 7, 26 and 25 lines) | 5.69 | 7,035 | 34.3 | 0 | – | – |

The coordinated crew is the one that keeps its board: every line costs it fewer pieces,
and it was the only crew to finish every game. What it pays is time — a wait on a
crewmate's claim or piece, a yield to an earlier claim — about a third more wall-clock
per game than the same agents playing blind over a LAN. A three-agent crew on an
18-column board (`--players 3`) is the stress case: more crossings, more waits.

The `stats:` line, one per agent per game:

```
stats: game=<id> mode=coop seat=1 coordinate=true pieces=44 lines=12 score=3738 shared_score=6310 cas_losses=0 walk_waits=1/32ms deferred_spawns=1/203ms spawn_unblocks=0 idle_vacates=0 lock_retries=0 locks_dropped=0 replans=droprow:1 claims_yielded=3 dep_waits=0/0ms echo_timeouts=0 duration=9.2s
```

## Tests

```sh
go test .                                                    # the pure tests
nats-server -js -p 4333 -sd /tmp/js &                        # and, against a live server,
JETRIS_NATS_URL=nats://127.0.0.1:4333 go test -race -count=1 .   # the ones that need one (~45 s)
```

The server-gated tests run two agents on one stream and one blackboard: claims seen in
revision order and gone with their TTL, the watch surviving the bucket's deletion, the
second planner planning round the first's claim and the first's lock ending the second's
wait, CAS locks retried and dropped, stale and seatless pieces vacated and a moved one
spared, a spawn box freed after 3 s, a running game left cleanly.

## Reading order

`golang-mk1`'s (`pieces.go` → `rng.go` → `engine.go` → `planner.go` → `difficulty.go` →
`types.go` → `agent.go` → `game.go` → `scoring.go` → `pipeline.go` → `shared.go` →
`main.go`), then what is new:

- `blackboard.go` — the bucket, the claim, posting, the watch and its recovery, the echo.
- `coord.go` — the projection, the conflict rules, the waits, the re-plan reasons.
- `path.go` — the path search and the waiting spot.
- `sweep.go` — the idle clocks, the stale spawn blocker, the vacates.
- `stats.go` — the `stats:` line and `--trace`.

Every protocol interaction cites the section of the agent guide it implements.
