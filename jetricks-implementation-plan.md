# Jetricks — Implementation Plan

This document is the authoritative implementation guide for Claude Code. It contains
everything needed to implement Jetricks from scratch. The full design specification is
in `jetricks-project-structure.md` (same directory); refer to it for rationale and
extended explanations. Gameplay mechanics (cooperative/competitive/teams modes, scoring,
gravity, line clears, game lifecycle) are defined in [`jetricks-gameplays.md`](jetricks-gameplays.md);
this plan defers to that document for gameplay behavior. This plan is structured to be
executed in strict phase order — each phase's packages are prerequisites for the next.

---

## Project Bootstrap

```bash
mkdir jetricks && cd jetricks
go mod init jetricks
go get github.com/nats-io/nats.go
go get github.com/nats-io/nats.go/jetstream
go get github.com/synadia-io/orbit.go/natscontext
go get github.com/synadia-io/orbit.go/jetstreamext
go get github.com/google/uuid
go get gioui.org                                    # native UI
go get github.com/nats-io/nats-server/v2/server   # embedded server for tests only
```

> The native (Gio) UI in `internal/nativeui` is the sole front end. Gio is pure-Go
> (no external C libs) on macOS/Windows; Linux builds need X11/Wayland/EGL/Vulkan
> dev headers.

Create the full directory tree from Section 1 of the spec.

---

## Phase 1 — Leaf Packages (no NATS, no goroutines)

Implement these first. They have zero internal dependencies and are fully unit-testable
without any infrastructure.

### 1.1 `internal/config`

**File:** `internal/config/config.go`

Everything in one file. Implement in this order:

**Types:**
```go
type Config struct {
    NATSContext  string
    NATSURL      string // --server (overrides context)
    NATSUser     string
    NATSPassword string
}

type GameMode int
const (
    ModeCooperative GameMode = iota
    ModeCompetitive
    ModeTeams // String() == "teams"
)

// TeamCount is the number of teams in a teams-mode game. Team indices are
// 0 ("A") and 1 ("B").
const TeamCount = 2

type GameStatus string
const (
    GameStatusCreated    GameStatus = "created"
    GameStatusStarting   GameStatus = "starting"
    GameStatusInProgress GameStatus = "in_progress"
    GameStatusFinished   GameStatus = "finished"
    GameStatusArchived   GameStatus = "archived"
    GameStatusCancelled  GameStatus = "cancelled"
)

type GameMeta struct {
    GameID      string     `json:"game_id"`
    Mode        GameMode   `json:"mode"`
    PlayerCount int        `json:"player_count"`
    TeamSize    int        `json:"team_size,omitempty"` // teams mode: players per team (PlayerCount = TeamCount*TeamSize)
    Seed        uint64     `json:"seed"`
    Status      GameStatus `json:"status"`
    CreatorID   string     `json:"creator_id"`
    CreatedAt   time.Time  `json:"created_at"`
    StartedAt   time.Time  `json:"started_at,omitempty"`
    FinishedAt  time.Time  `json:"finished_at,omitempty"`
    Abandoned   bool       `json:"abandoned,omitempty"`
    PieceIdx    uint64     `json:"piece_idx"`
}
```

**Constants:**
```go
const (
    TotalRows              = 48   // max rows (supports competitive with many players: 24 + playerCount visible + 4 headroom)
    HeadroomRows           = 4
    VisibleRows            = 24   // base visible rows (cooperative default)
    VisibleRowStart        = 4    // base visible row start (cooperative; competitive adjusts per game)
    StandardWidth          = 10
    LobbyKVBucket          = "JETRICKS_LOBBY"
    LobbyChatStream        = "JETRICKS_LOBBY_CHAT"
    LobbyChatSubject       = "jetricks.lobby.chat"
    LobbyChatMaxAge        = 7 * 24 * time.Hour
    ArchiveStream          = "JETRICKS_ARCHIVE"
    ArchiveSubject         = "jetricks.archive"

    PresenceHeartbeat      = 5 * time.Second

    // Abandoned-game detection (lobby.runAbandonedChecker)
    AbandonedCheckInterval    = 1 * time.Minute   // how often each client re-checks
    AbandonedIdleTimeout      = 1 * time.Minute   // in_progress: max stream silence
    AbandonedUnstartedTimeout = 15 * time.Minute  // created/starting: max age since CreatedAt
)

// CompetitiveVisibleRows returns the number of visible rows for a competitive
// game with the given player count: VisibleRows + playerCount (each extra player
// makes the board one row taller).
func CompetitiveVisibleRows(playerCount int) int {
    return VisibleRows + playerCount
}

// CompetitiveTotalRows returns the total rows (headroom + visible) for a
// competitive game with the given player count.
func CompetitiveTotalRows(playerCount int) int {
    return HeadroomRows + CompetitiveVisibleRows(playerCount)
}

// CompetitiveVisibleRowStart returns the first visible row index for a competitive
// game. Headroom is a constant, so this always equals HeadroomRows (4); the board
// grows taller per player rather than shifting the visible window down.
func CompetitiveVisibleRowStart(playerCount int) int {
    return HeadroomRows
}

// TeamBoardWidth returns the width of one team's shared board: one standard
// 10-column section per teammate, like the cooperative board.
func TeamBoardWidth(teamSize int) int {
    return teamSize * StandardWidth
}

// TeamVisibleRows returns the visible rows for a team board. Like competitive,
// the board grows one row per garbage-producing player on the opposing team
// (which has teamSize players), leaving room for adversarial rows.
func TeamVisibleRows(teamSize int) int {
    return VisibleRows + teamSize
}

// TeamTotalRows returns the total rows (headroom + visible) for a team board.
func TeamTotalRows(teamSize int) int {
    return HeadroomRows + TeamVisibleRows(teamSize)
}

// TeamVisibleRowStart returns the first visible row index for a team board.
func TeamVisibleRowStart(teamSize int) int {
    return HeadroomRows
}
```

**Archive types:**
```go
type PlayerResult struct {
    PlayerID   string `json:"player_id"`
    Score      int    `json:"score"`
    Level      int    `json:"level,omitempty"` // level achieved at game end
    PieceCount uint64 `json:"piece_count"`
    Winner     bool   `json:"winner,omitempty"`
    Team       int    `json:"team,omitempty"` // teams mode: 0 = A, 1 = B
}

type ArchiveRecord struct {
    GameID      string         `json:"game_id"`
    Mode        GameMode       `json:"mode"`
    PlayerCount int            `json:"player_count"`
    Players     []PlayerResult `json:"players"`
    StartedAt   time.Time      `json:"started_at"`
    FinishedAt  time.Time      `json:"finished_at"`
    TotalScore  int            `json:"total_score,omitempty"` // cooperative mode only
    FinalLevel  int            `json:"final_level,omitempty"` // cooperative: shared level at game end
    TeamSize    int            `json:"team_size,omitempty"`   // teams mode
    WinningTeam int            `json:"winning_team"`          // teams mode: 0 or 1; -1 = draw or not a team game
    TeamScores  []int          `json:"team_scores,omitempty"` // teams mode: final score per team
    TeamLevels  []int          `json:"team_levels,omitempty"` // teams mode: final level per team
    Boards      []BoardPicture `json:"boards,omitempty"`      // end-of-game playfield snapshot(s)
}

// BoardPicture is the saved end-of-game state of one board (latest cell message
// per cell of the visible region). One per cooperative game, one per player for
// competitive, one per team for teams. BoardCell.Data is the raw cell message.
type BoardPicture struct {
    Label  string      `json:"label,omitempty"` // player ID, "Team A"/"Team B", or "" (cooperative)
    Idx    int         `json:"idx"`             // player/team index for coloring; -1 if n/a
    Width  int         `json:"w"`
    Height int         `json:"h"` // visible row count (row 0 = first visible row)
    Cells  []BoardCell `json:"cells,omitempty"`
}

type BoardCell struct {
    Row  int             `json:"r"`
    Col  int             `json:"c"`
    Data json.RawMessage `json:"d"`
}
```

**Subject builders** — implement all of these, producing the exact strings shown:
```go
func GameStream(gameID string) string
// → "JETRICKS_GAME_" + gameID

func GameSubjectFilter(gameID string) string
// → "jetricks.game." + gameID + ".>"

// The playfield is stored in NATS as ONE MESSAGE PER CELL (x/y position) —
// each cell of the board is its own subject, and the last message on that
// subject is the cell's current content.
//
// Cooperative, competitive and teams modes use ENTIRELY SEPARATE cell subject
// schemes — not one builder parameterised by playerID. A game is exactly one mode.
//
// Cooperative — single shared board, no player token (ownership in the payload
// via Cell.PlayerIdx; coop never filters cells by player):
func CoopCellSubject(gameID string, row, col int) string
// → "jetricks.game." + gameID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
func CoopCellSubjectFilter(gameID string) string
// → "jetricks.game." + gameID + ".playfield.cell.>"

// Competitive — per-player board scoped by player UUID:
func CompetitiveCellSubject(gameID, playerID string, row, col int) string
// → "jetricks.game." + gameID + ".player." + playerID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
func CompetitiveCellSubjectFilter(gameID, playerID string) string
// → "jetricks.game." + gameID + ".player." + playerID + ".playfield.cell.>"

// Teams — one shared board PER TEAM. Like the cooperative scheme the subject
// carries no player token (all teammates publish to and consume from the same
// cell subjects; per-cell ownership lives in the payload via Cell.PlayerIdx,
// which holds the GLOBAL roster index), but the board is scoped by team index
// so the two teams' boards are disjoint:
func TeamCellSubject(gameID string, team, row, col int) string
// → "jetricks.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
func TeamCellSubjectFilter(gameID string, team int) string
// → "jetricks.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell.>"

func MetaSubject(gameID string) string
// → "jetricks.game." + gameID + ".meta"

func RosterSubject(gameID, playerID string) string
// → "jetricks.game." + gameID + ".roster." + playerID

func EventsSubject(gameID string) string
// → "jetricks.game." + gameID + ".events"

func CountdownSubject(gameID string) string
// → "jetricks.game." + gameID + ".countdown"

func GameChatSubject(gameID string) string
// → "jetricks.lobby.chat.game." + gameID (on the CHAT stream, not the game
//   stream — game streams keep only the latest message per subject). Lobby and
//   game chat share one stream, distinguished purely by subject; the consumer
//   derives the scope via GameIDFromChatSubject(subject) ("" = lobby).

func LobbyPlayerKey(playerID string) string
// → "players." + playerID

func LobbyGameKey(gameID string) string
// → "games." + gameID
```

The archive subject is the `ArchiveSubject` const (`"jetricks.archive"`), not a
builder function.

**Tests** (`internal/config/config_test.go`):
- Verify every subject builder returns the exact expected string for a known gameID/playerID/(row, col).
- Verify `CompetitiveCellSubject` produces the correct per-player subject, `CoopCellSubject` produces the shared subject with no player token, and `TeamCellSubject` produces the team-scoped subject (also with no player token); likewise for the `*CellSubjectFilter` builders.
- Verify the team board-dimension helpers: `TeamBoardWidth(teamSize) == teamSize×10`, `TeamVisibleRows(teamSize) == 24+teamSize`, `TeamTotalRows`, `TeamVisibleRowStart == 4`.
- Verify `GameStream` produces a valid NATS stream name (alphanumeric + dash + underscore only, `strings.ContainsAny` check).

---

### 1.2 Player Name Validation (in `internal/config`)

Player identity is handled at runtime through the UI — no persistent identity file.
The player name entered in the UI is used as both the player ID and display name.
Add the following validation function to `internal/config/config.go`:

```go
// ValidatePlayerName checks that a player name is valid for use as a
// NATS subject token (and thus as a player ID).
func ValidatePlayerName(name string) error
```

Rules:
- Must be 1–32 characters
- Cannot contain `.`, ` ` (space), `*`, `>`, tab, newline, carriage return, or null

**Tests:** valid names pass, names with dots/spaces/wildcards are rejected, empty and
overly long names are rejected.

#### Lobby name-collision check (in `internal/lobby/presence.go`)

Login (`nativeui.App.doLogin`) calls `lobby.IsNameInUse(ctx, kv, name) (bool, error)`
after the shape validation. The function lists `players.*` keys in the lobby KV bucket
and returns true if any active presence entry has a matching `Name`
(case-insensitive, whitespace-trimmed). Stale entries — `LastSeen` older than
3× `config.PresenceHeartbeat` — are skipped so a previous unclean shutdown
doesn't permanently block re-entry under the same name. An empty bucket
(`jetstream.ErrNoKeysFound`) is treated as "no one in the lobby".

If `IsNameInUse` returns true (and the login was not forced), the login screen
shows a confirmation prompt. Its **Yes, join** button re-runs `doLogin` with
`force=true`, which skips the collision check and proceeds with `initLobby`.
**Cancel** clears the prompt so the player can pick a different name.

---

### 1.3 `internal/game`

Pure game logic. Six files. No imports from other internal packages.

#### `internal/game/piece.go`

The 7 standard piece types. Each piece is defined by a 4x4 bitmask for each of its
4 orientations. Store these as `[4][4][4]bool` tables or as `[][2]int` cell offset
tables — whichever you prefer. The `Cells()` method must return the (row, col) offsets
occupied by the piece at a given orientation relative to its anchor (top-left of
bounding box).

```go
type PieceType int
const (
    PieceI PieceType = iota
    PieceO
    PieceT
    PieceS
    PieceZ
    PieceJ
    PieceL
)

type Piece struct {
    Type        PieceType
    Orientation int    // 0–3, clockwise from spawn
    Row         int    // anchor row in playfield (0 = top of headroom)
    Col         int    // anchor column
}

// Cells returns (row, col) pairs of cells occupied by this piece.
// Each pair is relative to the playfield origin, not the anchor.
func (p Piece) Cells() [][2]int
```

Use the standard Tetris piece shapes:
- I: horizontal line of 4
- O: 2x2 square
- T: T-shape
- S, Z: S and Z skews
- J, L: J and L hooks

Spawn orientation 0 is the standard "spawn state" per Tetris Guideline.

#### `internal/game/row.go`

```go
type Cell struct {
    Occupied    bool      `json:"o,omitempty"`
    PieceType   PieceType `json:"t,omitempty"`
    Active      bool      `json:"a,omitempty"`
    Orientation int       `json:"r,omitempty"`
    AnchorRow   int       `json:"ar,omitempty"`
    AnchorCol   int       `json:"ac,omitempty"`
    PlayerIdx   int       `json:"pi,omitempty"` // which player's active piece (cooperative mode)
    Adversarial bool      `json:"g,omitempty"`  // permanent adversarial cell (competitive shrink); row can never be completed
}

// Marshal encodes the cell as JSON. An empty cell encodes as "{}" (every
// field is omitempty), which is the payload published to VACATE a cell.
func (c Cell) Marshal() ([]byte, error)        // json.Marshal
func UnmarshalCell(data []byte) (Cell, error)  // json.Unmarshal

// CellPos identifies one cell of the playfield by position. Used as the key
// of per-cell projection diffs and publish batches.
type CellPos struct {
    Row int
    Col int
}

// Row is the IN-MEMORY representation only — the playfield is stored in NATS
// as one message per cell, so Row never marshals to a NATS payload. (There is
// no Row.Marshal/UnmarshalRow.)
type Row struct {
    Cells []Cell `json:"cells"`
}

func NewRow(width int) Row        // empty row of the given width
func (r Row) Clone() Row          // deep copy (Cell is all scalars)
func (r Row) Equal(other Row) bool
func CloneRows(rows []Row) []Row  // deep copies, safe for concurrent reads
func (r Row) IsFull() bool        // all occupied, none active, no adversarial
func (r Row) IsEmpty() bool
```

The wire payload of a playfield message is a single `Cell` JSON. Key invariant:
**all Active cells belonging to the same piece carry identical Orientation,
AnchorRow, AnchorCol values**. This allows the piece to be reconstructed from
any single active cell. An empty (zero-value) cell marshals to `{}` — that is
the exact payload published to a cell's subject to vacate it.

#### `internal/game/playfield.go`

