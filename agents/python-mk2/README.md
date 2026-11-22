# python-mk2: golang-mk2, in Python

A port of [`golang-mk2`](../golang-mk2/README.md), the Jetris agent whose teammates talk,
to Python: the same wire protocol from
[`../../jetris-agent-guide.md`](../../jetris-agent-guide.md), the same game rules from
[`../../jetris-gameplays.md`](../../jetris-gameplays.md), the same fair-play rules, the
same flags, and the same **blackboard** (guide §1.4), so a `python-mk2` and a `golang-mk2`
on one board coordinate with each other as two of either do. It is self-contained (its
only dependency is [`nats-py`](https://github.com/nats-io/nats.py); nothing from the
jetris repository) and plays all three modes as an ordinary peer: cooperative (survival,
line goals and per-seat scoring included), competitive, and teams.

It is also the proof of the guide's claim that the blackboard is an open protocol:
nothing here reads golang-mk2's code at run time, only the guide's subjects and payloads.

It keeps pace with `golang-mk2`: the guide's optional claim fields (§1.4: `at`, `drop`,
`wait_on`, `next`, `well`) and what goes with them are here too — route costs on a
forecast of the teammates' pieces, the right of way between blocked pieces, claims for
the next pieces, the well, the check of a landing against the board before it locks, and
transform batches folded to one write a cell. [golang-mk2's README](../golang-mk2/README.md)
describes each; the names below are the same, in snake case.

## What it is, module by module

The package mirrors golang-mk2's files one for one, so the two read side by side:

| `python_mk2/` | golang-mk2 | |
|---|---|---|
| `pieces.py`, `rng.py`, `scoring.py`, `difficulty.py`, `rules.py` | `pieces.go`, `rng.go`, `scoring.go`, `difficulty.go` | geometry, the seeded piece sequence (bit for bit), the scoring and garbage tables |
| `engine.py`, `planner.py` | `engine.go`, `planner.go` | the Dellacherie evaluator and the beam lookahead over the revealed preview |
| `path.py` | `path.go` | the path search (a flood that names the seats in its way) and the waiting spot |
| `agent.py`, `main.py`, `natsctx.py` | `agent.go`, `main.go` | the lobby: presence, invitations, joining, hosting, the countdown; NATS CLI contexts |
| `game.py`, `shared.py`, `pipeline.py`, `survival.py` | same names | the engine for one game: spawn, moves, locks, clears, garbage, outcomes, the archive |
| `blackboard.py`, `coord.py`, `sweep.py`, `stats.py` | same names | claims and their right of way, the projection and the conflict rules, the forecast and the route costs, the well, the idle sweeps, the `stats:` line |
| `replay.py`, `serverlog.py` | same names | the replay copy and its retention, the server log |
| `workers.py` | | the lookahead's worker processes |
| `jsx.py`, `wire.py`, `aio.py`, `consts.py` | (nats.go, orbit) | what the Go client libraries gave golang-mk2: CAS requests, atomic batches, the multi-subject direct get, TTL'd KV puts, a KV watch that ends with its bucket |

One asyncio loop runs everything. Go's `g.mu` is an `asyncio.Lock` held across the same
round trips, the goroutines are tasks, and the planner's search runs off the loop when
the preview is deep, so the consumers and the heartbeats keep running under it.

## What differs from golang-mk2

- **The planner runs on worker processes.** Same algorithm, same constants, same
  answers: `tests/test_vectors.py` checks 160 seeded boards against golang-mk2's own
  output (placements, lines, waits, scores and the lookahead's best line, at every
  lookahead depth, with and without a projection, a route, an intent and a well). It works on whole-board integers instead of row masks (a feature is a
  handful of big-integer operations), which makes a plan at the full six-piece preview
  on a two-seat board about 170 ms in one process, where golang-mk2, its six searches
  on goroutines, takes 11 ms (`uv run python bench_plan.py`, next to `go test -bench Plan`). A plan is
  six independent searches, one per placement in the beam, so they run side by side on
  worker processes (`workers.py`), started with the first game that reveals a deep
  preview: **about 30 ms a plan**. `--plan-workers N` sets how many (default: six, as
  the cores allow), `--plan-workers 0` plans in-process, as an agent on a single core
  does on its own. It matters beyond speed: a claim is posted when the plan is in, and
  a crew whose plans outlast the think pause sees each other's claims too late to plan
  on them (measured below).
- **The next pieces' placements cost no second search.** golang-mk2 runs the lookahead
  once more from the placement it chose, to say where its NEXT pieces go (`next`). Here
  every search in the beam hands back the line that made its total, and the chosen
  placement's is used as it is (the same line: `tests/test_vectors.py` checks it against
  golang-mk2's); only a placement from outside the beam is searched again.
- **The route costs run on the loop**: the floods that price every placement's walk on
  a forecast of the teammates' pieces (`coord.route_costs`) take about 15 ms for a piece
  on a four-seat board with three pieces in the air (`bench_plan.py`), against
  golang-mk2's 2.5 ms; a plan pays it once.
- **Boards of any width** work: Python's integers have no width, where golang-mk2's row
  masks hold 63 columns and it joins no game on a wider board.
- **A game whose stream is gone is noticed by a probe** (every 5 s) rather than by a
  closed subscription.
- **Board pictures in the archive record** are read with the batched direct get, a
  round trip per board instead of one per cell.
- **Contexts**: `url`, `user`/`password`, `token`, `creds`, `nkey`, TLS `cert`/`key`/`ca`,
  `inbox_prefix` and `jetstream_domain` are read; `creds` and `nkey` need the `nkeys`
  package (`uv sync --extra nkeys`); `socks_proxy`, `nsc` lookups and Windows cert stores
  are refused by name.
- Where golang-mk2 and the guide differ, this follows golang-mk2: the flash carries no
  `tm`.

## Fair visibility

As golang-mk2's: it decides only on what a human sees (guide §1): its own committed
board, the crewmates' pieces on it, and the pieces the game reveals (`next_count`,
trimmed by the difficulty, never extended; `revealed_pieces` in `planner.py` is the
planner's one source of lookahead, and `tests/test_preview.py` checks every difficulty
against every preview size). A claim carries the piece in play and an intention about
it, and tentative placements for the pieces its writer's own NEXT well shows (two at
most, and never more than the planner may see); never a teammate's sequence, the seed's
future or a stream sequence. An agent reads only its own board's keys.

