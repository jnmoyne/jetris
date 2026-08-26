# Jetris agents

This directory is the home for **contributed agents** — programs that play Jetris on
their own. Write yours in **any language** and open a pull request adding it here.

## The only contract is the game

There is no framework to plug into and no interface to implement. An agent is just a
program that connects to the same NATS server as everyone else and plays by the rules. The
complete, authoritative contract is two documents at the repo root:

- **[`../jetris-agent-guide.md`](../jetris-agent-guide.md)** — the wire protocol (the
  NATS subjects, JetStream streams, KV keys, message payloads, and the CAS write
  discipline) plus the fair-play rules every agent must follow.
- **[`../jetris-gameplays.md`](../jetris-gameplays.md)** — the game rules (modes,
  spawning, gravity, line clears, garbage, lifecycle) that your agent's own logic
  implements.

Read those two and you can build a conformant agent in any language, depending on **nothing
in this repository**. The game does not know or care how any agent is built — only that it
speaks the protocol and follows the rules.

## The rules, in brief (the guide is the full text)

- **Fair visibility.** Decide only on what a human sees in the UI: your committed board,
  the opponents'/team boards, the roster, scores, the countdown, and the game's piece
  preview (`GameMeta.next_count` upcoming pieces, 0-4 — your lookahead stops there).
  The horizon is the game's setting, read from its meta (absent = 0): a difficulty
  or flag of yours may use less of it, never more. **Never** the game seed beyond
  that horizon, and never protocol internals the UI doesn't render.
- **Garbage has a per-game shape.** The meta's `garbage_holes` (0-4, absent = 0) is how
  many empty cells every garbage row you raise on your own board is punched with (one
  random column set per raise, or one per row when `random_garbage_holes` is true). A
  solid garbage row never clears; a holed one clears like
  any other line once its holes are filled — your completed-row scan is "all settled and
  some non-garbage cell", not "no garbage cell".
- **Attack by the game's rule.** A clear owes one garbage row per line, unless the meta's
  `guideline_garbage` is true — then the Guideline table: a single owes nothing (touch no
  register), a double 1 row, a triple 2, a Tetris 4.
- **Identify as an agent.** Set `agent: true` on your presence and roster entries; honor
  each game's `max_agents` policy and `invite_only` restriction; accept invitations from
  your per-game `invites.<name>.<gameID>` KV mailbox keys (accept = join + delete the
  key; decline = rewrite it with `declined: true`). Residents wait to be invited by
  default — offer an `--auto-join` style opt-in for active scanning.
- **Name yourself `<agent-name>-<instance>-<difficulty>`**, e.g.
  `claude-fable-5-xhigh-3f7a-hard`: a codename for your agent's version/build, a fresh
  random instance id per connection (so several copies coexist), and a difficulty label.
  Keep the name ≤ 32 characters using only `-`, `/`, `_`, `=`, `.` and alphanumerics (it is
  a NATS subject token *and* a KV key). Bump the codename when your play logic changes so
  game history records which version played.
- **Broadcast your CAS-failure flashes** on `jetris.flash.<game>.<name>` (core NATS, not
  the stream) so spectators see the same contention feedback a human's board shows.
- **Carry your lifecycle weight**: presence heartbeat, join via CAS, run the 5→0 countdown
  if your ready toggle completes the set, archive the game if you trigger its finish, and
  leave cleanly (free your seat, delete your presence).

## Adding your agent

1. Create `agents/<your-agent-name>/` — **self-contained**, in whatever language you like,
   with its own build and dependencies (a nested Go module with its own `go.mod`, a Python
   venv, a Rust crate, a Node package, …). It must be independently buildable and must not
   need to be part of the main repo's `go build ./...`, so heavy dependencies (LLM API
   clients, ML runtimes) never touch the game or the reference agent.
2. Add `agents/<your-agent-name>/README.md` stating what it is, the language, how to build
   and run it, and confirming it follows `jetris-agent-guide.md`.
3. Open a pull request.

## The worked example: `example-py`

[`example-python/`](example-python/) is a complete minimal agent written in Python that
depends on nothing in this repository — a single file implementing the wire protocol
(lobby KV CAS, atomic cell batches, its own engine, events, archive) straight against
NATS. It is the proof of the "any language, only NATS" contract and a good starting
point to copy: see its [README](example-python/README.md).

## The reference agent: `golang-mk1`

[`golang-mk1/`](golang-mk1/) is the repo's own agent and the same idea in Go: an
independent module (its own `go.mod`, depending only on NATS client libraries — `nats.go`
plus the orbit `natscontext` helper — **not** on this repository) that implements the
wire protocol straight against NATS/JetStream. It
goes beyond the minimal example by playing with the El-Tetris **Dellacherie** heuristic
and `easy`/`medium`/`hard` difficulties, so it is both a template for a "real language"
agent and a strong opponent — and it plays all three game modes. Use it to play against while you develop:

```sh
cd golang-mk1 && go build .

# a resident opponent that joins agent-allowed games as they appear
./golang-mk1 --server nats://localhost:4222 --difficulty medium --auto-join

# or have it host a game and wait for you
./golang-mk1 --server nats://localhost:4222 --create --players 2
```

It is built solely from the same two documents your agent uses — every protocol
interaction in its source cites the guide section it implements — so it doubles as a
worked, conformant reading of the contract: see its [README](golang-mk1/README.md).
