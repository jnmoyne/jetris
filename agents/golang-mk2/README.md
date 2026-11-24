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
- **The whole board, and a forecast of it** (`coord.go` `forecast`, `routeCosts`). A
  piece cannot pass through another, but it can pass above or below it, and the
  claims say where each one is going: a planned, hard-dropped piece will be gone
  from where it stands within its `eta_ms`; a soft-dropped, a waiting or an
  unclaimed one stays in its columns and sinks. The planner prices every placement
  by the walk to it on that forecast — one flood of the path search per plan — so a
  teammate's piece across the way costs the wait for it to go, and distance costs
  next to nothing: a seat plays wherever the board needs a piece, not under its
  spawn point.
- **"You go, I wait"** (`blackboard.go` `announceWait`, `pause`; `coord.go`
  `blockPatience`). A blocked piece says so — its claim again, with `wait_on` naming
  the seats in its way — and keeps its place in the **right of way** (the order of
  first claims: a claim said again for the same placement does not move). The seat
  named cuts its think pause short and goes. Of two pieces in each other's way the
  earlier claim holds its plan and the later one gives way: it waits as long as the
  earlier claim says it needs, out of that claim's drop shaft, then plans again among
  the placements it can walk to right now. A piece is no longer locked where it
  happens to stand because its plan was out of reach.
- **Claims for the pieces to come** (`planner.go` `principalVariation`). Each claim
  carries the lookahead's line for the next two pieces of the writer's own NEXT well
  (`next`); teammates take them as a nudge — never a wall — and a seat's fresh piece is
  expected where its last claim said it would go, before it has claimed it.
- **A well** (`coord.go` `wellFor`, `planner.go` `evaluateWell`; `--well`). A crew
  whose claims all name the same `well` — the board's last column — keeps it open:
  the board is evaluated as if it were a column narrower, nothing is laid in the
  well or across it (a cell buried in it costs more than three holes), a row banked
  beside it counts as the empty row a clear will make of it (or the evaluator hoards
  rows instead of clearing them), and an I piece goes down it for four rows at once —
  for whatever rows there are, once the stack stands past half the board. With a human or a `golang-mk1`
  on the board there is no agreement and no well (a `python-mk2` names the well as this agent does).
- **A plan is checked against the board it lands on** (`game.go` `spoils`,
  `execute`). A claim is a plan made on a projection; by the time the piece drops,
  a teammate's claim may have gone elsewhere, a clear may have shifted the stack, a
  lock may have landed in between. A landing that would cover more empty cells than
  its plan expected is planned again instead of locked, and a clear that moves the
  landing by anything but the rows it cleared re-plans too. On a four-seat board
  this is the difference between a crew that tops out in fifteen seconds and one
  that finishes every game (below).
- **A collapse that always goes through** (`shared.go` `mergeCells`). A clear's
  batch is the settled cells' diff plus the teammates' falling pieces shifted down
  with the stack; a piece hanging within the cleared rows' count of the stack shifts
  into cells the stack held, and the batch then wrote those cells twice — which the
  server rejects as a CAS conflict every time, for every seat that tries. The rows
  stood full and the crew topped out on a clean stack. The batch is folded to one
  write a cell (guide §4.3); a single row seldom shows the bug, four rows at once
  show it often. The gated clear and the garbage raise had the same seam, and there
  the batch committed with the empty write last: the piece left the wire. All three
  are folded, here and in `golang-mk1` and `python-mk2` (`onewrite_test.go`).
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
- **Time constants that follow gravity** (`coord.go`): from level 12 a row falls faster
  than the agent's fixed delays run, and a piece that thinks or waits at the old pace
  is on the stack under its spawn before it moves. So the think pause is no longer
  than three rows of the level being played take (hard's 100 ms up to level 11, 55 ms
  at level 13, 21 ms at level 15); a plan is priced from where gravity will have the
  piece when it moves, not from the spawn row, and what the stack walls off from
  there is out of reach; a wait on a crewmate (a claim depended on, a piece across the
  way) ends with about half the piece's air gone; and the lock delay is the time a
  piece takes to fall the board, 500 ms at most. Four hard seats on one seed: 125-140
  lines before, 143-162 after, levels 13 and 14 played as cleanly as the ones below;
  level 15 (7 ms a row) is where the game now ends.
