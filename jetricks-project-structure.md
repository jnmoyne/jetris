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
12. [Front ends: native (default) and web (`--web`)](#12-front-ends-native-default-and-web---web)
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
│   │   ├── move.go
│   │   ├── consumer.go
│   │   └── events.go
│   ├── lobby/
│   │   ├── lobby.go
│   │   ├── presence.go
│   │   ├── listing.go
│   │   └── events.go
│   ├── cleanup/
│   │   └── cleanup.go
│   ├── archive/
│   │   └── archive.go
│   ├── render/
│   │   └── colors.go
│   ├── nativeui/
│   │   ├── app.go
│   │   ├── board.go
│   │   ├── bridge.go
│   │   ├── game.go
│   │   ├── input.go
│   │   ├── lifecycle.go
│   │   ├── lobby.go
│   │   └── login.go
│   ├── testutil/
│   │   └── nats.go
│   └── ui/
│       ├── server.go
│       ├── handlers.go
│       └── broadcast.go
├── go.mod
└── go.sum
```

---

## 2. Package Dependency Graph

Arrows indicate "depends on". The rule is that `internal/game`, `internal/rng`, and `internal/config` are leaves — they have no internal dependencies. The front-end layers (`internal/nativeui`, `internal/ui`) depend on engine and lobby but neither engine nor lobby depends on a front end. All packages may depend on config.

```
cmd/jetricks
    ├── internal/config
    ├── internal/nats              ← uses: orbit.go/natscontext, orbit.go/jetstreamext
    ├── internal/rng
    ├── internal/game
    ├── internal/engine            ← depends on: nats, game, rng, config
    ├── internal/lobby             ← depends on: nats, config
    ├── internal/cleanup           ← depends on: nats, lobby, config
    ├── internal/archive           ← depends on: nats, engine, lobby, config
    ├── internal/render            ← depends on: game (cell/board appearance)
    ├── internal/nativeui          ← depends on: engine, lobby, render, config (default front end)
    └── internal/ui                ← depends on: engine, lobby, config (web front end, --web)

Leaf packages (no internal deps):
    internal/config
    internal/game
    internal/rng

orbit.go modules used:
    orbit.go/natscontext   → internal/nats     (connection via NATS CLI contexts)
    orbit.go/jetstreamext  → internal/nats     (atomic batch publish, GetLastMsgsFor)
```

---

## 3. cmd/jetricks

**File:** `cmd/jetricks/main.go`

The entrypoint. Responsible only for wiring — it constructs all top-level components, injects dependencies, and starts the application. Contains no business logic.

### Responsibilities

- Parse CLI flags into a `config.Config`
- Establish the NATS connection (shared by both UIs)
- Ensure lobby streams and KV exist (shared by both UIs)
- Branch on the `--web` flag:
  - **default (native):** `runNative` opens a native OS window via the Gio front end (`internal/nativeui`). Gio's `app.Main()` owns the OS main thread, so the app logic runs on a goroutine that calls `App.Run`.
  - **`--web`:** `runWeb` starts the HTTP/UI server (`ui.Server`) and opens the browser. Lobby creation is deferred until the player enters their name.
- Block on OS signal / window close and perform graceful shutdown

In both modes the player enters a name on a login screen; identity is the same NATS-backed presence (no browser session). The native window is the default so no HTTP service or browser is involved unless `--web` is passed.

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | `""` | NATS context name (as configured with `nats context add`). Empty string uses the currently selected context. |
| `--server` / `--user` / `--password` | `""` | Explicit NATS URL + credentials, overriding `--context`. |
| `--web` | `false` | Use the web browser UI (HTTP/SSE/Datastar) instead of the default native window. |
| `--port` | `7777` | Local HTTP server port (only used with `--web`). |
| `--webview` | `false` | Legacy/unused flag (`config.Webview`); has no effect in the current build. |

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
    NATSContext  string // NATS context name; empty = currently selected context
    NATSURL      string // explicit server URL (overrides context)
    NATSUser     string
    NATSPassword string
    Port         int    // HTTP port (web UI only)
    Webview      bool   // legacy/unused
    Web          bool   // use the web browser UI instead of the native window
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
    TotalRows       = 48   // max rows; the board grows taller downward as player count rises
    HeadroomRows    = 4
    VisibleRows     = 24   // base visible rows (cooperative and single mode)
    VisibleRowStart = 4    // base first visible row index (cooperative; competitive adjusts per game)
    StandardWidth   = 10

    LobbyKVBucket          = "JETRICKS_LOBBY"
    LobbyChatStream        = "JETRICKS_LOBBY_CHAT"
    LobbyChatSubject       = "jetricks.lobby.chat"
    ArchiveStream          = "JETRICKS_ARCHIVE"
    ArchiveSubject         = "jetricks.archive"
    LobbyChatMaxAge        = 7 * 24 * time.Hour
    PresenceHeartbeat      = 5 * time.Second
)

// CompetitiveVisibleRows returns the number of visible rows for a competitive
// game with the given player count: VisibleRows + playerCount (each player adds
// one row, so the board grows taller downward).
func CompetitiveVisibleRows(playerCount int) int {
    return VisibleRows + playerCount
}

// CompetitiveTotalRows returns the total rows (headroom + visible) for a
// competitive game with the given player count.
func CompetitiveTotalRows(playerCount int) int {
    return HeadroomRows + CompetitiveVisibleRows(playerCount)
}

// CompetitiveVisibleRowStart returns the first visible row index for a competitive
// game. The board grows taller downward, so headroom stays constant and this
// always equals HeadroomRows (4) regardless of player count.
func CompetitiveVisibleRowStart(playerCount int) int {
    return HeadroomRows
}
```

### Archive Types

```go
// PlayerResult holds one player's outcome in a completed game.
type PlayerResult struct {
    PlayerID   string `json:"player_id"`
    Score      int    `json:"score"`
    PieceCount uint64 `json:"piece_count"`
    Winner     bool   `json:"winner,omitempty"`
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

// Cooperative and competitive modes use entirely separate playfield subject
// schemes — they are not parameterisations of one builder and are free to
// diverge. A game is one mode or the other, so an engine uses only one scheme.
//
// Cooperative — single shared wide playfield (playerCount × StandardWidth);
// row subjects carry NO player token. Every player publishes to / consumes from
// the same subjects; per-cell ownership lives in the payload (Cell.PlayerIdx).
func CoopRowSubject(gameID string, row int) string
//   → jetricks.game.<id>.playfield.row.<n>
func CoopRowSubjectFilter(gameID string) string
//   → jetricks.game.<id>.playfield.row.>

// Competitive — each player owns a private playfield scoped by their UUID.
func CompetitiveRowSubject(gameID string, playerID string, row int) string
//   → jetricks.game.<id>.player.<pid>.playfield.row.<n>
func CompetitiveRowSubjectFilter(gameID string, playerID string) string
//   → jetricks.game.<id>.player.<pid>.playfield.row.>

func MetaSubject(gameID string) string
func RosterSubject(gameID string, playerID string) string
func EventsSubject(gameID string) string
func CountdownSubject(gameID string) string
func ChatSubject(gameID string) string

func LobbyPlayerKey(playerID string) string
func LobbyGameKey(gameID string) string

// The archive subject is the ArchiveSubject const ("jetricks.archive") — there
// is no builder function for it.
```

All subject and stream names in the application are produced exclusively through these builders. No package constructs subject strings by hand.

### Stream Configuration Notes

`JETRICKS_GAME_<id>` is created with `FileStorage`, `LimitsPolicy` retention, and two stream-level flags:
- `AllowAtomicPublish: true` — required for jetstreamext atomic batch move publishing
- `AllowDirect: true` — enables direct get / `GetLastMsgsFor` for fast playfield reconstruction and per-subject refetch

Both flags are set unconditionally on every game stream regardless of mode. No `MaxAge` is set (game streams are deleted at game end), and `AllowMsgCounter` is **not** set — the cooperative score is a plain local counter propagated via events, not a server-side counter CRDT.

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

The `PieceIdx` field is the number of pieces that have locked in across the entire game. In cooperative mode every player initialises their RNG from the same `Seed` and tracks their own `pieceIdx` locally — players still receive different pieces at any moment because their indices advance independently as each locks in, and each piece spawns offset into that player's section (`p.Col += playerIdx*StandardWidth`). `PieceIdx` in meta is not used for cooperative mode piece tracking. In competitive mode each player has an independent piece sequence (also seeded from `Seed`) — `PieceIdx` tracks the piece index for the player whose piece last locked in. Competing engines track their own index locally and only publish to meta when their own piece locks in.

> **Implementation note:** `PieceIdx` in meta is eventually consistent. A joining engine that reads it mid-game will get the last published value, which may lag by at most one piece lock-in. The engine should treat this as a starting lower bound: after applying the FetchPlayfieldState snapshot, it scans the playfield for active piece presence, and if an active piece is visible but its index would correspond to `PieceIdx`, the index is correct. If no active piece is present (lock-in just happened but meta not yet updated), the engine can wait for the ordered consumer to deliver the next row state, which will show the new piece spawning — at that point the implied piece index is `PieceIdx + 1`. This self-corrects without any special handling.

---

## 5. Player Identity

Player identity is handled entirely in the UI at startup — no persistent files are stored on disk. When a player starts Jetricks (native window by default, or the browser with `--web`), they are prompted on a login screen to enter a player name. This name **is** the player ID used in all NATS subjects, KV keys, and game rosters. There is no separate display name.

### Validation

Player names are validated to be legal NATS subject tokens:
- Must be 1–32 characters
- Cannot contain `.`, ` ` (space), `*`, `>`, tab, newline, carriage return, or null

Validation is implemented in `config.ValidatePlayerName(name) error`.

### Flow

The flow is identical in both UIs; only the transport differs (native calls the handlers directly; web posts `/login` with Datastar signals).

1. App starts → login screen is shown (no lobby exists yet)
2. Player enters a name → the name shape is validated (`config.ValidatePlayerName`) and then the lobby KV is checked (`lobby.IsNameInUse`) for an active player presence entry with the same name (case-insensitive, whitespace-trimmed). Stale presence entries — `LastSeen` older than 3× `config.PresenceHeartbeat` — are ignored so unclean shutdowns don't permanently block the name.
3. If the name collides with an active player, a confirmation prompt ("a user with this name is already in the lobby — join anyway?") with **Yes, join** / **Cancel** is shown. **Yes, join** forces login, skipping the collision check. (Web carries this via the `forceLogin` Datastar signal re-posting `/login`; native sets an internal force flag and retries.)
4. On success, the lobby is created and the app moves to the lobby screen.

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

// SealGameStream sets Sealed: true on a game stream, permanently preventing
// writes. Only used by the cleanup pass for orphaned finished streams; normal
// game end DELETES the stream instead.
func SealGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// DeleteGameStream deletes a game stream entirely (normal game end, cancelled
// games, and orphaned-stream cleanup).
func DeleteGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// ListGameStreams returns names of all streams matching the JETRICKS_GAME_ prefix.
func ListGameStreams(ctx context.Context, js jetstream.JetStream) ([]string, error)
```

#### `kv.go`

```go
// EnsureLobbyKV creates or retrieves the lobby KV bucket.
func EnsureLobbyKV(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error)
```

The KV bucket is created with only `Bucket` and `Storage: FileStorage` — no bucket-level TTL and no `DeleteMarkerTTL` (game listings must persist indefinitely). Presence staleness is handled in application code instead: each player refreshes its presence entry on the heartbeat, and the presence watcher prunes entries whose `LastSeen` timestamp is older than 3× the heartbeat interval (`pruneStalePresence`).

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
// RowUpdate represents a single row's new state and the CAS expectation. The
// caller supplies the fully-built row subject, so this package is subject-
// agnostic — it knows nothing about game modes or players. The engine builds
// Subject with the mode-appropriate scheme (Coop*/Competitive*RowSubject) and
// orders the slice for the desired consumer apply order.
type RowUpdate struct {
    Subject         string
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
    updates []RowUpdate,
) error

// PublishRowsAtomicallyNoCAS publishes a set of row updates as a SINGLE
// atomic batch WITHOUT CAS expectations. Used for authoritative state
// transitions (lock, hard-drop landing, line-clear, shrink) where the
// publisher's view is the new ground truth.
func PublishRowsAtomicallyNoCAS(
    ctx context.Context,
    js jetstream.JetStream,
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
// FetchPlayfieldState retrieves the current state of the given row subjects
// for a game in a single round trip using jetstreamext.GetLastMsgsFor.
// Used by the engine on startup and reconnect to reconstruct the full
// playfield instantly without replaying the entire game stream history.
//
// The caller builds the subjects with the mode-appropriate scheme (coop or
// competitive), so this function is subject-agnostic. Results are keyed by the
// row index parsed from each subject, so it works for either subject shape.
func FetchPlayfieldState(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    subjects []string,
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

`FetchPlayfieldState` calls `jetstreamext.GetLastMsgsFor(ctx, js, streamName, rowSubjects)` where `rowSubjects` is the caller-supplied list of row subjects for the game. The engine builds it with the mode-appropriate scheme: `config.CoopRowSubject(gameID, n)` for the shared cooperative board (no player token) or `config.CompetitiveRowSubject(gameID, playerID, n)` for one competitive player's board. This returns the last message per subject in a single server round trip — far more efficient than replaying the entire stream from sequence 0 on join or reconnect. The engine uses this for its initial playfield snapshot before starting the ordered consumer, then the consumer takes over for live updates from that point forward.

---

## 7. internal/rng

**File:** `rng/rng.go`

Deterministic, seekable piece sequence generation. Uses Go's `math/rand/v2` with a PCG source. In **both** modes every player initialises their RNG from the same `Seed` stored in game metadata. In competitive mode all players therefore produce the identical piece sequence. In cooperative mode players still see different pieces at any given moment because each advances its own `pieceIdx` independently and each spawn is column-offset into that player's section — the sequence itself is shared, not forked with `seed+1`.

### Key Types

```go
type Sequence struct {
    seed uint64
}

// New creates a Sequence from the given seed.
func New(seed uint64) *Sequence

// Piece returns the piece type at position index in the sequence.
// Seeking directly to index means any piece can be retrieved without
// replaying all prior calls — safe for reconnect and state reconstruction.
// Each call derives a fresh PCG source from (seed, index/7) and shuffles the
// 7-bag, so no per-instance mutable state is kept.
func (s *Sequence) Piece(index uint64) game.PieceType
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
    Height int    // total rows (headroom + visible); varies per mode/player count
    Rows   []Row  // length == Height
    // LastSeq tracks the stream sequence of the last message received
    // for each row subject — used for CAS expectations. length == Height.
    LastSeq []uint64
}