```go
type Playfield struct {
    Width   int
    Height  int        // total rows (headroom + visible); set at construction
    Rows    []Row      // length == Height
    LastSeq []uint64   // length == Width × Height, flat row-major (row*Width + col)
}

// seqIdx returns the flat LastSeq index of cell (row, col): row*pf.Width + col.
func (pf *Playfield) seqIdx(row, col int) int

// CellLastSeq returns the stream sequence of the last message applied to cell
// (row, col) — the per-subject CAS expectation for that cell.
func (pf *Playfield) CellLastSeq(row, col int) uint64

// NewPlayfield creates an empty playfield with the default TotalRows height.
func NewPlayfield(width int) *Playfield

// NewPlayfieldWithHeight creates an empty playfield with a specific height. The
// engine sizes playfields at runtime via this constructor: cooperative boards are
// width = playerCount × StandardWidth and height = HeadroomRows + VisibleRows;
// competitive boards are width = StandardWidth and height =
// CompetitiveTotalRows(playerCount).
func NewPlayfieldWithHeight(width, height int) *Playfield

// Apply updates the cell at (row, col) from a decoded cell message and records
// its stream sequence. It is the single reconciliation point for both the
// consumer echo and the engine's publish write-through: the message is applied
// only if its sequence is STRICTLY HIGHER than the cell's current LastSeq —
// a same-or-lower sequence (e.g. the consumer echo of a write the engine
// already wrote through) is a harmless no-op. Out-of-bounds positions are
// ignored.
func (pf *Playfield) Apply(row, col int, cell Cell, seq uint64)

// ActivePieceForPlayer returns the active piece belonging to the given playerIdx
// (matching Cell.PlayerIdx). Used in cooperative mode where two players' active
// pieces coexist on the same shared playfield. Returns nil if no active piece
// with that playerIdx is present.
func (pf *Playfield) ActivePieceForPlayer(playerIdx int) *Piece

// SetActivePieceForPlayer / ClearActiveCellsForPlayer mutate the playfield in
// place (SetActivePieceForPlayer clears the player's old active cells first).
// They are used by ProjectShrink (to probe candidate piece placements) and by
// unit-test setup; the engine itself still mutates e.playfield only via Apply.
// See the "State mutation invariant" note at the start of Phase 3.
func (pf *Playfield) SetActivePieceForPlayer(p Piece, playerIdx int)
func (pf *Playfield) ClearActiveCellsForPlayer(playerIdx int)

// Projection helpers compute the would-be NEW ROWS of a state change WITHOUT
// mutating pf. They stay row-oriented (the natural unit of game logic); the
// engine then DIFFS their output against the live board (diffCells /
// changedCells) and publishes only the per-cell messages that changed. pf is
// mutated only via Apply() — by the consumer on echo and by the publish
// write-through on commit.
func (pf *Playfield) ProjectMove(affectedRows []int, newPiece *Piece, playerIdx int) map[int]Row
func (pf *Playfield) ProjectLock(affectedRows []int, playerIdx int) map[int]Row
func (pf *Playfield) ProjectHardDrop(affectedRows []int, dest Piece, playerIdx int, lockOnLand bool) map[int]Row
func (pf *Playfield) ProjectClearRows(completed []int, shiftAnchors bool) []Row
func (pf *Playfield) ProjectShrink(rowsToAdd, causerIdx, ownPlayerIdx int) ([]Row, bool)

// ProjectShrinkShared is the SHARED-BOARD shrink projection (teams mode):
// shifts the locked stack up by rowsToAdd, adds adversarial garbage rows
// (tagged causerIdx) at the bottom, and overlays EVERY player's active cells
// AT THEIR CURRENT POSITIONS — no piece is lifted. A piece overtaken by the
// risen stack simply remains where it is (it will lock there, "crushed"); a
// shared shrink can therefore never top a player out, so there is no topOut
// return. Compare ProjectShrink, which lifts the single own piece and can
// squeeze it off the top.
func (pf *Playfield) ProjectShrinkShared(rowsToAdd, causerIdx int) []Row

// AdversarialRowCount returns the number of contiguous bottom rows containing
// at least one adversarial cell. Garbage rows are permanent and bottom-anchored,
// so this count grows monotonically — it is the idempotency guard for the
// teams-mode shrink application ("≥1 adversarial cell" rather than "all cells"
// because crushed pieces lock INTO garbage rows and vacated active overlays
// leave hollow cells in them).
func (pf *Playfield) AdversarialRowCount() int
```

`ActivePieceForPlayer(playerIdx)` implementation: iterate rows `0..Height-1`, for each
row iterate cells. The first cell with `Active == true` and matching `PlayerIdx` gives
you the anchor, orientation, and type. Return immediately — don't scan further. The
piece type comes from PieceType if Active (note: the Cell.PieceType field is reused for
active pieces; it holds the type of the falling piece, not a locked piece). If
Occupied==true and Active==false, it's a locked cell, skip. If Active==true, it's a
falling piece cell.

#### `internal/game/rotation.go`

Implement SRS (Super Rotation System) wall kick tables. The standard SRS defines
5 kick positions to try for each rotation transition:

For J, L, S, T, Z pieces:
```
0→1: (0,0), (-1,0), (-1,+1), (0,-2), (-1,-2)
1→0: (0,0), (+1,0), (+1,-1), (0,+2), (+1,+2)
1→2: (0,0), (+1,0), (+1,-1), (0,+2), (+1,+2)
2→1: (0,0), (-1,0), (-1,+1), (0,-2), (-1,-2)
2→3: (0,0), (+1,0), (+1,+1), (0,-2), (+1,-2)
3→2: (0,0), (-1,0), (-1,-1), (0,+2), (-1,+2)
3→0: (0,0), (-1,0), (-1,-1), (0,+2), (-1,+2)
0→3: (0,0), (+1,0), (+1,+1), (0,-2), (+1,-2)
```

For I piece, separate kick table (see Tetris Guideline SRS).

```go
// Rotate applies a CW (clockwise==true) or CCW rotation to the piece,
// trying SRS wall kick offsets in order. Returns the rotated piece and
// true on success, or the original piece and false if all kicks fail.
func Rotate(p Piece, clockwise bool, pf *Playfield) (Piece, bool)
```

Kick offsets are (dRow, dCol). Try each: newPiece = piece with new orientation and
position adjusted by kick. Call CanPlace(newPiece, pf) — if true, return it.

#### `internal/game/collision.go`

```go
// CanPlace returns true if all cells of p are within bounds and not
// occupied by a locked cell in pf.
func CanPlace(p Piece, pf *Playfield) bool

// CanPlaceCoop returns true if all cells of p are within bounds and not
// occupied by a locked cell or by the OTHER player's active cells.
// Used in cooperative mode where both players' pieces coexist on the
// same playfield. ownPlayerIdx identifies the moving player so their
// own active cells are excluded from the collision check.
func CanPlaceCoop(p Piece, pf *Playfield, ownPlayerIdx int) bool

// HardDropDestination returns the lowest valid position for p by
// repeatedly attempting dRow=+1 until collision.
func HardDropDestination(p Piece, pf *Playfield) Piece

// HardDropDestinationCoop is like HardDropDestination but uses CanPlaceCoop so
// the other player's active cells are treated as obstacles.
func HardDropDestinationCoop(p Piece, pf *Playfield, ownPlayerIdx int) Piece
```

Bounds: row in [0, pf.Height), col in [0, pf.Width).
Collision: any cell at that (row, col) has Occupied==true and Active==false.

`HardDropDestination`: start from p's current position. Move down one row at a time,
checking `CanPlace` on the piece moved down one row. When the moved-down piece cannot
be placed, return the current p.

#### `internal/game/lineclear.go`

```go
// CompletedRows returns indices of rows where all cells have Occupied==true
// and Active==false (fully locked, no active piece cells).
func CompletedRows(pf *Playfield) []int

// Mode scoring is computed inline in the lock-in handler (consumer.go
// handleLockIn): cooperative adds playerCount×lines, competitive adds the line
// count. There is no separate guideline-score helper. See jetricks-gameplays.md.

// Level: increases every 10 lines cleared.
//   level = totalLinesCleared / 10  (capped at 19 for the speed curve)
func Level(totalLinesCleared int) int

// GravityInterval: standard speed curve by level.
//   Level 0: 800ms, level 1: 717ms, ... level 19: 33ms
//   Use the Tetris Guideline gravity table (frames at 60fps × 16.67ms).
func GravityInterval(level int) time.Duration
```

Gravity intervals (approximate, Tetris Guideline):
```
0:800ms 1:717ms 2:633ms 3:550ms 4:467ms 5:383ms 6:300ms 7:217ms
8:133ms 9:100ms 10:83ms 11:83ms 12:83ms 13:67ms 14:67ms 15:67ms
16:50ms 17:50ms 18:50ms 19+:33ms
```

**Tests for `internal/game`:**
- `Cells()` for every piece type and orientation returns the correct cell coordinates.
- `CanPlace` rejects pieces out of bounds or overlapping locked cells.
- `CanPlaceCoop` rejects pieces overlapping locked cells or the other player's active cells, but allows overlapping own active cells.
- `HardDropDestination` lands on the correct row with a tower of occupied cells.
- `Rotate` applies SRS kicks correctly — at minimum test all J/L/S/T/Z transitions plus I.
- `CompletedRows` correctly identifies full rows. (Mode scoring is not a `game` function; it is computed inline in `handleLockIn` — competitive adds the line count, cooperative adds `playerCount` per line. See `jetricks-gameplays.md`.)
- `GravityInterval(0)==800ms`, `GravityInterval(19)==33ms`.
- `Cell.Marshal()` and `UnmarshalCell()` round-trip correctly for occupied and active cells, and the empty cell encodes as exactly `{}` (the vacate payload) and decodes back to the zero `Cell`.
- `ActivePieceForPlayer()` returns only the piece matching the given playerIdx on a shared playfield with two active pieces (and nil when no active piece for that player is present).
- `SetActivePieceForPlayer()` clears only the matching player's active cells before placing new ones, leaving the other player's active cells intact.
- `Playfield.Apply()` updates the cell content and `CellLastSeq(row, col)`; a same-sequence re-apply is a no-op and a strictly higher sequence overwrites (the per-cell seq rules the write-through/echo convergence relies on).
- `ProjectShrinkShared()` (`teamshrink_test.go`) holds every active piece at its current position while the locked stack shifts up (no lift), tags the new bottom rows with the causer's index, and leaves a piece overtaken by the stack in place (to be crushed/locked there).
- `AdversarialRowCount()` counts only the contiguous bottom garbage rows, including rows partially filled by crushed pieces or hollowed by vacated overlays.

---

### 1.4 `internal/rng`

**File:** `internal/rng/rng.go`

```go
import "math/rand/v2"

type Sequence struct {
    seed uint64
}

func New(seed uint64) *Sequence {
    return &Sequence{seed: seed}
}

// Piece returns the piece type at the given index using a seekable PCG.
// This must be deterministic: New(seed).Piece(N) == New(seed).Piece(N) always.
// Use a 7-bag randomiser: index/7 gives the bag number, index%7 gives position in bag.
// Shuffle each bag using the PCG seeded with (seed ^ bagNumber).
func (s *Sequence) Piece(index uint64) game.PieceType
```

**7-bag implementation:**
```go
func (s *Sequence) Piece(index uint64) game.PieceType {
    bag := index / 7
    pos := index % 7
    // Seed the bag's RNG with a deterministic value derived from seed and bag number
    src := rand.NewPCG(s.seed, bag)
    r := rand.New(src)
    pieces := [7]game.PieceType{game.PieceI, game.PieceO, game.PieceT,
        game.PieceS, game.PieceZ, game.PieceJ, game.PieceL}
    r.Shuffle(7, func(i, j int) { pieces[i], pieces[j] = pieces[j], pieces[i] })
    return pieces[pos]
}
```

This is seekable because any index can be computed independently — no sequential RNG
state. Two clients with the same seed always produce the same sequence.

**Tests:**
- Two `New(42)` sequences produce identical output for indices 0–49.
- `Piece(N)` == sequential calls pattern for N in 0..20.
- Each bag of 7 contains all 7 piece types exactly once.
- Different seeds produce different sequences.

---

## Phase 2 — NATS Plumbing

Requires a running NATS server for integration tests. Add a `testutil` package.

### 2.0 `internal/testutil` (test-only helper)

**File:** `internal/testutil/nats.go` (build tag `//go:build !production`)

```go
// StartServer starts an embedded NATS server with JetStream enabled.
// Returns the server and a context file path pointing at it.
// The server is shut down when t.Cleanup fires.
func StartServer(t *testing.T) (serverURL string, contextFile string)
```

Use `github.com/nats-io/nats-server/v2/server` to start an in-process NATS server:
```go
opts := &server.Options{
    Port:      -1,  // random port
    JetStream: true,
    StoreDir:  t.TempDir(),
}
s, err := server.NewServer(opts)
s.Start()
t.Cleanup(s.Shutdown)
```

Write a minimal NATS context JSON file to `t.TempDir()` pointing at the server URL.
The context file format:
```json
{"url": "nats://127.0.0.1:<port>"}
```

Return both the URL and the path to the context file. Tests pass the context file path
to `natscontext.Connect`.

---

### 2.1 `internal/nats`

Seven files. Implement in order: `connection.go` → `streams.go` → `kv.go` →
`consumer.go` → `publish.go` → `fetch.go` → `subjects.go`.

#### `connection.go`

```go
func Connect(contextName string, opts ...nats.Option) (*nats.Conn, jetstream.JetStream, natscontext.Settings, error) {
    nc, settings, err := natscontext.Connect(contextName, opts...)
    if err != nil { return nil, nil, settings, err }
    var js jetstream.JetStream
    if settings.JSDomain != "" {
        js, err = jetstream.NewWithDomain(nc, settings.JSDomain)
    } else {
        js, err = jetstream.New(nc)
    }
    return nc, js, settings, err
}
```

#### `streams.go`

`EnsureGameStream` creates a stream with these **required** config fields:
```go
jetstream.StreamConfig{
    Name:               config.GameStream(gameID),
    Subjects:           []string{config.GameSubjectFilter(gameID)},
    AllowAtomicPublish: true,  // required for jetstreamext batch publish
    AllowDirect:        true,  // direct get for fast last-message-per-subject fetches
    MaxMsgsPerSubject:  1,     // only the latest message per subject is needed
    Storage:            jetstream.MemoryStorage,
    Retention:          jetstream.LimitsPolicy,
}
```

Use `js.CreateOrUpdateStream(ctx, cfg)` — idempotent if stream already exists with
compatible config.

`EnsureLobbyChatStream` (carries the lobby chat AND every game's chat,
distinguished by subject):
```go
jetstream.StreamConfig{
    Name:     config.LobbyChatStream,
    Subjects: []string{config.LobbyChatSubject, config.GameChatSubjectFilter},
    MaxAge:   config.LobbyChatMaxAge,
    Storage:  jetstream.FileStorage,
}
```

`EnsureArchiveStream`:
```go
jetstream.StreamConfig{
    Name:     config.ArchiveStream,
    Subjects: []string{config.ArchiveSubject},
    Storage:  jetstream.FileStorage,
}
```

`SealGameStream`: use `js.UpdateStream(ctx, cfg)` with the existing config plus
`Sealed: true`. Fetch the current config first with `js.Stream(ctx, name)`.

`DeleteGameStream`: `js.DeleteStream(ctx, config.GameStream(gameID))`.

`ListGameStreams`: use `js.StreamNames(ctx)` with a filter prefix `JETRICKS_GAME_`.
The API accepts a `jetstream.StreamNamesFilter` or iterate the names channel/slice
and filter by prefix.

#### `kv.go`

```go
func EnsureLobbyKV(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
    // No bucket-level TTL — game listings must persist indefinitely. Player
    // presence entries are kept fresh by heartbeats; stale entries are detected
    // by comparing LastSeen timestamps in the presence logic, not by KV TTL.
    return js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
        Bucket:  config.LobbyKVBucket,
        Storage: jetstream.FileStorage,
    })
}
```

Note: the bucket has no TTL or delete-marker TTL. Presence staleness is handled
in application logic (`LastSeen` timestamps + `pruneStalePresence`) rather than by
the KV layer, so a heartbeat refresh simply rewrites the entry.

#### `consumer.go`

```go
type OrderedConsumerConfig struct {
    Stream         string
    FilterSubject  string
    StartSeq       uint64   // 0 = DeliverAllPolicy (from beginning)
    ReplayOriginal bool
}

func NewOrderedConsumer(
    ctx context.Context,
    js jetstream.JetStream,
    cfg OrderedConsumerConfig,
) (<-chan jetstream.Msg, context.CancelFunc, error)
```

Implementation:
```go
consumerCfg := jetstream.OrderedConsumerConfig{
    FilterSubjects: []string{cfg.FilterSubject},
}
if cfg.StartSeq > 0 {
    consumerCfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
    consumerCfg.OptStartSeq = cfg.StartSeq
} else {
    consumerCfg.DeliverPolicy = jetstream.DeliverAllPolicy
}
if cfg.ReplayOriginal {
    consumerCfg.ReplayPolicy = jetstream.ReplayOriginalPolicy
}
cons, err := js.OrderedConsumer(ctx, cfg.Stream, consumerCfg)
// Use cons.Messages() to get a MessageIterator, then pump into channel in goroutine.
```

The channel should be buffered (size 64). The goroutine pumps messages from the
iterator into the channel. The returned `context.CancelFunc` cancels the consumer
context, which terminates the goroutine.

#### `publish.go`

```go
// The caller supplies the fully-built cell Subject, so this package is
// subject-agnostic — it knows nothing about game modes or players. The engine
// builds Subject with the mode-appropriate scheme (Coop*/Competitive*CellSubject)
// and orders the slice for the desired consumer apply order.
type CellUpdate struct {
    Subject       string
    Payload       []byte
    ExpectLastSeq uint64
}

var ErrCASFailure = errors.New("CAS sequence expectation not met")

// PublishMoveAtomically publishes a set of cell updates as a SINGLE atomic
// batch with PER-SUBJECT CAS expectations. Either every cell commits or none
// does; consumers never observe a torn state. On success it returns the commit
// ack's stream sequence (the last message's sequence) so the caller can write
// through the committed cells into its playfield without waiting for the echo.
func PublishMoveAtomically(ctx context.Context, js jetstream.JetStream, updates []CellUpdate) (uint64, error)

// PublishCellsAtomicallyNoCAS publishes a set of cell updates as a SINGLE
// atomic batch WITHOUT CAS expectations. Used for authoritative state changes
// (lock, hard-drop landing, line-clear, shrink) where the publisher's view is
// the new ground truth. Also returns the commit ack's stream sequence.
func PublishCellsAtomicallyNoCAS(ctx context.Context, js jetstream.JetStream, updates []CellUpdate) (uint64, error)
```

