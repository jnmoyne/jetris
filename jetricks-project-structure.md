# Jetricks — Go Project Structure

**Version:** 0.1 Draft
**Status:** Design Phase
**Date:** March 2026

> **Gameplay reference:** All gameplay mechanics (cooperative/competitive/teams modes, scoring, gravity, line clears, game lifecycle) are defined in [`jetricks-gameplays.md`](jetricks-gameplays.md). This spec defers to that document for gameplay behavior and focuses on architecture, package structure, and implementation details.

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
12. [Front end: the native Gio UI](#12-front-end-the-native-gio-ui)
13. [Event Channel Contracts](#13-event-channel-contracts)
14. [Bootstrap Sequence](#14-bootstrap-sequence)
15. [Key Interfaces](#15-key-interfaces)
16. [Goroutine Inventory](#16-goroutine-inventory)
17. [orbit.go Module Reference](#17-orbitgo-module-reference)
18. [Testing Strategy](#18-testing-strategy)
19. [Design Decision Log](#19-design-decision-log)
20. [Release Pipeline](#20-release-pipeline)
21. [internal/agent and cmd/jetricks-agent](#21-internalagent-and-cmdjetricks-agent)

---

## 1. Repository Layout

```
jetricks/
├── .github/
│   └── workflows/
│       └── release.yml
├── cmd/
│   ├── jetricks/
│   │   └── main.go
│   └── jetricks-agent/
│       └── main.go
├── internal/
│   ├── config/
│   │   └── config.go
│   ├── nats/
│   │   ├── connection.go
│   │   ├── embedded.go
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
│   │   ├── rtt.go
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
│   ├── agent/
│   │   ├── agent.go
│   │   ├── difficulty.go
│   │   ├── eval.go
│   │   ├── executor.go
│   │   └── planner.go
│   ├── render/
│   │   └── colors.go
│   ├── nativeui/
│   │   ├── app.go
│   │   ├── archive_view.go
│   │   ├── board.go
│   │   ├── brand.go
│   │   ├── bridge.go
│   │   ├── fonts.go
│   │   ├── game.go
│   │   ├── input.go
│   │   ├── lifecycle.go
│   │   ├── lobby.go
│   │   ├── login.go
│   │   └── natslog.go
│   └── testutil/
│       └── nats.go
├── scripts/
│   └── cleanup.sh
├── jetricks-agent-guide.md
├── go.mod
└── go.sum
```

---

## 2. Package Dependency Graph

Arrows indicate "depends on". The rule is that `internal/game`, `internal/rng`, and `internal/config` are leaves — they have no internal dependencies. The front-end layer (`internal/nativeui`) depends on engine and lobby but neither engine nor lobby depends on the front end. All packages may depend on config.

```
cmd/jetricks
    ├── internal/config
    ├── internal/nats              ← uses: orbit.go/natscontext, orbit.go/jetstreamext
    ├── internal/rng
    ├── internal/game
    ├── internal/engine            ← depends on: nats, game, rng, config
    ├── internal/lobby             ← depends on: nats, config
    ├── internal/cleanup           ← depends on: nats, lobby, config
    ├── internal/archive           ← depends on: nats, engine, game, lobby, config
    ├── internal/render            ← depends on: game (cell/board appearance)
    └── internal/nativeui          ← depends on: engine, lobby, render, config (the front end)

cmd/jetricks-agent
    └── internal/agent               ← depends on: engine, lobby, game, rng, nats, config,
                                     archive, cleanup (headless player; no UI packages)

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
- Open the window immediately — **no NATS connection is made at startup**. `natspkg.ListContexts()` enumerates the NATS CLI contexts and `nativeui.NewWithPicker(cfg, names, selected)` builds the App; there is a single combined login screen (name entry + CONNECT TO chooser) and the App dials NATS itself when the player hits Play. `--server`/`--context` never connect directly — they only seed the picker's defaults. The App owns the connection; `main`'s shutdown paths call `a.DrainConn()` (nil-safe).
- Run the Gio front end (`internal/nativeui`): `runNative(ctx, cancel, a)` opens a native OS window. Gio's `app.Main()` owns the OS main thread, so the app logic runs on a goroutine that calls `App.Run`.
- Block on OS signal / window close and perform graceful shutdown

The player enters a name on the same screen; identity is NATS-backed presence. Lobby creation is deferred until the player connects and enters their name.

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | `""` | NATS context (as configured with `nats context add`) to preselect in the login screen's connection picker. |
| `--server` / `--user` / `--password` | `""` | NATS URL + credentials: `--server` pre-fills the picker's URL field and makes the URL option the starting choice (beating `--context`); user/password apply to URL connects. |
| `--version` | `false` | Print the version and exit. The `main.version` variable defaults to `dev` and is overridden at release time via `-ldflags "-X main.version=<tag>"` (see [Section 20 — Release Pipeline](#20-release-pipeline)). |

Connecting via a context ultimately maps to `natscontext.Connect(contextName)` from `orbit.go/natscontext`. This means Jetricks shares the same connection configuration — server URL, credentials, TLS certificates, JetStream domain — as the `nats` CLI tool on the same machine. No separate connection config file or credential management is needed. Operators configure contexts once with `nats context add` and both the CLI and Jetricks use them.

The login screen always shows the **CONNECT TO** section offering the machine's NATS CLI contexts through a "Context:" pull-down button (preset to the CLI's currently selected context, which is labeled "(selected)" in the opened list), an always-present **NATS URL** option, default `nats://demo.nats.io:4222` (`nativeui.DefaultNATSURL`), and an always-present **LAN mode (embedded NATS server)** option with an indented "Port:" field (pre-filled with `config.DefaultEmbeddedPort` = 4222, digits-only; editing it selects the option, the same way typing a URL selects the URL option) — Play with it selected starts an in-process JetStream-enabled `nats-server` (default account, no auth, the entered port on all interfaces, storage in `./jetstream-data`) and connects to it via its LAN address. While the option is selected, an indented `Your server's URL is nats://<lan-ip>:<port>` line (LAN IP resolved once at construction into `App.lanIP`) shows the address to share, and the lobby displays it again once connected. The default choice precedence is `--server` (URL option, field pre-filled with its value) → `--context` (context option, the pull-down preset to it; appended to the list if the lister didn't find it) → the CLI's selected context → the URL option. A **Check connection** button dials the current choice, measures the server ping (flush round trip), reports `✓ <server> · ping <rtt>` or `✗ <error>` — and closes that probe connection without provisioning anything (for the embedded option it dials nothing and reports the address the server serves, or would serve, on).

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
    RunEmbedded  bool   // run an in-process JetStream-enabled nats-server and connect to it
    EmbeddedPort int    // port for the embedded server (0 = DefaultEmbeddedPort)
}

// Embedded-server settings for the login screen's "LAN mode (embedded NATS
// server)" option: the default port the in-process server listens on (all
// interfaces; the player can override it in the picker) and the local
// directory holding its JetStream storage.
const (
    DefaultEmbeddedPort = 4222
    EmbeddedStoreDir    = "jetstream-data"
)

type GameMode int

const (
    ModeCooperative GameMode = iota
    ModeCompetitive
    ModeTeams                      // String() → "teams"
)

// TeamCount is the number of teams in a teams-mode game. Team indices are
// 0 (team A) and 1 (team B); a teams game has PlayerCount = TeamCount×TeamSize.
const TeamCount = 2
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

// Abandoned-game detection (see lobby.runAbandonedChecker): every client
// re-checks the lobby's games on a timer; abandoned games grow a Delete
// button in the lobby.
const (
    AbandonedCheckInterval    = 1 * time.Minute   // how often each client re-checks
    AbandonedIdleTimeout      = 1 * time.Minute   // in_progress: max stream silence
    AbandonedUnstartedTimeout = 15 * time.Minute  // created/starting: max age since CreatedAt
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

// TeamVisibleRowStart returns the first visible row index for a team board —
// always HeadroomRows, as in competitive (the board grows downward).
func TeamVisibleRowStart(teamSize int) int {
    return HeadroomRows
}
```

### Archive Types

```go
// PlayerResult holds one player's outcome in a completed game.
type PlayerResult struct {
    PlayerID   string `json:"player_id"`
    Score      int    `json:"score"`
    Level      int    `json:"level,omitempty"` // level achieved at game end (from the player's line total)
    PieceCount uint64 `json:"piece_count"`
    Winner     bool   `json:"winner,omitempty"`
    Team       int    `json:"team,omitempty"` // teams mode: 0 = A, 1 = B
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
    TotalScore  int            `json:"total_score,omitempty"` // cooperative mode only (unset for teams)
    FinalLevel  int            `json:"final_level,omitempty"` // cooperative: shared level at game end
    TeamSize    int            `json:"team_size,omitempty"`   // teams mode
    WinningTeam int            `json:"winning_team"`          // teams mode: 0 or 1; -1 = draw or not a team game
    TeamScores  []int          `json:"team_scores,omitempty"` // teams mode: final score per team (indexed by team)
    TeamLevels  []int          `json:"team_levels,omitempty"` // teams mode: final level per team (indexed by team)
    Boards      []BoardPicture `json:"boards,omitempty"`      // end-of-game playfield snapshot(s)
}

// BoardPicture is a saved snapshot of one board as it stood when the game
// ended — the latest cell messages from the (now-deleted) game stream for the
// board's visible region. One picture for cooperative, one per player for
// competitive, one per team for teams mode. Rebuilt into an engine.BoardSnapshot
// and redrawn by the lobby's history viewer.
type BoardPicture struct {
    Label  string      `json:"label,omitempty"` // player ID, "Team A"/"Team B", or "" (cooperative)
    Idx    int         `json:"idx"`             // player/team index for coloring; -1 if not applicable
    Width  int         `json:"w"`
    Height int         `json:"h"`               // visible row count stored (row 0 = first visible row)
    Cells  []BoardCell `json:"cells,omitempty"` // sparse: only the non-empty cells
}

// BoardCell is one non-empty cell of a BoardPicture. Data is the raw cell
// message exactly as published to the game stream (see game.Cell).
type BoardCell struct {
    Row  int             `json:"r"` // 0-based within the stored visible region
    Col  int             `json:"c"`
    Data json.RawMessage `json:"d"`
}
```

### Game ID Format

Game IDs are UUID v4 strings with dashes (e.g. `550e8400-e29b-41d4-a716-446655440000`). NATS stream names allow alphanumeric characters plus dashes and underscores, so `JETRICKS_GAME_550e8400-e29b-41d4-a716-446655440000` is a valid stream name. UUIDs are generated by the game creator's client using `github.com/google/uuid`.

### Subject Builders

```go
func GameStream(gameID string) string        // → "JETRICKS_GAME_<id>"
func GameSubjectFilter(gameID string) string // → "jetricks.game.<id>.>"

// Each mode uses its own playfield subject scheme — they are not
// parameterisations of one builder and are free to diverge. A game is exactly
// one mode, so an engine uses only one scheme.
//
// The playfield is stored as ONE MESSAGE PER CELL: each (row, col) position is
// its own subject, whose last message is that cell's current state.
//
// Cooperative — single shared wide playfield (playerCount × StandardWidth);
// cell subjects carry NO player token. Every player publishes to / consumes from
// the same subjects; per-cell ownership lives in the payload (Cell.PlayerIdx).
func CoopCellSubject(gameID string, row, col int) string
//   → jetricks.game.<id>.playfield.cell.<row>.<col>
func CoopCellSubjectFilter(gameID string) string
//   → jetricks.game.<id>.playfield.cell.>

// Competitive — each player owns a private playfield scoped by their UUID.
func CompetitiveCellSubject(gameID string, playerID string, row, col int) string
//   → jetricks.game.<id>.player.<pid>.playfield.cell.<row>.<col>
func CompetitiveCellSubjectFilter(gameID string, playerID string) string
//   → jetricks.game.<id>.player.<pid>.playfield.cell.>

// Teams — two shared boards (one per team), each TeamBoardWidth(teamSize) wide.
// Like the cooperative scheme the subject carries NO player token — all
// teammates publish to / consume from the same subjects and per-cell ownership
// lives in the payload (Cell.PlayerIdx, the GLOBAL roster index) — but the
// board is scoped by team index so the two teams' boards are disjoint.
func TeamCellSubject(gameID string, team, row, col int) string
//   → jetricks.game.<id>.team.<t>.playfield.cell.<row>.<col>
func TeamCellSubjectFilter(gameID string, team int) string
//   → jetricks.game.<id>.team.<t>.playfield.cell.>

func MetaSubject(gameID string) string
func RosterSubject(gameID string, playerID string) string
func EventsSubject(gameID string) string
func CountdownSubject(gameID string) string

// Lobby chat and per-game chat share the SAME stream (LobbyChatStream),
// distinguished purely by subject: lobby messages on LobbyChatSubject
// ("jetricks.lobby.chat"), a game's messages on GameChatSubject
// ("jetricks.lobby.chat.game.<id>"). Game chat cannot live on the game stream
// because game streams keep only the latest message per subject.
func GameChatSubject(gameID string) string
const GameChatSubjectFilter = "jetricks.lobby.chat.game.*" // stream config
func GameIDFromChatSubject(subject string) string          // "" = lobby

func LobbyPlayerKey(playerID string) string
func LobbyGameKey(gameID string) string

// The archive subject is the ArchiveSubject const ("jetricks.archive") — there
// is no builder function for it.
```

All subject and stream names in the application are produced exclusively through these builders. No package constructs subject strings by hand.

### Stream Configuration Notes

`JETRICKS_GAME_<id>` is created with `MemoryStorage`, `MaxMsgsPerSubject: 1`, `LimitsPolicy` retention, and two stream-level flags:
- `AllowAtomicPublish: true` — required for jetstreamext atomic batch move publishing
- `AllowDirect: true` — enables direct get / `GetLastMsgsFor` for fast playfield reconstruction and per-subject refetch

The stream uses **memory storage** (game streams are ephemeral and deleted at game end, so there is no need to persist them to disk) and retains **only the latest message per subject** (`MaxMsgsPerSubject: 1`) — only the current state for each subject/key is needed. Both flags are set unconditionally on every game stream regardless of mode. No `MaxAge` is set (game streams are deleted at game end), and `AllowMsgCounter` is **not** set — the cooperative score is a plain local counter propagated via events, not a server-side counter CRDT.

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
    Mode        GameMode   `json:"mode"`          // cooperative, competitive, or teams
    PlayerCount int        `json:"player_count"`  // max players (teams: TeamCount×TeamSize)
    TeamSize    int        `json:"team_size,omitempty"` // teams mode: players per team

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

The `PieceIdx` field is the number of pieces that have locked in across the entire game. In cooperative mode every player initialises their RNG from the same `Seed` and tracks their own `pieceIdx` locally — players still receive different pieces at any moment because their indices advance independently as each locks in, and each piece spawns offset into that player's section (`p.Col += playerIdx*StandardWidth`). `PieceIdx` in meta is not used for cooperative mode piece tracking. In competitive mode each player has an independent piece sequence (also seeded from `Seed`) — `PieceIdx` tracks the piece index for the player whose piece last locked in. Competing engines track their own index locally and only publish to meta when their own piece locks in. Teams mode follows the cooperative scheme: every player on both teams seeds from the same `Seed` and starts its local `pieceIdx` at 0, so the two teams draw from the identical 7-bag sequence, and each spawn is column-offset into the player's team-board section (`p.Col += teamSlot*StandardWidth`).

> **Implementation note:** `PieceIdx` in meta is eventually consistent. A joining engine that reads it mid-game will get the last published value, which may lag by at most one piece lock-in. The engine should treat this as a starting lower bound: after applying the FetchPlayfieldState snapshot, it scans the playfield for active piece presence, and if an active piece is visible but its index would correspond to `PieceIdx`, the index is correct. If no active piece is present (lock-in just happened but meta not yet updated), the engine can wait for the ordered consumer to deliver the next cell updates, which will show the new piece spawning — at that point the implied piece index is `PieceIdx + 1`. This self-corrects without any special handling.

---

## 5. Player Identity

Player identity is handled entirely in the UI at startup — no persistent files are stored on disk. When a player starts Jetricks, they are prompted on a login screen to enter a player name. This name **is** the player ID used in all NATS subjects, KV keys, and game rosters. There is no separate display name.

### Validation

Player names are validated to be legal NATS subject tokens:
- Must be 1–32 characters
- Cannot contain `.`, ` ` (space), `*`, `>`, tab, newline, carriage return, or null

Validation is implemented in `config.ValidatePlayerName(name) error`.

### Flow

1. App starts → login screen is shown (no lobby exists yet)
2. Player enters a name → the name shape is validated (`config.ValidatePlayerName`) and then the lobby KV is checked (`lobby.IsNameInUse`) for an active player presence entry with the same name (case-insensitive, whitespace-trimmed). Stale presence entries — `LastSeen` older than 3× `config.PresenceHeartbeat` — are ignored so unclean shutdowns don't permanently block the name.
3. If the name collides with an active player, a confirmation prompt ("a user with this name is already in the lobby — join anyway?") with **Yes, join** / **Cancel** is shown. **Yes, join** forces login, skipping the collision check. (The UI sets an internal force flag and retries the login.)
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

Also in `connection.go`:

```go
// Bootstrap connects per cfg (NATSURL wins over NATSContext, matching the CLI
// flag precedence) and provisions the lobby chat stream, lobby KV, and archive
// stream. On any post-connect failure nc is closed before returning — callers
// never receive a live connection together with an error.
func Bootstrap(ctx context.Context, cfg config.Config) (*nats.Conn, jetstream.JetStream, jetstream.KeyValue, error)

// CheckConnection dials per cfg, measures the server round-trip time with a
// flush ping, and closes the connection. Provisions nothing — backs the login
// screen's "Check connection" button.
func CheckConnection(cfg config.Config) (serverURL string, rtt time.Duration, err error)
```

`Bootstrap` is the single connect+provision path, invoked from the login screen's picker via `doConnectAndLogin`. The URL path adds `nats.Timeout(5s)` so a black-holed address fails promptly instead of hanging the UI's "Connecting…" state.

#### `contexts.go`

```go
// ListContexts returns the sorted names of the NATS CLI contexts defined under
// <XDG_CONFIG_HOME|~/.config>/nats/context/*.json plus the currently selected
// context name from <parent>/nats/context.txt.
func ListContexts() (names []string, selected string, err error)
```

Hand-rolled because `orbit.go/natscontext` exposes only `Connect` — no lister. It mirrors that package's path resolution exactly. A missing context directory yields `(nil, "", nil)`; non-`.json` entries and subdirectories are skipped; a `context.txt` naming a context that no longer exists reports `selected == ""`.

#### `embedded.go`

```go
// StartEmbeddedServer runs a JetStream-enabled nats-server inside this
// process, listening on every interface at the given port and storing stream
// data under storeDir. The returned server is ready for connections; stop it
// with Shutdown(). Backs the login screen's "LAN mode (embedded NATS server)"
// option (port from the picker, default config.DefaultEmbeddedPort; storage
// config.EmbeddedStoreDir).
func StartEmbeddedServer(storeDir string, port int) (*natsserver.Server, error)

// LanIP returns the machine's primary IPv4 address on the local network — the
// address other players should dial to reach an embedded server. The UDP dial
// sends nothing; it only resolves which local address routes outward. Falls
// back to scanning the interfaces, then to the loopback address.
func LanIP() string
```

#### `streams.go`

```go
// EnsureGameStream creates the per-game stream if it does not exist.
// Called when creating a new game.
func EnsureGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// EnsureLobbyChatStream creates the chat stream if it does not exist. It
// carries BOTH the lobby chat and every game's chat (subjects
// LobbyChatSubject + GameChatSubjectFilter), distinguished purely by subject.
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

// PurgeGameChat removes one game's chat messages from the shared chat stream
// (purge by the game's GameChatSubject). Used by archive.ArchiveAndCleanup and
// lobby.DeleteGame.
func PurgeGameChat(ctx context.Context, js jetstream.JetStream, gameID string) error
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
// CellUpdate represents a single cell's new state and the CAS expectation. The
// caller supplies the fully-built cell subject, so this package is subject-
// agnostic — it knows nothing about game modes or players. The engine builds
// Subject with the mode-appropriate scheme (Coop*/Competitive*/Team*CellSubject)
// and orders the slice for the desired consumer apply order.
type CellUpdate struct {
    Subject         string
    Payload         []byte
    ExpectLastSeq   uint64  // Nats-Expected-Last-Subject-Sequence for this cell
                            // (per-subject CAS, not stream-level)
}

// PublishMoveAtomically publishes a set of cell updates as a SINGLE atomic
// batch with per-subject CAS expectations
// (jetstreamext.WithBatchExpectLastSequencePerSubject, which sets the
// Nats-Expected-Last-Subject-Sequence header). Either every cell commits or
// none does. Returns ErrCASFailure if any subject's sequence expectation is
// not met. On success it returns the commit ack's stream sequence (assigned
// to the LAST message in the batch); the batch's messages get consecutive
// sequences, so the caller can infer every cell's assigned sequence from it.
//
// Per-subject CAS (not WithBatchExpectLastSequence, which is stream-level)
// is what we want: each cell is its own subject, so concurrent writes to
// other cells don't cause spurious rejections.
//
// Callers must keep a batch within the server's atomic-batch limit (default
// max_batch_size is 1000 messages); the engine chunks larger writes.
func PublishMoveAtomically(
    ctx context.Context,
    js jetstream.JetStream,
    updates []CellUpdate,
) (uint64, error)

// PublishCellsAtomicallyNoCAS publishes a set of cell updates as a SINGLE
// atomic batch WITHOUT CAS expectations. Used for authoritative state
// transitions (lock, hard-drop landing, line-clear, shrink) where the
// publisher's view is the new ground truth. Subject to the same 1000-message
// atomic-batch limit; also returns the commit ack's stream sequence.
func PublishCellsAtomicallyNoCAS(
    ctx context.Context,
    js jetstream.JetStream,
    updates []CellUpdate,
) (uint64, error)

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
// FetchPlayfieldState retrieves the current state of the given cell subjects
// for a game in one round trip (or a few, for large boards) using
// jetstreamext.GetLastMsgsFor. Used by the engine on startup and reconnect to
// reconstruct the full playfield instantly without replaying the entire game
// stream history, and by the merge-retry refetch.
//
// The caller builds the subjects with the mode-appropriate scheme (coop or
// competitive), so this function is subject-agnostic. Results are keyed by the
// (row, col) parsed from each subject, so it works for either subject shape.
// Cells that have never been written have no last message and are simply
// absent from the result (empty cell).
func FetchPlayfieldState(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    subjects []string,
) ([]PlayfieldCellMsg, error)

type PlayfieldCellMsg struct {
    Row     int
    Col     int
    Payload []byte
    Seq     uint64
}

// ParseCellFromSubject extracts the (row, col) position from a cell subject —
// the last two tokens. Returns (-1, -1) if the subject doesn't end in two
// numeric tokens.
func ParseCellFromSubject(subject string) (int, int)

// FetchGameMeta retrieves the latest game metadata message directly.
// Returns the decoded GameMeta, the stream sequence of the message
// (used as ExpectLastSeq for the next CAS update to meta), and any error.
func FetchGameMeta(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
) (config.GameMeta, uint64, error)
```

`FetchPlayfieldState` calls `jetstreamext.GetLastMsgsFor(ctx, js, streamName, cellSubjects)` where `cellSubjects` is the caller-supplied list of cell subjects for the game. The engine builds it with the mode-appropriate scheme: `config.CoopCellSubject(gameID, row, col)` for the shared cooperative board (no player token), `config.CompetitiveCellSubject(gameID, playerID, row, col)` for one competitive player's board, or `config.TeamCellSubject(gameID, team, row, col)` for one team's shared board. This returns the last message per subject in a single server round trip — far more efficient than replaying the entire stream from sequence 0 on join or reconnect. The engine uses this for its initial playfield snapshot before starting the ordered consumer, then the consumer takes over for live updates from that point forward.

One NATS server limit matters here: a multi-last direct get is hard-capped at **1024 responses** per request (the server answers `413 Too Many Results`, with no pagination). A full board snapshot asks for `width × height` cell subjects — well over the cap for a wide coop board — so when more than 512 subjects are requested, `FetchPlayfieldState` splits them into chunks of ≤512 `GetLastMsgsFor` calls, each bounded to a common point in the stream via `jetstreamext.GetLastMsgsUpToSeq(stream last seq)` so the combined snapshot is consistent; anything newer is replayed by the caller's consumer (`startSeq = maxSeq+1`).

---

## 7. internal/rng

**File:** `rng/rng.go`

Deterministic, seekable piece sequence generation. Uses Go's `math/rand/v2` with a PCG source. In **all** modes every player initialises their RNG from the same `Seed` stored in game metadata. In competitive mode all players therefore produce the identical piece sequence. In cooperative mode players still see different pieces at any given moment because each advances its own `pieceIdx` independently and each spawn is column-offset into that player's section — the sequence itself is shared, not forked with `seed+1`. Teams mode works like cooperative within each team (and both teams get the identical 7-bag).

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
    // LastSeq tracks the stream sequence of the last message applied to each
    // CELL subject — used for per-cell CAS expectations. Flat row-major:
    // index = row*Width + col (see seqIdx). length == Width*Height.
    LastSeq []uint64
}

// CellLastSeq returns the stream sequence of the last message applied to cell
// (row, col) — the per-subject CAS expectation for that cell. (seqIdx is the
// unexported flat-index helper.)
func (pf *Playfield) CellLastSeq(row, col int) uint64

// NewPlayfield creates an empty playfield with the default TotalRows height.
func NewPlayfield(width int) *Playfield

// NewPlayfieldWithHeight creates an empty playfield with a specific height.
// Cooperative uses HeadroomRows+VisibleRows; competitive uses
// CompetitiveTotalRows(playerCount) (taller as player count rises).
func NewPlayfieldWithHeight(width, height int) *Playfield

// Apply updates one cell of the playfield from a decoded cell message. It is
// the single reconciliation point for both the consumer echo and the engine's
// publish write-through: the message is applied only if its sequence is
// STRICTLY HIGHER than the cell's current LastSeq, otherwise it is skipped.
// Updates both the cell content and the per-cell LastSeq.
func (pf *Playfield) Apply(row, col int, cell Cell, seq uint64)

// ActivePieceForPlayer returns the active piece belonging to the given playerIdx
// (matching Cell.PlayerIdx). Used in cooperative mode where two players' active
// pieces coexist on the same shared playfield. Returns nil if no active piece
// with that playerIdx is present.
func (pf *Playfield) ActivePieceForPlayer(playerIdx int) *Piece

// SetActivePieceForPlayer / ClearActiveCellsForPlayer mutate the playfield in
// place. They are used by ProjectShrink to recompute the projected rows (and in
// unit-test setup); see "Invariant: NATS as single source of truth for the
// playfield" in section 9.
func (pf *Playfield) SetActivePieceForPlayer(p Piece, playerIdx int)
func (pf *Playfield) ClearActiveCellsForPlayer(playerIdx int)

// Projection helpers — compute the projected ROW contents for a state change
// WITHOUT mutating pf. They are deliberately still row-oriented (rows are the
// natural unit of game logic in memory); the engine then DIFFS the projection
// against the live board (diffCells / changedCells) and publishes only the
// cells that changed. The engine never mutates pf; the consumer applies the
// published cells on echo via Apply().
func (pf *Playfield) ProjectMove(affectedRows []int, newPiece *Piece, playerIdx int) map[int]Row
func (pf *Playfield) ProjectLock(affectedRows []int, playerIdx int) map[int]Row
func (pf *Playfield) ProjectHardDrop(affectedRows []int, dest Piece, playerIdx int, lockOnLand bool) map[int]Row
func (pf *Playfield) ProjectClearRows(completed []int, shiftAnchors bool) []Row
func (pf *Playfield) ProjectShrink(rowsToAdd, causerIdx, ownPlayerIdx int) ([]Row, bool)

// ProjectShrinkShared is the teams-mode variant of ProjectShrink for a shared
// team board where several teammates' active pieces coexist: the locked stack
// shifts up by rowsToAdd and rowsToAdd permanent adversarial rows tagged with
// causerIdx are added at the bottom. Unlike the competitive ProjectShrink, NO
// piece is lifted: EVERY player's active cells (the applier's own included)
// are overlaid back at their CURRENT, unshifted positions, and there is no
// topOut return. Any teammate may win the race to apply a shared-board shrink,
// and a lift would relocate other players' mid-flight pieces from a snapshot
// that may already be stale; holding every piece in place keeps the transform
// pure and symmetric. A piece overtaken by the risen stack sits in the holes
// its overlay preserved and locks there on its next blocked drop — it is
// "crushed" rather than carried up. Top-out on a team board therefore happens
// at spawn time, never during a shrink.
func (pf *Playfield) ProjectShrinkShared(rowsToAdd, causerIdx int) []Row

// AdversarialRowCount returns the number of garbage rows at the bottom of the
// board: contiguous bottom rows containing AT LEAST ONE adversarial cell.
// Garbage rows are permanent and bottom-anchored, so the count is monotonically
// non-decreasing — the engine uses it as the idempotency guard when several
// teammates race to apply the same shrink to their shared board. "At least
// one" rather than "all" because a garbage row can transiently hold a
// teammate's overlaid (crushed) active piece and can permanently keep the
// hollow cells a vacated overlay leaves behind; a piece covers at most 4 of
// the row's cells, so a garbage row always retains adversarial cells.
func (pf *Playfield) AdversarialRowCount() int
```

#### `row.go`

Defines the `Cell` type — the unit of NATS storage — plus `CellPos` and the in-memory-only `Row` type. Each NATS playfield message carries exactly **one `Cell`**, encoded as **JSON** — straightforward to debug with the `nats` CLI and sufficient for the update rate. An empty cell marshals to `{}` (every field is `omitempty`), which is the payload published to **vacate** a cell.

```go
// Cell represents a single cell of the playfield. One Cell JSON document is
// the payload of one playfield message.
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

func (c Cell) Marshal() ([]byte, error)        // empty cell → "{}" (the vacate payload)
func UnmarshalCell(data []byte) (Cell, error)

// CellPos identifies one cell by position — the key of the engine's per-cell
// projection diffs and publish batches.
type CellPos struct {
    Row int
    Col int
}

// Row is the IN-MEMORY representation only — the playfield is stored in NATS
// as one message per cell. There is no Row.Marshal / UnmarshalRow.
type Row struct {
    Cells []Cell `json:"cells"`
}
```

Piece position and orientation are encoded in the `Active`/`Orientation`/`AnchorRow`/`AnchorCol` fields of every cell the piece occupies. All occupied cells of the same active piece carry identical anchor and orientation values, making the full piece reconstructable from any single active cell. This redundancy is intentional — and with one message per cell it is even more load-bearing: the engine can reconstruct the active piece from any single cell message without scanning the rest of the board first.

#### Lock-in implicit detection

There is no explicit lock-in event message. Instead the engine detects lock-in by observing the transition in cell data delivered by the ordered consumer: cells that were `Active: true` in the previous state become `Active: false, Occupied: true` in the new state, and no `Active: true` cells remain anywhere in the playfield. When the engine detects this transition it:

1. Increments `pieceIdx`.
2. Publishes an updated `GameMeta` with `PieceIdx = pieceIdx` to `jetricks.game.<id>.meta` — this is a CAS update using the current meta sequence. If the CAS fails (the other player's engine raced to publish first for the same lock-in), the engine reads the new meta value; since both engines increment by 1 from the same base, the value is idempotent and the race winner's value is correct.
3. Calls `rng.Sequence.Piece(pieceIdx)` to determine the next piece type and spawns it at the top of the playfield.

This makes `PieceIdx` in `GameMeta` eventually consistent: any engine joining mid-game via `FetchGameMeta` gets the current piece count in one round trip.

The same lock-in transition also triggers the **completed-line check** (`CompletedRows` → clear). Because the ordered consumer applies a publish batch **one cell message at a time** and the lock-in (and thus the completion check) fires the instant the player's last `Active` cell disappears, the **order of the cells within a batch matters** even though the batch commits atomically. Every publish path uses one ordering rule, `orderedCellKeys`: cells are batched by **category of their NEW content — active first, locked/occupied second, empty vacates last** — tie-broken by ascending (row, col). This single rule covers all the cases that previously needed the per-row `bottomFirst` flag:

- A **relocating piece** (gravity, lateral move, rotation, hard drop with the piece staying active) never transiently has **zero** active cells: its new active cells are all applied before its old positions are vacated, so no *spurious* lock-in fires. This covers the single-row horizontal I — whose old and new footprints don't overlap — in **every** direction, not just downward.
- An **in-place lock or hard drop** fires lock-in exactly once, at the batch's **last** message (the vacate that removes the player's final active cell) — by which point all landing/locked cells are already applied, so a line completed by the drop is detected at that lock, not one piece later.
- A **coop line clear** applies the other player's shifted active piece before vacating its old positions.
- A **competitive shrink** applies the re-stamped piece first, the rising stack second, vacates last.

The `bottomFirst`/`applyBottomFirst` parameters that used to thread through the publish helpers are gone.

> On a **shared board** (coop and teams alike), lock, hard-drop, and line-clear go through `publishProjectedCellsWithMergeRetry` (CAS + refetch-merge-retry), not a plain NoCAS write — see §`internal/engine` and the publish table in the implementation plan. A NoCAS write could overwrite (or vacate) the *other* player's mid-flight active cells from a possibly-stale snapshot, corrupting their piece; CAS+merge skips those cells. Every publish path publishes only the cells that **actually changed** (`diffCells` for moves/spawn/lock/hard-drop, `changedCells` for clear/shrink) — a move is ~4–8 cell messages, and a clear publishes only the cells that differ after the shift, which keeps the merge-retry from exhausting against the other player's moving piece (the contention that dropped clears/spawns: uncleared line + stuck player). Per-cell CAS itself makes contention much rarer: two coop pieces in the same row no longer conflict, only writes to the *same cell* do. Competitive clear/shrink publish their changed cells NoCAS (single-writer per-player boards).

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
// Used by the engine's hard-drop handler to build the destination cell updates.
func HardDropDestination(p Piece, pf *Playfield) Piece
```

The `Coop`-suffixed helpers are written against *any* shared board, not the cooperative board specifically — teams mode reuses `CanPlaceCoop` / `HardDropDestinationCoop` (and SRS rotation) unchanged on the team board; `collision.go` and `rotation.go` needed no changes for teams.

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

The in-memory `*game.Playfield` held by the engine is a **read-only replica** for everyone except the cell consumer (`runConsumer` in `consumer.go`). Specifically:

- **The only place `e.playfield` is mutated is `pf.Apply(row, col, cell, seq)` inside the cell consumer.** That call is invoked when an ordered-consumer message for one of this engine's cell subjects is delivered.
- **No game action mutates the playfield directly** — not the local player's moves, not hard drops, not piece locks, not line clears, not opponent shrinks, not piece spawns. Each action computes the *projected* row contents using the helpers in `internal/game/playfield.go` (`ProjectMove`, `ProjectLock`, `ProjectHardDrop`, `ProjectClearRows`, `ProjectShrink`, `ProjectShrinkShared`), diffs them against the live board down to the changed cells (`diffCells` / `changedCells`), and publishes only those cells. The consumer then applies those cells when it receives the echo, and the UI re-renders from the updated `e.playfield`.
- **The UI renders only from `e.playfield`.** It never sees pre-publish state.

This eliminates two-way drift between the local replica and the stream: every player on every machine sees the playfield evolve in the same order it was committed to JetStream. The price is that there is a NATS round-trip between input and visual feedback, and that two rapid inputs may both validate against the same pre-echo state — the second is dropped via CAS rejection (per-subject `ExpectLastSequencePerSubject`), surfaced as a CAS-flash event for visual feedback.

### Atomic batches with per-subject CAS

Every publication of multiple cells from the engine is a SINGLE atomic batch:

- `natspkg.PublishMoveAtomically` — multi-cell batch with **per-subject CAS** expectations (`Nats-Expected-Last-Subject-Sequence`, applied via `jetstreamext.WithBatchExpectLastSequencePerSubject(seq)`). Used for moves, rotations, and spawns.
- `natspkg.PublishCellsAtomicallyNoCAS` — multi-cell batch without CAS. Used for authoritative state transitions (piece lock, hard-drop landing, line-clear, opponent-shrink application).

Why per-subject CAS, not stream-level (`WithBatchExpectLastSequence`)? Each cell is its own NATS subject. Per-subject CAS rejects only when *our* cell was overwritten since we last saw it; concurrent writes to *other* cells don't conflict. This is essential in cooperative mode where two players write the same shared playfield — and per-cell granularity makes the conflict window far smaller than the old per-row scheme: two coop pieces moving through the same row no longer contend at all, only writes to the *same cell* do. It is also useful in competitive mode for parallelism between meta/event publishes and cell publishes.

Why atomic batch, not cell-by-cell? A single move typically touches 4–8 cells (the new footprint plus the vacated old positions). If those messages arrived at consumers as independent publishes, every other player would briefly observe a half-erased / half-placed piece between consumer applies. Atomic batch makes the multi-cell update visible to consumers as one indivisible step. Within the batch the cells follow the `orderedCellKeys` category order (active, locked, empty) — see the lock-in section in §8 — because the consumer still applies the batch's messages one at a time. One server limit applies: the default atomic-batch limit is **1000 messages**, so the engine keeps every batch at or below that and `publishProjectedCellsNoCAS` chunks larger writes (only reachable on degenerate many-player boards).

The expected-last-sequence value for each cell comes from `e.playfield.CellLastSeq(row, col)` (the flat per-cell `LastSeq` array, index `row*Width+col`), updated via `pf.Apply(row, col, cell, seq)` from two places: the cell consumer on an ordered-consumer echo, and the **publish write-through** (`applyPublishedCells`), which advances it from the batch commit ack the instant a write commits. The write-through keeps the CAS expectation current so the next write doesn't lose a per-subject race against the engine's own just-committed write; `pf.Apply`'s strictly-higher-sequence rule reconciles the two sources (the echo of our own write carries the same sequence we already applied and is skipped; only a higher sequence updates memory).

**Optimistic sequence write-through (all modes).** The per-subject CAS expectation is the cell's `LastSeq` entry, advanced by `Playfield.Apply`. Rather than waiting for the engine's own consumer to echo a published cell back before that expectation (and the board content) catches up, a **successful publish is written through into the playfield immediately**: the batch commit ack returns the stream sequence of the last message, and since an atomic batch's messages get consecutive stream sequences the engine infers each cell's sequence (`message i of N → commitSeq − (N−1−i)`) and applies the committed content + sequence via `pf.Apply`. The two batch publishers (`PublishMoveAtomically`, `PublishCellsAtomicallyNoCAS`) return that commit sequence; `applyPublishedCells` does the write-through. `pf.Apply`'s "apply only a **strictly higher** sequence" rule reconciles this with the later echo: the echo of our own write carries the same sequence we already wrote through and is skipped, while a higher sequence (the other player's write in coop, or a NoCAS write we didn't originate) still updates memory. In coop the write-through applies only what actually committed (the first-attempt or merge-retry batch), so it never clobbers the other player's cells. This keeps the in-memory view current so a player cannot lose a per-subject CAS race against their own just-committed write (gravity vs. input, a write right after a NoCAS line-clear/shrink, a fast input burst). `applyPublishedCells` takes `e.mu` unless the caller already holds it — a `locked` flag is threaded through the publish helpers and `spawnPiece` because `spawnPiece` and the line-clear publish run under the consumer's lock while every other publish path runs with the lock released (`spawnPiece` itself takes `e.mu` around its projection+diff when called with `locked=false` on the Start path, releasing before publish).

CAS-failure handling for **player moves** (same in all modes): **drop the move, no retry, no NATS publish**. The engine emits an `UpdateCASFlash` directly on its local `Updates` channel; the player must retry the input themselves.

In cooperative mode the shared playfield has two writers, so CAS rejections on moves are an expected, regular occurrence. A silent server-side retry would mask the conflict and make the player's own input timing feel non-deterministic. Instead we surface the failure loudly: the UI renders the `UpdateCASFlash` as a **rainbow outline flash on the player's own piece** — cells in `FlashCells` cycle through the seven spectrum colors over roughly 600 ms with a matching glow, then revert. The other players see nothing, since one player's input rejection is information of no use to anyone else.

CAS-failure handling for **engine-driven (internal) writes** — piece spawn and gravity ticks. The player did not press a key for either, so a flash would be misleading; and both share cell subjects with the other player in coop mode. Both **must** succeed: a dropped spawn would leave the player pieceless, and a dropped gravity tick would make the piece appear frozen for one tick interval. On a shared board (coop and teams) both go through `publishProjectedCellsWithMergeRetry`: on CAS failure, refetch the latest message for **all** affected cells in one batched round trip (`FetchPlayfieldState` / `GetLastMsgsFor`), keep our content except where the latest stream state holds the other player's mid-flight active cell (those cells are skipped entirely), and retry the batch with refreshed per-subject CAS expectations (up to 16 retries, with a short per-player-offset backoff between tries that breaks lockstep with the other player's retry loop). In competitive mode each player owns their subjects, so both go through the regular `publishProjectedCells`. **Gravity and player input run on one goroutine (`runInput`), so a player's own gravity tick and move are serialized and cannot lose the per-subject CAS race against each other** — this is what removed the spurious rainbow flashes that were otherwise visible in competitive play.

The rainbow flash fires for any dropped CAS write (player moves, gravity ticks, and spawns alike). The `internal` boolean threaded through `attemptMove` / `attemptMoveStandard` / `attemptMoveCoop` distinguishes the source: the moves arm of `runInput` (player input) calls `attemptMove(move, false)`, while its gravity arm calls `attemptMove(MoveDown, true)`. On a shared board (coop/teams), `internal=true` routes through merge-retry so gravity flashes only after all retries are exhausted.

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
    gameMode    config.GameMode  // cooperative, competitive, or teams
    mode        atomic.Int32     // current Mode; atomic (cross-goroutine, see Concurrency)
    initialMode Mode             // original mode at creation (ModePlayer or ModeSpectator)
    playerIdx   int              // GLOBAL roster index; used on shared boards for Cell.PlayerIdx
    playerCount int              // number of players in the game
    teamIdx     int              // teams mode: which team this player is on (0 = A, 1 = B)
    teamSlot    int              // teams mode: section index within the team board (spawn column offset)
    teamSize    int              // teams mode: players per team (from meta at Start)

    mu        sync.Mutex
    playfield *game.Playfield    // own (coop/teams: the shared wide playfield)

    // Opponent playfields — competitive: one per opponent, discovered
    // dynamically via the roster consumer, each maintained by its own ordered
    // consumer on that opponent's cell subjects. Teams: the single OPPOSING
    // team's shared board, keyed by TeamBoardKey(team).
    opponentPlayfields map[string]*game.Playfield // keyed by opponent playerID / TeamBoardKey
    opponentPlayerID   string                     // the known opponent (2-player join), if any

    seq      *rng.Sequence
    pieceIdx atomic.Uint64
    metaSeq  uint64

    // mode/score/level/totalLines/pieceIdx are sync/atomic — read and written from
    // the consumer, runInput, events and UI goroutines with no single covering lock
    // (transitionToSpectator sets mode both under and without e.mu). e.mu guards the
    // structured state (playfield, the maps).
    score             atomic.Int64
    totalLines        atomic.Int64
    level             atomic.Int64
    hadActivePiece    bool            // guarded by e.mu (plus one pre-goroutine write in Start); written by the own-cells consumer, spawnPiece (post-publish), and handleTeamTopOut
    spawnPending      bool            // shared boards: spawn deferred (blocked only by another player's ACTIVE piece); guarded by e.mu; retried by retrySpawnIfPending on the gravity tick
    eliminatedPlayers map[string]bool // players who have topped out (competitive/teams); guarded by e.mu
    eliminatedTeam    map[string]int  // teams: eliminated player → team; guarded by e.mu
    teamOutcomeDone   bool            // teams: win/loss/draw already decided; guarded by e.mu
    expectedGarbage   int             // teams: cumulative adversarial rows owed to this team's board; guarded by e.mu
    visibleRowStart   int             // first visible row index (varies per mode/player count)

    // Channels for outbound events to the UI layer
    Updates        chan EngineUpdate
    OnGameFinished func() // called after the game transitions to finished (wired to archive.ArchiveAndCleanup)
    OnStreamMsg    func(ts time.Time, subject string, payload []byte) // optional tap on every game-stream message delivered by this engine's consumers (set before Start; drives the UI's "Show NATS messages" panel)

    // internal
    js          jetstream.JetStream
    ctx         context.Context
    cancelFn    context.CancelFunc
    moves       chan MoveType
    cellUpdated chan struct{}
}

// New constructs an engine; it takes no ctx (Start derives one) and a SINGLE
// opponentPlayerID (the known opponent for a 2-player join; other opponents are
// discovered dynamically via the roster consumer). It does NOT take playerCount,
// seed, teamSize, or initialPieceIdx — those are read from GameMeta in Start.
// playerIdx/teamIdx/teamSlot are the values returned by lobby.JoinGame
// (JoinResult); spectators pass 0,0 for the team arguments.
func New(
    js jetstream.JetStream,
    gameID, playerID, opponentPlayerID string,
    gameMode config.GameMode,
    mode Mode,
    playerIdx, teamIdx, teamSlot int,
) *Engine

// Start begins all consumer goroutines and (if ModePlayer) the combined
// input+gravity goroutine (runInput).
// In cooperative mode, starts ONE ordered consumer on the shared cell subjects
// (no player token). In competitive mode, starts the own-cells consumer plus the
// roster consumer, and one consumer per opponent as they are discovered.
// In teams mode, starts the own-team consumer plus ONE opposing-team board
// consumer (startTeamBoardConsumer) and does NOT run the roster consumer — the
// roster is fixed before the game starts and elimination events carry the team.
func (e *Engine) Start() error

// sharedBoard reports whether this engine's own playfield is shared with other
// writers (cooperative or teams). Most code paths that used to test
// gameMode == ModeCooperative — spawn placement and merge-retry, attemptMove
// routing, handleLockIn shift/merge/score/level, the runInput level update,
// the publishPieceIdxUpdate early-return — now branch on sharedBoard().
func (e *Engine) sharedBoard() bool

// TeamBoardKey is the opponentPlayfields/OpponentSnapshots key under which a
// team's shared board is filed ("team-<idx>").
func TeamBoardKey(team int) string

// startTeamBoardConsumer creates a playfield and ordered consumer for the given
// team's shared board, modeled on startOpponentConsumer: the board files into
// opponentPlayfields under TeamBoardKey(team) so OpponentSnapshots() flows to
// the UI unchanged. Players consume the OPPOSING team's board through it;
// spectator engines (teamIdx 0 default) consume team 0 as their "own" board and
// team 1 via this consumer.
func (e *Engine) startTeamBoardConsumer(ctx context.Context, team int)

// Teams accessors (used by the archive and the UI):
func (e *Engine) TeamIdx() int
func (e *Engine) TeamSlot() int
func (e *Engine) TeamSize() int

// Stop tears down all goroutines cleanly.
func (e *Engine) Stop()

// Move input is delivered through MoveType values dispatched onto the internal
// moves channel and is only acted on when mode == ModePlayer. (The native
// front end translates key input into these moves.)

// transitionToSpectator is called internally when the game ends for the local
// player. It sets mode = ModeGameOver and emits UpdateGameOver{Won}. It does not
// itself stop the gravity/move goroutines — those self-exit because they guard on
// mode == ModePlayer — and the consumers keep running.
func (e *Engine) transitionToSpectator(won bool)
```

#### `consumer.go`

Manages the ordered consumer goroutine(s). In cooperative mode, ONE consumer runs on the shared cell subjects (no player token — the subject carries no player segment), updating the single shared `Playfield`. In competitive mode, 1 + N consumers run — one for the local player's cells and one per opponent — each updating a separate `Playfield` instance. In teams mode, exactly TWO consumers run — one on the own team's shared cell subjects (updating `e.playfield`) and one on the opposing team's (updating the board under `TeamBoardKey` in `opponentPlayfields`).

```go
func (e *Engine) runConsumer(ctx context.Context, pf *game.Playfield, filterSubject, opponentID string, startSeq uint64, isOpponent bool)
```

**Startup sequence:**

1. Call `nats.FetchGameMeta(gameID)` — returns `GameMeta` including `Seed`, `PieceIdx`, and `Status`. In **all** modes `e.seq = rng.New(meta.Seed)`. In competitive mode `e.pieceIdx = meta.PieceIdx`; in cooperative and teams mode `e.pieceIdx = 0` and each player tracks its own index independently (the sequence is shared, not forked with `seed+1` — in teams both teams therefore get the identical 7-bag). `e.playerIdx` was supplied at construction (from `lobby.JoinGame`); no discovery is done here. `e.playerCount`, `e.teamSize`, and `e.visibleRowStart` are set from meta, and the playfield is (re)allocated at the mode-appropriate width/height (teams: `TeamBoardWidth(teamSize)` × `TeamTotalRows(teamSize)` with `visibleRowStart = TeamVisibleRowStart(teamSize)`).
2. Call `nats.FetchPlayfieldState(gameID, subjects)` for the player's own cell subjects — `cellSubjects()` builds all `width × height` of them, row-major (coop: the shared `playfield.cell.*` subjects with no player token; competitive: the player's own `player.<pid>.playfield.cell.*`; teams: the own team's `team.<t>.playfield.cell.*`). Above 512 subjects the fetch is chunked into ≤512-subject `GetLastMsgsFor` calls bounded to a common stream sequence (the server caps a multi-last direct get at 1024 responses). Apply all fetched cells to `e.playfield` via `pf.Apply`; never-written cells are absent from the result and stay empty. Record `maxSeq = max(all cell sequences)`.
3. Start the ordered consumer with `startSeq = maxSeq + 1`. In cooperative mode this is ONE consumer on the shared cell subjects (filter `jetricks.game.<id>.playfield.cell.>`); in teams mode it filters the own team's board (`jetricks.game.<id>.team.<t>.playfield.cell.>`). In competitive mode this is the consumer for the player's own cells. Messages on non-cell subjects (events, meta, chat) that arrived between the lowest and highest fetched cell sequence are a tolerable gap — at most a few milliseconds of game time.
4. In competitive mode only, also start the **roster consumer** (`runRosterConsumer`, watching `jetricks.game.<id>.roster.*`) which discovers opponents dynamically and calls `startOpponentConsumer` for each — fetching that opponent's cells and starting one `runConsumer` per opponent targeting `jetricks.game.<id>.player.<opponentPID>.playfield.cell.>`. A known opponent passed at construction is started immediately; late joiners are picked up as their roster entries appear. In cooperative mode there is no opponent consumer — both players write to and read from the same shared cell subjects. In teams mode there is no roster consumer either (the roster is fixed before the game starts and elimination events carry the team); instead `startTeamBoardConsumer(ctx, 1-teamIdx)` starts the single opposing-team board consumer.

**Cooperative mode design:**

In cooperative mode both players share a SINGLE wide playfield of width `playerCount × StandardWidth` (20 columns for 2 players). Cell subjects carry no player token — the shared board publishes to `jetricks.game.<id>.playfield.cell.<row>.<col>` (every player publishes to and consumes from the same subjects) via the `config.CoopCellSubject` scheme, distinct from the competitive `config.CompetitiveCellSubject` scheme. Per-player filtering is never needed in coop, so the player identity lives entirely in the payload rather than the subject. Both players' active pieces exist on the same playfield and can move anywhere on it — they are not restricted to their own section. Each cell of an active piece is tagged with `Cell.PlayerIdx` (0 for creator, 1 for joiner) so the engine can distinguish which player's piece each cell belongs to.

Each player spawns their piece centered in their section (player 0: center of cols 0–9, player 1: center of cols 10–19) but can move it anywhere on the full-width board. `ActivePieceForPlayer(playerIdx)` finds only the piece belonging to that player (by matching `Cell.PlayerIdx`). `SetActivePieceForPlayer(p, playerIdx)` only clears active cells with matching `PlayerIdx` before setting new ones. Collision detection (`CanPlaceCoop`) treats the other player's active cells as obstacles in addition to locked cells.

Both players seed their RNG from the same `meta.Seed` but track their own `pieceIdx` independently, so they receive different pieces at any given moment. Each engine has ONE playfield (the shared one) and ONE ordered consumer (on the shared cell subjects) — no separate opponent playfield is needed. Both players write to the same shared cell subjects, though per-cell CAS means they only actually conflict when writing the *same cell*. CAS conflicts on **moves** (left, right, down, rotate) are NOT retried — the move is simply dropped and the player must try another move. CAS conflicts on **state changes** (lock-in, spawn, line clear) ARE retried with a batched refetch of the affected cells from the stream, since these must succeed for game consistency.

Line clears work on the full 20-wide rows. The score is shared — both players' line clears contribute to the same score total. The UI renders the single wide playfield directly (no concatenation of two separate playfields).

**Teams mode design:**

Teams mode is **cooperative within a team, competitive between the two teams**. Each team of `teamSize` players shares one team-scoped board of width `TeamBoardWidth(teamSize)` and height `TeamTotalRows(teamSize)` (the board grows one row per opposing player, like competitive, to leave room for adversarial rows). Within the team everything works exactly as in cooperative mode, reused through the `sharedBoard()` helper rather than duplicated: per-player sections by **team slot** (spawn offset `teamSlot*StandardWidth` instead of coop's `playerIdx*StandardWidth`), `Cell.PlayerIdx` carrying the **global roster index**, `CanPlaceCoop` collision against teammates' active pieces, and the full CAS+merge-retry publish discipline for spawn/gravity/lock/hard-drop/clear. `cellSubject`/`cellFilterSubject` build the `TeamCellSubject(gameID, teamIdx, …)` scheme; the two teams' subject spaces are disjoint, so cross-team writes are impossible by construction.

Between teams the competitive mechanics apply at team granularity:

- **Garbage attack.** A team's line clears add unclearable adversarial rows to the OPPOSING team's shared board. The clearing engine publishes `GameEvent{Kind: EventShrink, Team: teamIdx, TargetTeam: 1-teamIdx, RowsRemoved: n, PlayerIdx: …}`; every **alive** member of the target team (`ev.TargetTeam == e.teamIdx && mode == ModePlayer`) races to apply it via `applyTeamShrink` — eliminated players and spectators never apply (their alive teammates do).
- **`applyTeamShrink` — guarded idempotent shared-board shrink.** Several teammates receive the same event, so the application must commit exactly once. The engine adds `ev.RowsRemoved` to `expectedGarbage`, then loops (16 attempts): under `e.mu` it computes `deficit = expectedGarbage − playfield.AdversarialRowCount()`; `deficit <= 0` means the shift (ours or a teammate's) already landed — done. Otherwise it projects `ProjectShrinkShared`, diffs with `changedCells`, builds the batch, and publishes **WITH CAS** (`PublishMoveAtomically`). On `ErrCASFailure` it waits on `e.cellUpdated` (capped, per-player-offset backoff) and **recomputes from fresh state** — never a blind merge-retry, since merging a stale shift after a teammate's shift committed would double-shift the stack. Exactly one teammate's batch commits per deficit (the winning batch wrote the full board width, so CAS rejects any batch from a torn/stale board); the rest converge through the deficit guard. The application ends with a full-board rerender.
- **No piece is lifted by a shared-board shrink.** `ProjectShrinkShared` overlays every player's active cells at their current, unshifted positions (see §`internal/game`); a piece overtaken by the risen stack is "crushed" — it locks where it is on its next blocked drop. A shrink can therefore never top a player out; top-out happens at spawn time only — and only when the spawn cells are held by LOCKED cells (a spawn covered only by a teammate's falling piece is DEFERRED and retried each gravity tick via `retrySpawnIfPending`, not a top-out).
- **Per-player elimination, per-team game over.** A topped-out player vacates their piece and spectates while their team plays on; a team loses only when ALL its members have topped out, and every member of the other team — already-eliminated ones included — wins.

Teams scoring is the coop rule per team: a clear scores `teamSize × lines`, and the clearing engine publishes `EventLineClear{Team, Score, LinesCleared}` — teammates fold **both** the score delta and the line count (as coop does too) so every teammate's level and gravity interval stay in sync with the team total. The opposing team's clears do not touch an engine's own `score`; their garbage reaches it as `EventShrink`. However **every** engine (both teams, eliminated players, spectators) also folds every `EventLineClear` into the per-team scoreboard `teamScores[ev.Team]`/`teamLines[ev.Team]` and emits `UpdateTeamStats`, so the live TEAM A / TEAM B scores (and per-team levels) render on every screen (see "Score tracking" below).

One subtle gate: lock-in detection in `runConsumer` only runs when `e.getMode() == ModePlayer`. A spectator — or an eliminated teams player, whose elimination vacated their piece on the still-live shared board — must never run lock-in side effects off the echoes of their teammates' play.

**Teams elimination and outcome flow:**

1. `handleTopOut` now takes `(ctx, locked bool)` (`locked` = caller holds `e.mu`; `spawnPiece`'s top-out branch always does) and routes teams games to `handleTeamTopOut`.
2. `handleTeamTopOut` sets `hadActivePiece = false` BEFORE publishing (so the vacate echo can't read as a lock-in), projects and publishes a **vacate of the player's own active cells** via merge-retry (the shared board stays live for the teammates), marks the player in `eliminatedPlayers`/`eliminatedTeam`, publishes `GameEvent{Kind: EventGameOver, Team: teamIdx, …}`, transitions to spectator with `Won: false`, and emits `UpdatePlayerEliminated`. It does NOT transition the game to finished — the team outcome logic decides that.
3. `handleTeamGameOverEvent` processes every elimination event (including the echo of our own): it tracks `eliminatedPlayers`/`eliminatedTeam`, computes per-team elimination counts, and decides the outcome **exactly once** (`teamOutcomeDone` flag). When the opposing team is dead, every `initialMode == ModePlayer` member of the winning team transitions to spectator with `Won: true` — already-eliminated winners flip their loss to a win — and calls `transitionGameToFinished` (CAS-deduped); it also emits `UpdateGameStatus "finished"` so an eliminated loser's "team plays on" UI flips. A defensive branch treats both-teams-dead as a draw (shouldn't happen — the ordered events subject means one team completes strictly first, and every engine reaches the same verdict).

**Per-message handling (cooperative mode — single shared playfield consumer):**

- Parses `(row, col)` from the subject via `ParseCellFromSubject`, decodes the cell payload (`game.UnmarshalCell`) and calls `pf.Apply(row, col, cell, seq)`, updating both the cell content and its per-cell `LastSeq`.
- After every cell update, scans for the **implicit lock-in signal** for this player's piece: if the previous state had an active piece for this `playerIdx` (`ActivePieceForPlayer(playerIdx) != nil`) and the new state has no active cells with matching `PlayerIdx`, a lock-in has just been committed by this player. The engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to spawn the next piece centered in this player's section.
- Emits a `UpdatePlayfield` with `ChangedRows: []int{row}` — the row is DERIVED from the cell position, so the UI event contract (and every UI package) is unchanged by per-cell storage — and the UI re-renders from the freshly applied `e.playfield`.
- On line-clear detection: checks the full-width playfield for completed rows. **Critically, the cleared rows are published synchronously before spawning the next piece** — this prevents a race condition where the spawn modifies the playfield while the clear is still being published. The score is updated and emitted to the UI. Level is recomputed and the gravity interval adjusted.
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

**Per-message handling (competitive mode — own and opponent playfield consumers):**

- Parses `(row, col)` from the subject via `ParseCellFromSubject`, decodes the cell payload and calls `pf.Apply(row, col, cell, seq)`, updating both the cell content and its per-cell `LastSeq`.
- After every cell update on the own playfield, scans for the **implicit lock-in signal**: if the previous state had an active piece and the new state has no active cells anywhere, a lock-in has just been committed. The engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to determine the next piece.
- Emits a `UpdatePlayfield` with `ChangedRows: []int{row}` (the row derived from the cell) so the UI re-renders from the freshly applied `e.playfield`.
- On receiving a message on the events subject: if it is a shrink event from another player (`ev.PlayerID != e.playerID`), calls `applyOpponentShrink` which publishes the shift's changed cells to the local player's own cell subjects. In 3+ player games, every opponent applies the same shrink independently.
- On line-clear detection: checks own playfield for completed rows. Cleared rows are published synchronously before spawning the next piece.
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

#### Input + gravity loop (`runInput`, in `move.go`)

The gravity arm also calls `retrySpawnIfPending` after each tick as the deferred-spawn BACKSTOP; the primary retry is message-driven — the own-board consumer re-attempts a pending spawn on every incoming cell change (the very message that may be the blocker moving away), because at agent speeds a blocking piece crosses the spawn cells in milliseconds and a tick-only cadence starved deferred players to a piece every few seconds. Both paths re-check under e.mu and re-defer or top out (locked cells) as appropriate. The same hook doubles as the **piece-less watchdog**: an alive player with no active piece and nothing pending for two consecutive ticks gets a forced spawn — the consumer's lock-in edge detector only fires when a message arrives on the board's consumer, so a wholesale-dropped spawn publish (or missed edge) on a since-silent board (last teammate eliminated) would otherwise strand the player piece-less forever. The watchdog is gated on `gameStarted` (set when the engine learns the meta is in_progress): the gravity ticker runs from engine start, during the countdown, and an ungated watchdog would force-spawn and start the game mid-countdown. `spawnPiece` additionally sets `hadActivePiece = true` after its publish (write-through already applied) so the consumer's lock-in edge detector cannot miss a piece that is hard-dropped before its spawn echo is processed.

```go
func (e *Engine) runInput(ctx context.Context)
```

The engine's single gameplay-write goroutine: it `select`s over the moves channel (player input) and the gravity timer. Running both on **one** goroutine is deliberate — a player's own gravity drop and a player move can never publish to their cell subjects concurrently, so they can never lose the per-subject CAS race against each other (in either mode; this removed the spurious rainbow flashes seen in competitive play). On each gravity tick it attempts to drop the active piece one row via `attemptMove(MoveDown, true)`; player input calls `attemptMove(move, false)`. On a shared board (cooperative and teams) it reads the current level from `totalLines` after each tick and adjusts the ticker interval when the level changes (in teams the folded `LinesCleared` keeps teammates' levels in sync); in competitive mode the interval is fixed.

**Cooperative gravity and lock-in:** When gravity cannot move a piece down, the engine distinguishes between two cases: (1) the piece is blocked by locked cells or out-of-bounds — the piece locks immediately, as in standard Tetris; (2) the piece is blocked only by the other player's active piece — the piece does NOT lock, since that obstacle is temporary (it will itself fall on its next gravity tick). In case (2), gravity simply waits and tries again on the next tick. This prevents premature lock-ins caused by two pieces passing through the same rows.

**Cooperative hard drop:** When a player hard-drops (space bar), the piece falls instantly to the lowest valid position — which may be on top of the other player's active piece. If the piece lands on locked cells or the floor, it locks immediately as usual. If it lands on the other player's active piece, it does NOT lock — instead it stays active and resumes falling by gravity. The other player's piece will itself fall on its next gravity tick, at which point gravity will continue dropping this piece further.

Both shared-board behaviours apply identically on the teams board, with teammates' active pieces as the temporary obstacles.

#### `move.go`

```go
// attemptMove is the central move dispatch function. internal=true marks an
// engine-driven move (e.g. a gravity tick); internal=false marks player input
// (player input drops + flashes on CAS failure, gravity ticks merge-retry in
// coop). It validates the move geometrically against the local playfield, builds
// the projection, diffs it to the changed cells (diffCells), and publishes them
// via the publishProjectedCells* helpers in engine.go. It dispatches by board:
// sharedBoard() (coop AND teams) → attemptMoveCoop, which works verbatim on the
// team board; competitive → attemptMoveStandard.
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

There is no `Publish`/`PublishHardDrop`/`ErrMoveDropped`/`ErrLockIn` API and no 50ms-wait-on-`cellUpdated` retry loop. All publish/CAS logic lives in `engine.go` (and the hard-drop helpers in `move.go`). The relevant helpers are:

```go
// cellSubject / cellFilterSubject / cellSubjects build this engine's own cell
// subjects with the mode-appropriate scheme (Coop*/Competitive*/Team*CellSubject;
// teams uses TeamCellSubject(gameID, teamIdx, ...)). cellSubjects returns all
// width×height of them, row-major (snapshot fetch).
func (e *Engine) cellSubject(row, col int) string
func (e *Engine) cellFilterSubject() string
func (e *Engine) cellSubjects() []string

// orderedCellKeys returns the keys of a cell-projection map in publish/apply
// order: by CATEGORY of the cell's NEW content — active first, locked/occupied
// second, empty vacates last (cellCategory) — tie-broken by ascending (row, col).
// This single rule replaces the old bottomFirst row ordering; see the lock-in
// section in §8 for the correctness cases it guarantees.
func orderedCellKeys(m map[game.CellPos]game.Cell) []game.CellPos

// publishProjectedCells publishes a cell diff as ONE atomic batch with
// per-subject CAS expectations sourced from e.playfield.CellLastSeq(row, col).
// On success it WRITE-THROUGHS the committed cells into e.playfield
// (applyPublishedCells). On CAS failure the step is DROPPED (no retry, no
// further publish) and the local player is signalled with a rainbow flash on
// flashCells (pass nil to suppress). Used for player moves, rotations, and
// competitive spawns/gravity ticks.
func (e *Engine) publishProjectedCells(ctx context.Context, cells map[game.CellPos]game.Cell, flashCells [][2]int, locked bool)

// publishProjectedCellsNoCAS publishes a cell diff as an atomic batch with NO
// CAS expectations — used for authoritative state (competitive lock, hard-drop
// landing, line-clear, opponent-shrink application). Write-throughs on success.
// A batch above the server's 1000-message atomic-batch limit (only reachable on
// degenerate many-player boards) is split into sequential atomic chunks along
// the already-ordered key list — the category order remains a correct total
// order across chunk boundaries, the only loss being a briefly visible
// intermediate board between chunks.
func (e *Engine) publishProjectedCellsNoCAS(ctx context.Context, cells map[game.CellPos]game.Cell, locked bool)

// publishProjectedCellsWithMergeRetry is the SHARED-BOARD (coop and teams) path
// for steps that MUST land (spawn, gravity tick, lock, hard drop, line clear,
// teams elimination vacate) on the shared board. On CAS
// failure it refetches the latest stream state of all affected cells in one
// batched round trip (refetchAndMerge), keeps our content except where the other
// player's mid-flight piece sits, and retries with refreshed per-subject CAS —
// up to 16 attempts with an escalating per-player-offset backoff
// (200µs × (attempt + playerIdx), capped at 2ms) that breaks lockstep with the
// other player's retry loop, then drops + flashes. Write-throughs the committed
// (first-attempt or merged) cells on success.
func (e *Engine) publishProjectedCellsWithMergeRetry(ctx context.Context, cells map[game.CellPos]game.Cell, flashCells [][2]int, locked bool)

// applyPublishedCells write-throughs a committed batch into e.playfield: each
// cell's content + the per-subject stream sequence inferred from the batch
// commit ack (message i of N → commitSeq−(N−1−i)), advancing both the board and
// the CAS expectation without waiting for the consumer echo. The `locked` flag
// is threaded through the publish helpers (and spawnPiece) because spawnPiece
// and the line-clear publish run under the consumer's lock while every other
// path runs with e.mu released — applyPublishedCells and buildBatchUpdates take
// e.mu unless locked.
func (e *Engine) applyPublishedCells(orderedKeys []game.CellPos, get func(game.CellPos) game.Cell, commitSeq uint64, locked bool)

// refetchAndMerge fetches the latest stream message for every cell in keys in
// ONE batched round trip (FetchPlayfieldState / GetLastMsgsFor) and rebuilds the
// publish batch with refreshed per-subject CAS expectations. The merge is per
// cell: our content is kept EXCEPT where the latest stream state holds the OTHER
// player's mid-flight (active) cell — those cells are skipped entirely (never
// overwrite or vacate their piece). A cell with no stream message is empty with
// CAS expectation 0. Returns the merged updates, cells, and key order (the
// caller's category order minus the skipped cells).
func (e *Engine) refetchAndMerge(ctx context.Context, keys []game.CellPos, cells map[game.CellPos]game.Cell) ([]natspkg.CellUpdate, map[game.CellPos]game.Cell, []game.CellPos, bool)

func (e *Engine) buildBatchUpdates(keys []game.CellPos, cells map[game.CellPos]game.Cell, locked bool) ([]natspkg.CellUpdate, error)

// diffCells returns the cells of a row projection that differ from the live
// board — only those are published, so a move costs ~4-8 cell messages (the new
// footprint plus the vacated old positions). Used by moves, spawns, locks, and
// hard drops; called under e.mu.
func diffCells(cur []game.Row, projected map[int]game.Row) map[game.CellPos]game.Cell

// changedCells returns the cells of projected[fromRow:toRow) whose content
// differs from cur — so a line clear / competitive shrink republishes only the
// cells that actually changed, not the whole visible range (far less per-subject
// CAS contention on the shared coop board). Replaces the old changedRows.
func changedCells(cur, projected []game.Row, fromRow, toRow int) map[game.CellPos]game.Cell
```

There is no recompute-and-retry-until-it-lands hard-drop loop. The hard-drop destination is computed **once** (`game.HardDropDestination` / `HardDropDestinationCoop`). In competitive mode the landing cells are published NoCAS (`publishHardDrop`); in cooperative mode through the merge-retry path (`publishHardDropCoop`, ≤16 retries). The `orderedCellKeys` category ordering guarantees the landing cells are applied before the vacates, so a line completed by the drop is detected at the lock, not one piece later. See the publish-strategy summary below.

#### `events.go`

Defines the `EngineUpdate` type sent from engine to UI over the `Updates` channel, and the event message format published to `jetricks.game.<id>.events`.

```go
type UpdateKind int

const (
    UpdatePlayfield        UpdateKind = iota  // one or more rows changed
    UpdatePieceLocked                         // active piece locked in
    UpdateLineClear                           // lines cleared, rows shifted
    UpdateGameOver                            // game ends for this player
    UpdateOpponentField                       // competitive: opponent's field changed; teams: opposing team's board changed
    UpdateOpponentShrink                      // competitive: opponent's field shrank (our line clear)
    UpdateScore                               // score changed
    UpdateLevel                               // cooperative: level changed
    UpdateGameStatus                          // game lifecycle status changed
    UpdateCountdown                           // pre-game countdown tick
    UpdatePlayerEliminated                    // competitive/teams: a player was eliminated
    UpdateCASFlash                            // a CAS-failure flash should be rendered
    UpdateRTT                                // a new publish→echo round-trip measurement
    UpdateBufferedMoves                       // the buffered-input queue changed
    UpdateTeamStats                           // teams: a team's score or level changed (totals in TeamScores/TeamLevels)
)

type EngineUpdate struct {
    Kind               UpdateKind
    ChangedRows        []int    // for UpdatePlayfield, UpdateLineClear, UpdateOpponentField
    Score              int      // for UpdateScore
    Level              int      // for UpdateLevel
    GameStatus         string   // for UpdateGameStatus
    Countdown          int      // for UpdateCountdown: seconds remaining (0 = GO!)
    Won                bool     // for UpdateGameOver: true if this player('s team) won
    EliminatedPlayerID string   // for UpdatePlayerEliminated: which player
    Team               int      // for UpdatePlayerEliminated: the eliminated player's team (teams)
    OpponentID         string   // for UpdateOpponentField/UpdateOpponentShrink: which opponent (teams: TeamBoardKey)
    FlashCells         [][2]int // for UpdateCASFlash: cells to flash
    FlashPlayerIdx     int      // for UpdateCASFlash: player index for flash color
    RTT               time.Duration // for UpdateRTT: latest publish→echo round trip
    TeamScores        [config.TeamCount]int // for UpdateTeamStats: both teams' scores
    TeamLevels        [config.TeamCount]int // for UpdateTeamStats: both teams' levels
}
```

#### `rtt.go`

Continuous **RTT** measurement, surfaced in both HUDs while playing: the time between
the moment the engine initiates a batch publish commit and the moment its own ordered
consumer delivers the batch's **first message** back — the full write→commit→echo loop
every visible board change travels.

Mechanics: a successful batch publish knows its commit-ack stream sequence, and an
atomic batch's N messages get consecutive sequences, so the batch's first message has
sequence `commitSeq-(N-1)`. `trackRTT(t0, commitSeq, n)` — called by all three publish
helpers with `t0` captured just before the publish call (first attempt and each
merge-retry attempt measure independently; each NoCAS chunk measures separately) —
registers `t0` under that sequence in `rttPending`; the own-board consumer calls
`noteRTTEcho(seq)` for every message it delivers, and the first batch message completes
the measurement, stores it (atomic, exposed via `RTT() time.Duration`) and emits
`UpdateRTT`.

The commit ack (publisher goroutine) and the echo (consumer goroutine) race — the
consumer can deliver the echo *before* `trackRTT` runs. `lastEchoSeq`, the highest
own-board sequence the consumer has delivered (maintained under `rttMu`, sufficient
because the ordered consumer delivers strictly by stream sequence), closes the race:
if the echo already passed, `trackRTT` completes the measurement immediately instead
of registering an entry that would never match. Stale pending entries (echo cut off by
shutdown) are pruned after 10 s. Spectators never publish, so they have no measurements
and the HUD shows an em dash.

The HUD readout is color-coded by `rttColor` (`internal/nativeui/game.go`): normal text
color up to 75 ms, a yellow→orange blend (`colWarn`→`colOrange`, `lerpColor`) from 75 ms
to 150 ms, and red (`colErr`) above 150 ms.

**Stream message tap (`OnStreamMsg`).** Every game-stream consumer — own/opponent/team
cells (`runConsumer`), events, meta, countdown, and the competitive roster consumer —
calls `e.tapMsg(msg)` on each delivered message before taking `e.mu`. If the optional
`OnStreamMsg` hook is set (before `Start`, like `OnGameFinished`), `tapMsg` passes it the
message's JetStream **stream timestamp** (`msg.Metadata().Timestamp`), subject, and raw
payload. The hook runs on the consumer goroutines and must not block. The native UI wires
it to `App.recordStreamMsg`, which appends to a capped (200-entry) log under `a.mu` —
gated on the `msgShow` flag mirrored each frame from the "Show NATS messages" checkbox,
so an unchecked panel costs one flag check per message — and the game screen renders the
tail in a fixed-height monospace strip across the bottom of the window (`natslog.go`:
timestamp · subject · payload, with the JSON payload syntax-colored by `jsonSpans`). The
log is cleared on `startGameScreen`/`returnToLobby`.

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
    LinesCleared int       `json:"lines_cleared,omitempty"` // for EventLineClear (coop and teams)
    TargetPlayer string    `json:"target_player,omitempty"` // present but unused — shrink is broadcast to all
    RowsRemoved  int       `json:"rows_removed,omitempty"`  // for EventShrink: how many rows
    ClearedRows  []int     `json:"cleared_rows,omitempty"`  // for EventLineClear: which rows
    Score        int       `json:"score,omitempty"`         // EventGameOver: final score; EventLineClear (coop/teams): score delta
    Level        int       `json:"level,omitempty"`         // EventGameOver: level achieved (from the sender's line total)
    PieceCount   uint64    `json:"piece_count,omitempty"`   // for EventGameOver: total pieces placed
    PlayerIdx    int       `json:"player_idx,omitempty"`    // causer's index (for EventShrink)
    Team         int       `json:"team"`                    // teams: sender's team (0 = A, 1 = B)
    TargetTeam   int       `json:"target_team"`             // teams: receiving team for EventShrink
}
```

**Shrink flow (competitive mode):**

1. Player A's engine detects a line clear after a lock-in (implicit detection from cell state).
2. Player A publishes an atomic batch: the cells changed by the row shift on its own playfield (cleared lines removed, rows above shifted down — `changedCells` diffs the shift so only cells that differ are published).
3. Player A also publishes a `GameEvent{Kind: EventShrink, PlayerID: playerA, RowsRemoved: n, PlayerIdx: ...}` to the events subject. The `TargetPlayer` field exists on `GameEvent` but is unused for shrink — the event is broadcast and ALL other players apply it.
4. Every other player's events consumer reads the shrink event. Since `ev.PlayerID != e.playerID`, each opponent calls `applyOpponentShrink(n)` which shifts their own playfield up by n rows and adds n fully occupied permanent adversarial rows at the bottom. In a 3+ player game, all opponents are shrunk simultaneously. Adversarial cells are marked with `Cell.Adversarial = true` and rendered with a distinct grey color. Adversarial rows can never be completed or cleared — `IsFull()` returns false for any row containing adversarial cells.
5. The shifted state is published using NoCAS (authoritative, same as line clears) to prevent stale consumer messages from undoing the shift. The opponent's own falling piece holds its position while the stack rises and is pushed up only as far as the rising stack/garbage forces it; `ProjectShrink` resolves the minimal lift (0..`rowsToAdd`) and returns a `topOut` flag. If no lift keeps the piece on the board, `applyOpponentShrink` calls `handleTopOut(ctx, false)`. See `jetricks-gameplays.md` for the full competitive shrink rules.

**Shrink flow (teams mode):** the same attack at team granularity, but on a multi-writer shared board — so the application is CAS-guarded rather than NoCAS, idempotent across racing teammates, and never lifts (or tops out) a piece. See the "Teams mode design" section above (`applyTeamShrink`, `ProjectShrinkShared`, `AdversarialRowCount`) and `jetricks-gameplays.md` for the rules.

**Score tracking:**

In **cooperative mode** the team score is a plain local counter (`score atomic.Int64`). When a player clears lines it adds `playerCount × lines` to its own `score` (reflecting the harder-to-fill wider playfield) and publishes a `GameEvent{Kind: EventLineClear, Score: delta, LinesCleared: n}` on the events subject; every other player's events consumer folds the delta into its own local `score` **and** the line count into `totalLines` (then `refreshLevel()` stores/emits the new level), so all clients converge on the same combined team total, shared level, and gravity. This is **not** a server-side counter CRDT and uses no score subject. See `jetricks-gameplays.md` for the authoritative scoring rules.

**Line clear publishing:** The cells changed by a clear (`changedCells` over the shifted projection) are published using a no-CAS publish in competitive mode (the cleared state is authoritative on a single-writer board) and through CAS+merge-retry in coop (so the shift can never overwrite the other player's mid-flight piece — `refetchAndMerge` skips any cell currently holding their active piece, and the category order applies their shifted piece before vacating its old positions). After the clear cells are published, the per-cell `LastSeq` entries are advanced by the write-through from the publish acknowledgment so subsequent CAS publishes use the correct sequences.

**CAS failure recovery:** After a no-CAS publish (or another player's committed batch), the other player's engine has stale per-cell `LastSeq` values until its consumer processes those messages. During this window, their writes to the same cells may fail with CAS errors. A failed player move is simply dropped (with a flash); the consumer echo carries strictly higher sequences, so `pf.Apply` corrects both the in-memory cell data and `LastSeq` and the next move validates against fresh state. Engine-driven coop writes recover faster: the merge-retry path refetches all affected cells in one batched round trip and retries immediately. Per-cell CAS also shrinks this stale window's blast radius — only the specific cells the other player touched can reject, not every write to a shared row.

In **competitive mode** each player keeps its own local score counter (`score atomic.Int64`), incremented by the number of lines it clears. The score is reported to other clients only at game end via the `EventGameOver` event (and rendered locally via `UpdateScore`); the per-player `score` subject is not used.

In **teams mode** each team's score follows the cooperative scheme: a clear adds `teamSize × lines`, published as `EventLineClear{Team, Score, LinesCleared}` and folded by every same-team engine — **both** the score delta and the line count, so teammates' levels and gravity intervals stay in sync (coop folds only the score). Events from the other team are never folded into the own-team `score`; their garbage arrives as `EventShrink` instead.

Additionally every engine keeps a **per-team scoreboard** (`teamScores` / `teamLines`, both `[config.TeamCount]atomic.Int64`): the clearing player folds its score delta and line count into its own team's slots in `handleLockIn`, and every other engine — teammates, the opposing team's players, eliminated players, and spectators — folds `EventLineClear.Score`/`LinesCleared` into `teamScores[ev.Team]`/`teamLines[ev.Team]` in `handleGameEvent`, regardless of team. Each fold emits `UpdateTeamStats` with both teams' score totals and levels (per-team level = `game.Level(teamLines[t])`; also exposed via `Engine.TeamScores()`/`Engine.TeamLevels()`), which drives the live `TEAM A` / `TEAM B` HUD scoreboard on every screen. The events subject is an ordered stream consumed from the start, so a spectator joining mid-game converges on the same totals.

**Top-out transition:**

When Player A's engine detects that the newly spawned piece (at the top of the playfield) cannot be placed **on locked cells** — on shared boards a spawn blocked only by another player's active piece sets `spawnPending` and is retried from `runInput`'s gravity tick instead, the same locked-vs-active distinction `attemptMoveCoop` makes — `handleTopOut(ctx, locked)` (`locked` = caller already holds `e.mu`; `spawnPiece`'s top-out branch always does):
1. Publishes `GameEvent{Kind: EventGameOver, PlayerID: playerA, Score: e.score, Level: e.AchievedLevel(), PieceCount: e.pieceIdx}` to the events subject (`AchievedLevel` = `game.Level(totalLines)`, the level reached at the moment of top-out — recorded in the archive).
2. Calls `e.transitionToSpectator(false)` — sets `mode = ModeGameOver` and emits `UpdateGameOver{Won: false}`. It does **not** itself stop the gravity ticker or move processor; those goroutines self-exit on their next iteration because they guard on `mode == ModePlayer`, and the consumers keep running. `handleTopOut` does not archive, delete the stream, or remove the KV entry.
3. In **cooperative mode**, any top-out ends the game for everyone: `handleTopOut` kicks off `transitionGameToFinished` (CAS the meta to `finished`).
4. In **competitive mode**, finishing is driven by last-player-standing in `handleGameEvent` rather than by `handleTopOut`: each engine tracks `eliminatedPlayers`; when a player receives game-over events for all but one player it calls `transitionToSpectator(true)` for itself if it is the survivor (win) and kicks off `transitionGameToFinished`. A simultaneous top-out (all eliminated) is a draw with no winner. The UI shows a player status list (playing/eliminated) and "YOU WON!"/"YOU LOST" at game over. See `jetricks-gameplays.md` for the authoritative game-over rules.
5. In **teams mode**, `handleTopOut` routes to `handleTeamTopOut` instead: the player vacates their piece from the still-live shared board and spectates while their team plays on, and finishing is driven by whole-team elimination in `handleTeamGameOverEvent` — see the "Teams mode design" section above and `jetricks-gameplays.md` for the authoritative rules.

**Meta transition + game archiving:** `transitionGameToFinished` CAS-retries the meta status to `finished` (setting `FinishedAt`), then — after `time.Sleep(5 * time.Second)`, giving every player time to receive the game-over — invokes `OnGameFinished`, which the front end wires to `archive.ArchiveAndCleanup`. That callback CAS-transitions the meta `finished → archived`, publishes an `ArchiveRecord` to the `JETRICKS_ARCHIVE` stream (subject `jetricks.archive`) with game ID, mode, player count, per-player results (ID, score, achieved level, piece count, winner), start/finish timestamps, and — for cooperative — the total score and final shared level (`TotalScore`/`FinalLevel`), then deletes the game stream, removes the KV entry, and purges the game's chat messages from the shared chat stream (`Purge` with the game's `GameChatSubject`). Archiving is therefore **delayed by ~5 s after game end**, not immediate, and is CAS-protected so only one client performs it.

For **teams mode** the archive builds `playerTeams` from the roster snapshot (the authoritative source) with `EventGameOver`'s `Team` field as the fallback for any player missing from it. The losing team is the team whose **every** member sent an `EventGameOver`; `WinningTeam` is the other team's index (or `-1` if both are dead — a draw). `Winner: true` is set on EVERY member of the winning team, eliminated members included (a team win is shared), `PlayerResult.Team` records each player's team, the record carries `TeamSize`/`WinningTeam`, `TotalScore` is left unset, and the final per-team totals are recorded in `TeamScores`/`TeamLevels` (slices indexed by team, taken from the archiving engine's converged `Engine.TeamScores()`/`TeamLevels()`) — the lobby history line renders them as `A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave` (stats omitted for pre-existing records without them).

---

## 10. internal/lobby

Manages all lobby-level state: player presence, game listings, global chat, invitations, and the lifecycle operations (create game, join game, leave game). Does not know about the UI layer.

`JoinGame` enforces the overall roster cap (`len(Players) >= PlayerCount` → `ErrGameFull`) inside its CAS loop for EVERY mode, in addition to the teams per-team cap (`ErrTeamFull`) — the authoritative guard against overfilling a game (the GUI hides Join on a full game and the agent pre-checks `joinable`, but neither is atomic with the roster).

**Invitations** (`invite.go`) let a creator restrict a game to chosen players. `CreateGame` takes an `inviteOnly` flag (stored on the listing as `InviteOnly` alongside `CreatorID`); `Invite(ctx, toPlayerID, gameID, team)` writes an `Invitation` to the invitee's PER-GAME KV key `config.LobbyInviteKey(invitee, gameID)` = `invites.<invitee>.<gameID>` — a player may hold invitations to several games at once, one key each. The key's lifecycle is the invitation's state machine: written = pending; deleted by the invitee = accepted (`JoinGame` consumes it via `consumeInvite`); rewritten with `Declined: true` (`DeclineInvite`) = declined, KEPT so the inviter sees the refusal until dismissing it; deleted by the inviter (`Uninvite`) = retracted (or a declined marker dismissed); `DismissInvite` silently drops a stale invitation whose game is gone. `handleInviteUpdate` tracks EVERY invitation in the bucket (inviters need the state of the ones they sent), exposed as `MyInvites()` (own pending, oldest first), `InviteTo(gameID)`, and `SentInvites(gameID)` (pending + declined, for the creator's status rows). `JoinGame` guards invite-only games inside its CAS loop: only `CreatorID` or the holder of a fresh invitation (`inviteFor`, read straight from KV to beat watcher lag) may join, and an invitation exempts the joiner from the `MaxAgents` policy (`ErrNotInvited` otherwise). Invitations expire after `config.InviteTTL` (2 min). Every invite action also publishes a lobby event (below).

**Lobby events** (`events.go` kinds + `lobby.go` `publishEvent`/`startEventListener`): every lobby action — `CreateGame`, a successful `JoinGame` roster append, `UnjoinGame` (`game.created`/`game.joined`/`game.left`), `Invite`/`Uninvite`/`DeclineInvite` (`invite.sent`/`invite.retracted`/`invite.declined`) — is also announced as a transient CORE NATS message (`LobbyEvent{Kind, GameID, PlayerID, TargetID, Team, Time}`) on `config.LobbyEventSubject(kind)` = `jetricks.lobby.event.<kind>`, captured by NO stream. `Start` subscribes (`config.LobbyEventsFilter`) and folds foreign events into immediate `LobbyUpdate` pings (games+players for game events, invite for invite events), so player availability and invitation state refresh in real time instead of at KV-watcher/heartbeat latency; `Stop` unsubscribes. `SetReady(ctx, gameID, ready)` is `ToggleReady`'s idempotent sibling (same CAS loop, exact value) used to CLEAR readiness when a player leaves the game screen.

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
    players   map[string]PlayerPresence  // keyed by playerID — access via Players()
    games     map[string]GameListing     // keyed by gameID — access via Games()
    abandoned map[string]bool            // games flagged abandoned — access via AbandonedGames()
    archives  []config.ArchiveRecord     // game history — access via Archives()

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

// AbandonedGames returns a shallow copy of the game IDs the periodic checker
// currently considers abandoned (see runAbandonedChecker below).
func (l *Lobby) AbandonedGames() map[string]bool

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

// CreateGame creates a new game; playerCount is selected by the user in the
// create game form. For teams mode, teamSize is the number of players per team
// and playerCount must be the total (TeamCount×teamSize); other modes pass
// teamSize 0. Sets meta.TeamSize and listing.TeamSize.
func (l *Lobby) CreateGame(ctx context.Context, mode config.GameMode, playerCount, teamSize int) (string, error)

// ErrTeamFull is returned by JoinGame when the requested team already has
// teamSize members.
var ErrTeamFull = errors.New("team is full")

// JoinResult is the roster position assigned to a player by JoinGame — the
// values passed to engine.New as playerIdx/teamIdx/teamSlot.
type JoinResult struct {
    PlayerIdx int // global roster index (0 for creator, 1 for first joiner, …)
    Team      int // teams mode: 0 = A, 1 = B
    TeamSlot  int // teams mode: section index within the team board
}

// JoinGame joins an existing game. For teams mode, team selects which team to
// join (0 or 1) and may fail with ErrTeamFull; other modes ignore it.
func (l *Lobby) JoinGame(ctx context.Context, gameID string, team int) (JoinResult, error)
func (l *Lobby) LeaveGame(ctx context.Context, gameID string) error
// ToggleReady toggles the local player's ready state and returns a snapshot:
// whether all players are now ready, the player list, and the caller's new state.
func (l *Lobby) ToggleReady(ctx context.Context, gameID string) (ToggleReadyResult, error)
func (l *Lobby) StartGame(ctx context.Context, gameID string)  // transitions game to in_progress after countdown
func (l *Lobby) SendChat(ctx context.Context, text string) error                                  // lobby chat
func (l *Lobby) SendGameChat(ctx context.Context, gameID, text string, spectator bool) error      // one game's chat

type ToggleReadyResult struct {
    AllReady bool
    Players  []PlayerSummary
    MyReady  bool
}

// runAbandonedChecker (goroutine started by Start) re-evaluates every listed
// game for abandonment every config.AbandonedCheckInterval (1 min) via
// checkAbandoned, which rebuilds the abandoned set from scratch (so a game
// where activity resumes is un-flagged) and emits LobbyUpdateGames on change.
func (l *Lobby) runAbandonedChecker(ctx context.Context)
func (l *Lobby) checkAbandoned(ctx context.Context)

// isAbandoned applies the rules to one listing: created/starting games are
// abandoned config.AbandonedUnstartedTimeout (15 min) after CreatedAt;
// in_progress games once the game stream's State.LastTime is older than
// config.AbandonedIdleTimeout (1 min) — or immediately if the stream is gone
// (ErrStreamNotFound); other errors don't flag (can't tell). `now` is a
// parameter so tests inject a future time instead of waiting.
func (l *Lobby) isAbandoned(ctx context.Context, g GameListing, now time.Time) bool

// DeleteGame tears down an abandoned game entirely: DeleteGameStream, then
// natspkg.PurgeGameChat (the game's messages in the shared chat stream), then
// the KV listing delete — whose watcher event removes the game (and its
// abandoned flag) from every client's maps.
func (l *Lobby) DeleteGame(ctx context.Context, gameID string) error
```

`JoinGame`'s listing update runs as a **CAS loop** (Get → mutate → `kv.Update(rev)` → retry on revision mismatch, like `ToggleReady`), replacing the old plain `kv.Put`: team capacity validation and `TeamSlot` assignment (the count of existing members on that team) must be atomic with the roster append, or two concurrent joins could both land on a full team / claim the same slot. The roster entry — whose `PlayerSummary` payload now includes `Team`/`TeamSlot` — is published only AFTER the CAS commit. "Both teams full → starting" reuses the existing `len(Players) >= PlayerCount` transition, since per-team capacity is enforced before the append.

The maps are unexported and accessed only through `Players()` and `Games()`, ensuring all reads hold the read lock and all writes hold the write lock. The KV watcher goroutine (in `listing.go`) calls `l.mu.Lock()` / `l.mu.Unlock()` around every map mutation. The UI calls `l.Players()` / `l.Games()` which take `l.mu.RLock()`, copy the map, and release before returning. The copy is a shallow copy of the map (new map, same value structs) — since `PlayerPresence` and `GameListing` are value types, this is safe.

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
    TeamSize    int               `json:"team_size,omitempty"` // teams mode: players per team
    Players     []PlayerSummary   `json:"players"`       // currently joined players
    CreatedAt   time.Time         `json:"created_at"`
    FinishedAt  time.Time         `json:"finished_at,omitempty"` // zero if not finished
}

// TeamMemberCount returns how many roster members belong to the given team —
// used by JoinGame's capacity check / slot assignment and the lobby UI's
// per-team join buttons.
func (g GameListing) TeamMemberCount(team int) int

// There is no lobby-local GameStatus type. GameListing.Status uses
// config.GameStatus (a string type with GameStatusCreated/Starting/InProgress/
// Finished/Archived/Cancelled, defined in internal/config).

type PlayerSummary struct {
    PlayerID string `json:"player_id"`
    Name     string `json:"name"`
    Ready    bool   `json:"ready"`
    Team     int    `json:"team"`      // teams mode: 0 = A, 1 = B
    TeamSlot int    `json:"team_slot"` // teams mode: section index within the team board (join order)
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
    ChatMsg *ChatMessage  // non-nil for LobbyUpdateChat (informational — the UI re-reads Lobby.ChatLog())
}

type ChatMessage struct {
    PlayerID  string    `json:"player_id"`
    Name      string    `json:"name"`
    Text      string    `json:"text"`
    Timestamp time.Time `json:"timestamp"`
    Spectator bool      `json:"spectator,omitempty"`
    // GameID scopes the message ("" = lobby). Not part of the payload — the
    // chat consumer derives it from the delivery subject, since lobby and
    // game chat share one stream distinguished purely by subject naming.
    GameID string `json:"-"`
}
```

The lobby's chat consumer (`runChatConsumer`) consumes the whole chat stream
unfiltered and tags each message with `GameIDFromChatSubject(msg.Subject())`;
the UI filters per screen (lobby screen: `GameID == ""`; a game's screen: that
game's ID plus lobby messages).

**The chat log lives in the Lobby, not in the UI.** `emitUpdate` is a
non-blocking send that drops updates when `Updates` is full — which is routine
during login, where `Start()`'s consumers replay KV presence/games, the
archive history, and the whole chat backlog before the UI pump attaches (the
`Updates` buffer is 256 to keep drops rare, but they remain possible). Every
other update kind is a "re-read the snapshot" ping, so a drop only costs a
repaint — and chat works the same way: `runChatConsumer` appends each message
to a mu-guarded `chatLog` (capped at `chatLogCap` = 200) before emitting, the
UI's `pumpLobby` re-reads `Lobby.ChatLog()` on every chat ping instead of
appending the update's `ChatMsg`, and `initLobby` seeds `App.chatLog` from
`ChatLog()` when the pump attaches so a backlog replayed during login shows
even if all its pings were dropped. (This fixed a real bug: a second player
joining a lobby with chat history and enough replay traffic to fill the old
16-slot buffer saw an empty chat panel, while the first player — whose pump
was already draining when the lines arrived live — saw all of them.)

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

## 12. Front end: the native Gio UI

Jetricks has a single front end, `internal/nativeui`, over the engine/lobby logic. It depends on `engine` and `lobby` (one-way) and communicates with them exclusively through their `Updates` channels and exported method calls — it is never imported by the business logic.

**`internal/nativeui`** is a native OS window built with **Gio** (`gioui.org`, pure-Go, cross-platform). It reads `engine.Updates` / `lobby.Updates` directly in bridge goroutines and repaints via `window.Invalidate()`, and it calls `engine.MoveLeft()` etc. directly from a key handler — a NATS update reaches the screen within one display frame. Files: `app.go` (window + frame loop + screen state machine), `bridge.go` (the `pumpEngine`/`pumpLobby` channel→UI pumps), `login.go`/`lobby.go`/`game.go` (screens), `archive_view.go` (the `screenArchive` history viewer — redraws a finished game's saved end-of-game boards), `board.go` (board drawing), `input.go` (keyboard → engine moves), `lifecycle.go` (login/create/join/spectate/countdown/teardown), `natslog.go` (the "Show NATS messages" panel: `recordStreamMsg` wired as `engine.OnStreamMsg`, the bottom message strip, a display-only JSON colorizer), `brand.go` (the embedded nats.io "N" logo — `nats-icon.png`, `go:embed` — the lobby/archive branding banner, and the inline `natsTag` "N"+"NATS.io" chip used on the login tagline and at the foot of the game HUD), `fonts.go` (the embedded "Press Start 2P" pixel face — `PressStart2P-Regular.ttf`, SIL OFL 1.1, license in `PressStart2P-OFL.txt` — with `uiFontCollection` and the `a.pixel` label helper), `fireworks.go` (the victory fireworks overlay for competitive/teams wins), `colors.go` alias to `internal/render`. Controls: ←/→ move, ↓ soft drop, ↑ or X rotate CW, Z rotate CCW, Space hard drop. Keyboard focus uses Gio's `key.FocusFilter` + `key.FocusCmd` on the board tag.

**Invite-only create flow (`invite.go`).** The create row's "Invite only" checkbox switches the button to "Create & Invite"; clicking it creates an invite-only game and opens the **invitee-picker** modal (`invitePickerOverlay`) over the lobby. There is NO send button — `handleInvitePicker` diffs each row's widget against its last-applied intent (`inviteChoice.lastSel`/`lastTeam`) every frame and dispatches immediately: selecting sends the invitation (`sendInvite` → `lobby.Invite`; a teams change re-invites to the new team), deselecting retracts it (`retractInvite` → `lobby.Uninvite`). The pinned first row is the CREATOR (`inviteSelfRow`, `inviteSelfSel`/`inviteSelfTeam` on `App`): pre-selected — creating an invitation game implies accepting your own invitation, so `openInvitePicker` takes a seat immediately (`lobby.JoinGame`, team A default) — and deselecting frees it (`selfSeat` → `UnjoinGame`; teams moves re-seat via unjoin+join). Rows carry live status from `pickerRowStatus` (roster + `SentInvites`): pending "✉ invited — waiting…", declined "✕ declined" (widget reset; re-selecting re-invites), joined/ready "joined ✓"(· ready) with the control hidden. `inviteSeatUsage` (roster + pending invites, per team for teams) backs the capacity guard, which REFUSES a selection that would over-fill a game/team by reverting the widget and showing `invitePickerErr`. The candidate list is reactive: `syncInvitePickerCandidates` runs each frame and `reconcileInvitePicker` folds in lobby joiners and drops leavers — except players involved with THIS game (roster/invitees, the `keep` set), who stay listed. **When the roster fills, `handleInvitePicker` closes the overlay and hands the creator over automatically**: `joinGame` (ready screen) if they kept their seat, `spectateGame` otherwise. "Close" merely hides the overlay (the game keeps filling; the lobby row shows the same status); "Cancel game" retracts all outstanding invitations (`cancelInviteGame`) and deletes the game. An invited player's client shows the incoming pop-up (`incomingInviteOverlay`, driven by `lobby.MyInvites` — oldest pending first, the next surfacing once answered), which lists the game's current roster (who joined, team, ready), with Accept & Play (→ `joinGame`, consuming the invitation) / Decline (`DeclineInvite`, or `DismissInvite` when the game is gone). Both modals are Stacked over the lobby behind a click-swallowing scrim. Game rows tag invite-only games "· invite only" and hide Join from anyone but the creator or a pending invitee (`InviteTo`); the creator's row additionally renders `inviteStatusRows` — one line per outstanding invitation (pending with an **Uninvite** button, declined with **Dismiss**, via the per-(game,invitee) `uninviteBtns` clickables) — and roster names in invite-only rows read "(joined)"/"(joined · ready ✓)".

**Spectator overlays and history controls.** For spectators the pre-game countdown overlay renders over the multi-board views exactly as over a player's board (`countdownVisible` admits every non-finished mode; `gameBoardArea` stacks the overlay over the spectator content, and `runMetaConsumer` emits `UpdateGameStatus` to every engine — spectators included — so the overlay clears the moment the meta reads in_progress; visibility is gated on a PRE-START status check, not is-in-progress, so the stale GO! cannot resurrect when the status moves past in_progress to finished). Spectators also render every player's **CAS-failure flash**: a player broadcasts its dropped-write flash over CORE NATS (`config.FlashSubject`, outside the game stream's capture so it is never persisted), spectator engines subscribe (`runFlashConsumer`, `initialMode == ModeSpectator`) and re-emit it as `UpdateCASFlash`, and the UI keys it per board (`specFlash`, by player index competitively / team in teams) — players still see only their own flash (local, `emitCASFlash`). In the competitive spectator view each eliminated player's board carries a centered **OUT** chip (only the chip has a background — the board stays visible) and, once the game is decided, the survivor's board reads **WINNER** (`spectatorBoards` + `boardOverlay`, driven by `eng.IsEliminated` over the roster); the teams view does the same per team board (**OUT** / **WINNERS**, `spectatorTeamBoards`). The lobby's GAME HISTORY header carries a sort selector (`histSortEnum`: "By score" — the default `sortedArchives` ranking — or "By date" — `sortedArchivesByDate`, most recent first) and an "Agent games" checkbox (`histAgentsCb`, checked by default) that filters out records with agent seats via `ArchiveRecord.HasAgents` (`archivesForDisplay`); the agent flag on each archived seat (`PlayerResult.Agent`) is stamped from the roster snapshot by `ArchiveAndCleanup`.

**Look and feel — modern 8-bit, NATS-branded.** Display type (the login title, section headers, buttons, HUD stats, ready badges, the countdown, the game-over dialog, and the branding banner) renders in the pixel face (`pixelTypeface`); body text (chat, lists, editors) stays in the Go faces for readability. All chrome corners are square; panels, editors, and the context pull-down carry chunky 2 dp `colBorder` frames; buttons and the game-over dialog sit on `hardShadow`'s offset solid shadow (`board.go`). Every playfield is drawn inside a `colBorder` arcade-well frame (`drawBoard`), filled cells are shaded with the classic 8-bit bevel — lighter top/left strips, darker bottom/right, a gloss pixel — gated by `CellAppearance.Bevel`, and `scanlines` paints a subtle CRT overlay over every frame (last in `App.layout`). The palette (`app.go`) is a dark blue-black (`colBg`/`colPanel`/`colBorder`) with the **NATS brand blue** `#27aae1` as `colAccent` and the NATS logo green as `colNATSGreen`, so the branding runs through the whole chrome; the login screen flanks the "JETRICKS" pixel title with NATS logos and ends with a "peer to peer · made with NATS.io" tagline. The theme is built by `newUITheme` (shared with the layout tests, so snapshots match the live window).

**Login screen connection picker.** The App is built via `NewWithPicker` and starts with nil `js`/`kv`; there is a single combined login screen — name entry plus a **CONNECT TO** section (`connSection`, `login.go`): a "Context:" radio paired with a pull-down button (`connDropButton`, an editor-style bordered box showing the chosen context `connCtx` and a ▼/▲ arrow); clicking it expands `connDropList`, a bordered scroll-capped (`~180dp`) `material.List` of the contexts from `nats.ListContexts` — the CLI's selected context labeled "(selected)", the current choice highlighted in the accent color — and picking a row (or merely touching the pull-down) also selects the context radio. Below it sits a "NATS URL" radio with an editable URL field; typing in the URL editor auto-selects its radio, but the constructor's programmatic `SetText` queues one synthetic `ChangeEvent` that is swallowed via the `connURLSeeded` flag so it cannot override the context default on the first frame. Default choice and URL text are seeded from the CLI flags (`--server` → URL option with that value; `--context` → the context option with the pull-down preset to it, appended to the list if undiscovered; else the CLI's selected context; else the URL option with `DefaultNATSURL`); whichever option starts out, `connCtx` is preset to `--context`, else the CLI's selected context, else the first known context. A **Check connection** row (`connCheckRow`) dials the current choice off the UI goroutine (`doCheckConn` → `nats.CheckConnection`), shows "Checking…" while busy, and renders `✓ <server> · ping <rtt>` (green, via `formatRTT`) or `✗ <error>` (red); the probe connection is closed immediately and provisions nothing. On Play, `submitLogin` resolves the choice (`pickerConfig`) and dispatches `doConnectAndLogin` (`lifecycle.go`): it first `disconnect()`s any connection left over from a previous attempt (e.g. a cancelled name collision), then runs `nats.Bootstrap` under a 15 s cap — errors land on the login screen for retry, success stores `a.nc/a.js/a.kv` (the App owns the connection — `teardown`/`DrainConn` drain it) and falls through into the normal `doLogin` flow. `quit()` (lobby → login) also `disconnect()`s, so the player always lands back on the full chooser and can switch servers. `App` state: `nc`, `needConn`/`connContexts`/`connSelected`/`connCfg`/`lanIP` (immutable after construction), `connChecking`/`connCheckOK`/`connCheckMsg` (mu-guarded), and the `connEnum`/`connCtx`/`connDropOpen`/`connDropBtn`/`connOptBtns`/`connURLEd`/`connPortEd`/`connList`/`connCheckBtn` widget state (all UI-goroutine only).

**LAN mode (embedded NATS server).** The chooser's third, always-present option (`connEnum` value "embedded") makes the player the host. The radio row is followed by an indented "Port:" digits-only editor (`connPortEd`, pre-filled with `config.DefaultEmbeddedPort` = 4222; editing it auto-selects the option, with a `connPortSeeded` swallow for the constructor's synthetic `ChangeEvent`, exactly like `connURLSeeded`) and, while the option is selected, an indented `Your server's URL is nats://<lan-ip>:<port>` line built from `App.lanIP` (resolved once in `NewWithPicker` via `nats.LanIP`) and the entered port. `pickerConfig` marks the config with `RunEmbedded` and the parsed `EmbeddedPort` (`pickerPort`: empty field → the default, otherwise a valid 1–65535 port or a login error), and `doConnectAndLogin` calls `ensureEmbeddedServer(cfg.EmbeddedPort)` (lifecycle.go), which starts `nats.StartEmbeddedServer(config.EmbeddedStoreDir, port)` — an in-process JetStream-enabled `nats-server` (default account, no auth) on `0.0.0.0:<port>` with its storage in `./jetstream-data` — records the shareable address `<lan-ip>:<port>` in `embAddr`, and rewrites the config to `nats://<lan-ip>:<port>` so the normal `Bootstrap` path provisions and connects through the same address other players dial. A running server is reused when the requested port matches; asking for a different port shuts it down and restarts it there. Not loopback: a foreign nats-server holding a `127.0.0.1:<port>`-specific bind would intercept a loopback dial even though the embedded `0.0.0.0` bind succeeded; as belt and braces, after connecting the app compares `nc.ConnectedServerId()` with the embedded server's `ID()` and fails the login with a clear "port already in use by another NATS server" error on a mismatch. While the current connection is to the embedded server (`usingEmbedded`, cleared by `disconnect`), the lobby header shows `YOUR SERVER'S URL IS nats://<ip>:<port> — share this address so others can join you` (`embeddedAddr`); other players connect to that address via the NATS URL option. The server outlives lobby exits (quit keeps it up for connected friends) and is shut down in `teardown` when the window closes. `App` state: `embSrv`/`embAddr`/`usingEmbedded` (mu-guarded).

**In-game chat.** The game screen has a chat strip at the bottom (`gameChatPanel`, `game.go`), shown to players and spectators: a scroll-capped list of this game's messages plus the lobby chat folded in — lobby lines are prefixed `@lobby` and colored `colLobby` so they're obviously not from the game (`chatLine` formats rows; game messages from spectators are marked `(spec)`). Typing rules (`canType` in `layoutGame`): players may type until the game starts; once `in_progress` their keyboard drives the piece — `handleKeys` grabs board focus only while `mode == ModePlayer && started`, which both frees the editor pre-start and locks it out during play — so only spectators and eliminated players can type (players see a muted "read-only while playing" hint instead of the editor). A message starting with `@lobby` is routed to the lobby chat; anything else goes to this game's chat subject (`sendGameChat`, `lifecycle.go` → `Lobby.SendGameChat`). Widgets: `gameChatEd`/`gameChatBtn`/`gameChatList` (scroll-to-end). The lobby screen's chat panel shows only lobby-scoped messages.

**Ready checklist styling.** While waiting for players to ready up, the HUD's ready list (`readyArea`, `game.go`) shows each player's name with a filled square-cornered tag on the right — green "READY" (`colGo`) or red "NOT READY" (`colErr`), dark pixel-face text, drawn by `readyBadge` over a plain `fillRect` — replacing the earlier subtle "✓ / …" prefix marks.

**Button styling.** Primary actions (Play, Join, Ready, Create Game, Send) use the `primaryButton(gtx, btn, label)` helper (`lobby.go`): the filled-accent `material.Button` restyled by `pixelize` (square corners, pixel face, 11 sp) over a `hardShadow`. Non-primary actions — Spectate, Quit, Back to Lobby, and the login collision-dialog Cancel — use the `secondaryButton(gtx, btn, label)` helper: an accent-colored (`colAccent`) pixel-face label and 2 dp accent border over the `colPanel` background, also on a hard shadow, so they read as clearly clickable instead of blending into the dark window background (a bare `colPanel` fill made them look disabled even though they worked). Destructive actions — the abandoned-game Delete and its "Yes, delete" confirmation — use `dangerButton(gtx, btn, label)`: the same secondary chrome but with the error red (`colErr`) label and border, so they cannot be mistaken for Join/Spectate. The spectator's HUD keeps the "Back to Lobby" button (the same `backBtn` → `leaveCurrentGame` path as a player), so a spectator can always return to the lobby.

**Leave & rejoin (`game.go` backBtn / `lifecycle.go` `leaveCurrentGame`).** "Back to Lobby" no longer just tears the screen down. For a player in an IN-PROGRESS game (`started && !gameOver`) the click sets `App.confirmLeave` and `layoutGame` stacks a scrim + `confirmLeaveOverlay` modal — "LEAVE GAME? … you can rejoin it from the lobby" with **Yes, leave** (`dangerButton` `leaveYesBtn`) / **No, keep playing** (`leaveNoBtn`); any other state leaves directly. `leaveCurrentGame` then: clears the READY mark if set (`lobby.SetReady(gameID, false)` — leaving pre-start revokes readiness so the countdown can't fire without you), calls `lobby.LeaveGame` (presence back to In Lobby) ONLY when the game is finished/gone or we hold no seat (`gameAlive`/`rosterHas` helpers), and finally `returnToLobby` (which, like `startGameScreen`, also resets `confirmLeave`). While a live seat is held the lobby row shows the game from the player's point of view — the status token reads **joined** (created/starting) or **playing** (in progress), in green — and the Join/Spectate buttons are replaced by a single **Rejoin** (`gameRow`'s `rejoin` path, reusing `btns.join` → `joinGame`, whose already-seated `JoinGame` branch returns the same `PlayerIdx`/team; the engine replays the stream to the live board). `startGameScreen` seeds `myReady` from the roster on (re)join so the READY button label is roster-accurate.

**Abandoned-game deletion (lobby).** `layoutLobby` reads `lb.AbandonedGames()` each frame (the set is maintained by the lobby package's minute-interval `runAbandonedChecker` — see Section on `internal/lobby`) and passes each row's flag into `gameRow(gtx, g, abandoned)`. An abandoned row appends a red `· abandoned` tag to its info line and shows a `dangerButton` **Delete** after Join/Spectate (`gameRowBtns` gains `del`/`delYes`/`delNo` Clickables in `app.go`). Clicking Delete stores the game in `App.confirmDeleteID` (UI-goroutine only); while it matches, the row's action buttons are replaced by a confirmation rendered on its **own line under the game info** (a vertical flex — beside the info it would squeeze the `· abandoned` tag): "Are you sure you want to delete this game?" in `colErr` with **Yes, delete** (`dangerButton`) and **Cancel** (`secondaryButton`) — so a stray click can't join or delete. Confirming clears `confirmDeleteID` and dispatches `go a.deleteGame(id)` (`lifecycle.go`) → `lobby.DeleteGame`, which deletes the game stream, purges the game's chat from the shared chat stream, and deletes the KV listing (removing the row everywhere).

Two small packages support the front end:
- **`internal/render`** — the single source of truth for cell/board appearance (piece/player colors, blend math) for the native UI. Exposes a single decision function, `CellStyle`, plus the RGBA surface (`CellAppearance` — fill, outline, outline width, and the `Bevel` flag that gates the drawer's 8-bit shading on filled cells — `PlayerColorRGBA`, `PlayerColorHex`), so every render path (own board, opponent boards, spectator view) draws from one visual model.
- **`internal/archive`** — `ArchiveAndCleanup(ctx, js, kv, eng, lb, gamePlayers)`, wired as `engine.OnGameFinished`; records the finished game to the archive stream and tears down its NATS resources. Before deleting the game stream it calls `buildBoardPictures`, which reads the latest message per cell (`FetchPlayfieldState`) for every board in the game — one for cooperative, one per player for competitive, one per team for teams — and stores them sparsely (non-empty cells only) as `ArchiveRecord.Boards`, so the end-of-game playfield survives the stream deletion.

**History list ordering & summary line.** The `GAME HISTORY` list is sorted by `sortedArchives` (`lobby.go`): headline score descending (`archiveScore` — co-op `TotalScore`, best entry of `TeamScores` for teams, best player score for competitive), and between two games with the same score the one with the shorter duration ranks higher (`archiveDuration` = `FinishedAt - StartedAt`, zero when either timestamp is missing), with `FinishedAt` (newest first) breaking remaining ties. Each summary line (`archiveLine`) is prefixed by `archiveWhen`: the start date/time in the viewer's local timezone (`2006-01-02 15:04 MST` format) and the duration rounded to the second (e.g. `2026-07-06 14:03 PDT · 4m32s · co-op · …`); records without timestamps skip the prefix and show just the mode-specific part (`archiveModeLine`).

**History viewer.** Each `GAME HISTORY` row shows its summary line with an accent-bordered **"View board"** button on the right (`viewBoardButton`, one `archiveBtns` Clickable per row) so it is obvious the finished game can be opened. Clicking it opens `screenArchive` (`archive_view.go`), which rebuilds each saved `BoardPicture` into an `engine.BoardSnapshot` (`boardSnapshotFromPicture`) and redraws it with the same `boardWidget` used live — cooperative shows the single wide board, competitive a board per player labeled by ID in player color, teams the two team boards. A `secondaryButton` "Back to Lobby" returns to the lobby.

**Cell appearance — single source of truth.** Every cell is drawn with an explicit fill color and outline computed by `internal/render`. Piece fills come from a piece-color table composited over the board background via `blend(fg, bg, alpha)` (active ≈0.9, locked ≈0.7, adversarial ≈0.8). Outlines: own active → white; spectator (`localPlayerIdx < 0`) → per-player color on active/locked cells; other player's active piece in a player view → grid line; locked non-adversarial → per-player color when `showOutline` (suppressed to the grid line on compact opponent boards). Because appearance is computed in one package, the visual model stays consistent across own/spectator/opponent renders. In competitive mode the UI distinguishes own-field updates (`UpdatePlayfield`) from opponent updates (`UpdateOpponentField`, keyed by `OpponentID`) and redraws the corresponding sidebar board. In cooperative mode the single wide playfield (playerCount × StandardWidth columns) is drawn directly — already the correct width, so there is no concatenation or visual separator between player sections.

**Ready/countdown flow:** While waiting for the game to start, each player sees the list of players with their ready state (green checkmark or red cross). Players toggle their ready state via the READY/NOT READY button (→ `lobby.ToggleReady`). When ALL players are ready, the button and player list are replaced by a 5-second countdown (5...4...3...2...1...GO!). During the countdown, players cannot change their ready state. After the countdown, the game transitions to `in_progress` and pieces begin to spawn.

The game over overlay is shown on `UpdateGameOver`: in cooperative mode any top-out ends the game for all; in competitive mode it shows "YOU WON!"/"YOU LOST" once the player is eliminated or is the last standing. Below the title/verdict, `gameOverBox` shows the final score in gold above the "Back to Lobby" button: `Score: N (level L)` (the shared total) for cooperative, `Your score: N (level L)` for competitive, and both team totals with the player's own team first (`TEAM A 42 (lvl 3) · TEAM B 17 (lvl 1)`) for teams — `gameOverBox` takes the local team index (`eng.TeamIdx()`) for this. The game screen hides controls and the ready button for spectators, showing "Spectating" as the player status instead.

**Victory fireworks** (`fireworks.go`). When `UpdateGameOver{Won: true}` arrives for a competitive or teams game, `pumpEngine` rolls a `fireworksShow` (`newFireworksShow`, plain `math/rand` — the deterministic-RNG rules apply to the engine, not the UI) and stores it on the App; `startGameScreen`/`returnToLobby` reset it. `layoutGame` stacks `fireworksOverlay` over the whole game screen (`layout.Stack`, paint-only ops — no `event.Op` — so input still reaches the widgets underneath) and keeps calling `invalidate()` while `fireworksShow.active(gtx.Now)`, following the countdown/CAS-flash idiom: each frame is a pure function of `gtx.Now`, no per-frame state is mutated. Once started the show never ends on its own: `active` stays true and the overlay draws at elapsed time modulo the ~8 s `cycle`, replaying the same choreography until the show is dropped. One cycle is 12 rockets staggered ~420 ms apart: each rises from the bottom edge as a warm-white streak (`drawRocketStreak`, ease-out cubic) to a random apex, then explodes into a logo burst (`drawLogoBurst`, 2.4 s) — the NATS "N" for nine rockets in ten, the **Synadia "Symbol"** (embedded `synadia-icon.png`, the official mark from synadia.com/about/brand — the white "S" swirl on the emerald rounded square) for the tenth — with a floor of one Synadia rocket per show, forced after the roll if none came up, since a show without one would loop forever without it (~28% chance over 12 rockets); there is no other burst kind. A burst has three phases: the particles pop out (easeOutBack over the first 25%) into a small replica of the logo, the intact logo holds while drifting down slightly, and at the halfway point (`fwScatterStart`) it splits into its small squares and blows apart — each block shrinks inside its grid cell (seams appear, making the break-up visible immediately) and flings outward along its own radial-from-center scatter velocity (ease-out spread, gravity droop), the debris shrinking as it flies rather than dimming in place, with an alpha fade only over the final quarter of the scatter. Each rocket also rolls a scatter tint at show creation: about two bursts in three recolor their flying blocks (`lerpColor` over the first 60% of the scatter) toward one traditional fireworks color from `fwBurstPalette` — gold, red, green, blue, purple, or silver — while the rest keep the logo’s own colors. The particles come from `fwLogoPoints` / `fwSynadiaPoints` (both via `sampleLogoPoints`, with a NATS fallback if the Synadia PNG fails to decode), which sample the embedded `nats-icon.png` / `synadia-icon.png` once on a 22×22 grid keeping one particle per mostly-opaque pixel with its color — the four brand-color quadrants and the white "N", the emerald square and white "S" — so the burst is the real logo, not an approximation; each particle also carries its precomputed scatter velocity (radial direction, random direction at the very center, 0.6–1.4× speed jitter from a fixed-seed RNG). Teams re-emits `Won: true` to already-eliminated members of the winning team, so their screens celebrate too; spectators and cooperative games get no fireworks.

**Teams mode UI.** In the lobby, the create-game form has a third mode radio ("teams"); for teams the count editor means players **per team** (its label flips to "Per team:") and `createGame` converts it to `(playerCount, teamSize)` for `lobby.CreateGame`. Each game row (`gameRow`) shows per-team rosters ("A: alice ✓ · B: bob") and **two** join buttons — "Join A (n/size)" / "Join B (n/size)" (`teamJoinButton`, `teamName` helpers; each button is hidden when its team is full; `gameRowBtns` gains `joinA`/`joinB` Clickables in `app.go`). `joinGame(gameID, team)` passes the resulting `JoinResult{PlayerIdx, Team, TeamSlot}` into `engine.New`; `spectateGame` passes `0,0`. The archive history line (`archiveLine`) renders "teams · A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave" using `WinningTeam` and the record's `TeamScores`/`TeamLevels` (stats omitted for older records without them); coop lines show `total N (lvl L)` and competitive lines show each player's `score (lvl L)`. On the game screen the HUD label reads "Teams · TEAM A/B", the single SCORE stat is replaced by a live per-team scoreboard — one `TEAM A` / `TEAM B` stat per team fed by `UpdateTeamStats` (own team's value in the accent color; spectators see each team's `score · lvl N` inline and no single SCORE/LEVEL stat), the legend groups players under TEAM A / TEAM B headers (swatch colors stay keyed by the global roster index), eliminated players get an "(out)" marker, and the opponent sidebar — fed by `OpponentSnapshots()` under `TeamBoardKey` — is shown labeled "OPPOSING TEAM". `gameOverBox` takes the game view and shows an interim "YOU'RE OUT / Your team plays on" while the player is eliminated mid-game, then "YOUR TEAM WON!"/"YOUR TEAM LOST" once the outcome is decided; spectators get `spectatorTeamBoards`, which renders both team boards side by side.

---

## 13. Event Channel Contracts

All cross-package communication uses buffered Go channels. The buffer size is chosen to absorb brief bursts without blocking the sender goroutine.

| Channel | Direction | Buffer | Notes |
|---------|-----------|--------|-------|
| `engine.Updates` | engine → front end | 64 | High-frequency during play (gravity ticks, every cell update). Consumed by the native bridge (`pumpEngine`). Dropping updates here is preferable to blocking the engine. If the channel is full the engine drops the update — the next update will correct the display. |
| `lobby.Updates` | lobby → front end | 16 | Lower frequency. Lobby changes are infrequent relative to game updates. |
| `engine.moves` (internal) | front end → engine | 8 | Player move requests dispatched onto the engine's internal moves channel (`runInput` reads it). Inputs are **serialized and buffered**: `runInput` processes them one at a time and each move's publish blocks on its batch commit ack (then applies the write-through) before the next move is dequeued, so a player never has two input batches in flight — a move issued while the previous one is still awaiting its ack waits in this buffer. The non-blocking send drops excess input rather than blocking the UI goroutine if a player outruns the ack round-trip by more than the buffer depth (not reached at human input rates). The engine keeps a FIFO **mirror** of this buffer (`bufferedMoves`, guarded by `bufferedMu`): `dispatch` appends on a successful enqueue, `runInput` pops via `popBufferedMove` the moment it dequeues a move, and `Engine.BufferedMoves()` exposes a copy. The UI renders it as the muted `← ← CW HD` line under the player's board (visible only when high RTT makes inputs queue); an `UpdateBufferedMoves` event triggers the redraw. |

Channels are never closed by the sender — they are abandoned when the owning goroutine exits via context cancellation. Receivers must always select on both the channel and `ctx.Done()`.

---

## 14. Bootstrap Sequence

The following steps happen in order at startup. Steps that can fail cause the application to exit with a clear error message.

```
1.  Parse CLI flags → config.Config (no connection is made at startup;
    --server/--context only seed the connection picker's defaults)
2.  natspkg.ListContexts() → context names + selected (warn-only on error)
3.  nativeui.NewWithPicker(cfg, names, selected); runNative: a goroutine calls
    App.Run (opens the Gio window); app.Main() owns the OS main thread
    → the native window shows the single combined login screen: name entry +
    CONNECT TO chooser (the optional "Check connection" button dials + pings +
    closes, no side effects)
4.  Player enters name, picks a connection (context or URL), and hits Play:
    validate (config.ValidatePlayerName), then doConnectAndLogin:
    a. disconnect() any connection left from a previous attempt, then
       natspkg.Bootstrap for the chosen context/URL (15 s cap): connect
       (ConnectURL when a URL, else natscontext.Connect) then
       EnsureLobbyChatStream, EnsureLobbyKV, EnsureArchiveStream — on failure
       the error shows on the login screen and the player retries with a
       different choice; on success a.nc/js/kv are set (the App owns the
       connection)
    b. Check lobby.IsNameInUse
    c. Create lobby.Lobby with playerName as both playerID and name
    d. Start lobby (KV watcher, chat consumer, archive consumer, heartbeat,
       abandoned-game checker)
    e. Wait for initial KV load
    f. Run cleanup.Run
    g. Move to the lobby screen
5.  Quit (lobby) → stop the lobby, disconnect(), back to the combined login
    screen — the player can connect to a different server
6.  Block on window close / os.Signal (SIGINT / SIGTERM)
7.  On exit: stop engine + lobby, cancel root context → goroutines exit,
    teardown / App.DrainConn drain the app-owned connection
8.  Exit
```

Step 4e — waiting for the KV watcher to finish its initial load — is critical for correctness of the cleanup pass (step 4f). The initial load is complete when the KV watcher receives a nil entry, which NATS delivers after all existing entries have been sent.

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
| Abandoned-game checker (`runAbandonedChecker`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Own-cells consumer (`runConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Events consumer (`runEventsConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Meta consumer (`runMetaConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Countdown consumer (`runCountdownConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Input + gravity loop (`runInput`) | `engine.Engine` | `engine.Start()` (ModePlayer only) | ctx cancel |
| Roster consumer (`runRosterConsumer`) | `engine.Engine` | `engine.Start()` (competitive only — teams does not run it) | ctx cancel |
| Per-opponent cells consumer (`runConsumer`) | `engine.Engine` | `startOpponentConsumer` per discovered opponent (competitive) | ctx cancel |
| Opposing-team board consumer (`runConsumer`) | `engine.Engine` | `startTeamBoardConsumer` from `engine.Start()` (teams only; spectators consume team 1 through it) | ctx cancel |
| Lobby/game update pumps (`pumpLobby` / `pumpEngine`) | native bridge (`nativeui`) | one per attached lobby/engine | ctx cancel |

---

## 17. orbit.go Module Reference

All orbit.go modules are independently versioned. Import only the modules needed rather than the whole library.

| Module | Import path | Used in | Purpose in Jetricks |
|--------|-------------|---------|-------------------|
| `natscontext` | `github.com/synadia-io/orbit.go/natscontext` | `internal/nats` | Connect using NATS CLI context files. Replaces raw URL + credential flags with a single context name, sharing config with the `nats` CLI tool. |
| `jetstreamext` | `github.com/synadia-io/orbit.go/jetstreamext` | `internal/nats` | Atomic batch publishing for move CAS operations. `GetLastMsgsFor` for instant playfield reconstruction on startup/reconnect (fetches the last message per cell subject; chunked via `GetLastMsgsUpToSeq` above 512 subjects to stay under the server's 1024-response cap). |

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

- `internal/game` — all functions are pure and take no external dependencies. Full coverage of piece rotation (all SRS wall kicks), collision detection, line clear detection, cell serialisation, score and level calculation, gravity interval curve. `teamshrink_test.go` covers the teams shared-board shrink: `ProjectShrinkShared` overlay/crush semantics and the `AdversarialRowCount` idempotency guard.
- `internal/rng` — verify determinism: two `Sequence` instances with the same seed produce identical output. Verify seek: `Piece(N)` equals the Nth output from sequential calls.
- `internal/config` — subject builder functions (including the teams cell-subject builders) and the team board dimension helpers produce correct values.

### Integration tests (require a NATS server)

- `internal/nats` — stream creation, KV operations, atomic batch publish happy path, CAS failure path, stream sealing, `FetchPlayfieldState` via `GetLastMsgsFor`. Tests use a local NATS context pointing at the test server so that `natscontext.Connect` is exercised end-to-end rather than bypassed.
- `internal/engine` — start an engine against a real NATS server with a test game stream. Submit moves and verify the playfield reaches the expected state. Simulate CAS failure by publishing a conflicting update from a second client. Verify the `FetchPlayfieldState` snapshot correctly seeds `LastSeq` before the ordered consumer starts. Verify cooperative score deltas propagated via `EventLineClear` converge to the same local total across two engine instances. `teams_test.go` covers teams mode end-to-end: garbage is applied to the target team's shared board **exactly once** despite racing teammates, and the elimination/team-win flow (eliminated player spectates while the team plays on; whole-team elimination flips every winning-team member to `Won: true`).
- `internal/lobby` — create/join/leave game operations, presence heartbeat expiry, KV watcher delivery. `teamjoin_test.go` covers the teams join CAS loop: team capacity (`ErrTeamFull`), atomic `TeamSlot` assignment under concurrent joins, and the both-teams-full → `starting` transition. `abandoned_test.go` covers the abandonment rules (`isAbandoned` takes `now` as a parameter precisely so tests inject a future time instead of waiting out the timeouts, plus the deleted-stream case and a `checkAbandoned` end-to-end pass) and `DeleteGame`'s full teardown (stream gone, KV listing gone, game chat purged, lobby chat untouched, idempotent re-delete).
- `internal/cleanup` — seed a NATS server with stale game streams in various states and verify cleanup produces the correct outcomes, including orphaned-stream deletion (via the `StreamNames` listing) when KV entries are missing.

The `internal/testutil` package (`nats.go`) provides helpers for spinning up an **embedded** NATS server for integration tests. Tests run against that real server rather than mocking NATS behind interfaces.

### Visual snapshots (opt-in, need a GPU)

`internal/nativeui` has three snapshot suites, all skipped unless `FW_SNAPSHOT_DIR` is set: `TestPickerSnapshots` (the login context pull-down closed and open, plus LAN mode selected with its port field and shareable-URL line), `TestScreenSnapshots` (login/lobby/game screens plus a hand-built sample board — the 8-bit look verification), and `TestCaptureREADMEScreenshots` (renders the README's screenshots from a **real** 2v2 teams game running against an embedded JetStream server — four player engines plus a spectator, prefilled stacks, live gravity — at 2x resolution via a headless GPU window).

### End-to-end

Two engine instances running against a shared NATS server, simulating a competitive game. Assert that line clears on one side produce shrink events on the other, that the CAS mechanism correctly serialises simultaneous moves, and that the archive sequence runs correctly at game end (record published, then the game stream deleted — normal game end deletes the stream rather than sealing it).

---

## 19. Design Decision Log

Decisions settled during design review, recorded here for future reference.

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | Competitive playfield topology | Player-scoped cell subjects within one shared stream (`jetricks.game.<id>.player.<pid>.playfield.cell.<row>.<col>`) | One stream per game keeps lifecycle management simple. Player-scoped subjects provide full isolation within it. |
| 2 | Lock-in detection | Implicit — engine scans the playfield state for the `Active→Occupied` transition after each cell message | No extra message; lock-in is definitionally visible in the cell data that would be fetched anyway on rejoin. |
| 3 | Line-clear row shift publisher | Client whose piece caused the lock-in | Avoids a first-CAS-wins race on a large batch; the publisher has the most current local state. |
| 4 | Opponent shrink in competitive | Player A publishes shrink event; Player B's engine applies it to its own cells | Player B's engine owns its cell subjects for CAS purposes. Shrink-as-event decouples A's writes from B's CAS keys. |
| 5 | Cell payload encoding | JSON (one `Cell` document per message; empty cell → `{}`, the vacate payload) | Simpler to implement and debug with `nats` CLI. Cell update rate is low enough that JSON overhead is not a concern. |
| 6 | Startup consumer start point | `max(cell seqs)+1` | Avoids reprocessing the entire stream history on every join/reconnect. The gap in non-cell subjects (at most a few milliseconds of game time) is acceptable; the playfield snapshot reflects any shrinks or clears that occurred in that window. |
| 7 | Lobby map concurrency | `sync.RWMutex` on `Lobby.mu`, maps unexported, accessed via `Players()` / `Games()` snapshot methods | Straightforward, low-overhead, and makes the access pattern explicit without channel complexity. |
| 8 | Cooperative score propagation | Plain local score counter (`atomic.Int64`), propagated via `EventLineClear` events on the events subject and summed locally | No server-side counter CRDT is needed; the events stream the game already runs carries the deltas. The game stream sets `AllowAtomicPublish` and `AllowDirect` (not `AllowMsgCounter`). |
| 9 | Game ID format | UUID v4 with dashes (`550e8400-e29b-41d4-a716-446655440000`) | UUIDs are globally unique, collision-free, and NATS stream names allow dashes. |
| 10 | Game-over semantics | Cooperative: any top-out ends for all. Competitive: eliminated player becomes spectator; game continues until one player remains. | See `jetricks-gameplays.md`. |
| 11 | HardDrop CAS behaviour | Destination computed once; competitive publishes the landing NoCAS, coop via merge-retry (≤16). No recompute-and-retry-until-it-lands loop. | The landing is authoritative state, so NoCAS (competitive) or CAS+merge (coop, to protect the other player's shared-board cells) is the right tool — not an unbounded CAS retry. |
| 12 | Opponent display in competitive | Full live view via one ordered consumer per opponent's cell subjects | Provides the same real-time fidelity as the player's own field. The overhead of additional consumers is minimal (at most 3 opponents in a 4-player game). |
| 13 | `pieceIdx` recovery on join/reconnect | Store `PieceIdx uint64` in `GameMeta`; locking engine CAS-updates it after each lock-in | `FetchGameMeta` gives any joining engine the current piece index in one round trip. No stream replay needed. |
| 14 | Cooperative playfield topology | Single shared playfield of width `playerCount × StandardWidth`; cell subjects carry no player token (shared board) | Both players' pieces coexist on one wide board. `Cell.PlayerIdx` in the payload distinguishes active pieces — player identity lives in the message, not the subject, since coop never filters cells per player. One ordered consumer per engine. Line clears span the full width. UI renders the single playfield directly. |
| 15 | `GameMeta` payload | Fully specified in Section 4 with lifecycle, identity, RNG seed, and `PieceIdx` fields | Status uses string constants for readability in the `nats` CLI. `PieceIdx` enables fast startup without stream replay. |
| 16 | Real-time UI updates from JetStream | All UI data backed by JetStream uses ordered consumers pushing through the `Updates` channels — never polling or periodic refresh | The lobby runs consumers for KV (players/games), chat, and archives. The engine runs consumers for playfield cells, events, meta, and countdown. Any change in a JetStream stream or KV bucket is immediately pushed to the UI via the consumer → Updates channel → bridge pipeline. |
| 17 | Playfield storage granularity | One message per CELL (`playfield.cell.<row>.<col>`), not per row | A cell's last message is its current state. Per-cell CAS shrinks coop contention to same-cell writes only; every publish is a diff of only the changed cells (~4–8 messages per move); the `orderedCellKeys` category order (active → locked → empty) replaces the per-row `bottomFirst` flag with one rule that covers every write path. The CAS/write-through/merge-retry/ordered-consumer architecture is unchanged, just at cell granularity. |
| 18 | Teams playfield topology | Two team-scoped shared boards (`jetricks.game.<id>.team.<t>.playfield.cell.<row>.<col>`), each the cooperative scheme at team scale | Within a team, teams mode IS cooperative — the coop shared-board machinery (`CanPlaceCoop`, merge-retry, `Cell.PlayerIdx` ownership) is reused verbatim via `sharedBoard()`. The team token in the subject keeps the two boards disjoint, so cross-team writes are impossible by construction; no roster consumer is needed (the roster is fixed pre-start). |
| 19 | Shrink on a shared team board | `ProjectShrinkShared`: NO piece is lifted — every active piece is overlaid at its current position; a piece overtaken by the risen stack is "crushed" (locks where it is); shrink never tops a player out (top-out happens at spawn time). Application is CAS-guarded and idempotent via the `expectedGarbage` − `AdversarialRowCount()` deficit | Any of several teammates may win the race to apply a shrink, and lifting would relocate other players' mid-flight pieces from a possibly-stale snapshot. Holding every piece in place keeps the transform pure and symmetric; the monotonic garbage-row count makes the racing applications converge to exactly one committed shift (a stale shift would double-shift the stack, so CAS failures recompute from fresh state rather than blind merge-retry). |
| 20 | Teams game-over semantics | A topped-out player vacates their piece and spectates while their team plays on; a team loses when ALL members topped out; every member of the other team (eliminated included) wins. Decided once per engine (`teamOutcomeDone`) off the ordered events subject | Per-player elimination keeps the shared board live for the teammates; the ordered events stream guarantees every engine reaches the same verdict without coordination. See `jetricks-gameplays.md`. |
| 23 | Roster overfill / stale invitations | `JoinGame` caps the overall roster (`ErrGameFull`) in its CAS loop for all modes; an agent whose invited join fails declines the invitation instead of retrying | The per-team teams cap left competitive/coop uncapped, so a race (or a mis-gated UI) could seat a 5th player in a 4-player game. An invited agent that couldn't be seated (team over-subscribed by the creator) otherwise re-accepted the same invitation in a tight loop; declining on failure breaks it. |
| 22 | Game invitations | Written to the invitee's PER-GAME KV mailbox key `invites.<invitee>.<gameID>` (several at once, 2-min TTL); the key's lifecycle is the state machine (delete = accept/retract, rewrite `declined: true` = decline, kept for the inviter to see); `JoinGame` guards invite-only games inside its CAS loop (creator or invitation holder only, invitation exempts from `MaxAgents`); agents auto-accept | Reuses the lobby KV and its existing whole-bucket watcher — no new stream; the invitation is both the routing (which game/team) and the authorization (the creator's explicit choice), so it cleanly overrides the open agent policy. One key per (invitee, game) supports concurrent invitations from several games and gives the inviter a live per-invitee status view (`SentInvites`) from the same watch. |
| 24 | Lobby events | Every lobby action (game created/joined/left, invite sent/retracted/declined) is also published as a transient CORE NATS `LobbyEvent` on `jetricks.lobby.event.<kind>`; every lobby subscribes and turns foreign events into immediate refresh pings | State stays in the KV (single source of truth); the events are pure low-latency signals — core NATS is enough, deliberately captured by no stream (nothing to replay, nothing to clean up). Closes the presence-heartbeat latency gap for "who is invitable right now" and gives external agents a push channel without polling. |
| 25 | Live invite picker & self-seat | The picker sends/retracts invitations the moment a selection changes (no send button) and pins the creator as a pre-selected first row whose selection IS a roster seat (`JoinGame` on open, `UnjoinGame` on deselect); when the roster fills the picker hands the creator to `joinGame` (kept seat) or `spectateGame` (opted out) | Selection-as-action removes a whole failure mode (configured-but-never-sent invites) and makes the picker double as the live status board; "creating an invitation game implies accepting your own invitation" collapses the creator's join into the same selection model. Capacity is guarded at click time from roster+pending usage, so over-invites are refused rather than bounced later at the door. |
| 26 | Leave/rejoin keeps the seat | "Back to Lobby" clears the READY mark (`SetReady(false)`) but keeps the roster seat while the game is alive; the lobby row reads **joined**/**playing** with a Rejoin button (`JoinGame`'s already-seated branch returns the same position); leaving an in-progress game asks for confirmation; presence stays In Game while a live seat is held | A seat is a commitment to the other players — silently freeing it on a screen change would strand games; keeping it makes leave/rejoin a pure view change (the stream replays the live board on rejoin). Ready must NOT survive the exit, though: an absent "ready" player would let the countdown fire without them. |
| 21 | Shared-board spawn blocked by another player's ACTIVE piece | DEFER the spawn (`spawnPending`) and retry it from `runInput`'s gravity tick (`retrySpawnIfPending`) — top out only when the spawn cells hold LOCKED cells (`CanPlaceCoop` fails AND `CanPlace` fails) | Mirrors the locked-vs-active distinction gravity/hard-drop already make; a teammate's piece merely crossing the spawn area must not eliminate a player (in teams permanently — the "one piece per team board" bug — and in coop it would end the game for everyone). The gravity ticker is the retry heartbeat: no new goroutine, the single-write-goroutine invariant holds, and the cadence matches how fast the blocker can move. Known deferred edge: a *disconnected* player's abandoned mid-air piece blocks indefinitely — a pre-existing engine-wide gap (it equally blocks movement/locks today). |
| 22 | Piece-less watchdog + no-regress meta transitions | `retrySpawnIfPending` force-spawns after 2 piece-less gravity ticks (gated on `gameStarted`); `lobby.transitionGameStatus` refuses to overwrite finished/archived/cancelled | The lock-in edge detector needs an incoming message to fire — a dropped spawn publish on a since-silent shared board (last teammate eliminated) stalls a player forever without the watchdog. And the countdown's final `StartGame` is a detached goroutine racing the game itself: a fast game (agents) can FINISH before that write lands, and an unguarded in_progress stamp over finished resurrects the game and strands it unarchivable. |
| 23 | Archive verdicts (winner / winning team) | Taken from the archiving ENGINE's live record (`IsEliminated` set, new `GameOutcome()` accessor), never from replaying the events subject | The game stream is `MaxMsgsPerSubject: 1` and all events share ONE subject, so a post-game replay sees only the LAST event — elimination history cannot be reconstructed (a who-ever-sent-an-event set also mis-scores near-simultaneous final top-outs as a draw). The archiver lived through the game: in competitive it knows every elimination; in teams it is by construction on the winning side (or a draw participant), so its own verdict IS the team verdict. |

---

## 20. Release Pipeline

**File:** `.github/workflows/release.yml`

Pushing a git tag matching `v*` (e.g. `v0.1.0`) triggers a GitHub Actions workflow that runs the test suite, builds the `jetricks` binary for every supported platform, and publishes a GitHub release containing one archive per platform plus a `SHA256SUMS` checksum file. Release notes are auto-generated from the commits since the previous tag.

### Supported platforms

| Platform | Runner | cgo | Archive |
|----------|--------|-----|---------|
| linux/amd64 | `ubuntu-latest` | yes | `.tar.gz` |
| linux/arm64 | `ubuntu-24.04-arm` | yes | `.tar.gz` |
| darwin/arm64 | `macos-latest` | yes | `.tar.gz` |
| darwin/amd64 | `macos-latest` | yes | `.tar.gz` |
| windows/amd64 | `windows-latest` | no | `.zip` |
| windows/arm64 | `windows-latest` | no | `.zip` |

### Why native runners per OS

Gio is not cross-compilable from a single host: on Linux it uses cgo against the X11/Wayland/EGL development headers (installed via `apt-get` in the workflow), and on macOS it uses cgo against the Apple frameworks. Gio must be v0.8.0+ — v0.7.x's `gioui.org/cpu` dependency does not compile on Linux under Go 1.24+. Windows is the exception — Gio's Windows backend is pure Go (win32 syscalls), so both Windows architectures build with `CGO_ENABLED=0`. Both macOS architectures build on the one macOS runner since the Apple SDK is multi-arch. linux/arm64 builds natively on `ubuntu-24.04-arm`; note that GitHub's free arm64 Linux runners are available to public repositories only, so the repo must be public for that matrix entry to run.

### Versioning

Binaries are built with `-ldflags "-s -w -X main.version=<tag>"`, which stamps the tag into `main.version` (default `dev` for local builds) — reported by the `--version` flag.

---

## 21. Agents: the `mk1` reference and the `agents/` home

**The agent model.** An agent is a standalone program that plays Jetricks by speaking the
game's NATS/JetStream protocol — there is no plugin interface or shared SDK to implement.
The single, language-neutral contract is the wire protocol plus the fair-play rules in
`jetricks-agent-guide.md` (with the game rules in `jetricks-gameplays.md`); a conformant
agent can be written in any language depending on nothing in this repo. Contributed agents
live in `agents/<name>/`, each self-contained (own language/build/deps, its own README);
`agents/README.md` is the submission guide. The game neither knows nor cares how any agent
is built. The first entry is `agents/example-python/` — a minimal single-file Python agent
(`example-py`, competitive mode) that implements the entire protocol from the guide with no
repo dependency: lobby KV CAS join/ready/countdown, a bit-exact port of the piece RNG
(Go `math/rand/v2` PCG + 7-bag), its own engine (spawn/gravity/lock/clears/garbage/top-out),
atomic CAS cell batches with write-through, shrink/game-over events, CAS-failure flashes,
and the finish→archive→cleanup sequence when it wins (its ArchiveRecord is byte-compatible
with the Go structs). `agent.py --selftest` runs offline conformance checks (RNG parity
fixtures generated from `internal/rng`).

**The reference agent `mk1`.** The repository ships one Go agent — `mk1`, source in
`internal/agent`, binary `cmd/jetricks-agent`. It is a *privileged* example: because it
lives in the repo it reuses the game's own Go engine (`engine`, `lobby`, `game`, `rng`,
`nats`, `config`, `archive`, `cleanup`) instead of re-implementing the protocol, so it
builds without cgo/Gio on every platform and its player name reads `mk1-<instance>-<difficulty>`.
It uses only the exported engine/lobby API (the six move methods and the state accessors),
never engine internals or direct cell publishes — the same discipline a wire-level agent
follows. Everything below describes that reference implementation.

### internal/agent files

| File | Contents |
|------|----------|
| `difficulty.go` | `Difficulty` (easy/medium/hard), `ParseDifficulty`, and `Tuning` — the knob set (per-piece think pause, per-move pause, blunder rate/depth, executor timeouts). No lookahead knob: agents only use UI-visible information, and the UI has no next-piece preview. Each difficulty maps to a `Tuning`; tests override knobs directly. |
| `eval.go` | Pure board evaluation: Dellacherie's six features (landing height, eroded cells, row transitions, column transitions, holes, cumulative wells) with the El-Tetris weights. Cells count as filled iff `Occupied && !Active`, which prices adversarial garbage in automatically. |
| `planner.go` / `rules.go` | Pure placement search over `game.Playfield` copies. `Rules{Shared, PlayerIdx, SectionIdx}` selects the board's collision variant — private boards use `CanPlace`/`Rotate`/`HardDropDestination`; shared (coop/teams) boards use the `*Coop` variants, where other players' mid-flight pieces block. `PlanPlacements` enumerates every placement reachable with the executor's move vocabulary by simulating the exact script (SRS rotations with kicks, one-column collision-gated slides, hard drop), simulates the lock/clear (`Row.IsFull` keeps garbage rows uncompletable), scores with `eval.go`, and returns placements best-first — current piece only, per the fair-visibility contract (no seed-derived lookahead). `ChoosePlacement` applies the blunder model. |
| `executor.go` | `Mover` — the slice of `*engine.Engine` the executor needs (six moves + `Playfield`, `PieceIdx`, `PlayerIdx`, `Mode`; the engine satisfies it, tests use a synchronous fake). `Execute` runs a sense–act loop: dispatch exactly one move toward the target, poll the committed playfield for its effect, repeat, hard drop. Errors are typed: `ErrStalled` (move never took effect), `ErrBoardChanged` (garbage landed mid-plan, detected via `AdversarialRowCount`), `ErrGameOver`. |
| `agent.go` | `Run(ctx, Config) (Result, error)` — setup (Bootstrap → name-collision check → lobby bring-up with `SetAgent(true)`, `WaitForInitialLoad`, best-effort `cleanup.Run`), then a game loop around `playOne`. The default is **resident**: wait in the lobby → play → back to the lobby, until ctx is cancelled — joining only games this agent is INVITED to unless `Config.AutoJoin` also enables scanning for open agent-allowed games (`Config.Once` restores one-shot; `--join`/`--create` are always one-shot). Stale invitations (game gone) are dropped via `DismissInvite`; a failed invited join is answered with `DeclineInvite` (the inviter sees the refusal). `playOne` mirrors `nativeui/lifecycle.go` for a single game of ANY mode: joinability guard (created/starting, free seat — a free team seat in teams — free **agent seat** per the game's `MaxAgents` policy) → `JoinGame` (teams: least-populated team, one retry on a lost `ErrTeamFull` race) → engine construction with the listing's mode/team/slot and `OnGameFinished = ArchiveAndCleanup` → `ToggleReady` (running the 5..0 countdown + `StartGame` when its toggle completes the ready set) → per-piece play loop under the mode's `Rules` → per-mode outcome: competitive polls `IsEliminated`; cooperative has no winner (shared score, `OVER`); teams waits for the team verdict (authoritative Won update, with a roster-eliminations poll as the lossy-channel fallback) → `waitArchived` (archiveDone when OUR engine archives — gated by `archiveStarted`, since `ArchiveAndCleanup` flips the meta to archived before the record publish — else meta/stream) or grace/linger (competitive loser) → teardown. A joined game that never starts within `WaitTimeout` is abandoned via `lobby.UnjoinGame` (frees the seat) before the agent rescans. `Result` carries per-run `Games`/`Wins` totals alongside the last game's stats. |

### Poll-not-events rule

`engine.Updates` is a bounded, lossy channel (drops when full), so the agent treats it as
a logging/wake-up hint only. Everything authoritative is polled from race-free
accessors: game over is `Mode() != ModePlayer`, win/loss prefers the pump-captured
`UpdateGameOver.Won` and falls back to `!IsEliminated(name)`, lock detection is
`PieceIdx()` advancing, and mid-plan garbage is `AdversarialRowCount()` growing. The
agent never reads the game seed: the piece sequence is deterministic, but the UI
shows humans no next-piece preview, so seed-derived lookahead would violate the
fair-visibility contract (agents decide only on what a human can see —
`jetricks-agent-guide.md`).

### cmd/jetricks-agent flags

| Flag | Meaning |
|------|---------|
| `--server` / `--context` / `--user` / `--password` | Connection choice, same semantics as `cmd/jetricks` (URL wins over context) |
| `--name` | The agent VERSION stem (default: `mk1`, the reference agent's `agent.Codename`, bumped when the agent's play logic changes). `Run` composes the full player name `<stem>-<instance>-<difficulty>` (`composeName` in agent.go) with a fresh 4-hex instance id per connection; every component sticks to the presence-KV charset and the whole passes `config.ValidatePlayerName` |
| `--difficulty` | `easy` \| `medium` \| `hard` (default `hard`) |
| `--join <gameID>` | Join a specific game (still subject to that game's agent policy) |
| `--create` + `--mode` + `--players N` + `--max-agents M` | Create a game (cooperative/competitive/teams; `--players` is per team in teams mode) and wait for opponents; `M` agent seats including this agent (0/default = all seats — an agent-hosted game is agent-friendly) |
| *(neither)* | **Resident mode**: wait in the lobby and play invited games, game after game, until interrupted |
| `--auto-join` | Residents also actively join the oldest open game of any mode that allows agents (default: invited games only) |
| `--once` | Exit after one game instead of staying resident |
| `--wait` | Max wait for a joined game to fill and start (default 10m; one-shot discovery too — resident discovery waits indefinitely) |
| `--linger` | After losing, stay connected until the game finishes |
| `--seed` | Blunder RNG seed for reproducible easy/medium play |
| `--version` | Print version and exit |

SIGINT/SIGTERM cancel the run context; the agent leaves the game, stops the lobby
(deleting its presence), and drains the connection on the way out. Exit status 0 covers
both winning and losing; non-zero means a setup or runtime error.

### Testing

`planner_test.go`/`eval_test.go` cover the pure search (placement enumeration counts,
script replay, clear-beats-hole ordering, garbage-row invariants, blunder
distribution). `executor_test.go` drives `Execute` against a
synchronous fake `Mover`. `bot_integration_test.go` plays a full agent-vs-agent game on an
embedded server (strong creator vs. rigged-bad auto-joiner, zero delays) and asserts
exactly one winner, the archive record, and stream/KV cleanup.
`resident_integration_test.go` exercises the resident lifecycle: two resident agents
appear (flagged) in the lobby presence list, skip a no-agents game, fill and play two
consecutive agent-allowed games created by a "human" lobby client, and exit cleanly on
interrupt reporting two games each. `modes_integration_test.go` plays a full
cooperative game (shared board, no winner, topper archives) and a 1v1 teams game
(auto team selection, garbage between team boards, exactly one winning team) end to
end.

### Agent policy (who may join)

The creator's agent policy lives on the lobby listing: `GameListing.MaxAgents` (0 = agents
not allowed) with `AgentCount()` counting `PlayerSummary.Agent` roster seats.
`Lobby.SetAgent(true)` marks a peer as an agent (stamped on presence and roster entries);
`JoinGame` enforces the policy inside its CAS loop (`ErrAgentsNotAllowed`,
`ErrAgentSlotsFull`), so concurrent agent joins can never exceed `MaxAgents`.
`Lobby.UnjoinGame` is the pre-start inverse of `JoinGame`: a CAS loop that removes
the caller from the roster (reverting `starting`→`created` when the roster is no
longer full), guarded by the game META's status (the listing never reads
`in_progress`), and purges the caller's roster announcement via
`nats.PurgeRosterEntry` so late joiners don't discover a ghost opponent. The GUI's
create row exposes the policy for every game mode ("Allow agents" checkbox +
max-agents editor, `nativeui/lobby.go:createRow`), tags agent players `[agent]` throughout
(lobby player list via `PlayerPresence.Agent`, game rows, ready roster, legend), and
shows `agents k/N` on agent-allowed game rows. Relatedly, `cleanup.Run` applies a
one-minute `creationGracePeriod` before treating a game as orphaned/creator-absent:
creation is several separate writes and agent-allowed games legitimately sit at zero
players until a resident agent's scan picks them up — without the grace, a peer (or
agent) logging in mid-creation could cancel a brand-new game.