// NewPlayfield creates an empty playfield with the default TotalRows height.
func NewPlayfield(width int) *Playfield

// NewPlayfieldWithHeight creates an empty playfield with a specific height.
// Cooperative uses HeadroomRows+VisibleRows; competitive uses
// CompetitiveTotalRows(playerCount) (taller as player count rises).
func NewPlayfieldWithHeight(width, height int) *Playfield

// Apply updates the playfield from a decoded row message received from NATS.
// Updates both the cell data and the LastSeq for that row.
func (pf *Playfield) Apply(rowIndex int, row Row, seq uint64)

// ActivePieceForPlayer returns the active piece belonging to the given playerIdx
// (matching Cell.PlayerIdx). Used in cooperative mode where two players' active
// pieces coexist on the same shared playfield. Returns nil if no active piece
// with that playerIdx is present.
func (pf *Playfield) ActivePieceForPlayer(playerIdx int) *Piece

// SetActivePieceForPlayer / ClearActiveCellsForPlayer mutate the playfield in
// place. They are used by ProjectShrink to recompute the row payloads (and in
// unit-test setup); see "Invariant: NATS as single source of truth for the
// playfield" in section 9.
func (pf *Playfield) SetActivePieceForPlayer(p Piece, playerIdx int)
func (pf *Playfield) ClearActiveCellsForPlayer(playerIdx int)

// Projection helpers — compute the row payloads that the engine should
// publish to NATS WITHOUT mutating pf. The engine never mutates pf; the
// consumer applies the published rows on echo via Apply().
func (pf *Playfield) ProjectMove(affectedRows []int, newPiece *Piece, playerIdx int) map[int]Row
func (pf *Playfield) ProjectLock(affectedRows []int, playerIdx int) map[int]Row
func (pf *Playfield) ProjectHardDrop(affectedRows []int, dest Piece, playerIdx int, lockOnLand bool) map[int]Row
func (pf *Playfield) ProjectClearRows(completed []int, shiftAnchors bool) []Row
func (pf *Playfield) ProjectShrink(rowsToAdd, causerIdx, ownPlayerIdx int) ([]Row, bool)
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
// treated as obstacles (in addition to locked cells). The moving player's own
// active cells (matching ownPlayerIdx) are excluded from collision.
func CanPlaceCoop(p Piece, pf *Playfield, ownPlayerIdx int) bool

// HardDropDestinationCoop is like HardDropDestination but uses CanPlaceCoop,
// so the other player's active piece counts as an obstacle.
func HardDropDestinationCoop(p Piece, pf *Playfield, ownPlayerIdx int) Piece

// HardDropDestination computes the lowest valid row the piece can occupy
// given the current playfield state, without modifying the playfield.
// The returned Piece has the same Type, Orientation, and Col as the input
// but with Row set to the lowest non-colliding position.
// Used by the engine's hard-drop handler to build the destination row updates.
func HardDropDestination(p Piece, pf *Playfield) Piece
```

#### `lineclear.go`

```go
// CompletedRows returns the indices of rows that are fully occupied
// by locked (non-active) cells.
func CompletedRows(pf *Playfield) []int

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

### Atomic batches with per-subject CAS

Every publication of multiple rows from the engine is a SINGLE atomic batch:

