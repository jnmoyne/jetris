# Jetricks — Gameplay Reference

**Version:** 1.0
**Status:** Authoritative
**Date:** April 2026

This document is the single source of truth for all gameplay mechanics in Jetricks. The spec (`jetricks-project-structure.md`) and plan (`jetricks-implementation-plan.md`) defer to this document for gameplay behavior. Any gameplay change must be reflected here first.

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

**Rotation:** Super Rotation System (SRS) with standard wall kick tables. Up to 5 kick offsets are tried per rotation attempt. The I-piece has its own kick table; the O-piece does not rotate.

**Piece sequence:** 7-bag randomizer — within each group of 7 pieces, all 7 types appear exactly once in a random order. The bag is shuffled using a seedable PCG RNG so the sequence is deterministic and seekable.

Each player has a color associated with it: used for the outline color of the piece in spectator mode, and also for the outline color of the piece when it's dropped.

---

## 2. Playfield

| Property | Value |
|----------|-------|
| Total rows | 28 |
| Headroom rows | 4 (rows 0-3, not rendered) |
| Visible rows | 24 (rows 4-27) in cooperative mode, for competitive this number depends on the number of players in the game |
| Standard width | 10 columns in competitive mode; for cooperative the width is `playerCount × 10` |

**Cell states:**

| State | Occupied | Active | Meaning |
|-------|----------|--------|---------|
| Empty | false | false | Nothing in this cell |
| Active | false | true | Part of a falling piece |
| Locked | true | false | Settled piece, permanent until line clear |
| Adversarial | true | false | Permanent garbage cell added by a competitive shrink (the `Adversarial` flag is set); its row can never be completed or cleared |

In cooperative mode, active cells carry a `PlayerIdx` field (0-indexed) identifying which player's piece they belong to.

---

## 3. Cooperative Mode

2 or more players, there is one piece per player, all the players see each other's pieces on the same common playfield.

### Playfield

The playfield is a single shared board of width `playerCount x 10`. Each player controls it's own piece. All players' pieces exist on the same playfield and can move anywhere on it — they are not restricted to any section, however player's tetrominoes can _not_ overlap.

### Piece Spawning

Players share the same RNG seed (`meta.Seed`) and produce the identical piece sequence

Each player tracks their own `pieceIdx` independently.

**Spawn position:** Each player's piece spawns centered in their section, but can immediately move anywhere:
- Player N spawns at column `N * 10 + 3` (center of their 10-column section)
- Anchor row 2 for **all** piece types, so every piece's lowest cell sits at row 3 (just inside the headroom) and they all become visible after the same number of gravity ticks. (Spawning the I one row higher made it appear a tick later than the rest, so a player hard-dropping each piece on sight would drop the I before seeing it.)

### Movement

Pieces can move anywhere on the full-width board. Collision detection (`CanPlaceCoop`) checks:
1. Boundaries (columns 0..width, rows 0..height — 28 in cooperative)
2. Locked cells (occupied, non-active)
3. The **other player's active cells** (treated as obstacles)
4. The moving player's own active cells are **excluded** from collision

#### Controls

Same in both game modes. The UI calls the engine's move methods directly (no input is sent
over NATS — each move is a local intent that publishes the resulting rows with CAS):

| Key | Action | Engine method |
|-----|--------|---------------|
| ← / → | move left / right | `MoveLeft` / `MoveRight` |
| ↓ | soft drop (one row) | `MoveDown` |
| ↑ or X | rotate clockwise | `RotateCW` |
| Z | rotate counter-clockwise | `RotateCCW` |
| Space | hard drop | `HardDrop` |

