# golang-mk1 — a self-contained Jetris agent in Go

A Go sibling of [`example-python`](../example-python/): it depends on **nothing in the
jetris repository** (its own `go.mod`; its only dependencies are the
[`nats.go`](https://github.com/nats-io/nats.go) client and the orbit
[`natscontext`](https://github.com/synadia-io/orbit.go) helper for NATS-CLI-compatible
contexts) and implements the wire protocol
from [`../../jetris-agent-guide.md`](../../jetris-agent-guide.md) — game rules from
[`../../jetris-gameplays.md`](../../jetris-gameplays.md) — directly against NATS/JetStream,
following the guide's fair-play rules. Unlike the example, it plays with the repo agent's
strong **Dellacherie** brain and its `easy`/`medium`/`hard` difficulties.

## What it does

- Plays **competitive** mode. It sits in the lobby as a resident, accepts invitations to
  competitive games (declining modes it can't play), and with `--auto-join` also joins
  open competitive games that allow agents. With `--create` it **hosts**: it creates the
  game stream + meta + lobby listing itself (agent seats open by default), joins its own
  game, and waits for opponents.
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
- **Strategy** is the El-Tetris one-ply Dellacherie heuristic (landing height, eroded cells,
  row/column transitions, holes, cumulative wells) with beam-pruned lookahead over the
  game's revealed piece preview — the same evaluator the in-repo reference agent uses.
  Difficulty tunes think/move pacing, a blunder model, and lookahead depth. It plays under
  the name `golang-mk1-<instance>-<difficulty>`.

## Fair visibility

It decides only on what a human sees: its own committed board and (for lookahead) the
pieces the game actually reveals (`GameMeta.next_count`, 0–4). It reads the meta seed only
to generate its OWN piece sequence, which every peer must do. No board state beyond the
revealed preview is ever consulted.

## Build & run

```sh
# build (a normal Go module — NOT part of the main repo's `go build ./...`)
go build -o golang-mk1 .

# resident: waits for invitations to competitive games
./golang-mk1 --server nats://localhost:4222

# ...or connect like the nats CLI: a named NATS context, or (bare) the selected one
./golang-mk1 --context my-context
./golang-mk1

# also auto-join open agent-allowed games; pick a strength
./golang-mk1 --auto-join --difficulty hard

# one game only / a specific game
./golang-mk1 --once
./golang-mk1 --join <gameID>

# host a competitive game (agent seats open by default), play it, exit
./golang-mk1 --create --players 2 --once

# offline conformance checks (RNG parity with the game, planner sanity)
./golang-mk1 --selftest
```

Flags: `--server` (overrides `--context`; `--user`/`--password` go with it), `--context`
(a NATS context; default: the selected one), `--name` (version stem, default
`golang-mk1`), `--difficulty` (`easy`/`medium`/`hard`), `--join`, `--create` (with
`--players`, `--max-agents`, `--next`), `--auto-join`, `--wait`, `--once`, `--selftest`.

To watch it play, start a local server (`nats-server -js`, or the GUI's LAN mode), run the
GUI and create a competitive game with agents allowed — or let one instance host for another:

```sh
./golang-mk1 --create --players 2 --once &
./golang-mk1 --auto-join --once
```

## Reading order

- `pieces.go` — tetromino geometry.
- `rng.go` — the PCG + 7-bag piece RNG (bit-exact with the game).
- `engine.go` — the settled-board model (collision, drop, completed rows, collapse).
- `planner.go` — the Dellacherie evaluator, placement enumeration, lookahead, blunder model.
- `difficulty.go` — the per-difficulty knobs.
- `types.go` — wire payloads and the CAS-safe lobby/meta read-modify-write helpers.
- `agent.go` — the lobby: presence, watching, select/join/ready/countdown, CAS publishing.
- `game.go` — one game: the engine, consumers, garbage, line clears, outcome, archive.
- `main.go` — flags, signals, the selftest.

Every protocol interaction cites the section of the agent guide it implements.