- `natspkg.PublishMoveAtomically` — multi-row batch with **per-subject CAS** expectations (`Nats-Expected-Last-Subject-Sequence`, applied via `jetstreamext.WithBatchExpectLastSequencePerSubject(seq)`). Used for moves, rotations, and spawns.
- `natspkg.PublishRowsAtomicallyNoCAS` — multi-row batch without CAS. Used for authoritative state transitions (piece lock, hard-drop landing, line-clear, opponent-shrink application).

Why per-subject CAS, not stream-level (`WithBatchExpectLastSequence`)? Each row is its own NATS subject. Per-subject CAS rejects only when *our* row was overwritten since we last saw it; concurrent writes to *other* rows don't conflict. This is essential in cooperative mode where two players write the same shared playfield, and useful in competitive mode for parallelism between meta/event publishes and row publishes.

Why atomic batch, not row-by-row? A single move typically touches 2+ rows (the row the piece is leaving and the row(s) it is entering). If those messages arrived at consumers one at a time, every other player would briefly observe a half-erased / half-placed piece between consumer applies. Atomic batch makes the multi-row update visible to consumers as one indivisible step.

The expected-last-sequence value for each row comes from `e.playfield.LastSeq[r]`, updated via `pf.Apply(rowIdx, row, seq)` from two places: the row consumer on an ordered-consumer echo, and the **publish write-through** (`applyPublishedRows`), which advances it from the batch commit ack the instant a write commits. The write-through keeps the CAS expectation current so the next write doesn't lose a per-subject race against the engine's own just-committed write; `pf.Apply`'s strictly-higher-sequence rule reconciles the two sources (the echo of our own write carries the same sequence we already applied and is skipped; only a higher sequence updates memory).

**Optimistic sequence write-through (both modes).** The per-subject CAS expectation is `pf.LastSeq[row]`, advanced by `Playfield.Apply`. Rather than waiting for the engine's own consumer to echo a published row back before that expectation (and the board content) catches up, a **successful publish is written through into the playfield immediately**: the batch commit ack returns the stream sequence of the last message, and since an atomic batch's messages get consecutive stream sequences the engine infers each row's sequence (`message i of N → commitSeq − (N−1−i)`) and applies the committed content + sequence via `pf.Apply`. The two batch publishers (`PublishMoveAtomically`, `PublishRowsAtomicallyNoCAS`) return that commit sequence; `applyPublishedRows` does the write-through. `pf.Apply`'s "apply only a **strictly higher** sequence" rule reconciles this with the later echo: the echo of our own write carries the same sequence we already wrote through and is skipped, while a higher sequence (the other player's write in coop, or a NoCAS write we didn't originate) still updates memory. In coop the write-through applies only what actually committed (the first-attempt or merge-retry batch), so it never clobbers the other player's cells. This keeps the in-memory view current so a player cannot lose a per-subject CAS race against their own just-committed write (gravity vs. input, a write right after a NoCAS line-clear/shrink, a fast input burst). `applyPublishedRows` takes `e.mu` unless the caller already holds it — a `locked` flag is threaded through the publish helpers and `spawnPiece` because `spawnPiece` and the line-clear publish run under the consumer's lock while every other publish path runs with the lock released.

CAS-failure handling for **player moves** (same in both modes): **drop the move, no retry, no NATS publish**. The engine emits an `UpdateCASFlash` directly on its local `Updates` channel; the player must retry the input themselves.

In cooperative mode the shared playfield has two writers, so CAS rejections on moves are an expected, regular occurrence. A silent server-side retry would mask the conflict and make the player's own input timing feel non-deterministic. Instead we surface the failure loudly: the UI renders the `UpdateCASFlash` as a **rainbow outline flash on the player's own piece** — cells in `FlashCells` cycle through the seven spectrum colors over roughly 600 ms with a matching glow, then revert. The other players see nothing, since one player's input rejection is information of no use to anyone else.

CAS-failure handling for **engine-driven (internal) writes** — piece spawn and gravity ticks. The player did not press a key for either, so a flash would be misleading; and both share row subjects with the other player in coop mode. Both **must** succeed: a dropped spawn would leave the player pieceless, and a dropped gravity tick would make the piece appear frozen for one tick interval. In coop mode both go through `publishProjectedRowsWithMergeRetry`: on CAS failure, refetch each affected row from the stream via `stream.GetLastMsgForSubject`, overlay this player's cells on top, retry the batch with refreshed per-subject CAS expectations (up to 5 retries). In competitive mode each player owns their subjects, so both go through the regular `publishProjectedRows`. **Gravity and player input run on one goroutine (`runInput`), so a player's own gravity tick and move are serialized and cannot lose the per-subject CAS race against each other** — this is what removed the spurious rainbow flashes that were otherwise visible in competitive play.

The rainbow flash fires for any dropped CAS write (player moves, gravity ticks, and spawns alike). The `internal` boolean threaded through `attemptMove` / `attemptMoveStandard` / `attemptMoveCoop` distinguishes the source: the moves arm of `runInput` (player input) calls `attemptMove(move, false)`, while its gravity arm calls `attemptMove(MoveDown, true)`. In coop, `internal=true` routes through merge-retry so gravity flashes only after all retries are exhausted.

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
    gameID      string
    playerID    string
    gameMode    config.GameMode  // cooperative or competitive
    mode        Mode
    initialMode Mode             // original mode at creation (ModePlayer or ModeSpectator)
    playerIdx   int              // 0 for creator, 1 for joiner; used in cooperative mode for Cell.PlayerIdx
    playerCount int              // number of players in the game

    mu        sync.Mutex
    playfield *game.Playfield    // own (coop: the single shared wide playfield)

    // Opponent playfields — populated in competitive mode only, discovered
    // dynamically via the roster consumer. One per opponent, each maintained by
    // its own ordered consumer on that opponent's row subjects.
    opponentPlayfields map[string]*game.Playfield // keyed by opponent playerID
    opponentPlayerID   string                     // the known opponent (2-player join), if any

    seq      *rng.Sequence
    pieceIdx uint64
    metaSeq  uint64

    score             int
    totalLines        int
    level             int
    hadActivePiece    bool
    eliminatedPlayers map[string]bool // players who have topped out (competitive)
    visibleRowStart   int             // first visible row index (varies per mode/player count)

    // Channels for outbound events to the UI layer
    Updates        chan EngineUpdate
    OnGameFinished func() // called after the game transitions to finished (wired to archive.ArchiveAndCleanup)

    // internal
    js         jetstream.JetStream
    ctx        context.Context
    cancelFn   context.CancelFunc
    moves      chan MoveType
    rowUpdated chan struct{}
}

// New constructs an engine; it takes no ctx (Start derives one) and a SINGLE
// opponentPlayerID (the known opponent for a 2-player join; other opponents are
// discovered dynamically via the roster consumer). It does NOT take playerCount,
// seed, or initialPieceIdx — those are read from GameMeta in Start. playerIdx is
// the value returned by lobby.JoinGame.
func New(
    js jetstream.JetStream,
    gameID, playerID, opponentPlayerID string,
    gameMode config.GameMode,
    mode Mode,
    playerIdx int,
) *Engine

// Start begins all consumer goroutines and (if ModePlayer) the combined
// input+gravity goroutine (runInput).
// In cooperative mode, starts ONE ordered consumer on the shared row subjects
// (no player token). In competitive mode, starts the own-rows consumer plus the
// roster consumer, and one consumer per opponent as they are discovered.
func (e *Engine) Start() error

// Stop tears down all goroutines cleanly.
func (e *Engine) Stop()

// Move input is delivered through MoveType values dispatched onto the internal
// moves channel and is only acted on when mode == ModePlayer. (The native and
// web front ends translate key/HTTP input into these moves.)