In the native UI these are dispatched from `internal/nativeui/input.go` (the board tag is
kept focused with Gio's `key.FocusFilter` + `key.FocusCmd`); in the web UI a key handler
`POST`s to `/game/move`.

### Gravity

Gravity ticks at the standard speed curve interval (see Section 6). On each tick, the engine attempts to move the piece down one row.

**Blocked by locked cells or bounds:** The piece locks immediately (standard Tetris behavior).

**Blocked only by the other player's active piece:** The piece does **NOT** lock. The obstacle is temporary — it will itself fall on its next gravity tick. Gravity waits and tries again on the next tick.

Detection: if `CanPlaceCoop` fails but `CanPlace` (which ignores all active cells) succeeds for the position one row below, the obstacle is the other player's active piece.

### Hard Drop

The piece falls instantly to the lowest valid position (stopped by locked cells, bounds, or the other player's active piece).

**Landed on locked cells or bounds:** Piece locks immediately.

**Landed on the other player's active piece:** Piece does **NOT** lock — it stays active and resumes falling by gravity. The other player's piece will itself fall, and gravity will continue dropping this piece further.

### Lock-In

A piece locks when gravity cannot move it down and the obstacle is locked cells or bounds (not another player's active piece). Lock-in converts all active cells (matching the player's `PlayerIdx`) to occupied/locked cells.

After lock-in:
1. Check for completed rows (full-width)
2. Clear completed rows and update score
3. Spawn the next piece

### Line Clears

A row is complete when **all cells across the entire width** are occupied (locked) and none are active. For a 2-player game, all 20 cells in the row must be locked.

Cleared rows are removed and everything above shifts down. Empty rows are added at the top. The cleared state is published with no-CAS (authoritative — the clear must not be undone by stale data).

A clear must **not** disturb the other players' falling pieces: only the player whose piece completed the row gets a new piece; everyone else's piece keeps dropping, shifted down by the number of cleared rows. Because the shift moves every piece **down**, the shifted rows are published **bottom-first** (highest row index first). Applied top-to-bottom instead, another player's mid-flight piece would be erased from its old rows before its new (shifted) rows arrived — its active-cell count would momentarily hit zero and that player's lock-in detector would fire a spurious lock, respawning their piece from the top. Bottom-first keeps the shifted piece always overlapping itself, so it never transiently vanishes.

Because the board is shared, a clear must be reflected on **every** player's screen, not just the player whose piece completed the row. The cleared rows are published to the shared playfield subject and every player's row consumer applies them, so the authoritative `playfield` state always converges. Rendering, however, is per-engine: the player who detected the clear re-renders the whole board directly, and every **other** player re-renders the whole board on receipt of the `EventLineClear` event. A full-board re-render (rather than relying on per-row repaint triggers) is used because a clear repaints the entire visible range at once, and individual per-row triggers can be dropped by the bounded, non-blocking UI update fan-out — which would otherwise leave stale, un-cleared rows on another player's board.

### Scoring

Score = `playerCount` per line cleared.

| Lines cleared | Score (2 players) | Score (3 players) |
|--------------|-------------------|-------------------|
| 1 | 2 | 3 |
| 2 | 4 | 6 |
| 3 | 6 | 9 |
| 4 | 8 | 12 |

There is no level-based multiplier in cooperative mode. The score is intentionally simple — it scales with the wider (harder to fill) playfield.

### Shared Score

There is a single shared score visible to all players. When any player clears lines:
1. That player adds the score delta locally
2. An `EventLineClear` is published to NATS with the `Score` delta
3. All other players receive the event and add the delta to their local score
4. All players' UIs update simultaneously

### Level Progression

Level = `totalLinesCleared / 10`, capped at 19. Level affects gravity speed (see Section 6). Level is computed independently by each engine from its local `totalLines` counter, which increases on both local clears and received `EventLineClear` events.

### Game Over

When **any** player tops out (newly spawned piece cannot be placed), the game ends for **all** players:
1. The topped-out player publishes `EventGameOver`
2. All other players' event consumers receive it and immediately transition to game over
3. All players see the "GAME OVER" overlay simultaneously

### Visual Indicators

The playfield is rendered entirely server-side: for **every** square the server
computes a concrete fill color and an outline color and emits them as an inline
`style` on the `<td>` (no per-color CSS classes). The fill is the tetromino color
composited over the board background to match the desired brightness (active ≈0.9,
locked ≈0.7, adversarial ≈0.8). Empty squares fall back to the board background
with a thin grid-line outline, so literally every square has an outline.

- **Own piece:** 2px white outline around each active cell
- **Other player's piece:** Standard piece color, no ownership outline (from a player's perspective)
- **Locked cells:** dimmed piece color with a 2px per-player outline (non-adversarial)
- **Spectator view:** Per-player colored outlines on every active and locked cell (P0=cyan #00ffff, P1=magenta #ff00ff, P2=yellow #ffff00, P3=orange #ff8800, …)
- **No divider:** The board is rendered as one seamless playfield with no visual separator between player sections

---

## 4. Competitive Mode

### Players

Competitive mode supports 2 or more players. Each player has their own independent playfield.

### Playfield

Each player has their own 10-column playfield. The playfield height scales with the number of players: the standard 24 visible rows plus one additional row per player. For 2 players: 26 visible rows (30 total with headroom). For 3 players: 27 visible rows (31 total). Playfields are rendered separately (own board + opponents in sidebar).

### Piece Spawning

Each player gets their own piece spawn on their own independent playfield. Players share the same RNG seed (`meta.Seed`) and produce the identical piece sequence.

**Spawn position:** Centered at column 3, anchor row 2 for all piece types (lowest cell at row 3).

### Movement

Standard Tetris movement. Collision detection is using CAS only.

### Gravity

Standard gravity. When a piece can't move down, it locks immediately (no "blocked by active piece" logic since there's only one piece per playfield).

### Line Clears

A row is complete when all 10 cells are occupied (locked). Standard Tetris rules apply. When one player clears a line, every other player gets a line added at the bottom of their playfield, so all of their already-locked rows shift up by one. The currently falling piece does **not** rise with the stack — it holds its on-screen position and is simply dropped into place as the stack rises to meet it. Only if the rising stack (or the new garbage) would overlap the falling piece is the piece pushed up, and then only by the minimum number of rows needed to clear the conflict. If that upward push would run the piece off the top of the playfield, the player tops out and is eliminated (see Game Over). Once a line is cleared and added to the other player(s), that added line can never be removed or completed.

### Scoring

The only score that is kept in competitive mode is the number of line each player clears. As the game ends when all be one of the players tops out, regardless of the score, the score is only kept for leaderboard puposes, the winner is the last player left that didn't top out, all the other players loose.

### Shrink Attack

When a player clears 1 or more lines, a shrink event is sent to **all** other players still in the game:
- **Rows added:** the number of lines cleared
- **Adversarial rows:** Each opponent's playfield shifts up. New rows at the bottom are fully occupied and permanent (can never be cleared)
- **Falling piece:** Each opponent's falling piece stays in place as the stack rises; it is pushed up only as far as the rising stack/garbage forces it. A push that would carry it off the top tops that player out (they lose)
- **Multi-player:** The shrink applies to ALL opponents simultaneously, not just one

### Game Over

The game continues until only **one player remains**. When a player tops out (either the newly spawned piece cannot be placed, or an opponent's line clear pushed the rising stack into the falling piece and the conflict-resolving upward shift ran the piece off the top), that player is eliminated and transitions to spectator mode — they can watch the remaining players continue. The last player standing wins. The game UI shows a player status list with each player marked as playing (green dot) or eliminated (red cross, struck through name). At game over, winners see "YOU WON!" and the loser(s) see "YOU LOST".

---

## 5. Game Lifecycle

```
created → starting → [countdown] → in_progress → finished → archived
                                                      ↑
                                                  cancelled
```

### Transitions

| From | To | Trigger |
|------|----|---------|
| — | created | Player clicks "Create Game" |
| created | starting | All player slots filled (roster full) |
| starting | [countdown] | All players click READY |
| [countdown] | in_progress | 5-second countdown completes |
| in_progress | finished | Game over (top-out) |
| finished | archived | Archive record published, game stream deleted (5s delay) |
| created/starting | cancelled | Creator absent, all players absent, or cleanup |

### Ready Flow

1. Players join the game and see the game page with a "WAITING FOR PLAYERS" header
2. Each player's ready state is shown (green checkmark = ready, red cross = not ready)
3. Players toggle READY/NOT READY by clicking the button
4. Ready state is stored in the KV game listing with CAS (prevents lost updates)
5. When ALL players are ready: countdown begins, ready toggle is locked
6. **Countdown:** 5...4...3...2...1...GO! (published to NATS countdown subject, consumed by all engines)
7. After "GO!": game meta transitions to `in_progress`, pieces spawn

### Archive Record

Published to `JETRICKS_ARCHIVE` stream when a game finishes:

```json
{
  "game_id": "uuid",
  "mode": 0,
  "player_count": 2,
  "players": [
    {"player_id": "Alice", "score": 42, "piece_count": 30, "winner": false},
    {"player_id": "Bob", "score": 42, "piece_count": 25, "winner": false}
  ],
  "started_at": "2026-03-21T10:00:00Z",
  "finished_at": "2026-03-21T10:05:30Z",
  "total_score": 42
}
```

---

## 6. Gravity Speed Curve

Standard Tetris Guideline gravity intervals:

| Level | Interval |
|-------|----------|
| 0 | 800 ms |
| 1 | 717 ms |
| 2 | 633 ms |
| 3 | 550 ms |
| 4 | 467 ms |
| 5 | 383 ms |
| 6 | 300 ms |
| 7 | 217 ms |
| 8 | 133 ms |
| 9 | 100 ms |
| 10-12 | 83 ms |
| 13-15 | 67 ms |
| 16-18 | 50 ms |
| 19+ | 33 ms |

---

## 7. Spectate Mode

Any player in the lobby can spectate an in-progress game:
- Engine starts in `ModeSpectator` (no gravity, no move input)
- The game page hides all controls and the ready button
- Shows "Spectating" status
- **Player legend:** Shows each player's name with their assigned color swatch
- **Colored outlines:** Each player's active piece has a distinct colored outline (not white)
- Spectators see the same real-time playfield updates as players

---

## 8. CAS (Compare-And-Swap) Strategy

All playfield state is stored in JetStream. Concurrent writes are managed via per-subject CAS (`Nats-Expected-Last-Subject-Sequence`) on atomic batch publishes — a multi-row move either commits all rows or none, never a torn intermediate.

### Optimistic sequence write-through (both modes)

The CAS expectation for every write is `pf.LastSeq[row]` — the stream sequence of the last message the engine has seen on that row's subject. Historically this advanced **only** when the engine's own consumer echoed a published row back, so between publishing a write and consuming its echo the in-memory sequence (and board content) lagged the stream. Any second write issued into that window — a gravity tick, the next keypress, or a write right after a NoCAS line-clear/shrink — carried a stale expectation and lost CAS to the engine's *own* earlier write, dropping the step and flashing.

To close that window, **a successful publish is written through into the in-memory playfield immediately**, without waiting for the echo. The batch commit ack returns the stream sequence of the last message in the batch; because an atomic batch's messages get consecutive stream sequences, the engine infers every row's sequence (`message i of N → commitSeq − (N−1−i)`) and applies the just-committed content **and** sequence to its playfield. The next move/gravity tick is therefore projected from — and CAS-checked against — up-to-date state and cannot self-race.

Reconciliation with the consumer echo is automatic and is the single rule in `Playfield.Apply`: a row is updated only if the incoming sequence is **strictly higher** than the one in memory. The echo of our own write carries the **same** sequence we already wrote through, so it is skipped (a harmless no-op); only a **higher** sequence — the other player's write in cooperative mode, or a NoCAS write we did not originate — updates memory. This applies in both competitive and cooperative modes (in coop the write-through applies only what actually committed — the first-attempt batch, or the merged batch after a merge-retry — so it never clobbers the other player's cells).

### Player-initiated moves (left, right, down, rotate, hard drop)

CAS failure = **move is dropped, no retry, in either game mode**. The player must press the input again. The engine signals the failure with a **rainbow flash on the outline of the player's own piece** — cells of the active piece cycle through the seven spectrum colors over ~600 ms with a matching glow, then revert. The flash is local-only: it is emitted directly to the local engine's UI Updates channel and is **not published to NATS**, so the other players see nothing.

This is intentional in cooperative mode where two players share one playfield: CAS rejections are routine and a silent server-side retry would mask conflicts from the player and make their input timing feel non-deterministic. Loud, immediate, local-only feedback gives the player full agency over how to recover.

The flash only ever fires for player-initiated moves. Engine-driven moves (gravity ticks, piece spawns) never flash.

### Engine-driven state changes (gravity, spawn, lock-in)

Gravity ticks and piece spawns must succeed even under contention — a dropped gravity tick would make the piece visibly freeze for one tick, and a dropped spawn would leave the player pieceless. Neither was player-initiated, so a flash would be misleading.

**Single-goroutine invariant (both modes):** a player's gravity ticks and their own input are processed on **one** engine goroutine (`runInput`), which `select`s over the moves channel and the gravity timer. This and the optimistic write-through above together remove a player's self-races: `runInput` ensures a gravity drop and a move never *project and publish concurrently*, and the write-through keeps that serialized writer's in-memory sequence current so even back-to-back writes don't carry a stale CAS expectation. Together they eliminate the spurious rainbow flashes that were otherwise visible in competitive play, where each player owns their row subjects and a self-race was the only way two of *their* writes could contend.

In **competitive mode** each player owns their row subjects, so — with gravity and input serialized on `runInput` and write-through keeping the view current — their writes do not contend with another player's in normal play.

In **cooperative mode** both share the same row subjects with the other player. On CAS failure the engine refetches each affected row from the stream, overlays this player's cells on top of the latest stream state, and retries the atomic batch with refreshed per-subject CAS expectations (up to 5 attempts).

Locks are published as no-CAS authoritative writes (see below) and so cannot fail CAS.

### Authoritative writes (lock, hard-drop landing, line clear, opponent-shrink application)

**No CAS.** The publisher's view is the new ground truth. Published as a single atomic no-CAS batch via `PublishRowsAtomicallyNoCAS` so consumers either see the entire authoritative state change at once or not at all.

---

## 9. Real-Time UI Updates

**Principle:** All UI data backed by JetStream flows through ordered consumers into the
engine's and lobby's `Updates` channels — never polling or periodic refresh. Both front
ends consume those same channels; only the last hop to the screen differs.

The lobby runs consumers for:
- KV bucket (players and game listings)
- Chat stream
- Archive stream

The engine runs consumers for:
- Playfield rows
- Game events (line clears, shrink, game over)
- Game meta (status transitions)
- Countdown

Any change in a JetStream stream or KV bucket lands on a Go `Updates` channel, then:

- **Native UI (default):** `internal/nativeui` bridge goroutines read `engine.Updates` /
  `lobby.Updates` directly and call `window.Invalidate()`; the next frame redraws the
  visible board from a race-free snapshot (`engine.Snapshot()`). No HTTP/SSE round-trip — a
  NATS update reaches pixels within one display frame, and the screen is redrawn only when
  something changed (idle = ~0 CPU).
- **Web UI (`--web`):** `internal/ui` fans each update out over a per-connection broadcaster
  to a Datastar SSE stream and patches the affected rows server-side by element ID
  (`#row-{n}`).

Both channels are bounded and **non-blocking** (a slow client must never stall the engine),
so individual row-update triggers can be dropped under a burst. This is safe because each UI
always re-renders from the converged `playfield` — but **bulk** changes that repaint the
whole visible range (line clears, competitive shrink) emit a single full-board re-render so
no row is left stale. Cell appearance (fill + outline) is computed in one shared place,
`internal/render` — `CellStyle` returns RGBA for the native drawer and `CellStyleCSS` the
identical colors as inline CSS for the web — so the two UIs never visually drift.

**CAS-rejection feedback** (a move rejected by per-subject CAS) is UI-agnostic: the engine
emits the affected cells on the local `Updates` channel only (never published to NATS, so
other players see nothing). The native UI draws a one-shot ~600 ms rainbow **border** on
those cells in Gio; the web UI re-renders them with a `cell-flash` class and a matching
rainbow CSS animation.
