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

## What it is, module by module

The package mirrors golang-mk2's files one for one, so the two read side by side:

| `python_mk2/` | golang-mk2 | |
|---|---|---|
| `pieces.py`, `rng.py`, `scoring.py`, `difficulty.py`, `rules.py` | `pieces.go`, `rng.go`, `scoring.go`, `difficulty.go` | geometry, the seeded piece sequence (bit for bit), the scoring and garbage tables |
| `engine.py`, `planner.py` | `engine.go`, `planner.go` | the Dellacherie evaluator and the beam lookahead over the revealed preview |
| `path.py` | `path.go` | the path search and the waiting spot |
| `agent.py`, `main.py`, `natsctx.py` | `agent.go`, `main.go` | the lobby: presence, invitations, joining, hosting, the countdown; NATS CLI contexts |
| `game.py`, `shared.py`, `pipeline.py`, `survival.py` | same names | the engine for one game: spawn, moves, locks, clears, garbage, outcomes, the archive |
| `blackboard.py`, `coord.py`, `sweep.py`, `stats.py` | same names | claims, the projection and the conflict rules, the idle sweeps, the `stats:` line |
| `replay.py`, `serverlog.py` | same names | the replay copy and its retention, the server log |
| `workers.py` | | the lookahead's worker processes |
| `jsx.py`, `wire.py`, `aio.py`, `consts.py` | (nats.go, orbit) | what the Go client libraries gave golang-mk2: CAS requests, atomic batches, the multi-subject direct get, TTL'd KV puts, a KV watch that ends with its bucket |

One asyncio loop runs everything. Go's `g.mu` is an `asyncio.Lock` held across the same
round trips, the goroutines are tasks, and the planner's search runs off the loop when
the preview is deep, so the consumers and the heartbeats keep running under it.

## What differs from golang-mk2

- **The planner runs on worker processes.** Same algorithm, same constants, same
  answers: `tests/test_vectors.py` checks 160 seeded boards against golang-mk2's own
  output (placements, lines, waits, scores, at every lookahead depth, with and without a
  projection). It works on whole-board integers instead of row masks (a feature is a
  handful of big-integer operations), which makes a plan at the full six-piece preview
  on a two-seat board about 170 ms in one process, where golang-mk2 takes 87 ms on the
  same machine (`uv run python bench_plan.py`, next to `go test -bench Plan`). A plan is
  six independent searches, one per placement in the beam, so they run side by side on
  worker processes (`workers.py`), started with the first game that reveals a deep
  preview: **about 30 ms a plan**. `--plan-workers N` sets how many (default: six, as
  the cores allow), `--plan-workers 0` plans in-process, as an agent on a single core
  does on its own. It matters beyond speed: a claim is posted when the plan is in, and
  a crew whose plans outlast the think pause sees each other's claims too late to plan
  on them (measured below).
- **Boards wider than 32 columns** work: Python's integers have no width.
- **A deleted bucket or stream is noticed by a probe** (every 5 s) rather than by a
  closed subscription, so the blackboard and lobby watches recover within about 6 s.
- **Board pictures in the archive record** are read with the batched direct get, a
  round trip per board instead of one per cell.
- **Contexts**: `url`, `user`/`password`, `token`, `creds`, `nkey`, TLS `cert`/`key`/`ca`,
  `inbox_prefix` and `jetstream_domain` are read; `creds` and `nkey` need the `nkeys`
  package (`uv sync --extra nkeys`); `socks_proxy`, `nsc` lookups and Windows cert stores
  are refused by name.
- Where golang-mk2 and the guide differ, this follows golang-mk2: the flash carries no
  `tm`, and the game stream is created with no per-subject limit.

## Fair visibility

As golang-mk2's: it decides only on what a human sees (guide §1): its own committed
board, the crewmates' pieces on it, and the pieces the game reveals (`next_count`,
trimmed by the difficulty, never extended; `revealed_pieces` in `planner.py` is the
planner's one source of lookahead, and `tests/test_preview.py` checks every difficulty
against every preview size). A claim carries the piece in play and an intention about
it, never a preview, the seed's future or a stream sequence, and an agent reads only its
own board's keys.

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

Three 40-line games per arm, crews of two at `hard` on a 14-column board, one machine,
the same seeds for every arm (`scripts/bench-mk2.sh -n 3 -l 40 -a "mk2 pymk2 mixed
pymk2solo"`, 2026-11-11):

| crew | reached the goal | pieces per line | score | seconds | CAS losses | waits | re-plans | yields |
|---|---|---|---|---|---|---|---|---|
| `golang-mk2` | 3 of 3 | 3.58 | 18,066 | 17.0 | 0.3 | 9.7 | 12.7 | 7.7 |
| `python-mk2` | 3 of 3 | 3.65 | 18,928 | 12.0 | 1.0 | 14.7 | 12.7 | 6.0 |
| one of each | 3 of 3 | 3.73 | 18,974 | 13.7 | 0 | 9.3 | 13.7 | 3.7 |
| `python-mk2 --coordinate=false` | 3 of 3 | 4.10 | 18,477 | 11.0 | 1.3 | 0 | 10.7 | 0 |

A Python crew keeps its board as a Go crew does, a mixed crew as either, and the
blackboard is worth about half a piece a line to it.
Three games an arm is a small sample: the arms within a tenth of a piece per line of
each other are not told apart by it. The Python crews finish sooner because a plan on
the workers is quicker than golang-mk2's. With `--plan-workers 0` (170 ms a plan, longer
than the 100 ms think pause) the same crew took 3.85 pieces a line and 24.7 s a game,
with 21 re-plans: it works, and it is the reason for the workers.

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
retention), plus the planner's parity vectors. `tests/data/planner_vectors.json` is
written by golang-mk2 itself:

```sh
cd ../golang-mk2 && MK2_VECTORS_OUT=../python-mk2/tests/data/planner_vectors.json go test -run TestWriteVectors .
```

The server-gated tests cover the JetStream plumbing (CAS requests, atomic batches
rejected whole, the multi-subject direct get on a 600-cell board, claims gone with their
TTL, a watch ending with its bucket) and golang-mk2's own server tests: two agents on
one stream and one blackboard.

## Reading order

golang-mk2's: `pieces.py` → `rng.py` → `engine.py` → `planner.py` → `difficulty.py` →
`wire.py` → `agent.py` → `game.py` → `scoring.py` → `pipeline.py` → `shared.py` →
`main.py`, then `blackboard.py`, `coord.py`, `path.py`, `sweep.py`, `stats.py`; and
`jsx.py` for what the protocol asks of JetStream beyond a client's stock calls.