// transitionToSpectator is called internally when the game ends for the local
// player. It sets mode = ModeGameOver and emits UpdateGameOver{Won}. It does not
// itself stop the gravity/move goroutines — those self-exit because they guard on
// mode == ModePlayer — and the consumers keep running.
func (e *Engine) transitionToSpectator(won bool)
```

#### `consumer.go`

Manages the ordered consumer goroutine(s). In cooperative mode, ONE consumer runs on the shared row subjects (no player token — the subject carries no player segment), updating the single shared `Playfield`. In competitive mode, 1 + N consumers run — one for the local player's rows and one per opponent — each updating a separate `Playfield` instance.

```go
func (e *Engine) runConsumer(ctx context.Context, pf *game.Playfield, filterSubject, opponentID string, startSeq uint64, isOpponent bool)
```

**Startup sequence:**

1. Call `nats.FetchGameMeta(gameID)` — returns `GameMeta` including `Seed`, `PieceIdx`, and `Status`. In **both** modes `e.seq = rng.New(meta.Seed)`. In competitive mode `e.pieceIdx = meta.PieceIdx`; in cooperative mode `e.pieceIdx = 0` and each player tracks its own index independently (the sequence is shared, not forked with `seed+1`). `e.playerIdx` was supplied at construction (from `lobby.JoinGame`); no discovery is done here. `e.playerCount` and `e.visibleRowStart` are set from meta, and the playfield is (re)allocated at the mode-appropriate width/height.
2. Call `nats.FetchPlayfieldState(gameID, subjects)` for the player's own row subjects (coop: the shared `playfield.row.*` subjects with no player token; competitive: the player's own `player.<pid>.playfield.row.*`). Apply all rows to `e.playfield` via `pf.Apply`. Record `maxSeq = max(all row sequences)`.
3. Start the ordered consumer with `startSeq = maxSeq + 1`. In cooperative mode this is ONE consumer on the shared row subjects (`jetricks.game.<id>.playfield.row.>`). In competitive mode this is the consumer for the player's own rows. Messages on non-row subjects (events, meta, chat) that arrived between the lowest and highest fetched row sequence are a tolerable gap — at most a few milliseconds of game time.
4. In competitive mode only, also start the **roster consumer** (`runRosterConsumer`, watching `jetricks.game.<id>.roster.*`) which discovers opponents dynamically and calls `startOpponentConsumer` for each — fetching that opponent's rows and starting one `runConsumer` per opponent targeting `jetricks.game.<id>.player.<opponentPID>.playfield.row.>`. A known opponent passed at construction is started immediately; late joiners are picked up as their roster entries appear. In cooperative mode there is no opponent consumer — both players write to and read from the same shared row subjects.

**Cooperative mode design:**

In cooperative mode both players share a SINGLE wide playfield of width `playerCount × StandardWidth` (20 columns for 2 players). Row subjects carry no player token — the shared board publishes to `jetricks.game.<id>.playfield.row.<n>` (every player publishes to and consumes from the same subjects) via the `config.CoopRowSubject` scheme, distinct from the competitive `config.CompetitiveRowSubject` scheme. Per-player filtering is never needed in coop, so the player identity lives entirely in the payload rather than the subject. Both players' active pieces exist on the same playfield and can move anywhere on it — they are not restricted to their own section. Each cell of an active piece is tagged with `Cell.PlayerIdx` (0 for creator, 1 for joiner) so the engine can distinguish which player's piece each cell belongs to.

Each player spawns their piece centered in their section (player 0: center of cols 0–9, player 1: center of cols 10–19) but can move it anywhere on the full-width board. `ActivePieceForPlayer(playerIdx)` finds only the piece belonging to that player (by matching `Cell.PlayerIdx`). `SetActivePieceForPlayer(p, playerIdx)` only clears active cells with matching `PlayerIdx` before setting new ones. Collision detection (`CanPlaceCoop`) treats the other player's active cells as obstacles in addition to locked cells.

Both players seed their RNG from the same `meta.Seed` but track their own `pieceIdx` independently, so they receive different pieces at any given moment. Each engine has ONE playfield (the shared one) and ONE ordered consumer (on the shared row subjects) — no separate opponent playfield is needed. Both players write to the same shared row subjects. CAS conflicts on **moves** (left, right, down, rotate) are NOT retried — the move is simply dropped and the player must try another move. CAS conflicts on **state changes** (lock-in, spawn, line clear) ARE retried with a direct fetch from the stream, since these must succeed for game consistency.

Line clears work on the full 20-wide rows. The score is shared — both players' line clears contribute to the same score total. The UI renders the single wide playfield directly (no concatenation of two separate playfields).

**Per-message handling (cooperative mode — single shared playfield consumer):**

- Decodes the row message and calls `pf.Apply(rowIndex, row, seq)`, updating both cell data and `LastSeq` for that row.
- After every row update, scans for the **implicit lock-in signal** for this player's piece: if the previous state had an active piece for this `playerIdx` (`ActivePieceForPlayer(playerIdx) != nil`) and the new state has no active cells with matching `PlayerIdx`, a lock-in has just been committed by this player. The engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to spawn the next piece centered in this player's section.
- Emits a `UpdatePlayfield` for the changed rows so the UI re-renders from the freshly applied `e.playfield`.
- On line-clear detection: checks the full-width playfield for completed rows. **Critically, the cleared rows are published synchronously before spawning the next piece** — this prevents a race condition where the spawn modifies the playfield while the clear is still being published. The score is updated and emitted to the UI. Level is recomputed and the gravity interval adjusted.
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

**Per-message handling (competitive mode — own and opponent playfield consumers):**

- Decodes the row message and calls `pf.Apply(rowIndex, row, seq)`, updating both cell data and `LastSeq` for that row.
- After every row update on the own playfield, scans for the **implicit lock-in signal**: if the previous state had an active piece and the new state has no active cells anywhere, a lock-in has just been committed. The engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to determine the next piece.
- Emits a `UpdatePlayfield` for the changed rows so the UI re-renders from the freshly applied `e.playfield`.
- On receiving a message on the events subject: if it is a shrink event from another player (`ev.PlayerID != e.playerID`), calls `applyOpponentShrink` which publishes the row shift batch to the local player's own rows. In 3+ player games, every opponent applies the same shrink independently.
- On line-clear detection: checks own playfield for completed rows. Cleared rows are published synchronously before spawning the next piece.
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

#### Input + gravity loop (`runInput`, in `move.go`)

```go
func (e *Engine) runInput(ctx context.Context)
```

The engine's single gameplay-write goroutine: it `select`s over the moves channel (player input) and the gravity timer. Running both on **one** goroutine is deliberate — a player's own gravity drop and a player move can never publish to their row subjects concurrently, so they can never lose the per-subject CAS race against each other (in either mode; this removed the spurious rainbow flashes seen in competitive play). On each gravity tick it attempts to drop the active piece one row via `attemptMove(MoveDown, true)`; player input calls `attemptMove(move, false)`. In cooperative mode it reads the current level from `totalLines` after each tick and adjusts the ticker interval when the level changes; in competitive mode the interval is fixed.

**Cooperative gravity and lock-in:** When gravity cannot move a piece down, the engine distinguishes between two cases: (1) the piece is blocked by locked cells or out-of-bounds — the piece locks immediately, as in standard Tetris; (2) the piece is blocked only by the other player's active piece — the piece does NOT lock, since that obstacle is temporary (it will itself fall on its next gravity tick). In case (2), gravity simply waits and tries again on the next tick. This prevents premature lock-ins caused by two pieces passing through the same rows.

**Cooperative hard drop:** When a player hard-drops (space bar), the piece falls instantly to the lowest valid position — which may be on top of the other player's active piece. If the piece lands on locked cells or the floor, it locks immediately as usual. If it lands on the other player's active piece, it does NOT lock — instead it stays active and resumes falling by gravity. The other player's piece will itself fall on its next gravity tick, at which point gravity will continue dropping this piece further.

#### `move.go`

```go
// attemptMove is the central move dispatch function. internal=true marks an
// engine-driven move (e.g. a gravity tick); internal=false marks player input
// (player input drops + flashes on CAS failure, gravity ticks merge-retry in
// coop). It validates the move geometrically against the local playfield, builds
// the projected row updates, and publishes them via the publishProjected* helpers
// in engine.go. It dispatches by mode to attemptMoveStandard / attemptMoveCoop.
func (e *Engine) attemptMove(ctx context.Context, move MoveType, internal bool) error
func (e *Engine) attemptMoveStandard(ctx context.Context, move MoveType, internal bool) error
func (e *Engine) attemptMoveCoop(ctx context.Context, move MoveType, internal bool) error

// MoveType is defined in events.go.
type MoveType int

const (
    MoveLeft MoveType = iota
    MoveRight
    MoveDown    // used by gravity ticker and soft-drop key
    RotateCW
    RotateCCW
    MoveHardDrop
)
```

#### Publish & CAS helpers (`engine.go` / `move.go`)

There is no `Publish`/`PublishHardDrop`/`ErrMoveDropped`/`ErrLockIn` API and no 50ms-wait-on-`rowUpdated` retry loop. All publish/CAS logic lives in `engine.go` (and the hard-drop helpers in `move.go`). The relevant helpers are:

```go
// publishProjectedRows publishes a map of row payloads as ONE atomic batch with
// per-subject CAS expectations sourced from e.playfield.LastSeq[r]. On CAS
// failure the step is DROPPED (no retry, no further publish) and the local
// player is signalled with a rainbow flash on flashCells (pass nil to suppress).
// Used for player moves, rotations, and competitive spawns/gravity ticks.
func (e *Engine) publishProjectedRows(ctx context.Context, rows map[int]game.Row, flashCells [][2]int, bottomFirst bool)

// publishProjectedRowsNoCAS publishes a map of rows as ONE atomic batch with NO
// CAS expectations — used for authoritative state (competitive lock, hard-drop
// landing, line-clear, opponent-shrink application). applyBottomFirst orders the
// batch so a row completed by a hard drop is in place when lock-in fires.
func (e *Engine) publishProjectedRowsNoCAS(ctx context.Context, rows map[int]game.Row, applyBottomFirst bool)

// publishProjectedRowsSliceNoCAS publishes a contiguous []Row (fromRow..toRow)
// NoCAS — used by the line-clear / shrink paths that produce a row slice.
func (e *Engine) publishProjectedRowsSliceNoCAS(ctx context.Context, rows []game.Row, fromRow, toRow int)