Use `jetstreamext.NewBatchPublisher(js)` and add each cell update with a per-subject
expected sequence — implemented by `jetstreamext.WithBatchExpectLastSequencePerSubject(seq)`,
which sets the `Nats-Expected-Last-Subject-Sequence` header. **Use the per-subject
form, not `WithBatchExpectLastSequence` (stream-level CAS).** Each cell has its
own subject; per-subject CAS lets concurrent writes to other cells succeed and
only rejects when our specific cell has been overwritten since we last saw it —
in coop, two pieces moving in the same ROW no longer conflict; only writes to
the SAME cell do.

**Batch size limit:** the server caps an atomic batch at its `max_batch_size`
(default **1000 messages**). Both functions assume the caller stays within the
limit; the engine chunks larger writes (only reachable on degenerate
many-player boards — see `publishProjectedCellsNoCAS`).

```go
batch, err := jetstreamext.NewBatchPublisher(js)
for i, u := range updates {
    msg := &nats.Msg{Subject: u.Subject, Data: u.Payload, Header: nats.Header{}}
    if i < len(updates)-1 {
        err = batch.AddMsg(msg, jetstreamext.WithBatchExpectLastSequencePerSubject(u.ExpectLastSeq))
    } else {
        _, err = batch.CommitMsg(ctx, msg, jetstreamext.WithBatchExpectLastSequencePerSubject(u.ExpectLastSeq))
    }
}
```

The engine sources `ExpectLastSeq` for each cell from
`e.playfield.CellLastSeq(row, col)`, which is updated by the cell consumer's
`pf.Apply(row, col, cell, seq)` call when the ordered consumer delivers each
cell's last message (and by the publish write-through on commit). Because every
cell is its own subject, the CAS expectation is per-cell and won't be
invalidated by other concurrent cell writes.

On CAS failure, the server returns a JetStream API error with code 10071
("wrong last msg seq for subject"). Detect this (via `errors.As` on
`*jetstream.APIError`, with a string-match fallback) and wrap it as
`ErrCASFailure`.

`PublishMeta`:
```go
func PublishMeta(ctx context.Context, js jetstream.JetStream, gameID string, payload []byte, expectLastSeq uint64) error {
    _, err := js.Publish(ctx, config.MetaSubject(gameID), payload,
        jetstream.WithExpectLastSequencePerSubject(expectLastSeq))
    if err != nil {
        if isCASError(err) {
            return ErrCASFailure
        }
        return err
    }
    return nil
}
```

#### `fetch.go`

```go
// PlayfieldCellMsg holds the fetched state for a single cell.
type PlayfieldCellMsg struct {
    Row     int
    Col     int
    Payload []byte
    Seq     uint64
}

// fetchChunkSize bounds the number of subjects per multi-last direct get. The
// server caps a single request at 1024 responses (413 Too Many Results, no
// pagination), so large boards are fetched in chunks bounded to a common
// stream sequence for a consistent snapshot.
const fetchChunkSize = 512

// The caller builds subjects with the mode-appropriate scheme (coop or
// competitive), so this stays subject-agnostic. Results are keyed by the
// (row, col) parsed from each subject, so it works for either subject shape.
// Cells that have never been written have no last message and are simply
// ABSENT from the result (empty cell).
func FetchPlayfieldState(ctx context.Context, js jetstream.JetStream, gameID string, subjects []string) ([]PlayfieldCellMsg, error) {
    var opts []jetstreamext.GetLastForOpt
    if len(subjects) > fetchChunkSize {
        // Bound every chunk to the stream's current last sequence so the
        // combined snapshot is consistent at one point in the stream; anything
        // newer is replayed by the caller's consumer (startSeq = maxSeq+1).
        stream, err := js.Stream(ctx, config.GameStream(gameID))
        if err != nil { return nil, err }
        opts = append(opts, jetstreamext.GetLastMsgsUpToSeq(stream.CachedInfo().State.LastSeq))
    }

    var result []PlayfieldCellMsg
    for start := 0; start < len(subjects); start += fetchChunkSize {
        end := min(start+fetchChunkSize, len(subjects))
        msgs, err := jetstreamext.GetLastMsgsFor(ctx, js, config.GameStream(gameID), subjects[start:end], opts...)
        if err != nil {
            if errors.Is(err, jetstreamext.ErrNoMessages) { continue } // board empty so far
            return nil, err
        }
        for msg, err := range msgs {
            if err != nil {
                if errors.Is(err, jetstreamext.ErrNoMessages) { continue }
                return nil, err
            }
            row, col := ParseCellFromSubject(msg.Subject)
            if row < 0 { continue }
            result = append(result, PlayfieldCellMsg{
                Row: row, Col: col, Payload: msg.Data, Seq: msg.Sequence,
            })
        }
    }
    return result, nil
}

func FetchGameMeta(ctx context.Context, js jetstream.JetStream, gameID string) (config.GameMeta, uint64, error) {
    stream, err := js.Stream(ctx, config.GameStream(gameID))
    if err != nil { return config.GameMeta{}, 0, err }
    msg, err := stream.GetLastMsgForSubject(ctx, config.MetaSubject(gameID))
    if err != nil { return config.GameMeta{}, 0, err }
    var meta config.GameMeta
    if err := json.Unmarshal(msg.Data, &meta); err != nil { return config.GameMeta{}, 0, err }
    return meta, msg.Sequence, nil
}
```

Boards stay within one round trip for the common sizes (a 28×10 competitive
board is 280 subjects; a 2-player coop board is 28×20 = 560, just over one
chunk); only the chunked path needs the `GetLastMsgsUpToSeq` bound, since a
single `GetLastMsgsFor` call is already a point-in-time read. The 512 chunk
size keeps each request comfortably under the server's 1024-response cap.

`ParseCellFromSubject(subject) (row, col int)`: the subject ends in
`.cell.<row>.<col>` — split on "." and parse the LAST TWO tokens. Returns
`(-1, -1)` if the subject doesn't end in two numeric tokens.

#### `subjects.go`

Re-export the config builders for convenience:
```go
var (
    GameStream                   = config.GameStream
    GameSubjectFilter            = config.GameSubjectFilter
    CoopCellSubject              = config.CoopCellSubject
    CoopCellSubjectFilter        = config.CoopCellSubjectFilter
    CompetitiveCellSubject       = config.CompetitiveCellSubject
    CompetitiveCellSubjectFilter = config.CompetitiveCellSubjectFilter
    TeamCellSubject              = config.TeamCellSubject
    TeamCellSubjectFilter        = config.TeamCellSubjectFilter
    // ... all others
)
```

**Integration tests** (`internal/nats/nats_test.go`):
- `EnsureGameStream` creates a stream that accepts atomic batch publishes.
- `EnsureLobbyChatStream` creates a stream with correct MaxAge.
- `EnsureLobbyKV` creates a bucket; a `Put` value is readable back via `Get`.
- `PublishMeta` succeeds on first publish, returns `ErrCASFailure` on a stale
  expectation.
- `SealGameStream` prevents further publishes.
- `TestPublishAndFetchCells`: published cells come back from
  `FetchPlayfieldState` with their (row, col) and non-zero sequences, and a
  never-written subject is absent from the result.
- `TestFetchPlayfieldStateChunked`: a 600-subject fetch (20×30 board, above
  `fetchChunkSize`) is split into bounded chunks and still returns exactly the
  written cells.
- `TestParseCellFromSubject`: round-trips (row, col) through both subject
  schemes and rejects malformed subjects.
- `FetchGameMeta` returns the latest meta.
- `NewOrderedConsumer` delivers messages in sequence order.

---

## Phase 3 — Engine

The hardest package. Implement in file order.

### State mutation invariant (read this first)

The in-memory `*game.Playfield` is mutated only via `pf.Apply(row, col, cell,
seq)`, from exactly two places: the cell consumer (`runConsumer`) on an
ordered-consumer echo, and the **publish write-through** (`applyPublishedCells`)
the instant a batch commits. Every other code path — local moves, hard drops,
locks, line clears, shrinks, piece spawns — must compute its result as a set of
*projected* rows (using `ProjectMove`, `ProjectLock`, `ProjectHardDrop`,
`ProjectClearRows`, `ProjectShrink` in `internal/game/playfield.go`), diff them
against the live board into per-cell payloads (`diffCells` / `changedCells`),
and publish those cells; it does not mutate `e.playfield` directly.

The write-through is what lets a successful publish advance `e.playfield`
(content **and** the per-cell `LastSeq`) without waiting for its own echo: the
batch commit ack returns the last message's stream sequence, and the engine
infers each cell's sequence from the consecutive batch ordering (`message i of
N → commitSeq − (N−1−i)`). `pf.Apply` only accepts a **strictly higher**
sequence per cell, so the later echo of our own write (same sequence) is a
harmless no-op, while a higher sequence — the other player's write in coop, or
a NoCAS write we didn't originate — still updates memory.

The UI renders from `e.playfield` exclusively. There is no separate "pending"
buffer; the write-through and the echo converge on the same content.

**Concurrency model.** `e.mu` guards the structured state — `e.playfield`
(`Rows`/`LastSeq`, via `pf.Apply`), `e.opponentPlayfields`, and
`e.eliminatedPlayers`. Every read of `LastSeq` that feeds a CAS expectation
(`buildBatchUpdates`) and every write-through takes `e.mu` (or relies on the
caller's lock via the `locked` flag the publish helpers thread through). The
scalar fields that several goroutines touch with no single covering lock —
`mode`, `score`, `level`, `totalLines`, `pieceIdx` — are `sync/atomic` values
rather than `e.mu`-guarded, because `transitionToSpectator` (which sets `mode`)
runs both under and without the lock and locking it would deadlock. The
accessors that hand playfield state to the UI/tests (`Playfield`,
`OpponentPlayfields`, `Snapshot`, `OpponentSnapshots`) return **deep copies**
(`Playfield.Clone` / `CloneRows`) taken under `e.mu`, so a caller can read the
result without the lock while the consumer and write-through keep mutating the
live playfield. Player input is serialized and buffered on the `e.moves` channel:
`runInput` processes one at a time and each publish blocks on its commit ack
before the next is dequeued, so a player never has two input batches in flight.
The engine mirrors that buffer in a `bufferedMu`-guarded FIFO (`bufferedMoves`):
`dispatch` appends on a successful enqueue, `runInput` pops (`popBufferedMove`)
the moment it dequeues a move (its batch publish is starting), and
`Engine.BufferedMoves()` returns a copy for the UI, which draws the queued moves
as a small muted line under the player's board (`bufferedMovesLine` in
`internal/nativeui/game.go`, e.g. `← ← CW HD`) — populated only when a
high RTT makes inputs queue behind the in-flight publish. `UpdateBufferedMoves`
on the `Updates` channel triggers the redraw.

Validation (e.g. `CanPlace`) reads from `e.playfield`, which the write-through
keeps current the moment each publish commits. Two rapid inputs (or a gravity tick
and a move) therefore validate against up-to-date state and the second no longer
loses per-subject CAS to the engine's own earlier write. A CAS rejection now means
a *genuine* conflict — in coop, the other player wrote the same shared **cell** —
and is surfaced as a `CASFlash` event for visual feedback.

### Atomic batch publish + per-subject CAS

Every multi-cell publication from the engine goes through a SINGLE atomic batch
— either `natspkg.PublishMoveAtomically` (CAS) or
`natspkg.PublishCellsAtomicallyNoCAS` (no CAS) — never cell-by-cell publishes.
Atomicity matters because a player's move typically touches ~4–8 cells (the
new footprint plus the vacated old positions); if those cells arrived at the
consumer one at a time and the batch could tear, every other player would
briefly see a half-erased / half-placed piece.

The CAS expectation is **per subject** (`Nats-Expected-Last-Subject-Sequence`,
applied via `jetstreamext.WithBatchExpectLastSequencePerSubject(seq)`), not
per stream. The expected sequence for each cell comes from
`e.playfield.CellLastSeq(row, col)`, kept current via `pf.Apply(row, col, cell,
seq)` — both by the cell consumer on echo and by the publish write-through
(which advances it from the commit ack the instant a batch commits, so a later
write doesn't carry a stale expectation). Per-subject CAS means a write to a
cell is only rejected if that cell specifically has been written by someone
else since we last saw it — concurrent writes to other cells (e.g. another
player's piece moving, even through the same row) are not in conflict. Cell
granularity makes coop contention far rarer than the old per-row scheme: only
writes to the **same cell** collide.

Mapping from engine action to publish path (every path takes a `diffCells`/
`changedCells` map; batch order always comes from `orderedCellKeys`):

| Action | Path | CAS? |
| ----- | ---- | ---- |
| Move left/right/rotate/down (player input, all modes) | `publishProjectedCells` → `PublishMoveAtomically` | yes, per-subject (drop on fail) |
| Gravity tick (competitive) | `publishProjectedCells` | yes, per-subject (drop on fail) |
| Gravity tick (coop/teams) | `publishProjectedCellsWithMergeRetry` | yes, per-subject (merge-retry) |
| Spawn (competitive) | `publishProjectedCells` | yes, per-subject (drop on fail) |
| Spawn (coop/teams) | `publishProjectedCellsWithMergeRetry` | yes, per-subject (merge-retry) |
| Lock-on-blocked-down (coop/teams) | `publishProjectedCellsWithMergeRetry` | yes, per-subject (merge-retry) |
| Lock-on-blocked-down (competitive) | `publishProjectedCellsNoCAS` | no |
| Hard drop (coop/teams) | `publishProjectedCellsWithMergeRetry` | yes, per-subject (merge-retry) |
| Hard drop (competitive) | `publishProjectedCellsNoCAS` | no |
| Line clear (coop/teams) | `publishProjectedCellsWithMergeRetry`, **changed cells only** | yes, per-subject (merge-retry) |
| Line clear (competitive) | `publishProjectedCellsNoCAS`, **changed cells only** | no |
| Opponent shrink (apply locally, competitive) | `publishProjectedCellsNoCAS`, **changed cells only** | no |
| Team shrink (apply locally, teams — every ALIVE member of the target team races) | `applyTeamShrink` → `PublishMoveAtomically`, **changed cells only** | yes, per-subject (**recompute on fail** behind the `expectedGarbage − AdversarialRowCount()` deficit guard — never merge-retry, which would double-shift; see Phase 8) |
| Elimination vacate (teams — topped-out player erases own active piece) | `publishProjectedCellsWithMergeRetry` | yes, per-subject (merge-retry) |

Every path publishes only the cells that **actually changed**: `diffCells`
diffs a `Project*` row map against the live board (moves, spawns, locks, hard
drops — ~4–8 messages per move), and `changedCells` diffs a full projected row
slice over a row range (line clears, shrinks — a low stack changes only a
handful of cells, not the whole visible range). On the shared coop board this
keeps the clear's per-subject CAS merge-retry from exhausting against the other
player's moving piece — the contention that otherwise dropped the clear
(uncleared line) and the follow-up spawn (stuck player).

**Why coop authoritative writes use CAS+merge-retry, not NoCAS.** In coop both
players write the **same** shared cell subjects. A lock or line clear is
projected from the writer's **local snapshot** — if that snapshot lags, a plain
NoCAS write can overwrite a cell the other player's mid-flight piece has since
moved into (or vacate one it now occupies), corrupting their piece (ghosts /
mixed-type pieces). Per-subject CAS prevents this: a stale batch is rejected,
and the merge-retry refetches the latest state of the affected cells and
**keeps our content everywhere except cells currently holding the other
player's active piece, which it skips entirely** — it neither overwrites nor
vacates their piece — then republishes. Competitive mode owns per-player
subjects (no shared writer), so NoCAS is safe there. The teams-mode shared
team board has the same multi-writer shape as coop (engine code gates on
`sharedBoard()`, true for coop **and** teams), so every argument in this
paragraph applies to it unchanged — except the team-shrink application, which
has its own CAS discipline (recompute, not merge — see Phase 8).

**Batch ordering (`orderedCellKeys`):** the ordered consumer applies a batch
one cell at a time and detects lock-in (firing the completed-line check) the
instant the player's last `Active` cell disappears — so the order *within* a
batch matters even though the batch commits atomically. A single rule covers
every write path: order the batch by **category of each cell's NEW content** —
**active cells first, locked/occupied cells second, empty (vacate) cells
last** — with an ascending (row, col) tie-break for determinism. Two
invariants follow:
(1) a **relocating piece** (gravity, lateral move, rotation, spawn, hard drop
that stays active) never transiently has zero active cells: all its new active
cells are applied before its old positions are vacated, so no **spurious
lock-in** fires — this covers the single-row horizontal I, whose old and new
footprints don't overlap; (2) a **lock** (in-place or hard-drop landing) fires
lock-in exactly once, at the LAST message that removes the player's final
active cell (the final vacate for a hard drop; the last active→occupied
conversion for an in-place lock) — by which point all locked/landing cells are
already applied, so a line completed by a hard drop is detected at *that* lock,
not one piece later. The same argument covers a coop line clear (the other
player's shifted active piece is applied before its old cells are vacated, so
their engine never sees it vanish) and a competitive shrink (the re-stamped
piece first, the risen stack second, vacates last). There are no
`bottomFirst`/`applyBottomFirst` parameters anywhere — the category rule
subsumes them.

CAS failure handling for **player moves** — same in both modes: **drop the
move, no retry**. The engine signals the local player with an `UpdateCASFlash`
directly on its own `Updates` channel. There is **no NATS publish** for
CAS-failure feedback — other players don't need to know that one player's
input was rejected. The player whose move failed is responsible for retrying
the input themselves.

This is especially important in cooperative mode where the shared playfield
has two writers and CAS rejections are an expected, regular occurrence: a
silent server-side retry would mask the conflict from the player and make
the timing of their own moves feel non-deterministic. Surfacing the failure
loudly (rainbow flash, see below) gives the player full agency over how to
recover.

The UI renders `UpdateCASFlash` as a **rainbow flash on the outline of the
player's own piece** — cells of `FlashCells` get a border that cycles
through the 7 spectrum colors over ~600ms. This is unmistakable visual
feedback that the move was rejected.

In the native UI, `bridge.go` records each `FlashCells` coordinate with its
arrival timestamp in the app's `flash` map; `board.go` overlays a border on
any cell whose entry is younger than `flashDur` (600ms), stepping through the
7-stop spectrum palette by elapsed time, and the frame loop keeps invalidating
while a flash is active. Expired entries are pruned each frame.

CAS failure handling for **engine-driven (internal) writes** — the two such
writes that use CAS are **piece spawn** and **gravity ticks** (the gravity arm
of `runInput` calls `attemptMove(MoveDown, internal=true)`). In cooperative mode both writes
share the same cell subjects with the other player and may race, and both
**must succeed** — the spawn loser must not be left pieceless, and a gravity
tick that silently drops would make the piece appear stuck for one tick
interval, then snap down. So both use `publishProjectedCellsWithMergeRetry` in
coop mode: on CAS failure, refetch the latest state of every affected cell in
ONE batched round trip (`refetchAndMerge` via `FetchPlayfieldState`), keep our
cells except where the latest stream content is the other player's active
piece, and retry the batch with refreshed per-subject CAS expectations (up to
16 attempts, with a short per-player-offset backoff between tries that breaks
lockstep with the other player).

In competitive mode each player owns their cell subjects, so neither spawn nor
gravity contends with another player; they go through the regular
`publishProjectedCells`. Crucially, **gravity and player input share one goroutine
(`runInput`)**, so a player's own gravity tick and move are serialized and cannot
lose the per-subject CAS race against each other — this is the fix for the
spurious rainbow flashes that were otherwise visible during competitive play.

**The rainbow flash fires for every CAS-dropped step, not only player input.**
Whenever a write is ultimately rejected by CAS and the step is dropped, the
local player is flashed — player moves, gravity ticks, and spawns alike. The
flash cells are precomputed by the caller while holding `e.mu` (the dropped
step's cells) and passed into the publish helper, which calls the lock-free
`emitCASFlash` on failure (it must not take `e.mu`, because `spawnPiece` can be
invoked while the consumer already holds the lock). For the merge-retry path
the flash fires only when **all** retries are exhausted (a transient CAS
failure that merges and commits is not a dropped step, so it does not flash —
otherwise coop gravity, which contends every tick, would strobe constantly).
Spectators never flash (`e.mode != ModePlayer`).

The mode→path mapping for CAS publishes is:

| Path | Mode | On CAS failure |
| ---- | ---- | -------------- |
| Player moves (`runInput` moves arm → `attemptMove(internal=false)`) | all | drop + local rainbow flash, no retry |
| Gravity (`runInput` gravity arm → `attemptMove(MoveDown, internal=true)`) | competitive | drop + flash (serialized with input on `runInput`, so it cannot self-race) |
| Gravity (`runInput` gravity arm → `attemptMove(MoveDown, internal=true)`) | cooperative/teams | merge-retry; flash only if all retries exhausted |
| Spawn (`spawnPiece`) | competitive | drop + flash (cannot race in practice) |
| Spawn (`spawnPiece`) | cooperative/teams | merge-retry; flash only if all retries exhausted |

### 3.1 `internal/engine`

#### `events.go` — implement first (pure types, no logic)

```go
type UpdateKind int
const (
    UpdatePlayfield UpdateKind = iota
    UpdatePieceLocked
    UpdateLineClear
    UpdateGameOver
    UpdateOpponentField
    UpdateOpponentShrink
    UpdateScore
    UpdateLevel
    UpdateGameStatus
    UpdateCountdown
    UpdatePlayerEliminated // competitive: a player was eliminated
    UpdateCASFlash         // a CAS failure flash should be rendered
    UpdateRTT             // a new publish→echo round-trip measurement
    UpdateBufferedMoves    // the buffered-input queue changed
    UpdateTeamStats        // teams: a team's score or level changed (totals in TeamScores/TeamLevels)
)