## Build & run

Needs Python 3.11 or later and [uv](https://docs.astral.sh/uv/) (or any virtualenv with
`pip install .`).

```sh
uv sync                                        # the virtualenv, nats-py, pytest

# a resident that waits for invitations (any mode), coordinating on shared boards
uv run python-mk2 --server nats://localhost:4222
uv run python-mk2 --context my-context         # ...or like the nats CLI: a context, or (bare) the selected one

# a mixed crew: a Go agent hosts a co-op game, a Python one joins it, and the blackboard shows both
../golang-mk2/golang-mk2 --server nats://localhost:4222 --create --mode cooperative --players 2 --once &
uv run python-mk2 --server nats://localhost:4222 --auto-join --once &
nats kv watch JETRIS_BLACKBOARD

# hosting, uncoordinated play, a fixed seed and a seatless host: golang-mk2's flags
uv run python-mk2 --create --mode teams --players 2 --split-pieces --once
uv run python-mk2 --auto-join --coordinate=false
uv run python-mk2 --auto-join --well off      # no column kept open for four-row clears (auto: every game but a survival one)
uv run python-mk2 --create --mode cooperative --players 2 --seed 42 --line-goal 40 --host-only --once

# offline conformance checks (RNG, split-deal and bag parity with the game, planner sanity)
uv run python-mk2 --selftest
```

Every golang-mk2 flag is here under the same name with the same default
(`uv run python-mk2 --help`); booleans are given as `--flag` or `--flag=false`. One flag
is its own: `--plan-workers`. The player name is `python-mk2-<4 hex>-<difficulty>`.

## Measuring it

`../../scripts/bench-mk2.sh` has three more arms for it: `pymk2` (crews of python-mk2),
`pymk2solo` (the same, `--coordinate=false`) and `mixed` (golang-mk2 and python-mk2
seats alternating on one board):

```sh
scripts/bench-mk2.sh -n 3 -l 40 -a "mk2 pymk2 mixed"
```

Three 40-line games per arm at `hard`, one machine, the same seeds for every arm
(2026-11-21). Crews of two on a 14-column board (`scripts/bench-mk2.sh -n 3 -l 40 -a
"mk2 pymk2 mixed"`):