// publishProjectedRowsWithMergeRetry is the COOP path for steps that MUST land
// (spawn, gravity tick, lock, hard drop, line clear) on the shared board. On CAS
// failure it refetches each affected row from the stream, overlays this player's
// cells (refetchAndMerge), and retries with refreshed per-subject CAS — up to 5
// attempts, then drops + flashes.
func (e *Engine) publishProjectedRowsWithMergeRetry(ctx context.Context, rows map[int]game.Row, flashCells [][2]int, bottomFirst bool)

func (e *Engine) refetchAndMerge(ctx context.Context, saved map[int][]game.Cell, bottomFirst bool) ([]natspkg.RowUpdate, bool)
func (e *Engine) buildBatchUpdates(rows map[int]game.Row, bottomFirst bool) ([]natspkg.RowUpdate, error)
```

There is no recompute-and-retry-until-it-lands hard-drop loop. The hard-drop destination is computed **once** (`game.HardDropDestination` / `HardDropDestinationCoop`). In competitive mode the landing rows are published NoCAS (`publishHardDrop`); in cooperative mode through the merge-retry path (`publishHardDropCoop`, ≤5 retries). `bottomFirst`/`applyBottomFirst` is `true` for hard drops and downward moves so a line completed by the drop is detected at the lock, not one piece later. See the publish-strategy summary below.

#### `events.go`

Defines the `EngineUpdate` type sent from engine to UI over the `Updates` channel, and the event message format published to `jetricks.game.<id>.events`.

```go
type UpdateKind int

const (
    UpdatePlayfield        UpdateKind = iota  // one or more rows changed
    UpdatePieceLocked                         // active piece locked in
    UpdateLineClear                           // lines cleared, rows shifted
    UpdateGameOver                            // game ends for this player
    UpdateOpponentField                       // competitive: opponent's field changed (live view)
    UpdateOpponentShrink                      // competitive: opponent's field shrank (our line clear)
    UpdateScore                               // score changed
    UpdateLevel                               // cooperative: level changed
    UpdateGameStatus                          // game lifecycle status changed
    UpdateCountdown                           // pre-game countdown tick
    UpdatePlayerEliminated                    // competitive: a player was eliminated
    UpdateCASFlash                            // a CAS-failure flash should be rendered
)

type EngineUpdate struct {
    Kind               UpdateKind
    ChangedRows        []int    // for UpdatePlayfield, UpdateLineClear, UpdateOpponentField
    Score              int      // for UpdateScore
    Level              int      // for UpdateLevel
    GameStatus         string   // for UpdateGameStatus
    Countdown          int      // for UpdateCountdown: seconds remaining (0 = GO!)
    Won                bool     // for UpdateGameOver: true if this player won (competitive)
    EliminatedPlayerID string   // for UpdatePlayerEliminated: which player
    OpponentID         string   // for UpdateOpponentField/UpdateOpponentShrink: which opponent
    FlashCells         [][2]int // for UpdateCASFlash: cells to flash
    FlashPlayerIdx     int      // for UpdateCASFlash: player index for flash color
}
```

**Game events published to `jetricks.game.<id>.events`:**

```go
// EventKind identifies the type of game event.
type EventKind string

const (
    EventLineClear EventKind = "line_clear"
    EventShrink    EventKind = "shrink"
    EventGameOver  EventKind = "game_over"
)

// GameEvent is the JSON payload published to the events subject.
type GameEvent struct {
    Kind         EventKind `json:"kind"`
    PlayerID     string    `json:"player_id"`               // who caused/detected the event
    LinesCleared int       `json:"lines_cleared,omitempty"` // for EventLineClear
    TargetPlayer string    `json:"target_player,omitempty"` // present but unused — shrink is broadcast to all
    RowsRemoved  int       `json:"rows_removed,omitempty"`  // for EventShrink: how many rows
    ClearedRows  []int     `json:"cleared_rows,omitempty"`  // for EventLineClear: which rows
    Score        int       `json:"score,omitempty"`         // EventGameOver: final score; EventLineClear (coop): score delta
    PieceCount   uint64    `json:"piece_count,omitempty"`   // for EventGameOver: total pieces placed
    PlayerIdx    int       `json:"player_idx,omitempty"`    // causer's index (for EventShrink)
}
```

**Shrink flow (competitive mode):**

1. Player A's engine detects a line clear after a lock-in (implicit detection from row state).
2. Player A publishes an atomic batch: the row shift on its own playfield rows (cleared lines removed, rows above shifted down).
3. Player A also publishes a `GameEvent{Kind: EventShrink, PlayerID: playerA, RowsRemoved: n, PlayerIdx: ...}` to the events subject. The `TargetPlayer` field exists on `GameEvent` but is unused for shrink — the event is broadcast and ALL other players apply it.
4. Every other player's events consumer reads the shrink event. Since `ev.PlayerID != e.playerID`, each opponent calls `applyOpponentShrink(n)` which shifts their own playfield up by n rows and adds n fully occupied permanent adversarial rows at the bottom. In a 3+ player game, all opponents are shrunk simultaneously. Adversarial cells are marked with `Cell.Adversarial = true` and rendered with a distinct grey color. Adversarial rows can never be completed or cleared — `IsFull()` returns false for any row containing adversarial cells.
5. The shifted state is published using NoCAS (authoritative, same as line clears) to prevent stale consumer messages from undoing the shift. The opponent's own falling piece holds its position while the stack rises and is pushed up only as far as the rising stack/garbage forces it; `ProjectShrink` resolves the minimal lift (0..`rowsToAdd`) and returns a `topOut` flag. If no lift keeps the piece on the board, `applyOpponentShrink` calls `handleTopOut`. See `jetricks-gameplays.md` for the full competitive shrink rules.

**Score tracking:**

In **cooperative mode** the team score is a plain local `score int`. When a player clears lines it adds `playerCount × lines` to its own `score` (reflecting the harder-to-fill wider playfield) and publishes a `GameEvent{Kind: EventLineClear, Score: delta}` on the events subject; every other player's events consumer folds that delta into its own local `score` so all clients converge on the same combined team total. This is **not** a server-side counter CRDT and uses no score subject. See `jetricks-gameplays.md` for the authoritative scoring rules.

**Line clear publishing:** Cleared rows are published using a no-CAS publish (the cleared state is authoritative). This prevents the CAS retry merge logic from restoring old occupied cells from stale NATS data, which would effectively undo the clear. After the cleared rows are published, `LastSeq` is updated from the publish acknowledgment so subsequent CAS publishes use the correct sequence.

**CAS failure recovery:** After a no-CAS line-clear publish, the other player's engine has stale `LastSeq` values until its consumer processes the clear messages. During this window, their moves may fail with CAS errors. When a move publish fails (CAS on any row), the engine immediately fetches the latest row state from NATS via direct get and corrects both the in-memory row data and `LastSeq`. This ensures the display stays in sync with NATS even when moves are dropped due to stale sequences.

In **competitive mode** each player keeps its own local `score int`, incremented by the number of lines it clears. The score is reported to other clients only at game end via the `EventGameOver` event (and rendered locally via `UpdateScore`); the per-player `score` subject is not used.

**Top-out transition:**

When Player A's engine detects that the newly spawned piece (at the top of the playfield) cannot be placed without collision, `handleTopOut`:
1. Publishes `GameEvent{Kind: EventGameOver, PlayerID: playerA, Score: e.score, PieceCount: e.pieceIdx}` to the events subject.
2. Calls `e.transitionToSpectator(false)` — sets `mode = ModeGameOver` and emits `UpdateGameOver{Won: false}`. It does **not** itself stop the gravity ticker or move processor; those goroutines self-exit on their next iteration because they guard on `mode == ModePlayer`, and the consumers keep running. `handleTopOut` does not archive, delete the stream, or remove the KV entry.
3. In **cooperative mode**, any top-out ends the game for everyone: `handleTopOut` kicks off `transitionGameToFinished` (CAS the meta to `finished`).
4. In **competitive mode**, finishing is driven by last-player-standing in `handleGameEvent` rather than by `handleTopOut`: each engine tracks `eliminatedPlayers`; when a player receives game-over events for all but one player it calls `transitionToSpectator(true)` for itself if it is the survivor (win) and kicks off `transitionGameToFinished`. A simultaneous top-out (all eliminated) is a draw with no winner. The UI shows a player status list (playing/eliminated) and "YOU WON!"/"YOU LOST" at game over. See `jetricks-gameplays.md` for the authoritative game-over rules.

**Meta transition + game archiving:** `transitionGameToFinished` CAS-retries the meta status to `finished` (setting `FinishedAt`), then — after `time.Sleep(5 * time.Second)`, giving every player time to receive the game-over — invokes `OnGameFinished`, which the front end wires to `archive.ArchiveAndCleanup`. That callback CAS-transitions the meta `finished → archived`, publishes an `ArchiveRecord` to the `JETRICKS_ARCHIVE` stream (subject `jetricks.archive`) with game ID, mode, player count, per-player results (ID, score, piece count, winner), start/finish timestamps, and total score (cooperative), then deletes the game stream and removes the KV entry. Archiving is therefore **delayed by ~5 s after game end**, not immediate, and is CAS-protected so only one client performs it.

---

## 10. internal/lobby

Manages all lobby-level state: player presence, game listings, global chat, and the lifecycle operations (create game, join game, leave game). Does not know about the UI layer.

### Files

#### `lobby.go`

```go
type Lobby struct {
    playerID string
    name     string
    kv       jetstream.KeyValue
    js       jetstream.JetStream

    // Channels for outbound events to the UI layer
    Updates chan LobbyUpdate

    // mu protects players, games, and archives. The KV/chat/archive watcher
    // goroutines hold the write lock when updating them; UI handler goroutines
    // hold the read lock when reading them.
    mu       sync.RWMutex
    players  map[string]PlayerPresence  // keyed by playerID — access via Players()
    games    map[string]GameListing     // keyed by gameID — access via Games()
    archives []config.ArchiveRecord     // game history — access via Archives()

    status          PresenceStatus       // local player's current presence status
    currentGameID   string               // game the local player is in, if any
    cancelFn        context.CancelFunc
    initialLoadDone chan struct{}         // closed when the KV watcher finishes its initial load
}

