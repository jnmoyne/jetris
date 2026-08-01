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
  the opponents'/team boards, the roster, scores, the countdown. **Never** the game seed /
  next piece, and never protocol internals the UI doesn't render.
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

## The reference agent: `mk1`

The repository ships one agent, **`mk1`** (`cmd/jetris-agent`, source in
`internal/agent`), written in Go. Because it lives in the repo it reuses the game's own
engine code instead of re-implementing the protocol, so it is a *privileged* example, not a
template you must follow — but everything it does over the wire, your agent can do too. Use
it to play against while you develop:

```sh
# a resident opponent that joins agent-allowed games as they appear
go run ./cmd/jetris-agent --server nats://localhost:4222 --difficulty medium

# or have it host a game and wait for you
go run ./cmd/jetris-agent --server nats://localhost:4222 --create --players 2
```

It reads the same guide your agent does; see `jetris-agent-guide.md` §3 for how it is
built and §4 for the protocol your agent implements instead.
