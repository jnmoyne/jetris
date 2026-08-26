# Jetris — Go Project Structure

**Version:** 0.1 Draft
**Status:** Design Phase
**Date:** March 2026

> **Gameplay reference:** All gameplay mechanics (cooperative/competitive/teams modes, scoring, gravity, line clears, game lifecycle) are defined in [`jetris-gameplays.md`](jetris-gameplays.md). This spec defers to that document for gameplay behavior and focuses on architecture, package structure, and implementation details.

---

## Table of Contents

1. [Repository Layout](#1-repository-layout)
2. [Package Dependency Graph](#2-package-dependency-graph)
3. [cmd/jetris](#3-cmdjetris)
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
21. [Agents: the golang-mk1 reference and the agents/ home](#21-agents-the-golang-mk1-reference-and-the-agents-home)

---

## 1. Repository Layout

```
jetris/
├── .github/
│   └── workflows/
│       └── release.yml
├── cmd/
│   └── jetris/
│       └── main.go
├── agents/
│   ├── example-python/              ← minimal Python agent (no repo dependency)
│   └── golang-mk1/                  ← the Go reference agent: its own module (own go.mod,
│                                      deps: nats.go + orbit natscontext/jetstreamext), NOT part of go build ./...
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
│   │   ├── lockdelay.go
│   │   ├── consumer.go
│   │   ├── registers.go
│   │   ├── ledger.go
│   │   ├── transform.go
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
│   ├── prefs/
│   │   └── favorites.go
│   ├── nativeui/
│   │   ├── app.go
│   │   ├── archive_view.go
│   │   ├── backdrop.go
│   │   ├── login-backdrop.jpg
│   │   ├── board.go
│   │   ├── brand.go
│   │   ├── bridge.go
│   │   ├── fonts.go
│   │   ├── game.go
│   │   ├── input.go
│   │   ├── lifecycle.go
│   │   ├── lobby.go
│   │   ├── login.go
│   │   ├── natslog.go
│   │   └── version.go
│   └── testutil/
│       └── nats.go
├── scripts/
│   └── cleanup.sh
├── jetris-agent-guide.md
├── go.mod
└── go.sum
```

---

## 2. Package Dependency Graph

Arrows indicate "depends on". The rule is that `internal/game`, `internal/rng`, and `internal/config` are leaves — they have no internal dependencies. The front-end layer (`internal/nativeui`) depends on engine and lobby but neither engine nor lobby depends on the front end. All packages may depend on config.

```
cmd/jetris
    ├── internal/config
    ├── internal/nats              ← uses: orbit.go/natscontext, orbit.go/jetstreamext
    ├── internal/rng
    ├── internal/game
    ├── internal/engine            ← depends on: nats, game, rng, config
    ├── internal/lobby             ← depends on: nats, config
    ├── internal/cleanup           ← depends on: nats, lobby, config
    ├── internal/archive           ← depends on: nats, engine, game, lobby, config
    ├── internal/render            ← depends on: game (cell/board appearance)
    ├── internal/prefs             ← local preferences (server-browser favorites); no internal deps
    ├── internal/update            ← startup check for a newer GitHub release; no internal deps
    └── internal/nativeui          ← depends on: engine, lobby, render, prefs, config (the front end)

agents/golang-mk1                  ← separate module: depends only on nats.go + orbit
                                     natscontext/jetstreamext (the headless reference player; speaks
                                     the wire protocol, uses no internal/ packages)

Leaf packages (no internal deps):
    internal/config
    internal/game
    internal/rng
    internal/prefs
    internal/update

orbit.go modules used:
    orbit.go/natscontext   → internal/nats     (connection via NATS CLI contexts)
    orbit.go/jetstreamext  → internal/nats     (atomic batch publish, GetLastMsgsFor)
```

---

## 3. cmd/jetris

**File:** `cmd/jetris/main.go`

The entrypoint. Responsible only for wiring — it constructs all top-level components, injects dependencies, and starts the application. Contains no business logic.

### Responsibilities

- Parse CLI flags into a `config.Config`
- Open the window immediately — **no NATS connection is made at startup**. `natspkg.ListContexts()` enumerates the NATS CLI contexts, `prefs.LoadFavorites()` reads the server browser's bookmarks (warn-only on error, defaults on a fresh install), and `nativeui.NewWithPicker(cfg, names, selected, favorites)` builds the App; there is a single combined login screen (name entry + the two-tab connection page + Play) and the App dials NATS itself when the player hits Play. `--server`/`--context` never connect directly — they only seed the browser's selection. The App owns the connection; `main`'s shutdown paths call `a.DrainConn()` (nil-safe).
- Run the Gio front end (`internal/nativeui`): `runNative(ctx, cancel, a)` opens a native OS window. Gio's `app.Main()` owns the OS main thread, so the app logic runs on a goroutine that calls `App.Run`.
- Block on OS signal / window close and perform graceful shutdown

The player enters a name on the same screen; identity is NATS-backed presence. Lobby creation is deferred until the player connects and enters their name.

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--context` | `""` | NATS context (as configured with `nats context add`) to preselect in the login screen's server browser. |
| `--server` / `--user` / `--password` | `""` | NATS URL + credentials: `--server` preselects that URL in the server browser (beating `--context`; listed under a COMMAND LINE section unless it is already a favorite); user/password apply to URL connects. |
| `--version` | `false` | Print the version and exit. The `main.version` variable defaults to `dev`, is overridden at release time via `-ldflags "-X main.version=<tag>"` (see [Section 20 — Release Pipeline](#20-release-pipeline)), and is passed to `nativeui.SetVersion` so the same string shows on the UI's top-right version plate. |
| `--no-update-check` | `false` | Skip the startup lookup of the latest GitHub release. Otherwise `checkForUpdate` runs on its own goroutine (10 s cap, `updateCheckTimeout`): `update.Check(ctx, version)` (`internal/update`) fetches `api.github.com/repos/jnmoyne/jetris/releases/latest` and compares its `tag_name` with the stamped version (`Newer`: major.minor.patch, a final release beating its pre-release; anything that isn't a version — `dev` — compares as nothing, and a `dev` build never even asks). A newer release is logged and handed to `App.NotifyUpdate(tag, url)`: the VER plate turns gold and reads `VER <this> · <new> AVAILABLE` on every screen, and the login screen shows `▲ UPDATE AVAILABLE · JETRIS <new>` with the release page's URL (`updateNotice`). A failed lookup (offline, rate-limited) is logged and otherwise ignored; nothing is ever downloaded or installed. |

Connecting via a context ultimately maps to `natscontext.Connect(contextName)` from `orbit.go/natscontext`. This means Jetris shares the same connection configuration — server URL, credentials, TLS certificates, JetStream domain — as the `nats` CLI tool on the same machine. No separate connection config file or credential management is needed. Operators configure contexts once with `nats context add` and both the CLI and Jetris use them.

The login screen's **CONNECT TO** page has two tabs. **NATS server browser** is a file-picker-style tree of collapsible sections: **FAVORITES** — the player's bookmarks, persisted by `internal/prefs` (`<XDG_CONFIG_HOME|~/.config>/jetris/favorites.json`) and pre-populated with `Demo.nats.io (US central)` → `nats://demo.nats.io:4222`, `Jetris (EU central)` → `nats://172.105.76.148:4222` and `Jetris (AP south)` → `nats://172.104.52.4:4222` on a fresh install, each row deletable (✕) and a trailing "+ Add a NATS URL…" row that expands an inline label + URL form; **CONTEXTS** — the machine's NATS CLI contexts (the CLI's current one labeled "(nats CLI current)" — deliberately not "(selected)", which would read as the browser's own selection — each showing its context file's `url` when known via `nats.ContextURL`), or a hint when there are none; and **COMMAND LINE**, present only while `--server` names a URL that isn't a favorite. A row is selected by clicking it, and the same click probes that server (a click while another probe is running is parked and fired once the slot frees, if the row is still selected); the selected row is a solid accent band with dark type (the tab chips' "active" treatment, unmistakable among the plain rows) and carries a ↻ chip (the pad's blocky rotate glyph) that probes it again on demand — a real dial, the **Core NATS ping** (a publish → inbox-subscription round trip), and a look at the server's lobby KV bucket to count the players currently connected there — and reports `✓ <server> · Core NATS ping <rtt> · <n> players online` (or `no lobby yet` / `nobody online`) on the line under the list, with a compact `<rtt> · <n> online` / `OFFLINE` readout inline on the row; the probe connection is closed again and provisions nothing. Between the list and that probe line a **SELECTED** line (an accent chip, then the row's name and URL — a context as `context <name>`, the `--server` row as its URL) names the row Play will use even when it is scrolled out of the list or its section is collapsed; with nothing selected it reads NOTHING SELECTED in the warning color. The panel keeps one fixed height (`connPanelH`) whichever tab is up, so switching tabs never moves the Play button. **LAN party mode (embedded NATS server)** has an "IP:" field (pre-filled with the auto-detected LAN address) and "Port:" field (pre-filled with `config.DefaultEmbeddedPort` = 4222, digits-only) — Play on this tab starts an in-process JetStream-enabled `nats-server` (default account, no auth, the entered port on all interfaces, storage in `./jetstream-data`) and connects to it via the entered address. The IP is editable because it is only auto-DETECTED: on a multi-homed/VPN'd/containerised machine the detected interface may not be the one other players can reach. It changes only what is advertised and dialed, never the bind, so any address that actually reaches the machine works; an empty field re-detects at connect time. The tab shows `Your server's URL is nats://<ip>:<port>` (built from those fields, falling back to `App.lanIP` and the default port mid-edit) — the address friends add to their own favorites — plus the `data in ./jetstream-data` hint, and the lobby displays the address again once connected. Its **Check embedded server** button starts (or reuses) the in-process server first, then connects and pings over its LAN address, applying the same server-identity check as Play (`✗ another NATS server is already using port <n>…`). The default selection precedence is `--server` → `--context` (appended to the list if the lister didn't find it) → the first favorite → the CLI's current context → the first context. The name entry sits above the page and a large attract-mode **Play** button below it; behind the card the embedded login artwork (`backdrop.go`, `login-backdrop.jpg`) — the neon Team A / Team B boards over the NATS logo — fills the window.

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
    EmbeddedHost string // address it is advertised/dialed on ("" = auto-detected LAN IP); it always LISTENS on every interface
    EmbeddedPort int    // port for the embedded server (0 = DefaultEmbeddedPort)
}

// Embedded-server settings for the login screen's "LAN party mode (embedded NATS
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

    LobbyKVBucket          = "JETRIS_LOBBY"
    ChatStream             = "JETRIS_CHAT"
    LobbyChatGameID        = "lobby"                // reserved chat "game ID" for the lobby chat
    LobbyChatSubject       = "jetris.chat.lobby"  // = GameChatSubject(LobbyChatGameID)
    ArchiveStream          = "JETRIS_ARCHIVE"
    ArchiveSubject         = "jetris.archive"
    ChatMaxAge             = 7 * 24 * time.Hour
    PresenceHeartbeat      = 30 * time.Second
    PresenceTTL            = 5 * time.Minute  // per-key TTL on presence entries (KV LimitMarkerTTL)
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

// ArchiveRecord is the JSON payload published to the JETRIS_ARCHIVE stream
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
    Chat        []ChatLine     `json:"chat,omitempty"`        // the game's chat history (last ArchiveChatCap = 200 lines), copied before PurgeGameChat
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

Game IDs are UUID v4 strings with dashes (e.g. `550e8400-e29b-41d4-a716-446655440000`). NATS stream names allow alphanumeric characters plus dashes and underscores, so `JETRIS_GAME_550e8400-e29b-41d4-a716-446655440000` is a valid stream name. UUIDs are generated by the game creator's client using `github.com/google/uuid`.

### Subject Builders

```go
func GameStream(gameID string) string        // → "JETRIS_GAME_<id>"
func GameSubjectFilter(gameID string) string // → "jetris.game.<id>.>"

// Replay archive — a top-ranked finished game's stream is copied into the ONE
// shared file-backed replay stream before deletion, each message republished
// under the game-scoped prefix (game ID right after "jetris.replay."), so one
// subject filter replays a game and a Purge with the same filter deletes a
// displaced one. Copies carry their ORIGINAL stream timestamp in the
// ReplayTsHeader header (republishing stamps copy-time timestamps); the copy
// ends with a marker message whose presence makes the replay complete and
// listable. A replay is KEPT while its game is in the keep set —
// ReplayKeepSet(recs) = ReplayTopRanked(recs) ∪ ReplayRecent(recs): the top
// config.ReplayTopN (10) of its (mode, with/without agents) bucket by
// RankBefore, or the config.ReplayRecentN (25) most recent finishes overall by
// RecentBefore — a pure function of the archive records, so every archiver
// (GUI or agent) cuts the identical set; the lobby highlights the
// ReplayTopRanked games as TOP 10.
const ReplayStream = "JETRIS_REPLAY"
const ReplaySubjectFilter = "jetris.replay.>"  // stream config
const ReplayMarkerFilter = "jetris.replay.*.done" // listing (one marker per game)
const ReplayTsHeader = "Jetris-Ts"             // original timestamp, int nanoseconds
func ReplayFilter(gameID string) string        // → "jetris.replay.<id>.>" (consume + purge)
func ReplayMarkerSubject(gameID string) string // → "jetris.replay.<id>.done"
func ReplayCopySubject(gameID, gameSubject string) string // jetris.game.<id>.<tail> → jetris.replay.<id>.<tail>
func GameIDFromReplayMarker(subject string) string

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
//   → jetris.game.<id>.playfield.cell.<row>.<col>
func CoopCellSubjectFilter(gameID string) string
//   → jetris.game.<id>.playfield.cell.>

// Competitive — each player owns a private playfield scoped by their UUID.
func CompetitiveCellSubject(gameID string, playerID string, row, col int) string
//   → jetris.game.<id>.player.<pid>.playfield.cell.<row>.<col>
func CompetitiveCellSubjectFilter(gameID string, playerID string) string
//   → jetris.game.<id>.player.<pid>.playfield.cell.>

// Teams — two shared boards (one per team), each TeamBoardWidth(teamSize) wide.
// Like the cooperative scheme the subject carries NO player token — all
// teammates publish to / consume from the same subjects and per-cell ownership
// lives in the payload (Cell.PlayerIdx, the GLOBAL roster index) — but the
// board is scoped by team index so the two teams' boards are disjoint.
func TeamCellSubject(gameID string, team, row, col int) string
//   → jetris.game.<id>.team.<t>.playfield.cell.<row>.<col>
func TeamCellSubjectFilter(gameID string, team int) string
//   → jetris.game.<id>.team.<t>.playfield.cell.>

// Per-board REGISTERS — every competitive/team board carries two single-subject
// registers under its playfield prefix, so board consumers and snapshot fetches
// cover cells AND registers with one widened `…playfield.>` filter. The GARBAGE
// register holds the cumulative garbage rows OWED to the board since game start
// (advanced by ATTACKERS with a per-subject-CAS read-add-publish, so
// simultaneous attacks serialize and converge to the exact sum). The TXN
// register holds the cumulative rows APPLIED and is the exactly-once gate for
// every bulk board transform (it rides FIRST in the transform's atomic batch
// with a per-subject CAS expectation); the board's deficit is
// garbage.Total − txn.Applied. Cooperative boards have neither — coop has no
// garbage, and its clears keep the merge-retry path.
func CompetitiveGarbageSubject(gameID, playerID string) string
//   → jetris.game.<id>.player.<pid>.playfield.garbage
func CompetitiveTxnSubject(gameID, playerID string) string
//   → jetris.game.<id>.player.<pid>.playfield.txn
func CompetitivePlayfieldFilter(gameID, playerID string) string
//   → jetris.game.<id>.player.<pid>.playfield.>     (cells + both registers)
func TeamGarbageSubject(gameID string, team int) string
//   → jetris.game.<id>.team.<t>.playfield.garbage
func TeamTxnSubject(gameID string, team int) string
//   → jetris.game.<id>.team.<t>.playfield.txn
func TeamPlayfieldFilter(gameID string, team int) string
//   → jetris.game.<id>.team.<t>.playfield.>         (cells + both registers)

// Register detection — suffix tests on a delivered subject; consumers branch on
// these BEFORE the cell parse (registers carry no cell payload).
func IsGarbageSubject(subject string) bool
func IsTxnSubject(subject string) bool

func MetaSubject(gameID string) string
func RosterSubject(gameID string, playerID string) string

// Game events are published to PER-KIND, PER-PLAYER subjects, so no event can
// ever overwrite an unrelated one, and the payloads stay correct under any
// retention or replay: line_clear carries the sender's CUMULATIVE totals (a
// newer total subsumes any older one, and a full-history replay — e.g. a
// spectator joining mid-game — folds to the same numbers) and each player
// publishes at most one game_over. Stream order across subjects is total, so
// every engine sees the same verdict order.
func EventKindSubject(gameID, kind, playerID string) string
//   → jetris.game.<id>.events.<kind>.<pid>
func EventsSubjectFilter(gameID string) string
//   → jetris.game.<id>.events.>                     (all kinds, all senders)

func CountdownSubject(gameID string) string

// Lobby chat and per-game chat share the SAME stream (ChatStream),
// distinguished purely by the game-ID token of the subject: a game's messages
// on GameChatSubject ("jetris.chat.<id>"), lobby messages under the
// reserved game ID LobbyChatGameID ("jetris.chat.lobby"). Game chat cannot
// live on the game stream because game streams keep only the latest message
// per subject.
func GameChatSubject(gameID string) string
const ChatSubjectFilter = "jetris.chat.*"       // stream config (lobby + games)
func GameIDFromChatSubject(subject string) string // "" = lobby

func LobbyPlayerKey(playerID string) string
func LobbyGameKey(gameID string) string

// The archive subject is the ArchiveSubject const ("jetris.archive") — there
// is no builder function for it.
```

All subject and stream names in the application are produced exclusively through these builders. No package constructs subject strings by hand.

### Stream Configuration Notes

`JETRIS_GAME_<id>` is created with `MemoryStorage`, `LimitsPolicy` retention, and two stream-level flags:
- `AllowAtomicPublish: true` — required for jetstreamext atomic batch move publishing
- `AllowDirect: true` — enables direct get / `GetLastMsgsFor` for fast playfield reconstruction and per-subject refetch

The stream uses **memory storage** (game streams are ephemeral and deleted at game end, so there is no need to persist them to disk) and retains the **full game history** — no per-subject cap. Full retention gives two guarantees: an ordered consumer delivers *every* write in order (a lagging viewer can never miss a cell's vacate because a later write to the same subject trimmed it, which used to leave stale piece cells on replicas), and a spectator joining mid-game can replay the whole game from the start before catching up to live play. Current state is still read as the last message per subject via direct get. Both flags are set unconditionally on every game stream regardless of mode. No `MaxAge` is set (game streams are deleted at game end), and `AllowMsgCounter` is **not** set — the cooperative score is a plain local counter propagated via events, not a server-side counter CRDT.

Every payload on the stream is additionally designed so that only its latest value matters. The board **registers** are cumulative monotonic totals, so a newer value subsumes every older one and a late joiner recovers everything owed/applied from the snapshot fetch. **Events** are scoped per kind and per sender (`EventKindSubject`): `line_clear` carries the sender's cumulative totals (receivers fold deltas, so a full-history replay converges to the same numbers), and each player's single `game_over` lives alone on its own subject — which is what lets the archiver replay every player's final event post-game.

### GameMeta Struct

`GameMeta` is the JSON payload published to `jetris.game.<id>.meta`. All lifecycle transitions are CAS updates to this subject.

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

    // Piece preview: how many upcoming pieces the game reveals
    // (0..config.MaxNextCount). One horizon for everyone — the UI's NEXT
    // panel and an agent's lookahead allowance alike. Deliberately NOT
    // omitempty: 0 ("show none") is a meaningful value, and metas written
    // before the field existed unmarshal to 0, preserving their behavior.
    NextCount   int        `json:"next_count"`

    // Garbage holes: how many empty cells every garbage row a raise lands is
    // punched with (0..config.MaxGarbageHoles; competitive/teams). 0 — the
    // zero value, and every meta written before the field — raises solid
    // rows that never clear; with holes, a garbage row clears like any other
    // line once its holes are filled (Row.IsFull). One raise = one random
    // column set on every row it lands, unless RandomGarbageHoles.
    GarbageHoles int       `json:"garbage_holes,omitempty"`
    // Random garbage holes: every garbage row draws its own hole columns
    // ("messy" garbage). Unset — the default, and every pre-field meta — the
    // rows of one raise share a single draw ("clean" garbage). Moot at 0 holes.
    RandomGarbageHoles bool `json:"random_garbage_holes,omitempty"`
    // Guideline garbage: a clear attacks by the Guideline table — 0/1/2/4 rows
    // for 1/2/3/4 lines (game.AttackRows). Unset — the default, and every
    // pre-field meta — every cleared line sends one row.
    GuidelineGarbage bool `json:"guideline_garbage,omitempty"`

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

Player identity is handled entirely in the UI at startup — no persistent files are stored on disk. When a player starts Jetris, they are prompted on a login screen to enter a player name. This name **is** the player ID used in all NATS subjects, KV keys, and game rosters. There is no separate display name.

### Validation

Player names are validated to be legal NATS subject tokens:
- Must be 1–32 characters
- Cannot contain `.`, ` ` (space), `*`, `>`, tab, newline, carriage return, or null

Validation is implemented in `config.ValidatePlayerName(name) error`.

### Flow

1. App starts → login screen is shown (no lobby exists yet)
2. Player enters a name → the name shape is validated (`config.ValidatePlayerName`) and then the lobby KV is checked (`lobby.IsNameInUse`) for a player presence entry with the same name (case-insensitive, whitespace-trimmed). A present key means an active player; stale entries self-expire via the presence TTL (below), so there is no `LastSeen` check — an unclean shutdown's entry frees the name once its TTL lapses.
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

Uses `natscontext.Connect(contextName, opts...)` from `orbit.go/natscontext`. The returned `Settings` struct includes `JSDomain`, which is passed to `jetstream.NewWithDomain` so that JetStream API calls are correctly scoped when connecting to a multi-domain NATS deployment. All connection config — server URL, credentials, TLS, SOCKS proxy — comes from the context file rather than from CLI flags, eliminating configuration drift between Jetris and the `nats` CLI.

Also in `connection.go`:

```go
// Bootstrap connects per cfg (NATSURL wins over NATSContext, matching the CLI
// flag precedence) and provisions the lobby chat stream, lobby KV, and archive
// stream. On any post-connect failure nc is closed before returning — callers
// never receive a live connection together with an error.
func Bootstrap(ctx context.Context, cfg config.Config) (*nats.Conn, jetstream.JetStream, jetstream.KeyValue, error)

// CheckResult reports the server a probe reached, its ID (so callers can tell
// an embedded server from a stranger on the same port), and the ping.
type CheckResult struct {
	ServerURL string
	ServerID  string
	RTT       time.Duration
}

// CheckConnection dials per cfg, measures the core NATS round-trip time, and
// closes the connection, reporting the server reached, its ID, the ping, and
// the player count in its lobby KV (LobbyPlayerCount; Lobby=false when the
// bucket doesn't exist yet). Provisions nothing — backs the login screen's
// server-browser row ↻ and LAN-mode check.
func CheckConnection(cfg config.Config) (CheckResult, error)

// CoreNATSPing times a real message round trip: subscribe to a fresh inbox,
// publish the send timestamp to it, wait for the server to deliver it back.
// Unlike (*nats.Conn).RTT it exercises the publish→subscribe path the game
// itself uses, not the protocol-level PING/PONG.
func CoreNATSPing(nc *nats.Conn, timeout time.Duration) (time.Duration, error)
```

`Bootstrap` is the single connect+provision path, invoked from the login screen's connection page via `doConnectAndLogin`. The URL path adds `nats.Timeout(5s)` so a black-holed address fails promptly instead of hanging the UI's "Connecting…" state.

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
// with Shutdown(). Backs the login screen's "LAN party mode (embedded NATS server)"
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

// EnsureChatStream creates the chat stream if it does not exist. It carries
// BOTH the lobby chat and every game's chat (one filter, ChatSubjectFilter),
// distinguished purely by the game-ID subject token.
func EnsureChatStream(ctx context.Context, js jetstream.JetStream) error

// EnsureArchiveStream creates the game archive stream if it does not exist.
func EnsureArchiveStream(ctx context.Context, js jetstream.JetStream) error

// SealGameStream sets Sealed: true on a game stream, permanently preventing
// writes. Only used by the cleanup pass for orphaned finished streams; normal
// game end DELETES the stream instead.
func SealGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// DeleteGameStream deletes a game stream entirely (normal game end, cancelled
// games, and orphaned-stream cleanup).
func DeleteGameStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// ListGameStreams returns names of all streams matching the JETRIS_GAME_ prefix.
func ListGameStreams(ctx context.Context, js jetstream.JetStream) ([]string, error)

// PurgeGameChat removes one game's chat messages from the shared chat stream
// (purge by the game's GameChatSubject). Used by archive.ArchiveAndCleanup and
// lobby.DeleteGame.
func PurgeGameChat(ctx context.Context, js jetstream.JetStream, gameID string) error

// The replay archive — ONE shared file-backed stream (config.ReplayStream)
// holding the full copy of every top-ranked finished game's stream (see
// internal/archive's replay step). CopyGameToReplayStream drains the game
// stream through an ordered consumer and republishes every message under
// config.ReplayCopySubject — payload verbatim, the ORIGINAL stream timestamp
// in the config.ReplayTsHeader header (republishing stamps copy-time
// timestamps, so the recorded pace travels in the header; an original-speed
// replay paces itself from it), the original headers deliberately dropped
// (their CAS expectations reference the dying game stream). Copies go out as
// chunked ASYNC publishes (a game stream holds thousands of messages; one ack
// round trip each would take minutes to a remote server) with every ack
// checked, and the marker (config.ReplayMarkerSubject) is published last,
// synchronously — its presence proves the whole copy landed, so an in-flight
// copy is never listed. Any failure purges the partial copy: a game has a
// complete replay or none.
func EnsureReplayStream(ctx context.Context, js jetstream.JetStream) error // Bootstrap-provisioned (AllowDirect: marker lookups)
func CopyGameToReplayStream(ctx context.Context, js jetstream.JetStream, gameID string) error

// PurgeReplay removes one game's replay — copies and marker — with a single
// subject-filtered purge (config.ReplayFilter): a failed copy, or a game
// displaced from its bucket's top N.
func PurgeReplay(ctx context.Context, js jetstream.JetStream, gameID string) error

// GetReplayMarker returns the marker's stream sequence (ErrMsgNotFound = the
// game has no finished replay); ListReplayGameIDs lists every marker via a
// subjects-filtered stream info (config.ReplayMarkerFilter).
func GetReplayMarker(ctx context.Context, js jetstream.JetStream, gameID string) (uint64, error)
func ListReplayGameIDs(ctx context.Context, js jetstream.JetStream) ([]string, error)
```

#### `kv.go`

```go
// EnsureLobbyKV creates or retrieves the lobby KV bucket.
func EnsureLobbyKV(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error)

// PutLobbyPresence writes a presence value with a per-message TTL
// (config.PresenceTTL) — a plain KV put that self-expires.
func PutLobbyPresence(ctx context.Context, js jetstream.JetStream, key string, data []byte) error
```

The bucket has **no bucket-wide TTL** (game/invite keys must persist) but **per-key TTL is enabled** via `LimitMarkerTTL: config.PresenceTTL`. Presence liveness is now the KV layer's job, not application code: each heartbeat writes the presence key with a fresh `PresenceTTL` (via `PutLobbyPresence`, which publishes straight to the key's `$KV.<bucket>.<key>` subject with `jetstream.WithMsgTTL` — the KV client's `Put`/`Update` drop the TTL header). If a client stops beating, the server deletes the key ~5 min later and emits a delete marker every watcher observes, so a dead client vanishes from the lobby with no `LastSeen` bookkeeping and no `pruneStalePresence`. Game/invite keys, written with plain `Put`, carry no TTL and live on. (Enabling per-key TTL sets `AllowMsgTTL`/`SubjectDeleteMarkerTTL` on the `KV_JETRIS_LOBBY` stream.)

#### `consumer.go`

```go
type OrderedConsumerConfig struct {
    Stream        string
    FilterSubject string        // optional subject filter
    StartSeq      uint64        // 0 = from beginning
}

// NewOrderedConsumer creates an ordered consumer and returns a channel of
// jetstream.Msg. The consumer is automatically restarted on sequence gaps.
// The returned cancel func tears it down cleanly — including while the stream
// is quiet (a watchdog stops the iterator on cancellation, so the pump never
// hangs in Next waiting for a message that will never come; a replay session
// on the shared replay stream is exactly that case after its game finishes
// delivering).
func NewOrderedConsumer(
    ctx context.Context,
    js jetstream.JetStream,
    cfg OrderedConsumerConfig,
) (<-chan jetstream.Msg, context.CancelFunc, error)
```

#### `publish.go`

```go
// ExpectMode selects which CAS expectation (if any) a batch message carries.
// A message can carry at most ONE expectation: about its own subject or about
// another subject (a "carrier" guard for state the batch doesn't rewrite).
type ExpectMode int

const (
    // ExpectOwnSubject asserts ExpectLastSeq against the message's own subject
    // (Nats-Expected-Last-Subject-Sequence). The ZERO VALUE, so existing
    // callers that fill only Subject/Payload/ExpectLastSeq keep per-cell CAS.
    ExpectOwnSubject ExpectMode = iota
    // ExpectNone carries no expectation — an authoritative overwrite inside a
    // batch whose consistency is guarded by another (gated) message.
    ExpectNone
    // ExpectForSubject asserts ExpectLastSeq against ExpectSubject instead of
    // the message's own subject (WithBatchExpectLastSequenceForSubject). The
    // server rejects the whole batch if that subject's last sequence moved —
    // used to guard other players' piece cells without rewriting them. The
    // server rejects an expectation about a subject the SAME batch already
    // wrote earlier, so carriers must precede any write to their asserted
    // subject.
    ExpectForSubject
)

// CellUpdate represents a single cell's new state and the CAS expectation. The
// caller supplies the fully-built cell subject, so this package is subject-
// agnostic — it knows nothing about game modes or players. The engine builds
// Subject with the mode-appropriate scheme (Coop*/Competitive*/Team*CellSubject)
// and orders the slice for the desired consumer apply order.
type CellUpdate struct {
    Subject         string
    Payload         []byte
    ExpectLastSeq   uint64      // the expected sequence for Expect's target
    Expect          ExpectMode  // default ExpectOwnSubject (per-subject CAS)
    ExpectSubject   string      // required when Expect == ExpectForSubject
}

// PublishMoveAtomically publishes a set of cell updates as a SINGLE atomic
// batch, each message carrying the CAS expectation selected by its ExpectMode
// (per-subject CAS by default). Either every message commits or none does:
// every expectation is checked at commit time and a single failure rejects the
// whole batch, surfacing as ErrCASFailure. On success it returns the commit
// ack's stream sequence (assigned to the LAST message in the batch); the
// batch's messages get consecutive sequences, so the caller can infer every
// cell's assigned sequence from it.
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
// transitions (lock, hard-drop landing, and the NoCAS tail chunks of an
// oversized gated transform) where the publisher's view is the new ground
// truth. Subject to the same 1000-message atomic-batch limit; also returns
// the commit ack's stream sequence.
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

// Sentinel errors for batch publish outcomes, matched with errors.Is.
// classifyPublishErr maps JetStream API error codes onto them:
//   - 10071 (wrong last sequence for subject) and 10164 (the batch/in-process
//     variant of the same check) → ErrCASFailure: an expectation lost the
//     race, the batch was atomically rejected, recompute from converged state.
//   - 10176 (batch incomplete/abandoned), 10210/10211 (too many inflight
//     batches) → ErrBatchTransient: server-side pressure, retry.
//   - 10199 (batch too large) → ErrBatchTooLarge: a bug — batches are chunked
//     client-side and must never exceed the server limit.
var (
    ErrCASFailure     = errors.New("CAS sequence expectation not met")
    ErrBatchTransient = errors.New("transient batch publish failure")
    ErrBatchTooLarge  = errors.New("atomic batch exceeds server limit")
)
```

Both batch publishers run with `jetstreamext.BatchFlowControl{AckFirst: false}` (`batchNoAckFirst`): per-message acks carry no error information (all expectation checks happen at commit), so requesting an ack on the batch's first message only cost a full extra round trip — every batch is now **one RTT**, not two.

#### `subjects.go`

Re-exports the subject builder functions from `config` as a convenience. Internal packages may use either; the builders always delegate to `config`.

#### `fetch.go`

```go
// FetchPlayfieldState retrieves the current state of the given playfield
// subjects for a game in one round trip (or a few, for large boards) using
// jetstreamext.GetLastMsgsFor. Used by the engine on startup and reconnect to
// reconstruct the full playfield instantly without replaying the entire game
// stream history, by the merge-retry refetch, by a gated transform's
// snapshot recompute, and by the attacker's garbage-register refresh.
//
// The caller builds the subjects with the mode-appropriate scheme (coop or
// competitive), so this function is subject-agnostic. Cell subjects come back
// keyed by the (row, col) parsed from the subject; non-cell playfield subjects
// (the per-board garbage/txn registers) come back with Row = Col = -1 and the
// caller branches on Subject. Subjects that have never been written have no
// last message and are simply absent from the result (empty cell).
func FetchPlayfieldState(
    ctx context.Context,
    js jetstream.JetStream,
    gameID string,
    subjects []string,
) ([]PlayfieldCellMsg, error)

type PlayfieldCellMsg struct {
    Row     int    // -1 for a non-cell (register) subject
    Col     int
    Subject string // the delivered subject (register routing via Is*Subject)
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

`FetchPlayfieldState` calls `jetstreamext.GetLastMsgsFor(ctx, js, streamName, cellSubjects)` where `cellSubjects` is the caller-supplied list of cell subjects for the game. The engine builds it with the mode-appropriate scheme: `config.CoopCellSubject(gameID, row, col)` for the shared cooperative board (no player token), `config.CompetitiveCellSubject(gameID, playerID, row, col)` for one competitive player's board, or `config.TeamCellSubject(gameID, team, row, col)` for one team's shared board. This returns the last message per subject in a single server round trip — far more efficient than replaying the entire stream from sequence 0 on join or reconnect. PLAYER engines use this for their initial playfield snapshot before starting the ordered consumer (`startSeq = maxSeq+1`), then the consumer takes over for live updates. SPECTATOR engines skip the snapshot entirely and start their board consumers from the beginning of the stream (`startSeq 0` → `DeliverAllPolicy`): the retained full history replays the whole game quickly on screen, then the consumer tracks live play.

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
// place. They are used on DETACHED playfields only — ProjectShrinkCascade's
// lift resolution (re-stamping each piece on its scratch board), the teams
// elimination vacate's projection on a snapshot clone, and unit-test setup;
// see "Invariant: NATS as single source of truth for the playfield" in
// section 9.
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

// ProjectShrinkCascade returns the full new set of rows after a garbage raise
// on ANY board — competitive or shared; the one-piece competitive board is
// simply the degenerate case of the same transform. The locked stack shifts up
// by rowsToAdd and rowsToAdd adversarial rows tagged with causerIdx fill the
// bottom; holes[k] lists the columns left EMPTY in the k-th added row, top to
// bottom (see RaiseHoles); a nil entry, or a nil/short slice, raises solid
// rows there, which can never clear.
//
// Every falling piece on the board — whoever owns it — holds its on-screen
// position while the stack rises beneath it ("dropped into place"). A piece is
// pushed up only when the risen stack, the new garbage, or another falling
// piece would overlap it, and only by the MINIMUM rows that clear the
// conflict. Lifts CASCADE: pieces are resolved bottom-most first (ties broken
// by playerIdx for determinism), and a piece already lifted is an obstacle for
// the pieces above it, so a rising stack can push a whole column of stacked
// pieces upward. A piece is never merged into the risen stack.
//
// topped lists the players whose pieces could not be kept on the board (the
// only conflict-free lift would push a cell above row 0); their pieces are
// left unstamped and the engine eliminates them. boardFull is true when the
// shift pushed a LOCKED cell past the top of the board (the stack itself no
// longer fits): the engine eliminates the board's owner (competitive) or every
// remaining player on it (teams).
func (pf *Playfield) ProjectShrinkCascade(rowsToAdd, causerIdx int, holes [][]int) (rows []Row, topped []int, boardFull bool)

// RaiseHoles returns the hole columns of each of the rows garbage rows one
// raise lands, top to bottom, for ProjectShrinkCascade: by default every row
// shares ONE draw ("clean" garbage — the holes stack into a well; each raise
// draws its own), with random every row draws its own ("messy" garbage).
// Nil when the game raises solid rows. The engine's default raise draw
// (Engine.garbageRaiseHoles; tests pin it).
func RaiseHoles(width, holes, rows int, random bool) [][]int

// RandomGarbageHoles draws the hole columns of one garbage row: holes
// distinct random columns of a width-wide board, sorted — clamped to
// 0..config.MaxGarbageHoles and to width−1 so every garbage row keeps at
// least one adversarial cell. Nil for holes <= 0 (a solid, permanent row).
func RandomGarbageHoles(width, holes int) []int
```

#### `attack.go`

```go
// AttackRows converts a clear of lines rows into the garbage it owes the
// opponents: one row per line by default; under a game's guideline-garbage
// rule (GameMeta.GuidelineGarbage) the Guideline table — a single sends
// nothing, a double 1 row, a triple 2, a Tetris 4. handleLockIn passes its
// result to bumpVictimLedgers (0 = no bump).
func AttackRows(lines int, guideline bool) int

// AdversarialRowCount returns the number of garbage rows at the bottom of the
// board: contiguous bottom rows containing AT LEAST ONE adversarial cell.
// Garbage is raised at the bottom and clears collapse the rows above it
// downward, so garbage rows always form one bottom-anchored block; the count
// can now DROP (a holed garbage row cleared through its holes). It plays no
// engine role (exactly-once application is the txn gate's job, and the
// deficit is register arithmetic) — the UI diffs successive counts to strobe
// newly landed rows (a drop strobes nothing), and tests observe it. "At
// least one" rather than "all" because a garbage row raised with holes has
// empty cells until a player fills them (and locked player cells once they
// do), and can transiently hold an overlaid active piece; the holes are
// capped below the width, so a garbage row always retains adversarial cells.
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
    Adversarial     bool      `json:"g,omitempty"`  // adversarial garbage cell (competitive/teams shrink); a row of nothing but these is permanent, one whose holes a player filled clears like any line
}

// IsFull reports whether the row is complete: every cell locked (occupied,
// not active) and at least one of them a player's. A solid garbage row —
// nothing but adversarial cells, what a 0-hole raise lands — is permanent and
// never completes; a garbage row raised with holes clears like any other line
// once a player's locked cells have filled every hole.
func (r Row) IsFull() bool

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
2. Publishes an updated `GameMeta` with `PieceIdx = pieceIdx` to `jetris.game.<id>.meta` — this is a CAS update using the current meta sequence. If the CAS fails (the other player's engine raced to publish first for the same lock-in), the engine reads the new meta value; since both engines increment by 1 from the same base, the value is idempotent and the race winner's value is correct.
3. Calls `rng.Sequence.Piece(pieceIdx)` to determine the next piece type and spawns it at the top of the playfield.

This makes `PieceIdx` in `GameMeta` eventually consistent: any engine joining mid-game via `FetchGameMeta` gets the current piece count in one round trip.

The same lock-in transition also triggers the **completed-line check** (`CompletedRows` → clear). Because the ordered consumer applies a publish batch **one cell message at a time** and the lock-in (and thus the completion check) fires the instant the player's last `Active` cell disappears, the **order of the cells within a batch matters** even though the batch commits atomically. Every publish path uses one ordering rule, `orderedCellKeys`: cells are batched by **category of their NEW content — active first, locked/occupied second, empty vacates last** — tie-broken by ascending (row, col). This single rule covers all the cases that previously needed the per-row `bottomFirst` flag:

- A **relocating piece** (gravity, lateral move, rotation, hard drop with the piece staying active) never transiently has **zero** active cells: its new active cells are all applied before its old positions are vacated, so no *spurious* lock-in fires. This covers the single-row horizontal I — whose old and new footprints don't overlap — in **every** direction, not just downward.
- An **in-place lock or hard drop** fires lock-in exactly once, at the batch's **last** message (the vacate that removes the player's final active cell) — by which point all landing/locked cells are already applied, so a line completed by the drop is detected at that lock, not one piece later.
- A **coop line clear** applies the other player's shifted active piece before vacating its old positions.
- A **garbage raise** (the gated shrink transform, competitive and teams alike) applies the re-stamped piece(s) first, the rising stack second, vacates last.

The `bottomFirst`/`applyBottomFirst` parameters that used to thread through the publish helpers are gone.

> On a **shared board** (coop and teams alike), lock, hard-drop, spawn, and gravity go through `publishProjectedCellsWithMergeRetry` (CAS + refetch-merge-retry), not a plain NoCAS write — see §`internal/engine` and the publish table in the implementation plan. A NoCAS write could overwrite (or vacate) the *other* player's mid-flight active cells from a possibly-stale snapshot, corrupting their piece; CAS+merge skips those cells. The **coop line clear** stays on the same merge-retry path (coop has no registers), while every whole-board transform on a competitive or team board — line-clear collapse, garbage application, teams elimination vacate — is a **txn-gated atomic batch** (`publishGatedTransform`, §`internal/engine`): NoCAS cells serialized by the board's txn register, with per-subject/for-subject guards protecting teammates' pieces on team boards. Every publish path publishes only the cells that **actually changed** (`diffCells` for moves/spawn/lock/hard-drop, `changedCells` for clear/shrink, diffed over the FULL row range `0..Height-1` — a truncated diff used to strand duplicated or orphaned cells in the headroom) — a move is ~4–8 cell messages, and a clear publishes only the cells that differ after the shift, which keeps the merge-retry from exhausting against the other player's moving piece (the contention that dropped clears/spawns: uncleared line + stuck player). Per-cell CAS itself makes contention much rarer: two coop pieces in the same row no longer conflict, only writes to the *same cell* do.

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
// decreasing according to the standard Guideline speed curve.
func GravityInterval(level int) time.Duration
```

---

## 9. internal/engine

The active game session. This is where NATS and game logic meet. One `Engine` instance is created per game the local player is participating in (as a player or spectator). The engine owns the ordered consumer for the game stream and drives all game state transitions.

### Invariant: NATS as single source of truth for the playfield

The in-memory `*game.Playfield` held by the engine is a **read-only replica** for everyone except the cell consumer (`runConsumer` in `consumer.go`). Specifically:

- **The only place `e.playfield` is mutated is `pf.Apply(row, col, cell, seq)` inside the cell consumer.** That call is invoked when an ordered-consumer message for one of this engine's cell subjects is delivered.
- **No game action mutates the playfield directly** — not the local player's moves, not hard drops, not piece locks, not line clears, not garbage raises, not piece spawns. Each action computes the *projected* row contents using the helpers in `internal/game/playfield.go` (`ProjectMove`, `ProjectLock`, `ProjectHardDrop`, `ProjectClearRows`, `ProjectShrinkCascade`), diffs them against the live board down to the changed cells (`diffCells` / `changedCells`), and publishes only those cells. The consumer then applies those cells when it receives the echo, and the UI re-renders from the updated `e.playfield`. (The one nuance is the publish **write-through** below, which applies a *committed* batch to the replica without waiting for the echo — still the same `pf.Apply` path, reconciled by sequence.)
- **The UI renders only from `e.playfield`.** It never sees pre-publish state.

This eliminates two-way drift between the local replica and the stream: every player on every machine sees the playfield evolve in the same order it was committed to JetStream. The price is that there is a NATS round-trip between input and visual feedback, and that two rapid inputs may both validate against the same pre-echo state — the second is dropped via CAS rejection (per-subject `ExpectLastSequencePerSubject`), surfaced as a CAS-flash event for visual feedback.

### Atomic batches with per-subject CAS

Every publication of multiple cells from the engine is a SINGLE atomic batch, in one of three shapes:

- `natspkg.PublishMoveAtomically` with **per-subject CAS** on every cell (`Nats-Expected-Last-Subject-Sequence`, applied via `jetstreamext.WithBatchExpectLastSequencePerSubject(seq)`). Used for moves, rotations, and spawns.
- `natspkg.PublishCellsAtomicallyNoCAS` — multi-cell batch without CAS. Used for authoritative state transitions (piece lock, hard-drop landing) and the NoCAS tail chunks of an oversized gated transform.
- The **txn-gated transform batch** (`publishGatedTransform`, `transform.go`) — the single publish path for every whole-board state change on a competitive/team board (garbage application, line-clear collapse, teams elimination vacate): the board's **txn register rides FIRST** with a per-subject CAS expectation at the register's last sequence (the exactly-once gate), followed by the changed cells as NoCAS overwrites — plus, on team boards, per-subject/for-subject guards for teammates' mid-flight pieces. A stale gate atomically rejects the loser's ENTIRE batch and the loser recomputes from a fresh server-side snapshot. Published through `PublishMoveAtomically` (mixed `ExpectMode`s per message).

Why per-subject CAS, not stream-level (`WithBatchExpectLastSequence`)? Each cell is its own NATS subject. Per-subject CAS rejects only when *our* cell was overwritten since we last saw it; concurrent writes to *other* cells don't conflict. This is essential in cooperative mode where two players write the same shared playfield — and per-cell granularity makes the conflict window far smaller than the old per-row scheme: two coop pieces moving through the same row no longer contend at all, only writes to the *same cell* do. It is also useful in competitive mode for parallelism between meta/event publishes and cell publishes.

Why atomic batch, not cell-by-cell? A single move typically touches 4–8 cells (the new footprint plus the vacated old positions). If those messages arrived at consumers as independent publishes, every other player would briefly observe a half-erased / half-placed piece between consumer applies. Atomic batch makes the multi-cell update visible to consumers as one indivisible step. Within the batch the ordering invariant is: **the txn register first** (when the batch is a gated transform — its echo must precede the vacates it explains), then the cells in `orderedCellKeys` category order (active, locked, empty) — see the lock-in section in §8 — because the consumer still applies the batch's messages one at a time. One server limit applies: the default atomic-batch limit is **1000 messages**, so the engine keeps every batch at or below that: `publishProjectedCellsNoCAS` chunks larger writes, and an oversized gated transform is split by `splitGatedItems` into a gate head + NoCAS tail (both only reachable on the largest many-player/4v4 boards).

The expected-last-sequence value for each cell comes from `e.playfield.CellLastSeq(row, col)` (the flat per-cell `LastSeq` array, index `row*Width+col`), updated via `pf.Apply(row, col, cell, seq)` from two places: the cell consumer on an ordered-consumer echo, and the **publish write-through** (`applyPublishedCells`), which advances it from the batch commit ack the instant a write commits. The write-through keeps the CAS expectation current so the next write doesn't lose a per-subject race against the engine's own just-committed write; `pf.Apply`'s strictly-higher-sequence rule reconciles the two sources (the echo of our own write carries the same sequence we already applied and is skipped; only a higher sequence updates memory).

**Optimistic sequence write-through (all modes).** The per-subject CAS expectation is the cell's `LastSeq` entry, advanced by `Playfield.Apply`. Rather than waiting for the engine's own consumer to echo a published cell back before that expectation (and the board content) catches up, a **successful publish is written through into the playfield immediately**: the batch commit ack returns the stream sequence of the last message, and since an atomic batch's messages get consecutive stream sequences the engine infers each cell's sequence (`message i of N → commitSeq − (N−1−i)`) and applies the committed content + sequence via `pf.Apply`. The two batch publishers (`PublishMoveAtomically`, `PublishCellsAtomicallyNoCAS`) return that commit sequence; `applyPublishedCells` does the write-through (gated transforms use `applyGatedItems`, which additionally advances the txn-register mirror `txnApplied`/`txnSeq`). `pf.Apply`'s "apply only a **strictly higher** sequence" rule reconciles this with the later echo: the echo of our own write carries the same sequence we already wrote through and is skipped, while a higher sequence (the other player's write in coop, or a NoCAS write we didn't originate) still updates memory. In coop the write-through applies only what actually committed (the first-attempt or merge-retry batch), so it never clobbers the other player's cells. This keeps the in-memory view current so a player cannot lose a per-subject CAS race against their own just-committed write (gravity vs. input, a write right after a NoCAS line-clear/shrink, a fast input burst). `applyPublishedCells` takes `e.mu` unless the caller already holds it — a `locked` flag is threaded through the publish helpers and `spawnPiece` because `spawnPiece` and the line-clear publish run under the consumer's lock while every other publish path runs with the lock released (`spawnPiece` itself takes `e.mu` around its projection+diff when called with `locked=false` on the Start path, releasing before publish).

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
    ownClearScore     atomic.Int64    // cumulative score from OWN clears only — the line_clear event's TotalScore
    ownClearLines     atomic.Int64    // cumulative lines from OWN clears only — the line_clear event's TotalLines
    hadActivePiece    bool            // guarded by e.mu (plus one pre-goroutine write in Start); written by the own-cells consumer, spawnPiece (post-publish), and handleTeamTopOut
    spawnPending      bool            // shared boards: spawn deferred (blocked only by another player's ACTIVE piece); guarded by e.mu; retried by retrySpawnIfPending on the gravity tick
    eliminatedPlayers map[string]bool // players who have topped out (competitive/teams); guarded by e.mu
    eliminatedTeam    map[string]int  // teams: eliminated player → team; guarded by e.mu
    teamOutcomeDone   bool            // teams: win/loss/draw already decided; guarded by e.mu
    visibleRowStart   int             // first visible row index (varies per mode/player count)

    // Garbage ledger + txn gate mirrors (competitive/teams; guarded by e.mu).
    // garbageOwed/garbageOwedSeq/garbageOwedBy mirror the own board's garbage
    // register (rows OWED, written by attackers); txnApplied/txnSeq mirror its
    // txn register (rows APPLIED, advanced by gated transforms) — the deficit
    // is garbageOwed − txnApplied. toppedByShrink is set when a txn echo lists
    // THIS player as topped, routing the imminent zero-active edge in
    // runConsumer to handleTopOut instead of handleLockIn. opponentGarbage
    // caches each opponent/opposing-team board's garbage register (keyed like
    // opponentPlayfields) so the attacker-side CAS-add starts from the replica.
    garbageOwed     int
    garbageOwedSeq  uint64
    garbageOwedBy   int
    txnApplied      int
    txnSeq          uint64
    toppedByShrink  bool
    opponentGarbage map[string]opponentLedger

    // eventTotals tracks, per sender, the last cumulative line_clear totals
    // folded from the events stream, so handleGameEvent folds DELTAS — a
    // full-history replay (mid-game spectator) or a missed intermediate event
    // converges to the same totals. Touched only by the events-consumer
    // goroutine — no lock needed.
    eventTotals map[string]struct{ score, lines int }

    // Channels for outbound events to the UI layer
    Updates        chan EngineUpdate
    OnGameFinished func() // called after the game transitions to finished (wired to archive.ArchiveAndCleanup)
    OnStreamMsg    func(ts time.Time, subject string, payload []byte, batchID string) // optional tap on every game-stream message delivered by this engine's consumers (set before Start; batchID is the Nats-Batch-Id of the atomic batch it committed in, "" for a single publish — drives the UI's "Show NATS messages" panel and its transaction grouping)

    // internal
    js          jetstream.JetStream
    ctx         context.Context
    cancelFn    context.CancelFunc
    moves       chan MoveType
    cellUpdated chan struct{}
    applyGarbage chan struct{} // cap 1: signals runInput to run one garbage-application attempt (register echo handlers signal whenever owed > applied; a signal sent before runInput starts is retained)

    // testHookBeforeGatedCommit, when set by a test, runs between a gated
    // transform's projection and its batch publish — the seam deterministic
    // race tests use to interleave a competing write and assert the gate
    // rejects and the recompute converges. Nil in production.
    testHookBeforeGatedCommit func(op string)
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

// Piece preview (GameMeta.NextCount, captured at Start). NextPieces returns
// the upcoming piece types in play order (element 0 spawns next), empty for a
// no-preview game — a pure read off the seekable sequence
// (seq.Piece(pieceIdx+1+i)), no queue state. The UI's NEXT well and the
// agent's planner both consume exactly this accessor, which is what keeps the
// fair-visibility horizon identical for humans and agents.
func (e *Engine) NextCount() int

// GarbageHoles reports how many holes every garbage row this game raises is
// punched with (GameMeta.GarbageHoles clamped at Start; 0 = solid rows).
func (e *Engine) GarbageHoles() int

// RandomGarbageHoles reports whether every garbage row of a raise draws its
// own hole columns (GameMeta.RandomGarbageHoles; false = one draw per raise).
func (e *Engine) RandomGarbageHoles() bool

// GuidelineGarbage reports whether this game's clears attack by the Guideline
// table — 0/1/2/4 rows for 1/2/3/4 lines (GameMeta.GuidelineGarbage; false =
// one row per cleared line). handleLockIn sizes the ledger bump with
// game.AttackRows(clearedLines, e.guidelineGarbage).
func (e *Engine) GuidelineGarbage() bool
func (e *Engine) NextPieces() []game.PieceType

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

Manages the ordered consumer goroutine(s). In cooperative mode, ONE consumer runs on the shared cell subjects (no player token — the subject carries no player segment), updating the single shared `Playfield`. In competitive mode, 1 + N consumers run — one for the local player's playfield namespace (cells + registers) and one per opponent — each updating a separate `Playfield` instance. In teams mode, exactly TWO consumers run — one on the own team's playfield namespace (updating `e.playfield`) and one on the opposing team's (updating the board under `TeamBoardKey` in `opponentPlayfields`). On competitive/team boards each consumer's filter is the widened `…playfield.>`, so the same delivery carries the board's garbage/txn register echoes, folded before the cell parse.

```go
func (e *Engine) runConsumer(ctx context.Context, pf *game.Playfield, filterSubject, opponentID string, startSeq uint64, isOpponent bool)
```

**Startup sequence:**

1. Call `nats.FetchGameMeta(gameID)` — returns `GameMeta` including `Seed`, `PieceIdx`, and `Status`. In **all** modes `e.seq = rng.New(meta.Seed)`. In competitive mode `e.pieceIdx = meta.PieceIdx`; in cooperative and teams mode `e.pieceIdx = 0` and each player tracks its own index independently (the sequence is shared, not forked with `seed+1` — in teams both teams therefore get the identical 7-bag). `e.playerIdx` was supplied at construction (from `lobby.JoinGame`); no discovery is done here. `e.playerCount`, `e.teamSize`, and `e.visibleRowStart` are set from meta, and the playfield is (re)allocated at the mode-appropriate width/height (teams: `TeamBoardWidth(teamSize)` × `TeamTotalRows(teamSize)` with `visibleRowStart = TeamVisibleRowStart(teamSize)`).
2. Call `nats.FetchPlayfieldState(gameID, subjects)` for the player's own board snapshot — `snapshotSubjects()` builds all `width × height` cell subjects, row-major, plus (competitive/teams) the board's two register subjects (`boardRegisterSubjects()`; coop has none). Above 512 subjects the fetch is chunked into ≤512-subject `GetLastMsgsFor` calls bounded to a common stream sequence (the server caps a multi-last direct get at 1024 responses). Apply all fetched cells to `e.playfield` via `pf.Apply`; register entries (delivered with `Row = -1`) are folded through `captureRegisterSnapshot` — the same paths the live consumer uses — so a late joiner or reconnecting engine discovers everything owed and applies the outstanding deficit before playing (a positive deficit is retained on the `applyGarbage` channel until `runInput` starts). Never-written subjects are absent from the result and stay empty. Record `maxSeq = max(all fetched sequences)`.
3. Start the ordered consumer with `startSeq = maxSeq + 1`. In cooperative mode this is ONE consumer on the shared cell subjects (filter `jetris.game.<id>.playfield.cell.>` — coop has no registers); in teams mode it filters the own team's whole playfield namespace (`TeamPlayfieldFilter`: `jetris.game.<id>.team.<t>.playfield.>`, cells + registers); in competitive mode the player's own (`CompetitivePlayfieldFilter`: `jetris.game.<id>.player.<pid>.playfield.>`). Messages on other subjects (events, meta, chat) that arrived between the lowest and highest fetched sequence are a tolerable gap — at most a few milliseconds of game time.
4. In competitive mode only, also start the **roster consumer** (`runRosterConsumer`, watching `jetris.game.<id>.roster.*`) which discovers opponents dynamically and calls `startOpponentConsumer` for each — fetching that opponent's cells and registers and starting one `runConsumer` per opponent on `CompetitivePlayfieldFilter(gameID, opponentPID)`. A known opponent passed at construction is started immediately; late joiners are picked up as their roster entries appear. In cooperative mode there is no opponent consumer — both players write to and read from the same shared cell subjects. In teams mode there is no roster consumer either (the roster is fixed before the game starts and elimination events carry the team); instead `startTeamBoardConsumer(ctx, 1-teamIdx)` starts the single opposing-team board consumer (cells + registers via `TeamPlayfieldFilter` — the opposing garbage register echo is what keeps the attacker-side ledger cache fresh).

**Cooperative mode design:**

In cooperative mode both players share a SINGLE wide playfield of width `playerCount × StandardWidth` (20 columns for 2 players). Cell subjects carry no player token — the shared board publishes to `jetris.game.<id>.playfield.cell.<row>.<col>` (every player publishes to and consumes from the same subjects) via the `config.CoopCellSubject` scheme, distinct from the competitive `config.CompetitiveCellSubject` scheme. Per-player filtering is never needed in coop, so the player identity lives entirely in the payload rather than the subject. Both players' active pieces exist on the same playfield and can move anywhere on it — they are not restricted to their own section. Each cell of an active piece is tagged with `Cell.PlayerIdx` (0 for creator, 1 for joiner) so the engine can distinguish which player's piece each cell belongs to.

Each player spawns their piece centered in their section (player 0: center of cols 0–9, player 1: center of cols 10–19) but can move it anywhere on the full-width board. `ActivePieceForPlayer(playerIdx)` finds only the piece belonging to that player (by matching `Cell.PlayerIdx`). `SetActivePieceForPlayer(p, playerIdx)` only clears active cells with matching `PlayerIdx` before setting new ones. Collision detection (`CanPlaceCoop`) treats the other player's active cells as obstacles in addition to locked cells.

Both players seed their RNG from the same `meta.Seed` but track their own `pieceIdx` independently, so they receive different pieces at any given moment. Each engine has ONE playfield (the shared one) and ONE ordered consumer (on the shared cell subjects) — no separate opponent playfield is needed. Both players write to the same shared cell subjects, though per-cell CAS means they only actually conflict when writing the *same cell*. CAS conflicts on **moves** (left, right, down, rotate) are NOT retried — the move is simply dropped and the player must try another move. CAS conflicts on **state changes** (lock-in, spawn, line clear) ARE retried with a batched refetch of the affected cells from the stream, since these must succeed for game consistency.

Line clears work on the full 20-wide rows. The score is shared — both players' line clears contribute to the same score total. The UI renders the single wide playfield directly (no concatenation of two separate playfields).

**Teams mode design:**

Teams mode is **cooperative within a team, competitive between the two teams**. Each team of `teamSize` players shares one team-scoped board of width `TeamBoardWidth(teamSize)` and height `TeamTotalRows(teamSize)` (the board grows one row per opposing player, like competitive, to leave room for adversarial rows). Within the team everything works exactly as in cooperative mode, reused through the `sharedBoard()` helper rather than duplicated: per-player sections by **team slot** (spawn offset `teamSlot*StandardWidth` instead of coop's `playerIdx*StandardWidth`), `Cell.PlayerIdx` carrying the **global roster index**, `CanPlaceCoop` collision against teammates' active pieces, and the full CAS+merge-retry publish discipline for spawn/gravity/lock/hard-drop/clear. `cellSubject`/`cellFilterSubject` build the `TeamCellSubject(gameID, teamIdx, …)` scheme; the two teams' subject spaces are disjoint, so cross-team writes are impossible by construction.

Between teams the competitive mechanics apply at team granularity:

- **Garbage attack.** A team's line clears owe unclearable adversarial rows to the OPPOSING team's shared board, delivered through that board's **garbage register** exactly as in competitive: the clearing player CAS-adds the cumulative rows-owed total (`bumpVictimLedgers` → `bumpLedger`, `ledger.go`), so overlapping attacks sum and none is ever lost. Every **alive** member of the receiving team may react to the register echo and apply the deficit (`applyOwedGarbage` on its own `runInput`); eliminated players and spectators structurally cannot (they don't run `runInput`).
- **Exactly-once via the txn gate.** Several teammates race to apply the same deficit, and each application is one txn-gated batch (`publishGatedTransform`): the gate's per-subject CAS admits exactly one — a loser's ENTIRE batch is atomically rejected (nothing stored), and its recompute from a fresh `fetchBoardSnapshot` finds `owed − applied` already zero and no-ops. No merge-retry, no double-shift; the common case is one batch, zero retries.
- **Pieces are pushed up, never crushed.** `ProjectShrinkCascade` (see §`internal/game`) holds every falling piece — whoever owns it — at its on-screen position unless the risen stack or garbage overlaps it, then lifts it by the **minimum** rows that clear the conflict; lifts cascade bottom-most-first through the pieces above. A piece pushed off the top eliminates its owner: the batch's txn record lists them in `Topped` (delivered before the vacating cells, since the register is the batch's first message), so the owner's engine treats the resulting zero-active edge as an elimination, not a lock-in. If the shift pushes **locked** rows past the top (`Full`), the whole board is lost and every remaining player on it is eliminated. Spawn-time top-out rules are unchanged (a spawn covered only by a teammate's falling piece is DEFERRED via `retrySpawnIfPending`, not a top-out).
- **Teammate moves can't be corrupted.** The applier's batch guards every other player's snapshot piece cells — and the headroom rows a teammate's fresh spawn could claim — with per-subject CAS at snapshot sequences; a foreign piece the transform leaves untouched is guarded by one `ExpectForSubject` "carrier". A teammate move/lock/spawn that landed first atomically rejects the whole batch, and the recompute sees the piece's new reality. The raise still overrides in-flight moves (their own per-subject CAS fails against the risen board — drop + flash) without ever burying or duplicating a mid-flight piece.
- **Per-player elimination, per-team game over.** A topped-out player vacates their piece and spectates while their team plays on; a team loses only when ALL its members have topped out, and every member of the other team — already-eliminated ones included — wins.

Teams scoring is the coop rule per team: a clear scores `teamSize × lines`, and the clearing engine publishes `EventLineClear{Team, Score, LinesCleared, TotalScore, TotalLines}` — teammates fold **both** the score delta and the line count (as coop does too) so every teammate's level and gravity interval stay in sync with the team total. The opposing team's clears do not touch an engine's own `score`; their garbage reaches it through the garbage register, not an event. However **every** engine (both teams, eliminated players, spectators) also folds every `EventLineClear` into the per-team scoreboard `teamScores[ev.Team]`/`teamLines[ev.Team]` and emits `UpdateTeamStats`, so the live TEAM A / TEAM B scores (and per-team levels) render on every screen (see "Score tracking" below).

One subtle gate: lock-in detection in `runConsumer` only runs when `e.getMode() == ModePlayer`. A spectator — or an eliminated teams player, whose elimination vacated their piece on the still-live shared board — must never run lock-in side effects off the echoes of their teammates' play.

**Teams elimination and outcome flow:**

1. `handleTopOut` now takes `(ctx, locked bool)` (`locked` = caller holds `e.mu`; `spawnPiece`'s top-out branch always does) and routes teams games to `handleTeamTopOut`.
2. `handleTeamTopOut` sets `hadActivePiece = false` BEFORE publishing (so the vacate echo can't read as a lock-in), then publishes a **vacate of the player's own active cells** as a txn-gated transform (`txnOpVacate` via `publishGatedTransform` — the gate serializes it against a racing garbage application, which could otherwise resurrect the dead piece from a stale snapshot; the projection no-ops if the piece is already gone). It marks the player in `eliminatedPlayers`/`eliminatedTeam`, publishes `GameEvent{Kind: EventGameOver, Team: teamIdx, …}` to the player's per-kind event subject, transitions to spectator with `Won: false`, and emits `UpdatePlayerEliminated`. It does NOT transition the game to finished — the team outcome logic decides that.
3. `handleTeamGameOverEvent` processes every elimination event (including the echo of our own): it tracks `eliminatedPlayers`/`eliminatedTeam`, computes per-team elimination counts, and decides the outcome **exactly once** (`teamOutcomeDone` flag). When the opposing team is dead, every `initialMode == ModePlayer` member of the winning team transitions to spectator with `Won: true` — already-eliminated winners flip their loss to a win — and calls `transitionGameToFinished` (CAS-deduped); it also emits `UpdateGameStatus "finished"` so an eliminated loser's "team plays on" UI flips. A defensive branch treats both-teams-dead as a draw (shouldn't happen — the event subjects share one totally-ordered stream, so one team completes strictly first and every engine reaches the same verdict).

**Per-message handling (cooperative mode — single shared playfield consumer):**

- Parses `(row, col)` from the subject via `ParseCellFromSubject`, decodes the cell payload (`game.UnmarshalCell`) and calls `pf.Apply(row, col, cell, seq)`, updating both the cell content and its per-cell `LastSeq`.
- After every cell update, scans for the **implicit lock-in signal** for this player's piece: if the previous state had an active piece for this `playerIdx` (`ActivePieceForPlayer(playerIdx) != nil`) and the new state has no active cells with matching `PlayerIdx`, a lock-in has just been committed by this player. The engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to spawn the next piece centered in this player's section.
- Emits a `UpdatePlayfield` with `ChangedRows: []int{row}` — the row is DERIVED from the cell position, so the UI event contract (and every UI package) is unchanged by per-cell storage — and the UI re-renders from the freshly applied `e.playfield`.
- On line-clear detection: checks the full-width playfield for completed rows. **Critically, the cleared rows are published synchronously before spawning the next piece** — this prevents a race condition where the spawn modifies the playfield while the clear is still being published. The score is updated and emitted to the UI. Level is recomputed and the gravity interval adjusted.
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

**Per-message handling (competitive/teams — own and opponent/opposing-team playfield consumers):**

- The widened `…playfield.>` filters also deliver the board **registers**; those are detected first (`IsGarbageSubject`/`IsTxnSubject` — they carry no cell payload) and folded via `handleGarbageRegisterEcho` / `handleTxnRegisterEcho` (`registers.go`): on the own board they advance the owed/applied mirrors and signal `runInput`'s garbage application when `owed > applied`; on an opponent board the garbage echo refreshes the attacker-side `opponentGarbage` cache (an opponent's txn gate is ignored — it matters only to that board's players).
- For a cell message: parses `(row, col)` from the subject via `ParseCellFromSubject`, decodes the cell payload and calls `pf.Apply(row, col, cell, seq)`, updating both the cell content and its per-cell `LastSeq`.
- After every cell update on the own playfield, scans for the **implicit lock-in signal**: if the previous state had an active piece and the new state has no active cells anywhere, a lock-in has just been committed — *unless* `toppedByShrink` is set (a remote gated shrink's txn register, delivered before its vacating cells, listed this player as pushed off the top), in which case the zero-active edge routes to `handleTopOut` instead of `handleLockIn`. On a lock-in the engine increments its own `pieceIdx` and calls `rng.Sequence.Piece(pieceIdx)` to determine the next piece.
- Emits a `UpdatePlayfield` with `ChangedRows: []int{row}` (the row derived from the cell) so the UI re-renders from the freshly applied `e.playfield`.
- On line-clear detection: checks own playfield for completed rows and publishes the collapse as a txn-gated transform, synchronously before spawning the next piece; the committed clear then advances every victim board's garbage register (`bumpVictimLedgers`, on a goroutine).
- Emits appropriate `EngineUpdate` events for the UI on each meaningful state change.

#### Input + gravity loop (`runInput`, in `move.go`)

The gravity arm also calls `retrySpawnIfPending` after each tick as the deferred-spawn BACKSTOP; the primary retry is message-driven — the own-board consumer re-attempts a pending spawn on every incoming cell change (the very message that may be the blocker moving away), because at agent speeds a blocking piece crosses the spawn cells in milliseconds and a tick-only cadence starved deferred players to a piece every few seconds. Both paths re-check under e.mu and re-defer or top out (locked cells) as appropriate. The same hook doubles as the **piece-less watchdog**: an alive player with no active piece and nothing pending for two consecutive ticks gets a forced spawn — the consumer's lock-in edge detector only fires when a message arrives on the board's consumer, so a wholesale-dropped spawn publish (or missed edge) on a since-silent board (last teammate eliminated) would otherwise strand the player piece-less forever. The watchdog is gated on `gameStarted` (set when the engine learns the meta is in_progress): the gravity ticker runs from engine start, during the countdown, and an ungated watchdog would force-spawn and start the game mid-countdown. `spawnPiece` additionally sets `hadActivePiece = true` after its publish (write-through already applied) so the consumer's lock-in edge detector cannot miss a piece that is hard-dropped before its spawn echo is processed. The meta consumer's game-start spawn has the inverse guard: it spawns only when no piece is on the board AND `!hadActivePiece` — a piece-less replica with `hadActivePiece` still true means a lock/hard-drop write-through happened and its echo (which fires the lock-in edge and spawns the next piece) is in flight; spawning there would re-stamp the same piece over the vacated cells and MASK that edge forever (the replica never reads zero active cells), silently swallowing the locked piece's line clear.

```go
func (e *Engine) runInput(ctx context.Context)
```

The engine's single gameplay-write goroutine: it `select`s over the moves channel (player input), the gravity timer, the lock-delay timer, the `cellUpdated` own-board-echo signal, and the `applyGarbage` signal channel. Running everything on **one** goroutine is deliberate — a player's own gravity drop, a player move, the piece's lock, and a garbage application can never publish to their cell subjects concurrently, so they can never lose the per-subject CAS race against each other (in either mode; this removed the spurious rainbow flashes seen in competitive play). On each gravity tick it attempts to drop the active piece one row via `attemptMove(MoveDown, true)`; player input calls `attemptMove(move, false)`. **Neither locks a piece**: a blocked downward step is a no-op, and after every move — and every own-board echo, via `cellUpdated` — `updateLockDelay` (`lockdelay.go`) re-times the Guideline lock delay: it arms the lock timer (`config.LockDelay`, 500 ms) when the piece has landed on locked cells or the floor (`game.CanPlace` one row down fails — the same test that ignores every active piece, so a piece resting on a teammate's still-falling piece is waiting, not landing), restarts it on a successful shift or rotation (at most `config.LockDelayMoveResets` = 15 times per lowest row the piece has reached; a new piece is recognised by `pieceIdx`), and stops it when the piece is airborne again. When the timer fires, `lockPieceIfGrounded` publishes the in-place lock — NoCAS in competitive, CAS+merge-retry on shared boards, exactly the batch the blocked gravity step used to publish — unless the stack changed under the piece meanwhile and it can fall again. Only a hard drop locks at once. The `applyGarbage` arm runs `applyOwedGarbage` (`ledger.go`) — applying owed garbage on this goroutine is what guarantees a raise never races the victim's own move publishes, and (with `getMode() != ModePlayer` guarded on every arm) makes application structurally impossible for spectators and eliminated players. The gravity arm doubles as the garbage **backstop**: if `owed − applied > 0` is still outstanding after a tick (a signal consumed by an attempt that lost its gate, or one that arrived before `runInput` started), it re-signals the application at gravity cadence. On a shared board (cooperative and teams) it reads the current level from `totalLines` after each tick and adjusts the ticker interval when the level changes (in teams the folded `LinesCleared` keeps teammates' levels in sync); in competitive mode the interval is fixed.

**Cooperative gravity and lock-in:** When gravity cannot move a piece down, the engine distinguishes between two cases: (1) the piece is blocked by locked cells or out-of-bounds — the piece has landed and locks when the lock delay expires (see above); (2) the piece is blocked only by the other player's active piece — the piece does NOT lock, since that obstacle is temporary (it will itself fall on its next gravity tick). In case (2), gravity simply waits and tries again on the next tick. This prevents premature lock-ins caused by two pieces passing through the same rows.

**Cooperative hard drop:** When a player hard-drops (space bar), the piece falls instantly to the lowest valid position — which may be on top of the other player's active piece. If the piece lands on locked cells or the floor, it locks immediately as usual. If it lands on the other player's active piece, it does NOT lock — instead it stays active and resumes falling by gravity. The other player's piece will itself fall on its next gravity tick, at which point gravity will continue dropping this piece further.

Both shared-board behaviours apply identically on the teams board, with teammates' active pieces as the temporary obstacles.

#### `move.go`

```go
// attemptMove is the central move dispatch function. internal=true marks an
// engine-driven move (e.g. a gravity tick); internal=false marks player input
// (player input drops + flashes on CAS failure, gravity ticks merge-retry in
// coop). It validates the move geometrically against the local playfield, builds
// the projection, diffs it to the changed cells (diffCells), and publishes them
// via the publishProjectedCells* helpers in engine.go. A blocked step is a
// no-op — a blocked MoveDown never locks the piece (that is the lock delay's
// job, lockdelay.go). It dispatches by board:
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

#### `lockdelay.go` — the Guideline lock delay

```go
// lockDelayState is the timing of the current piece's lock: the pending lock
// timer (fire is nil while none is pending; runInput selects on it), the
// pieceIdx the counters describe, the lowest row the piece has reached and
// the timer restarts spent since it last advanced. runInput's goroutine only.
type lockDelayState struct { ... }

// updateLockDelay re-times the lock after anything that may have moved the
// piece or changed the stack under it (a player move, a gravity tick, an
// own-board echo). before is the piece before the engine's own move, so a
// successful shift/rotation (which restarts the timer) can be told from a
// blocked or CAS-dropped step (which does not).
func (e *Engine) updateLockDelay(before pieceSnapshot)

// lockPieceIfGrounded is the timer's expiry: the in-place lock publish,
// skipped if the piece can fall again.
func (e *Engine) lockPieceIfGrounded(ctx context.Context)
```

`Engine.lockDelay` is `config.LockDelay` (tests shorten it). The delay is engine-local: nothing about it is on the wire.

#### Publish & CAS helpers (`engine.go` / `move.go`)

There is no `Publish`/`PublishHardDrop`/`ErrMoveDropped`/`ErrLockIn` API and no 50ms-wait-on-`cellUpdated` retry loop (`cellUpdated` only wakes `runInput` to re-time the lock delay). All publish/CAS logic lives in `engine.go` (and the hard-drop helpers in `move.go`). The relevant helpers are:

```go
// cellSubject / cellFilterSubject / cellSubjects build this engine's own cell
// subjects with the mode-appropriate scheme (Coop*/Competitive*/Team*CellSubject;
// teams uses TeamCellSubject(gameID, teamIdx, ...)). cellFilterSubject is the
// own-board consumer filter: the whole playfield namespace (…playfield.>,
// cells + registers) in competitive/teams, cells-only in coop (no registers).
// cellSubjects returns all width×height cell subjects, row-major;
// snapshotSubjects appends boardRegisterSubjects (the garbage/txn subjects,
// empty in coop) — the board snapshot fetch asks for the whole set.
func (e *Engine) cellSubject(row, col int) string
func (e *Engine) cellFilterSubject() string
func (e *Engine) cellSubjects() []string
func (e *Engine) boardRegisterSubjects() []string
func (e *Engine) snapshotSubjects() []string

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
// CAS expectations — used for authoritative state on the competitive board
// (in-place lock, hard-drop landing); whole-board transforms go through the
// gated path in transform.go instead. Write-throughs on success.
// A batch above the server's 1000-message atomic-batch limit (only reachable on
// degenerate many-player boards) is split into sequential atomic chunks along
// the already-ordered key list — the category order remains a correct total
// order across chunk boundaries, the only loss being a briefly visible
// intermediate board between chunks.
func (e *Engine) publishProjectedCellsNoCAS(ctx context.Context, cells map[game.CellPos]game.Cell, locked bool)

// publishProjectedCellsWithMergeRetry is the SHARED-BOARD (coop and teams) path
// for piece-level steps that MUST land (spawn, gravity tick, lock, hard drop —
// plus the coop line clear; a teams clear and the teams elimination vacate are
// gated transforms instead, see transform.go). On CAS
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
// differs from cur — so a bulk transform (line-clear collapse, garbage raise)
// republishes only the cells that actually changed rather than every cell (far
// less per-subject CAS contention on the shared coop board). Every bulk
// transform diffs the FULL row range (0, Height) — headroom included; a
// truncated diff used to strand duplicated or orphaned cells in rows 0-3.
// Replaces the old changedRows.
func changedCells(cur, projected []game.Row, fromRow, toRow int) map[game.CellPos]game.Cell
```

There is no recompute-and-retry-until-it-lands hard-drop loop. The hard-drop destination is computed **once** (`game.HardDropDestination` / `HardDropDestinationCoop`). In competitive mode the landing cells are published NoCAS (`publishHardDrop`); in cooperative mode through the merge-retry path (`publishHardDropCoop`, ≤16 retries). The `orderedCellKeys` category ordering guarantees the landing cells are applied before the vacates, so a line completed by the drop is detected at the lock, not one piece later. See the publish-strategy summary below.

#### `registers.go` — the board registers

The payload types and echo handlers for the two per-board registers (see §4 for the subjects; competitive/team boards only):

```go
// GarbageRegister is the payload of a board's garbage register: the cumulative
// number of garbage rows OWED to the board since game start. Attackers advance
// it with per-subject CAS (read-add-publish, bounded retry), so simultaneous
// attacks serialize and converge to the exact sum. The board's players apply
// deficit = Total − TxnRegister.Applied; the register being cumulative makes
// application idempotent across duplicate signals, replays, and reconnects.
type GarbageRegister struct {
    Total int `json:"total"`
    By    int `json:"by"` // playerIdx of the most recent attacker (UI attribution)
}

// TxnRegister is the payload of a board's txn register — the FIRST message of
// every gated bulk-transform batch, carrying a per-subject CAS expectation
// that makes the transform exactly-once. Applied is the cumulative garbage
// rows applied (advanced by shrink transforms; restated unchanged by
// clear/vacate transforms). Topped lists the players whose falling pieces the
// transform pushed off the top — the register precedes the vacating cells in
// the batch, so a victim's consumer learns "this zero-active edge is a shrink
// top-out, not a lock-in" in-band, before the vacates arrive. Full means the
// shift pushed LOCKED rows past the top — the board is lost (competitive: the
// owner; teams: every remaining player on it).
type TxnRegister struct {
    Applied int    `json:"applied"`
    Op      string `json:"op"`     // txnOpShrink "shrink" | txnOpClear "clear" | txnOpVacate "vacate"
    Topped  []int  `json:"topped,omitempty"`
    Full    bool   `json:"full,omitempty"`
    By      int    `json:"by"`     // playerIdx of the applier
}

// handleGarbageRegisterEcho folds a garbage-register message (consumer echo or
// snapshot fetch): own board — advance the owed mirror and signalGarbageApply
// when rows are owed; opponent/opposing-team board — refresh the attacker-side
// opponentGarbage cache (newest sequence wins).
func (e *Engine) handleGarbageRegisterEcho(boardKey string, isOpponent bool, payload []byte, seq uint64)

// handleTxnRegisterEcho folds a txn-register echo (own board only). It
// advances the applied mirror and handles REMOTE shrink eliminations: Topped
// listing this player sets toppedByShrink (the imminent zero-active edge is an
// elimination); Full tops out every remaining player directly (their piece may
// still be stamped, so no zero-active edge would come). The APPLIER never takes
// these paths off its own echo — its write-through already advanced txnSeq to
// the commit sequence, so the echo fails the strictly-higher guard; it handles
// its own elimination inline (applyOwedGarbage).
func (e *Engine) handleTxnRegisterEcho(ctx context.Context, isOpponent bool, payload []byte, seq uint64)

// signalGarbageApply nudges runInput to run one garbage-application attempt.
// Non-blocking; the channel holds at most one pending signal, and a signal
// sent before runInput starts (the Start snapshot reconcile) is retained.
func (e *Engine) signalGarbageApply()

// captureRegisterSnapshot routes a register message from a board snapshot
// fetch (Row < 0 entries) into the same fold paths the live consumer uses —
// including a late joiner discovering its board was already lost (Full).
func (e *Engine) captureRegisterSnapshot(ctx context.Context, boardKey string, isOpponent bool, subject string, payload []byte, seq uint64)
```

#### `ledger.go` — attacker CAS-add and victim application

```go
// bumpVictimLedgers advances the garbage register of every victim board by
// `lines` rows: competitive — every surviving opponent; teams — the opposing
// team's board. One goroutine per victim; each runs an independent CAS-add.
// Called from handleLockIn after a committed clear (on a goroutine —
// handleLockIn holds e.mu and the bump must not extend the critical section).
func (e *Engine) bumpVictimLedgers(ctx context.Context, lines int)

// bumpLedger CAS-adds `lines` to one victim board's garbage register: publish
// {total+lines, by} expecting the register's last sequence; on a lost race,
// refresh straight from the stream (FetchPlayfieldState on the one subject)
// and re-add, with a small per-player-offset backoff that desynchronizes
// simultaneous attackers (bounded by ledgerBumpMaxAttempts = 20 — conflicts
// only come from other attackers' bumps, so a handful of cycles converges).
// The first attempt starts from the cached register (opponentGarbage, kept
// fresh by that board's consumer) instead of a read round trip.
func (e *Engine) bumpLedger(ctx context.Context, cacheKey, subject string, lines int)

// applyOwedGarbage runs on runInput — the engine's single gameplay-write
// goroutine — and applies the board's outstanding deficit (owed − applied) as
// ONE gated cascade transform (ProjectShrinkCascade via publishGatedTransform,
// op "shrink"). The cells are NoCAS: the raise overrides any in-flight move,
// whose own per-subject CAS then fails against the risen board (drop + flash).
// If the projection tops out THIS player (or the board is full), it clears
// hadActivePiece BEFORE the publish so the batch's own vacate echoes cannot
// fire a spurious lock-in, then calls handleTopOut after the commit. Ends with
// a full-board rerender.
func (e *Engine) applyOwedGarbage(ctx context.Context)
```

#### `transform.go` — the gated bulk transform

The single publish path for every whole-board state change on a competitive or team board — garbage application ("shrink"), line-clear collapse, and the teams elimination vacate. A transform is one atomic batch: `[ txn register (per-subject CAS — the gate) | changed cells (NoCAS) ]`, with teammate guards on team boards (per-subject CAS on rewritten foreign-piece/headroom cells, one `ExpectForSubject` carrier per untouched foreign piece — carriers must precede any write to their asserted subject, which holds trivially since a carried subject is never written).

```go
// gatedProjection computes one transform attempt against a playfield snapshot
// plus the register values; ok=false means nothing left to do (deficit closed,
// rows already cleared by someone else) and ends the transform as a no-op.
type gatedProjection func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) (rows []game.Row, newTxn TxnRegister, ok bool)

const (
    gatedBatchLimit           = 1000 // mirrors the server's default max atomic batch size
    gatedTransformMaxAttempts = 6    // gate-rejection recomputes; contention is self-limiting
)

// publishGatedTransform runs one bulk transform to completion: project → gated
// atomic batch → on gate rejection, recompute from a fresh server-side
// snapshot and try again. project runs against the live replica on the first
// attempt and against fetchBoardSnapshot's result on retries. locked reports
// whether the caller already holds e.mu (handleLockIn runs under the
// consumer's lock). Returns true when a batch committed; the caller reads the
// outcome (topped/full) from whatever its last project call captured.
func (e *Engine) publishGatedTransform(ctx context.Context, op string, locked bool, project gatedProjection) bool

// fetchBoardSnapshot fetches a consistent server-side snapshot of the own
// board — full cell contents with sequences plus both registers — for a
// gate-rejection recompute (a detached playfield; no lock needed).
func (e *Engine) fetchBoardSnapshot(ctx context.Context) (*game.Playfield, GarbageRegister, TxnRegister, uint64, error)

// buildGatedItems assembles the batch: the txn register first (CAS at txnSeq),
// then the changed cells in orderedCellKeys order, applying the teammate
// guards on team boards. Degenerate case: more held-piece carriers than plain
// writes to ride on falls back to re-writing one snapshot cell of each
// remaining held piece with its unchanged content under per-subject CAS — an
// equivalent, consumer-invisible guard.
func (e *Engine) buildGatedItems(snap *game.Playfield, cells map[game.CellPos]game.Cell, newTxn TxnRegister, txnSeq uint64) ([]gatedUpdate, error)

// publishGatedItems publishes the assembled transform and write-throughs each
// committed chunk (applyGatedItems: cells + inferred sequences + the txn
// mirror, so the later echo is a no-op). Returns (committed, retry): retry on
// ErrCASFailure/ErrBatchTransient, false on a hard error.
func (e *Engine) publishGatedItems(ctx context.Context, op string, items []gatedUpdate, cells map[game.CellPos]game.Cell, newTxn TxnRegister, locked bool) (bool, bool)

// splitGatedItems splits an oversized batch (above gatedBatchLimit — only
// reachable on the largest 4v4 team boards) into a gate head + NoCAS tail:
// the head keeps the txn register and EVERY expectation-carrying message (a
// guard in a NoCAS tail would be no guard at all) plus as many leading plain
// cells as fit; the remaining plain cells spill to NoCAS tail chunks, relative
// order preserved. Winning the gate excludes concurrent bulk transforms, and
// racing moves are still caught by their own per-subject CAS, so the tail
// stays consistent.
func splitGatedItems(items []gatedUpdate) (head, tail []gatedUpdate)
```

Gate-rejection semantics: when two transforms race — teammates applying the same garbage, or a clear racing a raise on the same board — the server atomically rejects the loser's ENTIRE batch (nothing stored; all expectations are checked at commit). The loser recomputes from a freshly fetched consistent snapshot: a lost raise finds the deficit already zero and no-ops; a lost clear re-detects its completed rows (still complete, possibly shifted) and lands on the next attempt. The `testHookBeforeGatedCommit` seam lets tests interleave a competing write between projection and publish to exercise exactly this path deterministically.

#### `events.go`

Defines the `EngineUpdate` type sent from engine to UI over the `Updates` channel, and the event message format published to the per-kind, per-player event subjects (`jetris.game.<id>.events.<kind>.<pid>`).

```go
type UpdateKind int

const (
    UpdatePlayfield        UpdateKind = iota  // one or more rows changed
    UpdatePieceLocked                         // active piece locked in
    UpdateLineClear                           // lines cleared, rows shifted
    UpdateGameOver                            // game ends for this player
    UpdateOpponentField                       // competitive: opponent's field changed; teams: opposing team's board changed
    UpdateOpponentShrink                      // retained in the enum but no longer emitted (raises surface as ordinary board updates off the register/cell echoes)
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
message's JetStream **stream timestamp** (`msg.Metadata().Timestamp`), subject, raw
payload, and the id of the **atomic batch** it committed in — the server keeps the
`Nats-Batch-Id` header on every stored message of a batch, so the tap reads it straight
off `msg.Headers()` ("" for a plain single-message publish). The hook runs on the
consumer goroutines and must not block. The native UI wires it to
`App.recordStreamMsg`, which appends to a capped (`msgLogCap` = 5000-entry) log under `a.mu` — gated on
the `msgShow` flag mirrored each frame from the "Show NATS messages" checkbox, so an
unchecked panel costs one flag check per message — and the game screen renders the tail
in a monospace strip across the bottom of the window (`natslog.go`: timestamp · subject ·
payload, with the JSON payload syntax-colored by `jsonSpans`). The log is cleared on
`startGameScreen`/`returnToLobby`.

*Transaction grouping.* `recordStreamMsg` resolves each batch id to a **tint ordinal**
via `App.msgGroup`: a fresh ordinal per newly seen batch id, kept in `msgGroupOf` (bounded
to the last `msgGroupCap` = 64 ids by the insertion-ordered `msgGroupSeen`), so every
message of one batch shares an ordinal even when another consumer's message interleaves
between them, and two adjacent batches always land on different `msgGroupPalette` slots.
Un-batched messages consume an ordinal of their own and record `batched: false`. `msgRow`
records the row's text into a macro first (the background and bracket can only be sized
once the row height is known), then paints, all in the group color: the tint at 7% alpha
across the full row width, the 3 dp left bar at 85%, and — on the run's first and last row
(`firstOfGroup`/`lastOfGroup`, computed by looking one row back and one row forward in the
list callback) — a 9 dp horizontal stub closing the bracket at the top and bottom. The
text then replays over it. The row's first column is a fixed-width gutter (`msgBatchIDLen`
= 6 chars via `shortBatchID`, blank unless the row opens a batch) carrying the batch id in
the group color, so the id is named without knocking the timestamp/subject columns out of
alignment. Only the first row of a run carries the 3 dp top gap, so a batch reads as one
block. The reset helper
`resetMsgGroups` clears the bookkeeping alongside `msgLog`.

*Resizable strip.* `natsMsgSection` stacks the divider (`msgPanelDivider`) above the strip
(`natsMsgPanel`). `msgPanelHeightPx` resolves the height each frame: the window-reactive
default (`max(msgPanelHeight, 20% of the available height)`) until `msgPanelDp` is set by
a drag, always clamped to `[msgPanelMinH, available − msgPanelKeepH]` so a shrinking
window can never bury the board and re-growing it restores the chosen height. Drag
handling follows the `gioui.org/x` resizer pattern: only the **last** `pointer.Drag` of
the frame is applied, because every event's position is relative to the divider as it was
laid out the previous frame and applying two would count the same movement twice; the
press position (`msgGrabY`) is subtracted so the panel edge tracks the grab point instead
of jumping to the cursor. The divider registers `gesture.Drag` plus
`pointer.CursorRowResize` over its whole bar and lights up (accent) while pressed.
`natslog_test.go` drives a real `input.Router` through the section to prove a 100 px
upward drag grows the strip by exactly 100 px and that a drag past the window bottom
stops at the floor.

**Game events published to `jetris.game.<id>.events.<kind>.<pid>` (per kind, per sender — consumed via `EventsSubjectFilter`, `…events.>`):**

```go
// EventKind identifies the type of game event.
type EventKind string

const (
    EventLineClear EventKind = "line_clear"
    EventGameOver  EventKind = "game_over"
)

// GameEvent is the JSON payload published to the events subjects.
//
// Garbage attacks are NOT events: they are recorded durably in the victim
// board's garbage register (GarbageRegister), which simultaneous attackers
// CAS-add and victims reconcile against — fire-and-forget events from
// simultaneous attackers could race each other, a cumulative register
// serializes and sums them, and the amount owed is recoverable from any
// snapshot. (CAS-failure feedback is not an event either; it stays local as
// UpdateCASFlash.)
type GameEvent struct {
    Kind         EventKind `json:"kind"`
    PlayerID     string    `json:"player_id"`               // who caused/detected the event
    LinesCleared int       `json:"lines_cleared,omitempty"` // for EventLineClear (coop and teams)
    ClearedRows  []int     `json:"cleared_rows,omitempty"`  // for EventLineClear: which rows
    Score        int       `json:"score,omitempty"`         // EventGameOver: final score; EventLineClear (coop/teams): score delta
    Level        int       `json:"level,omitempty"`         // EventGameOver: level achieved (from the sender's line total)
    PieceCount   uint64    `json:"piece_count,omitempty"`   // for EventGameOver: total pieces placed
    PlayerIdx    int       `json:"player_idx,omitempty"`
    Team         int       `json:"team"`                    // teams: sender's team (0 = A, 1 = B)

    // line_clear only: the sender's CUMULATIVE totals from its OWN clears
    // (ownClearScore/ownClearLines). Receivers fold the DELTA against the last
    // total they saw from that sender (eventTotals), so any missed
    // intermediate event is subsumed by the
    // next — and a late joiner replaying each sender's last event reconstructs
    // the full scoreboard.
    TotalScore int `json:"total_score,omitempty"`
    TotalLines int `json:"total_lines,omitempty"`
}
```

**Shrink flow (competitive mode):**

1. Player A's engine detects a line clear after a lock-in (implicit detection from cell state) and publishes the collapse as a **txn-gated transform** (`txnOpClear`): the batch's txn register restates the applied total unchanged and gates the collapse against a concurrent raise on A's own board; the cells changed by the row shift ride NoCAS (`changedCells` diffs the full row range so only cells that differ are published).
2. The committed clear then advances every victim board's **garbage register** (`bumpVictimLedgers`: every surviving opponent, one goroutine each): read the register's cached value, publish `{total + n, by: A}` expecting its last sequence, refresh-and-re-add on a lost race. Simultaneous attackers serialize on the CAS and the totals **sum** — nothing is trimmed or lost, unlike the old fire-and-forget shrink event.
3. Each victim's own-board consumer folds the register echo (`handleGarbageRegisterEcho`) and signals its `runInput`, which applies the deficit `owed − applied` as one gated cascade transform (`applyOwedGarbage` → `ProjectShrinkCascade` → `publishGatedTransform`, op `"shrink"`): the locked stack shifts up, `n` adversarial rows fill the bottom (`Cell.Adversarial = true`, rendered grey) — solid when the game's `GarbageHoles` is 0, otherwise punched with that many empty cells at one random column set per raise — or one per row in a `RandomGarbageHoles` game (`RaiseHoles`, the engine's `garbageRaiseHoles` draw); a solid garbage row is never completable while a holed one clears like any line once a player fills its holes (`IsFull()`: every cell locked and at least one non-adversarial) — and the txn register advances `Applied` to the owed total — exactly once, however many signals arrive. In a 3+ player game every victim applies its own board's deficit independently.
4. The transform's cells are NoCAS — the raise overrides the victim's in-flight move, whose own per-subject CAS then fails against the risen board and is dropped + flashed. The victim's falling piece holds its position while the stack rises and is lifted only by the minimum rows a conflict forces; a piece pushed off the top (`topped` containing the victim) or locked rows pushed past the top (`boardFull`) top the victim out (`handleTopOut`). See `jetris-gameplays.md` for the full competitive shrink rules.
5. A victim that is behind — high RTT, a reconnect, a late join — reconciles from the registers whenever it catches up: the Start snapshot captures both registers and applies everything owed before playing. Spectators and eliminated players structurally never apply (no `runInput`).

**Shrink flow (teams mode):** the identical ledger + gated-transform machinery at team granularity — the clearing player CAS-adds the OPPOSING team's board register, any alive member of the receiving team applies, and the txn gate makes the application exactly-once across racing teammates (a loser's whole batch is rejected and its recompute finds the deficit zero). The cascade lift, `Topped` routing, and `Full` semantics are the same transform (`ProjectShrinkCascade` applies to both modes); the shared-board extra is the teammate guards on the batch. See the "Teams mode design" section above and `jetris-gameplays.md` for the rules.

**Score tracking:**

In **cooperative mode** the team score is a plain local counter (`score atomic.Int64`). When a player clears lines it adds `playerCount × lines` to its own `score` (reflecting the harder-to-fill wider playfield) and publishes a `GameEvent{Kind: EventLineClear, Score: delta, LinesCleared: n, TotalScore, TotalLines}` on its per-kind event subject; every other player's events consumer folds the **delta between the sender's cumulative totals and the last totals it saw from them** (`eventTotals`) into its own local `score` and `totalLines` (then `refreshLevel()` stores/emits the new level), so all clients converge on the same combined team total, shared level, and gravity — any missed intermediate event's delta is absorbed by the next, and a full-history replay folds to the same totals. This is **not** a server-side counter CRDT and uses no score subject. See `jetris-gameplays.md` for the authoritative scoring rules.

**Line clear publishing:** The cells changed by a clear (`changedCells` over the shifted projection, full row range) are published as a txn-gated transform in competitive and teams mode (NoCAS cells serialized by the board's gate against a concurrent raise) and through CAS+merge-retry in coop (so the shift can never overwrite the other player's mid-flight piece — `refetchAndMerge` skips any cell currently holding their active piece, and the category order applies their shifted piece before vacating its old positions). Score, events, and the attack are derived from the rows the committed transform **actually** cleared, not the pre-race detection — a gated clear that loses its gate re-detects its completed rows (still complete, possibly shifted) from the fresh snapshot. After the clear cells are published, the per-cell `LastSeq` entries are advanced by the write-through from the publish acknowledgment so subsequent CAS publishes use the correct sequences.

**CAS failure recovery:** After a no-CAS publish (or another player's committed batch), the other player's engine has stale per-cell `LastSeq` values until its consumer processes those messages. During this window, their writes to the same cells may fail with CAS errors. A failed player move is simply dropped (with a flash); the consumer echo carries strictly higher sequences, so `pf.Apply` corrects both the in-memory cell data and `LastSeq` and the next move validates against fresh state. Engine-driven coop writes recover faster: the merge-retry path refetches all affected cells in one batched round trip and retries immediately. Per-cell CAS also shrinks this stale window's blast radius — only the specific cells the other player touched can reject, not every write to a shared row.

In **competitive mode** each player keeps its own local score counter (`score atomic.Int64`), incremented by the number of lines it clears. The score is reported to other clients only at game end via the `EventGameOver` event (and rendered locally via `UpdateScore`); the per-player `score` subject is not used.

In **teams mode** each team's score follows the cooperative scheme: a clear adds `teamSize × lines`, published as `EventLineClear{Team, Score, LinesCleared, TotalScore, TotalLines}` and folded by every same-team engine — **both** the score delta and the line count (as coop does), so teammates' levels and gravity intervals stay in sync with the team total. Events from the other team are never folded into the own-team `score`; their garbage arrives through the board's garbage register, not an event.

Additionally every engine keeps a **per-team scoreboard** (`teamScores` / `teamLines`, both `[config.TeamCount]atomic.Int64`): the clearing player folds its score delta and line count into its own team's slots in `handleLockIn`, and every other engine — teammates, the opposing team's players, eliminated players, and spectators — folds the cumulative-total deltas into `teamScores[ev.Team]`/`teamLines[ev.Team]` in `handleGameEvent`, regardless of team. Each fold emits `UpdateTeamStats` with both teams' score totals and levels (per-team level = `game.Level(teamLines[t])`; also exposed via `Engine.TeamScores()`/`Engine.TeamLevels()`), which drives the live `TEAM A` / `TEAM B` HUD scoreboard on every screen. The event subjects are part of the same ordered stream, consumed from the start — a spectator joining mid-game replays each sender's last retained `line_clear` event, whose cumulative totals reconstruct the full scoreboard.

**Top-out transition:**

A player tops out when the newly spawned piece cannot be placed **on locked cells** (on shared boards a spawn blocked only by another player's active piece sets `spawnPending` and is retried from `runInput`'s gravity tick instead, the same locked-vs-active distinction `attemptMoveCoop` makes), when a garbage raise pushes their falling piece off the top (`ProjectShrinkCascade`'s `topped` — applied locally by `applyOwedGarbage`, or learned remotely from a txn echo via `toppedByShrink`), or when a raise pushes the locked stack itself past the top (`boardFull`/`Full` — the whole board is lost). Every path lands in `handleTopOut(ctx, locked)` (`locked` = caller already holds `e.mu`; `spawnPiece`'s top-out branch always does):
1. Publishes `GameEvent{Kind: EventGameOver, PlayerID: playerA, Score: e.score, Level: e.AchievedLevel(), PieceCount: e.pieceIdx}` to the player's per-kind event subject (`EventKindSubject(gameID, "game_over", playerA)` — one subject per player, so a player's single game_over can never be trimmed by other events; `AchievedLevel` = `game.Level(totalLines)`, the level reached at the moment of top-out — recorded in the archive).
2. Calls `e.transitionToSpectator(false)` — sets `mode = ModeGameOver` and emits `UpdateGameOver{Won: false}`. It does **not** itself stop the gravity ticker or move processor; those goroutines self-exit on their next iteration because they guard on `mode == ModePlayer`, and the consumers keep running. `handleTopOut` does not archive, delete the stream, or remove the KV entry.
3. In **cooperative mode**, any top-out ends the game for everyone: `handleTopOut` kicks off `transitionGameToFinished` (CAS the meta to `finished`).
4. In **competitive mode**, finishing is driven by last-player-standing in `handleGameEvent` rather than by `handleTopOut`: each engine tracks `eliminatedPlayers`; when a player receives game-over events for all but one player it calls `transitionToSpectator(true)` for itself if it is the survivor (win) and kicks off `transitionGameToFinished`. A simultaneous top-out (all eliminated) is a draw with no winner. The UI shows a player status list (playing/eliminated) and "YOU WON!"/"YOU LOST" at game over. See `jetris-gameplays.md` for the authoritative game-over rules.
5. In **teams mode**, `handleTopOut` routes to `handleTeamTopOut` instead: the player vacates their piece from the still-live shared board and spectates while their team plays on, and finishing is driven by whole-team elimination in `handleTeamGameOverEvent` — see the "Teams mode design" section above and `jetris-gameplays.md` for the authoritative rules.

**Meta transition + game archiving:** `transitionGameToFinished` CAS-retries the meta status to `finished` (setting `FinishedAt`), then immediately invokes `OnGameFinished` (on its own goroutine), which the front end wires to `archive.ArchiveAndCleanup`. That callback CAS-transitions the meta `finished → archived`, publishes an `ArchiveRecord` to the `JETRIS_ARCHIVE` stream (subject `jetris.archive`) with game ID, mode, player count, per-player results (ID, score, achieved level, piece count, winner — the other players' final score/level/piece count are recovered by draining every `game_over` event off the stream via `EventsSubjectFilter`: events live on per-kind, per-player subjects, so each player's single game_over survives retention; verdicts still come from the archiving ENGINE's live elimination record, never the replay), start/finish timestamps, — for cooperative — the total score and final shared level (`TotalScore`/`FinalLevel`), and the game's chat history (`Chat`, via `gameChatHistory`: the archiver's `lobby.ChatLog()` filtered to this game, last `ArchiveChatCap` = 200 lines — captured BEFORE the purge below, after which the record is the conversation's only home; nil lobby archives without it), then archives the replay (`maybeArchiveReplay`, below) and — only once `streamDeleteGrace` (5 s) has passed since the finish, giving every player's consumer time to receive the final events — deletes the game stream, removes the KV entry, and purges the game's chat messages from the shared chat stream (`Purge` with the game's `GameChatSubject`). The record is published **immediately** (the `game_over` drain ends as soon as a delivery reports `NumPending == 0`; a 1 s idle timer is the fallback), so every lobby's history shows the game at once; only the destructive steps wait out the grace. Archiving is CAS-protected so only one client performs it.

For **teams mode** the archive builds `playerTeams` from the roster snapshot (the authoritative source) with `EventGameOver`'s `Team` field as the fallback for any player missing from it. The losing team is the team whose **every** member sent an `EventGameOver`; `WinningTeam` is the other team's index (or `-1` if both are dead — a draw). `Winner: true` is set on EVERY member of the winning team, eliminated members included (a team win is shared), `PlayerResult.Team` records each player's team, the record carries `TeamSize`/`WinningTeam`, `TotalScore` is left unset, and the final per-team totals are recorded in `TeamScores`/`TeamLevels` (slices indexed by team, taken from the archiving engine's converged `Engine.TeamScores()`/`TeamLevels()`) — the lobby history line renders them as `A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave` (stats omitted for pre-existing records without them).

---

## 10. internal/lobby

Manages all lobby-level state: player presence, game listings, global chat, invitations, and the lifecycle operations (create game, join game, leave game). Does not know about the UI layer.

`JoinGame` enforces the overall roster cap (`len(Players) >= PlayerCount` → `ErrGameFull`) inside its CAS loop for EVERY mode, in addition to the teams per-team cap (`ErrTeamFull`) — the authoritative guard against overfilling a game (the GUI hides Join on a full game and the agent pre-checks `joinable`, but neither is atomic with the roster).

**Invitations** (`invite.go`) let a creator restrict a game to chosen players. `CreateGame` takes an `inviteOnly` flag (stored on the listing as `InviteOnly` alongside `CreatorID`); `Invite(ctx, toPlayerID, gameID, team)` writes an `Invitation` to the invitee's PER-GAME KV key `config.LobbyInviteKey(invitee, gameID)` = `invites.<invitee>.<gameID>` — a player may hold invitations to several games at once, one key each. The key's lifecycle is the invitation's state machine: written = pending; deleted by the invitee = accepted (`JoinGame` consumes it via `consumeInvite`); rewritten with `Declined: true` (`DeclineInvite`) = declined, KEPT so the inviter sees the refusal until dismissing it; deleted by the inviter (`Uninvite`) = retracted (or a declined marker dismissed); `DismissInvite` silently drops a stale invitation whose game is gone. `handleInviteUpdate` tracks EVERY invitation in the bucket (inviters need the state of the ones they sent), exposed as `MyInvites()` (own pending, oldest first), `InviteTo(gameID)`, and `SentInvites(gameID)` (pending + declined, for the creator's status rows). `JoinGame` guards invite-only games inside its CAS loop: only `CreatorID` or the holder of a fresh invitation (`inviteFor`, read straight from KV to beat watcher lag) may join, and an invitation exempts the joiner from the `MaxAgents` policy (`ErrNotInvited` otherwise). Invitations expire after `config.InviteTTL` (2 min). Every invite action also publishes a lobby event (below).

**Lobby events** (`events.go` kinds + `lobby.go` `publishEvent`/`startEventListener`): every lobby action — `CreateGame`, a successful `JoinGame` roster append, `UnjoinGame` (`game.created`/`game.joined`/`game.left`), `Invite`/`Uninvite`/`DeclineInvite` (`invite.sent`/`invite.retracted`/`invite.declined`) — is also announced as a transient CORE NATS message (`LobbyEvent{Kind, GameID, PlayerID, TargetID, Team, Time}`) on `config.LobbyEventSubject(kind)` = `jetris.lobby.event.<kind>`, captured by NO stream. `Start` subscribes (`config.LobbyEventsFilter`) and folds foreign events into immediate `LobbyUpdate` pings (games+players for game events, invite for invite events), so player availability and invitation state refresh in real time instead of at KV-watcher/heartbeat latency; `Stop` unsubscribes. `SetReady(ctx, gameID, ready)` is `ToggleReady`'s idempotent sibling (same CAS loop, exact value) used to CLEAR readiness when a player leaves the game screen.

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
    topRanked map[string]bool            // archived game IDs in their bucket's top N — access via IsTopRanked()
    replays   map[string]bool            // archived game IDs with a replay stream — access via HasReplay()

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

// HasReplay reports whether an archived game has a replay (and thus a Replay
// button in the history list). The set is driven by the replay stream's
// copy-complete markers: runReplayMarkerConsumer (an ordered consumer on
// config.ReplayMarkerFilter) flips a game on the moment its marker lands and
// kicks runReplayRefresher, which re-lists the markers
// (natspkg.ListReplayGameIDs) after a short coalescing delay — the archiver
// purges the replays its game displaced BEFORE publishing its marker, so that
// one listing is the reconciliation that drops them. (Kicked once at Start
// too, for the initial set.)
func (l *Lobby) HasReplay(gameID string) bool

// IsTopRanked reports whether an archived game ranks in the top
// config.ReplayTopN of its replay bucket among the records seen so far
// (config.ReplayTopRanked, recomputed as each record arrives) — the games
// the history highlights as TOP 10.
func (l *Lobby) IsTopRanked(gameID string) bool

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
// teamSize 0. Sets meta.TeamSize and listing.TeamSize. maxAgents is the agent
// policy (0 = agents may not join; listing-only) and inviteOnly restricts
// joining to invited players. nextCount is the piece-preview size, clamped to
// 0..config.MaxNextCount and stored on BOTH records: meta (the game-stream
// protocol — every peer, agents included, reads its lookahead allowance
// there) and the listing (the lobby row's "next N" tag). garbageHoles is how
// many empty cells every garbage row is raised with (clamped to
// 0..config.MaxGarbageHoles; 0 = solid rows that never clear), stored on both
// records the same way (meta rules the raise, listing tags the row "holes N").
// randomHoles makes every garbage row draw its own hole columns (off by
// default; forced off at 0 holes; both records, tag "random holes N").
// guidelineGarbage makes clears attack by the Guideline table, 0/1/2/4 rows
// for 1/2/3/4 lines (off by default; both records, tag "guideline garbage").
// ghost is the hard-drop ghost rule, stored inverted as GameMeta.NoGhost.
func (l *Lobby) CreateGame(ctx context.Context, mode config.GameMode, playerCount, teamSize, maxAgents, nextCount, garbageHoles int, randomHoles, guidelineGarbage, ghost, inviteOnly bool) (string, error)

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
// runHeartbeat writes a presence update to the lobby KV bucket every
// PresenceHeartbeat interval via PutLobbyPresence (each write carries a fresh
// PresenceTTL). On context cancellation it best-effort-deletes the local
// player's presence key and returns. There is no pruneStalePresence — stale
// entries are removed by the KV per-key TTL.
func (l *Lobby) runHeartbeat(ctx context.Context)

// Leave deletes this player's presence key immediately so other clients' KV
// watchers get a delete event right away (instead of waiting for the TTL).
// Called synchronously from the UI's quit/teardown while the connection is up.
func (l *Lobby) Leave(ctx context.Context)

// IsNameInUse reports whether a presence entry already uses the given name
// (case-insensitive, whitespace-trimmed). A present key = an active player;
// stale entries self-expire via the TTL, so there is no LastSeen check.
func IsNameInUse(ctx context.Context, kv jetstream.KeyValue, name string) (bool, error)

type PlayerPresence struct {
    PlayerID string         `json:"player_id"`
    Name     string         `json:"name"`
    Status   PresenceStatus `json:"status"`
    GameID   string         `json:"game_id,omitempty"` // non-empty if in a game or spectating
    LastSeen time.Time      `json:"last_seen"`         // heartbeat timestamp (informational; liveness is the KV TTL)
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
    NextCount   int               `json:"next_count,omitempty"` // piece-preview size, mirrors GameMeta.NextCount for the lobby row's "next N" tag
    GarbageHoles int              `json:"garbage_holes,omitempty"` // holes per garbage row, mirrors GameMeta.GarbageHoles for the lobby row's "holes N" tag
    RandomGarbageHoles bool       `json:"random_garbage_holes,omitempty"` // each row draws its own holes, mirrors GameMeta.RandomGarbageHoles ("random holes N" tag)
    GuidelineGarbage   bool       `json:"guideline_garbage,omitempty"`    // Guideline attack table, mirrors GameMeta.GuidelineGarbage ("guideline garbage" tag)
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

It enumerates game streams using only `natspkg.ListGameStreams` (the JetStream `StreamNames` API filtered to the `jetris.game.>` subject) — there is no `orbit.go/natssysclient`, no `Jsz`/system-account query, and no system-account fallback.

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
| `JETRIS_GAME_<id>` stream exists, no matching KV entry | If meta status is `in_progress`/`starting`, re-create the KV listing (don't delete a live game); otherwise delete the orphaned stream (also delete if meta can't be read) |

Note: During normal play the engine archives a finished game right at game end via `OnGameFinished` → `archive.ArchiveAndCleanup` (record at once; delete stream + remove KV after a 5 s grace; see Section 9). The cleanup pass handles only games left in a stale state by a crash or disconnect, and seals (rather than deletes) an orphaned finished stream.

### CAS Coordination

All transitions go through CAS on `jetris.game.<id>.meta`. If a CAS fails during cleanup, the function re-reads the current status and re-evaluates. A failed CAS means another client already handled that game — no further action is needed.

---

## 12. Front end: the native Gio UI

Jetris has a single front end, `internal/nativeui`, over the engine/lobby logic. It depends on `engine` and `lobby` (one-way) and communicates with them exclusively through their `Updates` channels and exported method calls — it is never imported by the business logic.

**`internal/nativeui`** is a native OS window built with **Gio** (`gioui.org`, pure-Go, cross-platform). It reads `engine.Updates` / `lobby.Updates` directly in bridge goroutines and repaints via `window.Invalidate()`, and it calls `engine.MoveLeft()` etc. directly from a key handler — a NATS update reaches the screen within one display frame. Files: `app.go` (window + frame loop + screen state machine), `bridge.go` (the `pumpEngine`/`pumpLobby` channel→UI pumps), `login.go`/`lobby.go`/`game.go` (screens), `backdrop.go` (the login backdrop: the embedded `login-backdrop.jpg` artwork, cover-scaled behind the card), `createwizard.go` (the modal create-game wizard the lobby's "Create a new game" button opens — step state `createWizStep`, per-frame dispatch `handleCreateWizard`, widget read-out/clamping `finishCreateWizard`, and the step renderers), `archive_view.go` (the `screenArchive` history viewer — redraws a finished game's saved end-of-game boards with a player roster beside them, winners highlighted), `board.go` (board drawing, plus `fitCellPx` — the window-reactive cell sizing every board view uses), `input.go` (keyboard → engine moves), `controls.go` (the on-screen arcade control pad and the animated MOVE BUFFER chip strip: blocky `fillRect` bitmap glyphs, `handlePadClicks` mouse-click → engine-move dispatch), `lifecycle.go` (login/create/join/spectate/countdown/teardown), `natslog.go` (the "Show NATS messages" panel: `recordStreamMsg` wired as `engine.OnStreamMsg`, the drag-resizable bottom message strip with per-transaction row tints, a display-only JSON colorizer), `brand.go` (the embedded nats.io "N" logo — `nats-icon.png`, `go:embed` — the lobby/archive branding banner, and the inline `natsTag` "N"+"NATS.io" chip used on the login tagline and at the foot of the game HUD), `fonts.go` (the embedded "Press Start 2P" pixel face — `PressStart2P-Regular.ttf`, SIL OFL 1.1, license in `PressStart2P-OFL.txt` — with `uiFontCollection` and the `a.pixel` label helper), `fireworks.go` (the victory fireworks overlay for competitive/teams wins), `version.go` (the build-version plate drawn in the window's top-right corner on every screen), `colors.go` alias to `internal/render`. Controls: ←/→ move, ↓ soft drop, ↑ or X rotate CW, Z rotate CCW, Space hard drop — and the same scheme as an on-screen **arcade control pad** under the board (`controlPad`, `controls.go`: ↺/←/↓/→/↻ buttons — the rotations are blocky circular-arrow glyphs (`glyphCW`, `glyphCCW` its mirror), not text — plus a wide accent DROP bar), so the game is fully mouse-playable; the pad renders dimmed pre-start and its clicks are swallowed (never queued) until the game runs. Keyboard focus uses Gio's `key.FocusFilter` + `key.FocusCmd` on the board tag. The game screen is **window-size reactive**: `fitCellPx` (`board.go`) picks the player-board cell size to fill the space left after the strip/pad (clamped 14–56 dp; when the NEXT well is shown its width is folded into the fit as extra columns so well + playfield always fit side by side), the spectator multi-board/team strips fit all boards side by side (scrolling below their minimum), the opponent thumbnail column scales with height, the HUD column takes ~19% of the width (200–300 dp), and the countdown numeral scales to ~1/8 of the window's short side. The window itself carries an `app.MinSize` of 760×720 dp (`minWinW`/`minWinH`, `app.go`) so it can never shrink below what the playfield, strip, pad, and chat need.

**Display-adaptive scale** (`scale.go`). Every screen is designed for the default 1280×820 dp window (`designWinW`/`designWinH`, matching `app.Size` in `Run`): the login card (`loginCardW`), the connection panel (`connPanelH`), dialog widths, side panels, the boards' cell clamps, the type sizes. On a larger display `App.layout` first runs the frame context through `scaledContext`, which multiplies the dp AND sp metric by `uiScale` — the window's excess over the design size in dp, the smaller of the two dimensions so nothing overflows, floored at 1 — so every `gtx.Dp`/`gtx.Sp` below it (and every material widget) grows in proportion and the screens fill the display instead of floating in empty space. Windows at or below the design size keep the 1:1 metric (the screens' own minimums and `app.MinSize` govern there), and HiDPI displays are compared in dp, so a Retina 2560×1600 window (1280×800 dp) is not stretched. `TestUIScale` pins the factor and the dp/sp effect.

**Piece-preview UI.** The create wizard's piece-preview step carries the **next-pieces editor** (`nextCountEd`, seeded "1", digits-only, clamped to 0..`config.MaxNextCount` in `finishCreateWizard`); it is gameplay rather than join policy, so every game gets the step (it precedes the who-can-join choice), and both create paths (`createGame`, `openInvitePicker`) thread it through to `lobby.CreateGame`. Game rows tag a revealing game "· next N", a holed-garbage game "· holes N" — "· random holes N" when each row draws its own — and a Guideline-table game "· guideline garbage" (`gameRow`). On the game screen, `nextWell` (`game.go`) renders the **NEXT well** — the upcoming-piece preview in its own sub-division hugging the playfield's top-left, classic arcade style: a miniature arcade well (the board's `colBorder` frame idiom over the `colPanel` background, so it reads as its own division) holding a scaled pixel-face NEXT label and one tile per revealed piece, stacked top-down in play order — read straight off `eng.NextPieces()` every frame (no queue state; the well advances the instant a lock-in bumps `pieceIdx`). It renders for players only (each seat spawns from its own `pieceIdx`, so a spectator engine has no meaningful queue) and only when `eng.NextPieces()` is non-empty. The well is **window-size reactive**: tiles use the SAME cell size as the playfield (the `fitCellPx` result), so the preview reads exactly like the pieces on the board, and `gameBoardArea` folds the well into the fit itself — `previewCols` extra board columns plus an 18 dp frame/gap slice of `reservedX` — so well + playfield always fit side by side. Each tile is `previewCols` (4) cells wide (every spawn orientation fits) and exactly its piece's rows tall — `drawMiniPiece` returns the drawn height so the stack packs evenly with no dead row under the 1-row I — drawn via `drawCell` with `render.CellStyle` locked-cell styling and whole-cell horizontal centering.

**Create-game wizard (`createwizard.go`).** The lobby's game-creation UI is a single **"Create a new game"** button (`createRow` → `createBtn`) that opens a modal wizard over the lobby (same scrim/Stack treatment as the invite overlays; `createWizStep` on `App` is the current step, 0 = closed). The steps, each a `wizStep*` constant with its own renderer: **1** `wizardModeStep` — game-type radios (`modeEnum`, with one-line descriptions) plus the seat-count editor (`countEd`, labeled per-team in teams mode); **2** `wizardNextStep` — the piece-preview count (`nextCountEd`), the ghost checkbox (`ghostCb`) and, for competitive/teams only (the step is retitled "PREVIEW, GHOST & GARBAGE"), the garbage-holes editor (`holesEd`, seeded "0", clamped to 0..`config.MaxGarbageHoles` in `finishCreateWizard`, forced to 0 for a cooperative game) with its "Random hole positions" checkbox (`randomHolesCb`, off by default, meaningful only with holes) and the "Guideline garbage" checkbox (`guidelineCb`, off by default — the 0/1/2/4 attack table); **3** `wizardJoinStep` — "Open game" vs "Invite only" radios (`createJoinEnum`); **4** `wizardAgentsStep` (open games only) — the agent policy (`allowAgentsCb`/`maxAgentsEd`). `handleCreateWizard` dispatches Cancel/Back/Next each frame; on the last step (3 for invite-only, where Next reads "Choose players…"; 4 for open, "Create game") `finishCreateWizard` reads and clamps the widgets and launches the game — `openInvitePicker` for invite-only, `createGame` otherwise. The widgets keep their values, so a later run starts from the previous run's choices.

**Invite-only create flow (`invite.go`).** Choosing "Invite only" in the create wizard makes its final step create an invite-only game and open the **invitee-picker** modal (`invitePickerOverlay`) over the lobby. There is NO send button — `handleInvitePicker` diffs each row's widget against its last-applied intent (`inviteChoice.lastSel`/`lastTeam`) every frame and dispatches immediately: selecting sends the invitation (`sendInvite` → `lobby.Invite`; a teams change re-invites to the new team), deselecting retracts it (`retractInvite` → `lobby.Uninvite`). The pinned first row is the CREATOR (`inviteSelfRow`, `inviteSelfSel`/`inviteSelfTeam` on `App`): UNSELECTED by default — `openInvitePicker` takes no seat, so the creator hosts as a spectator (row reads "spectating when the game starts"); selecting the row seats them (`selfSeat` → `JoinGame`, team A default) and deselecting frees it (`selfSeat` → `UnjoinGame`; teams moves re-seat via unjoin+join). Rows carry live status from `pickerRowStatus` (roster + `SentInvites`): pending "✉ invited — waiting…", declined "✕ declined" (widget reset; re-selecting re-invites), joined/ready "joined ✓"(· ready) with the control hidden — and every row is pinned to a fixed height (`inviteRowHeight`, ~30dp = the control's height) so a row doesn't shrink and shift the list up when its control disappears on join. A prominent bold header (built from `capLines`, rendered at `unit.Sp(16)`) tallies seats as "k/size seats filled — j joined · p invited · o open" (one line per team in teams mode). `inviteSeatUsage` (roster + pending invites, per team for teams) backs both that tally and the capacity guard, which REFUSES a selection that would over-fill a game/team by reverting the widget and showing `invitePickerErr`. The candidate list is reactive: `syncInvitePickerCandidates` runs each frame and `reconcileInvitePicker` folds in lobby joiners and drops leavers — except players involved with THIS game (roster/invitees, the `keep` set), who stay listed. **When the roster fills, `handleInvitePicker` closes the overlay and hands the creator over automatically**: `joinGame` (ready screen) if they kept their seat, `spectateGame` otherwise. "Close" merely hides the overlay (the game keeps filling; the lobby row shows the same status); "Cancel game" retracts all outstanding invitations (`cancelInviteGame`) and deletes the game. The creator can re-open the picker for an already-created invite-only game that still has open seats via an **Invite** button on its lobby row (`gameRowBtns.reinvite` → `reopenInvitePicker`): unlike `openInvitePicker` it neither creates the game nor forces a seat — it seeds the picker from the existing listing and mirrors current state (pending invitations pre-checked, the creator's own row reflecting whether they presently hold a seat), so a creator who joined and went "Back to Lobby" can still invite more players. An invited player's client shows the incoming pop-up (`incomingInviteOverlay`, driven by `lobby.MyInvites` — oldest pending first, the next surfacing once answered), which lists the game's current roster (who joined, team, ready), with Accept & Play (→ `joinGame`, consuming the invitation) / Decline (`DeclineInvite`, or `DismissInvite` when the game is gone). Both modals are Stacked over the lobby behind a click-swallowing scrim. Game rows tag invite-only games "· invite only" and hide Join from anyone but the creator or a pending invitee (`InviteTo`); the creator's row additionally renders `inviteStatusRows` — one line per outstanding invitation (pending with an **Uninvite** button, declined with **Dismiss**, via the per-(game,invitee) `uninviteBtns` clickables) — and roster names in invite-only rows read "(joined)"/"(joined · ready ✓)".

**Spectator overlays and history controls.** For spectators the pre-game countdown overlay renders over the multi-board views exactly as over a player's board (`countdownVisible` admits every non-finished mode; `gameBoardArea` stacks the overlay over the spectator content, and `runMetaConsumer` emits `UpdateGameStatus` to every engine — spectators included — so the overlay clears the moment the meta reads in_progress; visibility is gated on a PRE-START status check, not is-in-progress, so the stale GO! cannot resurrect when the status moves past in_progress to finished). Spectators also render every player's **CAS-failure flash**: a player broadcasts its dropped-write flash over CORE NATS (`config.FlashSubject`, outside the game stream's capture so it is never persisted), spectator engines subscribe (`runFlashConsumer`, `initialMode == ModeSpectator`) and re-emit it as `UpdateCASFlash`, and the UI keys it per board (`specFlash`, by player index competitively / team in teams) — players still see only their own flash (local, `emitCASFlash`). In the competitive spectator view each eliminated player's board carries a centered **OUT** chip while the game goes on (only the chip has a background — the board stays visible; `spectatorBoards` + `boardOverlay`, driven by `eng.IsEliminated` over the roster), and once the game is decided the spectator gets **the replay's ending, live** (`spectator_reveal.go`): `layoutGame` resolves `gameView.outcome` every frame through `resolveOutcome` — `spectatorVerdict` derives the decision from the roster, the engine's eliminations and (co-op) the shared game over: competitive is decided once all but one player are out, teams once a team is fully out, co-op at the shared game over, everyone out at once a draw — and on the first decided frame stamps `App.decidedAt` (the show's clock) and ranks the game (`rankLiveGame`: `config.ReplayRank` against the lobby's history using a provisional record built by `liveRecord` from the roster, the engine's per-player line-clear totals (`Engine.PlayerScores`, the archive's own numbers), the team totals or the shared score — then the archive record itself, via `lobby.ArchiveFor`, the moment it arrives, after which the rank is final). The winning board(s) then wear the winner show (`crownBoard` — the same frame pulse, rising banner, rank-graded trophy and prize piece the replay ending floats), every other board the OUT wash (`knockoutBoard`), the winners' labels and legend lines go gold in bold italic with a 🏆 (`boardLabel`; the winning team's legend header in `pixelEmph`) while the beaten keep their board colors, and `spectatorResultBox` sits beside the boards: GAME OVER, the verdict (`liveVerdict`: TEAM A/B WINS!, ALICE WINS!, ALICE & BOB WIN!, DRAW, FINAL SCORE n) in bold italic gold, the final scores (both teams', or every player's winners first) and Back to Lobby; the teams view does the same per team board (`spectatorTeamBoards`), and a spectated co-op board wears the show with GAME OVER once the crew tops out. A draw washes every board OUT and crowns nobody; `layoutGame` keeps invalidating while the show is up, and `startGameScreen`/`returnToLobby` reset the clock and rank (`App.decidedAt`/`liveRank`/`liveOf`/`liveRankFinal`). Spectator content is wrapped in `layout.Center` so the boards stay centered in the board area with or without the countdown Stack; both spectator multi-board strips (and the archive final-playfield strip) are laid by `scrollableBoards`, which keeps the boards centered while they fit but turns the strip into a horizontally scrollable `material.List` (with a scrollbar) once the boards together are wider than the window — so an overflowing board can be scrolled to instead of spilling off the edge or overlapping its neighbour (it measures the strip's natural width, fixed by the cell size, against the available width to decide). (Spectator engines never receive the players' `UpdateGameOver` in competitive or teams, which is why the decision is derived from the elimination events rather than waited for.) The lobby's GAME HISTORY header carries a sort selector (`histSortEnum`: "By score" — the default `sortedArchives` ranking, grouped by crew composition — or "By date" — `sortedArchivesByDate`, strictly chronological, the last game played first, no crew grouping) and three crew filter checkboxes — "Players only" (`histHumansCb`), "Agents and players" (`histMixedCb`), "Agents only" (`histAgentsOnlyCb`), all checked by default, each listing or hiding exactly the games of its `ArchiveRecord.AgentClass` (`archivesForDisplay`, via `histFilterBox`); the agent flag on each archived seat (`PlayerResult.Agent`) is stamped from the roster snapshot by `ArchiveAndCleanup`. Each history row's MODE cell carries a crew line — green **HUMANS** or orange **WITH AGENTS** (`archiveModeCell`) — and when the displayed history includes teams games a **TEAMS OVERALL** standings line renders between the header and the table (`teamStandingsLine`/`teamStandings`, `lobby.go`): per-team win and summed-point totals across those games, leader (wins, then points) in gold, agent filter applied.

**Look and feel — modern 8-bit, NATS-branded.** Display type (the login title, section headers, buttons, HUD stats, ready badges, the countdown, the game-over dialog, and the branding banner) renders in the pixel face (`pixelTypeface`); body text (chat, lists, editors) stays in the Go faces for readability. All chrome corners are square; panels, editors, and the server browser's list carry chunky 2 dp `colBorder` frames (the connection page's tab chips and panel use the accent); buttons and the game-over dialog sit on `hardShadow`'s offset solid shadow (`board.go`). Every playfield is drawn inside a `colBorder` arcade-well frame (`drawBoard`), filled cells are shaded with the classic 8-bit bevel — lighter top/left strips, darker bottom/right, a gloss pixel — gated by `CellAppearance.Bevel`, and `scanlines` paints a subtle CRT overlay over every frame (last in `App.layout`). The palette (`app.go`) is a dark blue-black (`colBg`/`colPanel`/`colBorder`) with the **NATS brand blue** `#27aae1` as `colAccent` and the NATS logo green as `colNATSGreen`, so the branding runs through the whole chrome; the login screen flanks the "JETRIS" pixel title with NATS logos and ends with a "peer to peer · made with NATS.io JetStream" tagline. The theme is built by `newUITheme` (shared with the layout tests, so snapshots match the live window).

**Login screen connection page.** The App is built via `NewWithPicker(cfg, contexts, selected, favorites)` and starts with nil `js`/`kv`; there is a single combined login screen (`login.go`): a "YOUR NAME" entry, the two-tab **CONNECT TO** page (`connPage`), and a full-width `bigAttractButton` Play. The tabs (`connTab`: `connTabBrowser` / `connTabLAN`, two `connTabChip`s on the panel's top edge) switch the panel between the **NATS server browser** (`browserTab`) and **LAN party mode** (`lanTab`). The browser is a `material.List` of rows (filling the fixed-height panel) built every frame from `connSections()`: collapsible sections (`sectionRow` — ▼/▶ + title + count, toggled via `connSecClosed`) holding `connEntry` rows (`entryRow`) — **FAVORITES** from `a.favorites` (`prefs.Favorite{Label, URL}`, key `urlKey(url)` = `"url:<url>"`, deletable through a per-row ✕ `connDelBtns` → `deleteFavorite`, which moves the selection to the next best row and persists), ending in the "+ Add a NATS URL…" row (`addRow`) that expands the inline `addForm` (label + URL editors, Add / Cancel; `addFavorite` validates, defaults the label to the scheme-less URL, refuses duplicates by selecting the existing row, selects the new row and persists through `favSave` — `prefs.SaveFavorites`, stubbed in tests; `connAddScroll` scrolls the list to the form when it opens); **CONTEXTS** from `nats.ListContexts` (key `ctxKey(name)` = `"ctx:<name>"`, the CLI's current context labeled "(nats CLI current)", each row's second line the context's URL from `nats.ContextURL` when known; `hintRow` when the machine has none); and **COMMAND LINE** only while `connCfg.NATSURL` (`--server`) isn't a favorite. Clicking a row sets `connSel` and probes it (`probeRow`: `startProbe` at once, or — while another probe is in flight — parked in `connProbeQueued`, which `drainQueuedProbe`, run every frame by `handleConnPage`, starts once the slot frees if the row is still the browser tab's selection); the selected row is an accent band (`entryRow`: `colAccent` fill, `colBg` label and ✕, the inline probe readout on a dark pill) carrying — in place of the other rows' blank gutter — the ↻ chip (`rowRefreshButton`: `glyphCW` on a `colBg`-bordered `connRefreshBtn`, faded while a probe runs). `connSelectedLine`, between the list and the status line, repeats the selection: a SELECTED chip plus `selectionCaption(entry)` (a favorite's label + URL, `context <name>` + URL, or the `--server` URL "from --server"), or NOTHING SELECTED in `colWarn` when `connSel` resolves to no row. That chip (a row click and LAN mode's `connCheckBtn` too, all dispatched in `handleConnPage` → `startProbe`) probes its entry off the UI goroutine (`doCheckConn(key, cfg)` → `checkConn` → `nats.CheckConnection`, which dials, measures the core NATS ping, counts the lobby KV's `players.*` entries via `nats.LobbyPlayerCount` and closes); results live in `connProbes` (a `probeResult` per key: `ok`, the full `msg`, `rtt`, `players`, `lobby`), `connProbing` is the key in flight. `connStatusLine` renders the selected key's state on the line under the list (LAN mode: under its button): "Connecting…", `✓ <server> · Core NATS ping <rtt> · <n> players online` (green; `playersText` words `no lobby yet` / `nobody online` / `1 player online`) or `✗ <error>` (red); each browser row also shows `probeSummary` inline — `…` while probing, `<rtt> · <n> online` (or `no lobby`) in green, `OFFLINE` in red. The page's panel is laid out at the exact `connPanelH` (326 dp) on both tabs — the browser list is the panel's `Flexed` filler with the status line pinned under it; LAN mode pins its check row to the bottom the same way — so the card, and Play, never shift when switching tabs. On Play, `submitLogin` resolves the page (`pickerConfig`: LAN tab → the embedded config; otherwise the selected entry → `NATSURL` or `NATSContext`, error when nothing is selected) and dispatches `doConnectAndLogin` (`lifecycle.go`): it first `disconnect()`s any connection left over from a previous attempt (e.g. a cancelled name collision), then runs `nats.Bootstrap` under a 15 s cap — errors land on the login screen for retry, success stores `a.nc/a.js/a.kv` (the App owns the connection — `teardown`/`DrainConn` drain it) and falls through into the normal `doLogin` flow. `quit()` (lobby → login) also `disconnect()`s, so the player always lands back on the full page and can switch servers. The default selection (`NewWithPicker`) is `--server` → `--context` (appended to the contexts if undiscovered) → the first favorite → the CLI's current context → the first context. `App` state: `needConn`/`connContexts`/`connSelected`/`connCtxURLs`/`connCfg`/`favSave`/`lanIP` (immutable after construction), `connProbes`/`connProbing` (mu-guarded), and the `connTab`/`connTabBtns`/`connSel`/`connProbeQueued`/`favorites`/`connSecClosed`/`connSecBtns`/`connRowBtns`/`connDelBtns`/`connBrowserLst`/`connAddOpen`/`connAddScroll`/`connAddRowBtn`/`connAddLabelEd`/`connAddURLEd`/`connAddBtn`/`connAddCancel`/`connHostEd`/`connPortEd`/`connRefreshBtn`/`connCheckBtn` widget state (all UI-goroutine only).

**Login backdrop (`backdrop.go`).** Behind the card the window is filled with a piece of artwork — Team A's cyan board and Team B's magenta board, each split into its two players' halves, standing over the NATS "N" logo — embedded in the binary as `login-backdrop.jpg` (`go:embed`; a JPEG rather than a PNG to keep the binary small). `backdropImage` decodes it once (`sync.Once`, like the NATS logo) into a `paint.ImageOp` whose GPU texture Gio caches across frames; `loginBackdrop` draws it with `widget.Image{Fit: widget.Cover, Position: layout.Center}` — scaled to cover the window, cropping the sides or the top/bottom as the aspect ratio demands, never letterboxing — under a light translucent `colBg` veil (`backdropDim`, 0.28) so the title and tagline stay readable over it (the card covers the picture's middle, so the neon "Team A" / "Team B" titles and the logo's wings frame it). One textured quad per frame, nothing scheduled: the login screen idles at zero CPU. The card itself sits on a 93 %-opaque `colBg` panel, so the boards show through it only faintly. A decoding failure leaves the plain background.

**LAN party mode (embedded NATS server).** The connection page's second tab (`connTab == connTabLAN`) makes the player the host. It explains itself in a line, then an "IP:" editor (`connHostEd`, pre-filled with the detected `App.lanIP`) and a "Port:" digits-only editor (`connPortEd`, pre-filled with `config.DefaultEmbeddedPort` = 4222); under them the `Your server's URL is nats://<ip>:<port>` line (`pickerAddr`: the entered address and port, each falling back to the detected IP / default port while its field is empty or mid-edit-invalid), the "Friends add it to their server browser's favorites · data in ./jetstream-data" hint, and the **Check embedded server** row (`connCheckRow` keyed `probeKeyLAN`). `pickerConfig` marks the config with `RunEmbedded`, the parsed `EmbeddedHost` (`pickerHost`: empty → "" = auto-detect at connect time; otherwise an IP — IPv6 literals included — or host name, rejecting a pasted scheme/port with "enter a valid IP address or host name") and the parsed `EmbeddedPort` (`pickerPort`: empty field → the default, otherwise a valid 1–65535 port or a login error), and `doConnectAndLogin` calls `ensureEmbeddedServer(cfg.EmbeddedHost, cfg.EmbeddedPort)` (lifecycle.go), which starts `nats.StartEmbeddedServer(config.EmbeddedStoreDir, port)` — an in-process JetStream-enabled `nats-server` (default account, no auth) on `0.0.0.0:<port>` with its storage in `./jetstream-data` — records the shareable address `net.JoinHostPort(host, port)` (host = the tab's IP, else `nats.LanIP()`) in `embAddr`, and rewrites the config to `nats://<addr>` so the normal `Bootstrap` path provisions and connects through the same address other players dial. Because the IP only picks the advertised/dialed address and never the bind, changing it needs no server restart (unlike the port). A running server is reused when the requested port matches; asking for a different port shuts it down and restarts it there. Not loopback: a foreign nats-server holding a `127.0.0.1:<port>`-specific bind would intercept a loopback dial even though the embedded `0.0.0.0` bind succeeded; as belt and braces, after connecting the app compares `nc.ConnectedServerId()` with the embedded server's `ID()` and fails the login with a clear "port already in use by another NATS server" error on a mismatch — `checkConn` applies the same check, so the embedded probe reports `✓ serving on <addr> · Core NATS ping <rtt> · <players>` only for OUR server. While the current connection is to the embedded server (`usingEmbedded`, cleared by `disconnect`), the lobby header shows `YOUR SERVER'S URL IS nats://<ip>:<port> — share this address so others can join you` (`embeddedAddr`); other players add that address to their server browser's favorites. The server outlives lobby exits (quit keeps it up for connected friends) and is shut down in `teardown` when the window closes. `App` state: `embSrv`/`embAddr`/`usingEmbedded` (mu-guarded).

**Button styling.** Primary actions (Join, Ready, the wizard's Next/Create game, Send, the add-favorite form's Add) use the `primaryButton(gtx, btn, label)` helper (`lobby.go`): the filled-accent `material.Button` restyled by `pixelize` (square corners, pixel face, 11 sp) over a `hardShadow`. The one action a screen is *waiting on* — the lobby's "Create a new game", the game's initial ready-up click, and the login screen's Play (`bigAttractButton`: the same treatment at marquee scale — 16 sp label, taller inset, stretched across the card; both share `attractStyled`) — upgrades to `attractButton(gtx, btn, label)`: the same primary chrome plus a bas-relief bevel (translucent white top/left, black bottom/right — the board cells' lit-from-upper-left block shading applied to a button) and a chunky 45°-stepped white glint band that sweeps across the face for `attractSweep` (600 ms) once every `attractPeriod` (3 s), arcade attract-screen style. The sweep is phase-locked to the wall clock (`gtx.Now` mod period, normalized for the zero `Now` of headless tests) so multiple attract buttons flash in unison; while sweeping it drives frames via `a.invalidate()`, and while idle it schedules exactly one wake-up at the next sweep (`gtx.Execute(op.InvalidateCmd{At: …})`) instead of redrawing every frame. Once the player has readied up, the (now "CLICK IF NOT READY ANYMORE") toggle drops back to plain `primaryButton` — standing down is not an action the UI should advertise. Non-primary actions — Spectate, the lobby's Disconnect (`quitBtn` → `quit()`), Back to Lobby, and the login collision-dialog Cancel — use the `secondaryButton(gtx, btn, label)` helper: an accent-colored (`colAccent`) pixel-face label and 2 dp accent border over the `colPanel` background, also on a hard shadow, so they read as clearly clickable instead of blending into the dark window background (a bare `colPanel` fill made them look disabled even though they worked). Destructive actions — the abandoned-game Delete and its "Yes, delete" confirmation — use `dangerButton(gtx, btn, label)`: the same secondary chrome but with the error red (`colErr`) label and border, so they cannot be mistaken for Join/Spectate. The spectator's HUD keeps the "Back to Lobby" button (the same `backBtn` → `leaveCurrentGame` path as a player), so a spectator can always return to the lobby.

**Leave & rejoin (`game.go` backBtn / `lifecycle.go` `leaveCurrentGame`).** "Back to Lobby" no longer just tears the screen down. For a player in an IN-PROGRESS game (`started && !gameOver`) the click sets `App.confirmLeave` and `layoutGame` stacks a scrim + `confirmLeaveOverlay` modal — "LEAVE GAME? … you can rejoin it from the lobby" with **Yes, leave** (`dangerButton` `leaveYesBtn`) / **No, keep playing** (`leaveNoBtn`); any other state leaves directly. `leaveCurrentGame` then: clears the READY mark if set (`lobby.SetReady(gameID, false)` — leaving pre-start revokes readiness so the countdown can't fire without you), calls `lobby.LeaveGame` (presence back to In Lobby) ONLY when the game is finished/gone or we hold no seat (`gameAlive`/`rosterHas` helpers), and finally `returnToLobby` (which, like `startGameScreen`, also resets `confirmLeave`). While a live seat is held the lobby row shows the game from the player's point of view — the status token reads **joined** (created/starting) or **playing** (in progress), in green — and the Join/Spectate buttons are replaced by a single **Rejoin** (`gameRow`'s `rejoin` path, reusing `btns.join` → `joinGame`, whose already-seated `JoinGame` branch returns the same `PlayerIdx`/team; the engine replays the stream to the live board). `startGameScreen` seeds `myReady` from the roster on (re)join so the READY button label is roster-accurate.

**Abandoned-game deletion (lobby).** `layoutLobby` reads `lb.AbandonedGames()` each frame (the set is maintained by the lobby package's minute-interval `runAbandonedChecker` — see Section on `internal/lobby`) and passes each row's flag into `gameRow(gtx, g, abandoned)`. An abandoned row appends a red `· abandoned` tag to its info line and shows a `dangerButton` **Delete** after Join/Spectate (`gameRowBtns` gains `del`/`delYes`/`delNo` Clickables in `app.go`). Clicking Delete stores the game in `App.confirmDeleteID` (UI-goroutine only); while it matches, the row's action buttons are replaced by a confirmation rendered on its **own line under the game info** (a vertical flex — beside the info it would squeeze the `· abandoned` tag): "Are you sure you want to delete this game?" in `colErr` with **Yes, delete** (`dangerButton`) and **Cancel** (`secondaryButton`) — so a stray click can't join or delete. Confirming clears `confirmDeleteID` and dispatches `go a.deleteGame(id)` (`lifecycle.go`) → `lobby.DeleteGame`, which deletes the game stream, purges the game's chat from the shared chat stream, and deletes the KV listing (removing the row everywhere).

Two small packages support the front end:
- **`internal/render`** — the single source of truth for cell/board appearance (piece/player colors, blend math) for the native UI. Exposes a single decision function, `CellStyle`, plus the RGBA surface (`CellAppearance` — fill, outline, outline width, and the `Bevel` flag that gates the drawer's 8-bit shading on filled cells — `PlayerColorRGBA`, `PlayerColorHex`), so every render path (own board, opponent boards, spectator view) draws from one visual model.
- **`internal/archive`** — `ArchiveAndCleanup(ctx, js, kv, eng, lb, gamePlayers)`, wired as `engine.OnGameFinished`; records the finished game to the archive stream and tears down its NATS resources. Before deleting the game stream it calls `buildBoardPictures`, which reads the latest message per cell (`FetchPlayfieldState`) for every board in the game — one for cooperative, one per player for competitive, one per team for teams — and stores them sparsely (non-empty cells only) as `ArchiveRecord.Boards`, so the end-of-game playfield survives the stream deletion. It also runs the **replay archive** step (`replay.go`, `maybeArchiveReplay`) right after the record publish: every record is read straight off the archive stream (`fetchArchiveRecords`) and the keep set computed (`config.ReplayKeepSet`: each bucket's top `config.ReplayTopN` (10) by `RankBefore` ∪ the `config.ReplayRecentN` (25) most recent finishes by `RecentBefore`). The replays of every listed game with a visible record that is no longer in the keep set are purged FIRST (`PurgeReplay`, one subject-filtered purge each), then the finishing game's ENTIRE stream — it is always kept, being the most recent — is copied into the shared `JETRIS_REPLAY` stream under the game's `jetris.replay.<id>.` prefix (`CopyGameToReplayStream`, see `internal/nats` `streams.go`), its marker published last: the lobby follows markers, so a marker means "replay ready, displaced ones gone". The copy runs after the record publish (it must not delay the history) and before the game stream deletion; it is best-effort — a failed copy is purged again and the game archives without a replay. (The reference agent `golang-mk1` implements the same step in its own `replay.go`, so agent-archived games — including agents-only showcases — get replays too; guide §5 step 6.)

**History list ordering & summary line.** The `GAME HISTORY` list is sorted by `sortedArchives` (`lobby.go`) using the shared `config.ArchiveRecord.RankBefore` ordering — headline score descending (`HeadlineScore` — co-op `TotalScore`, best entry of `TeamScores` for teams, best player score for competitive), shorter `Duration` breaking a score tie, then `FinishedAt` (newest first), then the game ID for a total order. It is the SAME ordering the replay archiver's top-N cut uses, so under "By score" the replay-carrying games are exactly the top of each group. Each summary line (`archiveLine`) is prefixed by `archiveWhen`: the start date/time in the viewer's local timezone (`2006-01-02 15:04 MST` format) and the duration rounded to the second (e.g. `2026-07-06 14:03 PDT · 4m32s · co-op · …`); records without timestamps skip the prefix and show just the mode-specific part (`archiveModeLine`).

**History table.** `GAME HISTORY` renders as an arcade high-score table (`lobbyRight`): a pixel-font column header (`archiveHistoryHeader`, **SCORE · TIME · MODE · PLAYERS** aligned to the shared `histScoreW`/`histTimeW`/`histModeW` widths via `fixedCol`) over one `archiveHistoryRow` per game (its cells in `archiveHistoryCells`), each closed by an `hrule` (colBorder) so a wrapping PLAYERS column can't blur into the next game. A game in its replay bucket's all-time top 10 (`lobby.IsTopRanked`, i.e. `config.ReplayTopRankedCut` over every record, not just the filtered rows — and only in buckets holding MORE than ten games, since in a younger bucket every game is trivially top-ten and the mark would light up the whole list) is marked: a 4 dp gold bar down the row's left edge (laid via an `op.Record` macro so the bar matches the row's measured height — deliberately a marker, not a row tint, which reads as a selection) and a gold pixel `TOP 10` tag in its score cell. Per row: `archiveScoreCell` (the headline `archiveScore` in gold pixel numerals — the largest figure — over `archiveHeadlineLevel`, then the `TOP 10` tag when ranked), `archiveTimeCell` (`archiveDuration` over the start date), `archiveModeCell` (mode name over player count / `NvN`), and the flexed `archivePlayersCell` — `archiveRosterLines` builds the winner(s)-first lines (a trophy + gold name via `competitiveRosterLines`/`teamRosterLines`, the rest muted; `coopRosterLines` just lists the shared roster). Each row carries an accent-bordered **"View board"** button on the right (`viewBoardButton`, one `archiveBtns` Clickable per row) so it is obvious the finished game can be opened. Clicking it opens `screenArchive` (`archive_view.go`), which rebuilds each saved `BoardPicture` into an `engine.BoardSnapshot` (`boardSnapshotFromPicture`) and redraws it with the same `boardWidget` used live — cooperative shows the single wide board, competitive a board per player labeled by ID in player color, teams the two team boards (the multi-board strip is laid by `scrollableBoards`, so it stays centered while it fits and scrolls horizontally with a scrollbar when the boards are together wider than the window). To the **left** of the boards a player roster (`archiveRoster`) lists everyone in their board color with the winner(s) highlighted (a trophy and a gold name): competitive players are colored by the same sorted-by-PlayerID index the boards use (`rosterCompetitive`, survivors flagged winners), teams grouped under color-matched TEAM A / TEAM B headers with the winning team in gold (`rosterTeams`), and cooperative players list plainly under a PLAYERS header — one shared board, so no per-player color and no winner (`rosterCoop`); each line is an `archivePlayerRow` (swatch + name + winner trophy). A **GAME CHAT** panel (`archiveChatPanel`, a 320 dp bordered `archiveChatList` beside the centered boards) replays the preserved conversation (`ArchiveRecord.Chat`) — `archiveChatLine` formats each line with local wall-clock time and the "(spec)" spectator marker. The panel is always present: a record with no chat (a silent game, or one archived before the field existed) shows "No chat was recorded for this game." rather than silently vanishing, so its absence is never mistaken for a missing feature. A `secondaryButton` "Back to Lobby" returns to the lobby.

**Game replay** (`replay_view.go`). A history row whose game has a replay archive — `lobby.HasReplay(gameID)`, flipped on by the lobby's `runReplayMarkerConsumer` the moment the game's copy-complete marker lands, with `runReplayRefresher` re-listing the markers (`ListReplayGameIDs`) at start and after each marker to drop displaced ones — grows a green **Replay** button (`smallActionButton`, one `replayBtns` Clickable per row) beside View board. Clicking it opens a modal speed-choice dialog (`replayChoiceOverlay`, `App.replayChoice`, dispatched with the other lobby overlays): **Replay at original speed** / **Replay as fast as possible** / Cancel. Either choice opens `screenReplay` and starts a consumer session (`runReplaySession`, a goroutine): the marker lookup (`GetReplayMarker`) both proves the replay still exists and pins its final sequence, then one ordered consumer on the game's slice of the shared replay stream (filter `jetris.replay.<id>.>`) delivers the copy. At original speed each message is applied on the recorded schedule by `replayPacer`: the original timestamps ride the `Jetris-Ts` header (the shared stream stamps copy-time timestamps), the first paced message anchors recorded time to the wall clock and every later one is due at anchor + offset — an absolute schedule that cannot drift — while messages recorded before `StartedAt` minus a 2 s clock-skew guard (`replayStartGuard`) fast-forward, skipping the pre-game roster/lobby wait; fast mode applies messages as delivered. A **Pause / Resume** button (`replayPauseBtn`) flips the session's `replayGate`; every message passes `replayWait` first, which holds it while paused — mid-gap at original speed, the rest of the gap — and on resume shifts the pacer's anchor by the paused span (`replayPacer.shift`) so playback continues where it stopped rather than racing to catch up; the status line reads PAUSED meanwhile, and a finished replay shows no pause control. The session folds every delivered cell message into the mode-appropriate board set (`newReplayView`: one shared board for coop, one per sorted player ID for competitive matching the archive coloring, one per team for teams; `applyReplayCell` demuxes by the subject's `playfield`/`player`/`team` token — the copied subjects keep the game-stream tail, so the demux is prefix-agnostic — and skips registers, meta, events, and countdown), and the screen redraws the boards live via the shared `boardsStrip` (the labeled-board strip `archiveBoards` also uses) with a status line — REPLAYING · ORIGINAL SPEED / FAST in green, REPLAY COMPLETE in gold once the game's marker arrives, or an error in red. **The ending is a reveal** (`replay_winner.go`): while the game plays back the summary line (`replaySummary` → `spansLine`, one colored run per player) names the players in their board colors — teams grouped under their color-matched TEAM A / TEAM B — with no scores and no trophies, so nothing spoils the outcome; when the marker lands (`replayView.doneAt`, the show's clock) `crownWinners` decorates the strip through `labeledBoard`'s optional `labelCol`/`emph`/`wrap` fields: the winning board(s) — `replayWinners`: the winning team's, the surviving player's, or the shared cooperative board — get a gold bold-italic label and the winner show (`crownBoard` → `drawWinnerShow`: the board's frame pulses in the tier's aura color, a WINNER / WINNERS / GAME OVER pixel banner rises out of the well with `easeOutBack` and floats on a slow bob under a pixel-art trophy (`trophyArt`, `drawTrophy`) holding the rank's prize piece (`prizePiece`: the internet's worst-to-best ranking of the seven tetrominoes — the I for #1, then T, L, J, O, S and the Z from #21 down — painted by `drawPrizePiece` in board cell styling, standing in the cup's mouth) and a rank caption), the beaten boards get `knockoutBoard` (a wash plus the live view's OUT chip), the summary line's winners go gold in bold italic with a 🏆 and every score appears, and the status line's verdict (`replayVerdict`: TEAM A WINS! / ALICE WINS! / DRAW / FINAL SCORE n) is set in `pixelEmph` — bold italic synthesized for the pixel face, which has no such variants, by a shear plus a one-pixel double strike. The trophy is graded by the game's standing in its replay bucket's all-time ranking (`config.ReplayRank`, computed in `startReplay` against `lobby.Archives()` — the same "By score" order behind the history's TOP 10 mark; `replayView.rank`/`of`): `trophyTierFor` makes the bucket's best game LEGENDARY (gold with a holographic rainbow shine sweep, a hue-cycling highlight, halo rings and fourteen sparkles), the top three EPIC (gold in a breathing purple glow with sparkles), the top ten RARE (silver with a white shine sweep) and any other game a plain bronze cup, each captioned `#rank · TIER` (or `#rank OF n`). Every frame of the show is a pure function of `gtx.Now` against `doneAt` (the fireworks idiom — `layoutReplay` keeps invalidating while the show is up); a draw crowns nobody. `screens_snapshot_test.go`'s `replay_done` renders the four tiers, `replay_winner_test.go` pins the tiers, the per-mode winners and verdicts, and the summary's no-spoilers rule. A displacement purge mid-watch stops deliveries without an error, so a quiet channel (`replayIdleCheck`, 10 s) re-checks the marker: gone means "this game's replay was just removed". "Back to Lobby" (`closeReplay`) cancels the session.

**Cell appearance — single source of truth.** Every cell is drawn with an explicit fill color and outline computed by `internal/render`. Piece fills come from a piece-color table composited over the board background via `blend(fg, bg, alpha)` (active ≈0.9, locked ≈0.7, adversarial ≈0.8). Outlines: own active → white; spectator (`localPlayerIdx < 0`) → per-player color on active/locked cells; other player's active piece in a player view → grid line; locked non-adversarial → per-player color when `showOutline` (suppressed to the grid line on compact opponent boards). Because appearance is computed in one package, the visual model stays consistent across own/spectator/opponent renders. In competitive mode the UI distinguishes own-field updates (`UpdatePlayfield`) from opponent updates (`UpdateOpponentField`, keyed by `OpponentID`) and redraws the corresponding sidebar board. In cooperative mode the single wide playfield (playerCount × StandardWidth columns) is drawn directly — already the correct width, so there is no concatenation or visual separator between player sections.

**Ready/countdown flow:** While waiting for the game to start, each player sees the list of players with their ready state (green checkmark or red cross). Players toggle their ready state via the button, which reads "CLICK WHEN READY TO PLAY" (in the shiny `attractButton` chrome) / "CLICK IF NOT READY ANYMORE" (plain `primaryButton`) (→ `lobby.ToggleReady`). When ALL players are ready, the button and player list are replaced by a 5-second countdown (5...4...3...2...1...GO!), centered over the player's own playfield (the overlay Stack wraps the board widget inside `boardCol`, not the whole board area — otherwise the NEXT well and centering whitespace would drift the numeral off the playfield's middle). During the countdown, players cannot change their ready state. After the countdown, the game transitions to `in_progress` and pieces begin to spawn.

The game over overlay is shown on `UpdateGameOver`: in cooperative mode any top-out ends the game for all; in competitive mode it shows "YOU WON!"/"YOU LOST" once the player is eliminated or is the last standing. Below the title/verdict, `gameOverBox` shows the final score in gold above the "Back to Lobby" button: `Score: N (level L)` (the shared total) for cooperative, `Your score: N (level L)` for competitive, and both team totals with the player's own team first (`TEAM A 42 (lvl 3) · TEAM B 17 (lvl 1)`) for teams — `gameOverBox` takes the local team index (`eng.TeamIdx()`) for this. The game screen hides controls and the ready button for spectators, showing "Spectating" as the player status instead.

**Victory fireworks** (`fireworks.go`). When `UpdateGameOver{Won: true}` arrives for a competitive or teams game — or a cooperative game ends having strictly beaten the best archived co-op `TotalScore` for the same seat count (`beatsCoopBest` → pure `coopScoreIsRecord`, `bridge.go`: scans `lobby.Archives()` for `ModeCooperative` records with the same `PlayerCount`, excluding the game's own `GameID` since its archive may already have round-tripped; zero scores never count; resolved before `pumpEngine` takes `a.mu` because `getLobby` locks it) — `pumpEngine` rolls a `fireworksShow` (`newFireworksShow`, plain `math/rand` — the deterministic-RNG rules apply to the engine, not the UI) and stores it on the App; `startGameScreen`/`returnToLobby` reset it. `layoutGame` stacks `fireworksOverlay` over the whole game screen (`layout.Stack`, paint-only ops — no `event.Op` — so input still reaches the widgets underneath) and keeps calling `invalidate()` while `fireworksShow.active(gtx.Now)`, following the countdown/CAS-flash idiom: each frame is a pure function of `gtx.Now`, no per-frame state is mutated. Once started the show never ends on its own: `active` stays true and the overlay draws at elapsed time modulo the ~8 s `cycle`, replaying the same choreography until the show is dropped. One cycle is 12 rockets staggered ~420 ms apart: each rises from the bottom edge as a warm-white streak (`drawRocketStreak`, ease-out cubic) to a random apex, then explodes into a logo burst (`drawLogoBurst`, 2.4 s) — the NATS "N" for nine rockets in ten, the **Synadia "Symbol"** (embedded `synadia-icon.png`, the official mark from synadia.com/about/brand — the white "S" swirl on the emerald rounded square) for the tenth — with a floor of one Synadia rocket per show, forced after the roll if none came up, since a show without one would loop forever without it (~28% chance over 12 rockets); there is no other burst kind. A burst has three phases: the particles pop out (easeOutBack over the first 25%) into a small replica of the logo, the intact logo holds while drifting down slightly, and at the halfway point (`fwScatterStart`) it splits into its small squares and blows apart — each block shrinks inside its grid cell (seams appear, making the break-up visible immediately) and flings outward along its own radial-from-center scatter velocity (ease-out spread, gravity droop), the debris shrinking as it flies rather than dimming in place, with an alpha fade only over the final quarter of the scatter. Each rocket also rolls a scatter tint at show creation: about two bursts in three recolor their flying blocks (`lerpColor` over the first 60% of the scatter) toward one traditional fireworks color from `fwBurstPalette` — gold, red, green, blue, purple, or silver — while the rest keep the logo’s own colors. The particles come from `fwLogoPoints` / `fwSynadiaPoints` (both via `sampleLogoPoints`, with a NATS fallback if the Synadia PNG fails to decode), which sample the embedded `nats-icon.png` / `synadia-icon.png` once on a 22×22 grid keeping one particle per mostly-opaque pixel with its color — the four brand-color quadrants and the white "N", the emerald square and white "S" — so the burst is the real logo, not an approximation; each particle also carries its precomputed scatter velocity (radial direction, random direction at the very center, 0.6–1.4× speed jitter from a fixed-seed RNG). Teams re-emits `Won: true` to already-eliminated members of the winning team, so their screens celebrate too; a co-op record lights up every crew member's screen (each member's engine emits the shared game over); spectators get no fireworks, and a cooperative game that doesn't set a record ends without them.

**Teams mode UI.** In the lobby, the create-game form has a third mode radio ("teams"); for teams the count editor means players **per team** (its label flips to "Per team:") and `createGame` converts it to `(playerCount, teamSize)` for `lobby.CreateGame`. Each game row (`gameRow`) shows per-team rosters ("A: alice ✓ · B: bob") and **two** join buttons — "Join A (n/size)" / "Join B (n/size)" (`teamJoinButton`, `teamName` helpers; each button is hidden when its team is full; `gameRowBtns` gains `joinA`/`joinB` Clickables in `app.go`). `joinGame(gameID, team)` passes the resulting `JoinResult{PlayerIdx, Team, TeamSlot}` into `engine.New`; `spectateGame` passes `0,0`. The archive history line (`archiveLine`) renders "teams · A 🏆 42 (lvl 3) alice, bob · B 17 (lvl 1) carol, dave" using `WinningTeam` and the record's `TeamScores`/`TeamLevels` (stats omitted for older records without them); coop lines show `total N (lvl L)` and competitive lines show each player's `score (lvl L)`. On the game screen the HUD label reads "Teams · TEAM A/B", the single SCORE stat is replaced by a live per-team scoreboard — one `TEAM A` / `TEAM B` stat per team fed by `UpdateTeamStats` (own team's value in the accent color; spectators see each team's `score · lvl N` inline and no single SCORE/LEVEL stat), the legend groups players under TEAM A / TEAM B headers (swatch colors stay keyed by the global roster index), eliminated players get an "(out)" marker, and the opponent sidebar — fed by `OpponentSnapshots()` under `TeamBoardKey` — is shown labeled "OPPOSING TEAM". `gameOverBox` takes the game view and shows an interim "YOU'RE OUT / Your team plays on" while the player is eliminated mid-game, then "YOUR TEAM WON!"/"YOUR TEAM LOST" once the outcome is decided; spectators get `spectatorTeamBoards`, which renders both team boards side by side — each labeled in its team's color and with its empty squares and grid lines lightly tinted toward it (`boardFX.tint`, `boardTintFill`/`boardTintGrid` in `board.go`: Team A cyan, Team B magenta; pieces keep their own palette) — plus, once the game is decided, the spectator reveal (`spectator_reveal.go`: the winner show on the winning team's board, the OUT wash on the beaten one, and `spectatorResultBox` — winning team + final scores — beside the boards).

---

## 13. Event Channel Contracts

All cross-package communication uses buffered Go channels. The buffer size is chosen to absorb brief bursts without blocking the sender goroutine.

| Channel | Direction | Buffer | Notes |
|---------|-----------|--------|-------|
| `engine.Updates` | engine → front end | 64 | High-frequency during play (gravity ticks, every cell update). Consumed by the native bridge (`pumpEngine`). Dropping updates here is preferable to blocking the engine. If the channel is full the engine drops the update — the next update will correct the display. |
| `lobby.Updates` | lobby → front end | 16 | Lower frequency. Lobby changes are infrequent relative to game updates. |
| `engine.moves` (internal) | front end → engine | 8 | Player move requests dispatched onto the engine's internal moves channel (`runInput` reads it). Inputs are **serialized and buffered**: `runInput` processes them one at a time and each move's publish blocks on its batch commit ack (then applies the write-through) before the next move is dequeued, so a player never has two input batches in flight — a move issued while the previous one is still awaiting its ack waits in this buffer. The non-blocking send drops excess input rather than blocking the UI goroutine if a player outruns the ack round-trip by more than the buffer depth (not reached at human input rates). The engine keeps a FIFO **mirror** of this buffer (`bufferedMoves`, guarded by `bufferedMu`): `dispatch` appends on a successful enqueue, `runInput` pops via `popBufferedMove` the moment it dequeues a move, and `Engine.BufferedMoves()` exposes a copy. The UI renders it as the animated **MOVE BUFFER** chip strip under the player's board (`bufferedMovesStrip` in `nativeui/controls.go`: eight big slots filling with gold pixel-glyph chips, pop-in on enqueue, chase glow while non-empty, `+N` overflow — very visible when high RTT makes inputs queue); an `UpdateBufferedMoves` event triggers the redraw. |

Channels are never closed by the sender — they are abandoned when the owning goroutine exits via context cancellation. Receivers must always select on both the channel and `ctx.Done()`.

---

## 14. Bootstrap Sequence

The following steps happen in order at startup. Steps that can fail cause the application to exit with a clear error message.

```
1.  Parse CLI flags → config.Config (no connection is made at startup;
    --server/--context only seed the server browser's selection)
2.  natspkg.ListContexts() → context names + selected (warn-only on error);
    prefs.LoadFavorites() → the server browser's bookmarks (defaults on a
    fresh install, warn-only on error)
3.  nativeui.NewWithPicker(cfg, names, selected, favorites); runNative: a
    goroutine calls App.Run (opens the Gio window); app.Main() owns the OS
    main thread → the native window shows the single combined login screen:
    name entry + the two-tab CONNECT TO page (clicking a browser row — or its ↻ —
    dials that server, core-NATS-pings it, counts its lobby's players and
    closes, no side effects; LAN mode's "Check embedded server" starts the
    embedded server before pinging it)
4.  Player enters name, selects a server in the browser (a favorite URL or
    a context) or switches to the LAN-mode tab, and hits Play:
    validate (config.ValidatePlayerName), then doConnectAndLogin:
    a. disconnect() any connection left from a previous attempt, then
       natspkg.Bootstrap for the chosen context/URL (15 s cap): connect
       (ConnectURL when a URL, else natscontext.Connect) then
       EnsureChatStream, EnsureLobbyKV, EnsureArchiveStream — on failure
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
5.  Disconnect (lobby; `quit()`) → stop the lobby, disconnect(), back to the combined login
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
| Replay-stream lister (`runReplayRefresher`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Replay marker consumer (`runReplayMarkerConsumer`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Lobby presence heartbeat (`runHeartbeat`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Abandoned-game checker (`runAbandonedChecker`) | `lobby.Lobby` | `lobby.Start()` | ctx cancel |
| Own-board consumer (`runConsumer`, cells + registers) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Events consumer (`runEventsConsumer`, filter `…events.>`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Garbage ledger bump (`bumpLedger`, one short-lived goroutine per victim board) | `engine.Engine` | `handleLockIn` → `bumpVictimLedgers` after a committed clear (competitive/teams) | CAS-add lands (or bounded retries exhausted) / ctx cancel |
| Meta consumer (`runMetaConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Countdown consumer (`runCountdownConsumer`) | `engine.Engine` | `engine.Start()` | ctx cancel |
| Input + gravity loop (`runInput`) | `engine.Engine` | `engine.Start()` (ModePlayer only) | ctx cancel |
| Roster consumer (`runRosterConsumer`) | `engine.Engine` | `engine.Start()` (competitive only — teams does not run it) | ctx cancel |
| Per-opponent cells consumer (`runConsumer`) | `engine.Engine` | `startOpponentConsumer` per discovered opponent (competitive) | ctx cancel |
| Opposing-team board consumer (`runConsumer`) | `engine.Engine` | `startTeamBoardConsumer` from `engine.Start()` (teams only; spectators consume team 1 through it) | ctx cancel |
| Lobby/game update pumps (`pumpLobby` / `pumpEngine`) | native bridge (`nativeui`) | one per attached lobby/engine | ctx cancel |
| Replay session (`runReplaySession`) | `nativeui` | `startReplay` (the history Replay button's speed dialog) | last replayed message delivered / Back to Lobby (`closeReplay` cancel) / ctx cancel |

---

## 17. orbit.go Module Reference

All orbit.go modules are independently versioned. Import only the modules needed rather than the whole library.

| Module | Import path | Used in | Purpose in Jetris |
|--------|-------------|---------|-------------------|
| `natscontext` | `github.com/synadia-io/orbit.go/natscontext` | `internal/nats` | Connect using NATS CLI context files. Replaces raw URL + credential flags with a single context name, sharing config with the `nats` CLI tool. |
| `jetstreamext` | `github.com/synadia-io/orbit.go/jetstreamext` | `internal/nats` | Atomic batch publishing for move CAS operations. `GetLastMsgsFor` for instant playfield reconstruction on startup/reconnect (fetches the last message per cell subject; chunked via `GetLastMsgsUpToSeq` above 512 subjects to stay under the server's 1024-response cap). |

These are the only two orbit.go modules used (`natsext` comes in as an indirect dependency). `counters` and `natssysclient` are **not** dependencies of Jetris.

### Modules considered but not used

| Module | Reason not used |
|--------|----------------|
| `counters` | The cooperative score is a plain local `int` propagated via `EventLineClear` events (cumulative totals on per-sender subjects, deltas folded locally) — no server-side counter CRDT (and no `AllowMsgCounter` stream flag). |
| `natssysclient` | Cleanup detects orphaned streams with the plain JetStream `StreamNames` listing (`ListGameStreams`); no system-account `Jsz` query is needed. |
| `kvcodec` | Jetris KV keys are already NATS-compatible (no dots, spaces, or special chars). Values are plain JSON. No encoding layer needed. |
| `natsext` (RequestMany) | Jetris uses ordered consumers and direct publishes. Scatter-gather request/reply is not part of any game or lobby flow. (Present only as an indirect dependency.) |
| `pcgroups` | Jetris uses ordered consumers for strict in-order delivery per client. Partitioned consumer groups target parallel work-queue consumption patterns, which is not applicable here. |

---

## 18. Testing Strategy

### Unit tests (no NATS required)

- `internal/game` — all functions are pure and take no external dependencies. Full coverage of piece rotation (all SRS wall kicks), collision detection, line clear detection, cell serialisation, score and level calculation, gravity interval curve. `cascadeshrink_test.go` covers `ProjectShrinkCascade`: hold-position/dropped-into-place, minimal lift, the bottom-most-first cascading lift through stacked pieces, `topped` (a piece squeezed off the top), and `boardFull` (locked rows pushed past row 0). `teamshrink_test.go` keeps the `AdversarialRowCount` cases (bottom-anchored count, holes, stack above).
- `internal/rng` — verify determinism: two `Sequence` instances with the same seed produce identical output. Verify seek: `Piece(N)` equals the Nth output from sequential calls.
- `internal/config` — subject builder functions (including the teams cell-subject builders) and the team board dimension helpers produce correct values.

### Integration tests (require a NATS server)

- `internal/nats` — stream creation, KV operations, atomic batch publish happy path, CAS failure path, stream sealing, `FetchPlayfieldState` via `GetLastMsgsFor`. `publish_test.go` covers the gated-batch mechanics against a real server: a batch mixing all three `ExpectMode`s commits atomically, an `ExpectForSubject` carrier rejects the whole batch when the guarded foreign cell moves, an expectation about a subject the same batch already wrote is rejected by the server (the carriers-precede-writes rule), and `classifyPublishErr` maps the API error codes onto the sentinel errors. Tests use a local NATS context pointing at the test server so that `natscontext.Connect` is exercised end-to-end rather than bypassed.
- `internal/engine` — start an engine against a real NATS server with a test game stream. Submit moves and verify the playfield reaches the expected state. Simulate CAS failure by publishing a conflicting update from a second client. Verify the `FetchPlayfieldState` snapshot correctly seeds `LastSeq` before the ordered consumer starts. Verify cooperative score deltas propagated via `EventLineClear` converge to the same local total across two engine instances. `garbage_test.go` covers the ledger protocol end-to-end: the full attack path (clear → CAS-add → gated application, `TestCompetitiveRaiseLedgerFlow`), simultaneous attackers' totals **summing** on every victim register (the high-RTT double-clear the old event path collapsed, `TestCompetitiveSimultaneousAttacksSum`), a raise cascading a hovering piece off the top and eliminating its owner (`TestShrinkCascadeTopsOutSqueezedPlayer`), rows owed before an engine starts being reconciled from the Start snapshot (`TestLateJoinAppliesOwedGarbage`), and spectators/eliminated players never applying (`TestSpectatorNeverAppliesGarbage`). `gatedclear_test.go` covers the gated clear: the collapse diffs the full row range headroom included, and — via the `testHookBeforeGatedCommit` seam — a stale gate atomically rejecting the whole batch and the recompute converging. `teams_test.go` covers teams mode end-to-end: garbage is applied to the target team's shared board **exactly once** despite racing teammates, and the elimination/team-win flow (eliminated player spectates while the team plays on; whole-team elimination flips every winning-team member to `Won: true`).
- `internal/lobby` — create/join/leave game operations, presence heartbeat expiry, KV watcher delivery. `teamjoin_test.go` covers the teams join CAS loop: team capacity (`ErrTeamFull`), atomic `TeamSlot` assignment under concurrent joins, and the both-teams-full → `starting` transition. `abandoned_test.go` covers the abandonment rules (`isAbandoned` takes `now` as a parameter precisely so tests inject a future time instead of waiting out the timeouts, plus the deleted-stream case and a `checkAbandoned` end-to-end pass) and `DeleteGame`'s full teardown (stream gone, KV listing gone, game chat purged, lobby chat untouched, idempotent re-delete).
- `internal/cleanup` — seed a NATS server with stale game streams in various states and verify cleanup produces the correct outcomes, including orphaned-stream deletion (via the `StreamNames` listing) when KV entries are missing.

The `internal/testutil` package (`nats.go`) provides helpers for spinning up an **embedded** NATS server for integration tests. Tests run against that real server rather than mocking NATS behind interfaces.

### Visual snapshots (opt-in, need a GPU)

`internal/nativeui` has three snapshot suites, all skipped unless `FW_SNAPSHOT_DIR` is set: `TestPickerSnapshots` (the login connection page: the server browser with probe readouts, the browser with a collapsed section and the add-favorite form open, and the LAN-mode tab), `TestScreenSnapshots` (login/lobby/game/archive screens, the game screen with a populated NATS message strip showing its transaction tints, plus a hand-built sample board — the 8-bit look verification), and `TestCaptureREADMEScreenshots` (renders the README's screenshots from a **real** 2v2 teams game running against an embedded JetStream server — four player engines plus a spectator, prefilled stacks, live gravity — at 2x resolution via a headless GPU window).

### End-to-end

Two engine instances running against a shared NATS server, simulating a competitive game. Assert that line clears on one side advance the other board's garbage register and land as applied garbage rows, that the CAS mechanism correctly serialises simultaneous moves, and that the archive sequence runs correctly at game end (record published, then the game stream deleted — normal game end deletes the stream rather than sealing it).

---

## 19. Design Decision Log

Decisions settled during design review, recorded here for future reference.

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | Competitive playfield topology | Player-scoped cell subjects within one shared stream (`jetris.game.<id>.player.<pid>.playfield.cell.<row>.<col>`) | One stream per game keeps lifecycle management simple. Player-scoped subjects provide full isolation within it. |
| 2 | Lock-in detection | Implicit — engine scans the playfield state for the `Active→Occupied` transition after each cell message | No extra message; lock-in is definitionally visible in the cell data that would be fetched anyway on rejoin. |
| 3 | Line-clear row shift publisher | Client whose piece caused the lock-in | Avoids a first-CAS-wins race on a large batch; the publisher has the most current local state. |
| 4 | Garbage attack delivery (competitive/teams) | An attack is never an event: the clearing player CAS-adds the victim board's cumulative **garbage register** (`…playfield.garbage`); the victim applies the deficit against its **txn register** (`…playfield.txn`) as a txn-gated transform on its own `runInput` | The victim still owns its cell subjects — the attacker only ever touches the register, so A's writes stay decoupled from B's CAS keys. Fire-and-forget events from near-simultaneous clears race each other — exactly the high-RTT case that matters most; a cumulative register makes simultaneous attacks SUM and the deficit recoverable from any snapshot (late join, reconnect, replay). |
| 5 | Cell payload encoding | JSON (one `Cell` document per message; empty cell → `{}`, the vacate payload) | Simpler to implement and debug with `nats` CLI. Cell update rate is low enough that JSON overhead is not a concern. |
| 6 | Startup consumer start point | `max(snapshot seqs)+1` — the snapshot covers all cells plus the board's garbage/txn registers | Avoids reprocessing the entire stream history on every join/reconnect. The gap in other subjects (at most a few milliseconds of game time) is acceptable; the board snapshot reflects any clears or raises in that window, and the registers ride in the same snapshot, so any garbage still owed is applied before play (nothing is lost to the gap). |
| 7 | Lobby map concurrency | `sync.RWMutex` on `Lobby.mu`, maps unexported, accessed via `Players()` / `Games()` snapshot methods | Straightforward, low-overhead, and makes the access pattern explicit without channel complexity. |
| 8 | Cooperative score propagation | Plain local score counter (`atomic.Int64`), propagated via `EventLineClear` events on the sender's per-kind subject (cumulative totals, deltas folded locally) | No server-side counter CRDT is needed; the event subjects the game already runs carry the totals, and folding deltas against a sender's cumulative totals survives retention trimming. The game stream sets `AllowAtomicPublish` and `AllowDirect` (not `AllowMsgCounter`). |
| 9 | Game ID format | UUID v4 with dashes (`550e8400-e29b-41d4-a716-446655440000`) | UUIDs are globally unique, collision-free, and NATS stream names allow dashes. |
| 10 | Game-over semantics | Cooperative: any top-out ends for all. Competitive: eliminated player becomes spectator; game continues until one player remains. | See `jetris-gameplays.md`. |
| 11 | HardDrop CAS behaviour | Destination computed once; competitive publishes the landing NoCAS, shared boards (coop/teams) via merge-retry (≤16). No recompute-and-retry-until-it-lands loop. | The landing is authoritative state, so NoCAS (competitive) or CAS+merge (shared boards, to protect the other players' cells) is the right tool — not an unbounded CAS retry. The clear a drop completes is a separate publish: gated (competitive/teams) or merge-retry (coop). |
| 12 | Opponent display in competitive | Full live view via one ordered consumer per opponent's cell subjects | Provides the same real-time fidelity as the player's own field. The overhead of additional consumers is minimal (at most 3 opponents in a 4-player game). |
| 13 | `pieceIdx` recovery on join/reconnect | Store `PieceIdx uint64` in `GameMeta`; locking engine CAS-updates it after each lock-in | `FetchGameMeta` gives any joining engine the current piece index in one round trip. No stream replay needed. |
| 14 | Cooperative playfield topology | Single shared playfield of width `playerCount × StandardWidth`; cell subjects carry no player token (shared board) | Both players' pieces coexist on one wide board. `Cell.PlayerIdx` in the payload distinguishes active pieces — player identity lives in the message, not the subject, since coop never filters cells per player. One ordered consumer per engine. Line clears span the full width. UI renders the single playfield directly. |
| 15 | `GameMeta` payload | Fully specified in Section 4 with lifecycle, identity, RNG seed, and `PieceIdx` fields | Status uses string constants for readability in the `nats` CLI. `PieceIdx` enables fast startup without stream replay. |
| 16 | Real-time UI updates from JetStream | All UI data backed by JetStream uses ordered consumers pushing through the `Updates` channels — never polling or periodic refresh | The lobby runs consumers for KV (players/games), chat, and archives. The engine runs consumers for playfield cells, events, meta, and countdown. Any change in a JetStream stream or KV bucket is immediately pushed to the UI via the consumer → Updates channel → bridge pipeline. |
| 17 | Playfield storage granularity | One message per CELL (`playfield.cell.<row>.<col>`), not per row | A cell's last message is its current state. Per-cell CAS shrinks coop contention to same-cell writes only; every publish is a diff of only the changed cells (~4–8 messages per move); the `orderedCellKeys` category order (active → locked → empty) replaces the per-row `bottomFirst` flag with one rule that covers every write path. The CAS/write-through/merge-retry/ordered-consumer architecture is unchanged, just at cell granularity. |
| 18 | Teams playfield topology | Two team-scoped shared boards (`jetris.game.<id>.team.<t>.playfield.cell.<row>.<col>`), each the cooperative scheme at team scale | Within a team, teams mode IS cooperative — the coop shared-board machinery (`CanPlaceCoop`, merge-retry, `Cell.PlayerIdx` ownership) is reused verbatim via `sharedBoard()`. The team token in the subject keeps the two boards disjoint, so cross-team writes are impossible by construction; no roster consumer is needed (the roster is fixed pre-start). |
| 19 | Shrink on a shared team board | One transform for BOTH modes: `ProjectShrinkCascade` holds every falling piece in place, lifts a conflicted piece by the minimum rows, cascades lifts bottom-most-first through the pieces above, and reports `topped` (piece off the top → owner eliminated) and `boardFull` (locked rows past the top → whole board out). Exactly-once application comes from the txn gate, and stale-snapshot piece corruption is prevented by the batch's teammate guards (per-subject CAS on rewritten foreign-piece/headroom cells, `ExpectForSubject` carriers for untouched pieces) | The old "crush" semantics buried a teammate's piece under the risen stack; lifting is safe now because the gate makes racing appliers atomic (a loser's whole batch is rejected — no double-shift, no `AdversarialRowCount` deficit heuristic) and the guards make any teammate move committed since the snapshot reject the batch, so the recompute always re-projects from the piece's current reality. The txn record doubles as the in-band elimination signal: `Topped`/`Full` arrive before the vacating cells, so a victim's zero-active edge is classified before it fires. |
| 20 | Teams game-over semantics | A topped-out player vacates their piece (a txn-gated transform, so a racing garbage application can't resurrect it from a stale snapshot) and spectates while their team plays on; a team loses when ALL members topped out; every member of the other team (eliminated included) wins. Decided once per engine (`teamOutcomeDone`) off the ordered event stream | Per-player elimination keeps the shared board live for the teammates; the per-kind, per-player event subjects still share one totally-ordered stream, so every engine reaches the same verdict without coordination — and a player's single game_over can never be trimmed by other traffic. See `jetris-gameplays.md`. |
| 23 | Roster overfill / stale invitations | `JoinGame` caps the overall roster (`ErrGameFull`) in its CAS loop for all modes; an agent whose invited join fails declines the invitation instead of retrying | The per-team teams cap left competitive/coop uncapped, so a race (or a mis-gated UI) could seat a 5th player in a 4-player game. An invited agent that couldn't be seated (team over-subscribed by the creator) otherwise re-accepted the same invitation in a tight loop; declining on failure breaks it. |
| 24 | Server browser with persisted favorites + a lobby head count per probe | The login screen's connection choice is a tree (FAVORITES / CONTEXTS / COMMAND LINE) rather than radios + a free URL field; favorites persist in `~/.config/jetris/favorites.json` (`internal/prefs`), pre-seeded with the nats.io demo server (US) and the Jetris EU server; a row's ↻ dials that server, measures the core NATS ping and counts the `players.*` presence entries in its lobby KV; LAN mode is its own tab | A multiplayer game wants a *server browser*: bookmarks you come back to, contexts you already have, and — before committing — how far the server is and whether anyone is there to play with. Reading the lobby KV (not a stream) gives a live head count for free thanks to the TTL'd presence entries, with no provisioning on a server you may never join. |
| 22 | Game invitations | Written to the invitee's PER-GAME KV mailbox key `invites.<invitee>.<gameID>` (several at once, 2-min TTL); the key's lifecycle is the state machine (delete = accept/retract, rewrite `declined: true` = decline, kept for the inviter to see); `JoinGame` guards invite-only games inside its CAS loop (creator or invitation holder only, invitation exempts from `MaxAgents`); agents auto-accept | Reuses the lobby KV and its existing whole-bucket watcher — no new stream; the invitation is both the routing (which game/team) and the authorization (the creator's explicit choice), so it cleanly overrides the open agent policy. One key per (invitee, game) supports concurrent invitations from several games and gives the inviter a live per-invitee status view (`SentInvites`) from the same watch. |
| 24 | Lobby events | Every lobby action (game created/joined/left, invite sent/retracted/declined) is also published as a transient CORE NATS `LobbyEvent` on `jetris.lobby.event.<kind>`; every lobby subscribes and turns foreign events into immediate refresh pings | State stays in the KV (single source of truth); the events are pure low-latency signals — core NATS is enough, deliberately captured by no stream (nothing to replay, nothing to clean up). Closes the presence-heartbeat latency gap for "who is invitable right now" and gives external agents a push channel without polling. |
| 25 | Live invite picker & self-seat | The picker sends/retracts invitations the moment a selection changes (no send button) and pins the creator as a first row whose selection IS a roster seat — UNSELECTED by default (the creator hosts as a spectator), selecting it `JoinGame`s, deselecting `UnjoinGame`s; when the roster fills the picker hands the creator to `joinGame` (kept seat) or `spectateGame` (opted out) | Selection-as-action removes a whole failure mode (configured-but-never-sent invites) and makes the picker double as the live status board; defaulting the creator to spectator keeps hosting and playing as two explicit, opt-in choices. Capacity is guarded at click time from roster+pending usage, so over-invites are refused rather than bounced later at the door. |
| 26 | Leave/rejoin keeps the seat | "Back to Lobby" clears the READY mark (`SetReady(false)`) but keeps the roster seat while the game is alive; the lobby row reads **joined**/**playing** with a Rejoin button (`JoinGame`'s already-seated branch returns the same position); leaving an in-progress game asks for confirmation; presence stays In Game while a live seat is held | A seat is a commitment to the other players — silently freeing it on a screen change would strand games; keeping it makes leave/rejoin a pure view change (the stream replays the live board on rejoin). Ready must NOT survive the exit, though: an absent "ready" player would let the countdown fire without them. |
| 27 | Piece preview (`next_count`) | Per-game 0..4, chosen at creation, stored in `GameMeta` (NOT omitempty — 0 is meaningful and pre-field metas unmarshal to 0) and mirrored on the listing; the NEXT well beside the playfield and the agent's lookahead both read `Engine.NextPieces()` | One attribute moves both eyes: the fair-visibility contract goes from "never look ahead" to "look ahead exactly as far as the preview", and it stays enforceable because UI and planner consume the identical accessor over the seekable 7-bag sequence (no queue state to reconcile). |
| 28 | Garbage holes (`garbage_holes`) | Per-game 0..4 (default 0), chosen on the wizard's preview step for competitive/teams, stored omitempty in `GameMeta` (0 = the pre-field behavior) and mirrored on the listing; `ProjectShrinkCascade` punches the raise's hole columns (one random set per raise, or one per row under `random_garbage_holes` — `RaiseHoles`) and `Row.IsFull` completes a garbage row once every cell is locked and at least one is a player's | Solid garbage stays the classic unclearable wall; with holes the garbage becomes playable Guideline-style (clean, well-aligned holes per attack) and a cleared garbage row is just a line — same scan, same clear transform, same score and counter-attack — so no second clearing rule, register, or transform was needed. |
| 29 | Guideline garbage (`guideline_garbage`) | Per-game bool (off by default), chosen on the wizard's preview step for competitive/teams, stored omitempty in `GameMeta` and mirrored on the listing; `handleLockIn` sizes the attack with `game.AttackRows(lines, guideline)` — 0/1/2/4 rows for a single/double/triple/Tetris — and a zero attack bumps no register | The only thing that changes is the number CAS-added to the victims' registers: the ledger, the gate, scoring and levels are untouched, so the Guideline's "singles don't attack, a Tetris is worth double a triple" strategy layer costs one table lookup. |
| 21 | Shared-board spawn blocked by another player's ACTIVE piece | DEFER the spawn (`spawnPending`) and retry it from `runInput`'s gravity tick (`retrySpawnIfPending`) — top out only when the spawn cells hold LOCKED cells (`CanPlaceCoop` fails AND `CanPlace` fails) | Mirrors the locked-vs-active distinction gravity/hard-drop already make; a teammate's piece merely crossing the spawn area must not eliminate a player (in teams permanently — the "one piece per team board" bug — and in coop it would end the game for everyone). The gravity ticker is the retry heartbeat: no new goroutine, the single-write-goroutine invariant holds, and the cadence matches how fast the blocker can move. Known deferred edge: a *disconnected* player's abandoned mid-air piece blocks indefinitely — a pre-existing engine-wide gap (it equally blocks movement/locks today). |
| 22 | Piece-less watchdog + no-regress meta transitions | `retrySpawnIfPending` force-spawns after 2 piece-less gravity ticks (gated on `gameStarted`); `lobby.transitionGameStatus` refuses to overwrite finished/archived/cancelled | The lock-in edge detector needs an incoming message to fire — a dropped spawn publish on a since-silent shared board (last teammate eliminated) stalls a player forever without the watchdog. And the countdown's final `StartGame` is a detached goroutine racing the game itself: a fast game (agents) can FINISH before that write lands, and an unguarded in_progress stamp over finished resurrects the game and strands it unarchivable. |
| 23 | Archive verdicts (winner / winning team) | Verdicts taken from the archiving ENGINE's live record (`IsEliminated` set, `GameOutcome()` accessor); per-player STATS (score/level/piece count) recovered by replaying each player's retained game_over event | With per-kind, per-player event subjects each player's single game_over can never be overwritten by other traffic, so the post-game replay recovers every player's final stats — but a who-ever-sent-an-event set would still mis-score near-simultaneous final top-outs as a draw, so the verdict stays with the engine that lived through the game: in competitive it knows every elimination; in teams it is by construction on the winning side (or a draw participant), so its own verdict IS the team verdict. |

---

## 20. Release Pipeline

**File:** `.github/workflows/release.yml`

Pushing a git tag matching `v*` (e.g. `v0.1.0`) triggers a GitHub Actions workflow that runs the test suite, builds the `jetris` binary for every supported platform, and publishes a GitHub release containing one archive per platform plus a `SHA256SUMS` checksum file. Release notes are auto-generated from the commits since the previous tag.

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

Binaries are built with `-ldflags "-s -w -X main.version=<tag>"`, which stamps the tag into `main.version` (default `dev` for local builds) — reported by the `--version` flag and, in the player binary, handed to `nativeui.SetVersion` at startup so it also shows on the **VER** plate in the window's top-right corner. The stamped tag is also what the startup update check (`internal/update`, see the `--no-update-check` flag) compares against GitHub's latest release, so every published build can tell its player when a newer one exists.

---

## 21. Agents: the `golang-mk1` reference and the `agents/` home

**The agent model.** An agent is a standalone program that plays Jetris by speaking the
game's NATS/JetStream protocol — there is no plugin interface or shared SDK to implement.
The single, language-neutral contract is the wire protocol plus the fair-play rules in
`jetris-agent-guide.md` (with the game rules in `jetris-gameplays.md`); a conformant
agent can be written in any language depending on nothing in this repo. Contributed agents
live in `agents/<name>/`, each self-contained (own language/build/deps, its own README);
`agents/README.md` is the submission guide. The game neither knows nor cares how any agent
is built. The first entry is `agents/example-python/` — a minimal single-file Python agent
(`example-py`, competitive mode) that implements the entire protocol from the guide with no
repo dependency: lobby KV CAS join/ready/countdown, a bit-exact port of the piece RNG
(Go `math/rand/v2` PCG + 7-bag), its own engine (spawn/gravity/lock/clears/top-out),
the garbage ledger (CAS-adds on victims' registers + txn-gated application),
atomic CAS cell batches with write-through, per-player game_over events, CAS-failure
flashes, and the finish→archive→cleanup sequence when it wins (its ArchiveRecord is
byte-compatible with the Go structs). `agent.py --selftest` runs offline conformance checks (RNG parity
fixtures generated from `internal/rng`).

**The reference agent `golang-mk1`.** The repository ships one Go agent — `golang-mk1`,
in `agents/golang-mk1/`. It is an **independent module** (its own `go.mod`; its only
dependencies are the `nats.go` client and the orbit `natscontext` and `jetstreamext` helpers), NOT part of
the main module's `go build ./...`: it
implements the wire protocol straight from `jetris-agent-guide.md` with no access to the
game's packages, exactly as a third-party agent would — every protocol interaction in its
source cites the guide section it implements, so it also serves as a worked reading of
the contract. It plays **all three modes** with the Dellacherie/El-Tetris heuristic at
`easy`/`medium`/`hard` difficulties, hosts games with `--create --mode …`, and its player name
reads `golang-mk1-<instance>-<difficulty>`. It builds without cgo/Gio on every platform.
Everything below describes that reference implementation.

### agents/golang-mk1 files

| File | Contents |
|------|----------|
| `pieces.go` | Tetromino geometry: spawn shapes and SRS rotations. |
| `rng.go` | Bit-exact port of the game's piece RNG (Go `math/rand/v2` PCG + 7-bag), so the agent's own piece sequence matches every peer's view of it (`rng_test.go` locks parity fixtures generated from `internal/rng`). |
| `engine.go` | The settled-board model: collision, drop row, completed rows, collapse. |
| `planner.go` | The brain: Dellacherie's six features with the El-Tetris weights, placement enumeration over the move vocabulary, beam-pruned lookahead over the game's revealed preview — `revealedPieces` is the planner's single source of upcoming pieces: the game's `next_count`, read from its meta (absent = 0), further trimmed by the difficulty and never past the preview (the fair-visibility contract; `preview_test.go` checks every difficulty against every preview size) — and the blunder model. |
| `difficulty.go` | The `easy`/`medium`/`hard` knob sets: think/move pacing, blunder rate/depth, lookahead cap. |
| `types.go` | Wire payloads and the `obj` raw-field map that keeps unknown fields — and the 64-bit seed's exact digits — intact across CAS read-modify-writes of the lobby KV and meta. |
| `agent.go` | The lobby: presence heartbeat, the KV mirror (listings + invitations), invitation accept/decline, select/join/ready CAS flows, the 5..0 countdown when its ready toggle completes the set, `--create` hosting (game stream + meta + listing + `game.created` event), and the pre-start un-join. |
| `game.go` | One game: consumers, its own engine loop (spawn/gravity/lock-in/clears/top-out), atomic CAS cell batches with write-through, the garbage ledger (CAS-adds on victims' registers + txn-gated application), per-player `game_over` events, CAS-failure flashes, outcome, and the finish→archive→cleanup sequence when it wins. |
| `shared.go` | Shared boards (coop/teams): the board consumer (teammates' pieces, adopted shifts of our own, register echoes), deferred spawns, coop merge-retry clears that shift other pieces with the stack, cascading garbage lifts with teammate-piece guards, the vacate-on-elimination transform, and team verdicts. |
| `main.go` | Flags, signal handling, and `--selftest` (offline RNG-parity and planner sanity checks). |

### golang-mk1 flags

| Flag | Meaning |
|------|---------|
| `--server` / `--context` / `--user` / `--password` | Connection choice, same semantics as `cmd/jetris` and the `nats` CLI: an explicit URL (with optional user/password) wins over `--context`; with neither, the currently selected NATS context connects (falling back to the client default `nats://127.0.0.1:4222` when no context exists). Contexts are the NATS-CLI-compatible kind (credentials, TLS, JetStream domain honored) via the orbit `natscontext` package |
| `--name` | The agent VERSION stem (default `golang-mk1`, its codename, bumped when the play logic changes). The full player name is `<stem>-<instance>-<difficulty>` with a fresh 4-hex instance id per connection; every component sticks to the presence-KV charset and the whole fits the 32-character cap |
| `--difficulty` | `easy` \| `medium` \| `hard` (default `hard`) |
| `--join <gameID>` | Join a specific game (still subject to that game's agent policy) |
| `--create` + `--mode` + `--players N` + `--max-agents M` + `--next K` | Host a game (cooperative/competitive/teams; `--players` is per team in teams mode) and wait for opponents; `M` agent seats including this agent (0/default = all seats — an agent-hosted game is agent-friendly); `K` upcoming pieces the game reveals (0-4, default 1) |
| *(neither)* | **Resident mode**: wait in the lobby and play invited games, game after game, until interrupted |
| `--auto-join` | Residents also actively join the oldest open agent-allowed game of any mode with a free (agent, and in teams team) seat (default: invited games only) |
| `--wait` | Max wait for a joined game to fill and start before un-joining it (default 10m) |
| `--once` | Exit after one game instead of staying resident |
| `--selftest` | Run the offline conformance checks and exit |

SIGINT/SIGTERM stop the agent; it deletes its presence and drains the connection on the
way out. Exit status 0 covers both winning and losing; non-zero means a setup or runtime
error.

### Testing

`rng_test.go` locks the RNG-parity fixtures, `preview_test.go` pins the fair-visibility
preview cap (no difficulty plans past the game's `next_count`; a meta without the field
is 0), and `--selftest` replays the fixtures offline along with a planner line-clear
sanity check — none need a server. Live conformance is
verified by playing it: against the GUI (create an agent-allowed competitive game on a
local `nats-server -js`), or agent-vs-agent (`--create --once` on one instance,
`--auto-join --once` on another) — the winner's archive record then shows up in the
lobby's game history like any other game's.

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
create wizard exposes the policy for every game mode on its agents step
("Allow agents to join" checkbox + max-agents editor,
`nativeui/createwizard.go:wizardAgentsStep`) — a step reached only for open games
(an invite-only game's agent policy is per-invitation, so its wizard ends at the
who-can-join step and `createGame` is called with maxAgents 0) — tags agent
players `[agent]` throughout
(lobby player list via `PlayerPresence.Agent`, game rows, ready roster, legend), and
shows `agents k/N` on agent-allowed game rows. Relatedly, `cleanup.Run` applies a
one-minute `creationGracePeriod` before treating a game as orphaned/creator-absent:
creation is several separate writes and agent-allowed games legitimately sit at zero
players until a resident agent's scan picks them up — without the grace, a peer (or
agent) logging in mid-creation could cancel a brand-new game.
