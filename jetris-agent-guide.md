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
nothing in this repository. The repo ships one Go agent, **`golang-mk1`**
(`agents/golang-mk1/`), itself built purely from this contract as a self-contained
module; §3 introduces it as a reference and sparring partner, and §4 is the contract
every agent — `golang-mk1` included — implements.
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
  so an agent's lookahead stops at the same horizon. (`golang-mk1`'s planner reads
  its allowance from the meta it already fetches and caps its lookahead there.)
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
  or you'll re-accept the same unsatisfiable invite forever. `golang-mk1`'s
  `agent.go` is a worked implementation of all of this.
- **Wait to be invited by default.** The reference resident agent
  (`golang-mk1`) only joins games it is invited to unless started with
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
  name has three parts, e.g. `golang-mk1-3f7a-hard`:
  - **version** — a stem naming your agent's CODE generation; bump it whenever
    your play logic changes, so rosters and game history record which version
    played (`golang-mk1` uses its codename).
  - **instance** — a short unique id (`golang-mk1` mints 4 random hex chars)
    generated fresh for every connection, so several copies of the same agent
    version can play at once and each connection is distinguishable.
  - **difficulty** — your strength label (`easy`/`medium`/`hard` for
    `golang-mk1`'s tunings, or your own).
  The name doubles as the NATS player ID and the presence KV key, so every
  component must use only `[-/_=.a-zA-Z0-9]` (no spaces, no parentheses) and
  the whole must fit 32 characters (`config.ValidatePlayerName`).

## 3. The reference agent `golang-mk1`

The repository's own agent, **`golang-mk1`** ([`agents/golang-mk1/`](agents/golang-mk1/)),
is exactly what this guide asks you to build: an independent module (its own `go.mod`,
depending only on NATS client libraries: `nats.go` plus the orbit `natscontext` and
`jetstreamext` helpers —
nothing from the game's packages) that
implements the §4 wire contract directly. It has **no privileged access** — everything
it does over the wire, your agent in any language can do too, and every protocol
interaction in its source cites the section of this guide it implements, so it doubles
as a worked, conformant reading of the contract.

What it covers, beyond the minimal Python example (`agents/example-python/`): the El-Tetris
**Dellacherie** placement heuristic with beam-pruned lookahead over the game's revealed
preview, `easy`/`medium`/`hard` difficulty tunings (think/move pacing, a blunder model,
lookahead depth), resident/invitation behavior with `--auto-join`, and hosting — with
`--create` it creates the game stream, meta, and lobby listing itself and waits for
opponents. It plays **competitive** mode, declining invitations to modes it can't play.

```sh
cd agents/golang-mk1 && go build .
./golang-mk1 --server nats://localhost:4222 --difficulty medium --auto-join
```

Its reading order (see its [README](agents/golang-mk1/README.md)): `pieces.go` →
`rng.go` (the bit-exact PCG + 7-bag port) → `engine.go` → `planner.go` →
`difficulty.go` → `types.go` → `agent.go` (the lobby) → `game.go` (one game).

One behavior of its move pipeline worth copying: moves that lose a CAS race are
**dropped, not retried** — it re-observes committed state, resyncs, and re-plans
(§4.3), which is what keeps a contended board consistent.

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
| `JETRIS_GAME_<gameID>` | stream | the blackboard: `jetris.game.<gameID>.>`, memory storage, full game history retained (no per-subject cap), atomic publish + direct get enabled |
| `jetris.lobby.event.>` | core NATS subjects | transient lobby events (`game.created/joined/left`, `invite.sent/retracted/declined`) — no stream, subscribe live |

The last property is the heart of the design: the stream keeps only the latest
message per subject, so **the last message on each cell subject IS that cell's
current value** — the stream is simultaneously the event log, the current state,
and the real-time push fabric.

### 4.2 Game-stream subjects

| Subject | Payload | Notes |
|---------|---------|-------|
| `jetris.game.<id>.meta` | `GameMeta` JSON | lifecycle state machine; CAS on last subject sequence; `next_count` (0-4) is the piece-preview size — your lookahead allowance; `no_ghost` is a UI-only rule (the hard-drop ghost preview) agents can ignore |
| `jetris.game.<id>.roster.<player>` | `PlayerSummary` JSON | join announcement (competitive opponent discovery) |
| `jetris.game.<id>.countdown` | `{"seconds": N}` | 5..0 before start |
| `jetris.flash.<id>.<player>` | `{"pi","tm","c"}` | **core NATS** (not on the game stream): a player's transient CAS-failure flash, for spectators |
| `jetris.game.<id>.events.<kind>.<player>` | `GameEvent` JSON | per-KIND, per-SENDER event subjects (`line_clear`, `game_over`); consume with the `events.>` filter. Per-subject retention can only ever trim an OLDER event of the same kind from the same player — `line_clear` carries the sender's cumulative `total_score`/`total_lines` (fold deltas) plus `cleared_rows` (the cleared rows' pre-collapse indices — teammates on a shared board flash them), and each player publishes at most one `game_over`, so nothing meaningful is ever lost |
| `jetris.game.<id>.playfield.cell.<row>.<col>` | `Cell` JSON | cooperative shared board |
| `jetris.game.<id>.team.<t>.playfield.cell.<row>.<col>` | `Cell` JSON | teams boards (t = 0/1) |
| `jetris.game.<id>.player.<player>.playfield.cell.<row>.<col>` | `Cell` JSON | competitive private boards |
| `…player.<player>.playfield.garbage` / `…team.<t>.playfield.garbage` | `{"total": N, "by": idx}` | the board's GARBAGE register: cumulative garbage rows OWED, CAS-added by attackers (§4.4) |
| `…player.<player>.playfield.txn` / `…team.<t>.playfield.txn` | `{"applied": N, "op": …, "topped": […], "full": bool, "by": idx}` | the board's TXN register: rows APPLIED + the exactly-once gate every bulk transform CASes through (§4.4) |

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
- **Authoritative writes** (your lock-in, hard-drop landing) publish **without
  CAS**. Bulk transforms — your line-clear collapse and applying owed garbage —
  are GATED batches: cells NoCAS, exactly-once through the txn register (§4.4).
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
  all of it, and the `golang-mk1` reference (§3) is a working implementation to compare against.
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

### 4.4 Garbage: the ledger and the gated transform

Garbage is **not an event**. An attack is recorded durably in the victim
board's GARBAGE register and applied by the victim as a GATED transform:

- **Attacking (you cleared N lines).** For every victim board (competitive:
  each surviving opponent; teams: the opposing board), CAS-add the garbage
  register: read its last message (`{"total": T}` at sequence S, or 0/0 if
  never written), publish `{"total": T+N, "by": <yourPlayerIdx>}` with
  `Nats-Expected-Last-Subject-Sequence: S`, and on a CAS rejection refresh
  and re-add (bounded retries). Simultaneous attackers serialize on the
  expectation and the register converges to the exact sum — an attack can
  never be lost, trimmed, or double-counted.
- **Applying (your register grew).** Your deficit is `garbage.total −
  txn.applied`. Apply it as ONE atomic batch whose FIRST message is your txn
  register — `{"applied": <new total>, "op": "shrink", "by": <you>}` with a
  per-subject CAS expectation at the txn's last sequence (**the gate**) — and
  whose remaining messages are the changed cells with NO expectations. A
  stale gate atomically rejects the whole batch (nothing is stored): refetch,
  recompute the deficit (now possibly zero), and retry. The same gated shape
  is used for your line-clear collapse (`"op": "clear"`, `applied` restated
  unchanged), which is what stops a clear and a raise on the same board from
  clobbering each other.
- **The transform itself (cascade rules, gameplays §4/§5):** the settled
  stack shifts up N rows and N full-width permanent adversarial rows
  (`{"o":true,"t":1,"g":true,"pi":<causer>}`) fill the bottom. Every falling
  piece holds its position unless the risen stack overlaps it — then it lifts
  the MINIMUM rows that clear the conflict, cascading through any piece above
  it. A piece pushed off the top eliminates its owner (list it in the txn's
  `topped`); settled rows pushed past the top mean the board is full
  (`"full": true`) — competitive: you top out; teams: the whole board's
  players are out.
- **Raises override moves.** The gated batch's cells carry no expectations,
  so an in-flight move loses to a committed raise (the move's own CAS then
  fails against the risen board — drop it and re-plan, as with any lost CAS).

### 4.5 Reading state

Join mid-game by fetching the last message of every playfield subject —
all cells PLUS the two registers (batched direct get) — then start **ordered
consumers** from `max(seq)+1` over your board's `playfield.>` filter,
opponent/team `playfield.>` filters, the `events.>` filter, `meta`, and
`countdown`. If the fetched registers show `total − applied > 0`, apply the
deficit before playing: owed garbage survives your downtime. Events arrive on
one ordered stream — every peer sees the same order, which is how all peers
agree on eliminations and outcomes without a coordinator.

## 5. Lifecycle responsibilities (every seat, agent or human)

1. **Presence**: write `players.<name>` every 5s; delete it on exit. Carry a
   per-message TTL of 5 minutes on each write (the `Nats-TTL` header on a
   publish to `$KV.JETRIS_LOBBY.players.<name>` — the bucket's
   `LimitMarkerTTL` enables it) so your entry self-deletes if you die without
   the clean exit; stale entries (3× heartbeat) are also pruned by others.
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
   **archives**, in this order: transition `finished → archived` (CAS; one
   winner), publish the `ArchiveRecord` **immediately** (it is what every
   lobby's history shows — do not make it wait for anything below), archive
   the replay (step 6), then wait until **5 s have passed since the finish**
   (so every peer's consumer has received the final events) before deleting
   the game stream and the KV listing. If that's you, don't disconnect until
   it's done. The record's optional `chat` field preserves the game's
   conversation (the archive purges the game's messages from the chat stream,
   so copy them into the record FIRST — last 200 lines; the GUI's
   archived-game viewer replays them). Best-effort: a record without `chat`
   simply shows no conversation.
6. **Replay archive** (part of archiving, AFTER the record publish and BEFORE
   the stream deletion): copy the ENTIRE game stream into the ONE shared
   file-backed **`JETRIS_REPLAY`** stream so the GUI can replay the game
   later. Every finishing game gets a replay; it is KEPT while the game is in
   the *keep set* — the top 10 of its bucket (one bucket per (mode,
   with/without agent seats) pair, ranked by headline score (coop total /
   best team / best player), then shorter duration, newer finish, game ID) or
   the 25 most recently finished games overall (newer finish first, game ID
   on ties). Both are pure functions of the archive records (read them all
   off `JETRIS_ARCHIVE`; yours included, as you just published it), so every
   archiver cuts the identical set. FIRST `Purge` (filter
   `jetris.replay.<gameID>.>`) the replay of every game you can see a record
   for that is no longer in the keep set — one purge removes a game's copies
   and marker alike. THEN republish each message of your game under
   `jetris.replay.<gameID>.<original tail>` (the tail is the game-stream
   subject after `jetris.game.<gameID>.`): payload verbatim, the message's
   ORIGINAL stream timestamp in a **`Jetris-Ts`** header (integer nanoseconds
   since the epoch — the copy gets a copy-time stream timestamp, so the
   recorded pace lives in the header; an original-speed replay paces itself
   from it), and the original headers DROPPED (their CAS expectations
   reference the dying game stream). Publish a
   `jetris.replay.<gameID>.done` **marker** message LAST, only after every
   copy is acked — the marker's presence is what marks the replay complete
   and listable (stream info with subjects filter `jetris.replay.*.done`),
   and lobbies follow the markers with a consumer: a marker means "this
   replay is ready and the displaced ones are already gone", so keep the
   purge-then-copy-then-marker order. Best-effort: on any failure purge your
   own half-made copy and archive without a replay. `golang-mk1`'s
   `replay.go` is the reference implementation.
7. **Walk away cleanly**: if a game you joined never starts, remove yourself from
   the roster (CAS, pre-start only) and purge your roster announcement so you
   don't linger as a ghost seat.

## 6. Testing your agent

- **Local server**: `nats-server -js`, or the GUI's LAN mode (it prints the URL to
  share).
- **Against the reference agent**: run `golang-mk1` residents at any difficulty and
  create agent-allowed games — `cd agents/golang-mk1 && go build . && ./golang-mk1
  --server nats://localhost:4222 --difficulty medium --auto-join` — or `./golang-mk1
  --create --mode teams --players 2` to host. `golang-mk1` implements everything
  in this guide, so it is a conformant sparring partner.
- **Against humans**: run the GUI and either create an invite-only game and invite
  the agent by name (it accepts immediately), or create a game with "Allow agents"
  checked and a max-agents count for an `--auto-join` resident to find, or `--join`
  it in directly.
- **In Go**: `internal/testutil.StartServer` gives an embedded JetStream server
  for protocol experiments and tests.

## 7. Checklist

- [ ] Decisions use only UI-visible information (no seed; lookahead at most the game's `next_count` preview)
- [ ] `agent: true` on presence and roster entries
- [ ] `max_agents` honored inside the join CAS
- [ ] `invite_only` games joined only when invited (watch `invites.<name>.*`; accept = join + delete key, decline = rewrite with `declined: true`)
- [ ] Name is `<agent-name>-<instance>-<difficulty>`, KV-key-safe, ≤32 chars
- [ ] Moves published as atomic CAS batches; dropped moves re-planned, not retried
- [ ] CAS-failure flashes broadcast on `jetris.flash.<id>.<name>` (core NATS)
- [ ] Gravity, lock-in, clears, garbage, spawn rules implemented
- [ ] Attacks delivered by CAS-adding victims' garbage registers (never events)
- [ ] Clears and garbage applied as txn-gated batches; deficit reconciled on join
- [ ] Countdown run when your ready toggle completes the set
- [ ] Archive performed when you trigger the finish
- [ ] Presence deleted and seats freed on the way out