type EngineUpdate struct {
    Kind               UpdateKind
    ChangedRows        []int
    Score              int
    Level              int
    GameStatus         string
    Countdown          int      // seconds remaining (0 = GO!)
    Won                bool     // competitive: true if this player won
    EliminatedPlayerID string   // competitive: which player was eliminated
    OpponentID         string   // for UpdateOpponentField: which opponent's board changed
    FlashCells         [][2]int // cells to flash (UpdateCASFlash)
    FlashPlayerIdx     int      // player index for flash color
    Team               int      // teams: team of the eliminated player (UpdatePlayerEliminated)
    RTT               time.Duration // latest publish→echo round trip (UpdateRTT)
    TeamScores        [config.TeamCount]int // teams: both teams' scores (UpdateTeamStats)
    TeamLevels        [config.TeamCount]int // teams: both teams' levels (UpdateTeamStats)
}

type EventKind string
const (
    EventLineClear EventKind = "line_clear"
    EventShrink    EventKind = "shrink"
    EventGameOver  EventKind = "game_over"
)

type GameEvent struct {
    Kind         EventKind `json:"kind"`
    PlayerID     string    `json:"player_id"`
    LinesCleared int       `json:"lines_cleared,omitempty"`
    TargetPlayer string    `json:"target_player,omitempty"`
    RowsRemoved  int       `json:"rows_removed,omitempty"`
    ClearedRows  []int     `json:"cleared_rows,omitempty"`
    Score        int       `json:"score,omitempty"`          // for EventGameOver: player's final score
    Level        int       `json:"level,omitempty"`          // for EventGameOver: level achieved at game end
    PieceCount   uint64    `json:"piece_count,omitempty"`    // for EventGameOver: total pieces placed
    PlayerIdx    int       `json:"player_idx,omitempty"`     // causer's index for EventShrink
    Team         int       `json:"team"`                     // teams: sender's team (0 = A, 1 = B)
    TargetTeam   int       `json:"target_team"`              // teams: receiving team for EventShrink
}

type MoveType int
const (
    MoveLeft MoveType = iota
    MoveRight
    MoveDown
    RotateCW
    RotateCCW
    MoveHardDrop
)
```

#### `engine.go`

```go
type Mode int
const (
    ModePlayer    Mode = iota
    ModeSpectator
    ModeGameOver
)

type Engine struct {
    gameID      string
    playerID    string
    gameMode    config.GameMode
    mode        atomic.Int32 // current Mode; atomic — see the concurrency note below
    initialMode Mode         // original mode at creation (ModePlayer or ModeSpectator)
    playerIdx   int  // 0 for creator, 1 for joiner; used on shared boards for Cell.PlayerIdx (GLOBAL roster index)
    playerCount int  // number of players in the game
    teamIdx     int  // teams mode: which team this player is on (0 = A, 1 = B)
    teamSlot    int  // teams mode: section index within the team board (spawn column offset)
    teamSize    int  // teams mode: players per team (from meta at Start)

    mu        sync.Mutex
    playfield *game.Playfield // cooperative: single shared wide playfield (playerCount × StandardWidth)

    opponentPlayfields map[string]*game.Playfield // keyed by opponent playerID (competitive)
    opponentPlayerID   string                     // single known opponent (2-player join); others discovered via roster

    seq      *rng.Sequence
    pieceIdx atomic.Uint64
    metaSeq  uint64 // last known sequence of meta message

    // mode/score/level/totalLines/pieceIdx are sync/atomic: they are read and
    // written across the consumer, runInput, events and UI goroutines with no
    // single covering lock (transitionToSpectator sets mode both under and without
    // e.mu, so locking it would deadlock). e.mu still guards the structured state.
    score             atomic.Int64
    totalLines        atomic.Int64
    level             atomic.Int64
    hadActivePiece    bool            // only the own-rows consumer goroutine touches it
    eliminatedPlayers map[string]bool // players who have topped out (competitive); guarded by e.mu
    eliminatedTeam    map[string]int  // teams: eliminated player → team; guarded by e.mu
    teamOutcomeDone   bool            // teams: win/loss/draw already decided; guarded by e.mu
    expectedGarbage   int             // teams: cumulative adversarial rows owed to this team's board; guarded by e.mu
    visibleRowStart   int             // first visible row index; cooperative: 4, competitive: CompetitiveVisibleRowStart(playerCount), teams: TeamVisibleRowStart(teamSize)

    Updates        chan EngineUpdate
    OnGameFinished func() // called after game transitions to finished (for archiving)

    js          jetstream.JetStream
    ctx         context.Context
    cancelFn    context.CancelFunc
    moves       chan MoveType // internal move dispatch channel (buf 8)
    cellUpdated chan struct{} // CAS notification channel (buf 1)
}

// New initialises fields and channels; it does NOT take a context — Start()
// creates the context. opponentPlayerID is a single known opponent (for a
// 2-player join); additional competitive opponents are discovered dynamically
// via the roster consumer. playerIdx/teamIdx/teamSlot come from
// lobby.JoinGame's JoinResult; teamIdx and teamSlot are only meaningful in
// teams mode (spectators and other modes pass 0, 0).
// New returns *Engine only (no error); Start() does the fetching and can fail.
func New(
    js jetstream.JetStream,
    gameID, playerID, opponentPlayerID string,
    gameMode config.GameMode,
    mode Mode,
    playerIdx, teamIdx, teamSlot int,
) *Engine
```

`New()` does NOT call `FetchGameMeta` or `FetchPlayfieldState` — that happens in
`Start()`. `New()` just initialises fields and creates channels.

```go
func (e *Engine) Start() error  // see consumer.go for startup sequence
func (e *Engine) Stop()         // cancel context, drain moves channel
func (e *Engine) MoveLeft()     { e.dispatch(MoveLeft) }
func (e *Engine) MoveRight()    { e.dispatch(MoveRight) }
func (e *Engine) MoveDown()     { e.dispatch(MoveDown) }
func (e *Engine) RotateCW()     { e.dispatch(RotateCW) }
func (e *Engine) RotateCCW()    { e.dispatch(RotateCCW) }
func (e *Engine) HardDrop()     { e.dispatch(MoveHardDrop) }

func (e *Engine) dispatch(m MoveType) {
    if e.mode != ModePlayer { return }
    select {
    case e.moves <- m:
    default: // drop if channel full
    }
}

func (e *Engine) transitionToSpectator(won bool) {
    e.mode = ModeGameOver
    e.emitUpdate(EngineUpdate{Kind: UpdateGameOver, Won: won})
    // It does NOT stop gravity/moves itself: those goroutines self-exit when
    // e.mode != ModePlayer. The consumers (rows/events/meta/countdown/roster)
    // keep running so the now-spectating player still sees the board update.
}
```

#### `consumer.go`

`Start()` implementation:

```go
func (e *Engine) Start() error {
    ctx, cancel := context.WithCancel(context.Background())
    e.ctx = ctx
    e.cancelFn = cancel

    // 1. Fetch meta to get seed and pieceIdx
    meta, metaSeq, err := natspkg.FetchGameMeta(ctx, e.js, e.gameID)
    if err != nil { cancel(); return err }
    e.playerCount = meta.PlayerCount
    // Set visibleRowStart based on game mode
    if e.gameMode == config.ModeCompetitive {
        e.visibleRowStart = config.CompetitiveVisibleRowStart(meta.PlayerCount)
    } else {
        e.visibleRowStart = config.VisibleRowStart // 4
    }

    // BOTH modes seed the RNG with meta.Seed — there is no seed+1. playerIdx was
    // supplied by the caller (lobby.JoinGame's return value) at construction, so
    // no creator/joiner discovery is needed here.
    if e.gameMode == config.ModeCooperative {
        e.seq = rng.New(meta.Seed)
        e.pieceIdx.Store(0)
        // Single shared wide playfield with the standard visible height. Both
        // players draw from the SAME sequence; their pieces differ only because
        // spawnPiece offsets the spawn column by playerIdx*StandardWidth.
        e.playfield = game.NewPlayfieldWithHeight(
            meta.PlayerCount*config.StandardWidth,
            config.HeadroomRows+config.VisibleRows,
        )
    } else {
        e.seq = rng.New(meta.Seed)
        e.pieceIdx.Store(meta.PieceIdx)
        // Competitive: taller board (extra rows per player).
        e.playfield = game.NewPlayfieldWithHeight(
            config.StandardWidth,
            config.CompetitiveTotalRows(meta.PlayerCount),
        )
    }
    e.metaSeq = metaSeq

    // 2. Fetch playfield state — one message per cell; never-written cells are
    // absent from the result and stay empty. The engine builds its own cell
    // subjects (row-major, via cellSubjects) with the mode-appropriate scheme —
    // coop: CoopCellSubject (shared board, no player token); competitive:
    // CompetitiveCellSubject (scoped by own UUID).
    cells, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, e.cellSubjects())
    if err != nil { cancel(); return err }
    var maxSeq uint64
    for _, c := range cells {
        data, _ := game.UnmarshalCell(c.Payload)
        e.playfield.Apply(c.Row, c.Col, data, c.Seq)
        if c.Seq > maxSeq { maxSeq = c.Seq }
    }
    e.hadActivePiece = e.playfield.ActivePieceForPlayer(e.playerIdx) != nil

    // 3. Start own cell consumer from maxSeq+1. The 3rd arg of runConsumer is a
    // FILTER SUBJECT (coop: shared cells; competitive: own player-scoped cells);
    // the 4th is the opponentID ("" for our own board); the last is isOpponent.
    go e.runConsumer(ctx, e.playfield, e.cellFilterSubject(), "", maxSeq+1, false)

    // 4. Competitive: start a consumer for the known opponent (if any) and run a
    // roster consumer that DISCOVERS the rest dynamically (one consumer per
    // opponent is started lazily by startOpponentConsumer). Cooperative has no
    // opponent consumers — both players share the same cell subjects.
    if e.gameMode == config.ModeCompetitive {
        if e.opponentPlayerID != "" {
            e.startOpponentConsumer(ctx, e.opponentPlayerID)
        }
        go e.runRosterConsumer(ctx)
    }

    // 5. Start the per-concern consumers (events, meta, countdown).
    go e.runEventsConsumer(ctx)
    go e.runMetaConsumer(ctx)
    go e.runCountdownConsumer(ctx)

    // 6. Start the combined input+gravity goroutine if playing. If the game is
    // already in progress and we have no active piece yet, spawn one immediately.
    // Gravity and input share one goroutine (runInput) so a player's own gravity
    // drop and move never publish to their cell subjects concurrently and lose the
    // per-subject CAS race (in either mode).
    if e.getMode() == ModePlayer {
        if e.playfield.ActivePieceForPlayer(e.playerIdx) == nil &&
            meta.Status == config.GameStatusInProgress {
            e.spawnPiece(ctx, false) // Start holds no lock
        }
        go e.runInput(ctx)
    }
    return nil
}
```

(The snippet shows the coop/competitive branches; the mode switch gained a
teams arm — team-sized shared board, coop-style RNG, one opposing-team board
consumer instead of the roster/opponent consumers — specified in Phase 8.)

The engine runs ONE ordered consumer per concern, not a single wildcard consumer:

- **Own cell consumer** (`runConsumer`): filter `cellFilterSubject()` — coop's shared
  `…playfield.cell.>` or competitive's own `…player.<ownID>.playfield.cell.>`.
- **Opponent cell consumers** (competitive only): one `runConsumer` per discovered
  opponent, filter `…player.<oppID>.playfield.cell.>`, started by
  `startOpponentConsumer` (which first fetches the opponent's board snapshot via
  `FetchPlayfieldState` over `CompetitiveCellSubject(gameID, oppID, r, c)` subjects).
- **Roster consumer** (competitive only, `runRosterConsumer`): filter
  `…roster.*` — discovers opponents (including late joiners) and spins up their
  cell consumers.
- **Opposing-team board consumer** (teams only): ONE extra `runConsumer` over the
  opposing team's shared board, started by `startTeamBoardConsumer(ctx, 1-e.teamIdx)`
  and stored in `opponentPlayfields` under `TeamBoardKey(team)` (`"team-<idx>"`),
  so `OpponentSnapshots` flows to the UI unchanged. Teams runs NO roster consumer
  and no per-player opponent consumers (the roster is fixed before the game
  starts; elimination events carry the player's team). See Phase 8.
- **Events consumer** (`runEventsConsumer`): filter `…events` — handles
  `EventLineClear`, `EventShrink`, and `EventGameOver`.
- **Meta consumer** (`runMetaConsumer`): filter `…meta` — spawns the first piece
  when the game transitions to `in_progress`.
- **Countdown consumer** (`runCountdownConsumer`): filter `…countdown`.

`runConsumer(ctx, pf, filterSubject, opponentID string, startSeq uint64, isOpponent bool)`:
the 3rd argument is a FILTER SUBJECT (not a playerID), the 4th tags emitted
`UpdateOpponentField` events, and `isOpponent` selects whether this consumer runs
lock-in detection (own board) or just re-renders the opponent's board. Each
delivered message is one cell: parse the position with
`natspkg.ParseCellFromSubject(subject)`, decode with `game.UnmarshalCell`, and
apply with `pf.Apply(row, col, cell, seq)` (stream sequence from
`msg.Metadata()`). The consumer emits `ChangedRows: []int{row}` derived from the
cell's row, so the UI's row-oriented render contract is unchanged.

**Lock-in detection logic** lives in the own cell consumer, run after each
`pf.Apply` call (under `e.mu`): it compares "had an active piece" against "has an
active piece for our `playerIdx`", and when the piece's last active cell
disappears it calls `handleLockIn(ctx)`. The check is gated on
`e.getMode() == ModePlayer` — an eliminated teams player's vacate batch (the
elimination erases their own active cells) echoes back through this same
consumer, and without the gate that echo would fire a spurious lock-in. The `orderedCellKeys` publish order
guarantees that this happens at the batch's LAST vacate, with the
landing/locked cells already applied. `handleLockIn` runs the completed-row
check and scoring, increments `pieceIdx`, fires `publishPieceIdxUpdate`, and (if
we are a player) spawns the next piece via `spawnPiece(ctx, true)` (locked=true:
handleLockIn runs under the consumer's e.mu), which calls `handleTopOut` if the
new piece cannot be placed.

`handleTopOut(ctx, locked bool)` (`locked` reports whether the caller already
holds `e.mu`) only publishes an `EventGameOver` (with `Score` and `PieceCount`)
and transitions this engine to spectator (`transitionToSpectator(false)`); in
COOPERATIVE mode it also kicks off `transitionGameToFinished`. In TEAMS mode it
routes to `handleTeamTopOut` instead — per-player elimination while the team
plays on; no `transitionGameToFinished` (see Phase 8). It does NOT publish
the archive record, delete the stream, or remove the KV entry — that happens later
in `archive.ArchiveAndCleanup`, wired via `engine.OnGameFinished` and run ~5s after
the finish transition. Competitive finishing is decided by the last player standing
in `handleGameEvent` (which also handles the all-eliminated draw); teams finishing
by `handleTeamGameOverEvent` when a whole team is eliminated (Phase 8).

```go
hasActive := pf.ActivePieceForPlayer(e.playerIdx) != nil
if e.hadActivePiece && !hasActive {
    e.handleLockIn(ctx) // completed-row check, scoring, pieceIdx++, spawn next
    hasActive = pf.ActivePieceForPlayer(e.playerIdx) != nil // RE-READ — see below
}
e.hadActivePiece = hasActive
```

The **re-read after `handleLockIn`** is required by the write-through:
`handleLockIn` spawns the next piece, and the publish write-through makes that
piece active in `pf` immediately (the whole block runs under `e.mu`). Without the
re-read, `hadActivePiece` would be set to the pre-spawn `false` even though a piece
is active — and if that piece then locks before the consumer processes another
echo (runInput races ahead on fast drops), its lock-in transition is missed and
the player stops spawning entirely. Re-reading captures the just-spawned piece, so
`hadActivePiece` stays correct.

`publishPieceIdxUpdate`: in cooperative mode this is a no-op (each player tracks
its own `pieceIdx` locally and never writes it to meta). In competitive mode it
does a read-then-CAS update of `meta.PieceIdx`. This is fire-and-forget (called
via `go`); errors are ignored rather than blocking the consumer goroutine.

**Shrink event handling** (events consumer):
```go
case EventShrink:
    if ev.PlayerID != e.playerID {
        // Every line clear shrinks ALL other players, so the shrink is applied
        // by any engine that didn't publish it (the GameEvent.TargetPlayer field
        // exists but is unused for shrinks).
        go e.applyOpponentShrink(ctx, ev.RowsRemoved, ev.PlayerIdx)
    }
