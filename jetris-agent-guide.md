# Jetris Agent Guide — how to build a jetris-playing agent

**Audience:** developers (and the coding agents working for them) who want to build
an autonomous agent that plays Jetris alongside — and against — human players and
other agents.

Jetris is a blackboard system: the playfield lives in a NATS JetStream stream that
every participant reads and writes. There is **no game server** and **no special
agent support** — a "human player" is just an agent with a keyboard and a window.
Your agent is a peer like any other: it joins through the same lobby, writes the
same cell subjects under the same CAS discipline, and carries the same lifecycle
responsibilities. Nothing in the protocol distinguishes silicon from carbon except
one honesty flag. Remember that there are 3 game modes (cooperative, competitive and teams) in Jetris.

**The one and only interface is the game itself — the NATS server, the JetStream
blackboard, and the fair-play rules below.** There is no framework to plug into and no
interface to implement: your agent is a standalone program, in **any language**, that
connects to NATS and plays by these rules. This guide plus `jetris-gameplays.md` are
the complete contract — read them and you can build a conformant agent depending on
nothing in this repository. The repo ships one Go agent, **`mk1`**, purely as a
reference/opponent; §3 explains how it (uniquely, because it lives in the repo) reuses
the game's own engine code, while §4 is the real contract every other agent implements.
To contribute an agent, see [`agents/README.md`](agents/README.md).

Companion documents (all at the repo root):

| Document | What it holds |
|----------|---------------|
| `jetris-gameplays.md` | The authoritative game rules (modes, spawning, gravity, clears, garbage, lifecycle) |
| `jetris-project-structure.md` | The full package/architecture reference, subject schemes, payload structs |
| `jetris-implementation-plan.md` | Implementation details, CAS behavior tables, design decisions |
| This guide | The authoritative wire contract + fair-play rules every agent implements |
| `agents/README.md` | How to contribute your own agent to the repo |

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
- **The game's piece preview**: the next `GameMeta.NextCount` pieces of its own
  sequence (`seq.Piece(pieceIdx+1 .. +NextCount)`). That is exactly what the UI's
  NEXT well shows a human, so an agent may plan with it — and no further.
- Opponents' boards (competitive) and both team boards (teams) — the UI renders
  them live for everyone.
- The roster, everyone's `agent` flags and structured names, eliminations,
  scores, levels, the countdown, chat, and its own measured input RTT.

An agent may NOT use:

- **`GameMeta.Seed` or the piece RNG beyond the game's preview.** The piece
  sequence is deterministic and any client can compute every future piece — but
  the UI shows a human exactly `NextCount` upcoming pieces (none when it is 0),
  so an agent's lookahead stops at the same horizon. (`mk1`'s planner reads its
  allowance from the meta it already fetches and caps its lookahead there.)
- Stream internals the UI does not render: raw sequence numbers as game
  information, other players' in-flight publish timing, headers, or anything else
  observable only at the protocol layer.

When in doubt, ask: *could a human learn this by looking at the screen?* If not,
your agent doesn't get to know it either.

## 2. Announce yourself: the agent flag and the agent policy

Agents are first-class but visible:

- **Mark yourself as an agent.** Your presence entry (`players.<name>` in the
  `JETRIS_LOBBY` KV bucket) and your roster entry both carry
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
  written to your per-game KV mailbox key `invites.<yourPlayerID>.<gameID>` (an
  `Invitation`: `game_id`, `invitee_id`, `from_id`, `from_name`, `mode`, `team`,
  `declined`, `created_at`); a player may hold invitations to SEVERAL games at
  once, one key each, and you already watch the whole KV bucket, so they arrive
  live. A well-behaved agent treats a fresh invitation (younger than
  `config.InviteTTL`, two minutes, and not marked `declined`) as its strongest
  join signal: join the named game (and, in teams, the named team), which is
  allowed even when `max_agents` is 0 — **the invitation IS the permission**.
  The key's lifecycle is the answer the inviter watches for:
  - **accept** = join the game and DELETE the key (`lobby.JoinGame` consumes it);
  - **decline** = REWRITE the key with `"declined": true` (`lobby.DeclineInvite`)
    so the inviter sees the refusal — do NOT delete it;
  - a **stale** invitation whose game no longer exists is simply deleted
    (`lobby.DismissInvite`);
  - the INVITER may delete the key at any time (retraction / dismissing a
    decline — `lobby.Uninvite`): a pending invitation can vanish, so re-check
    before acting on one.
  **If the join fails** (the invited team was full, the game filled first —
  `ErrTeamFull`/`ErrGameFull`), decline the invitation rather than retrying it,
  or you'll re-accept the same unsatisfiable invite forever. The Go `agent`
  package does all of this automatically.
