# example-py — a minimal Jetris agent in Python

A worked example that the Jetris agent contract is really "any language, only NATS":
this agent depends on **nothing in the jetris repository**. It is a single Python file
that implements the wire protocol from [`../../jetris-agent-guide.md`](../../jetris-agent-guide.md)
(game rules from [`../../jetris-gameplays.md`](../../jetris-gameplays.md)) directly
against NATS/JetStream with the [`nats-py`](https://github.com/nats-io/nats.py) client,
and it follows the guide's fair-play rules.

## What it does

- Plays **competitive** mode. It sits in the lobby as a resident, auto-joins open
  competitive games that allow agents, and accepts invitations to competitive games
  (declining invitations to modes it cannot play).
- Carries every peer responsibility itself: presence heartbeat, the join CAS on the
  lobby KV, the roster announcement, the ready toggle and (when its toggle completes
  the set) the 5→0 countdown, its own engine — piece RNG (a bit-exact port of the
  game's PCG + 7-bag), spawning, gravity, lock-in, line clears, garbage application,
  top-out — plus shrink/game-over events, CAS-failure flashes, and, when it wins,
  the finish → archive → cleanup sequence.
- Publishes every board change as an **atomic CAS batch** to its cell subjects
  (active → locked → empty message ordering, per-subject expected-last-sequence,
  write-through), exactly as the guide specifies.
- Strategy is a naive one-ply greedy (`easy`): enumerate placements reachable with
  kick-free rotations and sideways moves, score the resulting board (lines, holes,
  height, bumpiness), pick the best. It plays under the name
  `example-py-<instance>-easy`.

## Run it

```sh
python3 -m venv venv && ./venv/bin/pip install -r requirements.txt

# resident: joins agent-allowed competitive games as they appear
./venv/bin/python agent.py --server nats://localhost:4222

# one game only / a specific game
./venv/bin/python agent.py --once
./venv/bin/python agent.py --join <gameID>

# offline conformance checks (RNG parity with the Go implementation, geometry)
./venv/bin/python agent.py --selftest
```

To watch it play, start a local server (`nats-server -js`, or the GUI's LAN mode),
run the GUI and create a competitive game with agents allowed — or let the reference
agent host one:

```sh
go run ../../cmd/jetris-agent --create --players 2 --max-agents 2 --once
```

## Reading order

`agent.py` is organized top-to-bottom as: identity & protocol constants → the piece
RNG port → the pure board model (collision, drop, clears, the placement heuristic) →
`Agent` (lobby: presence, watching, join/ready/countdown, atomic batch publishing) →
`Game` (one game: consumers, the engine, events, outcome, archive) → the play loop.
Every protocol interaction cites the section of the agent guide it implements.