// Players returns a shallow copy of the current player presence map.
// Safe to call from any goroutine; the caller receives a consistent snapshot
// that will not be mutated after return.
func (l *Lobby) Players() map[string]PlayerPresence

// Games returns a shallow copy of the current game listing map.
func (l *Lobby) Games() map[string]GameListing

// Archives returns a shallow copy of the archive records (game history).
func (l *Lobby) Archives() []config.ArchiveRecord

// New takes no ctx and returns *Lobby only (no error).
func New(
    js jetstream.JetStream,
    kv jetstream.KeyValue,
    playerID string,
    name string,
) *Lobby

func (l *Lobby) Start(ctx context.Context) error
func (l *Lobby) WaitForInitialLoad(ctx context.Context) error
func (l *Lobby) Stop()

func (l *Lobby) CreateGame(ctx context.Context, mode config.GameMode, playerCount int) (string, error) // playerCount is 2–4, selected by the user in the create game form
// JoinGame returns the caller's player index (0 for creator, 1 for first joiner, …),
// which is passed to engine.New as playerIdx.
func (l *Lobby) JoinGame(ctx context.Context, gameID string) (int, error)
func (l *Lobby) LeaveGame(ctx context.Context, gameID string) error
// ToggleReady toggles the local player's ready state and returns a snapshot:
// whether all players are now ready, the player list, and the caller's new state.
func (l *Lobby) ToggleReady(ctx context.Context, gameID string) (ToggleReadyResult, error)
func (l *Lobby) StartGame(ctx context.Context, gameID string)  // transitions game to in_progress after countdown
func (l *Lobby) SendChat(ctx context.Context, text string) error

type ToggleReadyResult struct {
    AllReady bool
    Players  []PlayerSummary
    MyReady  bool
}
```

The maps are unexported and accessed only through `Players()` and `Games()`, ensuring all reads hold the read lock and all writes hold the write lock. The KV watcher goroutine (in `listing.go`) calls `l.mu.Lock()` / `l.mu.Unlock()` around every map mutation. UI SSE handlers call `l.Players()` / `l.Games()` which take `l.mu.RLock()`, copy the map, and release before returning. The copy is a shallow copy of the map (new map, same value structs) — since `PlayerPresence` and `GameListing` are value types, this is safe.

#### `presence.go`

```go
// runHeartbeat publishes a presence update to the lobby KV bucket every
// PresenceHeartbeat interval and, each tick, calls pruneStalePresence. On
// context cancellation it deletes the local player's presence key and returns.
func (l *Lobby) runHeartbeat(ctx context.Context)

// pruneStalePresence drops players whose LastSeen is older than 3× the heartbeat
// interval (the local player is never pruned). This is how stale entries expire,
// in place of any KV TTL.
func (l *Lobby) pruneStalePresence()

// IsNameInUse reports whether an active (non-stale) presence entry already uses
// the given name (case-insensitive, whitespace-trimmed).
func IsNameInUse(ctx context.Context, kv jetstream.KeyValue, name string) (bool, error)

type PlayerPresence struct {
    PlayerID string         `json:"player_id"`
    Name     string         `json:"name"`
    Status   PresenceStatus `json:"status"`
    GameID   string         `json:"game_id,omitempty"` // non-empty if in a game or spectating
    LastSeen time.Time      `json:"last_seen"`         // heartbeat timestamp; staleness check
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
    GameID      string            `json:"game_id"`
    Mode        config.GameMode   `json:"mode"`
    Status      config.GameStatus `json:"status"`        // the string status type from config
    PlayerCount int               `json:"player_count"`  // configured max players
    Players     []PlayerSummary   `json:"players"`       // currently joined players
    CreatedAt   time.Time         `json:"created_at"`
    FinishedAt  time.Time         `json:"finished_at,omitempty"` // zero if not finished
}

// There is no lobby-local GameStatus type. GameListing.Status uses
// config.GameStatus (a string type with GameStatusCreated/Starting/InProgress/
// Finished/Archived/Cancelled, defined in internal/config).

type PlayerSummary struct {
    PlayerID string `json:"player_id"`
    Name     string `json:"name"`
    Ready    bool   `json:"ready"`
}
```

#### `events.go`

```go
type LobbyUpdateKind int

const (
    LobbyUpdatePlayers   LobbyUpdateKind = iota  // player list changed
    LobbyUpdateGames                              // game listing changed
    LobbyUpdateChat                               // new chat message
    LobbyUpdateArchive                            // game history (archive) changed
)

type LobbyUpdate struct {
    Kind    LobbyUpdateKind
    ChatMsg *ChatMessage  // non-nil for LobbyUpdateChat
}

type ChatMessage struct {
    PlayerID  string    `json:"player_id"`
    Name      string    `json:"name"`
    Text      string    `json:"text"`
    Timestamp time.Time `json:"timestamp"`
    Spectator bool      `json:"spectator,omitempty"`
}
```

---

## 11. internal/cleanup

**File:** `cleanup/cleanup.go`

Runs once at startup, after the ordered consumer on the lobby KV has caught up to current state. Inspects all known game streams and lobby KV entries and resolves any stale or abandoned state.

It enumerates game streams using only `natspkg.ListGameStreams` (the JetStream `StreamNames` API filtered to the `jetricks.game.>` subject) — there is no `orbit.go/natssysclient`, no `Jsz`/system-account query, and no system-account fallback.

### Key Function

```go
// Run performs the full startup cleanup pass.
// Must be called after lobby state is fully loaded.
// ctx should have a reasonable timeout (e.g. 30s).
func Run(ctx context.Context, js jetstream.JetStream, kv jetstream.KeyValue, lobby *lobby.Lobby) error
```

Orphaned-stream detection relies solely on the JetStream `StreamNames` listing compared against the lobby KV game entries.

### Cleanup Cases (in order of evaluation)

| Condition | Action (`cleanup.go`) |
|-----------|--------|
| Status `finished` (orphaned — not yet archived) | `archiveGame`: CAS-transition meta `→ archived`, `SealGameStream`, and update the KV listing to `archived` (this is the only place a stream is sealed) |
| Status `created`, creator absent from KV | `cancelGame`: CAS-transition `→ cancelled`, delete stream, remove KV entry |
| Status `starting`, all rostered players absent from KV | `cancelGame`: CAS-transition `→ cancelled`, delete stream, remove KV entry |
| Status `in_progress`, all players absent from KV | `finishAbandonedGame`: CAS-transition `→ finished` with `abandoned: true` and `FinishedAt` (a later pass then archives it) |
| `JETRICKS_GAME_<id>` stream exists, no matching KV entry | If meta status is `in_progress`/`starting`, re-create the KV listing (don't delete a live game); otherwise delete the orphaned stream (also delete if meta can't be read) |

Note: During normal play the engine archives a finished game ~5 s after game end via `OnGameFinished` → `archive.ArchiveAndCleanup` (delete stream + remove KV; see Section 9). The cleanup pass handles only games left in a stale state by a crash or disconnect, and seals (rather than deletes) an orphaned finished stream.

### CAS Coordination

All transitions go through CAS on `jetricks.game.<id>.meta`. If a CAS fails during cleanup, the function re-reads the current status and re-evaluates. A failed CAS means another client already handled that game — no further action is needed.

---

## 12. Front ends: native (default) and web (`--web`)

Jetricks has two interchangeable front ends over the same engine/lobby logic. Both depend on `engine` and `lobby` (one-way) and communicate with them exclusively through their `Updates` channels and exported method calls — neither is imported by the business logic.

- **`internal/nativeui` (default).** A native OS window built with **Gio** (`gioui.org`, pure-Go, cross-platform). It reads `engine.Updates` / `lobby.Updates` directly in bridge goroutines and repaints via `window.Invalidate()`, and it calls `engine.MoveLeft()` etc. directly from a key handler — so there is **no HTTP or SSE round-trip**; a NATS update reaches the screen within one display frame. Files: `app.go` (window + frame loop + screen state machine), `bridge.go` (the `pumpEngine`/`pumpLobby` channel→UI pumps), `login.go`/`lobby.go`/`game.go` (screens), `board.go` (board drawing), `input.go` (keyboard → engine moves), `lifecycle.go` (login/create/join/spectate/countdown/teardown), `colors.go` alias to `internal/render`. Controls: ←/→ move, ↓ soft drop, ↑ or X rotate CW, Z rotate CCW, Space hard drop. Keyboard focus uses Gio's `key.FocusFilter` + `key.FocusCmd` on the board tag.
- **`internal/ui` (web, `--web`).** The HTTP server + Datastar/SSE rendering described below.

Two small packages are shared by both front ends:
- **`internal/render`** — the single source of truth for cell/board appearance (piece/player colors, blend math). Exposes `CellStyleCSS` (web, byte-for-byte the historical output) and `CellStyle`→RGBA (native) from one decision function, so the two UIs can never visually drift. Extracted from `internal/ui/handlers.go`.
- **`internal/archive`** — `ArchiveAndCleanup(ctx, js, kv, eng, lb, gamePlayers)`, wired as `engine.OnGameFinished`; records the finished game to the archive stream and tears down its NATS resources. Shared so both UIs archive identically.

### 12a. internal/ui (web UI — only with `--web`)

The HTTP server and all UI rendering. Depends on `engine` and `lobby` but is never imported by them — the dependency is one-way. Communicates with engine and lobby exclusively through their `Updates` channels and exported method calls.

### Files

#### `server.go`

```go
type Server struct {
    port   int
    js     jetstream.JetStream
    kv     jetstream.KeyValue
    lobby  *lobby.Lobby  // nil until player logs in
    router *http.ServeMux
    srv    *http.Server
    ctx    context.Context

    mu          sync.Mutex
    engine      *engine.Engine
    gamePlayers []lobby.PlayerSummary // players in the current game (for spectator legend)

    // Broadcasters fan an Updates channel out to all open SSE connections.
    lobbyBroadcaster *Broadcaster[lobby.LobbyUpdate]
    gameBroadcaster  *Broadcaster[engine.EngineUpdate]
}

