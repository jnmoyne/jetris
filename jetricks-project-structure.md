# Jetricks — Go Project Structure

**Version:** 0.1 Draft
**Status:** Design Phase
**Date:** March 2026

> **Gameplay reference:** All gameplay mechanics (cooperative/competitive modes, scoring, gravity, line clears, game lifecycle) are defined in [`jetricks-gameplays.md`](jetricks-gameplays.md). This spec defers to that document for gameplay behavior and focuses on architecture, package structure, and implementation details.

---

## Table of Contents

1. [Repository Layout](#1-repository-layout)
2. [Package Dependency Graph](#2-package-dependency-graph)
3. [cmd/jetricks](#3-cmdjetricks)
4. [internal/config](#4-internalconfig)
5. [Player Identity](#5-player-identity)
6. [internal/nats](#6-internalnats)
7. [internal/rng](#7-internalrng)
8. [internal/game](#8-internalgame)
9. [internal/engine](#9-internalengine)
10. [internal/lobby](#10-internallobby)
11. [internal/cleanup](#11-internalcleanup)
12. [internal/ui](#12-internalui)
13. [Event Channel Contracts](#13-event-channel-contracts)
14. [Bootstrap Sequence](#14-bootstrap-sequence)
15. [Key Interfaces](#15-key-interfaces)
16. [Goroutine Inventory](#16-goroutine-inventory)
17. [orbit.go Module Reference](#17-orbitgo-module-reference)
18. [Testing Strategy](#18-testing-strategy)

---

## 1. Repository Layout

```
jetricks/
├── cmd/
│   └── jetricks/
│       └── main.go
├── internal/
│   ├── config/
│   │   └── config.go
│   ├── nats/
│   │   ├── connection.go
│   │   ├── streams.go
│   │   ├── kv.go
│   │   ├── consumer.go
│   │   ├── publish.go
│   │   ├── fetch.go
│   │   └── subjects.go
│   ├── rng/
│   │   └── rng.go
│   ├── game/
│   │   ├── piece.go
│   │   ├── playfield.go
│   │   ├── rotation.go
│   │   ├── collision.go
│   │   ├── lineclear.go
│   │   └── row.go
│   ├── engine/
│   │   ├── engine.go
│   │   ├── gravity.go
│   │   ├── move.go
│   │   ├── cas.go
│   │   ├── consumer.go
│   │   └── events.go
│   ├── lobby/
│   │   ├── lobby.go
│   │   ├── presence.go
│   │   ├── listing.go
│   │   └── events.go
│   ├── cleanup/
│   │   └── cleanup.go
│   └── ui/
│       ├── server.go
│       ├── sse.go
│       ├── lobby/
│       │   ├── handler.go
│       │   ├── lobby.templ
│       │   └── lobby_templ.go
│       ├── game/
│       │   ├── handler.go
│       │   ├── board.templ
│       │   ├── board_templ.go
│       │   ├── hud.templ
│       │   ├── hud_templ.go
│       │   ├── chat.templ
│       │   └── chat_templ.go
│       └── shared/
│           ├── layout.templ
│           └── layout_templ.go
├── go.mod
└── go.sum
```

---

## 2. Package Dependency Graph

Arrows indicate "depends on". The rule is that `internal/game`, `internal/rng`, and `internal/config` are leaves — they have no internal dependencies. The UI layer depends on engine and lobby but neither engine nor lobby depends on UI. All packages may depend on config.

```
cmd/jetricks
    ├── internal/config
    ├── internal/nats              ← uses: orbit.go/natscontext, orbit.go/jetstreamext
    ├── internal/rng
    ├── internal/game
    ├── internal/engine            ← depends on: nats, game, rng, config
    │                                uses: orbit.go/counters (cooperative score)
    ├── internal/lobby             ← depends on: nats, config
    ├── internal/cleanup           ← depends on: nats, config
    │                                uses: orbit.go/natssysclient (stream inventory)
    └── internal/ui                ← depends on: engine, lobby, config
            ├── internal/ui/lobby
            ├── internal/ui/game
            └── internal/ui/shared

Leaf packages (no internal deps):
    internal/config
    internal/game
    internal/rng

orbit.go modules used:
    orbit.go/natscontext   → internal/nats     (connection via NATS CLI contexts)
    orbit.go/jetstreamext  → internal/nats     (atomic batch publish, GetLastMsgsFor)
    orbit.go/counters      → internal/engine   (cooperative mode shared score CRDT)
    orbit.go/natssysclient → internal/cleanup  (JetStream stream inventory for orphan detection)
```

---

## 3. cmd/jetricks

**File:** `cmd/jetricks/main.go`

The entrypoint. Responsible only for wiring — it constructs all top-level components, injects dependencies, and starts the application. Contains no business logic.

### Responsibilities

- Parse CLI flags into a `config.Config`
- Establish the NATS connection
- Ensure lobby streams and KV exist
- Start the HTTP/UI server (`ui.Server`) — lobby creation is deferred until the player enters their name in the UI
- Open the browser or webview
- Block on OS signal and perform graceful shutdown

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | `""` | NATS context name (as configured with `nats context add`). Empty string uses the currently selected context. |
| `--webview` | `false` | Launch as a webview desktop app instead of opening browser |
| `--port` | `7777` | Local HTTP server port |

The `--context` flag maps directly to `natscontext.Connect(contextName)` from `orbit.go/natscontext`. This means Jetricks shares the same connection configuration — server URL, credentials, TLS certificates, JetStream domain — as the `nats` CLI tool on the same machine. No separate connection config file or credential management is needed. Operators configure contexts once with `nats context add` and both the CLI and Jetricks use them.

### Bootstrap Order

See [Section 14 — Bootstrap Sequence](#14-bootstrap-sequence) for the full ordered startup flow.

---

## 4. internal/config

**File:** `config/config.go`

A single `Config` struct populated at startup and passed read-only to all packages that need it. Also contains all constants and subject/stream name builder functions so that naming is defined in exactly one place.

### Key Types

```go
type Config struct {
    NATSContext string // NATS context name; empty = currently selected context
    Port        int
    Webview     bool
}

type GameMode int

const (
    ModeCooperative GameMode = iota
    ModeCompetitive
)
```

### Constants

```go
const (
    TotalRows       = 32   // max for 4 players: 28 visible + 4 headroom
    HeadroomRows    = 4
    VisibleRows     = 24   // cooperative default: TotalRows - HeadroomRows (not used in competitive)
    VisibleRowStart = 4    // cooperative default: first visible row index (not used in competitive)
    StandardWidth   = 10
)

// CompetitiveVisibleRows returns the number of visible rows for a competitive
// game with the given player count: 24 + playerCount.
func CompetitiveVisibleRows(playerCount int) int {
    return 24 + playerCount
}

// CompetitiveVisibleRowStart returns the first visible row index for a competitive
// game with the given player count: TotalRows - CompetitiveVisibleRows(playerCount).
func CompetitiveVisibleRowStart(playerCount int) int {
    return TotalRows - CompetitiveVisibleRows(playerCount)
}

    LobbyKVBucket         = "JETRICKS_LOBBY"
    LobbyChatStream       = "JETRICKS_LOBBY_CHAT"
    LobbyChatSubject      = "jetricks.lobby.chat"
    LobbyChatMaxAge       = 7 * 24 * time.Hour
    LobbyKVPresenceTTL    = 10 * time.Second
    LobbyKVDeleteMarkerTTL = 60 * time.Second

    ArchiveStream         = "JETRICKS_ARCHIVE"
    ArchiveSubject        = "jetricks.archive"

    PresenceHeartbeat     = 5 * time.Second

    CoopPlayfieldID       = "coop"  // used as playerID token in cooperative row subjects
)
```

### Archive Types

```go
// PlayerResult holds one player's outcome in a completed game.
type PlayerResult struct {
    PlayerID   string `json:"player_id"`
    Score      int    `json:"score"`
    PieceCount int    `json:"piece_count"`
    Winner     bool   `json:"winner"`
}

// ArchiveRecord is the JSON payload published to the JETRICKS_ARCHIVE stream
// when a game finishes. Contains the full game outcome for historical display.
type ArchiveRecord struct {
    GameID      string         `json:"game_id"`
    Mode        GameMode       `json:"mode"`
    PlayerCount int            `json:"player_count"`
    Players     []PlayerResult `json:"players"`
    StartedAt   time.Time      `json:"started_at"`
    FinishedAt  time.Time      `json:"finished_at"`
    TotalScore  int            `json:"total_score,omitempty"` // cooperative mode only
}
```

### Game ID Format

Game IDs are UUID v4 strings with dashes (e.g. `550e8400-e29b-41d4-a716-446655440000`). NATS stream names allow alphanumeric characters plus dashes and underscores, so `JETRICKS_GAME_550e8400-e29b-41d4-a716-446655440000` is a valid stream name. UUIDs are generated by the game creator's client using `github.com/google/uuid`.

### Subject Builders

```go
func GameStream(gameID string) string        // → "JETRICKS_GAME_<id>"
func GameSubjectFilter(gameID string) string // → "jetricks.game.<id>.>"

// RowSubject builds the subject for a playfield row.
//   Cooperative: playerID = CoopPlayfieldID ("coop") → jetricks.game.<id>.player.coop.playfield.row.<n>
//     Both players share a single wide playfield (playerCount × StandardWidth).
//     effectivePlayerID() returns CoopPlayfieldID in cooperative mode.
//   Competitive: playerID = player's UUID → jetricks.game.<id>.player.<pid>.playfield.row.<n>
func RowSubject(gameID string, playerID string, row int) string

func MetaSubject(gameID string) string
func RosterSubject(gameID string, playerID string) string
func EventsSubject(gameID string) string
func CountdownSubject(gameID string) string
func ChatSubject(gameID string) string

// Score subjects differ by mode — see Section 9 (engine/events.go) for detail.
// Cooperative uses a counter subject on the shared stream.
// Competitive uses a per-player CAS subject.
func CoopScoreSubject(gameID string) string
func CompetitiveScoreSubject(gameID string, playerID string) string

func PlayerStateSubject(gameID string, playerID string) string
func LobbyPlayerKey(playerID string) string
func LobbyGameKey(gameID string) string

func ArchiveSubjectStr() string
// → "jetricks.archive"
```

All subject and stream names in the application are produced exclusively through these builders. No package constructs subject strings by hand.

### Stream Configuration Notes

`JETRICKS_GAME_<id>` requires two stream-level flags:
- `AllowAtomicPublish: true` — required for jetstreamext atomic batch move publishing
- `AllowMsgCounter: true` — required for orbit.go/counters cooperative score CRDT

Both flags are set unconditionally on every game stream regardless of mode — the unused flag has no runtime cost and avoids conditional stream configuration. This combination should be verified to work correctly on the target NATS 2.12+ server early in implementation, as it is an uncommon configuration.

### GameMeta Struct

`GameMeta` is the JSON payload published to `jetricks.game.<id>.meta`. All lifecycle transitions are CAS updates to this subject.

```go
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
    // Identity
    GameID      string     `json:"game_id"`
    Mode        GameMode   `json:"mode"`          // "cooperative" or "competitive"
    PlayerCount int        `json:"player_count"`  // max players

    // RNG — shared seed for deterministic piece sequence
    Seed        uint64     `json:"seed"`

    // Lifecycle
    Status      GameStatus `json:"status"`
    CreatorID   string     `json:"creator_id"`    // playerID of the game creator
    CreatedAt   time.Time  `json:"created_at"`
    StartedAt   time.Time  `json:"started_at,omitempty"`
    FinishedAt  time.Time  `json:"finished_at,omitempty"`
    Abandoned   bool       `json:"abandoned,omitempty"`   // true if all players disconnected

    // Piece index — updated on each lock-in by the locking engine.
    // Allows joining/reconnecting engines to recover the current piece
    // position in the shared sequence with a single FetchGameMeta call,
    // without replaying the entire stream to count lock-in events.
    PieceIdx    uint64     `json:"piece_idx"`
}
```

The `PieceIdx` field is the number of pieces that have locked in across the entire game. In cooperative mode each player has their own independent piece sequence and tracks their own `pieceIdx` locally — each player gets their own RNG sequence (creator uses `seed`, joiner uses `seed+1`). `PieceIdx` in meta is not used for cooperative mode piece tracking. In competitive mode each player has an independent piece sequence — `PieceIdx` tracks the piece index for the player whose piece last locked in. Competing engines track their own index locally and only publish to meta when their own piece locks in.

> **Implementation note:** `PieceIdx` in meta is eventually consistent. A joining engine that reads it mid-game will get the last published value, which may lag by at most one piece lock-in. The engine should treat this as a starting lower bound: after applying the FetchPlayfieldState snapshot, it scans the playfield for active piece presence, and if an active piece is visible but its index would correspond to `PieceIdx`, the index is correct. If no active piece is present (lock-in just happened but meta not yet updated), the engine can wait for the ordered consumer to deliver the next row state, which will show the new piece spawning — at that point the implied piece index is `PieceIdx + 1`. This self-corrects without any special handling.

---

## 5. Player Identity

Player identity is handled entirely in the UI at startup — no persistent files are stored on disk. When a player opens Jetricks in their browser, they are prompted to enter a player name. This name **is** the player ID used in all NATS subjects, KV keys, and game rosters. There is no separate display name.

### Validation

Player names are validated to be legal NATS subject tokens:
- Must be 1–32 characters
- Cannot contain `.`, ` ` (space), `*`, `>`, tab, newline, carriage return, or null

Validation is implemented in `config.ValidatePlayerName(name) error`.

### Flow

1. Browser opens → login page is served (no lobby exists yet)
2. Player enters a name → `POST /login` validates the name shape (`config.ValidatePlayerName`) and then checks the lobby KV (`lobby.IsNameInUse`) for an active player presence entry with the same display name (case-insensitive, whitespace-trimmed). Stale presence entries — `LastSeen` older than 3× `config.PresenceHeartbeat` — are ignored so unclean shutdowns don't permanently block the name.
3. If the name collides with an active player, the server returns a confirmation modal ("looks like there is already a user with this name in the lobby, are you sure you want to join with this name?") with **Yes, join** / **Cancel** buttons. **Yes, join** sets the `forceLogin` Datastar signal to `true` and re-posts `/login`, which skips the collision check and proceeds.
4. On success, the lobby is created and the browser redirects to the lobby page.

Since the player name is the player ID, two players choosing the same name share one KV presence key and roster subject — actions taken by either binary in the lobby (e.g. ToggleReady) target whichever entry matches the playerID first. The collision check makes that condition opt-in: the user is told and must confirm before proceeding.

---

## 6. internal/nats

Wraps all NATS/JetStream client operations. Nothing in this package is game-specific — it is purely NATS plumbing. All other packages that need to talk to NATS do so through types defined here.

### Files

#### `connection.go`

```go
// Connect establishes a NATS connection using the named NATS context.
// An empty contextName connects using the currently selected context,
// matching the behaviour of the nats CLI tool.
// The returned Settings carry JSDomain and other context values needed
// to construct the JetStream handle correctly.
func Connect(contextName string, opts ...nats.Option) (*nats.Conn, jetstream.JetStream, natscontext.Settings, error)
```

Uses `natscontext.Connect(contextName, opts...)` from `orbit.go/natscontext`. The returned `Settings` struct includes `JSDomain`, which is passed to `jetstream.NewWithDomain` so that JetStream API calls are correctly scoped when connecting to a multi-domain NATS deployment. All connection config — server URL, credentials, TLS, SOCKS proxy — comes from the context file rather than from CLI flags, eliminating configuration drift between Jetricks and the `nats` CLI.

#### `streams.go`

```go
// EnsureGameStream creates the per-game stream if it does not exist.
// Called when creating a new game.
func EnsureGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// EnsureLobbyChatStream creates the lobby chat stream if it does not exist.
func EnsureLobbyChatStream(ctx context.Context, js jetstream.JetStream) error

// EnsureArchiveStream creates the game archive stream if it does not exist.
func EnsureArchiveStream(ctx context.Context, js jetstream.JetStream) error

// SealGameStream sets Sealed: true on a game stream, permanently preventing writes.
func SealGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// DeleteGameStream deletes a game stream entirely (used for cancelled games).
func DeleteGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// ListGameStreams returns names of all streams matching the JETRICKS_GAME_ prefix.
func ListGameStreams(ctx context.Context, js jetstream.JetStream) ([]string, error)
```

#### `kv.go`

```go
// EnsureLobbyKV creates or retrieves the lobby KV bucket with correct TTL config.
func EnsureLobbyKV(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error)
```

The KV bucket is configured with `DeleteMarkerTTL` set to `config.LobbyKVDeleteMarkerTTL`. Per-key TTL is set on individual Put operations for presence entries.

#### `consumer.go`

```go
type OrderedConsumerConfig struct {
    Stream        string
    FilterSubject string        // optional subject filter
    StartSeq      uint64        // 0 = from beginning
    ReplayOriginal bool         // for archived game replay
}

// NewOrderedConsumer creates an ordered push consumer and returns a channel
// of jetstream.Msg. The consumer is automatically restarted on sequence gaps.
// The returned cancel func tears it down cleanly.
func NewOrderedConsumer(
    ctx context.Context,
    js jetstream.JetStream,
    cfg OrderedConsumerConfig,
) (<-chan jetstream.Msg, context.CancelFunc, error)
```

#### `publish.go`

```go
// RowUpdate represents a single row's new state and the CAS expectation.
type RowUpdate struct {
    Row             int
    PlayerID        string
    Payload         []byte
    ExpectLastSeq   uint64  // Nats-Expected-Last-Subject-Sequence for this row
                            // (per-subject CAS, not stream-level)
}

// PublishMoveAtomically publishes a set of row updates as a SINGLE atomic
// batch with per-subject CAS expectations
// (jetstreamext.WithBatchExpectLastSequencePerSubject, which sets the
// Nats-Expected-Last-Subject-Sequence header). Either every row commits or
// none does. Returns ErrCASFailure if any subject's sequence expectation is
// not met.
//
// Per-subject CAS (not WithBatchExpectLastSequence, which is stream-level)
// is what we want: each row is its own subject, so concurrent writes to
// other rows don't cause spurious rejections.
func PublishMoveAtomically(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    updates []RowUpdate,
) error

// PublishRowsAtomicallyNoCAS publishes a set of row updates as a SINGLE
// atomic batch WITHOUT CAS expectations. Used for authoritative state
// transitions (lock, hard-drop landing, line-clear, shrink) where the
// publisher's view is the new ground truth.
func PublishRowsAtomicallyNoCAS(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    updates []RowUpdate,
) error

// PublishMeta publishes a game metadata update with a global CAS expectation.
func PublishMeta(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    payload []byte,
    expectLastSeq uint64,
) error

var ErrCASFailure = errors.New("CAS sequence expectation not met")
```

#### `subjects.go`

Re-exports the subject builder functions from `config` as a convenience. Internal packages may use either; the builders always delegate to `config`.

#### `fetch.go`

```go
// FetchPlayfieldState retrieves the current state of all 28 row subjects
// for a game in a single round trip using jetstreamext.GetLastMsgsFor.
// Used by the engine on startup and reconnect to reconstruct the full
// playfield instantly without replaying the entire game stream history.
//
// playerID is CoopPlayfieldID ("coop") in cooperative mode or the
// player's own UUID in competitive mode.
func FetchPlayfieldState(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    playerID string,
) ([]PlayfieldRowMsg, error)

type PlayfieldRowMsg struct {
    Row     int
    Payload []byte
    Seq     uint64
}

// FetchGameMeta retrieves the latest game metadata message directly.
// Returns the decoded GameMeta, the stream sequence of the message
// (used as ExpectLastSeq for the next CAS update to meta), and any error.
func FetchGameMeta(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
) (config.GameMeta, uint64, error)
```

`FetchPlayfieldState` calls `jetstreamext.GetLastMsgsFor(ctx, js, streamName, rowSubjects)` where `rowSubjects` is the list of all 28 row subjects for the game, constructed using `config.RowSubject(gameID, playerID, n)` for n in 0..27. The `playerID` parameter is `CoopPlayfieldID` in cooperative mode or the player's own UUID in competitive mode. This returns the last message per subject in a single server round trip — far more efficient than replaying the entire stream from sequence 0 on join or reconnect. The engine uses this for its initial playfield snapshot before starting the ordered consumer, then the consumer takes over for live updates from that point forward.

---

## 7. internal/rng

**File:** `rng/rng.go`

Deterministic, seekable piece sequence generation. Uses Go's `math/rand/v2` with a PCG source. In competitive mode, all players initialise their RNG from the same seed (stored in game metadata) and independently produce the identical piece sequence. In cooperative mode, each player gets their own independent RNG sequence — the game creator uses `seed` and the joiner uses `seed+1` — so both players receive different pieces simultaneously.

### Key Types

```go
type Sequence struct {
    src *rand.PCG
}

// New creates a Sequence from the given seed.
func New(seed uint64) *Sequence

// Piece returns the piece type at position index in the sequence.
// Seeking directly to index means any piece can be retrieved without
// replaying all prior calls — safe for reconnect and state reconstruction.
func (s *Sequence) Piece(index uint64) game.PieceType

// PieceCount computes the current piece index from the number of
// lock-in events in the game stream. Used on startup and reconnect.
func PieceCount(lockInEvents int) uint64
```

The piece type distribution follows a standard bag randomiser (7-bag): within each group of 7 pieces all 7 types appear exactly once in a random order. The bag boundaries are derived from the seed deterministically, so all players always see the same bags.

---

## 8. internal/game

Pure game logic. No NATS, no IO, no goroutines. Fully unit-testable in isolation. This package defines the core data types that the rest of the application builds on.

### Files

#### `piece.go`

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
    Orientation int    // 0-3, clockwise rotations from spawn orientation
    Row         int    // top-left anchor row (0-indexed, 0 = headroom top)
    Col         int    // top-left anchor column
}

// Cells returns the (row, col) pairs occupied by this piece in its
// current position and orientation.
func (p Piece) Cells() [][2]int
```

#### `rotation.go`

Implements the Super Rotation System (SRS). Provides the wall kick tables for each piece type and rotation transition.

```go
// Rotate returns the piece after applying a clockwise or counter-clockwise
// rotation, including SRS wall kick offsets tried in order.
// Returns (rotated piece, true) if any kick position is valid,
// or (original piece, false) if all kick positions are blocked.
func Rotate(p Piece, clockwise bool, pf *Playfield) (Piece, bool)
```

#### `playfield.go`

```go
// Playfield is the in-memory representation of the game board.
// It is the client-side replica maintained by the ordered consumer.
type Playfield struct {
    Width  int
    Rows   [TotalRows]Row
    // LastSeq tracks the stream sequence of the last message received
    // for each row subject — used for CAS expectations.
    LastSeq [TotalRows]uint64
}

func NewPlayfield(width int) *Playfield

// Apply updates the playfield from a decoded row message received from NATS.
// Updates both the cell data and the LastSeq for that row.
func (pf *Playfield) Apply(rowIndex int, row Row, seq uint64)

// ActivePiece returns the currently falling piece extracted from the row data,
// or nil if no active piece is present.
func (pf *Playfield) ActivePiece() *Piece

// ActivePieceForPlayer returns the active piece belonging to the given playerIdx
// (matching Cell.PlayerIdx). Used in cooperative mode where two players' active
// pieces coexist on the same shared playfield. Returns nil if no active piece
// with that playerIdx is present.
func (pf *Playfield) ActivePieceForPlayer(playerIdx int) *Piece

// SetActivePieceForPlayer / ClearActiveCellsForPlayer / LockActivePieceForPlayer
// mutate the playfield in place. They are retained for unit-test setup only;
// the engine never calls them. See "Invariant: NATS as single source of
// truth for the playfield" in section 9.
func (pf *Playfield) SetActivePieceForPlayer(p Piece, playerIdx int)
func (pf *Playfield) ClearActiveCellsForPlayer(playerIdx int)
func (pf *Playfield) LockActivePieceForPlayer(playerIdx int)

// Snapshot returns a copy of the LastSeq array at the current moment,
// for use as CAS expectations in an upcoming publish batch.
func (pf *Playfield) Snapshot() [TotalRows]uint64

// Projection helpers — compute the row payloads that the engine should
// publish to NATS WITHOUT mutating pf. The engine never mutates pf; the
// consumer applies the published rows on echo via Apply().
func (pf *Playfield) ProjectMove(affectedRows []int, newPiece *Piece, playerIdx int) map[int]Row
func (pf *Playfield) ProjectLock(affectedRows []int, playerIdx int) map[int]Row
func (pf *Playfield) ProjectHardDrop(affectedRows []int, dest Piece, playerIdx int, lockOnLand bool) map[int]Row
func (pf *Playfield) ProjectClearRows(completed []int, shiftAnchors bool) []Row
func (pf *Playfield) ProjectShrink(rowsToAdd int, causerIdx int) []Row
```

#### `row.go`

Defines the `Row` type and its serialisation to/from the bytes stored in NATS messages. Row payloads are encoded as **JSON** — straightforward to debug with the `nats` CLI and sufficient for the update rate.

```go
// Cell represents a single cell in a row.
type Cell struct {
    Occupied    bool      `json:"o,omitempty"`  // true if a locked piece occupies this cell
    PieceType   PieceType `json:"t,omitempty"`  // type of the locked piece, if Occupied
    Active      bool      `json:"a,omitempty"`  // true if this cell is part of the falling piece
    Orientation int       `json:"r,omitempty"`  // rotation of the active piece (0-3)
    AnchorRow   int       `json:"ar,omitempty"` // anchor row of the active piece
    AnchorCol   int       `json:"ac,omitempty"` // anchor col of the active piece
    PlayerIdx   int       `json:"pi,omitempty"` // which player's active piece this cell belongs to (cooperative mode)
    Adversarial     bool      `json:"g,omitempty"`  // permanent adversarial cell (competitive shrink); row can never be completed
}

type Row struct {
    Cells []Cell `json:"cells"`
}

func (r Row) Marshal() ([]byte, error)
func UnmarshalRow(data []byte) (Row, error)
```

Piece position and orientation are encoded in the `Active`/`Orientation`/`AnchorRow`/`AnchorCol` fields of every cell the piece occupies. All occupied cells of the same active piece carry identical anchor and orientation values, making the full piece reconstructable from any single active cell. This redundancy is intentional — it allows the engine to reconstruct the active piece from any partial row read without needing to scan all rows first.

#### Lock-in implicit detection

There is no explicit lock-in event message. Instead the engine detects lock-in by observing the transition in row data delivered by the ordered consumer: a set of cells that were `Active: true` in the previous row state become `Active: false, Occupied: true` in the new state, and no `Active: true` cells appear anywhere in the playfield. When the engine detects this transition it:

1. Increments `pieceIdx`.
2. Publishes an updated `GameMeta` with `PieceIdx = pieceIdx` to `jetricks.game.<id>.meta` — this is a CAS update using the current meta sequence. If the CAS fails (the other player's engine raced to publish first for the same lock-in), the engine reads the new meta value; since both engines increment by 1 from the same base, the value is idempotent and the race winner's value is correct.
3. Calls `rng.Sequence.Piece(pieceIdx)` to determine the next piece type and spawns it at the top of the playfield.

This makes `PieceIdx` in `GameMeta` eventually consistent: any engine joining mid-game via `FetchGameMeta` gets the current piece count in one round trip.

The same lock-in transition also triggers the **completed-line check** (`CompletedRows` → clear). Because the ordered consumer applies a publish batch **one row at a time** and the lock-in (and thus the completion check) fires the instant the player's last `Active` cell disappears, batch **row order matters for hard drops**. A hard drop teleports the piece, so its batch both clears the old (higher-up, lower-index) active cells *and* sets the new locked cells lower down. If the vacated rows were applied first, lock-in would fire before the landing rows were applied and a line completed by the drop would be missed until the next piece locked. Hard-drop batches are therefore published **bottom row first** (the `bottomFirst`/`applyBottomFirst` flag on the publish helpers) so the completed row is in place when lock-in fires.

The same bottom-first ordering is used for **every downward move** (gravity tick and soft drop), for a related reason: a piece that occupies a **single row** — the horizontal I — relocates by clearing its old row and setting its new row in separate messages. If the old row is applied first, the consumer briefly sees the player with **zero** active cells and fires a *spurious* lock-in, replacing the I with the next piece before any input. Applying the lower (new) row first keeps at least one active cell present throughout the move. Multi-row pieces overlap a row when moving down, so they never vanish; left/right moves stay in one row (one message); an in-place lock converts active→occupied in place — none of those need ordering, and bottom-first is harmless for them.

> In **coop**, lock, hard-drop, and line-clear go through `publishProjectedRowsWithMergeRetry` (CAS + refetch-overlay-retry), not a plain NoCAS write — see §`internal/engine` and the publish table in the implementation plan. A NoCAS write replaces the whole shared row including the *other* player's active cells from a possibly-stale snapshot, which corrupts their mid-flight piece; CAS+merge preserves the other player's cells. The `bottomFirst` flag is threaded through that merge path for coop hard drops.

#### `collision.go`

```go
// CanPlace returns true if the piece can occupy its current position
// without conflicting with locked cells or going out of bounds.
func CanPlace(p Piece, pf *Playfield) bool

// CanPlaceCoop returns true if the piece can occupy its current position
// without conflicting with locked cells, out-of-bounds, OR the other player's
// active cells. In cooperative mode the other player's active piece cells are
// treated as obstacles (in addition to locked cells).
func CanPlaceCoop(p Piece, pf *Playfield, ownPlayerIdx int) bool

// WouldCollide returns true if applying the given move delta (dRow, dCol)
// to the piece would result in a collision or out-of-bounds position.
func WouldCollide(p Piece, pf *Playfield, dRow, dCol int) bool

// HardDropDestination computes the lowest valid row the piece can occupy
// given the current playfield state, without modifying the playfield.
// The returned Piece has the same Type, Orientation, and Col as the input
// but with Row set to the lowest non-colliding position.
// Used by PublishHardDrop to build the destination row updates for CAS publish.
func HardDropDestination(p Piece, pf *Playfield) Piece
```

#### `lineclear.go`

```go
// CompletedRows returns the indices of rows that are fully occupied
// by locked (non-active) cells.
func CompletedRows(pf *Playfield) []int

// ScoreDelta returns the score awarded for clearing n lines.
// See jetricks-gameplays.md for mode-specific scoring rules.
// Cooperative: playerCount per line cleared.
// Competitive: simple line count (score = linesCleared).
func ScoreDelta(linesCleared int, level int) int

// Level returns the current level derived from total lines cleared.
// Used only in cooperative mode.
func Level(totalLinesCleared int) int

// GravityInterval returns the gravity tick duration for the given level.
// Used only in cooperative mode. Fixed at the base interval for level 0,
// decreasing according to the standard Tetris speed curve.
func GravityInterval(level int) time.Duration
```

---

## 9. internal/engine

The active game session. This is where NATS and game logic meet. One `Engine` instance is created per game the local player is participating in (as a player or spectator). The engine owns the ordered consumer for the game stream and drives all game state transitions.

### Invariant: NATS as single source of truth for the playfield

The in-memory `*game.Playfield` held by the engine is a **read-only replica** for everyone except the row consumer (`runConsumer` in `consumer.go`). Specifically:

- **The only place `e.playfield` is mutated is `pf.Apply(rowIdx, row, seq)` inside the row consumer.** That call is invoked when an ordered-consumer message for one of this engine's row subjects is delivered.
- **No game action mutates the playfield directly** — not the local player's moves, not hard drops, not piece locks, not line clears, not opponent shrinks, not piece spawns. Each action computes the *projected* row payloads using the helpers in `internal/game/playfield.go` (`ProjectMove`, `ProjectLock`, `ProjectHardDrop`, `ProjectClearRows`, `ProjectShrink`) and publishes them. The consumer then applies those rows when it receives the echo, and the UI re-renders from the updated `e.playfield`.
- **The UI renders only from `e.playfield`.** It never sees pre-publish state.

This eliminates two-way drift between the local replica and the stream: every player on every machine sees the playfield evolve in the same order it was committed to JetStream. The price is that there is a NATS round-trip between input and visual feedback, and that two rapid inputs may both validate against the same pre-echo state — the second is dropped via CAS rejection (per-subject `ExpectLastSequencePerSubject`), surfaced as a CAS-flash event for visual feedback.

The legacy in-place mutators on `Playfield` (`SetActivePieceForPlayer`, `LockActivePieceForPlayer`, `ClearActiveCellsForPlayer`, `ClearRows`) are retained only for unit-test setup of `internal/game`. They must not be called from `internal/engine`.

### Atomic batches with per-subject CAS

Every publication of multiple rows from the engine is a SINGLE atomic batch:

- `natspkg.PublishMoveAtomically` — multi-row batch with **per-subject CAS** expectations (`Nats-Expected-Last-Subject-Sequence`, applied via `jetstreamext.WithBatchExpectLastSequencePerSubject(seq)`). Used for moves, rotations, and spawns.
- `natspkg.PublishRowsAtomicallyNoCAS` — multi-row batch without CAS. Used for authoritative state transitions (piece lock, hard-drop landing, line-clear, opponent-shrink application).

Why per-subject CAS, not stream-level (`WithBatchExpectLastSequence`)? Each row is its own NATS subject. Per-subject CAS rejects only when *our* row was overwritten since we last saw it; concurrent writes to *other* rows don't conflict. This is essential in cooperative mode where two players write the same shared playfield, and useful in competitive mode for parallelism between meta/event publishes and row publishes.

Why atomic batch, not row-by-row? A single move typically touches 2+ rows (the row the piece is leaving and the row(s) it is entering). If those messages arrived at consumers one at a time, every other player would briefly observe a half-erased / half-placed piece between consumer applies. Atomic batch makes the multi-row update visible to consumers as one indivisible step.

The expected-last-sequence value for each row comes from `e.playfield.LastSeq[r]`, which is updated only by the row consumer's `pf.Apply(rowIdx, row, seq)` call when an ordered-consumer message is delivered. Because the consumer is the only writer to `LastSeq`, the CAS expectation always reflects what we have actually observed from the stream — not optimistic local edits.

CAS-failure handling for **player moves** (same in both modes): **drop the move, no retry, no NATS publish**. The engine emits an `UpdateCASFlash` directly on its local `Updates` channel; the player must retry the input themselves.

In cooperative mode the shared playfield has two writers, so CAS rejections on moves are an expected, regular occurrence. A silent server-side retry would mask the conflict and make the player's own input timing feel non-deterministic. Instead we surface the failure loudly: the UI renders the `UpdateCASFlash` as a **rainbow outline flash on the player's own piece** — cells in `FlashCells` cycle through the seven spectrum colors over roughly 600 ms with a matching glow, then revert. The other players see nothing, since one player's input rejection is information of no use to anyone else.

CAS-failure handling for **engine-driven (internal) writes** — piece spawn and gravity ticks. The player did not press a key for either, so a flash would be misleading; and both share row subjects with the other player in coop mode. Both **must** succeed: a dropped spawn would leave the player pieceless, and a dropped gravity tick would make the piece appear frozen for one tick interval. In coop mode both go through `publishProjectedRowsWithMergeRetry`: on CAS failure, refetch each affected row from the stream via `stream.GetLastMsgForSubject`, overlay this player's cells on top, retry the batch with refreshed per-subject CAS expectations (up to 5 retries). In competitive mode neither can race (each player owns their subjects), so both go through the regular `publishProjectedRows(flashOnFailure=false)`.

The rainbow flash fires **only** for player-initiated moves. The `internal` boolean threaded through `attemptMove` / `attemptMoveStandard` / `attemptMoveCoop` distinguishes the source: `runMoves` (the moves channel — player input) calls `attemptMove(move, false)`, while `runGravity` calls `attemptMove(MoveDown, true)`. The `flashOnFailure` parameter on `publishProjectedRows` is the same flag passed downstream — `internal` true means flash false, and vice versa.

### Files

#### `engine.go`

```go
type Mode int

const (
    ModePlayer    Mode = iota  // local player is actively playing
    ModeSpectator              // watching only — no move input, no gravity, no controls
    ModeGameOver               // any player has topped out; game ends for all
)

type Engine struct {
    gameID    string
    playerID  string
    gameMode  config.GameMode  // cooperative or competitive
    mode      Mode
    playerIdx int              // 0 for creator, 1 for joiner; used in cooperative mode for Cell.PlayerIdx

    // visibleRowStart is the first visible row index in the playfield.
    // Cooperative: config.VisibleRowStart (4).
    // Competitive: config.CompetitiveVisibleRowStart(playerCount) — varies with player count.
    visibleRowStart int

    // Own playfield — always present.
    // In cooperative mode this is the single shared playfield of width
    // playerCount × StandardWidth (e.g. 20 for 2 players). Both players'
    // active pieces coexist here, distinguished by Cell.PlayerIdx.
    playfield *game.Playfield

    // Opponent playfields — non-nil in competitive mode only.
    // One per opponent, maintained by ordered consumers on each opponent's row subjects;
    // rendered as compact sidebar views.
    // In cooperative mode this is nil — there is only one shared playfield.
    opponentPlayfields map[string]*game.Playfield // keyed by opponent playerID

    seq      *rng.Sequence
    pieceIdx uint64

    // Channels for outbound events to the UI layer
    Updates chan EngineUpdate

    // internal
    js       jetstream.JetStream
    cancelFn context.CancelFunc
}

func New(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    playerID string,
    opponentPlayerIDs []string // empty for spectator/coop; set in competitive mode (all opponents)
    gameMode config.GameMode,
    mode Mode,
    playerCount int,
    seed uint64,
    initialPieceIdx uint64,
) (*Engine, error)

// Start begins all consumer goroutines and (if ModePlayer) the gravity ticker.
// In cooperative mode, starts ONE ordered consumer on the shared row subjects
// (using CoopPlayfieldID). In competitive mode, starts 1 + len(opponentPlayerIDs)
// ordered consumers — one for own rows and one per opponent's rows.
func (e *Engine) Start() error

// Stop tears down all goroutines cleanly.
func (e *Engine) Stop()

// Move input — only processed when mode == ModePlayer.
// Each call is non-blocking; the move is dispatched to the internal move channel.
func (e *Engine) MoveLeft()
func (e *Engine) MoveRight()
func (e *Engine) MoveDown()   // soft drop
func (e *Engine) RotateCW()
func (e *Engine) RotateCCW()
func (e *Engine) HardDrop()   // auto-retries until piece lands — see cas.go

// transitionToSpectator is called internally when the local player tops out.
// Stops the gravity ticker and move processing, keeps the consumers running.
func (e *Engine) transitionToSpectator()
```

#### `consumer.go`

Manages the ordered consumer goroutine(s). In cooperative mode, ONE consumer runs on the shared row subjects (using `CoopPlayfieldID` as the player token), updating the single shared `Playfield`. In competitive mode, 1 + N consumers run — one for the local player's rows and one per opponent — each updating a separate `Playfield` instance.

```go
func (e *Engine) runConsumer(ctx context.Context, pf *game.Playfield, filterSubject string)
```

**Startup sequence:**

1. Call `nats.FetchGameMeta(gameID)` — returns `GameMeta` including `Seed`, `PieceIdx`, and `Status`. In competitive mode, initialise `e.seq = rng.New(meta.Seed)` and `e.pieceIdx = meta.PieceIdx`. In cooperative mode, initialise `e.seq = rng.New(meta.Seed)` for the creator or `e.seq = rng.New(meta.Seed + 1)` for the joiner; each player tracks their own `pieceIdx` independently. Set `e.playerIdx` to 0 for the creator or 1 for the joiner.
2. Call `nats.FetchPlayfieldState(gameID, playerToken)` — where `playerToken` is `CoopPlayfieldID` in cooperative mode or the player's own UUID in competitive mode. Returns last message per row subject with stream sequences. Apply all rows to `e.playfield` via `pf.Apply`. Record `maxSeq = max(all row sequences)`.
3. Start the ordered consumer with `StartSeq: maxSeq + 1`. In cooperative mode this is ONE consumer on the shared row subjects (`jetricks.game.<id>.player.coop.playfield.row.>`). In competitive mode this is the consumer for the player's own rows. Messages on non-row subjects (events, meta, chat) that arrived between the lowest and highest fetched row sequence are a tolerable gap — at most a few milliseconds of game time.
4. In competitive mode only, repeat steps 2–3 for each opponent's rows using `nats.FetchPlayfieldState(gameID, opponentPlayerID)`, starting one consumer goroutine per opponent targeting `jetricks.game.<id>.player.<opponentPID>.playfield.row.>`. In a 4-player game this means 3 opponent consumers plus the player's own consumer. In cooperative mode there is no opponent consumer — both players write to and read from the same shared row subjects.

**Cooperative mode design:**

In cooperative mode both players share a SINGLE wide playfield of width `playerCount × StandardWidth` (20 columns for 2 players). Row subjects use `CoopPlayfieldID = "coop"` as the player token (e.g. `jetricks.game.<id>.player.coop.playfield.row.<n>`), and `effectivePlayerID()` returns `CoopPlayfieldID` in cooperative mode. Both players' active pieces exist on the same playfield and can move anywhere on it — they are not restricted to their own section. Each cell of an active piece is tagged with `Cell.PlayerIdx` (0 for creator, 1 for joiner) so the engine can distinguish which player's piece each cell belongs to.

Each player spawns their piece centered in their section (player 0: center of cols 0–9, player 1: center of cols 10–19) but can move it anywhere on the full-width board. `ActivePieceForPlayer(playerIdx)` finds only the piece belonging to that player (by matching `Cell.PlayerIdx`). `SetActivePieceForPlayer(p, playerIdx)` only clears active cells with matching `PlayerIdx` before setting new ones. `LockActivePieceForPlayer(playerIdx)` only locks cells belonging to that player. Collision detection (`CanPlaceCoop`) treats the other player's active cells as obstacles in addition to locked cells.

Each player has their own independent RNG sequence (creator uses `seed`, joiner uses `seed+1`) and tracks their own `pieceIdx` independently. Each engine has ONE playfield (the shared one) and ONE ordered consumer (on the shared row subjects) — no separate opponent playfield is needed. Both players write to the same shared row subjects. CAS conflicts on **moves** (left, right, down, rotate) are NOT retried — the move is simply dropped and the player must try another move. CAS conflicts on **state changes** (lock-in, spawn, line clear) ARE retried with a direct fetch from the stream, since these must succeed for game consistency.

Line clears work on the full 20-wide rows. The score is shared — both players' line clears contribute to the same score total. The UI renders the single wide playfield directly (no concatenation of two separate playfields).

**Per-message handling (cooperative mode — single shared playfield consumer):**

- Decodes the row message and calls `pf.Apply(rowIndex, row, seq)`, updating both cell data and `LastSeq` for that row.
- After every row update, scans for the **implicit lock-in signal** for this player's piece: if the previous state had an active piece for this `playerIdx` (`ActivePieceForPlayer(playerIdx) != nil`) and the new state has no active cells with matching `PlayerIdx`, a lock-in has just been committed by this player. The engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to spawn the next piece centered in this player's section.
- Signals the CAS notification channel (see `cas.go`) that the row state has been updated, unblocking any pending CAS retry evaluation.
- On line-clear detection: checks the full-width playfield for completed rows. **Critically, the cleared rows are published synchronously before spawning the next piece** — this prevents a race condition where the spawn modifies the playfield while the clear is still being published. The score is updated and emitted to the UI. Level is recomputed and the gravity interval adjusted.
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

**Per-message handling (competitive mode — own and opponent playfield consumers):**

- Decodes the row message and calls `pf.Apply(rowIndex, row, seq)`, updating both cell data and `LastSeq` for that row.
- After every row update on the own playfield, scans for the **implicit lock-in signal**: if the previous state had an active piece and the new state has no active cells anywhere, a lock-in has just been committed. The engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to determine the next piece.
- Signals the CAS notification channel (see `cas.go`) that the row state has been updated, unblocking any pending CAS retry evaluation.
- On receiving a message on the events subject: if it is a shrink event from another player (`ev.PlayerID != e.playerID`), calls `applyOpponentShrink` which publishes the row shift batch to the local player's own rows. In 3+ player games, every opponent applies the same shrink independently.
- On line-clear detection: checks own playfield for completed rows. Cleared rows are published synchronously before spawning the next piece.
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

#### `gravity.go`

```go
func (e *Engine) runGravity(ctx context.Context)
```

Runs on a dedicated goroutine. Fires a tick at the current gravity interval. On each tick, attempts to drop the active piece one row via `attemptMove`. In cooperative mode, reads the current level from the playfield state after each tick and adjusts the ticker interval if the level has changed. In competitive mode, the interval is fixed.

**Cooperative gravity and lock-in:** When gravity cannot move a piece down, the engine distinguishes between two cases: (1) the piece is blocked by locked cells or out-of-bounds — the piece locks immediately, as in standard Tetris; (2) the piece is blocked only by the other player's active piece — the piece does NOT lock, since that obstacle is temporary (it will itself fall on its next gravity tick). In case (2), gravity simply waits and tries again on the next tick. This prevents premature lock-ins caused by two pieces passing through the same rows.

**Cooperative hard drop:** When a player hard-drops (space bar), the piece falls instantly to the lowest valid position — which may be on top of the other player's active piece. If the piece lands on locked cells or the floor, it locks immediately as usual. If it lands on the other player's active piece, it does NOT lock — instead it stays active and resumes falling by gravity. The other player's piece will itself fall on its next gravity tick, at which point gravity will continue dropping this piece further.

#### `move.go`

```go
// attemptMove is the central move dispatch function.
// It validates the move geometrically against the local playfield,
// constructs the row updates, and calls cas.Publish.
// If the move is a gravity tick that results in lock-in, it calls lockIn.
func (e *Engine) attemptMove(ctx context.Context, move MoveType) error

type MoveType int

const (
    MoveLeft MoveType = iota
    MoveRight
    MoveDown    // used by gravity ticker and soft-drop key
    RotateCW
    RotateCCW
    HardDrop
)
```

#### `cas.go`

```go
// Publish attempts the atomic batch CAS publish for a set of row updates.
// On ErrCASFailure, it waits briefly for the ordered consumer to deliver
// the conflicting update (which updates LastSeq), then re-evaluates:
//   - If the move is still geometrically valid in the updated playfield: retry once.
//   - If the move would collide: return ErrMoveDropped.
//   - If gravity and the piece cannot move down: return ErrLockIn.
func (e *Engine) Publish(ctx context.Context, updates []natspkg.RowUpdate, move MoveType) error

// PublishHardDrop is like Publish but with automatic retry semantics.
// Hard drop always has a valid destination (the lowest empty row the piece fits),
// so on CAS failure the engine recomputes the destination from the updated playfield
// and retries immediately. This continues until the publish succeeds.
// The destination can change between attempts if another client modified the rows,
// but a valid destination always exists as long as the game is not over.
func (e *Engine) PublishHardDrop(ctx context.Context) error

var (
    ErrMoveDropped = errors.New("move dropped: collision in updated state")
    ErrLockIn      = errors.New("piece locked in: gravity blocked in updated state")
)
```

The brief wait for the consumer to deliver the conflicting update is implemented as a short `select` with a timeout (e.g. 50ms) on a notification channel that the consumer goroutine signals whenever it applies a new row update. This avoids a fixed sleep while ensuring the local view is fresh before re-evaluation.

`PublishHardDrop` reuses the same notification channel mechanism — on each CAS failure it waits for the consumer to update the local view, recomputes the ghost piece destination from scratch using `game.HardDropDestination`, builds fresh row updates, and retries. Since the piece always has a valid destination while the game is live, this loop always terminates.

#### `events.go`

Defines the `EngineUpdate` type sent from engine to UI over the `Updates` channel, and the event message format published to `jetricks.game.<id>.events`.

```go
type UpdateKind int

const (
    UpdatePlayfield      UpdateKind = iota  // one or more rows changed
    UpdatePieceLocked                       // active piece locked in
    UpdateLineClear                         // lines cleared, rows shifted
    UpdateGameOver                          // any player topped out; game ends for all
    UpdateOpponentField                     // competitive: opponent's field changed (live view)
    UpdateOpponentShrink                    // competitive: opponent's field shrank (our line clear)
    UpdateScore                             // score changed
    UpdateLevel                             // cooperative: level changed
    UpdateGameStatus                        // game lifecycle status changed
)

type EngineUpdate struct {
    Kind         UpdateKind
    ChangedRows  []int   // for UpdatePlayfield, UpdateLineClear, UpdateOpponentField
    OpponentID   string  // for UpdateOpponentField, UpdateOpponentShrink: which opponent
    Score        int     // for UpdateScore
    Level        int     // for UpdateLevel
    GameStatus   string  // for UpdateGameStatus
}
```

**Game events published to `jetricks.game.<id>.events`:**

```go
// EventKind identifies the type of game event.
type EventKind string

const (
    EventLineClear     EventKind = "line_clear"
    EventShrink        EventKind = "shrink"
    EventGameOver      EventKind = "game_over"
)

// GameEvent is the JSON payload published to the events subject.
type GameEvent struct {
    Kind          EventKind `json:"kind"`
    PlayerID      string    `json:"player_id"`           // who caused/detected the event
    LinesCleared  int       `json:"lines_cleared,omitempty"` // for EventLineClear
    RowsRemoved   int       `json:"rows_removed,omitempty"`  // for EventShrink: how many rows
    Score         int       `json:"score,omitempty"`         // for EventGameOver: player's final score
    PieceCount    int       `json:"piece_count,omitempty"`   // for EventGameOver: total pieces placed
}
```

**Shrink flow (competitive mode):**

1. Player A's engine detects a line clear after a lock-in (implicit detection from row state).
2. Player A publishes an atomic batch: the row shift on its own playfield rows (cleared lines removed, rows above shifted down).
3. Player A also publishes a `GameEvent{Kind: EventShrink, PlayerID: playerA, RowsRemoved: n}` to the events subject. There is no `TargetPlayer` field — ALL other players apply the shrink.
4. Every other player's events consumer reads the shrink event. Since `ev.PlayerID != e.playerID`, each opponent calls `applyOpponentShrink(n)` which shifts their own playfield up by n rows and adds n fully occupied permanent adversarial rows at the bottom. In a 3+ player game, all opponents are shrunk simultaneously. Adversarial cells are marked with `Cell.Adversarial = true` and rendered with a distinct grey color. Adversarial rows can never be completed or cleared — `IsFull()` returns false for any row containing adversarial cells.
5. The shifted state is published using NoCAS (authoritative, same as line clears) to prevent stale consumer messages from undoing the shift. If the shift pushes the active piece out of bounds, it triggers a top-out. See `jetricks-gameplays.md` for the full competitive shrink rules.

**Score tracking:**

In **cooperative mode**, each player's line clears independently update the local score. The score is `playerCount` per line cleared — reflecting the harder-to-fill wider playfield. Both players' scores represent the combined team total. See `jetricks-gameplays.md` for the authoritative scoring rules.

**Line clear publishing:** Cleared rows are published using a no-CAS publish (the cleared state is authoritative). This prevents the CAS retry merge logic from restoring old occupied cells from stale NATS data, which would effectively undo the clear. After the cleared rows are published, `LastSeq` is updated from the publish acknowledgment so subsequent CAS publishes use the correct sequence.

**CAS failure recovery:** After a no-CAS line-clear publish, the other player's engine has stale `LastSeq` values until its consumer processes the clear messages. During this window, their moves may fail with CAS errors. When a move publish fails (CAS on any row), the engine immediately fetches the latest row state from NATS via direct get and corrects both the in-memory row data and `LastSeq`. This ensures the display stays in sync with NATS even when moves are dropped due to stale sequences.

In **competitive mode**, each player maintains their own score independently on `jetricks.game.<id>.player.<pid>.score` as a simple incrementing CAS publish. No contention exists since only the owning player writes their own score.

**Top-out transition:**

When Player A's engine detects that the newly spawned piece (at the top of the playfield) cannot be placed without collision, it:
1. Publishes `GameEvent{Kind: EventGameOver, PlayerID: playerA, Score: currentScore, PieceCount: pieceIdx}` to the events subject.
2. CAS-transitions the game meta status to `finished` and sets `FinishedAt = time.Now()`.
3. Calls `e.transitionToSpectator()` — stops the gravity ticker and move input processing.
4. Emits `UpdateGameOver` to the UI, which shows the game-over overlay.
5. In **cooperative mode**, when ANY player tops out the game ends for ALL players immediately.
6. In **competitive mode** (2–4 players), only the topped-out player is eliminated (transitions to spectator). Other players continue. The engine tracks eliminated players via `eliminatedPlayers` map. When only one player remains, that player wins. The UI shows a player status list (playing/eliminated) and "YOU WON!"/"YOU LOST" at game over. See `jetricks-gameplays.md` for the authoritative game-over rules.

**Game archiving:** When a game finishes, the engine immediately publishes an `ArchiveRecord` to the `JETRICKS_ARCHIVE` stream (subject `jetricks.archive`) containing the game ID, mode, player count, player results (ID, score, piece count, winner), start/finish timestamps, and total score (cooperative). After the archive record is published, the game stream is deleted and the KV entry is removed. There is no archive delay — archiving happens immediately on game end.

---

## 10. internal/lobby

Manages all lobby-level state: player presence, game listings, global chat, and the lifecycle operations (create game, join game, leave game). Does not know about the UI layer.

### Files

#### `lobby.go`

```go
type Lobby struct {
    playerID  string
    name      string
    kv        jetstream.KeyValue
    js        jetstream.JetStream

    // Channels for outbound events to the UI layer
    Updates chan LobbyUpdate

    // mu protects Players and Games. The KV watcher goroutine holds the write lock
    // when updating these maps; UI handler goroutines hold the read lock when reading them.
    mu      sync.RWMutex
    players map[string]PlayerPresence  // keyed by playerID — lowercase, access via methods
    games   map[string]GameListing     // keyed by gameID — lowercase, access via methods
}

// Players returns a shallow copy of the current player presence map.
// Safe to call from any goroutine; the caller receives a consistent snapshot
// that will not be mutated after return.
func (l *Lobby) Players() map[string]PlayerPresence

// Games returns a shallow copy of the current game listing map.
func (l *Lobby) Games() map[string]GameListing

func New(
    ctx context.Context,
    js jetstream.JetStream,
    kv jetstream.KeyValue,
    playerID string,
    name string,
) (*Lobby, error)

func (l *Lobby) Start() error
func (l *Lobby) Stop()

func (l *Lobby) CreateGame(ctx context.Context, mode config.GameMode, playerCount int) (string, error) // playerCount is 2–4, selected by the user in the create game form
func (l *Lobby) JoinGame(ctx context.Context, gameID string) error
func (l *Lobby) LeaveGame(ctx context.Context, gameID string) error
func (l *Lobby) ToggleReady(ctx context.Context, gameID string) (allReady bool, err error) // toggle ready/not-ready; returns true when all players are ready
func (l *Lobby) StartGame(ctx context.Context, gameID string)  // transitions game to in_progress after countdown
func (l *Lobby) SendChat(ctx context.Context, text string) error
```

The maps are unexported and accessed only through `Players()` and `Games()`, ensuring all reads hold the read lock and all writes hold the write lock. The KV watcher goroutine (in `listing.go`) calls `l.mu.Lock()` / `l.mu.Unlock()` around every map mutation. UI SSE handlers call `l.Players()` / `l.Games()` which take `l.mu.RLock()`, copy the map, and release before returning. The copy is a shallow copy of the map (new map, same value structs) — since `PlayerPresence` and `GameListing` are value types, this is safe.

#### `presence.go`

```go
// runHeartbeat publishes a presence update to the lobby KV bucket
// every PresenceHeartbeat interval. Stops on context cancellation.
func (l *Lobby) runHeartbeat(ctx context.Context)

type PlayerPresence struct {
    PlayerID    string
    Name        string
    Status      PresenceStatus
    GameID      string  // non-empty if in a game or spectating
}

type PresenceStatus int

const (
    StatusInLobby    PresenceStatus = iota
    StatusInGame
    StatusSpectating
)
```

#### `listing.go`

```go
type GameListing struct {
    GameID      string
    Mode        config.GameMode
    Status      GameStatus
    PlayerCount int             // configured max players
    Players     []PlayerSummary // currently joined players
    CreatedAt   time.Time
    FinishedAt  time.Time       // zero if not finished
}

type GameStatus int

const (
    GameCreated    GameStatus = iota
    GameStarting
    GameInProgress
    GameFinished
    GameArchived
    GameCancelled
)

type PlayerSummary struct {
    PlayerID string
    Name     string
    Ready    bool
}
```

#### `events.go`

```go
type LobbyUpdateKind int

const (
    LobbyUpdatePlayers   LobbyUpdateKind = iota  // player list changed
    LobbyUpdateGames                              // game listing changed
    LobbyUpdateChat                               // new chat message
)

type LobbyUpdate struct {
    Kind    LobbyUpdateKind
    ChatMsg *ChatMessage  // non-nil for LobbyUpdateChat
}

type ChatMessage struct {
    PlayerID    string
    Name        string
    Text        string
    Timestamp   time.Time
    Spectator   bool
}
```

---

## 11. internal/cleanup

**File:** `cleanup/cleanup.go`

Runs once at startup, after the ordered consumer on the lobby KV has caught up to current state. Inspects all known game streams and lobby KV entries and resolves any stale or abandoned state.

Uses `orbit.go/natssysclient` to query JetStream server stats (`sys.Jsz`) for a full list of streams on the server. This is more reliable than listing streams via the JetStream API alone, particularly for detecting orphaned streams whose KV entries have already been deleted.

### Key Function

```go
// Run performs the full startup cleanup pass.
// Must be called after lobby state is fully loaded.
// ctx should have a reasonable timeout (e.g. 30s).
func Run(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, nc *nats.Conn, lobby *lobby.Lobby) error
```

The `nc` connection is used to construct a `natssysclient.SysClient` for the JetStream stats query. This requires the connection to have system account access; if not available, orphaned stream detection falls back to the standard JetStream stream list API.

### Cleanup Cases (in order of evaluation)

| Condition | Action |
|-----------|--------|
| Status `finished` (orphaned — not yet archived) | Publish archive record, delete game stream, remove KV entry |
| Status `starting`, all rostered players absent from KV | CAS-transition to `cancelled`, delete stream, remove KV entry |
| Status `created`, creator absent from KV, creation timestamp stale | CAS-transition to `cancelled`, delete stream, remove KV entry |
| Status `in_progress`, all players absent from KV for > heartbeat TTL | CAS-transition to `finished` (with `abandoned: true` in meta), then archive immediately (publish archive record, delete stream, remove KV entry) |
| `JETRICKS_GAME_<id>` stream exists, no matching KV entry | Delete orphaned stream |

Note: Games are archived immediately when they finish during normal play (see Section 9, top-out transition). The cleanup pass handles only orphaned finished games that were not archived due to a crash or disconnect.

### CAS Coordination

All transitions go through CAS on `jetricks.game.<id>.meta`. If a CAS fails during cleanup, the function re-reads the current status and re-evaluates. A failed CAS means another client already handled that game — no further action is needed.

---

## 12. internal/ui

The HTTP server and all UI rendering. Depends on `engine` and `lobby` but is never imported by them — the dependency is one-way. Communicates with engine and lobby exclusively through their `Updates` channels and exported method calls.

### Files

#### `server.go`

```go
type Server struct {
    port    int
    js      jetstream.JetStream
    kv      jetstream.KeyValue
    nc      *nats.Conn
    lobby   *lobby.Lobby  // nil until player logs in
    router  *http.ServeMux
}

func New(port int, js jetstream.JetStream, kv jetstream.KeyValue, nc *nats.Conn) *Server
func (s *Server) Start() error
func (s *Server) Stop()

// AttachEngine registers an active game engine with the server,
// wiring its Updates channel to the game SSE multiplexer.
// Called when the local player joins or creates a game.
func (s *Server) AttachEngine(e *engine.Engine)

// DetachEngine unregisters the engine when the game ends.
func (s *Server) DetachEngine()
```

Routes:
- `GET /` — login page (if no lobby) or lobby view (initial HTML)
- `POST /login` — validate player name, create lobby, redirect to lobby
- `GET /lobby/stream` — Datastar SSE stream for lobby updates
- `POST /lobby/chat` — send a lobby chat message
- `POST /lobby/game/create` — create a new game
- `POST /lobby/game/{id}/join` — join a game
- `POST /lobby/game/{id}/spectate` — spectate an in-progress game (creates engine in ModeSpectator)
- `GET /game` — game view (initial HTML)
- `GET /game/stream` — Datastar SSE stream for game updates
- `POST /game/move` — player move input

#### `sse.go`

```go
// SSEWriter wraps an http.ResponseWriter to emit Datastar-compatible
// Server-Sent Events. Provides PatchElements for DOM morphing and
// PatchSignals for updating Datastar client-side signals.
type SSEWriter struct { ... }

func NewSSEWriter(w http.ResponseWriter, r *http.Request) *SSEWriter
func (s *SSEWriter) PatchElements(html string) error
func (s *SSEWriter) PatchSignals(signals map[string]any) error
func (s *SSEWriter) Close()
```

Each open SSE connection is a long-lived HTTP response. The lobby SSE handler runs a loop selecting on `lobby.Updates` and translating each `LobbyUpdate` into one or more `PatchElements` calls using the templ templates. The game SSE handler does the same for `engine.EngineUpdate`.

### `ui/lobby/`

#### `handler.go`

Handles lobby view routes. Runs a goroutine per open SSE connection that selects on `lobby.Updates` and renders the appropriate templ fragment.

#### `lobby.templ`

Templates for lobby fragments:
- `PlayerList(players []lobby.PlayerPresence)` — sidebar player list
- `GameList(games []lobby.GameListing)` — main game listing with "Spectate" button on in-progress games
- `ChatLine(msg lobby.ChatMessage)` — single chat message appended to chat history
- `GameListItem(game lobby.GameListing)` — single game card (in-place update); in-progress games show a "Spectate" button
- `CreateGameForm()` — modal form for creating a new game, includes a "Players" number input (2–4)
- `ArchiveTable(records []config.ArchiveRecord)` — "Game History" section below active games, showing a table of archived games with mode, players, duration, and scores; updated in real time via the lobby's archive consumer

### `ui/game/`

#### `handler.go`

Handles game view routes. Runs a goroutine per open SSE connection selecting on `engine.Updates`. In competitive mode the engine emits both `UpdatePlayfield` (own rows) and `UpdateOpponentField` (opponent rows, one per opponent) — the handler distinguishes these by `UpdateKind` and the `OpponentID` field on `EngineUpdate`, patching the appropriate DOM elements for each opponent's sidebar board.

#### `board.templ`

Templates for the playfield:
- `Board(pf *game.Playfield, visibleRowStart int)` — full board render (initial load only); renders rows from `visibleRowStart` to `TotalRows-1`
- `BoardRow(row game.Row, rowIndex int)` — single row fragment (used for incremental updates)
- `OpponentBoard(pf *game.Playfield, visibleRowStart int)` — compact read-only board for an opponent's field in competitive mode (visible rows only, no active piece cursor, rendered in a sidebar column)
- `OpponentBoardRow(row game.Row, rowIndex int)` — single opponent row fragment for incremental patches

Only changed rows are re-rendered on each `UpdatePlayfield` or `UpdateOpponentField` event. The `ChangedRows` field on `EngineUpdate` tells the handler exactly which row fragments to patch. In competitive mode each opponent board is a separate DOM subtree with element IDs namespaced by opponent ID (e.g. `id="board-opp-<pid>-row-{n}"`) so patches to each board never interfere. In cooperative mode the single wide playfield (playerCount x StandardWidth columns) is rendered directly using the standard `Board` and `BoardRow` templates — the playfield is already the correct width (e.g. 20 columns for 2 players), so no concatenation or special template is needed. There is no visual separator between player sections — it looks like one unified playfield.

**Cell appearance — single source of truth.** Every `<td>` is rendered with an
explicit, server-computed fill color and outline emitted as an inline `style`; the
stylesheet carries **no** per-color cell classes (only structural rules and the
`.cell-flash` animation). All render paths funnel through one helper,
`cellStyle(cell game.Cell, localPlayerIdx int, showOutline bool) string`, which
returns `background:#..;outline:Npx solid #..;outline-offset:-1px`. Piece fills come
from a `pieceColors` table composited over the board background via `blend(fg, bg,
alpha)` (active ≈0.9, locked ≈0.7, adversarial ≈0.8), turning the old opacity
layering into concrete hexes. Outlines: own active → white; spectator
(`localPlayerIdx < 0`) → per-player color on active/locked cells; other player's
active piece in a player view → grid line; locked non-adversarial → per-player color
when `showOutline` (suppressed to the grid line on compact opponent boards). Because
appearance is computed in Go, the browser never decides colors and the visual model
stays consistent across own/spectator/opponent renders.

#### `hud.templ`

Templates for the heads-up display:
- `Score(score int)` — current score
- `Level(level int)` — current level (cooperative mode only)
- `NextPiece(piece game.PieceType)` — next piece preview
- `PlayerStatus(status string)` — player state indicator (shows "Spectating" for spectators)
- `Countdown(seconds int)` — pre-game countdown (5...4...3...2...1...GO!)
- `ReadyList(players []PlayerSummary)` — shows each player with a green checkmark (ready) or red cross (not ready)
- `GameOver()` — game-over overlay shown when any player tops out (game ends for all players immediately); the handler patches it with the results screen and returns the player to the lobby

**Ready/countdown flow:** While waiting for the game to start, each player sees the list of players with their ready state (green checkmark or red cross). Players can toggle their ready state by clicking the READY/NOT READY button. When ALL players are ready, the button and player list are replaced by a 5-second countdown (5...4...3...2...1...GO!). During the countdown, players cannot change their ready state. After the countdown, the game transitions to `in_progress` and pieces begin to spawn.

The game page hides controls and the ready button for spectators, showing "Spectating" as the player status instead.

#### `chat.templ`

- `ChatLine(msg lobby.ChatMessage)` — shared with lobby chat rendering
- `ChatPanel()` — in-game chat panel shell

### `ui/shared/`

#### `layout.templ`

- `LobbyPage(...)` — outer HTML shell for the lobby view
- `GamePage(...)` — outer HTML shell for the game view

Both include the Datastar script tag and establish the initial SSE connection via `data-on-load="@get('/lobby/stream')"` or `data-on-load="@get('/game/stream')"`.

---

## 13. Event Channel Contracts

All cross-package communication uses buffered Go channels. The buffer size is chosen to absorb brief bursts without blocking the sender goroutine.

| Channel | Direction | Buffer | Notes |
|---------|-----------|--------|-------|
| `engine.Updates` | engine → ui | 64 | High-frequency during play (gravity ticks, every row update). Dropping updates here is preferable to blocking the engine. If the channel is full the engine drops the update — the next update will correct the display. |
| `lobby.Updates` | lobby → ui | 16 | Lower frequency. Lobby changes are infrequent relative to game updates. |
| Internal engine move channel | ui → engine | 8 | Player move requests from the HTTP handler to the engine goroutine. Small buffer prevents the HTTP handler blocking on a slow game loop. |

Channels are never closed by the sender — they are abandoned when the owning goroutine exits via context cancellation. Receivers must always select on both the channel and `ctx.Done()`.

---

## 14. Bootstrap Sequence

The following steps happen in order at startup. Steps that can fail cause the application to exit with a clear error message.

```
1.  Parse CLI flags → config.Config
2.  Connect to NATS via natscontext.Connect(config.NATSContext) → *nats.Conn, jetstream.JetStream, natscontext.Settings
    (empty context name uses the currently selected nats CLI context)
3.  EnsureLobbyChatStream
3a. EnsureArchiveStream
4.  EnsureLobbyKV
5.  Create ui.Server (with js, kv, nc — no lobby yet)
6.  Start ui.Server (HTTP listener ready)
7.  Open browser or webview at http://localhost:<port>
    → Browser shows login page (player name prompt)
8.  Player enters name → POST /login validates and triggers:
    a. Create lobby.Lobby with playerName as both playerID and name
    b. Start lobby (KV watcher, chat consumer, archive consumer, heartbeat)
    c. Wait for initial KV load
    d. Run cleanup.Run
    e. Redirect to lobby page
9.  Block on os.Signal (SIGINT / SIGTERM)
10. On signal: cancel root context → all goroutines exit via ctx.Done()
11. Close NATS connection
12. Exit
```

Step 8c — waiting for the KV watcher to finish its initial load — is critical for correctness of the cleanup pass (step 8d). The initial load is complete when the KV watcher receives a nil entry, which NATS delivers after all existing entries have been sent.

---

## 15. Key Interfaces

Where packages need to be decoupled for testing, interfaces are defined in the consuming package and implemented by the dependency.

```go
// In internal/engine — allows nats publish to be mocked in tests
type Publisher interface {
    PublishMoveAtomically(ctx context.Context, gameID string, updates []RowUpdate) error
    PublishMeta(ctx context.Context, gameID string, payload []byte, expectLastSeq uint64) error
}

// In internal/lobby — allows KV operations to be mocked in tests
type KVStore interface {
    Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error)
    Put(ctx context.Context, key string, value []byte) (uint64, error)
    Delete(ctx context.Context, key string, opts ...jetstream.KVDeleteOpt) error
    WatchAll(ctx context.Context, opts ...jetstream.WatchOpt) (jetstream.KeyWatcher, error)
}

// In internal/engine — allows game logic to be substituted in tests
type Playfield interface {
    Apply(rowIndex int, row game.Row, seq uint64)
    ActivePiece() *game.Piece
    Snapshot() [config.TotalRows]uint64
    CanPlace(p game.Piece) bool
}
```

---

## 16. Goroutine Inventory

All goroutines are started with a context derived from the root context and exit cleanly on cancellation. No goroutine is started without a corresponding documented exit path.

| Goroutine | Owner | Started | Exits on |
|-----------|-------|---------|----------|
| Lobby KV watcher | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Lobby chat consumer | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Lobby archive consumer | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Lobby presence heartbeat | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Game stream consumer | `engine.Engine` | `engine.Start()` | ctx cancel |
| Gravity ticker | `engine.Engine` | `engine.Start()` | ctx cancel |
| Lobby SSE handler | `ui/lobby.handler` | per HTTP connection | client disconnect or ctx cancel |
| Game SSE handler | `ui/game.handler` | per HTTP connection | client disconnect or ctx cancel |

---

## 17. orbit.go Module Reference

All orbit.go modules are independently versioned. Import only the modules needed rather than the whole library.

| Module | Import path | Used in | Purpose in Jetricks |
|--------|-------------|---------|-------------------|
| `natscontext` | `github.com/synadia-io/orbit.go/natscontext` | `internal/nats` | Connect using NATS CLI context files. Replaces raw URL + credential flags with a single context name, sharing config with the `nats` CLI tool. |
| `jetstreamext` | `github.com/synadia-io/orbit.go/jetstreamext` | `internal/nats` | Atomic batch publishing for move CAS operations. `GetLastMsgsFor` for instant playfield reconstruction on startup/reconnect (fetches last message per row subject in one round trip). |
| `counters` | `github.com/synadia-io/orbit.go/counters` | `internal/engine` | Distributed CRDT counter for cooperative mode shared score. Both players increment independently; the counter converges without CAS contention. |
| `natssysclient` | `github.com/synadia-io/orbit.go/natssysclient` | `internal/cleanup` | Query JetStream stream inventory via the NATS system API (`Jsz`) to detect orphaned game streams whose KV entries no longer exist. |

### Modules considered but not used

| Module | Reason not used |
|--------|----------------|
| `kvcodec` | Jetricks KV keys are already NATS-compatible (no dots, spaces, or special chars). Values are plain JSON. No encoding layer needed. |
| `natsext` (RequestMany) | Jetricks uses ordered consumers and direct publishes. Scatter-gather request/reply is not part of any game or lobby flow. |
| `pcgroups` | Jetricks uses ordered consumers for strict in-order delivery per client. Partitioned consumer groups target parallel work-queue consumption patterns, which is not applicable here. |

---

## 18. Testing Strategy

### Unit tests (no NATS required)

- `internal/game` — all functions are pure and take no external dependencies. Full coverage of piece rotation (all SRS wall kicks), collision detection, line clear detection, row serialisation, score and level calculation, gravity interval curve.
- `internal/rng` — verify determinism: two `Sequence` instances with the same seed produce identical output. Verify seek: `Piece(N)` equals the Nth output from sequential calls.
- `internal/config` — subject builder functions produce correct strings.

### Integration tests (require a NATS server)

- `internal/nats` — stream creation, KV operations, atomic batch publish happy path, CAS failure path, stream sealing, `FetchPlayfieldState` via `GetLastMsgsFor`. Tests use a local NATS context pointing at the test server so that `natscontext.Connect` is exercised end-to-end rather than bypassed.
- `internal/engine` — start an engine against a real NATS server with a test game stream. Submit moves and verify the playfield reaches the expected state. Simulate CAS failure by publishing a conflicting update from a second client. Verify the `FetchPlayfieldState` snapshot correctly seeds `LastSeq` before the ordered consumer starts. Verify cooperative score increments via the `counters` CRDT converge correctly across two engine instances.
- `internal/lobby` — create/join/leave game operations, presence heartbeat expiry, KV watcher delivery.
- `internal/cleanup` — seed a NATS server with stale game streams in various states and verify cleanup produces the correct outcomes. Verify `natssysclient` orphaned stream detection when KV entries are missing.

A `testutil` package (not listed above, internal to tests) provides helpers for spinning up an embedded NATS server for integration tests, writing a temporary NATS context file pointing at it, and asserting stream message contents.

### End-to-end

Two engine instances running against a shared NATS server, simulating a competitive game. Assert that line clears on one side produce shrink events on the other, that the CAS mechanism correctly serialises simultaneous moves, and that the archive/seal sequence runs correctly at game end.

---

## 19. Design Decision Log

Decisions settled during design review, recorded here for future reference.

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | Competitive playfield topology | Player-scoped row subjects within one shared stream (`jetricks.game.<id>.player.<pid>.playfield.row.<n>`) | One stream per game keeps lifecycle management simple. Player-scoped subjects provide full isolation within it. |
| 2 | Lock-in detection | Implicit — engine scans row state for the `Active→Occupied` transition | No extra message; lock-in is definitionally visible in the row data that would be fetched anyway on rejoin. |
| 3 | Line-clear row shift publisher | Client whose piece caused the lock-in | Avoids a first-CAS-wins race on a large batch; the publisher has the most current local state. |
| 4 | Opponent shrink in competitive | Player A publishes shrink event; Player B's engine applies it to its own rows | Player B's engine owns its rows for CAS purposes. Shrink-as-event decouples A's writes from B's CAS keys. |
| 5 | Row payload encoding | JSON | Simpler to implement and debug with `nats` CLI. Row update rate is low enough that JSON overhead is not a concern. |
| 6 | Startup consumer start point | `max(row seqs)+1` | Avoids reprocessing the entire stream history on every join/reconnect. The gap in non-row subjects (at most a few milliseconds of game time) is acceptable; the playfield snapshot reflects any shrinks or clears that occurred in that window. |
| 7 | Lobby map concurrency | `sync.RWMutex` on `Lobby.mu`, maps unexported, accessed via `Players()` / `Games()` snapshot methods | Straightforward, low-overhead, and makes the access pattern explicit without channel complexity. |
| 8 | Cooperative score stream | Both `AllowMsgCounter` and `AllowAtomicPublish` on the same `JETRICKS_GAME_<id>` stream | Keeps the stream count at one per game. Combination must be verified on the target NATS version. |
| 9 | Game ID format | UUID v4 with dashes (`550e8400-e29b-41d4-a716-446655440000`) | UUIDs are globally unique, collision-free, and NATS stream names allow dashes. |
| 10 | Game-over semantics | Cooperative: any top-out ends for all. Competitive: eliminated player becomes spectator; game continues until one player remains. | See `jetricks-gameplays.md`. |
| 11 | HardDrop CAS behaviour | Auto-retry (`PublishHardDrop`) — intent always fulfilled | Destination is always computable from a valid game state. Player intent should be honoured unconditionally. |
| 12 | Opponent display in competitive | Full live view via one ordered consumer per opponent's row subjects | Provides the same real-time fidelity as the player's own field. The overhead of additional consumers is minimal (at most 3 opponents in a 4-player game). |
| 13 | `pieceIdx` recovery on join/reconnect | Store `PieceIdx uint64` in `GameMeta`; locking engine CAS-updates it after each lock-in | `FetchGameMeta` gives any joining engine the current piece index in one round trip. No stream replay needed. |
| 14 | Cooperative playfield topology | Single shared playfield of width `playerCount × StandardWidth` with `CoopPlayfieldID = "coop"` as the row subject player token | Both players' pieces coexist on one wide board. `Cell.PlayerIdx` distinguishes active pieces. One ordered consumer per engine. Line clears span the full width. UI renders the single playfield directly. |
| 15 | `GameMeta` payload | Fully specified in Section 4 with lifecycle, identity, RNG seed, and `PieceIdx` fields | Status uses string constants for readability in the `nats` CLI. `PieceIdx` enables fast startup without stream replay. |
| 16 | Real-time UI updates from JetStream | All UI data backed by JetStream uses ordered consumers pushing to Datastar SSE — never polling or periodic refresh | The lobby runs consumers for KV (players/games), chat, and archives. The engine runs consumers for playfield rows, events, meta, and countdown. Any change in a JetStream stream or KV bucket is immediately pushed to the UI via the consumer → Updates channel → broadcaster → SSE pipeline. |
