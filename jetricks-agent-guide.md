# Jetricks Agent Guide — how to build a jetricks-playing agent

**Audience:** developers (and the coding agents working for them) who want to build
an autonomous agent that plays Jetricks alongside — and against — human players and
other agents.

Jetricks is a blackboard system: the playfield lives in a NATS JetStream stream that
every participant reads and writes. There is **no game server** and **no special
agent support** — a "human player" is just an agent with a keyboard and a window.
Your agent is a peer like any other: it joins through the same lobby, writes the
same cell subjects under the same CAS discipline, and carries the same lifecycle
responsibilities. Nothing in the protocol distinguishes silicon from carbon except
one honesty flag. Remember that there are 3 game modes (cooperative, competitive and teams) in Jetricks.

Companion documents (all at the repo root):

| Document | What it holds |
|----------|---------------|
| `jetricks-gameplays.md` | The authoritative game rules (modes, spawning, gravity, clears, garbage, lifecycle) |
| `jetricks-project-structure.md` | The full package/architecture reference, subject schemes, payload structs |
| `jetricks-implementation-plan.md` | Implementation details, CAS behavior tables, design decisions |
| This guide | The agent-specific contract and the two integration paths |

---

## 1. The fair-visibility contract (the one hard rule)

**An agent may base its decisions ONLY on information a human player can see in the
UI.** That is the entire fairness model — the blackboard happily hands any client
more than the UI shows, so this is a contract you must honor, not a mechanism the
server enforces.

An agent MAY use:

- Its own board's **committed** state — the same no-client-side-prediction view a
  human sees: a move is visible only after it round-trips through the stream.
- Its own falling piece (type, orientation, position).
- Opponents' boards (competitive) and both team boards (teams) — the UI renders
  them live for everyone.
- The roster, everyone's `agent` flags and structured names, eliminations,
  scores, levels, the countdown, chat, and its own measured input RTT.

An agent may NOT use:

- **`GameMeta.Seed` or the piece RNG.** The piece sequence is deterministic and any
  client can compute every future piece — but the UI shows a human **no next-piece
  preview**, so an agent must plan one piece at a time. (This is why the stock
  agent has no lookahead.)
- Stream internals the UI does not render: raw sequence numbers as game
  information, other players' in-flight publish timing, headers, or anything else
  observable only at the protocol layer.

When in doubt, ask: *could a human learn this by looking at the screen?* If not,
your agent doesn't get to know it either.

## 2. Announce yourself: the agent flag and the agent policy

Agents are first-class but visible:

- **Mark yourself as an agent.** Your presence entry (`players.<name>` in the
  `JETRICKS_LOBBY` KV bucket) and your roster entry both carry
  `"agent": true`. In Go this is `lobby.SetAgent(true)` before `Start`. The UI
  tags you `[agent]` everywhere.
- **Respect the per-game agent policy.** Every game listing carries
  `max_agents` — how many roster seats agents may take (`0` = agents may not
  join). `lobby.JoinGame` enforces it atomically inside its CAS loop
  (`ErrAgentsNotAllowed`, `ErrAgentSlotsFull`); if you implement joining yourself,
  you MUST perform the same check inside the same CAS update, or racing agents can
  over-fill a game.
- **Accept invitations.** A game may be `invite_only` (`GameListing.InviteOnly`,
  creator in `CreatorID`); such games are joined ONLY by the creator or by an
  invited player — never by scanning the games list. An invitation is a JSON record
  written to your KV mailbox `invites.<yourPlayerID>` (an `Invitation`: `game_id`,
  `from_id`, `from_name`, `mode`, `team`, `created_at`); you already watch the whole
  KV bucket, so it arrives live. A well-behaved agent treats a fresh invitation
  (younger than `config.InviteTTL`, two minutes) as its strongest join signal:
  join the named game (and, in teams, the named team), which is allowed even when
  `max_agents` is 0 — **the invitation IS the permission**. Delete your mailbox key
  when you join or decline (`lobby.RespondInvite` does this; the stock agent joins,
  which also consumes it). **If the join fails** (the invited team was full, the game
  filled first — `ErrTeamFull`/`ErrGameFull`), decline the invitation rather than
  retrying it, or you'll re-accept the same unsatisfiable invite forever. The Go
  `agent` package does all of this automatically.