func New(port int, js jetstream.JetStream, kv jetstream.KeyValue) *Server
func (s *Server) Start(ctx context.Context) error
func (s *Server) Stop()

// AttachEngine registers an active game engine with the server, pumping its
// Updates channel into gameBroadcaster (one goroutine fans it out to every open
// game SSE connection). Called when the local player joins/creates/spectates a game.
func (s *Server) AttachEngine(e *engine.Engine)

// DetachEngine unregisters the engine when the game ends.
func (s *Server) DetachEngine()
```

Routes (registered in `registerRoutes`):
- `GET /` — login page (if no lobby) or lobby view (initial HTML)
- `POST /login` — validate player name, create lobby
- `GET /lobby/stream` — Datastar SSE stream for lobby updates
- `POST /lobby/chat` — send a lobby chat message
- `POST /lobby/game/create` — create a new game
- `POST /lobby/game/{id}/join` — join a game
- `POST /lobby/game/{id}/spectate` — spectate an in-progress game (creates engine in ModeSpectator)
- `POST /lobby/quit` — quit/leave the lobby
- `GET /game` — game view (initial HTML)
- `GET /game/stream` — Datastar SSE stream for game updates
- `POST /game/move` — player move input
- `POST /game/ready` — toggle ready state

There is **no** `sse.go` and **no** templ files: the web UI does not use SSEWriter wrappers or generated templates. SSE is provided directly by the **datastar-go SDK** (`github.com/starfederation/datastar-go/datastar`): each handler calls `datastar.NewSSE(w, r)` and emits `sse.PatchElements(html, datastar.WithSelectorID(...))` / `sse.PatchSignals(...)` / `sse.ExecuteScript(...)`. HTML fragments are produced by plain Go string helpers in `handlers.go` (e.g. `renderBoard`, `renderPlayerList`, `renderGameList`, `renderArchiveTable`, `renderReadyList`, `renderPlayerLegend`, `renderScoreInner`, `renderLevelInner`), and cell appearance comes from `internal/render`. There are no `ui/lobby`, `ui/game`, or `ui/shared` Go packages — those directories exist but are empty.

#### `handlers.go`

All HTTP handlers and HTML rendering. The lobby stream handler subscribes to `lobbyBroadcaster`, sends an initial full render (player list, game list, archive table, chat), then loops on the subscription patching the affected fragments per `LobbyUpdate.Kind` (chat is appended via `datastar.WithModeAppend()`). The game stream handler subscribes to `gameBroadcaster` and, per `EngineUpdate.Kind`, patches the board, score, level, countdown, ready list, player legend, and game-over UI. In competitive mode it distinguishes own-field updates (`UpdatePlayfield`) from opponent updates (`UpdateOpponentField`, keyed by `OpponentID`) and patches the corresponding sidebar board. In cooperative mode the single wide playfield (playerCount × StandardWidth columns) is rendered directly — already the correct width, so there is no concatenation, special template, or visual separator between player sections.

#### `broadcast.go`

A generic fan-out helper used for both the lobby and game update streams:

```go
type Broadcaster[T any] struct { ... }

func NewBroadcaster[T any]() *Broadcaster[T]
func (b *Broadcaster[T]) Subscribe() (<-chan T, func()) // per-connection channel (large buffer) + unsubscribe
func (b *Broadcaster[T]) Send(v T)                      // non-blocking; drops on a full subscriber buffer
func (b *Broadcaster[T]) Close()
```

The server holds `lobbyBroadcaster *Broadcaster[lobby.LobbyUpdate]` and `gameBroadcaster *Broadcaster[engine.EngineUpdate]`; one pump goroutine per source copies from the `Updates` channel into the broadcaster, and each SSE connection `Subscribe()`s for its own buffered channel.

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

**HUD and page rendering.** The HUD elements (score, level, next-piece preview, player status / "Spectating", countdown, ready list, game-over overlay) are produced by the same Go string helpers in `handlers.go` and patched into stable DOM element IDs over the game SSE stream — there is no separate `hud.templ`. The full lobby and game page shells (`loginPageHTML`, `lobbyPageHTML`, plus the game page) are emitted by `handleRoot`/`handleGamePage`; each shell includes the Datastar script tag and establishes its SSE connection with `data-on-load="@get('/lobby/stream')"` / `data-on-load="@get('/game/stream')"`. Chat fragments reuse the lobby chat render helper.

**Ready/countdown flow:** While waiting for the game to start, each player sees the list of players with their ready state (green checkmark or red cross). Players toggle their ready state via the READY/NOT READY button (`POST /game/ready` → `lobby.ToggleReady`). When ALL players are ready, the button and player list are replaced by a 5-second countdown (5...4...3...2...1...GO!). During the countdown, players cannot change their ready state. After the countdown, the game transitions to `in_progress` and pieces begin to spawn.

The game over overlay is patched in from the game SSE handler on `UpdateGameOver`: in cooperative mode any top-out ends the game for all; in competitive mode it shows "YOU WON!"/"YOU LOST" once the player is eliminated or is the last standing. The game page hides controls and the ready button for spectators, showing "Spectating" as the player status instead.

---

## 13. Event Channel Contracts

All cross-package communication uses buffered Go channels. The buffer size is chosen to absorb brief bursts without blocking the sender goroutine.

| Channel | Direction | Buffer | Notes |
|---------|-----------|--------|-------|
| `engine.Updates` | engine → front end | 64 | High-frequency during play (gravity ticks, every row update). Consumed by the native bridge or, in `--web`, pumped into `gameBroadcaster`. Dropping updates here is preferable to blocking the engine. If the channel is full the engine drops the update — the next update will correct the display. |
| `lobby.Updates` | lobby → front end | 16 | Lower frequency. Lobby changes are infrequent relative to game updates. |
| `engine.moves` (internal) | front end → engine | 8 | Player move requests dispatched onto the engine's internal moves channel (`runInput` reads it). Inputs are **serialized and buffered**: `runInput` processes them one at a time and each move's publish blocks on its batch commit ack (then applies the write-through) before the next move is dequeued, so a player never has two input batches in flight — a move issued while the previous one is still awaiting its ack waits in this buffer. The non-blocking send drops excess input rather than blocking the UI goroutine if a player outruns the ack round-trip by more than the buffer depth (not reached at human input rates). |

Channels are never closed by the sender — they are abandoned when the owning goroutine exits via context cancellation. Receivers must always select on both the channel and `ctx.Done()`.

---

## 14. Bootstrap Sequence

The following steps happen in order at startup. Steps that can fail cause the application to exit with a clear error message.

```
1.  Parse CLI flags → config.Config
2.  Connect to NATS via natscontext.Connect(config.NATSContext) → *nats.Conn, jetstream.JetStream, natscontext.Settings
    (empty context name uses the currently selected nats CLI context; --server overrides)
