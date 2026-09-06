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

- Its own board's **committed** state, and its own optimistic projection of it —
  the committed cells with the agent's OWN in-flight writes applied on top. The
  human client draws exactly that projection in its optimistic publish mode, so
  an agent may plan from it too. What nobody gets is certainty: a projection can
  be rolled back by a lost CAS race, and the recovery discipline in §4.3 applies
  to agents exactly as it does to the human client.
- Its own falling piece (type, orientation, position).
- **The game's piece preview**: the next `GameMeta.NextCount` pieces of its own
  sequence (`seq.Piece(pieceIdx+1 .. +NextCount)`). That is exactly what the UI's
  NEXT well shows a human, so an agent may plan with it — and no further. In a
  teams game with `split_pieces` (below) "its own sequence" is the seat's
  RATION, so the preview reveals only the types that seat holds.
- Opponents' boards (competitive) and both team boards (teams) — the UI renders
  them live for everyone.
- The roster, everyone's `agent` flags and structured names, eliminations,
  scores, levels, the countdown, chat, and its own measured input RTT.

An agent may NOT use:

- **`GameMeta.Seed` or the piece RNG beyond the game's preview.** The piece
  sequence is deterministic and any client can compute every future piece — but
  the UI shows a human exactly `NextCount` upcoming pieces (none when it is 0),
  so an agent's lookahead stops at the same horizon. See §1.1: the horizon is the
  game's setting, and nothing on the agent's side may raise it.
- Stream internals the UI does not render: raw sequence numbers as game
  information, other players' in-flight publish timing, headers, or anything else
  observable only at the protocol layer.

When in doubt, ask: *could a human learn this by looking at the screen?* If not,
your agent doesn't get to know it either.

### 1.1 The preview horizon is the game's setting, not yours

How far ahead anyone in a game may see is decided once, by whoever creates it:
`next_count` in the game's meta record (0-6, fixed for the life of the game, the
number the lobby row advertises as `next N`). It binds every seat equally —
humans and agents — and an agent MUST respect it:

- **Read it from the meta of the game you are in**, every game, and plan with at
  most that many upcoming pieces. A meta without the field (a game created before
  the attribute existed) means **0**: no preview, no lookahead. Never substitute
  the create wizard's default of 1, or any default of your own.
- **Nothing on your side may raise it.** A difficulty level, a command-line flag, a
  configuration default or a "hard" mode can only use *less* of the preview than
  the game reveals, never more. `golang-mk1`'s `hard` asks for the full 4-piece
  preview and still plans one piece at a time in a `next_count: 0` game.
- **Hosting changes nothing.** An agent that creates a game writes `next_count`
  into the meta like any host — and then plays by that same number.
- **The seed is not a loophole.** You must read `seed` to generate your own piece
  sequence, as every peer does, but `seq.Piece(pieceIdx + next_count)` is the
  last index you may evaluate while a piece is in play. Computing further is
  cheating even if you "only use it a little".

### 1.2 Split pieces: your seat's ration (teams)

A teams game whose meta carries `split_pieces: true` (and `team_size` > 1) does
not give every seat the same 7-bag: the seven types are **dealt out between the
teammates**, and your seat's sequence is a bag of its own ration alone. You must
compute the same deal every other peer does, or you will spawn pieces your
board's other clients will not accept:

1. **Deal** the seven types among `team_size` seats off `seed`, and take the set
   at YOUR `team_slot` (the roster's `team_slot`, the same number that picks your
   spawn section). Every type goes to somebody and no seat is left empty-handed;
   every team's slot N gets the same ration, which is what keeps the match fair.
2. **Draw** your pieces from that ration: index `i` is position `i % k` of a
   Fisher-Yates shuffle of the ration (`k` = its size, so bag `i / k`), seeded by
   PCG(mix(`seed`, ration), bag) — the game's seed mixed with the ration's 7-bit
   piece mask through splitmix64.