| crew | reached the goal | pieces per line | score | seconds | four-row clears | waits | re-plans | yields |
|---|---|---|---|---|---|---|---|---|
| `golang-mk2` | 3 of 3 | 3.69 | 20,312 | 16.7 | 3.0 | 5.3 | 22.7 | 7.3 |
| `python-mk2` | 3 of 3 | 3.64 | 22,505 | 11.3 | 4.0 | 12.3 | 13.0 | 6.3 |
| one of each | 3 of 3 | 3.65 | 21,911 | 13.7 | 3.7 | 8.7 | 19.3 | 6.7 |

Crews of four on a 22-column board (`-k 4 -x 4`):

| crew | reached the goal | pieces per line | score | seconds | four-row clears | waits | re-plans | yields |
|---|---|---|---|---|---|---|---|---|
| `golang-mk2` | 3 of 3 | 5.69 | 24,799 | 26.7 | 4.3 | 32.7 | 96.7 | 40.0 |
| `python-mk2` | 3 of 3 | 5.68 | 24,037 | 12.7 | 3.7 | 31.3 | 66.3 | 25.7 |
| two of each | 3 of 3 | 5.93 | 25,628 | 18.0 | 4.7 | 38.3 | 84.0 | 30.7 |
| `python-mk2` as it was before 2026-11-21 | 2 of 3 (one topped out at 4 lines) | 7.88 | 13,832 | 11.3 | 0 | 37.3 | 33.3 | 23.0 |

A Python crew keeps its board as a Go crew does, and a mixed crew as either: the two
agree on the well over the blackboard (the four-row clears in the mixed rows are made by
both kinds) and on who goes first when their pieces cross. A 22-column row is 5.5
pieces, so a four-seat crew wastes next to nothing. The last row is this agent before it
learned the routes, the right of way, the well and the landing's check — the same games,
the same day, on `nats-py` 2.17.
Three games an arm is a small sample: the arms within a tenth of a piece per line of
each other are not told apart by it. The Python crews finish sooner in these tables
because golang-mk2's plan was then the slower one: half a second on an eight-seat
board, most of a game. Its planner has since been made faster than the workers
(2026-11-22), and on the same seeds a golang-mk2 crew now takes 10.3 s with two seats,
9.0 s with four and 13.3 s with eight, a python-mk2 crew 11.3 s, 12.7 s and 24.0 s, at
the same pieces per line. With `--plan-workers 0` (a plan longer than
the 100 ms think pause) a crew of two took 3.85 pieces a line and 24.7 s a game with 21
re-plans, and uncoordinated (`--coordinate=false`, the `pymk2solo` arm) 4.10 pieces a
line, when both were measured on 2026-11-11: it works without the workers, and they are
the reason it works well.

The `stats:` line at the end of every game is golang-mk2's, field for field, and
`--trace` logs every piece's lifecycle (with the search's time).

## Tests

```sh
uv run pytest                                              # the pure tests
nats-server -js -p 4333 -sd /tmp/js &                      # and, against a live server,
JETRIS_NATS_URL=nats://127.0.0.1:4333 uv run pytest        # the ones that need one
```

The pure tests are golang-mk2's, ported (the RNG, split and bag fixtures, the scoring
and survival tables, the planner's features, the path search, the projection and the
conflict rules, the sweeps' clocks, the lobby's join and start rules, the replay
retention; the claims' right of way, the forecast, the route costs, the well and the
landing's check in `test_routes.py`), plus the planner's parity vectors and a pin of the
packed search's well evaluation to its cell-at-a-time form. `tests/data/planner_vectors.json`
is written by golang-mk2 itself — regenerate it whenever golang-mk2's planner changes:

```sh
cd ../golang-mk2 && MK2_VECTORS_OUT=../python-mk2/tests/data/planner_vectors.json go test -run TestWriteVectors .
```

The server-gated tests cover the JetStream plumbing (CAS requests, atomic batches
rejected whole, the multi-subject direct get on a 600-cell board, claims gone with their
TTL, a watch ending with its bucket) and golang-mk2's own server tests: two agents on
one stream and one blackboard, a wait announced to a teammate that hurries, and a
transform's batch that writes each cell once (`test_onewrite_server.py`). They need
`nats-py` 2.17 or later, which reads the ack subjects of current servers; 2.16 loses
every watch against them.

## Reading order

golang-mk2's: `pieces.py` → `rng.py` → `engine.py` → `planner.py` → `difficulty.py` →
`wire.py` → `agent.py` → `game.py` → `scoring.py` → `pipeline.py` → `shared.py` →
`main.py`, then `blackboard.py`, `coord.py`, `path.py`, `sweep.py`, `stats.py`; and
`jsx.py` for what the protocol asks of JetStream beyond a client's stock calls.