3.  EnsureLobbyChatStream
3a. EnsureArchiveStream
4.  EnsureLobbyKV
5.  Branch on config.Web:
    DEFAULT (native):                          --web (browser):
      5a. runNative: build nativeui.App          5b. runWeb: create + Start ui.Server (HTTP listener)
      5b. goroutine calls App.Run (opens the         open browser/webview at http://localhost:<port>
          Gio window); app.Main() owns the
          OS main thread
      → native window shows the login screen     → browser shows the login page
6.  Player enters name (login screen / POST /login): validate (config.ValidatePlayerName),
    check lobby.IsNameInUse, then:
    a. Create lobby.Lobby with playerName as both playerID and name
    b. Start lobby (KV watcher, chat consumer, archive consumer, heartbeat)
    c. Wait for initial KV load
    d. Run cleanup.Run
    e. Move to the lobby screen
7.  Block on window close / os.Signal (SIGINT / SIGTERM)
8.  On exit: stop engine + lobby, cancel root context → goroutines exit, Drain/Close NATS
12. Exit
```

Step 8c — waiting for the KV watcher to finish its initial load — is critical for correctness of the cleanup pass (step 8d). The initial load is complete when the KV watcher receives a nil entry, which NATS delivers after all existing entries have been sent.

---

## 15. Key Interfaces

The codebase does **not** define decoupling interfaces such as `Publisher`, `KVStore`, or `Playfield`. The engine uses `jetstream.JetStream` and the concrete `*game.Playfield` directly, and the lobby uses `jetstream.KeyValue` directly. Rather than mocking these behind interfaces, integration tests run against a real embedded NATS server provided by `internal/testutil` (see Section 18), so the production NATS code paths are exercised end-to-end instead of substituted.

---

## 16. Goroutine Inventory

All goroutines are started with a context derived from the root context and exit cleanly on cancellation. No goroutine is started without a corresponding documented exit path.

| Goroutine | Owner | Started | Exits on |
|-----------|-------|---------|----------|
| Lobby KV watcher (`runKVWatcher`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Lobby chat consumer (`runChatConsumer`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Lobby archive consumer (`runArchiveConsumer`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Lobby presence heartbeat (`runHeartbeat`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Own-rows consumer (`runConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Events consumer (`runEventsConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Meta consumer (`runMetaConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Countdown consumer (`runCountdownConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Input + gravity loop (`runInput`) | `engine.Engine` | `engine.Start()` (ModePlayer only) | ctx cancel |
| Roster consumer (`runRosterConsumer`) | `engine.Engine` | `engine.Start()` (competitive only) | ctx cancel |
| Per-opponent rows consumer (`runConsumer`) | `engine.Engine` | `startOpponentConsumer` per discovered opponent (competitive) | ctx cancel |
| Lobby/game update pumps + SSE connections | `ui.Server` (web) / native bridge | per pump and per HTTP connection (web) | client disconnect or ctx cancel |

---

## 17. orbit.go Module Reference

All orbit.go modules are independently versioned. Import only the modules needed rather than the whole library.

| Module | Import path | Used in | Purpose in Jetricks |
|--------|-------------|---------|-------------------|
| `natscontext` | `github.com/synadia-io/orbit.go/natscontext` | `internal/nats` | Connect using NATS CLI context files. Replaces raw URL + credential flags with a single context name, sharing config with the `nats` CLI tool. |
| `jetstreamext` | `github.com/synadia-io/orbit.go/jetstreamext` | `internal/nats` | Atomic batch publishing for move CAS operations. `GetLastMsgsFor` for instant playfield reconstruction on startup/reconnect (fetches last message per row subject in one round trip). |

These are the only two orbit.go modules used (`natsext` comes in as an indirect dependency). `counters` and `natssysclient` are **not** dependencies of Jetricks.

### Modules considered but not used

| Module | Reason not used |
|--------|----------------|
| `counters` | The cooperative score is a plain local `int` propagated via `EventLineClear` events on the events subject and summed locally — no server-side counter CRDT (and no `AllowMsgCounter` stream flag). |
| `natssysclient` | Cleanup detects orphaned streams with the plain JetStream `StreamNames` listing (`ListGameStreams`); no system-account `Jsz` query is needed. |
| `kvcodec` | Jetricks KV keys are already NATS-compatible (no dots, spaces, or special chars). Values are plain JSON. No encoding layer needed. |
| `natsext` (RequestMany) | Jetricks uses ordered consumers and direct publishes. Scatter-gather request/reply is not part of any game or lobby flow. (Present only as an indirect dependency.) |
| `pcgroups` | Jetricks uses ordered consumers for strict in-order delivery per client. Partitioned consumer groups target parallel work-queue consumption patterns, which is not applicable here. |

---

## 18. Testing Strategy

### Unit tests (no NATS required)

- `internal/game` — all functions are pure and take no external dependencies. Full coverage of piece rotation (all SRS wall kicks), collision detection, line clear detection, row serialisation, score and level calculation, gravity interval curve.
- `internal/rng` — verify determinism: two `Sequence` instances with the same seed produce identical output. Verify seek: `Piece(N)` equals the Nth output from sequential calls.
- `internal/config` — subject builder functions produce correct strings.

### Integration tests (require a NATS server)

- `internal/nats` — stream creation, KV operations, atomic batch publish happy path, CAS failure path, stream sealing, `FetchPlayfieldState` via `GetLastMsgsFor`. Tests use a local NATS context pointing at the test server so that `natscontext.Connect` is exercised end-to-end rather than bypassed.
- `internal/engine` — start an engine against a real NATS server with a test game stream. Submit moves and verify the playfield reaches the expected state. Simulate CAS failure by publishing a conflicting update from a second client. Verify the `FetchPlayfieldState` snapshot correctly seeds `LastSeq` before the ordered consumer starts. Verify cooperative score deltas propagated via `EventLineClear` converge to the same local total across two engine instances.
- `internal/lobby` — create/join/leave game operations, presence heartbeat expiry, KV watcher delivery.
- `internal/cleanup` — seed a NATS server with stale game streams in various states and verify cleanup produces the correct outcomes, including orphaned-stream deletion (via the `StreamNames` listing) when KV entries are missing.

The `internal/testutil` package (`nats.go`) provides helpers for spinning up an **embedded** NATS server for integration tests. Tests run against that real server rather than mocking NATS behind interfaces.

### End-to-end

Two engine instances running against a shared NATS server, simulating a competitive game. Assert that line clears on one side produce shrink events on the other, that the CAS mechanism correctly serialises simultaneous moves, and that the archive sequence runs correctly at game end (record published, then the game stream deleted — normal game end deletes the stream rather than sealing it).

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
| 8 | Cooperative score propagation | Plain local `score int`, propagated via `EventLineClear` events on the events subject and summed locally | No server-side counter CRDT is needed; the events stream the game already runs carries the deltas. The game stream sets `AllowAtomicPublish` and `AllowDirect` (not `AllowMsgCounter`). |
| 9 | Game ID format | UUID v4 with dashes (`550e8400-e29b-41d4-a716-446655440000`) | UUIDs are globally unique, collision-free, and NATS stream names allow dashes. |
| 10 | Game-over semantics | Cooperative: any top-out ends for all. Competitive: eliminated player becomes spectator; game continues until one player remains. | See `jetricks-gameplays.md`. |
| 11 | HardDrop CAS behaviour | Destination computed once; competitive publishes the landing NoCAS, coop via merge-retry (≤5). No recompute-and-retry-until-it-lands loop. | The landing is authoritative state, so NoCAS (competitive) or CAS+merge (coop, to protect the other player's shared-board cells) is the right tool — not an unbounded CAS retry. |
| 12 | Opponent display in competitive | Full live view via one ordered consumer per opponent's row subjects | Provides the same real-time fidelity as the player's own field. The overhead of additional consumers is minimal (at most 3 opponents in a 4-player game). |
| 13 | `pieceIdx` recovery on join/reconnect | Store `PieceIdx uint64` in `GameMeta`; locking engine CAS-updates it after each lock-in | `FetchGameMeta` gives any joining engine the current piece index in one round trip. No stream replay needed. |
| 14 | Cooperative playfield topology | Single shared playfield of width `playerCount × StandardWidth`; row subjects carry no player token (shared board) | Both players' pieces coexist on one wide board. `Cell.PlayerIdx` in the payload distinguishes active pieces — player identity lives in the message, not the subject, since coop never filters rows per player. One ordered consumer per engine. Line clears span the full width. UI renders the single playfield directly. |
| 15 | `GameMeta` payload | Fully specified in Section 4 with lifecycle, identity, RNG seed, and `PieceIdx` fields | Status uses string constants for readability in the `nats` CLI. `PieceIdx` enables fast startup without stream replay. |
| 16 | Real-time UI updates from JetStream | All UI data backed by JetStream uses ordered consumers pushing to Datastar SSE — never polling or periodic refresh | The lobby runs consumers for KV (players/games), chat, and archives. The engine runs consumers for playfield rows, events, meta, and countdown. Any change in a JetStream stream or KV bucket is immediately pushed to the UI via the consumer → Updates channel → broadcaster → SSE pipeline. |
