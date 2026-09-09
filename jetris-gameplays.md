# Jetris — Gameplay Reference

**Version:** 1.0
**Status:** Authoritative
**Date:** April 2026

This document is the single source of truth for all gameplay mechanics in Jetris. The spec (`jetris-project-structure.md`) and plan (`jetris-implementation-plan.md`) defer to this document for gameplay behavior. Any gameplay change must be reflected here first.

---

## 1. Piece Types

Seven standard tetrominoes, each with 4 orientations (0-3):

| Piece | Color   | Shape |
|-------|---------|-------|
| I     | Cyan    | 4-cell horizontal line |
| O     | Yellow  | 2x2 square |
| T     | Purple  | T-shape |
| S     | Green   | S-skew |
| Z     | Red     | Z-skew |
| J     | Blue    | J-hook |
| L     | Orange  | L-hook |

**Rotation:** Super Rotation System (SRS) with standard wall kick tables. Up to 5 kick offsets are tried per rotation attempt. The I-piece has its own kick table; the O-piece does not rotate. A **half turn** (180°, the Z key, `Rotate180`) uses SRS-X's 180 kicks — the rotation system the 180° twists of [harddrop.com/wiki/List_of_twists](https://harddrop.com/wiki/List_of_twists) are drawn for — up to 12 offsets, three cells sideways or up and down at the most, so a piece can twist into a slot or pass through a one-cell wall; the I has its own 180 table too (`internal/game/rotation.go`, every one of the wiki's 180 twists in `Test180Twists`).