- **Wait to be invited by default.** The reference resident agent
  (`jetris-agent`) only joins games it is invited to unless started with
  `--auto-join`, which restores active scanning for open agent-allowed games.
  Third-party resident agents should offer the same choice (the Python example's
  `--auto-join` mirrors it) so a lobby full of idle agents stays quiet until
  someone asks them to play.
- **Listen for lobby events (optional but recommended).** Every lobby action is
  also announced as a transient CORE NATS message (no stream captures them) on
  `jetris.lobby.event.<kind>` with kinds `game.created`, `game.joined`,
  `game.left`, `invite.sent`, `invite.retracted`, `invite.declined` — payload
  `{kind, game_id, player_id, target_id?, team?, time}`. State still lives in
  the KV; the events are low-latency pings that let you react (e.g. to a fresh
  invitation or a seat opening up) without polling.
- **Structured names: `<version>-<instance>-<difficulty>`.** An agent's player
  name has three parts, e.g. `mk1-3f7a-hard`:
  - **version** — a stem naming your agent's CODE generation; bump it whenever
    your play logic changes, so rosters and game history record which version
    played (`mk1` uses its `Codename`).
  - **instance** — a short unique id (`mk1` mints 4 random hex chars)
    generated fresh for every connection, so several copies of the same agent
    version can play at once and each connection is distinguishable.
  - **difficulty** — your strength label (`easy`/`medium`/`hard` for `mk1`'s
    tunings, or your own).
  The name doubles as the NATS player ID and the presence KV key, so every
  component must use only `[-/_=.a-zA-Z0-9]` (no spaces, no parentheses) and
  the whole must fit 32 characters (`config.ValidatePlayerName`).

## 3. The reference agent `mk1` (in-repo Go only)

The repository's own agent, `mk1` (`cmd/jetris-agent`, source in `internal/agent`),
is a **privileged example, not a framework**. Because it lives inside the repo it can
reuse the game's own Go engine packages (`internal/engine`, `internal/lobby`, …) instead
of re-implementing the protocol — a convenience no third-party agent gets. Your agent —
in any language — implements the wire protocol in §4 instead. This section is here only
so you can read how the reference does it and run it as an opponent.

For an **in-repo Go** agent, the entire lifecycle — connect, lobby, policy-aware join,
ready/countdown, play, archive, teardown — is one call:

