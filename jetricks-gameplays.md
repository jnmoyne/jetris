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
| Visible rows | 24 (rows 4-27) in cooperative mode; for competitive and teams this number depends on the number of players in the game |
| Standard width | 10 columns in competitive mode; for cooperative the width is `playerCount × 10`; for teams each team's board is `teamSize × 10` |

**Cell states:**

| State | Occupied | Active | Meaning |
|-------|----------|--------|---------|
| Empty | false | false | Nothing in this cell |
| Active | false | true | Part of a falling piece |
| Locked | true | false | Settled piece, permanent until line clear |
| Adversarial | true | false | Permanent garbage cell added by a competitive or teams shrink (the `Adversarial` flag is set); its row can never be completed or cleared |

On shared boards (cooperative, teams), active cells carry a `PlayerIdx` field (0-indexed, global across the whole game) identifying which player's piece they belong to.

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

**Spawn blocked (shared boards):** the same distinction gravity makes applies at spawn time. If the spawn cells are held by **locked cells**, the player tops out. If they are covered **only by another player's active (falling) piece** — a transient obstacle that will itself fall away — the spawn does **not** top out: it is deferred and retried as soon as the shared board changes (every incoming cell message may be the blocker moving away — at agent speeds a piece crosses the spawn cells in milliseconds), with the gravity tick as the backstop, until it succeeds or the cells become locked (a genuine top-out). Detection mirrors movement: `CanPlaceCoop` fails but `CanPlace` (which ignores active cells) succeeds. Without this rule, a teammate's piece merely crossing the spawn area would spuriously eliminate the player (and in cooperative end the game for everyone). The engine also runs a piece-less **watchdog** on the same gravity tick: an alive player with no piece and no deferred spawn for two consecutive ticks gets a forced (re)spawn, healing a spawn whose publish was lost on a board that has since gone silent. Neither the deferral retry nor the watchdog runs before the game starts.

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
| ← / → | move left / right | `MoveLeft` / `MoveRight` |
| ↓ | soft drop (one row) | `MoveDown` |
| ↑ or X | rotate clockwise | `RotateCW` |
| Z | rotate counter-clockwise | `RotateCCW` |
| Space | hard drop | `HardDrop` |