**Piece sequence:** 7-bag randomizer — within each group of 7 pieces, all 7 types appear exactly once in a random order. The bag is shuffled using a seedable PCG RNG so the sequence is deterministic and seekable. (Two exceptions, by choice: a game's **bag** rule (§1b) can deal a **double bag** — two of each type in every fourteen — or **no bag** at all, every piece an independent draw; and a teams game created with **split pieces** deals the seven types out between the teammates and gives each seat a bag of its own ration — §5.)

Each player has a color associated with it: used for the outline color of the piece in spectator mode, and also for the outline color of the piece when it's dropped.

---

## 1b. Piece Preview (the game's NEXT count), Ghost, Hold, Bag, Hidden Rows, Garbage — the play rules

**Every play rule below is chosen on one step of the create-game wizard (step 2,
GAME RULES) by a single radio.** **Guideline** — the default — plays every rule at
the setting closest to the Guideline this game can offer
(`config.GuidelineRules`): `next_count` 6, the ghost piece, the `hold` queue, the
standard 7-bag, the hidden rows out of sight and, for the modes that raise garbage, `garbage_holes` 1 with the rows of one attack
sharing their hole column and `guideline_garbage`; the lobby row tags such a game
`guideline`. **Custom** exposes each rule as its own editor or checkbox — opening
at the preset's own settings, so the creator changes only what they mean to —
and the row tags every rule that differs from the
classic game (`next N`, `hold`, `double bag` / `no bag`, `hidden rows`, `holes N` / `random holes N`,
`guideline garbage`).
Whichever way they were chosen, the rules are stored in the game's meta record —
the rule book every engine reads at start — and bind every seat equally.

**How many upcoming pieces a game reveals is a per-game attribute**: `next_count`,
an integer 0-6 (the custom editor opens at the preset's 6)
and fixed for the life of the game in its meta record (`GameMeta.NextCount`). It applies to
every mode and to **everyone in the game equally — humans and agents**:

- **0** — nobody sees anything coming: no NEXT well, no agent lookahead (the
  original Jetris behavior, and what games created before the attribute existed
  replay as).
- **1-6** — while playing, a **NEXT well** sits in its own framed sub-division
  hugging the playfield's top-left, classic arcade style: one mini tile per
  revealed piece, stacked top-down in play order, always your **own** queue
  (each seat advances its own `pieceIdx`, so "next" is per-seat; spectators get
  no well). The tiles use the same cell size as the playfield itself and scale
  with it, so the preview reads exactly like the pieces on the board at any
  window size. The lobby row advertises the setting as a `next N` tag.

**The hard-drop ghost is a per-game attribute on the same wizard step**: "Show
ghost piece" (on by default) decides whether every player sees the translucent
landing preview of their falling piece — where it would hard-drop right now,
derived from committed board state, never published. Stored inverted in the meta
(`GameMeta.NoGhost`), so games created before the attribute — and agent-hosted
games — show the ghost. Off means everyone eyeballs their drops; like the piece
preview it is one rule for every eye (agents already compute their drop
destinations, so the ghost only levels the field for humans either way).

**The hold queue is a per-game attribute on the same step**: `hold`
(`GameMeta.Hold`; the custom checkbox "Hold piece", on by default as in the
Guideline preset). With it on, **C** — or the control pad's HOLD button, or a tap
on the HOLD box itself, or an upward swipe on the playfield — sets the falling piece
aside, as in the Guideline: with
the slot **empty**, the falling piece goes in and the **next piece of the queue
comes out** at the spawn point in its spawn orientation, and the queue advances
(`pieceIdx` +1 exactly as a lock-in would, so the NEXT well moves on); with a
piece **in the slot**, the two **trade places** and the queue stays where it is.
**One hold per piece**: the piece that came out cannot be held again until the
next piece spawns from the queue — the HOLD box dims its tile meanwhile. A hold
whose incoming piece cannot be placed (locked cells in the spawn rows — the
stack has reached the top, and the next spawn will call the top-out — or, on a
shared board, another player's piece crossing the spawn cells, a transient
obstacle) is a no-op. Every seat has its own slot. **The slot is never on the
wire**: the swap is an ordinary CAS move batch — the outgoing piece's cells
vacated, the incoming piece's placed, active cells first so no peer ever sees
the player piece-less — dropped and flashed like any move if it loses its CAS
race (the slot then stays as it was), so spectators, replays and agents simply
see a new piece appear at the spawn point, and the lock delay starts afresh for
it. Games created before the attribute have no hold. Agents may hold under the
same rule; the reference agent does not.

**Garbage holes are a per-game attribute on the same wizard step** (shown for the
modes that raise garbage — competitive and teams): `garbage_holes`, an integer 0-4
(the custom editor opens at the preset's 1; `config.MaxGarbageHoles`), stored in the meta (`GameMeta.GarbageHoles`)
and mirrored on the lobby row as a `holes N` tag. It is how many **empty cells every
garbage row is raised with**:

- **0** — the rows an attack lands are solid, and a solid garbage row is
  **permanent**: it can never be completed or cleared (the original Jetris
  behavior, and what games created before the attribute existed replay as).
- **1-4** — every garbage row comes with that many holes, at random columns, and
  **a garbage row clears like any other line** once a player's locked cells have
  filled all of its holes — it scores, it counts toward the level, and it sends
  garbage to the opponents exactly as a line built from scratch would. By default
  all the rows of one raise share the same hole columns, so they line up into a
  well the victim can fill straight down (classic "clean" garbage); each raise
  draws its own columns. A falling piece hovering over the holes when the stack
  rises keeps its position and simply finds itself sitting in them.

**Random hole positions** is a companion attribute on the same step (a checkbox,
off by default, only meaningful with holes): `random_garbage_holes`
(`GameMeta.RandomGarbageHoles`). When set, **every garbage row draws its own hole
columns** instead of the rows of one raise sharing a draw — "messy" garbage whose
holes wander from row to row, so a single well no longer digs out a whole attack
and each row has to be cleared on its own terms. The lobby row tags such a game
`random holes N`. Games created before the attribute — and 0-hole games — behave
as unset.

**Guideline garbage** is a third attribute on the same step (a checkbox, on by
default as in the preset): `guideline_garbage` (`GameMeta.GuidelineGarbage`). It sets **how much
garbage a clear sends**. Unset, every cleared line owes one garbage row (the
original Jetris rule), whatever the clear was. Set, the attack follows the
Guideline table (`game.Clear.AttackRows`; tetris.wiki/Garbage, "General Garbage
System in Guideline Games"):

| Clear                             | Rows sent | Back-to-Back bonus |
|-----------------------------------|---|--------------------|
| Single / Double / Triple / Jetris | 0 / 1 / 2 / 4 | Jetris +2          |
| Mini T-Spin Single / Double       | 0 / 1 | +1 / +1            |
| T-Spin Single / Double / Triple   | 2 / 4 / 6 | +1 / +2 / +3       |
| 180 Spin Single / Double / Triple | 2 / 4 / 6 | +1 / +2 / +3       |
| Perfect clear                     | +10 on top of the clear's rows |                    |

So a plain single attacks nothing, a Jetris is worth twice a triple, and the
spins and chains the scoring rewards (§2 Scoring) attack hardest. Combos send
nothing extra (the wiki lists no table for them). Scoring and levels are the
same under both settings; only the rows owed change. Independent of the hole
attributes. The lobby row tags such a game `guideline garbage`.

**The bag** is a fourth attribute on the same step (a radio, the 7-bag by
default): `bag` (`GameMeta.Bag`). It sets **how the pieces are dealt** — the
randomizer every seat's sequence is drawn with:

- **7-bag** (the field absent: the default, the Guideline's randomizer, and what
  every game created before the attribute plays) — every seven pieces are the
  seven types shuffled, so every type turns up in every seven and a drought of
  one type never lasts more than twelve pieces.
- **`double`** — the double bag: every fourteen pieces are two of each type
  shuffled together. Fair over a longer stretch, so two of a kind can come back
  to back and a drought can last up to twenty-four pieces.
- **`none`** — no bag: every piece is drawn on its own, any type as likely as any
  other whatever came before. The old-school randomizer — three S's in a row and
  a forty-piece I drought are both fair game.

Every kind is dealt off the game's seed and stays seekable (§1): bag `k` is a
Fisher-Yates shuffle of the bag's contents seeded by PCG(`seed`, `k`), and index
`i` is position `i mod |bag|` of bag `i div |bag|`; no bag seeds PCG(`seed`, `i`)
and takes one uniform pick (`rng.NewBag`). The standard 7-bag is bit for bit
the sequence it always was. In a split-pieces teams game (§5) the rule shapes
each seat's ration the same way — a double bag of the ration, two of each of
its types, or independent draws from it. One rule for every seat, humans and
agents alike (an agent reads `bag` from the meta and deals accordingly — agent
guide §1.3). The lobby row tags such a game `double bag` or `no bag`; the
Guideline preset always deals the 7-bag.

**The hidden rows** are a fifth attribute on the same step (the custom checkbox
"Show hidden rows", off by default; off in the Guideline preset): `show_headroom`
(`GameMeta.ShowHeadroom`). Every board has `config.HeadroomRows` (4) rows above
its 20 visible ones where a piece spawns (§2), normally out of sight — the
Guideline playfield is the twenty rows and no more. With the setting on, **every
board of the game draws its headroom rows above the playfield, behind smoked
glass**: a dark translucent pane with a diagonal sheen and a lit lower edge where
it meets the playfield, through which a piece shows dimly the moment it spawns
and a stack shows how close it is to topping out. The pane covers everything up
there — cells, the ghost, the CAS outlines, the row strobes — and it is on the
player's own board, the opponents' thumbnails, every spectator board and the
replay alike (the replay reads it off the recorded meta). Presentation only:
spawning, the top-out rule, the visible region the engines play by and every
protocol message are exactly what they were, and agents ignore the field. Absent
— every game created before the attribute — the boards start at the playfield.
The lobby row tags such a game `hidden rows`.

Because every bag's sequence is seekable, the preview is a pure read
(`seq.Piece(pieceIdx+1 .. +next_count)`) — no queue state exists anywhere.

The same number is an **agent's lookahead allowance**: the fair-visibility contract
(§11) lets an agent plan with exactly the pieces a human can see in the NEXT well
and no further. One knob moves both eyes — and it is the game's knob: an agent
reads it from the meta of the game it is in (a meta without the field means 0),
and no difficulty setting or flag of the agent's can raise it, only use less of it.

---

## 2. Playfield

| Property | Value |
|----------|-------|
| Total rows | 24 for one seat: a competitive board, and a shared board before any extra rows. A SHARED board — cooperative, or one team's — is `24 + (seats − 1) × extraRows` tall, where `extraRows` is the game's `meta.extra_rows` (0–10, the create wizard's extra-rows slider, default 0: absent means no extra rows, the standard playfield whatever the seat count) |
| Headroom rows | 4 (rows 0-3, not rendered) — the same on every board: a taller board grows downwards, so the spawn rows and the top-out rule never move |
| Visible rows | 20 (rows 4-23) for one seat, plus the extra rows of a shared board |
| Standard width | 10 columns in competitive mode. A SHARED board — cooperative, or one team's — is `10 + (seats − 1) × extraColumns` wide, where `seats` is `playerCount` (cooperative) or `teamSize` (teams) and `extraColumns` is the game's `meta.extra_columns` (4–10, the create wizard's board-width slider, default 4). A meta without the field — every game created before the slider — reads as 10, the historical `seats × 10` board |

**The line goal (`meta.line_goal`):** a game's length in lines, 0 (absent) for
the classic run until someone tops out. With a goal the game ends the moment a
PLAYFIELD has cleared that many lines in total — every engine decides it at the
same point, when the `line_clear` that crosses it is consumed from the ordered
events stream (the crew's board counts every seat's lines, a team's board its
members', a competitive board its player's; competitive boards announce their
clears for it): on a single playfield the game is over — the crew is done, a
board scored per seat crowns its top scorer(s) — and across several the first
playfield there wins for its team, or its player. Every player's engine then
moves the meta to `finished` (the CAS makes it idempotent), the HUD counts
`LINES 12 / 40` all along, and the game-over box reads GOAL REACHED. A crew
that **tops out before its goal has lost**: the game ends as at any top-out
(Game Over, below), but the verdict is decided — nobody a winner — on every
engine alike, the box reads GAME OVER over `GOAL MISSED · YOU LOST`, no name
is crowned, the high-score fireworks stay dark, and the archive record marks
no winner.

**Cell states:**

| State | Occupied | Active | Meaning |
|-------|----------|--------|---------|
| Empty | false | false | Nothing in this cell |
| Active | false | true | Part of a falling piece |
| Locked | true | false | Settled piece, permanent until line clear |
| Adversarial | true | false | Garbage cell added by a competitive or teams shrink (the `Adversarial` flag is set). A row made of nothing but garbage cells — what a 0-hole game raises — is permanent and can never be completed; a garbage row raised with holes (§1b) clears like any other line once the holes are filled with locked cells |

On shared boards (cooperative, teams), active cells carry a `PlayerIdx` field (0-indexed, global across the whole game) identifying which player's piece they belong to.

---

## 3. Cooperative Mode

1 or more players, there is one piece per player, all the players see each other's pieces on the same common playfield.

**A crew of one is allowed** (`config.MinPlayerCount`: 1 for cooperative, 2 for competitive, 1 per team): a **solo co-op game** is one player alone on the standard 10-column board, playing for the **highest score** — nobody to cooperate with, nobody to beat, only the record. Everything else is the cooperative game as written below: the shared score is the player's own, the level is theirs, the game ends at their top-out, and the score competes for the archive's solo (1-player) co-op record — the high-score fireworks (Game Over, below) play when it beats it. The create wizard's seat editor opens at 1 — a co-op game is solo unless the creator adds seats — and floors at 1 for a co-op game (annotating the solo choice), an agent hosts one with `--players 1`, and the lobby starts it the moment its one seat is filled and ready. On the wire a solo game is a **journal**: with nobody else writing the board, nothing can lose a CAS race, so the game is played on the player's local board at full speed and every write goes out behind it as a batch with no expectation, never waited for — the HUD's publishing switch only decides whether one batch or any number may be in flight (README §5.11, `internal/engine/solo.go`).

### Playfield

The playfield is a single shared board of width `10 + (playerCount − 1) × extraColumns` — the standard 10 columns the first player needs plus the game's `meta.extra_columns` (§2) for each player after them, so 2 players share 14 columns at the default of 4, 3 players 18, and at the maximum of 10 every player has a full 10-column section of their own. A solo game is the standard 10 columns whatever the setting, so the create wizard shows a one-seat shared board no width slider. Each player controls it's own piece. All players' pieces exist on the same playfield and can move anywhere on it — they are not restricted to any section, however player's tetrominoes can _not_ overlap.

### Piece Spawning

Players share the same RNG seed (`meta.Seed`) and produce the identical piece sequence

Each player tracks their own `pieceIdx` independently.

**Spawn position:** Each player's piece spawns centered in their section, but can immediately move anywhere:
- Player N spawns at column `N * extraColumns + 3` — the seats are spaced one `extra_columns` step apart, the same step the board's width is built from, so the first seat spawns at the left edge and the last one's 10-wide spawn box ends exactly on the board's last column. (At the maximum of 10 this is the historical `N * 10 + 3`, the center of an own 10-column section; at the default of 4 the seats sit shoulder to shoulder — a horizontal I spans 4 columns, so no two spawn boxes ever overlap.)
- Anchor row 2 for **all** piece types, so every piece's lowest cell sits at row 3 (just inside the headroom) and they all become visible after the same number of gravity ticks. (Spawning the I one row higher made it appear a tick later than the rest, so a player hard-dropping each piece on sight would drop the I before seeing it.)

**Spawn blocked (shared boards):** the same distinction gravity makes applies at spawn time. If the spawn cells are held by **locked cells**, the player tops out. If they are covered **only by another player's active (falling) piece** — a transient obstacle that will itself fall away — the spawn does **not** top out: it is deferred and retried as soon as the shared board changes (every incoming cell message may be the blocker moving away — at agent speeds a piece crosses the spawn cells in milliseconds), with the gravity tick as the backstop, until it succeeds or the cells become locked (a genuine top-out). Detection mirrors movement: `CanPlaceCoop` fails but `CanPlace` (which ignores active cells) succeeds. Without this rule, a teammate's piece merely crossing the spawn area would spuriously eliminate the player (and in cooperative end the game for everyone). The engine also runs a piece-less **watchdog** on the same gravity tick: an alive player with no piece and no deferred spawn for two consecutive ticks gets a forced (re)spawn, healing a spawn whose publish was lost on a board that has since gone silent. Neither the deferral retry nor the watchdog runs before the game starts.

**Between a lock and the next spawn:** the next piece is spawned by the lock-in, which the engine detects on its own consumer as the lock's cells echo back, and the spawn is a publish of its own — so on the wire the gap from a lock to the next piece is a round trip or two, and on a far server (a beta player's 189 ms batch round trip) a fast player has already pressed the next piece's keys before it is on the board. What happens to those presses depends on whose lock it was:

- **Behind the player's own hard drop**, the presses are the next piece's: a rotation, a shift, a hold, the next drop. The engine keeps them in its move queue — visible in the MOVE BUFFER strip — until the next piece is on the board (`Engine.AwaitingSpawn`, `runInput`), then plays them on it in the order they were made. The UI dispatches them at once in that gap rather than holding any itself, so the order survives, and its accidental-drop guard counts the drops the player made (one lock-in pays for each) rather than guarding the gap (`nativeui/dropguard.go`).
- **Behind a lock the player did not make** (the lock delay ran out), a move that reaches the engine with no piece on the board is a no-op, as ever: the UI holds shifts and gestures for the next piece and the guard refuses the hard drop for the gap and its window past the spawn (§1 HANDLING).

The lock-in releases the engine's lock across the clear's publish and the spawn's (and sends its line-clear event without waiting for the ack), so the round trips stall neither the screen nor the input loop; meanwhile the gravity tick's piece-less watchdog stands down for a lock of the engine's own still on its way back (`lockInFlight`, for at most `lockEchoGrace`) and for a spawn in progress (`spawning`) — a tick that fired during a drop's publish runs the instant the ack writes the lock through, before the consumer has seen a thing, and would otherwise count the gap as a stall and spawn a second piece.

### Movement

Pieces can move anywhere on the full-width board. Collision detection (`CanPlaceCoop`) checks:
1. Boundaries (columns 0..width, rows 0..height — 28 in cooperative)
2. Locked cells (occupied, non-active)
3. The **other player's active cells** (treated as obstacles)
4. The moving player's own active cells are **excluded** from collision

#### Controls

Same in both game modes. The UI calls the engine's move methods directly (no input is sent
over NATS — each move is a local intent that publishes the changed cells with CAS):

| Key | Action | Engine method |
|-----|--------|---------------|
| ← / → or A / D | move left / right | `MoveLeft` / `MoveRight` |
| ↓ or S | soft drop (one row) | `MoveDown` |
| ↑ or W | rotate clockwise | `RotateCW` |
| Ctrl or X | rotate counter-clockwise | `RotateCCW` |
| Z | rotate 180° — a half turn, on the SRS-X kicks | `Rotate180` |
| Space | hard drop | `HardDrop` |
| Shift or C | hold (games with the hold rule, §1b); Shift acts on its **release** | `Hold` |
| Tab | switch the keys between the piece and the chat (a shifted Tab too) | — |

Those are the defaults. The scheme is the player's: every line of the lobby menu's KEYS
legend opens a small dialog on the keys it lists, and any of them can be swapped for
another key — one no other move has (Tab, Escape, ⌘, Alt and Super are reserved). The
bindings are kept across launches (`prefs.Keymap`: `keys.json` beside `handling.json`, or
a localStorage key in the browser); the touch gestures are fixed. Every key is turned into
the *move* it makes (`keyBinds.actionFor`, `internal/nativeui/keymap.go`) *before* any
dispatch, so two keys bound to one move share one DAS/ARR machine per axis — A and ← are
the same key held, not two keys racing — and what is fixed in the dispatch is what each
move does, whatever key makes it.

Ctrl and Shift are the Guideline's two modifier controls, and Gio delivers a modifier's
own press *and release* as a `key.Event` named for it, so they are filtered as keys like
any other. Every board key filter therefore carries `Optional: ModShift|ModCtrl` — a filter
naming no modifier matches only an *unmodified* event, so without it holding Shift would
kill every other control, and the modifiers' own presses (which carry their bit) would
never arrive at all. ⌘ and Alt are deliberately left unmapped: they are the platform's.

Shift, as the hold key, is the one key that acts on its **release**, because a shifted Tab
is still the board/chat switch and a player reaching for the chat did not mean to spend
the hold: the press arms it, the release spends it, and a Tab in between — or losing the
keys, whose release the board would never see — disarms it unspent. Every other hold key
holds on the press as ever.

These are dispatched from `internal/nativeui/input.go` (the board tag is kept focused
with Gio's `key.FocusFilter` + `key.FocusCmd`). The on-screen control pad and, on a touch
screen, the playfield's gestures dispatch the same engine methods (see *Mouse controls*
and *Touch gestures* further down).

### Gravity

Gravity ticks at the standard speed curve interval (see Section 7). On each tick, the engine attempts to move the piece down one row.

**Blocked by locked cells or bounds:** The piece has **landed**. It does not lock on the spot: the lock delay starts (see Lock Delay below) and the piece locks where it rests when the delay expires.

**Blocked only by the other player's active piece:** The piece has **not** landed — it neither locks nor starts its lock delay. The obstacle is temporary — it will itself fall on its next gravity tick. Gravity waits and tries again on the next tick (and the moment the obstacle locks beneath it, the piece counts as landed and its delay starts).

Detection: if `CanPlaceCoop` fails but `CanPlace` (which ignores all active cells) succeeds for the position one row below, the obstacle is the other player's active piece.

### Lock Delay

The Guideline lock delay: **500 ms** (`config.LockDelay`). A piece that lands on locked cells or the floor — by gravity, by a soft drop, or because the stack rose to meet it — rests for the delay before it locks. Every successful shift or rotation made while it rests **restarts** the delay (move reset), at most **15 times** per piece (`config.LockDelayMoveResets`); the allowance is renewed whenever the piece falls to a new lowest row. A piece that leaves the stack (shifted over a hole, kicked upward by a rotation) stops the timer, which starts afresh when it lands again. A soft drop pressed against the floor is not a lock and does not restart the delay; only a **hard drop** locks a piece at once.

The delay is timed by each player's own engine (`internal/engine/lockdelay.go`, on the same single goroutine as gravity and input): nothing about it is on the wire — the other peers simply see the piece's cells stay active until the lock batch commits.

### Hard Drop

The piece falls instantly to the lowest valid position (stopped by locked cells, bounds, or the other player's active piece).

**Landed on locked cells or bounds:** Piece locks immediately — the hard drop is the one move that skips the lock delay.

**Landed on the other player's active piece:** Piece does **NOT** lock — it stays active and resumes falling by gravity. The other player's piece will itself fall, and gravity will continue dropping this piece further.

### Lock-In

A piece locks when it has rested on locked cells or bounds (not another player's active piece) for the lock delay, or on a hard drop. Lock-in converts all active cells (matching the player's `PlayerIdx`) to occupied/locked cells.

After lock-in:
1. Check for completed rows (full-width)
2. Clear completed rows and update score
3. Spawn the next piece

### Line Clears

A row is complete when **all cells across the entire width** are occupied (locked) and none are active. For a 2-player game, all 20 cells in the row must be locked.

Cleared rows are removed and everything above shifts down — **including the hidden headroom rows**: the published collapse covers the full row range 0..H−1, so a cell sitting in the headroom drops with the rest of the stack (a visible-rows-only republish would leave headroom cells duplicated or orphaned). The cleared state is published with the shared-board CAS merge-retry so the shift can't clobber another player's mid-flight piece.

A clear must **not** disturb the other players' falling pieces: only the player whose piece completed the row gets a new piece; everyone else's piece keeps dropping, shifted down by the number of cleared rows. Only the cells that actually change are published, and within the atomic batch they are ordered by their **new** content — active cells first, locked cells second, vacated (now-empty) cells last. Vacating first instead, another player's mid-flight piece would be erased from its old cells before its new (shifted) cells arrived — its active-cell count would momentarily hit zero and that player's lock-in detector would fire a spurious lock, respawning their piece from the top. Active-cells-first keeps the shifted piece always present on the board, so it never transiently vanishes.

Because the board is shared, a clear must be reflected on **every** player's screen, not just the player whose piece completed the row. The changed cells are published to the shared per-cell playfield subjects and every player's cell consumer applies them, so the authoritative `playfield` state always converges. Rendering, however, is per-engine: the player who detected the clear re-renders the whole board directly, and every **other** player re-renders the whole board on receipt of the `EventLineClear` event. A full-board re-render (rather than relying on per-row repaint triggers) is used because a clear repaints the entire visible range at once, and individual per-row triggers can be dropped by the bounded, non-blocking UI update fan-out — which would otherwise leave stale, un-cleared rows on another player's board.

### Scoring

Every mode scores by the Guideline (tetris.wiki/Scoring, "Recent guideline compatible games"; `game.Clear`). A lock is worth the clear it made, multiplied by the **level before the clear** — Jetris levels are 0-based (`totalLines / 10`, the gravity curve's index), so the Guideline's multiplier is `level + 1` — plus the piece's drop points, which are never multiplied:

| Action                                                             | Points × (level + 1)                                                               | Difficult   |
|--------------------------------------------------------------------|------------------------------------------------------------------------------------|-------------|
| Single / Double / Triple / Jetris                                  | 100 / 300 / 500 / 800                                                              | Jetris only |
| Mini T-Spin, no lines / T-Spin, no lines                           | 100 / 400                                                                          | no          |
| Mini T-Spin Single / Double                                        | 200 / 400                                                                          | yes         |
| T-Spin Single / Double / Triple                                    | 800 / 1200 / 1600                                                                  | yes         |
| 180 Spin, no lines / Single / Double / Triple                     | 400 / 800 / 1200 / 1600 (as a T-spin)                                              | yes (with lines) |
| Back-to-Back difficult clear                                       | the action's points × 1.5                                                          |             |
| Combo                                                              | + 50 × combo count (0 for the first clear of a run, 1 for the next…)               |             |
| Perfect clear (no locked cell left on the board, garbage included) | + 800 / 1200 / 1800 / 2000 for 1 / 2 / 3 / 4 lines, 3200 for a Back-to-Back Jetris |             |
| Soft drop / hard drop                                              | 1 / 2 per cell, not multiplied                                                     |             |

A **T-spin** is a T whose last successful move was a rotation, resting with at least three of the four corners of its 3×3 box filled — a locked cell, the floor or a wall; another player's falling piece is not a corner. It is a full T-spin when both corners on the side the T points to are filled, or when a quarter turn used the last SRS kick (the T-spin-triple kick — a half turn counts as a rotation but has no such kick, however far it went); otherwise a Mini (`game.DetectTSpin`). A **180 spin** is any piece whose last move was a half turn (the Z key) and which rests **immobile** — unable to shift left, right or up, the walls and the locked cells blocking it (the wiki's immobile-twist rule, [harddrop.com/wiki/List_of_twists](https://harddrop.com/wiki/List_of_twists) "Rewards for twists") — or a T the corner rule would call a full T-spin; it scores, attacks and chains exactly as a full T-spin and the banner says `180 SPIN DOUBLE` (`game.DetectSpin`, `TSpin180`). A hard drop of zero cells is not a move, so a piece turned into its slot and hard-dropped in place is still a spin; any shift, soft drop, gravity step or real fall forgets the rotation. **Back-to-Back**: a difficult clear — a Jetris, or any T-spin that cleared lines — right after another difficult clear scores one and a half times; only a plain single, double or triple breaks the chain, while a T-spin with no lines or a piece that clears nothing leaves it alone. **Combo**: consecutive locks that each cleared lines; a lock that clears nothing ends the run. The combo and the chain are per player: on a shared board each player's sequence is their own, and the points go to the shared score.

Cooperative: the crew shares one score and the multiplier is the shared level (`totalLines` counts every clear on the board). The score no longer scales with the seat count — a Jetris is 800 × (level + 1) whether one or six play.

### Shared Score

There is a single shared score visible to all players. When any player's piece locks and scores — a clear, a T-spin that cleared nothing, or just the drop's points:
1. That player adds the points (and the cleared-line count) locally
2. An `EventLineClear` is published to NATS with the `Score` (the lock's points), `LinesCleared` (0 for a lock that only earned drop points), the clear's Guideline names (`t_spin` 0/1/2/3 — none, Mini, T-spin, 180 spin — `back_to_back`, `combo`, `perfect`) and the sender's cumulative own totals (`total_score`, `total_lines`)
3. All other players (and spectators) receive the event and fold the delta against the last totals seen from that sender into their local score **and** `totalLines` — so the shared level stays in sync on every engine; a lock with no lines moves only the score
4. All players' UIs update simultaneously, and the crew's HUD names the clear over the board (`alice: T-SPIN DOUBLE · B2B · COMBO 2` / `+2700`)

A player's `game_over` event carries the same cumulative totals, so the ending player's last points — a drop's, which only the next event would have carried — count on every engine before the game is archived.

### Individual Scoring (`meta.scoring: "individual"`)

The crew's one board can be a competitive one — the create wizard's **single
playfield, competitive**: the board, the pieces, the deal and the level are
shared exactly as above, but the score is not. Every seat keeps its own
(`ownScore`, the points of its own locks), the HUD shows yours and lists every
player's beside their name, best first, and a peer's `line_clear` folds the
crew's LINES (the shared level) but never their points. The game still ends
for everyone at the first top-out (or at the line goal), and the verdict is a
ranking: the seats' published totals — the cumulative `total_score` the ordered
events stream carried up to the ending event, the topper's own game_over
included — name the top scorer(s), a tie crowning everyone tied; the winners
see YOU WON! with the victory fireworks, the beaten YOU LOST, both over a
ranking line (`alice 1200 · bob 940`). The archive record carries
`scoring: "individual"`, every seat's score with `winner` on the top one, and
no shared `total_score`, so such games rank in a replay bucket of their own and
never against a crew's shared run.

### Level Progression

Level = `totalLinesCleared / 10`, capped at 19. Level affects gravity speed (see Section 7). Level is computed independently by each engine from its local `totalLines` counter, which increases on both local clears and received `EventLineClear` events.

### Game Over

When **any** player tops out (newly spawned piece cannot be placed **on locked cells** — a spawn covered only by another player's falling piece waits instead, see Piece Spawning), the game ends for **all** players:
1. The topped-out player publishes `EventGameOver` — retried past a connection blip rather than dropped: the announcement is what everyone else's game over runs on — and CASes the meta to `finished`
2. All other players' event consumers receive it and immediately transition to game over — and every seated player CASes the meta to `finished` too (idempotent, like a line goal's finish), so the finish never depends on the topper's client staying connected: a lost finish once left an open game listed, joinable and playing on for hours after its crew's top-out
3. All players see the "GAME OVER" overlay simultaneously; with a line goal the crew has lost (`GOAL MISSED · YOU LOST`, nobody crowned)

The overlay shows the team's final result — `Score: N (level L)`, the shared total — above the "Back to Lobby" button.

A solo game ends the same way at its one player's top-out (there is nobody else to end it), and its score is the shared total it competes with.

**High-score fireworks:** if the crew's shared score strictly beats the best archived co-op score **for the same number of players** (the `TotalScore` of past cooperative games in the lobby's GAME HISTORY; the very first co-op game at a seat count sets the first record, though a zero score never counts — so a solo game competes only with solo games), every crew member's screen plays the same victory fireworks show a competitive winner gets — a new best is a win for the whole crew. Ties don't count, and the finished game never competes against its own just-written archive record.

### Visual Indicators

For **every** square, `internal/render` computes a concrete fill color and an outline
color (`CellStyle` returns a `CellAppearance`), and the Gio drawer fills the cell and
strokes its border accordingly. The fill is the tetromino color
composited over the board background to match the desired brightness (active ≈0.9,
locked ≈0.7, adversarial ≈0.8). Empty squares fall back to the board background
with a thin grid-line outline, so literally every square has an outline.

- **Own piece:** 2px white outline around each active cell
- **Other player's piece:** Standard piece color, no ownership outline (from a player's perspective)
- **Locked cells:** dimmed piece color with a 2px per-player outline (non-adversarial)
- **Spectator view:** Per-player colored outlines on every active and locked cell (P0=cyan #00ffff, P1=magenta #ff00ff, P2=yellow #ffff00, P3=orange #ff8800, …)
- **No divider:** The board is rendered as one seamless playfield with no visual separator between player sections
- **8-bit block shading:** every filled square (active, locked, adversarial) is drawn
  as a classic 8-bit block — a lighter bevel strip along its top and left edges, a
  darker strip along the bottom and right, and a small gloss pixel in the top-left —
  while empty squares stay flat (`CellAppearance.Bevel`). Each playfield sits inside a
  chunky arcade-well frame.

The whole UI shares this "modern 8-bit" look: display text (titles, headers, buttons,
HUD stats, the countdown, the game-over dialog) is set in the embedded "Press Start 2P"
pixel font, chrome corners are square with thick borders and hard offset shadows, a
subtle CRT scanline overlay covers every frame, and the accent color throughout is the
NATS brand blue — with NATS.io logos and "made with NATS.io JetStream" branding on the login
screen, lobby/archive banner, and game HUD.

---

## 4. Competitive Mode

### Players

Competitive mode supports 2 or more players. Each player has their own independent playfield.

### Playfield

Each player has their own 10-column playfield, 20 visible rows tall (24 with the headroom) however many players are in the game — the same board every other mode is played on. Playfields are rendered separately (own board + opponents in sidebar).

### Piece Spawning

Each player gets their own piece spawn on their own independent playfield. Players share the same RNG seed (`meta.Seed`) and produce the identical piece sequence.

**Spawn position:** Centered at column 3, anchor row 2 for all piece types (lowest cell at row 3).

### Movement

Standard guideline-style movement. Collision detection is using CAS only.

### Gravity

Standard gravity with the same lock delay as cooperative mode (see §3 Lock Delay). There is no "blocked by active piece" logic since there's only one piece per playfield: when a piece can't move down it has landed, and it locks when the lock delay expires (or at once on a hard drop).

### Line Clears

A row is complete when all 10 cells are occupied (locked). Standard guideline-style rules apply. When one player clears a line, every other player gets a line added at the bottom of their playfield, so all of their already-locked rows shift up by one. The currently falling piece does **not** rise with the stack — it holds its on-screen position and is simply dropped into place as the stack rises to meet it. Only if the rising stack (or the new garbage) would overlap the falling piece is the piece pushed up, and then only by the minimum number of rows needed to clear the conflict. If that upward push would run the piece off the top of the playfield, the player tops out and is eliminated (see Game Over). Once a line is cleared and added to the other player(s), that added line can never be removed or completed.

### Scoring

Competitive scores by the same Guideline table (§2 Scoring): each player keeps their own score — their clears, T-spins, Back-to-Back, combos and drop points — multiplied by the level their **own** line total reaches (`lines / 10`, shown as the HUD's LEVEL; competitive gravity does not speed up with it). The score decides nothing: the winner is the last player left who has not topped out, and the score is kept for the leaderboard. No line-clear events are published in competitive; the score travels in the player's `game_over` event and the archive record.

### Shrink Attack

When a player clears 1 or more lines, garbage rows are owed to **all** other players still in the game — one row per cleared line, or under the game's `guideline_garbage` attribute (§1b) the Guideline table: 0, 1, 2 or 4 rows for a single, double, triple or Jetris, 2/4/6 for a T-Spin single/double/triple, a Back-to-Back bonus and 10 more for a perfect clear (`game.Clear.AttackRows`; a plain single then owes nothing and no register is touched). The attack is delivered through each victim board's **garbage register** — a durable, cumulative rows-owed counter that the clearing player advances with a CAS-add (read the latest total, publish `total + lines` expecting the read sequence; on a lost race, refresh and re-add). Because the register is cumulative and its writes serialize on CAS, two players clearing at nearly the same instant both land — the totals **sum**, nothing is trimmed or lost — and a victim that is briefly behind (high RTT, a reconnect, a late join) reconciles the full amount owed the moment it catches up.

- **Applying:** the victim applies its deficit (rows owed − rows applied) as one **txn-gated atomic batch** (§9): the locked stack shifts up, adversarial rows fill the bottom — solid in a 0-hole game, otherwise punched with the game's `garbage_holes` empty cells at one random set of columns shared by every row of the raise, or one draw per row in a `random_garbage_holes` game (§1b) — and the applied total recorded in the board's txn register advances — exactly once, regardless of duplicate signals or replays.
- **Clearing garbage:** a solid garbage row is permanent. A garbage row raised with holes is an ordinary line once its holes are filled: it is detected by the same completed-row scan at the filler's lock-in, collapses with the same clear transform, scores, and owes garbage to the opponents like any other cleared line.
- **Falling piece:** the victim's falling piece does **not** rise with the stack — it holds its on-screen position and is dropped into place as the stack rises to meet it (over the holes of a holed raise, it ends up sitting in them). It is pushed up only when the risen stack or garbage would overlap it, and then only by the minimum number of rows needed to clear the conflict. A push that would carry it off the top tops that player out (they lose).
- **Stack overflow:** if the shift pushes already-locked rows past the top of the board, the board is full — the player tops out (standard garbage death; the rows are not silently destroyed).
- **Raises override moves:** the gated batch's cells carry no CAS expectations, so a raise never loses to the victim's in-flight move; the move's own per-subject CAS then fails against the risen board and the move is dropped + flashed like any other lost move.

### Game Over

The game continues until only **one player remains** — in an open game, until only one board still has a seated player who has not topped out: a board whose player left is out. When a player tops out (the newly spawned piece cannot be placed, an opponent's raise pushed the rising stack into the falling piece and the conflict-resolving upward shift ran the piece off the top, or the raise pushed the locked stack itself past the top of the board), that player is eliminated and transitions to spectator mode — they can watch the remaining players continue. The last player standing wins. The game UI shows a player status list with each player marked as playing (green dot) or eliminated (red cross, struck through name). At game over, winners see "YOU WON!" and the loser(s) see "YOU LOST", and the overlay shows the player's own final result — `Your score: N (level L)` — above the "Back to Lobby" button. The winner’s screen also plays a **victory fireworks show** over the whole game screen: rockets rise from the bottom edge and every one bursts into a small **NATS "N" logo** (particles sampled from the embedded nats.io icon) — except one rocket in ten (guaranteed at least once per show, since the show loops a fixed choreography), which bursts into the **Synadia Symbol** instead (the official mark from synadia.com/about/brand — the white "S" swirl on the emerald rounded square), which pops in, holds for a beat, and then visibly splits into its small squares and blows apart — the blocks flying out in every direction like a bursting shell and shrinking away as they go, rather than fading in place. As they fly, each burst either keeps the logo’s own colors or transitions its blocks to one traditional fireworks color (gold, red, green, blue, purple, or silver) — chosen per rocket at random. The show loops until the winner heads back to the lobby, and is paint-only — it never blocks input or the "Back to Lobby" button.

---

## 5. Teams Mode

Two or more teams of equal size ("A" = team 0, "B" = team 1, "C" = team 2, …). **Within a team, play is cooperative** — teammates share one wide board with all the cooperative-mode mechanics (per-player sections, shared pieces as obstacles, merge-retry on the shared subjects). **Between the teams, play is competitive** — each team has its own independent board, line clears send garbage to an opposing team's board (unclearable unless the game was created with garbage holes, §1b), and the last team with a player standing wins.

### How Many Teams

`meta.team_count` (`GameMeta.TeamCount`, the create wizard's step-1 **Teams** slider, `config.MinTeamCount`..`MaxTeamCount` = 2..6) is how many teams the game is played between. **Two — Team A vs Team B — is the default**, and a meta without the field (every game created before the slider) reads as two, so an archived duel still rebuilds exactly as it was played. Everything below is written for the general case; at two teams every rule reduces to the game teams mode has always been.

### Players & Teams

`TeamSize` players per team; `PlayerCount = TeamCount × TeamSize` total. Players **choose their team when joining** (one Join A / Join B / Join C … button per team in the lobby, each shown while that team has room); a join on a full team — or on a team index the game does not have — is rejected. The game transitions to `starting` when every team is full. Each player keeps a **global** roster index (`PlayerIdx`, used for piece ownership and colors) plus a **team slot** (0..TeamSize-1, join order within the team) that selects their spawn section on the team board.

### Playfield

One shared board per team: width `10 + (teamSize − 1) × extraColumns` (§2 — 14 columns for a team of 2 at the default setting of 4), 20 visible rows (plus 4 headroom rows) like every other board — however many teams are playing and however hard they attack, the height is the same. Cell subjects are scoped by team (`…team.<idx>.playfield.cell.<r>.<c>`), so the boards are disjoint subject trees and each one behaves exactly like the cooperative shared board for its members.

### Piece Spawning

Coop rules per team: player at team slot N spawns centered in their section at column `N×extraColumns + 3`, anchor row 2. Every player runs the full 7-bag sequence from the shared `meta.Seed` with an independent piece index (the coop scheme), so every team sees the identical, fair piece sequence.

#### Split pieces (`split_pieces`)

A teams game can be created with the pieces **dealt out between the teammates** instead of every seat running the same full bag: `split_pieces` (`GameMeta.SplitPieces`, the create wizard's step-2 custom-rules checkbox "Distribute the pieces between the players of a playfield", off by default and never set by the Guideline preset, mirrored on the lobby row as a `split pieces` tag). Each seat is dealt a **ration** of one or more piece types and its sequence is a bag of that ration alone — three types means those three, shuffled, forever; one type means that one piece, forever. The deal's rules:

- **Every one of the seven types goes to somebody, and nobody is left empty-handed.** The team between them still has the whole bag; it just has to co-operate to use it, because the teammate holding the I is the only one who can hand the board an I. With two seats one holds four types and the other three, with seven they hold one each, and past seven the types start doubling up (`rng.PieceSets`).
- **The deal follows `meta.Seed`, so it is the same on every peer that computes it** — both teams' boards, a spectator's engine, an agent's own port of the function. In particular **team A's slot N holds exactly what team B's slot N holds**, which is what keeps the match fair, exactly as both teams have always seen one identical piece sequence.
- **The ration also seeds the sequence** (`rng.NewSet`: the game's seed mixed with the ration's piece mask), so seats holding different rations draw independently while the same ration draws the same order wherever it is held. A game without the setting is bit-for-bit the sequence it always was.
- The setting **needs teammates to split between**: it is recorded only for a teams game of two or more per team (`GameMeta.SplitsPieces`). A team of one, or any other mode, ignores it — the wizard does not even offer the box there.
- Everything else is unchanged: the preview (§1b) shows the seat's OWN next pieces, so it only ever reveals types that seat holds; the hold queue holds what the seat was dealt; and agents read `split_pieces` and play their own ration like any other peer.

### Movement, Gravity, Hard Drop, Lock-In

Identical to cooperative mode, scoped to the team board: teammates' active pieces are obstacles, a piece blocked downward only by a teammate's active piece waits instead of locking, and all engine-driven shared-cell writes use CAS merge-retry.

### Line Clears & Scoring

Coop scoring within the team: a lock scores the Guideline's points (§2 Scoring, at the team's level) to the **team score**; every teammate folds the clearing player's points **and line count** from the line-clear event — a lock that only earned drop points announces itself the same way, with no lines — so the team's level (and gravity speed) stays in sync for all members. Other teams' clears do not affect your own team's score.

In addition to the own-team score, **every** engine — every team's players, eliminated players, and spectators — folds **every** team's line-clear events into a per-team scoreboard: a `TEAM A` / `TEAM B` / … score total **and** a per-team cleared-line total, from which each team's level is derived. Every team's score (and, for spectators, level) is therefore visible and live on every screen. Line-clear events live on per-sender subjects and carry the sender's **cumulative** totals, and receivers fold deltas against the last total seen from that sender — so a trimmed intermediate event is subsumed by the next one, and a spectator who joins mid-game reconstructs the full scoreboard from each sender's last retained event.

### Garbage Attack (team shrink)

When a team clears N lines, N adversarial rows — or the Guideline table's rows in a `guideline_garbage` game (§1b: 0/1/2/4 for a plain clear, more for a T-spin, a Back-to-Back or a perfect clear) — are owed to **one opposing team's** shared board, delivered through that board's garbage register exactly as in competitive (§4): the clearing player CAS-adds the cumulative rows-owed total, so overlapping attacks sum and none is ever lost.

**Which opponent, past two teams.** In a duel there is only one answer and the rule never comes up. With three or more teams a raise still weighs exactly what it always did: it goes to **one** opponent, and each attacker **rotates** through the others — attack 1 to the next team up, attack 2 to the one after, wrapping around and skipping its own. (Giving every opponent the full raise would multiply the garbage in play by the number of teams and end a six-way game in a minute; rotating keeps a team both sending and receiving what it would in a duel.) The rotor is per **engine**, so a team's several players spread their own attacks independently, and over a game the raises land evenly across the opponents. Nothing on the wire changes: the victim reads its own garbage register and cannot tell — nor need it — which of its opponents was on rotation. The rows land solid (permanent) or punched with the game's `garbage_holes` (§1b) — one random column set per raise across the whole team-wide row, or one per row with `random_garbage_holes` — and a holed garbage row a teammate fills clears like any other team line. Application uses the same txn-gated transform, with three shared-board specifics:

- **Any teammate applies; the gate makes it exactly-once.** Every alive member of the receiving team may react to the register. Each applies the deficit as a txn-gated batch, and the gate's per-subject CAS admits exactly one — a loser's entire batch is atomically rejected (nothing stored), and its recompute from fresh state finds the deficit already zero. No merge-retry, no double-shift.
- **Pieces are pushed up, never crushed.** Every falling piece on the board — whoever owns it — holds its on-screen position unless the risen stack or garbage overlaps it, then lifts by the **minimum** rows that clear the conflict. Lifts **cascade**: a lifted piece is an obstacle for the pieces above it, so a rising stack can push a whole column of stacked falling pieces upward, each moving just enough; a piece is never merged into the risen stack. A piece pushed off the top eliminates its owner — the batch's txn record lists them (delivered before the vacating cells), so the owner's engine treats the resulting zero-active edge as an elimination, not a lock-in. If the shift pushes **locked** rows past the top, the whole board is full and every remaining player on it is eliminated (the team is out).
- **Teammate moves can't be corrupted.** The applier's batch guards every other player's piece cells — and the headroom rows a teammate's fresh spawn could claim — with CAS at its snapshot sequences; a piece it leaves untouched is guarded by an expectation carrier instead. A teammate move that lands first atomically rejects the whole batch, and the recompute sees the piece's new reality. The raise therefore overrides in-flight moves (their own CAS fails against the risen board — drop + flash) without ever burying or duplicating a mid-flight piece.

### Elimination & Game Over

**A player out is not a team out.** When a player tops out (their next spawn cannot be placed on locked cells — a spawn blocked only by a teammate's falling piece is deferred, not fatal — or a garbage raise pushed their falling piece off the top; a raise that pushes locked rows past the top takes the whole board and every remaining player on it), they vacate any of their active cells from the team board (a txn-gated transform, so a racing garbage application can never resurrect the dead piece from a stale snapshot), publish their elimination, and become a spectator of their own team's board — but their teammates play on. The UI shows "YOU'RE OUT — your team plays on" until the game resolves.

A team is **out when ALL its members have topped out** — or, in an open game, when nobody holds a seat on it any more — and the game is over when **at most one team is still standing**. In a duel that is the moment either side falls; past two teams the survivors play on, a fallen team simply stops attacking and stops being attacked, until only one team is left. That last team — every member of it, alive or already eliminated — wins: alive winners stop playing, and an eliminated member of the winning team sees their "you're out" flip to "YOUR TEAM WON!". Everyone else sees "YOUR TEAM LOST"; the last teams falling together is a draw, nobody crowned. In every case (and on the interim "you're out" box) the overlay shows every team's score and level with the player's own team first — `TEAM A 42 (lvl 3) · TEAM B 17 (lvl 1)` — above the "Back to Lobby" button. All engines observe the same ordered event stream, so they reach the same verdict; the meta transition to `finished` is CAS-deduplicated across the winning engines. Every member of the winning team — including already-eliminated members, whose engines re-emit the win — gets the same victory fireworks show as a competitive winner (rockets bursting into small NATS "N" logos that then blow apart) on their own screen.

### Visual Indicators

- HUD shows `Teams · TEAM A/B/…`, a live per-team scoreboard (one row per team, own team highlighted), and the team level; spectators instead see each team's score **and level** inline (`42 · lvl 3`) with no single SCORE/LEVEL stat
- When the game reveals upcoming pieces (§1b), players also get the **NEXT well** beside their playfield with their own queue as mini piece tiles — and, in a game with the hold rule, the **HOLD box** off the playfield's other side (HOLD left of the playfield, NEXT right of it — and the on-screen pad's D-pad and buttons under them), showing the set-aside piece (dimmed once the hold is spent for the piece in play)
- Legend groups players under TEAM A / TEAM B / … headers with their global player colors; eliminated players are marked `(out)`. In a `split_pieces` game each name carries the seat's **ration** under it — its piece letters, each in that piece's own color (`I O Z`) — for every seat on every team, so a player can see at a glance who on their board can supply the shape the stack is waiting for (and which opponent is holding it)
- Every opposing team's board renders in the sidebar, each labeled with its own team ("TEAM B", "TEAM C", …)
- Spectators see every team's board side by side

---

## 6. Game Lifecycle

### Login & Server Selection

There is a single login screen where the player both picks where to connect and enters their name — no connection is made until they hit Play:

- If NATS CLI contexts are defined on the machine, a "Context:" option offers them in a pull-down button. The pull-down starts on the currently selected context (per `nats context select`); opening it lists every context — the selected one labeled "(selected)" — and picking one closes the list and makes it the choice.
- A "NATS URL" option is **always** available, pre-filled with `nats://demo.nats.io:4222`; typing in the URL field selects it automatically.
- A "LAN party mode (embedded NATS server)" option is **always** available: hitting Play with it selected starts a JetStream-enabled NATS server **inside the Jetris process itself** — default account, no authentication of any kind, listening on all interfaces on the port entered in the option's "Port:" field (default 4222; editing it selects the option automatically), storing its stream data in the local `jetstream-data` directory. The option's "IP:" field holds the address the game advertises and connects through — pre-filled with the machine's auto-detected LAN address and editable, since on a multi-homed, VPN'd or containerised machine the detected one may not be the address other players can reach (the server listens on every interface regardless, so any address that actually reaches the machine works; clearing the field detects it again at connect time). While the option is selected the login screen shows the shareable URL built from those two fields (`Your server's URL is nats://<ip>:<port>`), and once connected the lobby shows it too (`YOUR SERVER'S URL IS nats://<ip>:<port> — share this address so others can join you`) so the host can invite other players, who connect to that address via the NATS URL option. The embedded server keeps running until the window closes, so quitting to the login screen doesn't kick connected friends; logging back in with a **different** port restarts it on the new port.
- The `--server`/`--context` flags don't connect directly — they only set the picker's starting choice: `--server` selects the URL option and replaces the default URL text with its value; `--context` picks the context option with the pull-down preset to that context.
- A **Check connection** button tests the current choice without joining: it makes a real NATS connection, measures the **Core NATS ping** — the round trip of a real message published to an inbox subscription, not a protocol-level ping — shows `✓ <server> · Core NATS ping <rtt>` in green under the button (or the error in red), and disconnects. With "LAN party mode (embedded NATS server)" selected the button reads **Check embedded server**: it starts the embedded server (or reuses the running one), connects to it over the same LAN address other players dial, pings it, and reports `✓ serving on nats://<lan-ip>:<port> · Core NATS ping <rtt>` — or, if a stranger already holds that port, says so instead. A result stops being shown as soon as a different connection option is selected, since it no longer describes what the button would check.
- Hitting Play connects with the chosen context/URL and logs in; a connection failure keeps the player on the login screen with the error shown so they can retry with a different choice.
- Quitting the lobby disconnects and returns to this same screen, so the player can connect to another server.

```
created → starting → [countdown] → in_progress → finished → archived
                                                      ↑
                                                  cancelled
```

### Transitions

| From | To | Trigger |
|------|----|---------|
| — | created | Player finishes the create-game wizard (open, or invite-only per its who-can-join step) |
| created | starting | The table is ready: an invite game once every seat is filled and every player has clicked READY (a join into an already-full game is refused); an open game once every playfield has a player who clicked READY — its first ready player on a single playfield, one per team or board with several — whatever seats stay free |
| starting | [countdown] | The ready toggle that completed the table runs the countdown |
| [countdown] | in_progress | 5-second countdown completes |
| in_progress | finished | Game over (top-out) |
| finished | archived | Archive record published, game stream deleted (5s delay) |
| created/starting | cancelled | Creator absent, all players absent, or cleanup (an open game only after two weeks without activity) |
| any | (deleted) | A player deletes an **abandoned** game from the lobby (see below) |

### Abandoned Games

Some games go nowhere: the creator never joins, the players never click READY, or everyone walks away mid-game. While the lobby is up, every client re-checks the listed games once a minute (`AbandonedCheckInterval`) and flags a game as **abandoned** when either rule holds:

- **Never started** — the game is still `created` or `starting` more than **15 minutes** after creation (`AbandonedUnstartedTimeout`).
- **Started, then deserted** — the game is `in_progress` but its game stream has seen **no messages for one minute** (`AbandonedIdleTimeout`; a live game publishes constantly, so a silent stream means every player is gone). An `in_progress` listing whose stream no longer exists is flagged immediately — it can never make progress.

Neither rule applies to an **open** game (§ Creating a Game below): its seats come and go, mid-game included, so an empty table or a quiet stream means nothing. An open game is abandoned only when **nothing has happened to it for two weeks** (`AbandonedOpenTimeout`) — no change to its listing (a join, a leave, a ready; the lobby KV revision's time, which every client's watcher keeps) and no message on its stream. A started open game whose stream is gone is still flagged at once. The login-time cleanup pass (`internal/cleanup`) leaves an open game alone on the same terms: it cancels or finishes one only once the lobby calls it abandoned.

The check rebuilds the flag set from scratch each pass, so a game where activity resumes (e.g. a player reconnects) is un-flagged again.

An abandoned game's lobby row is marked `· abandoned` in red and grows a red **Delete** button next to Join/Spectate. Clicking it replaces the row's action buttons with a confirmation on its own line under the game info (so the question never squeezes the info text) — **"Are you sure you want to delete this game?"** with **Yes, delete** / **Cancel** — so a stray click can't destroy the game. Confirming tears down everything the game left behind: the per-game stream, the game's chat messages in the shared chat stream, and the lobby KV listing (whose deletion removes the game from every player's list).

### Creating a Game: the Create Wizard, Open vs. Invite-Only

Games are created through a **create-game wizard**: the lobby carries a single
**"Create a new game"** button (drawn with the shiny "attract" treatment — an
embossed bevel and a diagonal glint sweeping across it every few seconds, so
the lobby's main call to action can't be missed) that opens a modal walking the creator through the
game's attributes one step at a time — **1. game type**: a **single
playfield** everyone shares — the number of players, and with two or more
the choice: **co-op**, all players scoring together (cooperative mode, §3),
or **competitive**, each player scored individually (the same board with
`scoring: "individual"`, §3); one player alone is a solo run for the high
score, nothing to score together or apart; or
**multiple playfields** — the **Playfields** slider, 2 to 6, default 2, and the
number of players per playfield: one per playfield is competitive mode (a board
each, the last standing wins, §4), two or more a teams game, a team on each
board, every team scoring together (§5), the total seat count being playfields
× per playfield.
**2. game rules**: the **game length** — **until top out**, or **a number of
lines** (default 40, the line goal of §2) — then a single radio: the
**Guideline** preset — the default, listed read-only: the play rules at their
Guideline settings, the board 4 columns wider per player and no taller, every
player of a playfield playing the same full bag — or **custom**, every rule
opening at the preset's setting: for a playfield with company the
**board-growth sliders** (**extra columns per player**, 4 to 10, default 4, so a
co-op pair or a team of two plays 14 columns wide, three 18, and the slider's
top of 10 restores the historical full 10-column section per player — the same
step spaces the seats' spawn points, §2, §3, §5 — and **extra rows per
player**, 0 to 10, default 0, the playfield 20 rows plus that much per player
after the first) and the **"Distribute the pieces between the players of a
playfield"** checkbox (off by default — every player plays the same full bag
unless the creator checks it, and then the seven piece types are dealt out
between the seats of a playfield, each seat only ever playing its own ration,
§5), then
the next-piece count, 0-6, the "Hold piece" and "Show ghost piece" checkboxes,
the garbage rules where several playfields raise garbage at each other, the
piece bag and the hidden rows — see §1b.
**3. players** (for a teams game first the **team names** — every
playfield's team is called after a piece colour, Cyan, Yellow, Purple, Green,
Red, Blue, unless the creator types another, 16 letters at most, stored as
the meta's `team_names` and shown wherever a team is named; then **invite
only** or **open**, and for an open game the agent policy below). Each step has Next/Back plus a Cancel that closes the wizard
without creating anything, and the previous run's choices are the next run's
defaults.

Step 3 decides how the wizard ends. A game is **invite-only** or **open**:
choosing invite-only ends the wizard with the Next button reading **"Choose
players…"** and hands off to the invitee picker — an invite-only game's agent
policy is per-invitation; choosing open shows the agent policy right there and
the button reads **"Create game"**.

- **Open games** list in the lobby with Join/Spectate buttons, and the
  **"Allow agents to join"** checkbox and max-agents count (per team in a
  teams game) under the choice control whether idle agents may take seats,
  with **"Agents pause when alone"** beneath them for an agent left as the
  only player to wait for company (§11). An open game's roster is
  DYNAMIC — players can enter and leave at any time: the game starts as soon
  as every playfield has a player who is ready (the crew's one board, every
  team, every competitive board), whatever seats are still free; a free seat
  of a running game is anyone's — the joiner plays on the live board at once,
  no ready step; and leaving a running game (see Leaving and Rejoining)
  takes the leaver's piece off the board and frees their seat. Seats are
  STABLE — the lowest free one is taken, a departed player's seat is the next
  one taken, nobody else's moves — so a cell's player index names one seat
  for the whole game. A piece left behind by a player who crashed or dropped
  off is vacated by the other players' engines once it has stood still for
  10 s (a live piece never does), or the moment its seat is seen empty; and
  with several playfields, a playfield nobody holds a seat on any more is
  out — the last one left wins.
- **Invite-only games** are joined by invitation only. Creating one opens an
  **invitee picker** over the lobby. There is no send button: **selecting a player
  sends their invitation at that moment**, and deselecting a still-pending player
  (or setting them back to **—** in teams) retracts it (their pop-up disappears).
  The picker's first row is **you**: by default the creator is listed
  **unselected** — you host as a spectator (the row reads **spectating when the
  game starts**) and will watch once the game fills. Select yourself to also take
  a seat and play (team A by default in teams mode, switchable to any team), and the row
  switches to **joined ✓**. Below you the
  picker lists every OTHER player **currently idle in the lobby** (players already
  in a game can't be invited — you can only invite people free to play). The list
  is **live**: players who enter the lobby appear, players who leave drop out
  (except those involved with THIS game — roster members and invitees stay listed),
  and each row shows its invitation's state as it changes: **✉ invited — waiting…**,
  **joined ✓** / **joined · ready ✓** (the row's control disappears — the seat
  answers for them), or **✕ declined** (re-selecting re-invites). For
  competitive/cooperative games each row is a simple **Invite** checkbox; for
  **teams** games each row is a selector (**— / A / B / …**, one entry per team) so you invite
  each player to a specific team (changing the team re-invites them to the new
  one). A prominent header line tallies the seats live, broken out so it's
  obvious at a glance — **k/size seats filled — j joined · p invited · o open**
  (one such line per team in teams mode) — and the picker refuses a selection
  that would over-fill the game or a team. **Close** puts the
  picker away without touching anything (the game keeps filling; its lobby row
  carries the same live status); **Cancel game** retracts every outstanding
  invitation and deletes the just-created game. The creator can always **re-open
  the picker** later: while the game still has open seats its lobby row carries an
  **Invite** button (so more players can be invited after the creator has joined
  and gone back to the lobby — the picker reopens with the pending invitations
  and the creator's own seat already reflected). **When the last seat fills, the
  picker hands the creator over automatically**: to the game screen (ready
  selection) if they kept their own seat, or to **spectating** the game if they
  deselected themselves. The invited game's lobby row is tagged **· invite only**
  and shows no Join button to uninvited browsers (the creator, and anyone holding
  a pending invitation, still see theirs); for the creator the row also lists each
  outstanding invitation's state — **✉ invited — waiting…** with an **Uninvite**
  button (retract while unanswered), or **✕ declined** with a **Dismiss** button —
  and marks roster members **(joined)** / **(joined · ready ✓)**.

### Invitations

An invitation is a small record the inviter writes to the invitee's per-game lobby
mailbox key (`invites.<invitee>.<gameID>`). **A player may hold invitations to
several games at once** — one per game — and each expires after two minutes.
Because every lobby watches the same store, both sides see every change
immediately; the key's lifecycle is the invitation's state machine: written =
pending, deleted by the invitee = accepted (joining consumes it), rewritten with
`declined: true` = declined (kept so the inviter sees the refusal until they
dismiss it), deleted by the inviter = retracted.

- **A human invitee** gets a **pop-up** — *"<inviter> invited you to a
  competitive/co-op/teams game"* (teams names the team) — showing **who has
  already joined** (the game's current roster, with team and ready marks, or "No
  one has joined yet"), with **Accept & Play** and **Decline**. Accepting joins
  the game (and, in teams, the team the inviter chose); declining marks the
  invitation declined for the inviter to see. With several pending invitations
  the pop-up shows the oldest first and the next one surfaces once it's answered.
  If the inviter retracts a pending invitation, the pop-up simply disappears.
- **An agent invitee accepts automatically**: a resident agent treats a pending
  invitation as the strongest join signal and joins the invited game (and team) at
  once. Inviting an agent is how you bring a *specific* agent into an invite-only
  game — and it is the DEFAULT way agents get into games at all: a resident
  agent (e.g. `golang-mk1`) only joins games it is invited to unless started with
  `--auto-join`, which restores active scanning for open agent-allowed games. If
  the join can't be honored (the invited team was already filled by other
  invitees, or the game filled without it), the agent **declines** the invitation and
  goes back to the lobby rather than retrying it — a stale invitation never wedges an
  agent.
- **The invitation is also permission**: an invited player joins even a game whose
  agent policy would otherwise exclude them (an invited agent bypasses the max-agents
  limit — the creator explicitly chose them). Uninvited players and agents who try to
  join an invite-only game are refused.

Third-party agents accept invitations by watching their own lobby mailbox keys —
see `jetris-agent-guide.md`.

### Lobby Events

Alongside the KV state, every lobby action is announced as a **transient core
NATS message** (deliberately captured by no stream — these are real-time signals,
not state) on `jetris.lobby.event.<kind>`:

| Kind | Published when |
|------|----------------|
| `game.created` | a game is created |
| `game.joined` | a player takes a roster seat |
| `game.left` | a roster seat is freed (un-join) |
| `invite.sent` | an invitation is written |
| `invite.retracted` | the inviter retracts/dismisses an invitation |
| `invite.declined` | the invitee declines |

The payload is `{kind, game_id, player_id, target_id?, team?, time}`. Every lobby
subscribes and turns foreign events into immediate refresh pings, so player
availability, rosters, and invitation state update in real time (the KV watcher
remains the source of truth; presence heartbeats alone would lag by seconds).

### Presence & liveness

Each player writes a presence entry (`players.<id>`) to the lobby KV and refreshes
it on a heartbeat. **Liveness is enforced by the KV itself, not by any client
watching timestamps:** every presence write carries a per-key **TTL of 5 minutes**,
so if a client crashes or drops off the network the server deletes its entry a few
minutes after the last heartbeat and every other client's KV watcher gets a delete
event — the player simply disappears from the lobby, no last-seen bookkeeping. When
a player **actively leaves** (quits the lobby / closes the window) the client
deletes its own presence key right then, so others see the departure immediately
rather than waiting out the TTL. (Game listings and invitations live in the same
bucket but are written without a TTL, so they persist until explicitly removed.)

### Leaving and Rejoining a Game

"Back to Lobby" does not give up your seat while the game is alive:

- **Before the game starts**, going back to the lobby **clears your READY mark**
  (you can't be counted ready while away) but keeps your roster seat. The lobby
  row shows the game's status as **joined** (green) with a **Rejoin** button in
  place of Join — clicking it puts you back on the game screen, same seat and
  team.
- **Once the game is in progress**, "Back to Lobby" first asks **"Are you sure
  you want to leave?"** (Yes, leave / No, keep playing) — the game keeps going
  without you. In an **invite-only** game the lobby row then shows **playing**
  (green) and the same **Rejoin** button returns you to your live board (the
  game stream replays the current state). In an **open** game leaving frees
  the seat: your falling piece is taken off the board first (a CAS with retry
  — it is yours, nothing else moves it), then your seat is removed from the
  roster for anyone to take; the lobby lists the game to **Join** again.
  Either way the board you return to is the game as it stands now: the
  replayed history brings the scoreboard up to date silently — a clear made
  before you came back is neither flashed nor named on the award banner,
  only the next one is — and your own share of it is picked back up from
  the last clear you announced, so the totals your next clear carries
  continue from where they were and the crew counts it.
- Presence-wise you stay marked **In Game** while you hold a live seat (so you
  can't be invited elsewhere); the seat is only released when the game ends, or
  when you free it explicitly (deselecting yourself in the invite picker, or an
  agent un-joining a game that never starts).

### Ready Flow

1. Players join the game and see the game page with a "WAITING FOR PLAYERS" header
2. Each player's ready state is shown as a filled pill badge next to their name: green "READY" / red "NOT READY"
3. Players toggle their own state by clicking the button, which reads "CLICK WHEN READY TO PLAY" (when not yet ready — drawn with the shiny "attract" treatment: an embossed bevel and a glint that sweeps across it every few seconds) / "CLICK IF NOT READY ANYMORE" (when ready, i.e. click to stand down; plain button, no glint)
4. Ready state is stored in the KV game listing with CAS (prevents lost updates)
5. When the table is ready — an invite game: every seat filled and ALL players ready; an open game: every playfield with at least one READY player — a single playfield starts on its first ready player, a multi-playfield game once every team (board) has one — whatever seats stay free and whoever else is seated without having clicked (they play from the start, like a later joiner; the bar names what it still waits for: `WAITING FOR A READY PLAYER ON TEAM B`) — the countdown begins and the ready toggle is locked. In an open game the one ready toggle that moves the listing from `created` to `starting` elects its client to run the countdown, so a player joining during the countdown never starts a second one; a player joining a running game gets no ready step at all
6. **Countdown:** 5...4...3...2...1...GO! (published to NATS countdown subject, consumed by all engines), drawn as a big numeral centered over your own playfield
7. After "GO!": game meta transitions to `in_progress`, pieces spawn

### Archive Record

Published to `JETRIS_ARCHIVE` stream when a game finishes:

```json
{
  "game_id": "uuid",
  "mode": 0,
  "player_count": 2,
  "players": [
    {"player_id": "Alice", "score": 6850, "level": 2, "lines": 14, "piece_count": 30, "winner": false},
    {"player_id": "Bob", "score": 6850, "level": 2, "lines": 9, "piece_count": 25, "winner": false}
  ],
  "started_at": "2026-03-21T10:00:00Z",
  "finished_at": "2026-03-21T10:05:30Z",
  "total_score": 6850,
  "final_level": 2,
  "boards": [ /* end-of-game playfield snapshot(s) — see below */ ],
  "chat": [ /* the game's chat history, preserved before the purge — see below */ ]
}
```

Each player result carries the `level` achieved at game end (derived from that engine's line total; sent in `EventGameOver`), the `lines` the player's own pieces cleared (the same event's `total_lines`; absent in records written before the field) and an `agent` flag (from the roster at archive time) marking seats that were played by agents. Cooperative records carry the shared `total_score` and `final_level`; the history list shows them plus per-player scores, and competitive history lines show each player's score and level.

**History controls:** the lobby's GAME HISTORY header carries a sort selector — **By score** (headline score, the default) or **By date** (most recently finished first) — and an **"Agent games"** checkbox (checked by default); unchecking it hides every game that had at least one agent seat. Records from before the agent flag existed read as all-human. Each row's MODE column also carries a **crew line** telling the two kinds apart at a glance: **HUMANS** (green) for human-vs-human games, **WITH AGENTS** (orange) when any seat was an agent. When the listed history contains teams games, a **TEAMS OVERALL** standings line sits between the header and the table — each team's total wins (draws credit neither side) and summed points across those games (e.g. `TEAM A 3W · 12400 PTS — TEAM B 1W · 6100 PTS`), with the leading team (by wins, points as the tie-break) in gold; the agent filter applies to the standings too.

The history is laid out as an **arcade high-score table**: a pixel-font column header (**SCORE · TIME · MODE · PLAYERS**) over one ruled row per game (a thin line delimits each game, so a row's PLAYERS column can wrap freely without blurring into the next). Per row: the headline **SCORE** is the largest figure, in gold pixel numerals, with the achieved level beneath it; **TIME** is the duration (prominent) over the start date; **MODE** names the game type over its player count (or `2v2` for teams); and **PLAYERS** puts the winner(s) first — a trophy and a gold name — with everyone else listed muted below. The headline score is the co-op total, the best team total for teams, or the best player score for competitive. The list is ordered by that headline score, highest first; when two games tie, the one with the **shorter game time** ranks higher, and any remaining tie shows the most recently finished game first.

Teams games additionally carry `team_size`, `winning_team` (0 or 1; -1 = draw or not a team game), a `team` field on each player result, and the final per-team totals `team_scores` and `team_levels` (indexed by team, taken from the archiving engine's converged per-team scoreboard) — these are what the history list shows for a teams game (`A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave`). Every member of the winning team has `winner: true`, eliminated members included — a team win is shared.

**End-of-game playfield snapshot.** The record also carries `boards`: a snapshot of every board exactly as it stood when the game ended, captured by the winning/finishing client from the game stream (latest message per cell) just before that stream is deleted. There is one board for cooperative, one per player for competitive, and one per team for teams mode — so the snapshot is complete for every mode. Each board stores its width, visible height, and the non-empty cells (the raw cell messages). In the lobby, each game in **GAME HISTORY** has a **"View board"** button that opens a viewer redrawing these boards — the picture of the playfield at the moment that game ended. To the **left** of the boards the viewer lists the game's players in their board colors, with the winner(s) highlighted — a trophy and a gold name: competitive survivors, or every member of the winning team (teams are grouped under color-matched TEAM A / TEAM B headers, the winning one in gold). Cooperative players — one shared board — simply list under a PLAYERS header with no per-player color or winner. When the game has enough boards that they are together wider than the window, the board strip is **horizontally scrollable** (a scrollbar appears) rather than letting a board spill off the edge.

**Preserved chat history.** The record also carries `chat`: the game's conversation (each line's sender, text, timestamp, and spectator mark, capped at the most recent 200 lines), copied out of the archiver's chat log just **before** the game's messages are purged from the shared chat stream — after the purge the record is the only place the conversation survives. The archived-game viewer always shows a **GAME CHAT** panel beside the boards (spectator lines marked "(spec)", times in the viewer's local clock); a game with no recorded conversation — including records from before this field existed — says so in the panel instead of hiding it.

---

## 6b. Chat

There are two chat scopes, sharing one NATS stream (`JETRIS_CHAT`) and distinguished purely by the game-ID token of the subject (`jetris.chat.<gameID>`): the **lobby chat** (the reserved game ID `lobby`, i.e. `jetris.chat.lobby`, shown on the lobby screen) and a **per-game chat** (`jetris.chat.<gameID>`), seen only by that game's players and spectators.

On the game screen (player or spectator) a chat strip is displayed at the bottom:

- It shows the game's messages, plus the lobby chat folded in — lobby lines are prefixed `@lobby` and rendered in a distinct color so they're obviously not from the game. Spectators' game messages are marked `(spec)`.
- Players and spectators can type at any time — before the game starts and while it is in progress.
- **While a player's game is in progress** the keyboard is shared between the piece and the chat, and a click decides who has it: clicking the chat panel hands the keys to the chat (the panel gets a white ring and the editor's hint changes accordingly), clicking anywhere else — or pressing Esc while typing — hands them back to the piece (the playfield's frame turns white), and Tab (shifted or not) switches either way without touching the mouse. The keys jump to the board the moment the game becomes playable (start or rejoin), so a player mid-sentence during the countdown isn't caught out.
- A message starting with `@lobby` is sent to the lobby chat (everyone sees it); anything else goes to the game's chat.

Lobby chat history is retained for 7 days; a game's chat messages are purged from the stream when the game is archived — but not lost: the archiver copies the conversation into the game's `ArchiveRecord` first, so the archived-game viewer can replay it (see §Archive Record). A player joining the lobby replays the retained history (up to the last 200 messages), so everyone in the lobby sees the same chat log regardless of when they logged in.

---

## 7. Gravity Speed Curve

The Guideline speed curve. With `L` the Guideline level (Jetris levels start at 0, so `L = level + 1`), the time a piece spends on each row is

    seconds per row = (0.8 − (L − 1) × 0.007) ^ (L − 1)

rounded to the millisecond and floored at one 60 Hz frame (≈17 ms): the engine moves a piece one row per gravity tick and each tick is a JetStream batch, so the sub-frame intervals of the Guideline's highest levels cannot be honoured row by row (`game.GravityInterval`).

| Level | Interval |
|-------|----------|
| 0 | 1000 ms |
| 1 | 793 ms |
| 2 | 618 ms |
| 3 | 473 ms |
| 4 | 355 ms |
| 5 | 262 ms |
| 6 | 190 ms |
| 7 | 135 ms |
| 8 | 94 ms |
| 9 | 64 ms |
| 10 | 43 ms |
| 11 | 28 ms |
| 12 | 18 ms |
| 13+ | 17 ms (one frame) |

Competitive mode stays at level 0 (1000 ms); shared boards (cooperative and teams) level up every 10 lines.

---

## 8. Spectate Mode

Any player in the lobby can spectate an in-progress game:
- Engine starts in `ModeSpectator` (no gravity, no move input)
- The game page hides all controls and the ready button
- Shows "Spectating" status
- **Player legend:** Shows each player's name with their assigned color swatch
- **Colored outlines:** Each player's active piece has a distinct colored outline (not white)
- Spectators see the same real-time playfield updates as players
- **Countdown:** the pre-game 5..0/GO! countdown is drawn over the spectator's boards exactly as over a player's board (a spectator who joins before the game starts sees the same start moment everyone else does)
- **Eliminations:** while the game goes on, an eliminated player's board (competitive) carries a centered **OUT** chip in the spectator's multi-board view; the board itself stays fully visible under it
- **The ending is the replay's reveal, live:** the moment the game is decided — the last player standing, a team fully out, the co-op crew topped out — the winning board wears the **winner show**: its frame pulses, a **WINNER** / **WINNERS** (co-op: **GAME OVER**) banner rises out of the well and floats under a **trophy graded by the game's rank** in its replay bucket (the same "By score" ranking behind the history's TOP 10 mark — the bucket's best game a LEGENDARY holographic gold cup with halo rings and sparkles, the top three EPIC gold in a purple aura, the top ten RARE silver with a shine sweep, a plain bronze cup below that) holding a **tetromino chosen by the same rank** from the internet's worst-to-best ranking of the seven pieces (the I for the bucket's best game, then the T, L, J, O and S, and the Z for a game nobody ranks); the beaten boards read **OUT** behind a wash; the winners' names go gold in bold italic with a 🏆 on the boards and in the legend while the beaten keep their board colors; and a **GAME OVER** result box beside the boards (never covering them) sets the verdict — TEAM B WINS! / ALICE WINS! / DRAW — in bold italic over the final scores (both teams', or every player's, winners first) and a Back to Lobby button. A simultaneous-top-out draw washes every board OUT and crowns nobody. The rank is provisional, from the totals the spectator's engine folded, and becomes final the moment the archive record arrives
- **Overflow scrolling:** the boards stay centered while they fit, but when there are enough of them that they are together wider than the window, the multi-board strip becomes **horizontally scrollable** (a scrollbar appears) so an overflowing board can be scrolled to rather than spilling off the edge or overlapping its neighbour — the same treatment as the archive final-playfield view
- The HUD keeps a "Back to Lobby" button, so a spectator can leave the game and return to the lobby at any time

---

## 9. CAS (Compare-And-Swap) Strategy

All playfield state is stored in JetStream as **one message per cell** — each (row, col) position has its own subject carrying that cell's current content (an empty cell marshals to `{}`, the vacate payload). Concurrent writes are managed via per-subject CAS (`Nats-Expected-Last-Subject-Sequence`) on atomic batch publishes — a multi-cell move either commits all its cells or none, never a torn intermediate. Every publish path diffs its projection against the live board and publishes only the cells that changed (a move is typically 4-8 cell messages), and within every batch cells are ordered by their new content — active first, locked/occupied second, empty vacates last — so a relocating piece never transiently has zero active cells and lock-in fires exactly once, at the last vacate, with all landed cells already applied.

Beside its cells, every competitive/team board carries two single-subject **registers** under the same playfield prefix (so board consumers and snapshot fetches cover them with one `…playfield.>` filter): the **garbage register** (`…playfield.garbage`, cumulative rows OWED, written by attackers) and the **txn register** (`…playfield.txn`, cumulative rows APPLIED plus the exactly-once gate for every bulk transform). Cooperative boards have neither — coop has no garbage.

### Optimistic sequence write-through (both modes)

The CAS expectation for every write is the per-cell `pf.CellLastSeq(row, col)` — the stream sequence of the last message the engine has seen on that cell's subject. Historically this advanced **only** when the engine's own consumer echoed a published cell back, so between publishing a write and consuming its echo the in-memory sequence (and board content) lagged the stream. Any second write issued into that window — a gravity tick, the next keypress, or a write right after a NoCAS line-clear/shrink — carried a stale expectation and lost CAS to the engine's *own* earlier write, dropping the step and flashing.

To close that window, **a successful publish is written through into the in-memory playfield immediately**, without waiting for the echo. The batch commit ack returns the stream sequence of the last message in the batch; because an atomic batch's messages get consecutive stream sequences, the engine infers every cell's sequence (`message i of N → commitSeq − (N−1−i)`) and applies the just-committed content **and** sequence to its playfield. The next move/gravity tick is therefore projected from — and CAS-checked against — up-to-date state and cannot self-race.

Reconciliation with the consumer echo is automatic and is the single rule in `Playfield.Apply`: a cell is updated only if the incoming sequence is **strictly higher** than the one in memory. The echo of our own write carries the **same** sequence we already wrote through, so it is skipped (a harmless no-op); only a **higher** sequence — the other player's write in cooperative mode, or a NoCAS write we did not originate — updates memory. This applies in both competitive and cooperative modes (in coop the write-through applies only what actually committed — the first-attempt batch, or the merged batch after a merge-retry — so it never clobbers the other player's cells).

### Player-initiated moves (left, right, down, rotate, hard drop)

CAS failure = **move is dropped, no retry, in either game mode**. The player must press the input again. The engine signals the failure with a **rainbow flash on the outline of the player's own piece** — cells of the active piece cycle through the seven spectrum colors over ~600 ms with a matching glow, then revert. The flash shows on the flashing player's own board, and — so a watcher sees the same contention feedback the players do — is also broadcast to **spectators**: the player publishes a transient **core NATS** message (fire-and-forget, on `jetris.flash.<gameID>.<playerID>`, deliberately NOT on the game stream so it is never persisted or replayed) that spectators subscribe to and render on that player's board. Other **players** do not subscribe, so a player still sees only their **own** CAS flashes; only spectators see everyone's.

This is intentional in cooperative mode where two players share one playfield: CAS rejections are routine and a silent server-side retry would mask conflicts from the player and make their input timing feel non-deterministic. Loud, immediate, local-only feedback gives the player full agency over how to recover.

The flash only ever fires for player-initiated moves. Engine-driven moves (gravity ticks, piece spawns) never flash.

### Engine-driven state changes (gravity, spawn, lock-in)

Gravity ticks and piece spawns must succeed even under contention — a dropped gravity tick would make the piece visibly freeze for one tick, and a dropped spawn would leave the player pieceless. Neither was player-initiated, so a flash would be misleading.

**Single-goroutine invariant (both modes):** a player's gravity ticks and their own input are processed on **one** engine goroutine (`runInput`), which `select`s over the moves channel and the gravity timer. This and the optimistic write-through above together remove a player's self-races: `runInput` ensures a gravity drop and a move never *project and publish concurrently*, and the write-through keeps that serialized writer's in-memory sequence current so even back-to-back writes don't carry a stale CAS expectation. Together they eliminate the spurious rainbow flashes that were otherwise visible in competitive play, where each player owns their cell subjects and a self-race was the only way two of *their* writes could contend.

In **competitive mode** each player owns their cell subjects, so — with gravity and input serialized on `runInput` and write-through keeping the view current — their writes do not contend with another player's in normal play.

In **cooperative mode** the players share the same cell subjects, but with per-cell granularity only writes to the **same cell** actually contend — two players moving in different parts of the board (even on the same row) never conflict, so contention is rare. On CAS failure the engine refetches the latest message for every affected cell in one batched round trip and merges per cell: this player's content is kept except where the stream now holds the other player's mid-flight (active) piece — those cells are skipped entirely, never overwritten or vacated. It then retries the atomic batch with refreshed per-subject CAS expectations (up to 16 attempts, with a short per-player-offset backoff between tries that breaks lockstep with the other player's retry loop).

**Teams mode** uses the cooperative scheme verbatim within each team's board (same merge-retry, same skip rule — "another player's active cell" naturally means a teammate's).

Locks are published as no-CAS authoritative writes (see below) and so cannot fail CAS.

### The garbage ledger: attacks as durable CAS-adds

An attack is never an event. Fire-and-forget events from near-simultaneous clears race each other — exactly the high-RTT case that matters most. Instead, the clearing player advances every victim board's **garbage register** with a CAS-add: read the register's last value `{total, by}` and sequence, publish `total + lines` expecting that sequence, and on a lost race refresh from the stream and re-add (bounded retries with a small per-player offset). The register is a **cumulative monotonic total**, so:

- simultaneous attackers serialize and the total converges to the exact **sum** — no attack lost, none double-counted;
- retention keeping only the last value is harmless — a newer total subsumes every older one;
- a victim that is behind (RTT, reconnect, late join) reconciles everything owed from the snapshot fetch — `total − applied` is the deficit, whenever it looks.

### Bulk transforms: the txn-gated batch (garbage application, line-clear collapse, elimination vacate)

Every whole-board transform on a competitive/team board is one atomic batch of the shape **[txn register | changed cells]**:

- **The gate.** The txn register message rides FIRST and carries a per-subject CAS expectation at the register's last sequence. When two transforms race — teammates applying the same garbage, or a clear racing a raise on the same board — the server atomically rejects the loser's ENTIRE batch (nothing of it is stored, per-message expectations are all checked at commit). The loser recomputes from a freshly fetched consistent snapshot: a lost raise finds the deficit already zero and no-ops; a lost clear re-detects its completed rows (still complete, possibly shifted) and lands on the next attempt. No merge-retry, no double-shift; the common case is one batch, zero retries.
- **The cells are NoCAS** — the transform overrides in-flight moves by design. A victim's move that raced the raise fails its own per-subject CAS against the risen board and is dropped + flashed, the established discipline for any lost move.
- **Teammate guards (shared boards).** A raise/clear batch must not corrupt a teammate's mid-flight piece from a stale snapshot: every write to a cell of another player's snapshot piece — and every write to a headroom row a fresh spawn could claim — carries per-subject CAS at the snapshot sequence, and a piece the transform leaves completely untouched contributes one expectation "carrier" (a batch message asserting that piece's cell is still at its snapshot sequence; the batch never writes that subject, so the assertion is legal anywhere in the batch). Any teammate move/lock/spawn committed since the snapshot rejects the whole batch.
- **The txn record is the in-band elimination signal.** It stores the cumulative applied total plus the transform's outcome: `topped` (players whose pieces were pushed off the top) and `full` (locked rows pushed past the top — the board is lost). Because the register precedes the vacating cells in the batch, a victim's consumer learns "this zero-active edge is a shrink top-out, not a lock-in" before the vacates arrive; `full` tops out every remaining player on the board directly. The applier handles its own elimination inline (its write-through already advanced the register mirror, so its own echo is a no-op).
- **The applier.** Garbage application runs on the victim's single gameplay-write goroutine (`runInput`), so it never races the victim's own move publishes; on team boards, whichever alive teammate reacts first applies, and the gate dedupes the rest. Application is structurally impossible for spectators and eliminated players (they don't run `runInput`).

The cumulative registers also make replay and restart safe: an engine that (re)starts mid-game captures both registers with its board snapshot and applies any outstanding deficit before playing.

### Authoritative writes (lock, hard-drop landing)

**No CAS.** The publisher's view is the new ground truth. The changed cells are published as a single atomic no-CAS batch via `PublishCellsAtomicallyNoCAS` so consumers either see the entire authoritative state change at once or not at all. The same content-ordered batching applies (active cells first, occupied second, vacates last). Whole-board transforms — clears, raises, vacates — use the txn-gated batch above instead: same NoCAS cells, plus the gate that serializes them against each other.

---

## 10. Real-Time UI Updates

**Principle:** All UI data backed by JetStream flows through ordered consumers into the
engine's and lobby's `Updates` channels — never polling or periodic refresh. The native
UI consumes those channels directly; the only remaining hop is drawing the next frame.

The lobby runs consumers for:
- KV bucket (players and game listings)
- Chat stream
- Archive stream

The engine runs consumers for:
- Playfield cells (one message per cell; the consumer parses the (row, col) from the subject, applies the single cell, and derives row-level repaint hints for the UI)
- Game events (line clears, shrink, game over)
- Game meta (status transitions)
- Countdown

Any change in a JetStream stream or KV bucket lands on a Go `Updates` channel:
`internal/nativeui` bridge goroutines read `engine.Updates` / `lobby.Updates` directly
and call `window.Invalidate()`; the next frame redraws the visible board from a
race-free snapshot (`engine.Snapshot()`). A NATS update reaches pixels within one
display frame, and the screen is redrawn only when something changed (idle = ~0 CPU).

Both channels are bounded and **non-blocking** (a slow UI must never stall the engine),
so individual row-update triggers can be dropped under a burst. This is safe because the
UI always re-renders from the converged `playfield` — but **bulk** changes that repaint the
whole visible range (line clears, competitive shrink) emit a single full-board re-render so
no row is left stale. Cell appearance (fill + outline) is computed in one place,
`internal/render` — `CellStyle` returns the RGBA fill and outline the drawer paints.

**CAS-rejection feedback** (a move rejected by per-subject CAS) stays local: the engine
emits the affected cells on the local `Updates` channel only (never published to NATS, so
other players see nothing), and the UI draws a one-shot ~600 ms rainbow **border** on
those cells in Gio.

### RTT display

While playing, the HUD shows a continuously updating **RTT** readout: the time between
the moment the engine initiates a batch publish commit and the moment its own ordered
consumer delivers the **first message of that batch** back. Every visible board change
travels this write→commit→echo loop, so the number is the real latency the player
experiences, not an artificial probe. Each successful batch publish (move, gravity tick,
spawn, lock, clear) produces a new measurement, emitted as an `UpdateRTT` on the local
`Updates` channel — so the readout refreshes at least once per gravity tick and on every
input. It shows an em dash until the first measurement completes, sub-10 ms values with
one decimal, and whole milliseconds above. Spectators publish nothing and therefore have
no RTT readout.

The readout is color-coded by latency: the normal text color up to 75 ms, then a warning
blend that starts yellow at 75 ms and reaches orange at 150 ms, and red above 150 ms.

### Link status

Every move is a publish that waits for its commit ack, and the moves behind it wait
in the engine's queue — so when the NATS connection drops (a tablet's WiFi blip), the
piece freezes: gravity too, since it runs behind the moves on the same loop. nats.go
reconnects on its own, and the in-flight publish waits out its ack timeout (jetstream's
5 s default) before the queue moves on, so such a drop plays out as a freeze of a few
seconds followed by the queued moves landing in a burst (nothing is lost — the queue is
unbounded). The game says so: while the connection is down the HUD shows a **LINK**
stat, `LOST 2.3s`, with a running clock (`internal/nativeui/link.go`, fed by the
connection's disconnect / reconnect / close callbacks, installed when the app adopts
its connection), and the "Show NATS messages" panel logs each `link` event —
`connection lost: … — reconnecting`, `reconnected to … after 2.3s`. Every app
connection is tuned for a flaky link (`linkOptions` in `internal/nats/connection.go`):
a 500 ms reconnect wait instead of nats.go's 2 s, unlimited reconnect attempts instead
of 60, and a 5 s ping (3 missed = dead) so a silently dead socket is noticed within
seconds rather than minutes.

### Move-buffer strip

Player inputs are serialized: each move's batch publish blocks on its commit ack before
the next move is dequeued, so on a high-RTT server inputs typed during an in-flight
publish wait in the engine's move buffer. A prominent **MOVE BUFFER** strip directly
below the playfield shows that queue like an arcade combo meter, oldest first: a row of
eight big chunky chip slots (dim outlines while vacant) that fill with bright gold chips
— blocky pixel-art arrows for the shifts and hard drop, blocky circular arrows (↻/↺)
for the rotations — as inputs queue, and a gold `+N` overflow marker beyond eight. Over
the slots is one unchanging caption, pinned to the left of the row: `MOVE BUFFER` and
the number queued, `0` when nothing is. It never re-words and never moves — wording that
came and went (`EMPTY` against `3 QUEUED`, an `IN FLIGHT` count behind them) slid the
line about under a board that has to sit still. The strip is animated in the video-game
idiom: a freshly queued chip **pops in** with a scale overshoot, and while anything is
queued a **glow chases** left-to-right across the chips. Each chip appears when the
input is accepted into the buffer and vanishes the moment its own batch publish starts,
so at low latency the strip just shows its empty slots (its footprint is constant — the
board never jumps as it fills). Spectators have no input and never see it.

### Mouse controls

Flanking the playfield (or under it, in a tall, narrow column) sits an on-screen
**arcade control pad** — the whole keyboard scheme as chunky 8-bit buttons, laid out
like a handheld's controls: on the left a **D-pad**, one cross-shaped plate whose
four arms are the arrow keys (▲ rotates clockwise like ↑, ◀ ▶ shift, ▼ soft-drops);
on the right the **face buttons** — ↺ and ↻ (the rotations drawn as blocky circular
arrows, not text) over the wide accent-filled `DROP` bar, the one button that
commits a piece, and, in a game with the hold rule, a `HOLD` bar (a blocky ⇄ swap
icon, the symbol the phone versions put on their hold button; absent otherwise).
On a touch screen the pad is thumb-sized. The HOLD box beside the playfield is itself tappable and
holds too, as on a phone. So the game is fully playable with the mouse alone, no
keyboard needed. When the board column is narrower than the pad (the minimum window
with an opponent column beside the board), the whole pad scales down as one row —
as a phone's controls shrink with its screen — rather than wrapping or clipping.
The pad is drawn dimmed before the countdown finishes (clicks made
while it is dimmed are swallowed, never queued) and disappears with the rest of
the play controls once the player is out. Clicking a pad button dispatches exactly
the same engine move as its key.

### Touch gestures

On a touch screen the playfield itself is a gesture surface (`internal/nativeui/gesture.go`),
driven the way the phone versions of the game are — a finger on the board rather than
a thumb on the pad. The pad stays; both dispatch exactly the same engine moves:

| Gesture | Action | Engine method |
|---------|--------|---------------|
| swipe left / right | shift the piece — a column per cell of travel, so the piece follows the finger | `MoveLeft` / `MoveRight` |
| tap the left half of the playfield | rotate counter-clockwise | `RotateCCW` |
| tap the right half | rotate clockwise | `RotateCW` |
| tap with two fingers | half turn (180°), on the SRS-X kicks | `Rotate180` |
| press and drag down | soft drop — a row per cell of travel, the piece still steerable sideways; the drag itself never locks it (the lock delay decides, as for ↓) | `MoveDown` |
| flick down | hard drop | `HardDrop` |
| swipe up | hold (games with the hold rule; the HOLD box is tappable too) | `Hold` |

Only touch presses gesture: a mouse click on the board stays what it is, the "keys back
to the board" click. Every finger is its own gesture: a thumb resting on the board's
edge blocks nothing, and a finger dragging the piece down while another taps a rotation
does both. The **playfield**, not the whole screen, is the surface, deliberately: on a
tablet the D-pad and face buttons flank the board, so a tap on "the left side of the
screen" would land on the D-pad and fire twice; a pointer area exactly on the playfield
can never receive a press that a pad button, the HOLD box, the chat or a HUD button
took, and it measures positions in the board's own coordinates, so the rotate tap splits
at the board's center.

Two rules keep gestures honest to the engine's move path (every move is a NATS round
trip — one publish in flight, the rest waiting in the engine's move queue, which has no
depth limit and never drops a move): a drag steps the piece toward a target set by the
finger's whole travel and queues at most a few moves ahead of the engine (6), catching
up frame by frame as the queue drains — so the piece tracks where the finger *is*, and a
swipe that turns back cancels the steps not yet queued instead of queuing them and
their undo; and while the finger moves fast and downward — a flick in progress,
≥ 1.2 dp/ms over the last 80 ms — nothing steps at all, so the release is a clean hard
drop (which also needs ≥ 40 dp of downward travel) rather than a hard drop queued
behind a burst of soft drops. That flick rule only holds for a gesture's first 200 ms:
a flick is over in 30–100 ms, and past the window a fast finger is a fast *drag* — a
soft drop swept top to bottom in under half a second — that steps like any other while
it moves (it was held back for as long as it stayed fast before that bound existed,
which read as the piece ignoring the finger until it slowed or lifted). A fast drag
that slows to a stop before lifting is no flick either: once the finger has been still
for 120 ms the rows it covered step after all, and the release soft-drops whatever is
left. Drift is filtered by axis: a drag that began sideways shifts a column
at half a cell of travel (responsive) but soft-drops only after a whole cell of droop;
one that began downward shifts only after a whole cell of wobble. A tap is a press
released within 300 ms and 12 dp; a held, still finger does nothing. Two fingers tapping
together — pressed within 100 ms of each other, neither past the slop — are one half
turn, fired by whichever of them lifts first (the other's release is then moot); a thumb
that was already resting on the board is no partner, so it still blocks nothing, and a
finger that has begun a drag is no partner either. That tap is the only thing two fingers
pressed together do: the moment either moves past the slop both are inert until they
lift — two fingers swiping down are no hard drop (they were two, once), no shift, no
hold — and so is a pair held past 300 ms. Gestures fire only
while the game is playable (as the pad's clicks do) and are dropped, not queued, before
the start, after elimination, and under the leave-game modal; a board re-planned under
the finger (the first touch switching the pad to thumb size, a resize) drops the gesture
in flight rather than mixing two coordinate frames.

Which builds get touch: Gio reports touch pointers from the browser (the wasm build —
every touch DOM event is `preventDefault`ed, so a swipe never scrolls the page),
Wayland, Android and iOS; a touchscreen on X11, Windows or macOS arrives as a mouse,
so there the pad is the touch control. The browser page also keeps the browser's own
touch behaviours off the canvas (`web/index.html`: `touch-action: none`, no text
selection or long-press callout, no overscroll, the body fixed while playing) — each
of those would take the finger's touches away from the game until the gesture
settled.

One WebKit behaviour needed a workaround of its own. Gio's browser backend calls
`canvas.getBoundingClientRect()` inside every touch event handler — a synchronous
layout in the middle of the gesture. On iPad (Safari and Chrome alike, both WebKit),
a fast flick — a 30–50 ms touch, the hard-drop gesture — then left WebKit delivering
**no touch events at all** to the page for several seconds, until the finger had
stayed off the glass; neither Gio nor the game ever saw those touches. It was found by
bisection on the game itself (`?touchdebug=1` recordings): the link, the engine, the
drop's processing, the frame requests, the viewport, and Gio's handlers deferred or
swallowed were each cleared, a plain page with the same CSS and touch handling never
showed it, and answering the layout call from a cache made it go away. So the page
(`web/index.html`) wraps the canvas's touch listeners and, while one runs, answers
`getBoundingClientRect()` from a cache kept warm outside the handlers (filled by Gio's
own resize call, refreshed on the frame after a resize or orientation change); the
canvas fills the viewport, so the cached rectangle is exact. That alone cut the
blackouts to a rare residual; the rest came from Gio's frame itself — requested on
every touch event, 15–40 ms of WebGL on an iPad — landing between a flick's events
and holding up their dispatch, so the page also holds the frames requested during the
first 60 ms of a touch until those 60 ms have passed (a flick is over by then; a tap or
a drag merely sees its first frame 60 ms later). `?nocacherect=1` turns the cache off
and `?delayframe=0` the hold. On the game's side, a swipe made while the next piece is
still spawning is not lost either: the recognizer holds its steps back while the
board has no active piece and lets them fly the moment one appears (`HasActivePiece`),
so the piece lands where the finger already is.

The last of it was Gio's own, and in the browser only. After a lock the engine's
consumer spawns the next piece while holding the engine lock across the spawn's NATS
round trip; a frame that runs during that round trip parks mid-way (its `Snapshot`
needs the lock) — after its input handlers have already drained — and a parked wasm
frame hands the event loop back to the browser, so a touch delivered then lands in
Gio's router mid-frame and `Router.Frame` discards it at the frame's end: the swipe
made right after a hard drop was lost whenever it coincided with the spawn's round
trip. The game marks each frame's span for the page (`window.jetrisInFrame`,
`frameBegin`/`frameEnd` in `view_js.go`), and the page's input shim answers an event
that arrives inside a frame at once (`preventDefault`) but hands it to Gio only once the
frame has ended, in order (`held-in-frame` on the badge counts them). Should touch still
misbehave on some device, open the page with
`?touchdebug=1`: a badge in the corner counts, at the window, every touch, pointer,
mouse, Safari gesture and scroll event the page receives (a `pointercancel` is
WebKit's word for a native gesture taking a touch over, a `gesturestart` for a second
finger read as a pinch), the most fingers seen at once and the idle time since the last
touch, beside what the game reports every frame (touch presses that reached the game
screen, frames laid out, moves queued), the page's zoom, selection, focus and
visibility, and a trace of the latest events — which places a stall: touches not
reaching the page at all, reaching it as something other than touches, not reaching
the game, or reaching it and going nowhere. `web/touchtest.html`, served beside the
game, is the same badge on a plain page with the game page's CSS and Gio's touch
handling (no wasm, no WebGL), with `?busy=200`, `?nopd=1` and `?pointer=1` variants,
to tell a browser/OS behaviour from a game-page one. On the Go side, `rapidinput_test.go`
drives the real router with taps and swipes as fast as the frames come and checks
that every one lands.

### Window-size reactivity

The game screen adapts to the window: the playfield's cell size is recomputed every
frame to the largest size that fits the available area (within chunky-pixel bounds,
14–56 dp) after reserving room for the move-buffer strip and control pad, the spectator
multi-board and team-board strips size their cells to fit every board side by side
(falling back to horizontal scrolling below a readability minimum), the opponent
thumbnail column scales with window height, the HUD column takes ~19% of the window
width (200–300 dp), the pre-game countdown numeral scales to ~1/8 of the window's short
side, and the chat and NATS-message panels grow with window height as before. The
window also enforces a **minimum size** (760×720 dp): the OS will not let it shrink
below what the playfield (at its minimum cell size), the move-buffer strip, the control
pad, and the chat panel need to stay displayed whole.

### NATS message panel

A **"Show NATS messages"** checkbox sits in the left HUD column while playing or
spectating. When checked, a monospace strip appears across the bottom of
the window showing the live tail (last 5000) of the messages the engine's game-stream
consumers deliver — the exact messages that update the in-memory playfields and drive
the UI: own/opponent/team cell echoes, game events, meta transitions, countdown ticks,
and roster entries. Each line prints the message's **JetStream stream timestamp** (taken
from the received message's metadata, not the local arrival time), its **subject**, and
its raw **JSON payload**, syntax-colored (keys blue, string values green, numbers gold,
`true`/`false`/`null` orange). The list sticks to the newest message. Messages are only
collected while the box is checked, and the log is cleared when entering or leaving a
game.

**Transactions are shown as blocks.** Nearly every board change is published as ONE
atomic batch (see Playfield Storage) — a move is 4–8 cell messages that commit together.
The server stores the batch's `Nats-Batch-Id` header on every message of the batch, so
the panel can group them. The rows of one batch get three matching cues, all in that
transaction's color:

- a **tinted background** behind every row of the batch, with no gap between them;
- a **bracket** down the left edge — a bar spanning the whole batch, closed by a short
  stub at the top of its first row and the bottom of its last — so the batch's extent is
  unambiguous even where two same-colored blocks would otherwise touch;
- the **batch id** (first 6 characters of the `Nats-Batch-Id`) printed in a left gutter on
  the batch's first row, naming the transaction those rows committed in.

The gutter is a fixed monospace width on every row, so the timestamp and subject columns
stay aligned down the whole strip. Successive transactions take successive colors from a
six-entry palette, so neighbouring batches never look like one. Single-message publishes
(meta, events, countdown, roster) carry no batch id: no tint, no bracket, blank gutter. A
batch keeps its color even when another consumer's message interleaves between its rows
(the id is printed again where it resumes). Reading down the strip, one bracketed block =
one indivisible state change every player saw at once.

**The strip is resizable.** A divider with a centered grip sits between the chat panel
and the message strip; dragging it up or down resizes the strip (the cursor turns into a
row-resize cursor over it). Until it is first dragged the strip is window-reactive — at
least 170 dp, growing to 20% of the available height. Once dragged, the chosen height is
kept, still clamped so the strip can never shrink below 56 dp or crowd out the board and
chat above it.

### Lobby branding

The lobby screen carries a banner across its top — the nats.io "N" logo flanking
"Jetris: peer to peer blackboard system made with NATS.io JetStream" — above the player/chat and
games/history columns.

### Version plate

Every screen — login, lobby, game, archive — carries a small arcade-cabinet plate in the
window's **top-right** corner reading **VER &lt;version&gt;** in the pixel face on its own
framed chip, so it stays readable over a board. The string is the build's version: the
release tag stamped into the binary (`-X main.version`), or `DEV` for a plain local
build — the same value `jetris --version` prints.

---

## 11. Agents and the reference agent (`golang-mk1`)

An agent is any standalone program that plays Jetris by speaking the game's NATS
protocol — there is no plugin interface; the contract is the wire protocol and the
fair-play rules in `jetris-agent-guide.md` (with these game rules). Agents can be
written in any language and contributed to the repo under `agents/<name>/`
(see `agents/README.md`); each plays under a name of the form
`<agent-name>-<instance>-<difficulty>` so rosters and history record exactly which agent,
which running copy, and how strong.

Jetris ships one reference agent, **`golang-mk1`** (`agents/golang-mk1/`), written in Go,
that plays **all three modes** — cooperative, competitive, and teams. It is deliberately an ordinary peer — the same lobby
join handshake, the same move vocabulary a human has (left, right, down, rotate CW/CCW/180,
hard drop), the same consumers and CAS discipline — with a planner where the GUI has a
keyboard. Nothing in the blackboard needed to change to admit a software agent: the agent
demonstrates that a NATS-coordinated peer-to-peer game is equally playable by humans and
programs. It is a **self-contained module** with no dependency on the game's code — it
implements the whole protocol, engine included, straight from the agent guide, exactly as
a third-party agent would; `agents/example-python/` is the same proof in miniature, a
minimal Python agent with no repo dependency.

### How it plays

- **Perception:** the agent plans against **committed** state — its board as the
  stream has echoed it back — plus its own optimistic projection of it: the committed
  cells with its own in-flight writes applied on top, the same view the human client's
  optimistic publish mode draws. Never anyone else's unacked writes, and never
  protocol internals the UI does not render.
- **Planning:** for each piece it enumerates every placement reachable with its move
  vocabulary (SRS rotations in place, one-column slides, hard drop), simulates the lock
  and line clear on a board copy, and scores the result with Pierre Dellacherie's
  six-feature heuristic (landing height, eroded cells, row/column transitions, holes,
  cumulative wells). Its lookahead is exactly the game's piece preview (§1b): each
  candidate's score adds the best play-out of the revealed next pieces on the
  simulated board (beam-pruned, spawn-blocked futures scored as top-outs). The piece
  sequence is deterministic from the game seed (§4), but reading it past
  `next_count` would violate the fair-visibility contract — in a no-preview game
  the agent plans one piece at a time, exactly like its human opponents.
- **Execution:** the agent walks the piece toward the target as atomic CAS batches
  (a whole walk can go out as one batch), honoring gravity on the way, then
  hard-drops. Its batches are pipelined by default — the next batch goes out without
  awaiting the last one's commit ack, per the agent guide's §4.3 disciplines — with
  the spawn, the lock-in and every gated transform settling the pipeline first. A
  move that loses a CAS race (against incoming garbage, say) is dropped, not
  retried: the agent flashes, resyncs its board from the stream, and re-plans from
  the converged state — a lost pipelined batch additionally vacates whatever strays
  the batches behind it left before re-planning.
- **Garbage awareness:** adversarial shrink rows are priced in naturally — they count
  as locked stack for every feature and the clear simulation refuses to complete them,
  exactly like the game's own full-row rule.

### Fair visibility: agents see only what humans see

An agent may base decisions ONLY on information a human player can see in the UI:
the committed boards (its own and the opponents'/teams'), the roster and
eliminations, scores/levels, the countdown, its own falling piece, and the game's
piece preview — the `next_count` upcoming pieces the NEXT well shows (§1b).
It may NOT read the game seed to predict pieces beyond that horizon (in a
`next_count: 0` game, no lookahead at all), nor any stream state the UI does not
render. This is the visibility contract every agent implementation must honor —
see `jetris-agent-guide.md`.

### Per-mode outcomes

- **Cooperative:** the agent plays for the shared score; anyone's top-out ends the game
  for everyone, and the agent finishes and archives the shared game — as the topper, and
  on a peer's top-out too (the CAS makes the finish idempotent; the topper's own may
  never land).
- **Competitive:** last standing wins; the agent reports WON/LOST, and — like any
  winning player — the winner archives before moving on. A loser stays connected
  briefly for the verdict rather than vanishing mid-game.
- **Teams:** the agent joins the invited team (or the emptier one when scanning); its
  own top-out is not the outcome — it vacates its dead piece (a gated transform) and
  stays connected until one team is fully out; a winning-team agent archives (the
  transition is CAS-protected so duplicates are safe).

### Difficulty levels

| Knob | Easy | Medium | Hard |
|------|------|--------|------|
| Think pause per piece | 1500 ms | 600 ms | 100 ms |
| Pause between moves | 300 ms | 150 ms | 30 ms |
| Blunder rate (P of not playing the best move) | 30% | 10% | 0 |
| Blunder depth (picks among ranks 2..N+1) | 4 | 2 | — |
| Lookahead (max preview pieces used in planning) | 0 | 1 | 6 |

Hard plays the best placement it finds, as fast as the NATS round-trips allow. Easy and
medium think slower, pace their moves, and sometimes deliberately play a lower-ranked
placement, so they are beatable and fun. Lookahead is always **further capped by the
game's own `next_count`** (§1b): even hard plans one piece at a time in a no-preview
game, and easy ignores the preview entirely.

### Agent policy: who decides whether agents may join

Every game (any mode) carries its creator's **agent policy**: `MaxAgents`, the number of
roster seats agent players may take (0 = agents may not join) — **per team in a teams
game** (at most that many agents on each team, so a creator can seat an agent on every
side, or one agent per team to fill in for the missing humans), over the whole game
elsewhere. In the GUI the policy is the create wizard's **agents step** — an **"Allow
agents to join" checkbox** (off by default — games are human-only unless opted in) plus
a **Max agents** count (**Max agents per team** for a teams game, capped at the players
per team), offered for every game mode, and beneath them **"Agents pause when alone"**
(off by default): with it, an agent left as the **only player** in the open game — everyone
else walked out — stops playing, keeping its seat, until someone (a player or another agent)
takes a seat again; without it, agents play on, alone or among themselves — enough of them
(one per team, say) keep a game going after the humans leave. The setting rides on the
listing (`agents_pause_alone`) and is the agents' to honour; the GUI itself never pauses.
(In a multi-playfield game a roster down to one player ends the game — the last playfield
standing wins — so the pause only ever bites on a single shared playfield.) The step is
**reached only for open games** — an invite-only game's agent policy is decided per
invitation, so its wizard ends at the who-can-join step and never asks. `lobby.JoinGame`
enforces the policy **atomically inside its CAS loop**: an agent joining a no-agents game
gets `ErrAgentsNotAllowed`, and once `MaxAgents` roster seats — of the team it asked for, in
teams mode — are held by agents further agent joins get `ErrAgentSlotsFull` — so several
idle agents racing for the last agent seat can never over-fill it. Agents are first-class
but visible: an agent's player name has
**three parts** — `<version>-<instance>-<difficulty>`, e.g. **`golang-mk1-3f7a-hard`**. The
version stem names the agent's CODE generation (`golang-mk1` uses its codename,
bumped whenever its play logic changes; third-party agents use
their own stem via `--name`); the instance id is 4 random hex chars
minted fresh for every connection, so several copies of one agent version can play
at once and each connection is distinguishable; the difficulty labels its strength.
The name doubles as the NATS player ID, which appears in subject tokens AND in the
presence KV key, whose charset is stricter (`[-/_=.a-zA-Z0-9]`), and the whole must
fit the 32-character cap — so opponents, spectators and the archive all see exactly
which agent, which copy, and how strong. Their presence entries and roster seats
are additionally flagged, and the UI tags them `[agent]` in the lobby player list, game
listings, ready roster and in-game legend; game rows show `agents k/N` when a game
allows them (`agents k, max N per team` for a teams game, and `agents pause when alone`
when the creator asked for it).

### Lobby behavior: agents are residents

With no `--join`/`--create`, an agent is a **lobby resident**: it idles in the lobby
**waiting to be invited** — invitations are accepted immediately — plays the game to
the end, returns to the lobby, and repeats until interrupted (`--once` restores
play-one-game-and-exit). Passing **`--auto-join`** widens the resident's appetite: it
then also actively joins the oldest open game that allows agents and has a free seat and a free agent seat. An "agent
that is not currently playing" is simply one sitting in the lobby waiting (or, with
`--auto-join`, scanning) — a playing agent can't join anything else. If a joined game
never starts (nobody shows up or readies), the agent **un-joins** after its wait
timeout (`--wait`, 10 minutes by default): a CAS roster removal (reverting a full
`starting` game to `created` so the freed seat is joinable again) plus a purge of its
roster announcement so it never lingers as a ghost seat — then goes back to waiting.

In every seat it takes, the agent carries the same lifecycle responsibilities as a GUI
player:

- It joins via the listing's CAS join (guarding first that the game is not yet
  running, has a free seat, and allows agents), toggles READY, and — if its toggle is
  the one that completes the ready set — **it runs the 5..0 countdown and transitions
  the game to in_progress**, exactly like the GUI client in that seat.
- If it wins, it CAS-transitions the meta to finished and **archives the game**
  (record, stream deletion, listing cleanup) before moving on, like any winning player.
- On losing it stays connected briefly for the verdict, then moves on.

One-shot game selection remains CLI-driven: `--join <gameID>` for a specific game
(still subject to that game's agent policy), or `--create --mode
cooperative|competitive|teams --players N [--max-agents M] [--next K]
[--split-pieces]` to host one
(`--players` is per team in teams mode, like the GUI's count, and floors like it — 1 for a cooperative game, which an agent may host and play solo for the high score, 2 for competitive; `--max-agents` is per team there too; `--split-pieces`
deals the seven types out between the teammates there, §5; `--pause-alone` asks the
agents of the game to wait for company when left as its only player) — agent-hosted games
allow agents in all seats by default, since the host itself takes one.