- **A collapse moves the plan, not the piece's mind**: a crewmate's clear that shifts
  our piece down shifts its planned landing with it — by the rows it cleared, and no
  others: a landing that moved differently, or a rising stack, re-plans.
- **`--publish optimistic`** (the default) or **`pessimistic`** — the human client's MOVE
  PUBLISHING switch, as in `golang-mk1`: pipelined with predicted expectations on a
  shared board (no expectation on in-flight cells of a private competitive board,
  where nobody else writes them), or every commit ack awaited.
- **A planner 150 times faster** (`engine.go`, `planner.go`): the same Dellacherie
  evaluator and beam lookahead over the game's revealed preview, its features computed
  a row at a time on bit masks and the lookahead run for the six best placements
  rather than every one — the six side by side, a goroutine each, every search scoring
  its placements on one board it reuses instead of a new one per placement: 11 ms a
  plan at the full six-piece preview on a two-seat board, where `golang-mk1` took
  1.7 s, and 46 ms on an eight-seat board of 38 columns (`go test -bench Plan`). A plan
  has to fit the think pause: a claim is posted when the plan is in, and the board a
  slow plan was made on has moved by the time it is played. Play is otherwise unchanged (`--coordinate=false`
  plays a shared board exactly as `golang-mk1` does, `preview_test.go` still pins the
  fair-visibility cap, `--selftest` still checks the RNG's parity with the game).
  A row's mask is 64 bits, so the widest board it plays is 63 columns (a crew of
  eight at up to seven extra columns a seat); it joins no game on a wider one.
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
more: a claim carries the piece in play and an intention about it, and tentative
placements for the pieces the writer's **own** NEXT well shows (as many as the game
reveals and the difficulty looks at, two at most) — never a teammate's sequence, the
seed's future, or a stream sequence — and an agent reads only its own board's keys.
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
- `--publish` takes `optimistic` (the default) or `pessimistic`, like `golang-mk1`.
- `--seed <n>` with `--create` — the game's piece seed, so benchmark runs deal every
  crew the same pieces.
- `--host-only` with `--create` — create the game and stay in the lobby without a seat
  until it is over: a benchmark's neutral host.
- `--trace` — every piece's lifecycle in the log.
- `--well auto|on|off` (default `auto`) — keep the board's last column open for
  four-row clears on a coordinated board, when every teammate's claim agrees; `auto`
  is every game but a survival one (a rising floor leaves no room to bank rows).
- `--soft-drop` — never hard-drop: the walk goes alone, then the piece soft-drops a row per
  move step to its landing and locks there (a point a row instead of two).

## Measuring it

`../../scripts/bench-mk2.sh` starts a private `nats-server`, has a seatless
`golang-mk2` host create identical cooperative line-goal games (same seed, goal and
width) for three crews in turn — `golang-mk2`, `golang-mk2 --coordinate=false`, and
`golang-mk1` — and tallies the archive records and the `stats:` lines: seconds to the
goal, pieces, pieces per line, score, and the crew's CAS losses, waits, deferred spawns,
re-plans, yields, reach (columns from spawn to landing), four-row clears and locks away
from the planned column. `-n` games, `-k` crew size, `-l` goal, `-d` difficulty, `-a`
arms, `-f` further flags for the `golang-mk2` seats (`-f "--well off"`, `-f "--trace"`).

Three 40-line games per arm, crews of two at `hard` on a 14-column board, on one
machine (`scripts/bench-mk2.sh -n 3`, 2026-11-20):

| crew | reached the goal | pieces per line | score | seconds | four-row clears | waits | yields |
|---|---|---|---|---|---|---|---|
| `golang-mk2` | 3 of 3 | 3.67 | 22,868 | 16.7 | 5.0 | 8.0 | 9.3 |
| `golang-mk2 --coordinate=false` | 2 of 3 (one top-out, at 37 lines) | 4.15 | 17,950 | 13.0 | 0 | 0.3 | 0 |
| `golang-mk1` | 0 of 3 (top-outs at 11, 15 and 19 lines) | 6.62 | 5,023 | 23.3 | – | – | – |

