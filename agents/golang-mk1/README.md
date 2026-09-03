# golang-mk1 — a self-contained Jetris agent in Go

A Go sibling of [`example-python`](../example-python/): it depends on **nothing in the
jetris repository** (its own `go.mod`; its only dependencies are the
[`nats.go`](https://github.com/nats-io/nats.go) client and two
[orbit](https://github.com/synadia-io/orbit.go) helpers — `natscontext` for NATS-CLI-compatible
contexts and `jetstreamext` for the multi-subject direct get that snapshots a board in
one round trip) and implements the wire protocol
from [`../../jetris-agent-guide.md`](../../jetris-agent-guide.md) — game rules from
[`../../jetris-gameplays.md`](../../jetris-gameplays.md) — directly against NATS/JetStream,
following the guide's fair-play rules. Unlike the example, it plays with the repo agent's
strong **Dellacherie** brain and its `easy`/`medium`/`hard` difficulties.

## What it does

- Plays **all three modes** — cooperative (one shared wide board, deferred spawns,
  merge-retry clears, shared score), competitive (private boards, the garbage ledger),
  and teams (a coop-style board per team, team-vs-team garbage with cascading piece
  lifts, team verdicts). It sits in the lobby as a resident, accepts invitations
  (teams invitations join the invited team), and with `--auto-join` also joins open
  games that allow agents. With `--create` it **hosts** any mode: it creates the game
  stream + meta + lobby listing itself (agent seats open by default), joins its own
  game, and waits for opponents.
- Outlives its server: when the NATS server goes away the resident just waits, idle
  (no CPU), while nats.go reconnects in the background — forever, it never gives up — and
  its lobby mirror and presence pick up by themselves once the server is back. Only a
  fatal close (an authorization failure on reconnect, say) ends it, with an error.
- Carries every peer responsibility itself: presence heartbeat, the join CAS on the lobby
  KV, the roster announcement, the ready toggle and (when its toggle completes the set) the
  5→0 countdown, its own engine — a bit-exact port of the game's PCG + 7-bag piece RNG,
  spawning, gravity, lock-in, line clears, garbage application, top-out — plus per-player
  game-over events, CAS-failure flashes, and, when it wins, the finish → archive → cleanup
  sequence.
- Publishes every board change as an **atomic CAS batch** to its cell subjects
  (active → locked → empty ordering, per-subject expected-last-sequence, write-through), and
  every bulk transform (line-clear collapse, garbage cascade) as a **gated** batch behind
  its txn register — exactly as the guide specifies.
- **Pipelines its move batches** (guide §4.3): `--publish` picks the discipline — `sync`
  (await every commit ack, the classic one-round-trip-per-batch path), `async` (the
  default: a batch is sent and the next move made at once, a cell an un-acked batch
  already wrote carrying no expectation), or `optimistic` (pipelined with the sequences
  in-flight writes are *predicted* to get, full CAS protection at the price of a repair on
  a wrong guess) — the same three modes the human client plays. A lost pipelined batch
  drains the acks, refetches the committed board in one multi-subject direct get, vacates
  the strays the poisoned batches left, flashes, and re-plans; the spawn, the lock-in and
  every gated transform settle the pipeline first so their expectations are exact
  (`pipeline.go`).
- **Strategy** is the El-Tetris one-ply Dellacherie heuristic (landing height, eroded cells,
  row/column transitions, holes, cumulative wells) with beam-pruned lookahead over the
  game's revealed piece preview — the same evaluator the in-repo reference agent uses.
  Difficulty tunes think/move pacing, a blunder model, and lookahead depth. It plays under
  the name `golang-mk1-<instance>-<difficulty>`.

## Fair visibility

It decides only on what a human sees: its own committed board and (for lookahead) the
pieces the game actually reveals (`GameMeta.next_count`, 0–6). It reads the meta seed only
to generate its OWN piece sequence, which every peer must do. No board state beyond the
revealed preview is ever consulted. The horizon is the game's, not the agent's:
`--difficulty` only trims it (easy 0, medium 1, hard the whole preview) and can never
reach past it — a `next_count: 0` game gets one-piece planning at every level, and a meta
without the field counts as 0. `revealedPieces` (`planner.go`) is the single place the
planner's lookahead comes from, and `preview_test.go` checks every difficulty against
every preview size so no tuning can quietly plan on pieces its opponents can't see.

## Build & run

```sh
# build (a normal Go module — NOT part of the main repo's `go build ./...`)
go build -o golang-mk1 .

# resident: waits for invitations (any mode)
./golang-mk1 --server nats://localhost:4222

# ...or connect like the nats CLI: a named NATS context, or (bare) the selected one
./golang-mk1 --context my-context
./golang-mk1

# also auto-join open agent-allowed games; pick a strength
./golang-mk1 --auto-join --difficulty hard

# one game only / a specific game
./golang-mk1 --once
./golang-mk1 --join <gameID>

# host a game (agent seats open by default), play it, exit
./golang-mk1 --create --players 2 --once
./golang-mk1 --create --mode cooperative --players 2 --once
./golang-mk1 --create --mode teams --players 2 --once     # 2v2 (--players is per team)

# offline conformance checks (RNG + split-deal parity with the game, planner sanity)
./golang-mk1 --selftest
```

Flags: `--server` (overrides `--context`; `--user`/`--password` go with it), `--context`
(a NATS context; default: the selected one), `--name` (version stem, default
`golang-mk1`), `--difficulty` (`easy`/`medium`/`hard`), `--join`, `--create` (with
`--mode`, `--players`, `--max-agents`, `--next`, `--holes` — holes per garbage row, 0-4,
written to the meta as `garbage_holes` — `--random-holes`, each garbage row drawing
its own columns, `random_garbage_holes` — `--guideline-garbage`, the
Guideline attack table (0/1/2/4 for plain clears, the T-spin rows, the Back-to-Back
and perfect-clear bonuses), `guideline_garbage` — `--hold`, the Guideline hold queue,
`hold`, which the agent itself never uses but the humans in its game may —
`--split-pieces`, the teams-mode piece split, `split_pieces`: the seven types
dealt out between the teammates, each seat playing only its own ration (a teams
game of two or more per team; ignored elsewhere) — and
`--guideline`, the GUI wizard's Guideline preset in one flag: next 6, hold, 1 hole
per garbage row, the Guideline attack table, overriding the individual rule flags),
`--publish` (`sync`/`async`/`optimistic` — how move batches are committed, default
`async`), `--auto-join`, `--wait`, `--once`, `--selftest`.

To watch it play, start a local server (`nats-server -js`, or the GUI's LAN mode), run the
GUI and create a game with agents allowed — or let one instance host for another:

```sh
./golang-mk1 --create --players 2 --once &
./golang-mk1 --auto-join --once
```

## Reading order

- `pieces.go` — tetromino geometry.
- `rng.go` — the PCG + 7-bag piece RNG, and the teams-mode piece split it deals
  when a game's meta says `split_pieces` (both bit-exact with the game).
- `engine.go` — the settled-board model (collision, drop, completed rows, collapse).
- `planner.go` — the Dellacherie evaluator, placement enumeration, lookahead, blunder model.
- `difficulty.go` — the per-difficulty knobs.
- `types.go` — wire payloads and the CAS-safe lobby/meta read-modify-write helpers.
- `agent.go` — the lobby: presence, watching, select/join/ready/countdown, CAS publishing.
- `game.go` — one game: the engine, consumers, garbage, line clears, outcome, archive.
- `scoring.go` — the Guideline scoring and garbage tables (a port of the GUI's
  `internal/game/scoring.go`): every lock scored like the GUI scores it, so the
  totals this agent announces fold into the same scoreboards.
- `pipeline.go` — the batch pipeline: the `--publish` disciplines (sync/async/optimistic),
  settle barriers, and the lost-batch repair.
- `shared.go` — shared boards: the coop/teams board consumer, merge-retry clears,
  deferred spawns, cascading garbage lifts, team verdicts.
- `main.go` — flags, signals, the selftest.

Every protocol interaction cites the section of the agent guide it implements.