```

`applyOpponentShrink(n, causerIdx)`: calls `e.playfield.ProjectShrink(n, causerIdx, e.playerIdx)`,
which shifts the locked stack up by `n`, adds `n` permanent adversarial garbage rows at the
bottom, holds our own falling piece in place (lifting it only as far as the rising stack/garbage
forces it, 0..n rows), and returns a `topOut` flag. The cells the shift actually changed
(`changedCells`, diffed under `e.mu`) are published NoCAS (authoritative, like line clears) —
`orderedCellKeys` re-stamps the held piece's active cells first, applies the risen stack second,
and vacates last, so the piece never transiently vanishes — followed by a full-board re-render.
When `topOut` is true — the piece was squeezed off the top — it calls `handleTopOut`.

#### `rtt.go` — continuous publish→echo latency measurement

While playing, the HUD shows a continuously updating **RTT**: the time from initiating
a batch publish commit to the own-board ordered consumer delivering that batch's **first
message** back (the loop every visible board change travels). Implementation:

- `trackRTT(t0, commitSeq, n)` — called by all three publish helpers after a successful
  commit, with `t0` captured immediately before the publish call (each merge-retry
  attempt and each NoCAS chunk measures independently). The batch's first message has
  sequence `commitSeq-(N-1)`; `t0` is registered under it in `rttPending`.
- `noteRTTEcho(seq)` — called by the own-board consumer for every delivered message;
  a match completes the measurement, stores it (exposed via `RTT() time.Duration`) and
  emits `UpdateRTT` on the `Updates` channel.
- Race closure: the echo can arrive before `trackRTT` runs (ack and delivery race on
  the same connection). `lastEchoSeq` — the consumer's high-water mark, valid because
  the ordered consumer delivers strictly by stream sequence, maintained under `rttMu` —
  lets `trackRTT` complete the measurement immediately in that case. Stale pending
  entries are pruned after 10 s.
- UI: the HUD adds a `RTT` stat (`formatRTT`: em dash before the first
  measurement, one decimal under 10 ms, whole ms above; reset on game enter/leave).
  The value is color-coded by `rttColor`: normal text color ≤ 75 ms, a yellow→orange
  blend from 75 ms to 150 ms (`colWarn`→`colOrange` via `lerpColor`), red above 150 ms.
  Spectators never publish, so their readout stays an em dash.
- Tests: `rtt_test.go` — a live engine measures a positive RTT after a move and drains
  `rttPending`; the echo-beats-ack race path is covered directly.

#### `move.go` — input + gravity loop

`runInput` is the engine's single gameplay-write goroutine: it processes player
input **and** drives the gravity ticker. Running both on one goroutine is
deliberate — a player's own gravity drop and a player move can never publish to
their cell subjects concurrently, so they can never lose the per-subject CAS race
against each other (in either mode). This is the fix for the spurious rainbow
flashes that were otherwise visible during competitive play.

```go
func (e *Engine) runInput(ctx context.Context) {
    level := 0
    timer := time.NewTimer(game.GravityInterval(level))
    defer timer.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case move := <-e.moves:
            if e.mode != ModePlayer { continue }
            // Player input — drop+flash on CAS failure (internal=false).
            _ = e.attemptMove(ctx, move, false)
        case <-timer.C:
            if e.mode != ModePlayer { return } // became a spectator
            // internal=true: gravity is engine-driven, not player input. In coop
            // this routes to merge-retry on CAS conflict (the piece keeps
            // falling); it flashes only if the tick is ultimately dropped. The
            // return value is ignored — lock-in is handled by the consumer's
            // lock-in detector, not here. Serialized with the moves arm above.
            _ = e.attemptMove(ctx, MoveDown, true)
            // Recompute level from the running line total in cooperative mode.
            if e.gameMode == config.ModeCooperative {
                if newLevel := game.Level(int(e.totalLines.Load())); newLevel != level {
                    level = newLevel
                }
            }
            timer.Reset(game.GravityInterval(level))
        }
    }
}

// attemptMove takes the lock, then dispatches to the board-shape-specific
// handler: sharedBoard() is true for cooperative AND teams (both are
// multi-writer shared boards), so teams play routes through the same
// attemptMoveCoop path. internal=true marks an engine-driven move (gravity
// ticks); on shared boards those use merge-retry on CAS failure so the piece
// keeps falling under contention.
func (e *Engine) attemptMove(ctx context.Context, move MoveType, internal bool) error {
    e.mu.Lock()
    if e.sharedBoard() { // coop || teams
        return e.attemptMoveCoop(ctx, move, internal)
    }
    return e.attemptMoveStandard(ctx, move, internal)
}
```

`attemptMoveStandard` / `attemptMoveCoop` (both enter holding `e.mu` and release
it before publishing):

- Read the active piece via `e.playfield.ActivePieceForPlayer(e.playerIdx)`;
  return if nil.
- `MoveHardDrop` delegates to `publishHardDrop` / `publishHardDropCoop`.
- For directional moves, compute `newPiece` and validate with `CanPlace`
  (standard) / `CanPlaceCoop` (coop); rotations use `Rotate` / `RotateCoop`.
- If a `MoveDown` is invalid, the piece locks: project the lock with
  `ProjectLock(affected, e.playerIdx)`, diff it to cells (`diffCells`) and
  publish them. Competitive publishes NoCAS; coop uses merge-retry. (Coop
  additionally distinguishes "blocked by the other player's active piece" —
  `CanPlace` would still succeed — and in that case does NOT lock; it waits
  for the next gravity tick.)
- A valid move projects with `ProjectMove(affected, &newPiece, e.playerIdx)`,
  diffs to cells with `diffCells(e.playfield.Rows, rows)` (still under `e.mu`)
  and publishes via `publishProjectedCells` (competitive / coop player input,
  drop+flash) or `publishProjectedCellsWithMergeRetry` (coop gravity). The
  `orderedCellKeys` batch order (new active cells before vacates) means a
  single-row horizontal-I never transiently vanishes mid-relocate.

There is no `buildMoveUpdates` or `lockIn` helper; the projected rows come from
the `Project*` methods, `diffCells` reduces them to the changed cells, and
`buildBatchUpdates` turns the ordered keys + cell map into a
`[]natspkg.CellUpdate` with per-subject CAS expectations sourced from
`e.playfield.CellLastSeq(row, col)`. Line-clear detection and the cleared-row shift live in
`consumer.go`'s `handleLockIn` (fired by the cell consumer's lock-in detector),
not on the move side. In competitive mode `handleLockIn` publishes an `EventShrink`
(with `PlayerID`/`PlayerIdx` set to this player) for every line clear (1+ lines;
rows added = lines cleared); all other players apply the shrink to their own
playfields. In teams mode `handleLockIn` scores `teamSize × lines`, publishes an
`EventLineClear{Team, Score, LinesCleared}` (teammates fold in BOTH the score and
the line count, keeping level/gravity in sync across the team) and an
`EventShrink{Team, TargetTeam: 1−teamIdx, RowsRemoved}` aimed at the OPPOSING
team's shared board (see Phase 8). See `jetricks-gameplays.md` for shrink rules.

Hard drops compute the destination ONCE (`HardDropDestination` /
`HardDropDestinationCoop`) and project it with `ProjectHardDrop(...,
lockOnLand=true)`; there is no recompute-and-retry-until-it-lands loop.
Competitive publishes the landing's changed cells NoCAS; coop uses merge-retry.
Either way `orderedCellKeys` applies the landing cells before the vacated old
positions, so a line completed by the drop is detected at this lock, not one
piece later. In coop, if the destination is only blocked by the OTHER player's
active piece (not locked cells/bounds), the drop lands as an active piece
(`lockOnLand=false`) and gravity retries.

In **cooperative** mode the board is shared, so a line clear must repaint every
player's board. The clearing engine emits a full-board re-render itself; every
other engine, on receiving the `EventLineClear` game event, also emits a
full-board re-render (`emitFullBoardRerender`) in addition to folding in the shared
score delta. A *full* re-render (the UI repaints all visible rows in one update) is
required because the bounded, non-blocking `Updates` channel can drop
individual per-row triggers, which would otherwise leave stale rows on the other
player's board. The same full-board re-render is emitted after a competitive
shrink (`applyOpponentShrink`).

Note the distinction between the **re-render** (whole board, UI only) and the
**publish**: the clear/shrink publish only the cells that *actually changed*
(`changedCells(cur, projected, fromRow, toRow)` diffs the projection against
the live rows over the visible range), not the whole visible range. A low stack
changes only a handful of cells, so on the shared coop board this sharply cuts
the per-subject CAS contention that was otherwise failing the clear's
merge-retry against the other player's moving piece — exhausting the retries
and dropping the clear (uncleared line) and the follow-up spawn (stuck player).
Competitive clear/shrink publish their changed cells NoCAS (per-player boards,
single writer — authoritative); the coop clear stays CAS+merge-retry because a
NoCAS shared-cell write from a stale snapshot could clobber the other player's
mid-flight piece.

#### Publish & CAS helpers (`engine.go` / `move.go`)

There is no separate CAS source file. There is no `Publish`, `PublishHardDrop`,
`ErrMoveDropped`, or `ErrLockIn`, and there is no wait-on-`cellUpdated`-then-retry
loop. All CAS logic lives in the publish helpers in `engine.go` and the hard-drop
helpers in `move.go`. Every helper takes a `map[game.CellPos]game.Cell` (the
output of `diffCells`/`changedCells`) and orders the batch with
`orderedCellKeys`:

- `publishProjectedCells(ctx, cells, flashCells, locked)` — single atomic batch
  with per-subject CAS from `e.playfield.CellLastSeq`. On success it
  write-throughs the committed cells (`applyPublishedCells`); on CAS failure it
  DROPS the step (no retry) and calls `emitCASFlash(flashCells)` so the local
  player gets a rainbow flash. Used for player moves in both modes and for
  competitive spawn/gravity.
- `publishProjectedCellsWithMergeRetry(ctx, cells, flashCells, locked)` — the
  ONLY retrying CAS path. On failure it refetches the latest state of every
  affected cell in ONE batched round trip (`refetchAndMerge` via
  `FetchPlayfieldState`), keeps our content for each cell UNLESS the latest
  stream content is the OTHER player's active cell — those cells are skipped
  entirely (neither overwritten nor vacated) — and retries with refreshed
  per-subject expectations (up to 16 attempts, escalating per-player-offset
  backoff of `(attempt+playerIdx)×200µs` capped at 2ms to break lockstep)
  before dropping + flashing. If the merge skips EVERY cell (all covered by
  the other player's mid-flight piece), the step is dropped with a flash.
  Write-throughs the committed (first-attempt or merged) cells on success.
  Used for ALL coop shared-cell writes (spawn, gravity, lock, hard drop,
  line clear).
- `publishProjectedCellsNoCAS(ctx, cells, locked)` — atomic batch with no CAS,
  for authoritative competitive writes (lock, hard-drop landing, line clear,
  shrink) where the publisher owns the per-player subjects. Write-throughs on
  success. A batch above the server's atomic-batch limit (1000 messages — only
  reachable on degenerate many-player boards) is split into sequential atomic
  chunks along the already-ordered key list; the category order remains a
  correct total order across chunk boundaries, at the cost of a briefly visible
  intermediate board between chunks.
- `applyPublishedCells(orderedKeys, get, commitSeq, locked)` — write-throughs a
  committed batch into `e.playfield`: each cell's content + the per-subject
  stream sequence inferred from the commit ack (`message i of N →
  commitSeq−(N−1−i)`), advancing the board and the CAS expectation without
  waiting for the echo. Takes `e.mu` unless `locked` (spawn/clear publish under
  the consumer's lock).
- `buildBatchUpdates(keys, cells, locked)` builds the `[]natspkg.CellUpdate`
  (per-subject CAS) from the ordered keys + cell map; it snapshots
  `CellLastSeq` under `e.mu` (unless `locked`) before marshalling the payloads.
- `orderedCellKeys(m map[game.CellPos]game.Cell) []game.CellPos` — the batch
  ordering rule (active → locked → empty, ascending (row, col) tie-break; see
  the "Batch ordering" discussion above). `cellCategory(c)` ranks a cell's new
  content.
- `diffCells(cur []game.Row, projected map[int]game.Row)` — the changed cells
  of a `ProjectMove`/`ProjectLock`/`ProjectHardDrop` projection (moves, spawn,
  lock, drop; ~4-8 messages per move). Call with `e.mu` held.
- `changedCells(cur, projected []game.Row, fromRow, toRow)` — the changed cells
  of a full-board projection over a row range (`ProjectClearRows` /
  `ProjectShrink`). Call with `e.mu` held.
- Hard drops: `publishHardDrop` (competitive, NoCAS) and `publishHardDropCoop`
  (coop, merge-retry) in `move.go`.

The consumer still signals `e.cellUpdated` (non-blocking) after each `pf.Apply`
so other paths can observe progress, but no CAS path blocks on it:
```go
select {
case e.cellUpdated <- struct{}{}:
default:
}
```

**Unit tests** (`internal/engine/cellorder_test.go`, no NATS server):
- `TestOrderedCellKeys` — the category publish order (active → locked → empty,
  ascending (row, col) tie-break) on a horizontal-I downward relocate.
- `TestDiffCells` — a move projection diffs to exactly the changed cells.
- `TestChangedCells` — a row-range projection diffs to exactly the changed cells.

**Integration tests** (`internal/engine`, against a real embedded NATS server via
`internal/testutil.StartServer`). Coop tests that need a pre-filled stack use the
`publishCoopRowCells` helper (`cleartiming_test.go`), which publishes one message
per occupied cell of a row. The current suite is:
- `TestEngineStart` — engine start fetches meta and playfield state and spawns a piece.
- `TestEngineMoveLeftRight` — directional moves produce the expected cell updates.
- `TestEngineHardDrop` — hard drop places the piece at the correct destination and locks it.
- `TestEngineUpdatesChannel` — updates are emitted on the `Updates` channel.
- `TestCoopLineClearRerendersOtherPlayer` / `TestCoopLineClearKeepsOtherPlayersPiece`
  — a coop line clear repaints the shared board without corrupting the other player's piece.
- `TestCoopHardDropClearsCompletingLineImmediately` — a hard drop that completes a
  line clears it at this lock (the `orderedCellKeys` landing-before-vacate order),
  not one piece later.
- `TestCoopConcurrentPlayNoPieceCorruption` — concurrent coop play keeps both pieces intact.
- `TestCoopHorizontalIFallsWithoutSpuriousLock` — the single-row horizontal I falls
  without firing a spurious lock-in.

TODO (not yet implemented): a competitive shrink A→B row-shift test, a
"CAS failure on MoveDown → lock-in" test, and a two-engine move-visibility test.

---

## Phase 4 — Lobby

### 4.1 `internal/lobby`

#### `events.go` (types only, implement first)

As specified in Section 10 of the project structure doc.

#### `listing.go` (types only)

As specified in Section 10, plus the teams-mode fields: `GameListing.TeamSize`
(players per team), `PlayerSummary.Team` and `PlayerSummary.TeamSlot` (the
player's team 0/1 and their section index within the team board, assigned in
join order), and `GameListing.TeamMemberCount(team int) int` (how many roster
members belong to the given team).

#### `presence.go`

`runHeartbeat`: every `config.PresenceHeartbeat`, call `l.kv.Put(ctx, key, value)`.
The value is JSON-encoded `PlayerPresence`. On context cancellation, delete the key
(so presence expires immediately rather than waiting for TTL).

#### `lobby.go`

```go
type Lobby struct {
    playerID        string
    name            string
    kv              jetstream.KeyValue
    js              jetstream.JetStream
    Updates         chan LobbyUpdate // 256-buffered; emitUpdate drops on full (pings only, state lives below)
    mu              sync.RWMutex
    players         map[string]PlayerPresence
    games           map[string]GameListing
    abandoned       map[string]bool
    archives        []config.ArchiveRecord
    chatLog         []ChatMessage // capped at chatLogCap (200); snapshot via ChatLog()
    status          PresenceStatus
    currentGameID   string
    cancelFn        context.CancelFunc
    initialLoadDone chan struct{}
}