Five games per arm, crews of four on a 22-column board (`-n 5 -k 4`), the stress
case — four pieces in the air over four adjoining spawn boxes:

| crew | reached the goal | pieces per line | score | seconds | four-row clears | waits | yields |
|---|---|---|---|---|---|---|---|
| `golang-mk2` | 5 of 5 | 5.77 | 25,413 | 27.4 | 4.2 | 33.6 | 41.4 |
| `golang-mk2 --well off` | 5 of 5 | 5.66 | 20,336 | 25.8 | 0 | 30.6 | 48.6 |
| `golang-mk1` | 0 of 5 (top-outs at 0 or 1 line) | – | 1,340 | 21.8 | – | – | – |

A row of the wide board is 22 cells, 5.5 pieces: the coordinated crew wastes almost
nothing. The well costs a couple of seconds and a few pieces left banked when the
goal falls, and pays a quarter more score. Before the landing was checked against
the board and the collapse batch folded (above), the same four-seat crew topped out
in all of its games, at 10.6 pieces a line. What coordination costs is time — a wait
on a crewmate's claim or piece, a yield to an earlier claim — about a quarter more
wall-clock per game than the same agents playing blind.

The seconds in both tables were measured before the planner ran its lookaheads side by
side (2026-11-22). On the same seeds a `golang-mk2` crew now reaches the goal in 10.3 s
with two seats, 9.0 s with four and 13.3 s with eight (`-k 8`, a 38-column board, where
it took 74.7 s), at the same pieces per line.

The `stats:` line, one per agent per game:

```
stats: game=<id> mode=coop seat=1 coordinate=true pieces=61 lines=12 score=5310 shared_score=25534 cas_losses=0 walk_waits=11/402ms deferred_spawns=1/101ms spawn_unblocks=0 idle_vacates=0 lock_retries=0 locks_dropped=0 replans=blocked:1,collapse:7,droprow:16,holes:2 claims_yielded=12 dep_waits=8/1330ms echo_timeouts=0 reach=6.4 quads=3 stand_locks=0 hurries=1 intents=15/51 holes=1/1 duration=31.6s
```

`reach` is the mean distance, in columns, from the seat's spawn point to its landings;
`quads` its four-row clears; `stand_locks` the locks made away from the planned column;
`hurries` the think pauses cut short for a teammate waiting on its piece; `intents` how
many of the placements it announced for its next piece it then played (kept/made);
`holes` the empty cells its locks covered, and how many of those its plans expected.

## Tests

```sh
go test .                                                    # the pure tests
nats-server -js -p 4333 -sd /tmp/js &                        # and, against a live server,
JETRIS_NATS_URL=nats://127.0.0.1:4333 go test -race -count=1 .   # the ones that need one (~45 s)
```

The server-gated tests run two agents on one stream and one blackboard: claims seen in
revision order and gone with their TTL, a wait announced keeping its place and hurrying
the teammate it names, the lock staying the last word on a key, the
second planner planning round the first's claim and the first's lock ending the second's
wait, CAS locks retried and dropped, stale and seatless pieces vacated and a moved one
spared, a spawn box freed after 3 s, a running game left cleanly.

## Reading order

`golang-mk1`'s (`pieces.go` → `rng.go` → `engine.go` → `planner.go` → `difficulty.go` →
`types.go` → `agent.go` → `game.go` → `scoring.go` → `pipeline.go` → `shared.go` →
`main.go`), then what is new:

- `blackboard.go` — the bucket, the claim, posting, the watch and its recovery, the echo.
- `coord.go` — the projection, the conflict rules, the waits, the re-plan reasons;
  then the forecast and the route costs, who waits and for how long, the next pieces'
  tentative landings, and the well.
- `path.go` — the flood, the path and the waiting spot.
- `sweep.go` — the idle clocks, the stale spawn blocker, the vacates.
- `stats.go` — the `stats:` line and `--trace`.

Every protocol interaction cites the section of the agent guide it implements.