These are dispatched from `internal/nativeui/input.go` (the board tag is kept focused
with Gio's `key.FocusFilter` + `key.FocusCmd`).

### Gravity

Gravity ticks at the standard speed curve interval (see Section 7). On each tick, the engine attempts to move the piece down one row.

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

A clear must **not** disturb the other players' falling pieces: only the player whose piece completed the row gets a new piece; everyone else's piece keeps dropping, shifted down by the number of cleared rows. Only the cells that actually change are published, and within the atomic batch they are ordered by their **new** content — active cells first, locked cells second, vacated (now-empty) cells last. Vacating first instead, another player's mid-flight piece would be erased from its old cells before its new (shifted) cells arrived — its active-cell count would momentarily hit zero and that player's lock-in detector would fire a spurious lock, respawning their piece from the top. Active-cells-first keeps the shifted piece always present on the board, so it never transiently vanishes.

Because the board is shared, a clear must be reflected on **every** player's screen, not just the player whose piece completed the row. The changed cells are published to the shared per-cell playfield subjects and every player's cell consumer applies them, so the authoritative `playfield` state always converges. Rendering, however, is per-engine: the player who detected the clear re-renders the whole board directly, and every **other** player re-renders the whole board on receipt of the `EventLineClear` event. A full-board re-render (rather than relying on per-row repaint triggers) is used because a clear repaints the entire visible range at once, and individual per-row triggers can be dropped by the bounded, non-blocking UI update fan-out — which would otherwise leave stale, un-cleared rows on another player's board.

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
1. That player adds the score delta (and the cleared-line count) locally
2. An `EventLineClear` is published to NATS with the `Score` delta and `LinesCleared`
3. All other players (and spectators) receive the event and add the delta to their local score **and** the line count to their local `totalLines` — so the shared level stays in sync on every engine
4. All players' UIs update simultaneously

### Level Progression

Level = `totalLinesCleared / 10`, capped at 19. Level affects gravity speed (see Section 7). Level is computed independently by each engine from its local `totalLines` counter, which increases on both local clears and received `EventLineClear` events.

### Game Over

When **any** player tops out (newly spawned piece cannot be placed **on locked cells** — a spawn covered only by another player's falling piece waits instead, see Piece Spawning), the game ends for **all** players:
1. The topped-out player publishes `EventGameOver`
2. All other players' event consumers receive it and immediately transition to game over
3. All players see the "GAME OVER" overlay simultaneously

The overlay shows the team's final result — `Score: N (level L)`, the shared total — above the "Back to Lobby" button.

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
NATS brand blue — with NATS.io logos and "made with NATS.io" branding on the login
screen, lobby/archive banner, and game HUD.

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

The game continues until only **one player remains**. When a player tops out (either the newly spawned piece cannot be placed, or an opponent's line clear pushed the rising stack into the falling piece and the conflict-resolving upward shift ran the piece off the top), that player is eliminated and transitions to spectator mode — they can watch the remaining players continue. The last player standing wins. The game UI shows a player status list with each player marked as playing (green dot) or eliminated (red cross, struck through name). At game over, winners see "YOU WON!" and the loser(s) see "YOU LOST", and the overlay shows the player's own final result — `Your score: N (level L)` — above the "Back to Lobby" button. The winner’s screen also plays a **victory fireworks show** over the whole game screen: rockets rise from the bottom edge and every one bursts into a small **NATS "N" logo** (particles sampled from the embedded nats.io icon) — except one rocket in ten (guaranteed at least once per show, since the show loops a fixed choreography), which bursts into the **Synadia Symbol** instead (the official mark from synadia.com/about/brand — the white "S" swirl on the emerald rounded square), which pops in, holds for a beat, and then visibly splits into its small squares and blows apart — the blocks flying out in every direction like a bursting shell and shrinking away as they go, rather than fading in place. As they fly, each burst either keeps the logo’s own colors or transitions its blocks to one traditional fireworks color (gold, red, green, blue, purple, or silver) — chosen per rocket at random. The show loops until the winner heads back to the lobby, and is paint-only — it never blocks input or the "Back to Lobby" button.

---

## 5. Teams Mode

Two teams of equal size ("A" = team 0, "B" = team 1). **Within a team, play is cooperative** — teammates share one wide board with all the cooperative-mode mechanics (per-player sections, shared pieces as obstacles, merge-retry on the shared subjects). **Between the teams, play is competitive** — each team has its own independent board, line clears send unclearable garbage to the opposing team's board, and the last team with a player standing wins.

### Players & Teams

`TeamSize` players per team; `PlayerCount = 2 × TeamSize` total. Players **choose their team when joining** (Join A / Join B in the lobby); a join on a full team is rejected. The game transitions to `starting` when both teams are full. Each player keeps a **global** roster index (`PlayerIdx`, used for piece ownership and colors) plus a **team slot** (0..TeamSize-1, join order within the team) that selects their spawn section on the team board.

### Playfield

One shared board per team: width `teamSize × 10`, visible rows `24 + teamSize` (plus 4 headroom rows). Like competitive, the extra rows leave room for garbage; the producer here is the opposing team's `teamSize` piece-locking players. Cell subjects are scoped by team (`…team.<idx>.playfield.cell.<r>.<c>`), so the two boards are disjoint subject trees and each one behaves exactly like the cooperative shared board for its members.

### Piece Spawning

Coop rules per team: player at team slot N spawns centered in their section at column `N×10 + 3`, anchor row 2. Every player runs the full 7-bag sequence from the shared `meta.Seed` with an independent piece index (the coop scheme), so both teams see the identical, fair piece sequence.

### Movement, Gravity, Hard Drop, Lock-In

Identical to cooperative mode, scoped to the team board: teammates' active pieces are obstacles, a piece blocked downward only by a teammate's active piece waits instead of locking, and all engine-driven shared-cell writes use CAS merge-retry.

### Line Clears & Scoring

Coop scoring within the team: a clear scores `teamSize × lines` to the **team score**; every teammate folds the clearing player's score **and line count** from the line-clear event, so the team's level (and gravity speed) stays in sync for all members. The opposing team's clears do not affect your own team's score.

In addition to the own-team score, **every** engine — both teams' players, eliminated players, and spectators — folds **every** team's line-clear events into a per-team scoreboard: `TEAM A` / `TEAM B` score totals **and** per-team cleared-line totals, from which each team's level is derived. Both teams' scores (and, for spectators, levels) are therefore visible and live on every screen. Spectators who join mid-game converge by replaying the ordered events subject from the start.

### Garbage Attack (team shrink)

When a team clears N lines, N permanent adversarial rows are added at the bottom of the **opposing team's** shared board and that board's locked stack shifts up — the competitive shrink, applied to a shared board. Two deliberate differences from the competitive shrink, both consequences of the board being shared:

- **Application is race-managed, not single-writer.** Every alive member of the receiving team gets the shrink event and races to apply the identical transform. The application is guarded by an idempotency check (cumulative expected garbage vs. the count of bottom adversarial rows actually on the board — monotonic, since garbage is permanent and bottom-anchored) and published **with** per-subject CAS; on CAS failure the projection is thrown away and **recomputed from fresh state** (never blind-retried, which could double-shift the stack). Exactly one member's shift commits; the rest observe the deficit reach zero and stop.
- **No piece rides up.** Falling pieces (all of them, the applier's own included) hold their on-screen position while the stack rises around them. A piece overtaken by the risen stack sits in the holes its overlay preserved and simply **locks where it is on its next blocked drop** ("crushed") — it is never carried upward, and a shrink alone never tops a player out. Top-out on a full team board happens at spawn time instead (spawn cells held by locked/garbage cells; a teammate's passing piece only delays the spawn). (Lifting pieces on a multi-writer board would mean relocating *other* players' mid-flight pieces from a possibly-stale snapshot; holding every piece in place keeps the transform pure and symmetric across whichever teammate wins the application race.)

A known cosmetic artifact (same class as the documented coop merge-skip): the winning shift batch preserves teammates' active cells in place, so a shifted locked cell that would land under a teammate's piece is skipped; when that piece later moves away, the cell reads empty. Holes in the risen stack, never board corruption — per-cell sequence authority converges every replica. A garbage row therefore stays a garbage row as long as it holds at least one adversarial cell (crushed pieces can fill, and vacated overlays can hollow, some of its cells).

### Elimination & Game Over

**A player out is not a team out.** When a player tops out (their next spawn cannot be placed on locked cells — a spawn blocked only by a teammate's falling piece is deferred, not fatal), they vacate any of their active cells from the team board, publish their elimination, and become a spectator of their own team's board — but their teammates play on. The UI shows "YOU'RE OUT — your team plays on" until the game resolves.

A team **loses when ALL its members have topped out**. At that point every member of the other team — alive or already eliminated — wins: alive winners stop playing, and an eliminated member of the winning team sees their "you're out" flip to "YOUR TEAM WON!". Losers see "YOUR TEAM LOST". In both cases (and on the interim "you're out" box) the overlay shows both teams' scores and levels with the player's own team first — `TEAM A 42 (lvl 3) · TEAM B 17 (lvl 1)` — above the "Back to Lobby" button. All engines observe the same ordered event stream, so they reach the same verdict; the meta transition to `finished` is CAS-deduplicated across the winning engines. Every member of the winning team — including already-eliminated members, whose engines re-emit the win — gets the same victory fireworks show as a competitive winner (rockets bursting into small NATS "N" logos that then blow apart) on their own screen.

### Visual Indicators

- HUD shows `Teams · TEAM A/B`, a live per-team scoreboard (`TEAM A` and `TEAM B` scores, own team highlighted), and the team level; spectators instead see each team's score **and level** inline (`42 · lvl 3`) with no single SCORE/LEVEL stat
- Legend groups players under TEAM A / TEAM B headers with their global player colors; eliminated players are marked `(out)`
- The opposing team's board renders in the sidebar (labeled "OPPOSING TEAM")
- Spectators see both team boards side by side

---

## 6. Game Lifecycle

### Login & Server Selection

There is a single login screen where the player both picks where to connect and enters their name — no connection is made until they hit Play:

- If NATS CLI contexts are defined on the machine, a "Context:" option offers them in a pull-down button. The pull-down starts on the currently selected context (per `nats context select`); opening it lists every context — the selected one labeled "(selected)" — and picking one closes the list and makes it the choice.
- A "NATS URL" option is **always** available, pre-filled with `nats://demo.nats.io:4222`; typing in the URL field selects it automatically.
- A "LAN mode (embedded NATS server)" option is **always** available: hitting Play with it selected starts a JetStream-enabled NATS server **inside the Jetricks process itself** — default account, no authentication of any kind, listening on all interfaces on the port entered in the option's "Port:" field (default 4222; editing it selects the option automatically), storing its stream data in the local `jetstream-data` directory. While the option is selected the login screen shows the shareable URL (`Your server's URL is nats://<lan-ip>:<port>`), and once connected the lobby shows it too (`YOUR SERVER'S URL IS nats://<ip>:<port> — share this address so others can join you`) so the host can invite other players, who connect to that address via the NATS URL option. The embedded server keeps running until the window closes, so quitting to the login screen doesn't kick connected friends; logging back in with a **different** port restarts it on the new port.
- The `--server`/`--context` flags don't connect directly — they only set the picker's starting choice: `--server` selects the URL option and replaces the default URL text with its value; `--context` picks the context option with the pull-down preset to that context.
- A **Check connection** button tests the current choice without joining: it connects, measures the server's ping (round-trip time), shows `✓ <server> · ping <rtt>` in green (or the error in red), and disconnects. With "LAN mode (embedded NATS server)" selected it dials nothing and reports the address the embedded server serves (or would serve) on.
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
| — | created | Player clicks "Create Game" (open) or "Create & Invite" (invite-only) |
| created | starting | All player slots filled (roster full; in teams mode, both teams full). A join into an already-full game is refused |
| starting | [countdown] | All players click READY |
| [countdown] | in_progress | 5-second countdown completes |
| in_progress | finished | Game over (top-out) |
| finished | archived | Archive record published, game stream deleted (5s delay) |
| created/starting | cancelled | Creator absent, all players absent, or cleanup |
| any | (deleted) | A player deletes an **abandoned** game from the lobby (see below) |

### Abandoned Games

Some games go nowhere: the creator never joins, the players never click READY, or everyone walks away mid-game. While the lobby is up, every client re-checks the listed games once a minute (`AbandonedCheckInterval`) and flags a game as **abandoned** when either rule holds:

- **Never started** — the game is still `created` or `starting` more than **15 minutes** after creation (`AbandonedUnstartedTimeout`).
- **Started, then deserted** — the game is `in_progress` but its game stream has seen **no messages for one minute** (`AbandonedIdleTimeout`; a live game publishes constantly, so a silent stream means every player is gone). An `in_progress` listing whose stream no longer exists is flagged immediately — it can never make progress.

The check rebuilds the flag set from scratch each pass, so a game where activity resumes (e.g. a player reconnects) is un-flagged again.

An abandoned game's lobby row is marked `· abandoned` in red and grows a red **Delete** button next to Join/Spectate. Clicking it replaces the row's action buttons with a confirmation on its own line under the game info (so the question never squeezes the info text) — **"Are you sure you want to delete this game?"** with **Yes, delete** / **Cancel** — so a stray click can't destroy the game. Confirming tears down everything the game left behind: the per-game stream, the game's chat messages in the shared chat stream, and the lobby KV listing (whose deletion removes the game from every player's list).

### Creating a Game: Open vs. Invite-Only

A game is created **open** (anyone in the lobby may join it) or **invite-only**. The
create row's **"Invite only"** checkbox chooses; when checked the button reads
**"Create & Invite"**.

- **Open games** work as always: they list in the lobby with Join/Spectate buttons,
  and (for the applicable modes) an agent policy — the **"Allow agents"** checkbox and
  max-agents count — controls whether idle agents may take seats.
- **Invite-only games** are joined by invitation only. Creating one opens an
  **invitee picker** over the lobby, listing every OTHER player **currently idle in
  the lobby** (players already in a game can't be invited — you can only invite people
  free to play). The list is **live**: while the picker is open, players who join the
  lobby appear in it and players who leave (quit, or join a game) drop out, with your
  existing selections preserved; deselecting a player (or setting them back to **—**
  in teams) un-invites them. For competitive/cooperative games each row is a simple **Invite**
  checkbox; for **teams** games each row is a three-way selector (**— / A / B**) so you
  invite each player to a specific team. The picker shows the open seats live
  (total, or **Team A: k/size · Team B: k/size** for teams) and refuses to send a
  selection that over-fills the game or a team — so you can't invite more players
  than the game holds. **Send invites** delivers them; **Cancel** abandons the
  just-created game. The invited game's lobby row is tagged **· invite
  only** and shows no Join button to uninvited browsers (the creator, and anyone
  holding a pending invitation, still see theirs).

### Invitations

An invitation is a small record the inviter writes to the invitee's lobby mailbox
(one pending invitation per player; a newer one replaces an older, and they expire
after two minutes). Because every lobby watches the same store, the invitee sees it
immediately:

- **A human invitee** gets a **pop-up** — *"<inviter> invited you to a
  competitive/co-op/teams game"* (teams names the team) — with **Accept & Play** and
  **Decline**. Accepting joins the game (and, in teams, the team the inviter chose);
  declining dismisses it. Joining the game consumes the invitation.
- **An agent invitee accepts automatically**: a resident agent treats a pending
  invitation as the strongest join signal and joins the invited game (and team) at
  once. Inviting an agent is how you bring a *specific* agent into an invite-only
  game. If the join can't be honored (the invited team was already filled by other
  invitees, or the game filled without it), the agent **declines** the invitation and
  goes back to the lobby rather than retrying it — a stale invitation never wedges an
  agent.
- **The invitation is also permission**: an invited player joins even a game whose
  agent policy would otherwise exclude them (an invited agent bypasses the max-agents
  limit — the creator explicitly chose them). Uninvited players and agents who try to
  join an invite-only game are refused.

Third-party agents accept invitations by watching their own lobby mailbox — see
`jetricks-agent-guide.md`.

### Ready Flow

1. Players join the game and see the game page with a "WAITING FOR PLAYERS" header
2. Each player's ready state is shown as a filled pill badge next to their name: green "READY" / red "NOT READY"
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
    {"player_id": "Alice", "score": 42, "level": 2, "piece_count": 30, "winner": false},
    {"player_id": "Bob", "score": 42, "level": 2, "piece_count": 25, "winner": false}
  ],
  "started_at": "2026-03-21T10:00:00Z",
  "finished_at": "2026-03-21T10:05:30Z",
  "total_score": 42,
  "final_level": 2,
  "boards": [ /* end-of-game playfield snapshot(s) — see below */ ]
}
```

Each player result carries the `level` achieved at game end (derived from that engine's line total; sent in `EventGameOver`) and an `agent` flag (from the roster at archive time) marking seats that were played by agents. Cooperative records carry the shared `total_score` and `final_level`; the history list shows them plus per-player scores, and competitive history lines show each player's score and level.

**History controls:** the lobby's GAME HISTORY header carries a sort selector — **By score** (headline score, the default) or **By date** (most recently finished first) — and an **"Agent games"** checkbox (checked by default); unchecking it hides every game that had at least one agent seat. Records from before the agent flag existed read as all-human.

Each history line starts with the game's start date and time in the viewer's local timezone (e.g. `2026-07-06 14:03 PDT`) and how long the game lasted (`finished_at - started_at`, rounded to the second); records missing these timestamps omit the prefix. The history list is ordered by each game's headline score, highest first — the co-op total, the best team total for teams, or the best player score for competitive. When two games have the same score, the one with the **shorter game time** ranks higher; any remaining tie shows the most recently finished game first.

Teams games additionally carry `team_size`, `winning_team` (0 or 1; -1 = draw or not a team game), a `team` field on each player result, and the final per-team totals `team_scores` and `team_levels` (indexed by team, taken from the archiving engine's converged per-team scoreboard) — these are what the history list shows for a teams game (`A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave`). Every member of the winning team has `winner: true`, eliminated members included — a team win is shared.

**End-of-game playfield snapshot.** The record also carries `boards`: a snapshot of every board exactly as it stood when the game ended, captured by the winning/finishing client from the game stream (latest message per cell) just before that stream is deleted. There is one board for cooperative, one per player for competitive, and one per team for teams mode — so the snapshot is complete for every mode. Each board stores its width, visible height, and the non-empty cells (the raw cell messages). In the lobby, each game in **GAME HISTORY** has a **"View board"** button that opens a viewer redrawing these boards — the picture of the playfield at the moment that game ended.

---

## 6b. Chat

There are two chat scopes, sharing one NATS stream and distinguished purely by subject naming: the **lobby chat** (`jetricks.lobby.chat`, shown on the lobby screen) and a **per-game chat** (`jetricks.lobby.chat.game.<gameID>`), seen only by that game's players and spectators.

On the game screen (player or spectator) a chat strip is displayed at the bottom:

- It shows the game's messages, plus the lobby chat folded in — lobby lines are prefixed `@lobby` and rendered in a distinct color so they're obviously not from the game. Spectators' game messages are marked `(spec)`.
- **Before the game starts**, both players and spectators can type.
- **Once the game is in progress**, only spectators (and eliminated players) can type — a playing player's keyboard drives the piece, so their input line is replaced by a "read-only while playing" hint.
- A message starting with `@lobby` is sent to the lobby chat (everyone sees it); anything else goes to the game's chat.

Lobby chat history is retained for 7 days; a game's chat messages are purged from the stream when the game is archived. A player joining the lobby replays the retained history (up to the last 200 messages), so everyone in the lobby sees the same chat log regardless of when they logged in.

---

## 7. Gravity Speed Curve

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

## 8. Spectate Mode

Any player in the lobby can spectate an in-progress game:
- Engine starts in `ModeSpectator` (no gravity, no move input)
- The game page hides all controls and the ready button
- Shows "Spectating" status
- **Player legend:** Shows each player's name with their assigned color swatch
- **Colored outlines:** Each player's active piece has a distinct colored outline (not white)
- Spectators see the same real-time playfield updates as players
- **Countdown:** the pre-game 5..0/GO! countdown is drawn over the spectator's boards exactly as over a player's board (a spectator who joins before the game starts sees the same start moment everyone else does)
- **Eliminations:** an eliminated player's board (competitive) — or a fully eliminated team's board (teams) — carries a centered **OUT** chip in the spectator's multi-board view; the board itself stays fully visible under it
- **Winner:** once the game is decided, the surviving player's board reads **WINNER** (competitive) and the surviving team's board **WINNERS** (teams); a simultaneous-top-out draw shows every board OUT with no winner
- The HUD keeps a "Back to Lobby" button, so a spectator can leave the game and return to the lobby at any time

---

## 9. CAS (Compare-And-Swap) Strategy

All playfield state is stored in JetStream as **one message per cell** — each (row, col) position has its own subject carrying that cell's current content (an empty cell marshals to `{}`, the vacate payload). Concurrent writes are managed via per-subject CAS (`Nats-Expected-Last-Subject-Sequence`) on atomic batch publishes — a multi-cell move either commits all its cells or none, never a torn intermediate. Every publish path diffs its projection against the live board and publishes only the cells that changed (a move is typically 4-8 cell messages), and within every batch cells are ordered by their new content — active first, locked/occupied second, empty vacates last — so a relocating piece never transiently has zero active cells and lock-in fires exactly once, at the last vacate, with all landed cells already applied.

### Optimistic sequence write-through (both modes)

The CAS expectation for every write is the per-cell `pf.CellLastSeq(row, col)` — the stream sequence of the last message the engine has seen on that cell's subject. Historically this advanced **only** when the engine's own consumer echoed a published cell back, so between publishing a write and consuming its echo the in-memory sequence (and board content) lagged the stream. Any second write issued into that window — a gravity tick, the next keypress, or a write right after a NoCAS line-clear/shrink — carried a stale expectation and lost CAS to the engine's *own* earlier write, dropping the step and flashing.

To close that window, **a successful publish is written through into the in-memory playfield immediately**, without waiting for the echo. The batch commit ack returns the stream sequence of the last message in the batch; because an atomic batch's messages get consecutive stream sequences, the engine infers every cell's sequence (`message i of N → commitSeq − (N−1−i)`) and applies the just-committed content **and** sequence to its playfield. The next move/gravity tick is therefore projected from — and CAS-checked against — up-to-date state and cannot self-race.

Reconciliation with the consumer echo is automatic and is the single rule in `Playfield.Apply`: a cell is updated only if the incoming sequence is **strictly higher** than the one in memory. The echo of our own write carries the **same** sequence we already wrote through, so it is skipped (a harmless no-op); only a **higher** sequence — the other player's write in cooperative mode, or a NoCAS write we did not originate — updates memory. This applies in both competitive and cooperative modes (in coop the write-through applies only what actually committed — the first-attempt batch, or the merged batch after a merge-retry — so it never clobbers the other player's cells).

### Player-initiated moves (left, right, down, rotate, hard drop)

CAS failure = **move is dropped, no retry, in either game mode**. The player must press the input again. The engine signals the failure with a **rainbow flash on the outline of the player's own piece** — cells of the active piece cycle through the seven spectrum colors over ~600 ms with a matching glow, then revert. The flash shows on the flashing player's own board, and — so a watcher sees the same contention feedback the players do — is also broadcast to **spectators**: the player publishes a transient **core NATS** message (fire-and-forget, on `jetricks.flash.<gameID>.<playerID>`, deliberately NOT on the game stream so it is never persisted or replayed) that spectators subscribe to and render on that player's board. Other **players** do not subscribe, so a player still sees only their **own** CAS flashes; only spectators see everyone's.

This is intentional in cooperative mode where two players share one playfield: CAS rejections are routine and a silent server-side retry would mask conflicts from the player and make their input timing feel non-deterministic. Loud, immediate, local-only feedback gives the player full agency over how to recover.

The flash only ever fires for player-initiated moves. Engine-driven moves (gravity ticks, piece spawns) never flash.

### Engine-driven state changes (gravity, spawn, lock-in)

Gravity ticks and piece spawns must succeed even under contention — a dropped gravity tick would make the piece visibly freeze for one tick, and a dropped spawn would leave the player pieceless. Neither was player-initiated, so a flash would be misleading.

**Single-goroutine invariant (both modes):** a player's gravity ticks and their own input are processed on **one** engine goroutine (`runInput`), which `select`s over the moves channel and the gravity timer. This and the optimistic write-through above together remove a player's self-races: `runInput` ensures a gravity drop and a move never *project and publish concurrently*, and the write-through keeps that serialized writer's in-memory sequence current so even back-to-back writes don't carry a stale CAS expectation. Together they eliminate the spurious rainbow flashes that were otherwise visible in competitive play, where each player owns their cell subjects and a self-race was the only way two of *their* writes could contend.

In **competitive mode** each player owns their cell subjects, so — with gravity and input serialized on `runInput` and write-through keeping the view current — their writes do not contend with another player's in normal play.

In **cooperative mode** the players share the same cell subjects, but with per-cell granularity only writes to the **same cell** actually contend — two players moving in different parts of the board (even on the same row) never conflict, so contention is rare. On CAS failure the engine refetches the latest message for every affected cell in one batched round trip and merges per cell: this player's content is kept except where the stream now holds the other player's mid-flight (active) piece — those cells are skipped entirely, never overwritten or vacated. It then retries the atomic batch with refreshed per-subject CAS expectations (up to 16 attempts, with a short per-player-offset backoff between tries that breaks lockstep with the other player's retry loop).

**Teams mode** uses the cooperative scheme verbatim within each team's board (same merge-retry, same skip rule — "another player's active cell" naturally means a teammate's).

Locks are published as no-CAS authoritative writes (see below) and so cannot fail CAS.

### Shared-board shrink (teams): recompute-on-CAS-failure with a garbage-row-count guard

A teams garbage attack is the one write that is **neither** single-writer authoritative (every alive member of the receiving team gets the event and may apply it) **nor** merge-retry safe (a blind merge would republish a *stale* stack shift after a teammate's shift already committed, double-shifting the board). It gets its own discipline:

- **Idempotency guard:** each engine accumulates the garbage rows its team is owed (`expectedGarbage`) and compares it to the number of garbage rows actually at the bottom of the converged board. Garbage is permanent and bottom-anchored, so that count only grows toward the target; a deficit of zero means the shift (anyone's) already landed and there is nothing to do. A row counts as a garbage row while it holds **at least one** adversarial cell — crushed pieces can fill, and vacated piece overlays can hollow, some of its cells.
- **CAS with full recompute:** the shift batch is published *with* per-subject CAS expectations. On failure the projection is discarded; the engine waits for its consumer to converge, re-checks the deficit guard, and re-projects from fresh state (bounded retries with per-player-offset waits). Any batch computed from a stale or torn board necessarily carries a stale expectation on at least one bottom-row cell — the winning batch wrote the full board width — so CAS rejects it. Exactly one teammate's shift commits per deficit.
- **No piece relocation:** the projection overlays every active piece at its current position (see Teams Mode), so the batch never touches piece cells, no lock-in can fire spuriously on any teammate, and the transform is identical regardless of which teammate wins the race.

This guard also makes event replay safe: an engine that (re)starts mid-game refetches the board first, so replayed shrink events find their garbage already on the board and skip.

### Authoritative writes (lock, hard-drop landing, line clear, opponent-shrink application)

**No CAS.** The publisher's view is the new ground truth. The changed cells are published as a single atomic no-CAS batch via `PublishCellsAtomicallyNoCAS` so consumers either see the entire authoritative state change at once or not at all. The same content-ordered batching applies (active cells first, occupied second, vacates last), so e.g. a competitive shrink re-stamps the falling piece before the rising stack and vacates last. (The teams shrink is the exception — it is CAS-guarded, see above — because the receiving board has multiple writers.)

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

### Buffered moves line

Player inputs are serialized: each move's batch publish blocks on its commit ack before
the next move is dequeued, so on a high-RTT server inputs typed during an in-flight
publish wait in the engine's move buffer. A small muted line directly below the
playfield shows that queue, oldest first (e.g. `← ← CW HD`) — each entry
appears when the input is accepted into the buffer and disappears the moment its own
batch publish starts. The line is empty (and invisible) at low latency, where moves are
dequeued as fast as they are typed; spectators have no input and never show it.

### NATS message panel

A **"Show NATS messages"** checkbox sits in the left HUD column while playing or
spectating. When checked, a fixed-height monospace strip appears across the bottom of
the window showing the live tail (last 200) of the messages the engine's game-stream
consumers deliver — the exact messages that update the in-memory playfields and drive
the UI: own/opponent/team cell echoes, game events, meta transitions, countdown ticks,
and roster entries. Each line prints the message's **JetStream stream timestamp** (taken
from the received message's metadata, not the local arrival time), its **subject**, and
its raw **JSON payload**, syntax-colored (keys blue, string values green, numbers gold,
`true`/`false`/`null` orange). The list sticks to the newest message. Messages are only
collected while the box is checked, and the log is cleared when entering or leaving a
game.

### Lobby branding

The lobby screen carries a banner across its top — the nats.io "N" logo flanking
"Jetricks: peer to peer and made with NATS.io" — above the player/chat and
games/history columns.

---

## 11. Agents and the reference agent (`mk1`)

An agent is any standalone program that plays Jetricks by speaking the game's NATS
protocol — there is no plugin interface; the contract is the wire protocol and the
fair-play rules in `jetricks-agent-guide.md` (with these game rules). Agents can be
written in any language and contributed to the repo under `agents/<name>/`
(see `agents/README.md`); each plays under a name of the form
`<agent-name>-<instance>-<difficulty>` so rosters and history record exactly which agent,
which running copy, and how strong.

Jetricks ships one reference agent, **`mk1`** (`jetricks-agent`), written in Go, that plays
**all three modes** — cooperative, competitive, and teams. It is deliberately an ordinary
peer — the same `lobby` join handshake, the same `engine` (all six moves: left, right,
down, rotate CW/CCW, hard drop), the same consumers and CAS discipline — with a planner
where the GUI has a keyboard. Nothing in the blackboard needed to change to admit a
software agent: the agent demonstrates that a NATS-coordinated peer-to-peer game is
equally playable by humans and programs. `mk1` reuses the game's own engine code because it
lives in the repo; a third-party agent implements the same behavior over the wire.

### How it plays

- **Perception:** the agent plans only against **committed** state (`engine.Playfield()`),
  never against predictions — the same no-client-side-prediction rule every player
  lives under.
- **Planning:** for each piece it enumerates every placement reachable with its move
  vocabulary (SRS rotations in place, one-column slides, hard drop), simulates the lock
  and line clear on a board copy, and scores the result with Pierre Dellacherie's
  six-feature heuristic (landing height, eroded cells, row/column transitions, holes,
  cumulative wells). It plans one piece at a time: the piece sequence is deterministic
  from the game seed (§4), but the UI shows humans no next-piece preview, so reading
  the seed to look ahead would violate the fair-visibility contract.
- **Execution:** moves are issued one at a time — observe the piece, dispatch the one
  move that advances it toward the target, wait for the effect to appear on the
  committed board, repeat, hard drop. A move that never takes effect (collision
  rejection, or a CAS loss against incoming garbage) or garbage rows arriving mid-plan
  trigger a re-plan from live state; after three re-plans on one piece the agent just
  hard-drops rather than stall the game.
- **Garbage awareness:** adversarial shrink rows are priced in naturally — they count
  as locked stack for every feature and the clear simulation refuses to complete them,
  exactly like the engine's `Row.IsFull`.
- **Shared boards (cooperative, teams):** planning switches to the same collision
  variant the engine uses — another player's mid-flight piece is a temporary obstacle
  (`CanPlaceCoop`/`RotateCoop`/`HardDropDestinationCoop`). The agent plans over the
  whole wide board; teammates fighting for the same cells resolve through the normal
  CAS discipline — a dropped move stalls, the agent re-plans from the converged board.

### Fair visibility: agents see only what humans see

An agent may base decisions ONLY on information a human player can see in the UI:
the committed boards (its own and the opponents'/teams'), the roster and
eliminations, scores/levels, the countdown, and its own falling piece. It may NOT
read the game seed to predict upcoming pieces (the UI shows no next-piece preview),
nor any stream state the UI does not render. This is the visibility contract every
agent implementation must honor — see `jetricks-agent-guide.md`.

### Per-mode outcomes

- **Cooperative:** the agent plays for the shared score; when anyone tops out the game
  ends for everyone and the agent reports `OVER` with the shared score (there is no
  winner). If the agent itself is the topper, its engine finishes the game and it
  archives before leaving, like any GUI topper.
- **Competitive:** last standing wins; the agent reports WON/LOST as before.
- **Teams:** the agent picks the team with the most free seats when auto-joining (and
  retries the other team if it loses that race). Its own top-out is not the outcome —
  the team plays on — so an eliminated agent stays connected until the verdict: the
  engine's authoritative game-over update, backed by polling the roster's
  eliminations (one team fully dead) in case the lossy update channel dropped it. A
  winning-team agent archives the game (every winner's engine fires the archive hook;
  the transition is CAS-protected so duplicates are safe).

### Difficulty levels

| Knob | Easy | Medium | Hard |
|------|------|--------|------|
| Think pause per piece | 1500 ms | 600 ms | 100 ms |
| Pause between moves | 300 ms | 150 ms | 30 ms |
| Blunder rate (P of not playing the best move) | 30% | 10% | 0 |
| Blunder depth (picks among ranks 2..N+1) | 4 | 2 | — |

Hard plays the best placement it finds, as fast as the NATS round-trips allow. Easy and
medium think slower, pace their moves, and sometimes deliberately play a lower-ranked
placement, so they are beatable and fun.

### Agent policy: who decides whether agents may join

Every game (any mode) carries its creator's **agent policy**: `MaxAgents`, the number of
roster seats agent players may take (0 = agents may not join). In the GUI's create row the
policy is an **"Allow agents" checkbox** (off by default — games are human-only unless
opted in) plus a **Max** count, shown for every game mode. `lobby.JoinGame`
enforces the policy **atomically inside its CAS loop**: an agent joining a no-agents game
gets `ErrAgentsNotAllowed`, and once `MaxAgents` roster seats are held by agents further agent
joins get `ErrAgentSlotsFull` — so several idle agents racing for the last agent seat can
never over-fill it. Agents are first-class but visible: an agent's player name has
**three parts** — `<version>-<instance>-<difficulty>`, e.g. **`mk1-3f7a-hard`**. The
version stem names the agent's CODE generation (`mk1` uses its `Codename`,
currently `mk1`, bumped whenever its play logic changes; third-party agents use
their own stem via `--name`/`Config.Name`); the instance id is 4 random hex chars
minted fresh for every connection, so several copies of one agent version can play
at once and each connection is distinguishable; the difficulty labels its strength.
The name doubles as the NATS player ID, which appears in subject tokens AND in the
presence KV key, whose charset is stricter (`[-/_=.a-zA-Z0-9]`), and the whole must
fit the 32-character cap — so opponents, spectators and the archive all see exactly
which agent, which copy, and how strong. Their presence entries and roster seats
are additionally flagged, and the UI tags them `[agent]` in the lobby player list, game
listings, ready roster and in-game legend; game rows show `agents k/N` when a game
allows them.

### Lobby behavior: agents are residents

With no `--join`/`--create`, an agent is a **lobby resident**: it idles in the lobby,
auto-joins the oldest game of any mode that allows agents and has a free seat (in teams,
a free seat on some team) and a free agent seat,
plays it to the end, returns to the lobby, and repeats until interrupted (`--once`
restores play-one-game-and-exit). A "agent that is not currently playing" is simply one
sitting in the lobby scanning — a playing agent can't join anything else. If a joined
game never starts (nobody shows up or readies), the agent **un-joins** after its wait
timeout — `lobby.UnjoinGame` removes it from the roster (reverting a full `starting`
game to `created`) and purges its roster announcement so it never lingers as a ghost
seat — then goes back to scanning.

In every seat it takes, the agent carries the same lifecycle responsibilities as a GUI
player:

- It joins via `lobby.JoinGame` (guarding first that the game is not yet
  running, has a free seat, and allows agents), toggles READY, and — if its toggle is
  the one that completes the ready set — **it runs the 5..0 countdown and transitions
  the game to in_progress**, exactly like the GUI client in that seat.
- If it wins, its engine drives the finished transition, so **it archives the game**
  (`ArchiveAndCleanup`) before moving on, like any winning player.
- On losing it moves on after a short grace (or lingers until the game finishes with
  `--linger`).

One-shot game selection remains CLI-driven: `--join <gameID>` for a specific game
(still subject to that game's agent policy), or `--create --mode
cooperative|competitive|teams --players N [--max-agents M]` to host one (`--players`
is per team in teams mode, like the GUI's count) — agent-hosted games allow agents in
all seats by default, since the host itself takes one.