`internal/rng` (`PieceSets`, `NewSet`) is the original; `agents/golang-mk1/rng.go`
is the bit-exact port, pinned against it by `TestSplitParity` and by
`--selftest`. A game without the field is the ordinary 7-bag, unchanged. The
rule is the gameplays doc's §5 "Split pieces".

The reference agent pins this with a test (`agents/golang-mk1/preview_test.go`:
every difficulty against every preview size, plus the absent-field case). Do the
same in yours — it is the easiest rule in this guide to break by accident while
tuning a planner.

### 1.3 The bag: how your sequence is dealt

The meta's `bag` (absent by default) is the game's randomizer — the way every
seat's sequence is dealt off `seed` — and you must deal yours the same way, or
you will spawn pieces no other peer expects (gameplays §1b):

- absent (or `""`) — the **standard 7-bag**: index `i` is position `i % 7` of a
  Fisher-Yates shuffle of the seven types seeded by PCG(`seed`, `i / 7`). What
  every game created before the field plays.
- `"double"` — the **double bag**: the fourteen pieces `[0..6, 0..6]` shuffled
  the same way, index `i` at position `i % 14` of bag `i / 14`.
- `"none"` — **no bag**: index `i` is one uniform pick from the seven —
  `uint64n(7)` off PCG(`seed`, `i`) — every piece independent of every other.

Under `split_pieces` (§1.2) the same rule applies to your ration: a double bag
holds the ration twice (`2k` pieces), no bag picks uniformly from the ration,
and the stream is the ration's mixed seed either way. Treat any other value as
the 7-bag, as the game does (`config.Bag.Normalized`). `internal/rng.NewBag` is
the original; `agents/golang-mk1/rng.go`'s `pieceAtBag` is the bit-exact port,
pinned by `TestBagParity` and `--selftest`, and `example-python`'s `piece_at`
takes the kind as its third argument. A game without the field is the 7-bag,
unchanged.

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
`rng.go` (the bit-exact PCG + 7-bag port, the `bag` kinds, and the `split_pieces` deal) →
`engine.go` → `planner.go` →
`difficulty.go` → `types.go` → `agent.go` (the lobby) → `game.go` (one game).

One behavior of its move pipeline worth copying: moves that lose a CAS race are
**dropped, not retried** — it re-observes committed state, resyncs, and re-plans
(§4.3), which is what keeps a contended board consistent. Its `--publish` flag
plays all three publish disciplines the contract allows — `sync` (await every
commit ack), `async` (pipelined, no expectation on in-flight cells, the
default), and `optimistic` (pipelined with predicted sequences) — the same
choice the human client's HUD offers, with the same repair-and-re-plan recovery
when a pipelined batch is lost.

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
| `JETRIS_LOBBY` | KV bucket | presence (`players.<name>`), game listings (`games.<gameID>`), invitations (`invites.<name>.<gameID>`, one per invited game), pinned replays (`pins.<gameID>`: the key's existence keeps that game's replay out of every archiver's purge — read them before you purge, step 6) |
| `JETRIS_CHAT` | stream | all chat on `jetris.chat.<gameID>`; the lobby chat uses the reserved game ID `lobby` |
| `JETRIS_ARCHIVE` | stream | finished-game records (`jetris.archive`) |
| `JETRIS_GAME_<gameID>` | stream | the blackboard: `jetris.game.<gameID>.>`, memory storage, full game history retained (no per-subject cap), atomic publish + direct get enabled |
| `jetris.lobby.event.>` | core NATS subjects | transient lobby events (`game.created/joined/left`, `invite.sent/retracted/declined`) — no stream, subscribe live |
| `jetris.voice.<gameID>.>` | core NATS subjects | the players' voice chat: 168-byte IMA ADPCM frames on `…all.<player>` and `…team.<t>.<player>` (the lobby's room is `jetris.voice.lobby.all.<player>`) — no stream, never replayed; nothing an agent needs to subscribe to or publish |