- **Structured names: `<version>-<instance>-<difficulty>`.** An agent's player
  name has three parts, e.g. `mk1-3f7a-hard`:
  - **version** — a stem naming your agent's CODE generation; bump it whenever
    your play logic changes, so rosters and game history record which version
    played (the stock agent's is its `Codename`, currently `mk1`).
  - **instance** — a short unique id (the stock agent mints 4 random hex chars)
    generated fresh for every connection, so several copies of the same agent
    version can play at once and each connection is distinguishable.
  - **difficulty** — your strength label (`easy`/`medium`/`hard` for the stock
    tunings, or your own).
  The name doubles as the NATS player ID and the presence KV key, so every
  component must use only `[-/_=.a-zA-Z0-9]` (no spaces, no parentheses) and
  the whole must fit 32 characters (`config.ValidatePlayerName`).

## 3. Path A (recommended): reuse the Go packages

The repository's own agent is a reusable library. The entire lifecycle — connect,
lobby, policy-aware join, ready/countdown, play, archive, teardown — is one call:

```go
import (
    "context"
    "jetricks/internal/agent"
    "jetricks/internal/config"
)

res, err := agent.Run(ctx, agent.Config{
    NATS:       config.Config{NATSURL: "nats://localhost:4222"},
    Name:       "mybrain",              // version stem → plays as "mybrain-<instance>-hard"
    Difficulty: agent.DifficultyHard,
})
```

To keep the plumbing but replace the brain, use the layers directly:

- **Perception** — `engine.Playfield()` (deep copy; `ActivePieceForPlayer(idx)`),
  `OpponentSnapshots()`, `Score()`, `Level()`, `PieceIdx()`, `Mode()`,
  `IsEliminated(id)`, `GameOutcome()`; the lossy `Updates` channel for wake-ups.
- **Action** — exactly six moves: `MoveLeft`, `MoveRight`, `MoveDown`, `RotateCW`,
  `RotateCCW`, `HardDrop`. That is a human's entire vocabulary, and yours.
- **Planning helpers** — `agent.PlanPlacements` (placement enumeration honoring the
  board's collision rules via `agent.Rules`), `agent.ChoosePlacement` (blunder
  model), `agent.Execute` (sense–act move execution that survives CAS-dropped
  moves and mid-plan garbage).
- **Lobby** — `lobby.New` + `SetAgent(true)` + `Start`, `CreateGame`, `JoinGame`,
  `ToggleReady`, `UnjoinGame`, `StartGame`.

Two behaviors of the pipeline your brain must tolerate: moves are **dropped, not
retried**, when they lose a CAS race (re-observe and re-plan — `agent.Execute`
already does), and the engine's input buffer is 8 deep with silent overflow (pace
your dispatches; don't spam).

## 4. Path B: any language, straight to the wire

A non-Go agent re-implements the client. Read
`jetricks-project-structure.md` §4/§6/§9 and `jetricks-gameplays.md` first; this is
the orientation map.

### 4.1 Resources

| Resource | Kind | Purpose |
|----------|------|---------|
| `JETRICKS_LOBBY` | KV bucket | presence (`players.<name>`), game listings (`games.<gameID>`), invitations (`invites.<name>`) |
| `JETRICKS_LOBBY_CHAT` | stream | lobby chat (`jetricks.lobby.chat`) + per-game chat (`….game.<gameID>`) |
| `JETRICKS_ARCHIVE` | stream | finished-game records (`jetricks.archive`) |
| `JETRICKS_GAME_<gameID>` | stream | the blackboard: `jetricks.game.<gameID>.>`, memory storage, **MaxMsgsPerSubject: 1**, atomic publish + direct get enabled |

The last property is the heart of the design: the stream keeps only the latest
message per subject, so **the last message on each cell subject IS that cell's
current value** — the stream is simultaneously the event log, the current state,
and the real-time push fabric.

### 4.2 Game-stream subjects

| Subject | Payload | Notes |
|---------|---------|-------|
| `jetricks.game.<id>.meta` | `GameMeta` JSON | lifecycle state machine; CAS on last subject sequence |
| `jetricks.game.<id>.roster.<player>` | `PlayerSummary` JSON | join announcement (competitive opponent discovery) |
| `jetricks.game.<id>.countdown` | `{"seconds": N}` | 5..0 before start |
| `jetricks.flash.<id>.<player>` | `{"pi","tm","c"}` | **core NATS** (not on the game stream): a player's transient CAS-failure flash, for spectators |
| `jetricks.game.<id>.events` | `GameEvent` JSON | line clears, garbage ("shrink"), game over — ONE subject, so only the last event is retained; consume live, never rely on replay |
| `jetricks.game.<id>.playfield.cell.<row>.<col>` | `Cell` JSON | cooperative shared board |
| `jetricks.game.<id>.team.<t>.playfield.cell.<row>.<col>` | `Cell` JSON | teams boards (t = 0/1) |
| `jetricks.game.<id>.player.<player>.playfield.cell.<row>.<col>` | `Cell` JSON | competitive private boards |

`Cell` JSON (empty cell marshals to `{}`, the vacate payload):

```json
{"o": true,  "t": 2, "a": true, "r": 1, "ar": 2, "ac": 13, "pi": 1, "g": false}
```
`o` occupied · `t` piece type (0-6 = I,O,T,S,Z,J,L) · `a` active (falling) ·
`r` orientation 0-3 · `ar`/`ac` anchor row/col · `pi` owning player index ·
`g` permanent adversarial garbage.

### 4.3 The write discipline

- **A move is not a message saying "left".** You locally validate the move
  (collision rules in `jetricks-gameplays.md`; SRS kicks), project the changed
  cells, and publish them as ONE **atomic batch** with per-subject CAS
  (`Nats-Expected-Last-Subject-Sequence` = the last sequence you have seen for
  each cell). Order cells within the batch by their new content: active first,
  locked second, empties last.
- **Player moves that lose CAS are dropped** — never retried. Re-observe, re-plan.
  On a dropped move, **broadcast a CAS-failure flash** so spectators can see it (see
  below).
- **Authoritative writes** (your lock-in, hard-drop landing, line clear, applying
  an opponent's garbage to your own board) publish **without CAS**.
- **Write-through**: after a successful publish, apply the committed cells and
  their inferred sequences to your in-memory board immediately (batch messages get
  consecutive sequences ending at the commit ack); your own echo then no-ops via a
  strictly-higher-sequence rule.
- **You are the engine.** There is no server running the game for you: your agent
  must tick gravity (`jetricks-gameplays.md` §7), detect its own lock-in (your
  active-cell count reaching zero on the consumer), clear lines, publish events,
  apply incoming garbage, spawn its next piece (including the deferred-spawn rule
  when another player's falling piece covers your spawn cells), and detect
  top-out. This is the bulk of the work — weigh it against Path A.
- **Broadcast CAS-failure flashes.** When one of your writes loses its per-subject
  CAS and you drop the move, publish a **core NATS** message (NOT JetStream — this
  is transient UI feedback that must never be persisted or replayed) to
  `jetricks.flash.<gameID>.<yourPlayerID>` with payload
  `{"pi": <yourPlayerIdx>, "tm": <yourTeam>, "c": [[row,col], …]}` (the cells of
  the piece that didn't move). Spectators subscribe to `jetricks.flash.<gameID>.*`
  and render each player's flash on that player's board — a human client does
  exactly this, so an agent must too, or a spectator watching your board would miss
  your contention feedback that every other player's board shows. You do NOT
  subscribe to or render other players' flashes (players see only their own). The
  Go `agent`/`engine` packages do this automatically.

### 4.4 Reading state

Join mid-game by fetching the last message of every cell subject (batched direct
get), then start **ordered consumers** from `max(seq)+1` over your board filter,
opponent/team filters, `events`, `meta`, and `countdown`. Events arrive on one
ordered stream — every peer sees the same order, which is how all peers agree on
eliminations and outcomes without a coordinator.

## 5. Lifecycle responsibilities (every seat, agent or human)

1. **Presence**: write `players.<name>` every 5s; delete it on exit. Stale entries
   (3× heartbeat) are pruned by others.
2. **Join**: CAS-update the `games.<gameID>` listing (append your `PlayerSummary`
   with `"agent": true`, honoring `max_agents` and, in teams, per-team capacity);
   after the CAS commits, publish your roster entry.
3. **Ready → countdown**: toggle your `ready` flag via CAS. **If your toggle is
   the one that completes the ready set, YOU run the countdown**: publish
   `{"seconds": 5..0}` at 1s intervals, pause ~700ms, then CAS the meta to
   `in_progress`. Skip this and the game never starts.
4. **Play** by the mode rules (`jetricks-gameplays.md` §3–§5). Never touch cells
   that aren't yours to change.
5. **Finish**: competitive's last player standing, any winning teams player, or
   the cooperative topper CAS-transitions the meta to `finished` — and then
   **archives**: transition `finished → archived` (CAS; one winner), publish the
   `ArchiveRecord`, delete the game stream and the KV listing. If that's you,
   don't disconnect until it's done.
6. **Walk away cleanly**: if a game you joined never starts, remove yourself from
   the roster (CAS, pre-start only) and purge your roster announcement so you
   don't linger as a ghost seat.

## 6. Testing your agent

- **Local server**: `nats-server -js`, or the GUI's LAN mode (it prints the URL to
  share).
- **Against the stock agent**: run `jetricks-agent` residents at any difficulty
  and create agent-allowed games; or `jetricks-agent --create --mode teams
  --players 2` to host. The stock agent is the reference implementation of
  everything in this guide.
- **Against humans**: run the GUI, create a game with "Allow agents" checked and a
  max-agents count; your agent auto-joins (if it's a resident) or `--join`s.
- **In Go**: `internal/testutil.StartServer` gives an embedded JetStream server;
  `internal/agent`'s integration tests show full agent-vs-agent games in ~15s.

## 7. Checklist

- [ ] Decisions use only UI-visible information (no seed, no next-piece lookahead)
- [ ] `agent: true` on presence and roster entries
- [ ] `max_agents` honored inside the join CAS
- [ ] `invite_only` games joined only when invited (watch `invites.<name>`)
- [ ] Name is `<name>-<strength>`, KV-key-safe, ≤32 chars
- [ ] Moves published as atomic CAS batches; dropped moves re-planned, not retried
- [ ] CAS-failure flashes broadcast on `jetricks.flash.<id>.<name>` (core NATS)
- [ ] Gravity, lock-in, clears, garbage, spawn rules implemented (or Path A reused)
- [ ] Countdown run when your ready toggle completes the set
- [ ] Archive performed when you trigger the finish
- [ ] Presence deleted and seats freed on the way out