// New constructs the lobby (no ctx, no error); Start(ctx) launches the goroutines.
func New(js jetstream.JetStream, kv jetstream.KeyValue, playerID, name string) *Lobby
func (l *Lobby) Start(ctx context.Context) error

func (l *Lobby) Players() map[string]PlayerPresence {
    l.mu.RLock()
    defer l.mu.RUnlock()
    out := make(map[string]PlayerPresence, len(l.players))
    for k, v := range l.players { out[k] = v }
    return out
}

func (l *Lobby) Games() map[string]GameListing  // same pattern
```

`Start()` starts five goroutines: KV watcher, lobby chat consumer, archive
consumer, heartbeat, and the abandoned-game checker.

All lobby state consumed by the UI — players, games, archives, and the chat
log — lives in the Lobby under `l.mu`; a `LobbyUpdate` is only a
"re-read the snapshot" ping, because `emitUpdate` is a non-blocking send that
drops when `Updates` is full (routine during login, where the consumers
replay their backlogs before the UI pump attaches). The chat consumer appends
each message to `chatLog` before emitting; the UI re-reads `ChatLog()` on
each chat ping and `initLobby` seeds its copy from it when the pump attaches,
so replayed chat survives dropped pings (`lobby_test.go`
`TestChatBacklogSurvivesUndrainedUpdates` pins this).

**KV watcher**: `kv.WatchAll(ctx)` — on each update, decode the key prefix to
determine if it's a player or game entry, update the appropriate map under `l.mu.Lock()`,
and send a `LobbyUpdate` to `l.Updates`.

`CreateGame(ctx, mode, playerCount, teamSize)` — `playerCount` is 2–4, selected
by the user in the create game form (no longer hardcoded to 2). For
`ModeTeams` the UI passes the per-team size and `playerCount =
config.TeamCount × teamSize`; other modes pass `teamSize = 0`:
1. Generate `gameID = uuid.New().String()`
2. Call `natspkg.EnsureGameStream(ctx, l.js, gameID)`
3. Create `config.GameMeta` with the new ID, mode, player count, team size, seed, status=created, creatorID
4. Publish to `config.MetaSubject(gameID)` with `ExpectLastSeq: 0`
5. Update own roster entry
6. Update `games.<id>` KV entry (listing carries `TeamSize` for teams games)
7. Return `gameID`

```go
// ErrTeamFull is returned by JoinGame when the requested team already has
// TeamSize members.
var ErrTeamFull = errors.New("team is full")

// JoinResult is the roster position assigned to a player by JoinGame.
type JoinResult struct {
    PlayerIdx int // GLOBAL roster index (join order across both teams)
    Team      int // teams mode: 0 = A, 1 = B
    TeamSlot  int // teams mode: section index within the team board
}
```

`JoinGame(ctx, gameID, team) (JoinResult, error)` — `team` is the team the
player picked (0 or 1; teams mode only, other modes ignore it; may fail with
`ErrTeamFull`). The caller passes `JoinResult`'s fields to `engine.New` as
`playerIdx`/`teamIdx`/`teamSlot`. The listing update is a **CAS loop** — team
capacity validation and `TeamSlot` assignment must be atomic with the roster
append, or two concurrent joiners could both land in the last slot of a team:
1. If already in the game's roster, update presence and return the existing
   `JoinResult` (rejoin is idempotent).
2. CAS loop on the `games.<id>` KV entry: `Get` the listing, validate the team
   has capacity (`TeamMemberCount(team) < TeamSize`, else `ErrTeamFull`),
   assign `TeamSlot = TeamMemberCount(team)`, append self, then
   `kv.Update(..., revision)` — on revision conflict, retry from the `Get`.
3. Publish own roster entry to `config.RosterSubject(gameID, l.playerID)` —
   only AFTER the CAS commit, so the roster never announces a join the listing
   rejected.
4. If the listing is now full (`len(Players) >= PlayerCount` — for teams this
   is exactly "both teams full", since per-team capacity is enforced above),
   set the listing to `starting` in the same CAS write and transition meta
   status to `starting`.
5. Update KV presence to `StatusInGame` with this gameID and return the
   assigned `JoinResult`.

`ToggleReady(ctx, gameID) (ToggleReadyResult, error)` — there is no `MarkReady`;
ready state is a toggle. It CAS-updates the player's `Ready` flag on the game-listing
KV entry (retrying on revision conflict) and reports whether all players are now
ready via `ToggleReadyResult{AllReady, Players, MyReady}`. Advancing to
`in_progress` is done separately by `StartGame(ctx, gameID)`, which transitions
meta to `in_progress` (setting `StartedAt`). The native UI renders each
player's state in the pre-game checklist as a filled pill badge (green
"READY" / red "NOT READY" — `readyBadge` in `nativeui/game.go`).

**Abandoned-game detection & deletion.** `runAbandonedChecker` (goroutine,
started by `Start`) re-evaluates every listed game once per
`config.AbandonedCheckInterval` (1 min) via `checkAbandoned`:

- `created`/`starting` games are abandoned once
  `now - CreatedAt > config.AbandonedUnstartedTimeout` (15 min) — the creator
  never joined, or the players never readied up.
- `in_progress` games are abandoned once the game stream's `State.LastTime` is
  older than `config.AbandonedIdleTimeout` (1 min) — a live game publishes
  constantly, so a silent stream means everyone left. An `in_progress` listing
  whose stream returns `ErrStreamNotFound` is flagged immediately; other stream
  lookup errors do NOT flag (can't tell — transient failure).

The rules live in `isAbandoned(ctx, g, now)`; `now` is a parameter so tests
inject a future time rather than waiting out the timeouts. Each pass rebuilds
the `abandoned` set from scratch (resumed activity un-flags a game) and emits
`LobbyUpdateGames` when the set changes; the UI reads it via
`AbandonedGames()`. `DeleteGame(ctx, gameID)` is the user-confirmed teardown:
`natspkg.DeleteGameStream` (tolerating `ErrStreamNotFound`), then
`natspkg.PurgeGameChat` (the game's messages in the shared chat stream), then
the `games.<id>` KV delete, whose watcher event removes the listing — and its
abandoned flag — on every client.

**Integration tests**: create/join flow, presence heartbeat renewal, KV watcher
delivers updates to Maps. `abandoned_test.go`: the abandonment rules with an
injected future `now` (fresh/stale created game, active/idle started game,
deleted-stream case, `checkAbandoned` end-to-end) and `DeleteGame`'s full
teardown (stream + KV listing gone, game chat purged, lobby chat untouched,
idempotent re-delete).

---

## Phase 5 — Cleanup

### 5.1 `internal/cleanup`

`Run(ctx, js, kv, lobby) error` (there is no system-account `*nats.Conn` client —
cleanup works entirely through JetStream and the lobby maps):

1. List game streams with `natspkg.ListGameStreams(ctx, js)` (which filters
   `js.StreamNames` by the `jetricks.game.>` subject). There is no
   `natssysclient`/`Jsz` fast path.
2. For each stream name matching `JETRICKS_GAME_`, look up its game ID in the
   lobby's Games and Players maps.
3. Apply cleanup rules: streams with no KV entry are deleted unless their meta is
   still `starting`/`in_progress` (in which case the KV entry is re-created);
   `finished` games are archived; `created`/`starting`/`in_progress` games whose
   players are all absent are cancelled or finished-as-abandoned.
4. All state transitions go through `natspkg.PublishMeta` with CAS.

---

## Phase 6 — UI

### 6.1 `internal/nativeui`

A Gio (`gioui.org`) desktop window — the sole front end. It reuses `engine`, `lobby`,
`game`, `nats`, `config`, `cleanup` unchanged and provides the presentation + input layer:

- `app.go` — `App` struct, `New`, `Run` (the `app.Window.Event()` frame loop), and the
  `screen` state machine (login → lobby → game). `app.Main()` runs on the OS main thread
  (from `main.go`); `App.Run` runs on a goroutine.
- `bridge.go` — `pumpEngine` / `pumpLobby`: drain `engine.Updates` / `lobby.Updates`, fold
  scalar state under a mutex, and call `window.Invalidate()`. No polling.
- `login.go` / `lobby.go` / `game.go` — the three screens. The lobby's game rows
  read `lb.AbandonedGames()`: an abandoned game shows a red `· abandoned` tag and
  a red `dangerButton` **Delete** whose click swaps the row's action buttons for
  an "Are you sure you want to delete this game?" confirmation on its own line
  under the game info, so it never squeezes the info text
  (`confirmDeleteID` on the App, `del`/`delYes`/`delNo` in `gameRowBtns`);
  confirming dispatches `deleteGame` (`lifecycle.go`) → `lobby.DeleteGame`.
- `board.go` — board drawing via `internal/render.CellStyle` (RGBA). `drawCell` shades
  filled cells with the 8-bit bevel (lighter top/left strips, darker bottom/right,
  a gloss pixel in the corner — `CellAppearance.Bevel` gates it so empty squares stay
  flat), `drawBoard` surrounds every playfield with a chunky `colBorder` arcade-well
  frame, `scanlines` is the subtle full-window CRT overlay painted last in
  `App.layout`, and `hardShadow` is the offset "sticker" drop shadow under
  buttons and dialogs.
- `fonts.go` — the embedded "Press Start 2P" pixel face (`PressStart2P-Regular.ttf`,
  SIL OFL 1.1 — license in `PressStart2P-OFL.txt`): `uiFontCollection()` (the Go faces
  plus the pixel face under the `pixelTypeface` name, shared by `newUITheme` and the
  layout tests) and the `a.pixel(size, txt, col)` label helper. The pixel face is the
  display font — title, headers, buttons, HUD stats, badges, countdown, banner — while
  body text (chat, lists, editors) stays in the Go faces for readability.
- `input.go` — keyboard → engine moves. The board tag registers `key.FocusFilter` +
  `key.Filter{Focus: tag, …}` each frame and is focused with `key.FocusCmd`; ←/→ move,
  ↓ soft drop, ↑/X rotate CW, Z rotate CCW, Space hard drop.
- `lifecycle.go` — `initLobby`, create/join/spectate engine wiring, `runCountdown`,
  `returnToLobby`, `teardown` (wires `engine.OnGameFinished` → `archive.ArchiveAndCleanup`,
  and `engine.OnStreamMsg` → `recordStreamMsg` for the NATS message panel).
- `natslog.go` — the "Show NATS messages" panel: `recordStreamMsg` (the `OnStreamMsg`
  hook, capped 200-entry log under `a.mu`, gated on the checkbox mirror `msgShow`),
  `natsMsgPanel` (fixed-height bottom strip, scroll-to-end list), and `jsonSpans`,
  a display-only JSON syntax colorizer rendered in the Go Mono face.
- `brand.go` — the nats.io "N" logo (`nats-icon.png`, embedded via `go:embed`,
  decoded once), `lobbyBanner` (the branding strip across the top of the lobby and
  archive screens), and `natsTag` (the inline "N"-logo + "NATS.io" chip reused on the
  login screen's tagline and at the foot of the game HUD). NATS branding is deliberately
  liberal: the chrome accent is the NATS brand blue and `colNATSGreen` is the logo's
  green, so the brand colors run through headers, buttons, and borders everywhere.
- `fireworks.go` — the victory fireworks overlay: `newFireworksShow` (rolled once in
  `pumpEngine` when `UpdateGameOver{Won: true}` arrives for a competitive/teams win),
  `fireworksOverlay` (a paint-only full-screen `layout.Stack` layer over the game
  screen; each frame is a pure function of `gtx.Now` in the countdown/CAS-flash
  idiom, drawn at elapsed time modulo the ~8 s `cycle` and kept animating via
  `invalidate()` — the show loops until dropped), and
  `fwLogoPoints`/`fwSynadiaPoints` (via `sampleLogoPoints`), which sample the
  embedded `nats-icon.png`/`synadia-icon.png` on a 22×22 grid — with
  per-particle radial scatter velocities — so every rocket explodes into a
  small particle logo (the NATS "N"; one rocket in ten bursts into the
  Synadia "Symbol" instead — the official mark from synadia.com/about/brand,
  the white "S" swirl on the emerald rounded square — with a floor of one
  Synadia rocket forced per show since the looping choreography would
  otherwise never display one on a zero roll) that pops
  in, holds, then splits into its small squares and
  flies apart in all directions, the blocks shrinking away rather than fading
  in place and — per rocket, at random — either keeping the logo colors or
  recoloring toward one traditional fireworks color from `fwBurstPalette`
  (`drawLogoBurst`, phases split at `fwScatterStart`). Cleared in
  `startGameScreen`/`returnToLobby`, which is what ends the show.

**Lobby screen.** Player sidebar, game list (with a "Spectate" button on in-progress
games), chat (lobby-scoped lines plus a message editor — per-game messages are
filtered out here), a create game form (with a "Players"
number input 2–4), and a "Game History" section below the active games showing
archived games with mode, players, and scores — fetched from the
`JETRICKS_ARCHIVE` stream on lobby load. Each history line is prefixed with the
game's start date/time in the viewer's local timezone (`2006-01-02 15:04 MST`)
and its duration (`FinishedAt - StartedAt`, rounded to the second) via
`archiveWhen`; records missing timestamps omit the prefix. The list is ordered
by `sortedArchives`: headline score first (highest on top — `archiveScore`:
co-op total, best team total for teams, best player score for competitive),
then, between games with the same score, the one with the **shorter duration**
ranks higher (`archiveDuration`), with most-recently-finished breaking any
remaining tie. Each history row carries an accent-bordered
**"View board"** button (`viewBoardButton`) on the right that opens
the `screenArchive` viewer (`archive_view.go`), which rebuilds the saved
`ArchiveRecord.Boards` into `engine.BoardSnapshot`s (`boardSnapshotFromPicture`) and
redraws the playfield exactly as it stood when that game ended — the single wide board
for cooperative, one board per player (labeled by ID in player color) for competitive,
and both team boards for teams. A "Back to Lobby" `secondaryButton` returns. A centered
branding banner spans the top of both the lobby and the archive screen: the nats.io "N"
logo flanking "JETRICKS: peer to peer and made with NATS.io" in the pixel face (the
"NATS.io" text in the NATS-blue accent).

**NATS message panel.** The game screen HUD (player AND spectator) has a "Show NATS
messages" checkbox. While checked, a 170 dp monospace strip across the bottom of the
window lists the tail of the messages delivered by the engine's game-stream consumers
(cells, events, meta, countdown, roster — tapped via `engine.tapMsg` → `OnStreamMsg`):
per line, the message's JetStream stream timestamp (`msg.Metadata().Timestamp`,
formatted `15:04:05.000`), the subject (accent color), and the raw JSON payload
syntax-colored by `jsonSpans`. Collection happens only while the checkbox is checked
(the UI mirrors it each frame into the `a.mu`-guarded `msgShow` flag read by the
consumer-side hook); the log resets on game entry/exit.

**Spectator mode.** The lobby's "Spectate" button creates an engine in `ModeSpectator`
(no gravity, no moves, no controls). The game screen hides controls and the ready
button, showing "Spectating" as the player status. The spectator's HUD keeps the
"Back to Lobby" button (the same `backBtn` → `returnToLobby` path as a player), so a
spectator can leave at any time.

**8-bit look and feel.** The whole UI is styled as "modern 8-bit": every label that
functions as display type (title, section headers, buttons, HUD stats, ready badges,
countdown, game-over dialog, branding banner) renders in the embedded "Press Start 2P"
pixel face while body text stays in the Go faces; chrome corners are square everywhere
(no rounded rects); panels and editors carry chunky 2 dp `colBorder` frames; buttons
and the game-over dialog sit on `hardShadow`'s offset solid shadow; filled board cells
get the classic bevel shading plus a corner gloss pixel; each playfield is wrapped in
an arcade-well frame; and a subtle `scanlines` CRT overlay is painted over every frame.
The palette is a dark blue-black (`colBg` #0d0d16, `colPanel` #16161a-ish) with the
NATS brand blue (#27aae1) as the accent. The board's cell color math
(`internal/render` blends over #111111) is unchanged.

**Button styles.** Primary actions (Play, Join, Ready, Create Game, Send) render via
`primaryButton`: the filled-accent `material.Button` restyled by `pixelize` (square
corners, pixel face) over a hard shadow. Non-primary actions — the lobby "Spectate"
and "Quit" buttons, the in-game "Back to Lobby" button, and the login collision-dialog
"Cancel" button — render via the `secondaryButton` helper: an accent (`colAccent`)
pixel-face label and 2 dp accent border over the `colPanel` background, also on a hard
shadow. This makes them read as clearly clickable instead of blending into the dark
window background.

**Per-square rendering** (`internal/render`). Cell appearance is computed by a single
helper, `render.CellStyle(cell, localPlayerIdx, showOutline)`, which is the one source
of truth used by every render path (own board, spectator boards, compact opponent
boards). It returns a `CellAppearance` (fill, outline color, outline width, and a
`Bevel` flag set for active/locked/adversarial cells); the
native drawer fills the cell with `Fill`, strokes a 1px-inset border of width
`OutlineW` in `Outline`, and — when `Bevel` is set — shades the fill with the 8-bit
highlight/shadow bevel. Fill is the tetromino's base color composited over the board
background (`blendHex(fg, "#111111", alpha)`; active ≈0.9, locked ≈0.7, adversarial
≈0.8) so opacity layering becomes a concrete color. Outline rules: own active piece →
white 2px; spectator → per-player color on active/locked; other player's active
piece (player view) → grid line only; locked non-adversarial → per-player color 2px
when `showOutline`; empty / adversarial / compact opponent board → 1px grid line.

---

## Phase 7 — Entrypoint

### 7.1 `cmd/jetricks/main.go`

Follow the bootstrap sequence from Section 14 of the spec exactly. Use `flag` package
for CLI flags. Use `os.Signal` channel with `signal.Notify` for graceful shutdown.

The lobby is **not** created at startup — it is created when the player enters their
name in the UI (`nativeui.App.initLobby`). No NATS connection is made at startup
either (see Phase 10 — Connection Picker): the window opens immediately and the
single combined login screen (name + CONNECT TO chooser) drives the connection.
`--server`/`--context` only seed the picker's defaults.

```go
func main() {
    cfg := parseFlags()
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // The App dials NATS itself when the player hits Play; the flags only
    // seed the picker's defaults.
    names, selected, _ := natspkg.ListContexts()
    runNative(ctx, cancel, nativeui.NewWithPicker(cfg, names, selected))
}