The last property is the heart of the design: the stream keeps only the latest
message per subject, so **the last message on each cell subject IS that cell's
current value** — the stream is simultaneously the event log, the current state,
and the real-time push fabric.

### 4.2 Game-stream subjects

| Subject | Payload | Notes |
|---------|---------|-------|
| `jetris.game.<id>.meta` | `GameMeta` JSON | lifecycle state machine; CAS on last subject sequence; `extra_columns` (4-10, absent = 10) is the SHARED board's width setting — a cooperative or team board is `10 + (seats − 1) × extra_columns` wide (`seats` = `player_count` in cooperative, `team_size` in teams) and the seats' spawn points are one `extra_columns` step apart, so seat N spawns at column `N × extra_columns + 3` (§2/§3/§5 of the gameplays); absent — every game created before the setting — means the historical full 10-column section per seat, and competitive ignores it entirely; `next_count` (0-6) is the piece-preview size — your lookahead allowance; `garbage_holes` (0-4, absent = 0) is how many empty cells every garbage row you raise is punched with, and `random_garbage_holes` (bool, absent = false) whether each row draws its own columns (§4.4); `guideline_garbage` (bool, absent = false) makes your clears attack by the Guideline table — 0/1/2/4 rows for 1/2/3/4 lines — instead of one row per line (§4.4); `hold` (bool, absent = false) switches on the Guideline hold queue for every seat — a player may swap the falling piece for a held one, once per piece: on the wire that is an ordinary CAS cell batch (the outgoing piece's cells vacated, the incoming type placed at the seat's spawn point, active cells first), so you need do nothing to *see* a hold, and to *use* one you publish that same batch yourself, keeping your own slot and advancing your `pieceIdx` only when the slot was empty (the reference agent never holds); `no_ghost` (the hard-drop ghost preview) and `show_headroom` (the four hidden rows above the playfield drawn behind smoked glass) are UI-only rules agents can ignore; `split_pieces` (bool, absent = false) is the TEAMS-mode piece split — the seven types are dealt out between a team's seats and your seat plays only its own ration, see below; `bag` (`"double"` / `"none"`, absent = the 7-bag) is how every seat's piece sequence is dealt — the double bag, or no bag at all (§1.3); `team_count` (2-6, absent = 2) is how many teams a TEAMS game is played between and `team_size` how many seats each of them holds, so `player_count = team_count × team_size`, team indices run `0..team_count-1`, a team board has the standard 20 visible rows like every other board, and the game is over once at most one team still has a member standing (§5 of the gameplays) |
| `jetris.game.<id>.roster.<player>` | `PlayerSummary` JSON | join announcement (competitive opponent discovery) |
| `jetris.game.<id>.countdown` | `{"seconds": N}` | 5..0 before start |
| `jetris.flash.<id>.<player>` | `{"pi","tm","c"}` | **core NATS** (not on the game stream): a player's transient CAS-failure flash, for spectators |
| `jetris.game.<id>.events.<kind>.<player>` | `GameEvent` JSON | per-KIND, per-SENDER event subjects (`line_clear`, `game_over`); consume with the `events.>` filter. Per-subject retention can only ever trim an OLDER event of the same kind from the same player — `line_clear` carries the sender's cumulative `total_score`/`total_lines` (fold deltas) plus `cleared_rows` (the cleared rows' pre-collapse indices — teammates on a shared board flash them) and the clear's Guideline names (`t_spin` 0/1/2, `back_to_back`, `combo`, `perfect`; §4.6) — on a shared board a lock that scored without clearing (a drop's points) is announced too, with `lines_cleared` 0: fold its totals like any other — and each player publishes at most one `game_over`, which carries the same totals so the sender's last points count, so nothing meaningful is ever lost |
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
`g` adversarial garbage. **A row of nothing but `g` cells is permanent** — it
never completes. A garbage row raised with holes (the game's `garbage_holes`)
completes like any other line once every cell is settled and at least one of
them is not garbage, i.e. the holes have been filled: your completed-row scan
must be "all settled AND some non-`g` cell", not "all settled AND no `g` cell".

### 4.3 The write discipline

- **A move is not a message saying "left".** You locally validate the move
  (collision rules in `jetris-gameplays.md`; SRS kicks), project the changed
  cells, and publish them as ONE **atomic batch** with per-subject CAS
  (`Nats-Expected-Last-Subject-Sequence` = the last sequence you have seen for
  each cell). Order cells within the batch by their new content: active first,
  locked second, empties last.
- **A batch can carry the whole walk.** Nothing limits a batch to one step:
  validate the path to your planned orientation and column step by step on your
  local board (each rotation in place, each shift one column), and publish the
  diff from where the piece stands to where the path ends as ONE batch — one
  round trip for the walk instead of one per step. A step blocked by another
  player's falling piece ends the walk there (wait, it falls away); one blocked
  by the stack at the very first step means the piece locks where it stands.
  Humans' clients merge the moves queued during a round trip the same way.
- **Batches may be pipelined.** Nothing in the contract makes you await one
  batch's commit ack before sending the next: you may publish your move batches
  asynchronously — send the commit, handle the ack when it comes — and keep
  several in flight, exactly as the human client's async/optimistic publish
  modes do. The catch is CAS: a per-subject expectation is an EXACT sequence
  known only from the ack, so a pipelined batch cannot carry an exact
  expectation about a cell an un-acked batch already wrote. Such a cell goes out
  either with NO expectation (the async discipline), or with the sequence the
  un-acked write is PREDICTED to get (the optimistic discipline: a batch's N
  messages take N consecutive stream sequences, so predict from the highest
  stream sequence you have observed, or the predicted end of the batch in flight
  ahead, whichever is later); every other cell keeps its exact per-subject CAS.
  Both are optimistic in the true sense — a batch sent behind an un-acked one is
  computed on the assumption that it commits. When a pipelined batch is lost (a
  CAS race, or a wrong guess), treat it like any dropped move, at pipeline
  scale: stop publishing, let every in-flight ack drain, re-fetch your committed
  board (§4.5), vacate any stray cells the batches poisoned behind the loss left
  committed, flash, and re-plan. Bound your depth (the human client keeps at
  most a handful of batches in flight) and keep your barriers honest: a lock-in,
  a spawn, and every gated transform (§4.4) still needs exact expectations, so
  settle the pipeline — drain, repair if broken — before publishing one.
- **Player moves that lose CAS are dropped** — never retried. Re-observe, re-plan.
  On a dropped move, **broadcast a CAS-failure flash** so spectators can see it (see
  below).
- **Authoritative writes** (your lock-in, hard-drop landing) publish **without
  CAS**. Bulk transforms — your line-clear collapse and applying owed garbage —
  are GATED batches: cells NoCAS, exactly-once through the txn register (§4.4).
- **Write-through**: after a successful publish, apply the committed cells and
  their inferred sequences to your in-memory board immediately (batch messages get
  consecutive sequences ending at the commit ack); your own echo then no-ops via a
  strictly-higher-sequence rule. A pipelined batch applies its CONTENT at send
  time (that is the optimistic projection §1 lets you plan from) and reconciles
  the actual sequences when its ack arrives.
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

- **Attacking (you cleared N lines).** The rows you owe are N — or, when the
  meta's `guideline_garbage` is true, the Guideline table: 0 for a single, 1
  for a double, 2 for a triple, 4 for a Jetris; 0/1 for a Mini T-Spin
  single/double, 2/4/6 for a T-Spin single/double/triple; +1 when a Mini or a
  T-Spin single is Back-to-Back, +2 for a T-Spin double or a Jetris, +3 for a
  T-Spin triple; +10 for a perfect clear (§4.6 defines those; owing 0 means
  you touch no register at all). For every victim board (competitive:
  each surviving opponent; teams: ONE opposing team's board — see below),
  CAS-add the garbage
  register: read its last message (`{"total": T}` at sequence S, or 0/0 if
  never written), publish `{"total": T+rows, "by": <yourPlayerIdx>}` with
  `Nats-Expected-Last-Subject-Sequence: S`, and on a CAS rejection refresh
  and re-add (bounded retries). Simultaneous attackers serialize on the
  expectation and the register converges to the exact sum — an attack can
  never be lost, trimmed, or double-counted.

  In teams, **which** opposing board takes the raise follows the game's
  `team_count`. With two teams there is only one answer. Past two, a raise
  still goes to exactly ONE opponent and your attacks **rotate** through the
  others — attack 1 to `(yourTeam + 1) % teamCount`, attack 2 to the next,
  wrapping around and skipping your own — so consecutive raises spread evenly
  instead of every opponent taking the full one (which would multiply the
  garbage in play by the number of teams). Keep the rotor per agent; the
  reference agent does, and the GUI engine does the same per engine. Nothing
  else on the wire changes: a victim reads its own register and cannot tell
  which opponent was on rotation.
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
  stack shifts up N rows and N adversarial rows
  (`{"o":true,"t":1,"g":true,"pi":<causer>}`) fill the bottom — full-width
  when the meta's `garbage_holes` is 0 or absent, otherwise with that many
  cells per row left EMPTY (publish nothing there, or `{}`): pick
  `garbage_holes` distinct random columns once per raise and leave them open
  on every row of the raise — or, when the meta's `random_garbage_holes` is
  true, draw afresh for every row — never more than width−1 so each row
  keeps garbage cells. Every falling
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

### 4.6 Scoring: your locks, by the Guideline

Every mode scores by the Guideline table (gameplays §2 Scoring; the
GUI's `internal/game/scoring.go`, the reference agent's `scoring.go`). Your
peers never recompute your score — they fold the cumulative
`total_score`/`total_lines` YOU announce — so an agent that scores its own
locks differently skews every shared and team scoreboard it plays on. For
each lock of yours:

- **Level** — the Guideline's 1-based level BEFORE the clear: the board's
  shared line total `/ 10 + 1` on a shared board, your own lines' in
  competitive (capped at 20).
- **The clear** — Single 100, Double 300, Triple 500, Jetris 800; a T-spin
  that cleared nothing 400 (Mini 100); Mini T-Spin Single/Double 200/400;
  T-Spin Single/Double/Triple 800/1200/1600. × 1.5 when it is Back-to-Back
  (a Jetris or a T-spin clear right after another such clear; only a plain
  single, double or triple breaks the chain). + 50 × the combo count
  (consecutive clearing locks: 0 for the first, 1 for the next…; a lock that
  clears nothing ends the run). + 800/1200/1800/2000 for a perfect clear of
  1-4 lines (3200 for a Back-to-Back Jetris) — nothing locked left on the
  board, garbage included. All of that × the level.
- **Drop points**, unmultiplied — 1 per cell soft-dropped, 2 per cell
  hard-dropped.
- **T-spins** — only if your agent rotates a T into place as its LAST move
  (`golang-mk1` never does: it turns at the spawn, shifts, drops): three of
  the T's 3×3 box corners filled by locked cells or the walls; full when both
  corners on the pointing side are, or the rotation used the fifth SRS kick;
  Mini otherwise.
- **Announce it** (§4.2) — a `line_clear` with `score` (the lock's points),
  `lines_cleared`, `cleared_rows`, the names (`t_spin`, `back_to_back`,
  `combo`, `perfect`) and your cumulative totals: for every clear, and on a
  shared board for every lock that scored at all (`lines_cleared` 0). Put the
  totals on your `game_over` too, with `total_lines` for the archive's
  per-player line count.
- **Garbage** follows the same clear (§4.4).

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
   simply shows no conversation. On a shared board the record must also carry
   the meta's `extra_columns` — the replay viewer rebuilds the board's width
   from it, and without it the recorded cells are laid out on the wrong
   geometry.
6. **Replay archive** (part of archiving, AFTER the record publish and BEFORE
   the stream deletion): copy the ENTIRE game stream into the ONE shared
   file-backed **`JETRIS_REPLAY`** stream so the GUI can replay the game
   later. Every finishing game gets a replay; it is KEPT while the game is in
   the *keep set* — the top 10 of its bucket (one bucket per (mode,
   with/without agent seats) pair, ranked by headline score (coop total /
   best team / best player), then shorter duration, newer finish, game ID) or
   the 25 most recently finished games overall (newer finish first, game ID
   on ties) — or **pinned**: a `pins.<gameID>` key in the `JETRIS_LOBBY` KV
   bucket (a player's Pin on the replay screen; its value names who pinned
   it and when) keeps the game in the set whatever its rank or age, until
   the key is deleted. The first two are pure functions of the archive
   records (read them all off `JETRIS_ARCHIVE`; yours included, as you just
   published it) and the pins are read straight off the bucket (list its
   keys under `pins.`), so every archiver cuts the identical set. If the pin
   keys cannot be read, purge NOTHING — the purge is the irreversible step.
   FIRST `Purge` (filter `jetris.replay.<gameID>.>`) the replay of every
   game you can see a record for that is no longer in the keep set — one
   purge removes a game's copies and marker alike. THEN republish each message of your game under
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

- [ ] Decisions use only UI-visible information (§1): the seed is read only to generate your own sequence; lookahead is capped by the game's `next_count` read from its meta (absent = 0), and no difficulty, flag or default of yours can raise it (§1.1)
- [ ] `agent: true` on presence and roster entries
- [ ] `max_agents` honored inside the join CAS
- [ ] `invite_only` games joined only when invited (watch `invites.<name>.*`; accept = join + delete key, decline = rewrite with `declined: true`)
- [ ] Name is `<agent-name>-<instance>-<difficulty>`, KV-key-safe, ≤32 chars
- [ ] Moves published as atomic CAS batches; dropped moves re-planned, not retried (pipelined/async batches allowed — a lost one drains, repairs, and re-plans; barriers settle the pipeline first, §4.3)
- [ ] CAS-failure flashes broadcast on `jetris.flash.<id>.<name>` (core NATS)
- [ ] Gravity, lock-in, clears, garbage, spawn rules implemented
- [ ] In a teams game with the meta's `split_pieces`, pieces drawn from YOUR seat's ration — the deal computed off `seed` and `team_size` for your `team_slot` (§1.2)
- [ ] Pieces dealt by the meta's `bag` — the 7-bag when absent, the double bag under `"double"`, independent draws under `"none"`, a ration shaped the same way (§1.3)
- [ ] Garbage rows raised with the meta's `garbage_holes` (one column set per raise, or one per row under `random_garbage_holes`), and a holed garbage row cleared like any line once its holes are filled — a solid garbage row never (§4.2, §4.4)
- [ ] Attacks delivered by CAS-adding victims' garbage registers (never events), sized one row per line — or by the Guideline table (0/1/2/4 for plain clears, the T-spin rows, the Back-to-Back and perfect-clear bonuses) when the meta's `guideline_garbage` is true (§4.4)
- [ ] Your own locks scored by the Guideline table and announced with your cumulative totals — every clear, and on a shared board every lock that scored (§4.6)
- [ ] Clears and garbage applied as txn-gated batches; deficit reconciled on join
- [ ] Countdown run when your ready toggle completes the set
- [ ] Archive performed when you trigger the finish
- [ ] Presence deleted and seats freed on the way out