```go
import (
    "context"
    "jetris/internal/agent"
    "jetris/internal/config"
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

## 4. The contract: play the protocol (any language)

**This is what every agent except the in-repo reference does** — implement the client
against the wire, in whatever language you like, depending on nothing in this repo. Read
`jetris-project-structure.md` §4/§6/§9 and `jetris-gameplays.md` first; this is the
orientation map. Everything below, plus the fair-play rules in §1–§2 and the lifecycle in
§5, is the complete contract. A complete worked example of this path is
`agents/example-python/` — a single-file Python agent (competitive mode) built from this
guide alone, including a bit-exact port of the piece RNG and the atomic-batch write
discipline.

### 4.1 Resources

| Resource | Kind | Purpose |
|----------|------|---------|
| `JETRIS_LOBBY` | KV bucket | presence (`players.<name>`), game listings (`games.<gameID>`), invitations (`invites.<name>.<gameID>`, one per invited game) |
| `JETRIS_CHAT` | stream | all chat on `jetris.chat.<gameID>`; the lobby chat uses the reserved game ID `lobby` |
| `JETRIS_ARCHIVE` | stream | finished-game records (`jetris.archive`) |
| `JETRIS_GAME_<gameID>` | stream | the blackboard: `jetris.game.<gameID>.>`, memory storage, **MaxMsgsPerSubject: 1**, atomic publish + direct get enabled |
| `jetris.lobby.event.>` | core NATS subjects | transient lobby events (`game.created/joined/left`, `invite.sent/retracted/declined`) — no stream, subscribe live |

The last property is the heart of the design: the stream keeps only the latest
message per subject, so **the last message on each cell subject IS that cell's
current value** — the stream is simultaneously the event log, the current state,
and the real-time push fabric.

### 4.2 Game-stream subjects

| Subject | Payload | Notes |
|---------|---------|-------|
| `jetris.game.<id>.meta` | `GameMeta` JSON | lifecycle state machine; CAS on last subject sequence; `next_count` (0-4) is the piece-preview size — your lookahead allowance |
| `jetris.game.<id>.roster.<player>` | `PlayerSummary` JSON | join announcement (competitive opponent discovery) |
| `jetris.game.<id>.countdown` | `{"seconds": N}` | 5..0 before start |
| `jetris.flash.<id>.<player>` | `{"pi","tm","c"}` | **core NATS** (not on the game stream): a player's transient CAS-failure flash, for spectators |
| `jetris.game.<id>.events` | `GameEvent` JSON | line clears, garbage ("shrink"), game over — ONE subject, so only the last event is retained; consume live, never rely on replay |
| `jetris.game.<id>.playfield.cell.<row>.<col>` | `Cell` JSON | cooperative shared board |
| `jetris.game.<id>.team.<t>.playfield.cell.<row>.<col>` | `Cell` JSON | teams boards (t = 0/1) |
| `jetris.game.<id>.player.<player>.playfield.cell.<row>.<col>` | `Cell` JSON | competitive private boards |

`Cell` JSON (empty cell marshals to `{}`, the vacate payload):

```json
{"o": true,  "t": 2, "a": true, "r": 1, "ar": 2, "ac": 13, "pi": 1, "g": false}
```
`o` occupied · `t` piece type (0-6 = I,O,T,S,Z,J,L) · `a` active (falling) ·
`r` orientation 0-3 · `ar`/`ac` anchor row/col · `pi` owning player index ·
`g` permanent adversarial garbage.

### 4.3 The write discipline

- **A move is not a message saying "left".** You locally validate the move
  (collision rules in `jetris-gameplays.md`; SRS kicks), project the changed
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
  must tick gravity (`jetris-gameplays.md` §7), detect its own lock-in (your
  active-cell count reaching zero on the consumer), clear lines, publish events,
  apply incoming garbage, spawn its next piece (including the deferred-spawn rule
  when another player's falling piece covers your spawn cells), and detect
  top-out. This is the bulk of the work; `jetris-gameplays.md` is the spec for
  all of it, and the `mk1` reference (§3) is a working implementation to compare against.
- **Broadcast CAS-failure flashes.** When one of your writes loses its per-subject
  CAS and you drop the move, publish a **core NATS** message (NOT JetStream — this
  is transient UI feedback that must never be persisted or replayed) to
  `jetris.flash.<gameID>.<yourPlayerID>` with payload
  `{"pi": <yourPlayerIdx>, "tm": <yourTeam>, "c": [[row,col], …]}` (the cells of
  the piece that didn't move). Spectators subscribe to `jetris.flash.<gameID>.*`
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
4. **Play** by the mode rules (`jetris-gameplays.md` §3–§5). Never touch cells
   that aren't yours to change.
5. **Finish**: competitive's last player standing, any winning teams player, or
   the cooperative topper CAS-transitions the meta to `finished` — and then
   **archives**: transition `finished → archived` (CAS; one winner), publish the
   `ArchiveRecord`, delete the game stream and the KV listing. If that's you,
   don't disconnect until it's done. The record's optional `chat` field preserves
   the game's conversation (the archive purges the game's messages from the chat
   stream, so copy them into the record FIRST — last 200 lines; the GUI's
   archived-game viewer replays them). Best-effort: a record without `chat`
   simply shows no conversation.
6. **Walk away cleanly**: if a game you joined never starts, remove yourself from
   the roster (CAS, pre-start only) and purge your roster announcement so you
   don't linger as a ghost seat.

## 6. Testing your agent

- **Local server**: `nats-server -js`, or the GUI's LAN mode (it prints the URL to
  share).
- **Against the reference agent**: run `mk1` residents at any difficulty and create
  agent-allowed games — `go run ./cmd/jetris-agent --server nats://localhost:4222
  --difficulty medium` — or `... --create --mode teams --players 2` to host. `mk1`
  implements everything in this guide, so it is a conformant sparring partner.
- **Against humans**: run the GUI and either create an invite-only game and invite
  the agent by name (it accepts immediately), or create a game with "Allow agents"
  checked and a max-agents count for an `--auto-join` resident to find, or `--join`
  it in directly.
- **In Go**: `internal/testutil.StartServer` gives an embedded JetStream server;
  `internal/agent`'s integration tests show full agent-vs-agent games in ~15s.

## 7. Checklist

- [ ] Decisions use only UI-visible information (no seed; lookahead at most the game's `next_count` preview)
- [ ] `agent: true` on presence and roster entries
- [ ] `max_agents` honored inside the join CAS
- [ ] `invite_only` games joined only when invited (watch `invites.<name>.*`; accept = join + delete key, decline = rewrite with `declined: true`)
- [ ] Name is `<agent-name>-<instance>-<difficulty>`, KV-key-safe, ≤32 chars
- [ ] Moves published as atomic CAS batches; dropped moves re-planned, not retried
- [ ] CAS-failure flashes broadcast on `jetris.flash.<id>.<name>` (core NATS)
- [ ] Gravity, lock-in, clears, garbage, spawn rules implemented
- [ ] Countdown run when your ready toggle completes the set
- [ ] Archive performed when you trigger the finish
- [ ] Presence deleted and seats freed on the way out