// Gio's app.Main() owns the OS main thread and blocks forever, so App.Run runs on a
// goroutine and the process exits when the window closes (or on Ctrl-C via a
// signal.Notify goroutine — the OS main loop has no signal hook). The App owns
// the connection it dials; shutdown calls a.DrainConn() (nil-safe).
func runNative(ctx context.Context, cancel context.CancelFunc, a *nativeui.App) {
    go func() {
        defer cancel()
        _ = a.Run(ctx)   // creates the window, runs the frame loop
        a.DrainConn()
        os.Exit(0)
    }()
    app.Main()
}
```

---

## Phase 8 — Teams Mode

Built on top of Phases 1–7: a third game mode that composes the cooperative
shared-board machinery (within a team) with the competitive garbage mechanic
(between teams). Gameplay rules are in `jetricks-gameplays.md` §5 ("Teams
Mode"); this phase defers to it for behavior and specifies the implementation.

**Gameplay summary.** Two teams ("A" and "B") of equal size: `TeamSize`
players each, `PlayerCount = config.TeamCount × TeamSize`. Within a team play
is exactly cooperative — one shared board of width `teamSize × 10`, height
`HeadroomRows + VisibleRows + teamSize`, per-player sections keyed by team
slot, `CanPlaceCoop` collision, merge-retry on shared-cell CAS conflicts.
Between teams it is competitive: a team's line clears add unclearable
adversarial garbage rows to the OPPOSING team's shared board. Players pick
their team when joining ("Join A" / "Join B"). A topped-out player vacates
their piece and spectates while their team plays on; a team loses when ALL of
its members have topped out, and every member of the other team — eliminated
members included — wins.

**KEY design decision — shared-board shrinks never lift pieces.** On a shared
team board NO active piece is lifted by a shrink: all active pieces are
overlaid at their current positions, a piece overtaken by the risen stack
locks where it is ("crushed"), and a shrink can never top a player out
(top-out happens at spawn failure only). This is what makes the multi-receiver
shrink application safe — the shrink batch never touches piece cells, so it
can neither corrupt a teammate's in-flight piece nor fire a spurious lock-in.

### 8.1 `internal/config`

All implemented in Phase 1's `config.go` (the updated specs above are
authoritative): `ModeTeams` (enum value 2, `String() == "teams"`), `const
TeamCount = 2`, `GameMeta.TeamSize` (`team_size,omitempty`),
`PlayerResult.Team`, `ArchiveRecord.TeamSize` + `WinningTeam`
(`winning_team`; `-1` = draw or not a teams game), the
`TeamBoardWidth`/`TeamVisibleRows`/`TeamTotalRows`/`TeamVisibleRowStart`
board-dimension helpers, and the `TeamCellSubject`/`TeamCellSubjectFilter`
builders — like coop the subjects carry no player token (per-cell ownership
lives in the payload via `Cell.PlayerIdx`, which holds the GLOBAL roster
index), scoped by team index so the two boards are disjoint.
`internal/nats/subjects.go` re-exports both builders.

### 8.2 `internal/game`

Two additions to `playfield.go` (specified in §1.3 above), no collision or
rotation changes:

- `ProjectShrinkShared(rowsToAdd, causerIdx int) []Row` — the shared-board
  shrink projection: shifts the locked stack up, appends adversarial bottom
  rows tagged `causerIdx`, and overlays EVERY player's active cells in place
  (no lift; an overtaken piece stays put to be crushed). No `topOut` return.
- `AdversarialRowCount() int` — contiguous bottom rows with **≥1** adversarial
  cell. Monotonic (garbage is permanent and bottom-anchored), which makes it
  the idempotency guard for the shrink application; "≥1" rather than "all
  cells" because crushed pieces fill — and vacated active overlays hollow —
  individual cells of a garbage row.

### 8.3 `internal/lobby`

Specified in the updated Phase 4 above: `GameListing.TeamSize`,
`PlayerSummary.Team`/`TeamSlot`, `GameListing.TeamMemberCount(team)`,
`CreateGame(ctx, mode, playerCount, teamSize)`, and
`JoinGame(ctx, gameID, team) → (JoinResult{PlayerIdx, Team, TeamSlot}, error)`
with the `ErrTeamFull` sentinel. The listing update was converted from a plain
`kv.Put` to a CAS loop (`Get` → validate team capacity + assign `TeamSlot` +
append → `kv.Update(rev)` → retry on conflict); the roster entry is published
only after the CAS commit. Both-teams-full reuses the existing roster-full →
`starting` transition unchanged.

### 8.4 `internal/engine`

**Fields and construction.** `Engine` gains `teamIdx`/`teamSlot`/`teamSize`,
`eliminatedTeam map[string]int`, `teamOutcomeDone bool`, and `expectedGarbage
int` (all specified in §3.1 above). `New(..., playerIdx, teamIdx, teamSlot)`
takes the `JoinResult` fields. The `sharedBoard()` helper (`coop || teams`)
replaces most former `ModeCooperative` checks — move routing
(`attemptMove` → `attemptMoveCoop`), lock/hard-drop/line-clear merge-retry,
anchor shifting on clears, shared level sync.

**`Start()` teams branch.** Board dims
`TeamBoardWidth(teamSize) × TeamTotalRows(teamSize)`; coop-style RNG
(`rng.New(meta.Seed)` with per-player local `pieceIdx` starting at 0 — every
player draws the full deterministic 7-bag, so both teams see the identical,
fair sequence); `cellSubject`/`cellFilterSubject` route to
`TeamCellSubject(gameID, e.teamIdx, …)`; `spawnPiece` offsets the spawn column
by `teamSlot × StandardWidth` (the within-team section, NOT the global
playerIdx). Instead of the competitive roster/opponent consumers, teams starts
exactly ONE extra consumer over the opposing team's shared board
(`startTeamBoardConsumer(ctx, 1-e.teamIdx)`), whose playfield lives in
`opponentPlayfields` under `TeamBoardKey(team)` (`"team-<idx>"`) so
`OpponentSnapshots` flows to the UI unchanged. The roster is fixed before the
game starts and elimination events carry the player's team, so no roster
consumer is needed; spectators (teamIdx 0 by default) consume team 0 as their
"own" board and team 1 via the team-board consumer.

**Elimination (`handleTeamTopOut`).** `handleTopOut(ctx, locked bool)` routes
teams to `handleTeamTopOut`, which implements per-player elimination while the
team plays on:

1. Under `e.mu`, set `hadActivePiece = false` BEFORE publishing the vacate —
   the vacate drives our active-cell count to zero and must not read as a
   lock-in (which would spawn a next piece for an eliminated player). The
   consumer-side guard (lock-in detection gated on `getMode() == ModePlayer`,
   §3.1) catches the echo for the same reason.
2. Project the removal of our own active cells (`ProjectMove(affected, nil,
   e.playerIdx)` → `diffCells`) and mark ourselves in `eliminatedPlayers` /
   `eliminatedTeam`.
3. Publish the vacate via merge-retry (shared board — the skip rule protects
   teammates' in-flight pieces), so teammates don't play around a dead piece.
4. Publish `EventGameOver{PlayerID, Team, Score, PieceCount}`, transition to
   spectator with `Won=false` (interim — flips to won if the team prevails),
   and emit `UpdatePlayerEliminated{EliminatedPlayerID, Team}`. NO
   `transitionGameToFinished` here — the game only finishes when a whole team
   is dead.

**Outcome (`handleTeamGameOverEvent`).** Every engine processes every
elimination event (including the echo of its own): record the sender in the
elimination maps, recount per-team eliminations, and decide the outcome
exactly once (`teamOutcomeDone`, set under `e.mu`). The events subject is one
ordered stream, so all engines see the same elimination order and reach the
same verdict. When the OTHER team is dead: every member of our team —
including already-eliminated members — transitions with `Won=true`, and each
winning `initialMode == ModePlayer` engine calls the CAS-deduped
`transitionGameToFinished` (idempotent: finished→archived CAS plus an
already-finished check). An `UpdateGameStatus` "finished" is emitted either
way so an eliminated loser's interim message resolves. A defensive draw branch
(both teams read as dead — shouldn't happen on an ordered stream) still makes
SOMEONE finish the game so it archives.

**Line clears (`handleLockIn` teams arm, `consumer.go`).** Score `teamSize ×
lines` (coop scoring within the team); publish
`EventLineClear{PlayerID, Team, Score, LinesCleared}` — teammates fold in the
score AND the line count, keeping every teammate's level/gravity in sync —
followed by `EventShrink{PlayerID, PlayerIdx, Team, TargetTeam: 1−teamIdx,
RowsRemoved: lines}` aimed at the opposing team. `handleGameEvent` branches on
teams for all three event kinds (`events.go` adds `GameEvent.Team` +
`GameEvent.TargetTeam`, `LinesCleared` set on team clears, and
`EngineUpdate.Team`).

**Per-team scoreboard.** On top of the own-team `score`, every engine keeps
`teamScores` and `teamLines` (both `[config.TeamCount]atomic.Int64`): the
clearer folds its score delta and line count into its own team's slots in
`handleLockIn`, and every OTHER engine — teammates, opposing-team players,
eliminated players, and spectators — folds `EventLineClear.Score`/
`LinesCleared` into `teamScores[ev.Team]`/`teamLines[ev.Team]` in
`handleGameEvent` regardless of team. Each fold emits `UpdateTeamStats` (both
teams' scores and levels, per-team level = `game.Level(teamLines[t])`; also
`Engine.TeamScores()`/`TeamLevels()`), and the HUD replaces the single SCORE
stat with live TEAM A / TEAM B stats (own team accented; spectators see each
team's `score · lvl N` inline and no single SCORE/LEVEL stat) — without this,
opposing players and spectators never saw a clearing team's score move (a
spectator's default `teamIdx` 0 folded only team A's clears).

**Shrink application (`applyTeamShrink`) — THE publish-path novelty.** Unlike
competitive's `applyOpponentShrink` (single writer per board → authoritative
NoCAS), every ALIVE member of the target team receives the same `EventShrink`
and races to apply the identical transform to the same subjects
(`ev.TargetTeam == e.teamIdx && getMode() == ModePlayer`; eliminated members
skip it). Two mechanisms make the race safe:

- **Idempotency guard:** `expectedGarbage` (under `e.mu`) accumulates the rows
  owed to this board across all shrink events; the deficit is
  `expectedGarbage − e.playfield.AdversarialRowCount()`. Garbage rows are
  permanent and bottom-anchored, so the count converges monotonically toward
  the target — `deficit <= 0` means the shift (ours or a teammate's) already
  landed and the loop stops.
- **Recompute-on-CAS-failure:** the projection (`ProjectShrinkShared(deficit,
  causerIdx)` → `changedCells`, under `e.mu`) is published WITH per-subject
  CAS (`PublishMoveAtomically`). On `ErrCASFailure` the projection is thrown
  away ENTIRELY: wait on `e.cellUpdated` (capped per-player-offset backoff
  desynchronizing teammates), re-check the guard, and RECOMPUTE from fresh
  state. **Never merge-retry** — a blind merge would republish a stale shift
  after a teammate's shift committed and double-shift the stack; recomputing
  cannot. Any batch built from a stale board necessarily carries a stale
  expectation on at least one garbage-row cell (the winning batch wrote the
  full board width), so CAS rejects it.

Exactly one teammate's batch commits per deficit; the rest converge and stop.
Bounded at 16 attempts; ends with a full-board re-render (the shift touches
most of the board). Because the projection overlays active pieces in place,
the batch never touches piece cells and no spurious lock-in can fire on any
teammate.

### 8.5 `internal/archive`

`playerTeams` maps playerID → team from the roster listing snapshot (the
authoritative source), with `EventGameOver.Team` as the fallback for players
missing from it. The losing team is the one whose members ALL sent
`EventGameOver`; `WinningTeam` is the other index (`-1` if both or neither
read as dead — draw). Every winning-team member gets `Winner: true` (the
eliminated ones included) and every `PlayerResult` carries `Team`; the record
carries `TeamSize`, `WinningTeam`, and the final per-team totals
`TeamScores`/`TeamLevels` (from the archiving engine's converged per-team
scoreboard — rendered in the history line, which previously showed no score
at all for team games). `TotalScore` stays unset for teams
(it remains cooperative-only).

`buildBoardPictures` captures the end-of-game playfield into `record.Boards`
**before** the game stream is deleted: for each board it builds the cell
subjects of the visible region and calls `FetchPlayfieldState` (latest message
per cell), then stores the non-empty cells sparsely as a `BoardPicture` (raw
cell messages, rows renumbered from the first visible row). The board set is
mode-driven — cooperative one shared board (`CoopCellSubject`), competitive one
per player ordered by ID for stable coloring (`CompetitiveCellSubject`), teams
one per team (`TeamCellSubject`) — so the snapshot is complete for every mode.

### 8.6 `internal/nativeui`

- **Lobby:** the create-game form gains a "teams" radio and a per-team count
  editor (the count label flips to "Per team:"); `createGame` converts the
  per-team count to `playerCount = TeamCount × teamSize`. Teams listings show
  per-team join buttons (`gameRowBtns.joinA`/`joinB`, "Join A (n/size)"),
  each hidden when its team is full. The archive line renders teams results
  as "teams · A 🏆 alice, bob · B carol, dave".
- **Game screen:** roster line and legend grouped under TEAM A / TEAM B
  headers (swatch colors stay GLOBAL roster colors); HUD shows
  "Teams · TEAM A/B"; the opponent sidebar is the opposing team's shared
  board labelled "OPPOSING TEAM"; spectators get `spectatorTeamBoards` —
  both teams' boards side by side. The `gameOverBox` shows an interim
  "YOU'RE OUT / Your team plays on" while the game is still in progress
  (driven by `UpdatePlayerEliminated` + the engine's interim `Won=false`
  spectator transition), then "YOUR TEAM WON!" / "YOUR TEAM LOST" once the
  outcome lands. Below the verdict it shows the final score in gold
  (`Score: N (level L)` shared total for co-op, `Your score: N (level L)`
  for competitive, `TEAM A 42 (lvl 3) · TEAM B 17 (lvl 1)` — own team
  first, via the `eng.TeamIdx()` parameter — for teams) above the
  "Back to Lobby" button.
- **Lifecycle:** `joinGame(gameID, team)` threads `lobby.JoinGame`'s
  `JoinResult` into `engine.New(..., res.PlayerIdx, res.Team, res.TeamSlot)`;
  spectate passes `0, 0, 0`.

### 8.7 Tests

- `internal/config/config_test.go` — team subject builders and board-dimension
  helpers (see §1.1 test list).
- `internal/game/teamshrink_test.go` — `ProjectShrinkShared` holds pieces in
  place (no lift, overtaken piece left to crush); `AdversarialRowCount`
  monotonic counting including partially-filled garbage rows.
- `internal/lobby/teamjoin_test.go` — slot assignment in join order,
  `ErrTeamFull`, rejoin idempotence, both-teams-full → `starting`, and
  concurrent joins racing the CAS loop never over-fill a team.
- `internal/engine/teams_test.go` — 2v2 integration against an embedded
  server: a clear's garbage lands on the opposing board EXACTLY once with
  both receivers racing `applyTeamShrink`, and never on the clearing team's
  board; teammates fold the score; ALL four engines (opposing team included)
  converge on the per-team scoreboard (`TeamScores()`) while the opposing
  team's own score stays untouched; an elimination leaves the team playing on,
  the second elimination ends the game with `Won=true` on every winner
  (eliminated winner included) and meta `finished`.

---

## Phase 9 — Release Automation

**File:** `.github/workflows/release.yml`

Tag-driven releases: pushing a tag matching `v*` runs a GitHub Actions workflow that
tests, builds, and publishes — no manual steps beyond `git tag vX.Y.Z && git push origin vX.Y.Z`.

1. **`test` job** (`ubuntu-latest`) — installs the Gio Linux build dependencies
   (X11/Wayland/EGL dev headers) and runs `go test ./...`. The build matrix only
   runs if tests pass.

2. **`build` matrix** — one native runner per OS, because Gio uses cgo against
   platform headers on Linux and macOS (Windows is pure Go, `CGO_ENABLED=0`):
   linux/amd64, linux/arm64 (on `ubuntu-24.04-arm`), darwin/arm64, darwin/amd64,
   windows/amd64, windows/arm64.
   Each target builds with `-trimpath -ldflags "-s -w -X main.version=<tag>"`
   and packages the binary as `jetricks-<tag>-<os>-<arch>.tar.gz` (`.zip` on
   Windows), uploaded as a workflow artifact. linux/arm64 requires the repo to
   be public (GitHub's free arm64 runners are public-repo only).

3. **`release` job** — downloads all artifacts, writes `SHA256SUMS`, and creates
   the GitHub release with `softprops/action-gh-release` using auto-generated
   release notes.

Supporting code change: `cmd/jetricks/main.go` declares `var version = "dev"` and a
`--version` flag that prints it and exits; the workflow stamps the tag into it via
`-X main.version`.

Dependency note: Gio is pinned at **v0.8.0 or later** because v0.7.x depends on
`gioui.org/cpu`, whose cgo type aliases fail to compile on Linux under Go 1.24+
("cannot define new methods on non-local type"). Gio v0.8.0 removed the compute
renderer and with it that dependency. The breakage only manifests on Linux cgo
builds — macOS/Windows compile the stub variant — so it surfaces in CI, not in
local macOS development.

---

## Phase 10 — Connection Picker (login-screen server selection)

Jetricks never connects at startup: the window opens immediately and the ONE
login screen combines name entry with a CONNECT TO chooser (contexts + URL).
`--server`/`--context` only seed the chooser's defaults — `--server` selects
the URL option and replaces the default URL text with its value; `--context`
picks the context option with its pull-down preset to that context (appended
to the list if the lister didn't find it). Quitting the lobby disconnects and
returns to this same screen.

**internal/nats:**
- `contexts.go` — `ListContexts() (names, selected, err)`: enumerates
  `<XDG_CONFIG_HOME|~/.config>/nats/context/*.json` (sorted; skips non-`.json`
  and directories) and reads the selected name from `<parent>/nats/context.txt`
  (reported only if its context file exists). Hand-rolled because
  `orbit.go/natscontext` exposes no lister — path resolution mirrors it.
- `Bootstrap(ctx, cfg)` (`connection.go`) — the single connect+provision path
  (invoked from `doConnectAndLogin`): `ConnectURL` when `cfg.NATSURL` set
  (with a 5 s dial timeout), else context `Connect`; then the three `Ensure*`
  calls, closing `nc` on any post-connect failure.
- `CheckConnection(cfg)` (`connection.go`) — dial, flush-ping RTT, close;
  provisions nothing (backs the "Check connection" button).

**cmd/jetricks/main.go:** never connects — `ListContexts` +
`nativeui.NewWithPicker(cfg, names, selected)`; `runNative(ctx, cancel, a)`;
shutdown calls `App.DrainConn()` (nil-safe) for the app-owned connection.

**internal/nativeui:** `NewWithPicker` (app.go) starts the App disconnected
and seeds the chooser from the flags (precedence: `--server` → URL option
with that value; `--context` → the context option with the pull-down preset
to it, appended if undiscovered; CLI's selected context; else URL with
`DefaultNATSURL` = `nats://demo.nats.io:4222`; the pull-down's `connCtx`
falls back to the first known context). `connSection` (login.go) renders the
chooser (a "Context:" radio + pull-down button `connDropButton` that expands
`connDropList`, a scroll-capped clickable context list; a URL radio/editor
row — the constructor's `SetText` ChangeEvent is swallowed once via
`connURLSeeded` so it can't steal the default; a "LAN mode (embedded NATS
server)" radio row — `connEnum` value "embedded" — followed by an indented
"Port:" digits-only editor `connPortEd` pre-filled with
`config.DefaultEmbeddedPort` = 4222 (editing it auto-selects the option;
seeded-ChangeEvent swallow via `connPortSeeded`), a muted `data in
./jetstream-data` hint, and — while the option is selected — a `Your
server's URL is nats://<lan-ip>:<port>` line built from `App.lanIP`
(resolved once in `NewWithPicker`); and the Check connection row);
`pickerConfig` resolves the choice; `doConnectAndLogin` (lifecycle.go) first `disconnect()`s any
connection left from a previous attempt, then runs `Bootstrap` (15 s cap) off
the UI goroutine — failure lands on `loginErr` for retry, success stores
`a.nc/js/kv` (App-owned; `teardown`/`DrainConn` drain it) and falls into the
normal `doLogin` flow. `quit()` also `disconnect()`s, returning the player to
the same combined screen to pick another server. `doCheckConn` runs
`CheckConnection` and renders `✓ <server> · ping <rtt>` (green, `formatRTT`)
or `✗ <error>` (red) next to the button.

**Embedded server option** ("LAN mode (embedded NATS server)").
`pickerConfig` maps the third radio to `cfg.RunEmbedded` plus the parsed
`cfg.EmbeddedPort` (`pickerPort`, login.go: empty field →
`config.DefaultEmbeddedPort`, otherwise a valid 1–65535 port or a login
error); `doConnectAndLogin` then calls `ensureEmbeddedServer(cfg.EmbeddedPort)`
(lifecycle.go), which starts
`nats.StartEmbeddedServer(config.EmbeddedStoreDir, port)` — an in-process
JetStream-enabled `nats-server` (default account, no auth) on
`0.0.0.0:<port>` storing its data in `./jetstream-data` — records the
shareable `<lan-ip>:<port>` (`nats.LanIP`, an outbound-route probe with an
interface-scan fallback) in `embAddr`, and rewrites the config to
`nats://<lan-ip>:<port>` so the normal Bootstrap path connects through the
same address other players dial. A running server is reused when the
requested port matches (`embeddedPortOrDefault` maps 0 to the default); a
different port shuts it down and restarts it there. NOT loopback: a foreign
nats-server holding a `127.0.0.1:<port>`-specific bind would intercept a
loopback dial even though our `0.0.0.0` bind succeeded (a real setup on NATS
developer machines) — and as belt and braces, after connecting the app
compares `nc.ConnectedServerId()` against the embedded server's `ID()` and
fails the login with a clear "port already in use by another NATS server"
error on a mismatch. `usingEmbedded` marks the CURRENT connection as being to
the embedded server; while it is set the lobby header shows `YOUR SERVER'S
URL IS nats://<ip>:<port> — share this address so others can join you`
(`embeddedAddr`, lobby.go), which is how the host invites other players (they
connect via the NATS URL option) — the login screen previews the same URL
while the option is selected. `disconnect` clears only the mark — the server
itself runs until `teardown` shuts it down, so friends stay connected across
the host's lobby exits and reconnects. `doCheckConn` with the embedded choice
dials nothing and reports `✓ will serve/serving on nats://<addr> · data in
./jetstream-data`. Tests: `internal/nats/embedded_test.go` (the server comes
up JetStream-enabled on a random port, accepts a client and a stream create;
`LanIP` yields a parseable IPv4) and `layout_test.go` subtests for the
embedded radio's `pickerConfig` resolution (default port, custom port,
out-of-range port error, empty-field fallback) and the lobby's YOUR SERVER'S
URL IS line.

**Tests:** `internal/nats/contexts_test.go` (XDG temp-dir lister cases:
missing dir, filtering/sorting, selected, stale selection),
`internal/nats/bootstrap_test.go` (Bootstrap provisions all three resources
against the embedded server; bad URL errors without leaking; CheckConnection
returns a positive RTT and errors on an unroutable URL), and
`layout_test.go` picker render subtests (contexts + selected default, none →
URL default, `--server` seeds the URL field and choice, `--context`
preselects and appends an undiscovered context).

---

## Phase 11 — Per-Game Chat

Lobby chat and per-game chat share the `JETRICKS_LOBBY_CHAT` stream,
distinguished purely by subject: lobby on `jetricks.lobby.chat`, a game's
messages on `jetricks.lobby.chat.game.<gameID>` (`config.GameChatSubject`; the
stream config adds `GameChatSubjectFilter`). Game chat cannot live on the game
stream (MaxMsgsPerSubject: 1 keeps only the latest message).

- **lobby:** `ChatMessage.GameID` (`json:"-"`, derived from the delivery
  subject via `config.GameIDFromChatSubject` — "" = lobby); `runChatConsumer`
  consumes the whole chat stream unfiltered and tags each message;
  `SendGameChat(ctx, gameID, text, spectator)` publishes to the game subject
  (shared `publishChat` helper with `SendChat`).
- **nativeui:** the game screen (player or spectator) shows a chat strip at
  the bottom (`gameChatPanel`): this game's messages plus lobby lines folded
  in — lobby lines prefixed `@lobby` in `colLobby`; spectator messages marked
  `(spec)` (`chatLine`). Typing: players until the game starts, spectators and
  eliminated players always (`canType`); `handleKeys` grabs board focus only
  while `ModePlayer && in_progress`, so the editor can hold focus pre-start
  and the playing player's keys drive the piece afterwards (muted hint shown
  instead of the editor). Messages starting with `@lobby` route to the lobby
  chat (`sendGameChat`). The lobby screen's chat panel filters to
  `GameID == ""`.
- **archive:** archiving a game purges its chat subject from the shared chat
  stream.

**Tests:** `TestGameChatScoping` (lobby: game vs lobby message tagging off the
subject, spectator flag round-trip), `TestGameIDFromChatSubject` (config),
`TestChatLine` + a `game-with-chat` render subtest (nativeui: formatting,
cross-game filtering).

---

## Cross-Cutting Implementation Rules

These rules apply throughout all phases:

1. **No hand-rolled subject strings.** Every subject or stream name is produced by a
   `config.*` builder. No `fmt.Sprintf("jetricks.game.%s...")` anywhere except in
   `config/config.go`.

2. **Goroutine lifecycle.** Every goroutine receives a `ctx context.Context` and must
   exit when `ctx.Done()` is closed. Every goroutine started in a `Start()` method
   must be stopped by the corresponding `Stop()` method or by context cancellation.
   No goroutine leaks.

3. **Channel discipline.** Channels are never closed by the sender. Receivers always
   `select` on both the data channel and `ctx.Done()`. Sends to the `Updates` channel
   are non-blocking (`select { case ch <- v: default: }`) where appropriate to prevent
   a slow UI from blocking the engine or lobby.

4. **CAS is the only concurrency mechanism for shared stream state.** No mutexes
   protecting NATS-backed state. Only `sync.RWMutex` on the in-memory lobby maps
   (which are derived state from NATS, not the source of truth).

5. **Error handling.** Functions that can fail return errors. The entrypoint calls
   `log.Fatal` on startup errors. Running goroutines log errors and continue (or exit
   via context cancellation) — they do not panic.

6. **Cell updates carry a fully-built subject.** The `CellUpdate` struct in
   `internal/nats` carries `Subject string`, so the NATS layer is subject-agnostic
   (it knows nothing about modes or players). The engine builds each subject with the
   mode-appropriate scheme via its `cellSubject(row, col)` helper — cooperative uses
   `config.CoopCellSubject` (shared board, no player token), competitive uses
   `config.CompetitiveCellSubject` (scoped by the moving player's UUID), teams uses
   `config.TeamCellSubject` (shared board scoped by team index, no player token). The
   modes' subject schemes are entirely separate, not parameterisations of one builder.

7. **Test NATS server.** All integration tests use `testutil.StartServer(t)`, not
   a running external NATS instance. Tests are hermetic.

8. **`AllowAtomicPublish` and `AllowDirect` together.** The game stream sets both
   (atomic batch publish for multi-cell moves, direct get for fast
   last-message-per-subject fetches). Verify this combination works on your NATS
   server version as the first integration test you write for `internal/nats`. If
   it fails, check the NATS server version (requires 2.12+). Mind the two server
   limits: an atomic batch holds at most **1000 messages** (default
   `max_batch_size`; the engine's NoCAS path chunks above it) and a multi-last
   direct get returns at most **1024 responses** (413, no pagination;
   `FetchPlayfieldState` chunks at 512 subjects, bounded by `GetLastMsgsUpToSeq`
   for a consistent snapshot). Note: the CAS merge-retry's batched refetch
   (`refetchAndMerge` → `FetchPlayfieldState` → `GetLastMsgsFor`) uses direct
   get, which is a non-consensus read — but the per-game streams are
   **single-replica**, so the only server is the leader and the read is never
   stale; the merge-retry does get a fresh sequence each retry. (On a
   multi-replica game stream direct get could read a follower briefly behind,
   which would warrant a consistent read.)

9. **Cooperative mode uses a single shared playfield.** Cooperative cell subjects
   carry no player token (the `config.CoopCellSubject` scheme) — both players
   share one wide playfield of width `playerCount × StandardWidth` (20 for 2 players).
   `Cell.PlayerIdx` tags which player's active piece each cell belongs to. Each engine
   has ONE playfield and ONE ordered consumer. Pieces can move anywhere on the full
   board. Collision detection (`CanPlaceCoop`) treats the other player's active cells
   as obstacles. Line clears span the full 20-wide rows. Both players share the SAME
   RNG sequence (`rng.New(meta.Seed)` — there is no `seed+1`); their pieces differ
   only because `spawnPiece` offsets each spawn by `playerIdx × StandardWidth`, so
   each player's pieces appear in their own section of the board. Each engine tracks
   its own `pieceIdx` locally (starting at 0). The UI renders the single wide
   playfield directly — no concatenation of separate playfields. Teams mode reuses
   this entire shared-board machinery per team (`sharedBoard()` is true for coop
   AND teams): each team's board is `teamSize × StandardWidth` wide, sections are
   keyed by `teamSlot`, and `Cell.PlayerIdx` carries the GLOBAL roster index. See
   Phase 8.
