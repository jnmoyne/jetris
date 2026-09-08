// Package nativeui is the native (Gio) player front end for Jetris. It drives
// the lobby and engine business logic, draws directly into an OS window, and
// reads engine.Updates / lobby.Updates straight off their Go channels, so a
// NATS update reaches the screen within one display frame.
package nativeui

import (
	"context"
	"image/color"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/gesture"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/prefs"
	"jetris/internal/qr"
	"jetris/internal/voice"
	"jetris/internal/webdist"
)

// Layout type aliases used throughout the package.
type (
	C      = layout.Context
	D      = layout.Dimensions
	colorN = color.NRGBA
)

type screenKind int

const (
	screenLogin screenKind = iota
	screenLobby
	screenGame
	screenArchive // viewing a finished game's end-of-game playfield from the history list
	screenReplay  // replaying an archived game from its replay stream
)

const flashDur = 600 * time.Millisecond

// countdownAnimDur is the pop-in duration for each pre-game countdown number;
// countdownBaseSp is the settled font size of the centered number (drawn in
// the pixel face, whose glyphs run much larger per point than the Go faces).
const (
	countdownAnimDur = 450 * time.Millisecond
	countdownBaseSp  = 96.0
)

// UI chrome colors (the board itself uses internal/render). The chrome is the
// "modern 8-bit" theme: a dark blue-black backdrop with neon accents, chunky
// square-cornered borders (colBorder), and hard offset shadows (colShadow).
// The accent is the NATS brand blue, so the whole chrome carries the branding;
// colNATSGreen is the logo's green, used for positive highlights.
var (
	colBg           = color.NRGBA{R: 0x0d, G: 0x0d, B: 0x16, A: 0xff}
	colPanel        = color.NRGBA{R: 0x16, G: 0x16, B: 0x24, A: 0xff}
	colBorder       = color.NRGBA{R: 0x2c, G: 0x2c, B: 0x44, A: 0xff} // panel frames
	colShadow       = color.NRGBA{A: 0x8c}                            // hard offset shadow under buttons/dialogs
	colFg           = color.NRGBA{R: 0xe6, G: 0xe6, B: 0xe6, A: 0xff}
	colMuted        = color.NRGBA{R: 0x8a, G: 0x8a, B: 0x9e, A: 0xff}
	colAccent       = color.NRGBA{R: 0x27, G: 0xaa, B: 0xe1, A: 0xff} // NATS brand blue
	colNATSGreen    = color.NRGBA{R: 0x8d, G: 0xc6, B: 0x3f, A: 0xff} // NATS brand green
	colErr          = color.NRGBA{R: 0xff, G: 0x55, B: 0x55, A: 0xff}
	colGold         = color.NRGBA{R: 0xff, G: 0xcc, B: 0x00, A: 0xff} // countdown numbers (matches web)
	colGo           = color.NRGBA{R: 0x00, G: 0xff, B: 0x88, A: 0xff} // countdown "GO!" (matches web)
	colWarn         = color.NRGBA{R: 0xff, G: 0xdd, B: 0x00, A: 0xff} // RTT warning start (yellow, at 75 ms)
	colOrange       = color.NRGBA{R: 0xff, G: 0x8c, B: 0x00, A: 0xff} // RTT warning end (orange, at 150 ms)
	colLobby        = color.NRGBA{R: 0x7f, G: 0xb2, B: 0xff, A: 0xff} // lobby messages shown inside a game's chat (@lobby)
	colStrobe       = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // line-clear row strobe (pure white)
	colFocus        = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // keyboard-focus outline while playing: the board's frame or the chat's ring
	colIntent       = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // the white outline on the piece being steered (the pre-rendered move)
	colAckedOutline = color.NRGBA{R: 0x8a, G: 0x8a, B: 0x9e, A: 0xff} // the grey outline where the acks have the piece (Optimistic async)
)

// gameRowBtns are the per-game-listing action buttons (rebuilt lazily per game).
type gameRowBtns struct {
	join     widget.Clickable
	joinTeam [config.MaxTeamCount]widget.Clickable // teams mode: one join button per team (only the game's first Teams() are drawn)
	spectate widget.Clickable
	reinvite widget.Clickable // invite-only creator: re-open the invitee picker
	del      widget.Clickable // abandoned games: opens the delete confirmation
	delYes   widget.Clickable // delete confirmation: "Yes, delete"
	delNo    widget.Clickable // delete confirmation: "Cancel"
}

// App holds all native-UI state. Fields read or written by more than one
// goroutine (the engine/lobby pumps plus the UI goroutine) are guarded by mu.
// Gio widget values are touched only by the UI goroutine and need no lock.
type App struct {
	js jetstream.JetStream
	kv jetstream.KeyValue
	nc *nats.Conn // the app-dialed NATS connection (nil until the player connects); guarded by mu

	// Connection picker: set at construction by NewWithPicker, immutable
	// afterwards. connCfg carries any --user/--password flags through to URL
	// connects; connCtxURLs is each context's server URL for display (best
	// effort, "" when unknown); favSave persists the favorites and
	// handlingSave the DAS/ARR/SDF knobs and panelsSave the two screens'
	// panel switches (prefs.Save* — tests stub them).
	needConn     bool
	connContexts []string
	connSelected string
	connCtxURLs  map[string]string
	connCfg      config.Config
	favSave      func([]prefs.Favorite) error
	handlingSave func(prefs.Handling) error
	keymapSave   func(prefs.Keymap) error
	panelsSave   func(prefs.Panels) error
	voiceSave    func(prefs.Voice) error
	// autoLogin: the player's name came with the connection (--name, or the
	// join page's ?player=), so the login screen submits itself on its first
	// frame. One-shot — read and cleared on the UI goroutine, so it needs no
	// lock — which is what leaves a failed connect, and a later quit back to
	// this screen, in the player's hands.
	autoLogin bool

	// Server probes — a browser row's click, the page-opening refresh of every
	// favorite, "Refresh all servers", and LAN mode's "Check embedded
	// server" (written by
	// doCheckConn; guarded by mu): the last result per server key
	// (connEntry.key, or probeKeyLAN for the embedded server), and the keys
	// being probed right now — several at once. connRound is the refresh
	// round's keys still out, connRoundDone that its last result is in
	// (applyRefreshRound then sorts the favorites by ping and selects the
	// fastest); connRefreshed that the page refreshed since it opened.
	connProbes    map[string]probeResult
	connProbing   map[string]bool
	connRound     map[string]bool
	connRoundDone bool
	connRefreshed bool

	// Embedded server ("LAN party mode (embedded NATS server)" option; guarded by
	// mu). The server starts on the first embedded login and runs until the
	// window closes — quitting to the login screen leaves it up for connected
	// friends; picking a different port on a later login restarts it there.
	// Beside it runs the browser build's HTTP server (webdist), on its own
	// port, with the same lifetime: the phones on the network open it, and
	// the game they load dials the embedded server's WebSocket listener.
	// usingEmbedded marks the CURRENT connection as being to it, which is
	// what gates the lobby's shareable-address lines.
	embSrv        natspkg.EmbeddedServer
	embAddr       string // shareable "<lan-ip>:<port>" — desktop builds and agents dial it
	embWSAddr     string // its WebSocket listener, "<lan-ip>:<wsport>" — what the browser build dials
	webSrv        *webdist.Server
	embHTTPAddr   string // the browser build's "<lan-ip>:<httpport>" — what the phones open
	embHTTPScheme string // "https" (the page has its certificate: the browser's microphone works) or "http"
	embName       string // what the party's server is called (the tab's Name field): the join link's label
	usingEmbedded bool

	// connName and connURL name the server the CURRENT connection reached,
	// for the lobby header and the game HUD: the name as the player knows it
	// ("Jetris EU central", "context ngs", the LAN party's own name) and the
	// URL actually dialed. They are kept apart, not pre-joined, because the
	// two are shown differently — the game HUD has room for the name alone,
	// and where both fit it is the URL that gives way first, never the name
	// (see connectionParts). A connection with no name to go by has the URL
	// as its name and nothing in connURL. Set on connect, cleared on
	// disconnect; guarded by mu.
	connName, connURL string
	// NATS link health (link.go): linkDownAt is when the connection dropped
	// (zero while it is up) and linkErr why; set by the connection's
	// callbacks, shown as the game HUD's LINK stat. Guarded by mu.
	linkDownAt time.Time
	linkErr    string

	win *app.Window
	th  *material.Theme
	ctx context.Context // app lifecycle context (set in Run)

	mu     sync.Mutex
	screen screenKind

	// lobby session
	lobby       *lobby.Lobby
	lobbyCancel context.CancelFunc

	// active game
	eng         *engine.Engine
	engCancel   context.CancelFunc
	gamePlayers []lobby.PlayerSummary

	// login transient state
	loggingIn bool
	loginErr  string
	// A newer release found by the startup check (NotifyUpdate): its tag and
	// download page, "" until/unless one is found. Shown on the version
	// plate and the login screen.
	updateTag      string
	updateURL      string
	loginCollision bool

	// game render snapshot (written by pumpEngine)
	score int
	level int
	award awardBanner // the last scored clear on the player's board, for the banner (award.go)

	teamScores   []int         // teams: live per-team scores, one entry per team in index order
	teamLevels   []int         // teams: live per-team levels, one entry per team in index order
	rtt          time.Duration // latest publish→echo round trip from the engine
	gameStatus   string
	countdown    int       // -1 none, 0 GO!, >0 seconds remaining
	countdownAt  time.Time // when the current countdown number arrived (for the pop animation)
	gameOver     bool
	won          bool
	myReady      bool
	readyPlayers []lobby.PlayerSummary
	readyNote    string // what the table still waits for (lobby.GameListing.ReadyBlocker), for the ready bar
	flash        map[[2]int]time.Time
	// casWant is the own board's blinking outline where a rejected step
	// wanted the piece — the move the CAS failure took away — keyed like
	// flash, which keeps the plain rainbow border for a rejection with no
	// target to point at (a lost spawn, lock or gravity step).
	casWant map[[2]int]time.Time
	// casKickAt is the recoil epoch that goes with it: when our last write
	// was rejected, which snaps the piece back and vibrates it where the
	// rejection put it (zero = idle); casKickFrom is the snap-back's start,
	// where the lost step wanted the piece relative to where it stood, in
	// cells (zero: nowhere, the buzz alone). The layout runs the recoil
	// (trackRecoil).
	casKickAt   time.Time
	casKickFrom [2]float64
	// specFlash holds CAS-failure flashes for SPECTATOR boards, broadcast
	// by players over core NATS. Keyed by board index — the flashing
	// player's global index (competitive) or team (teams). A player's own
	// board flash lives in `flash`, not here.
	specFlash map[int]map[[2]int]time.Time
	fireworks *fireworksShow // victory fireworks show; nil until a competitive/teams win
	// A spectator's winner show (spectator_reveal.go): when the game was first
	// seen decided (the show's clock; zero until then) and the game's rank in
	// its replay bucket — provisional from the live totals, final once the
	// lobby holds the archive record. Written by resolveOutcome on the UI
	// goroutine and reset with the rest of the game state; guarded by mu.
	decidedAt     time.Time
	liveRank      int // 0 = not yet ranked
	liveOf        int
	liveRankFinal bool
	// rowStrobes holds the own board's arcade row strobes: rows just cleared
	// on it (by this player, or a teammate on a shared board) blink white, and
	// in competitive/teams garbage rows that just landed blink in the
	// attacker's color. Written by pumpEngine (clears) and by the layout's
	// garbage-arrival detection (detectGarbage).
	rowStrobes map[int]rowStrobe
	// garbageRows/garbageSeen track the adversarial-row count last observed on
	// the own-board snapshot; a frame that sees the count grow strobes exactly
	// the new rows and kicks the shake. garbageSeen gates the first
	// observation so a rejoin never strobes the whole pre-existing stack.
	garbageRows int
	garbageSeen bool
	shakeStart  time.Time // garbage impact-shake epoch (zero = idle)
	// specRowStrobes / specGarbageRows are the SPECTATOR boards' counterparts
	// of rowStrobes / garbageRows, keyed like specFlash (player index or
	// team): garbage that lands on a watched board strobes there in the
	// attacker's color — the victims' impact feedback, minus the shake.
	// Written by the layout's per-board detection (detectGarbageOn).
	specRowStrobes  map[int]map[int]rowStrobe
	specGarbageRows map[int]int

	// chat log (written by pumpLobby)
	chatLog []lobby.ChatMessage

	// NATS message panel: msgShow mirrors the "Show NATS messages" checkbox
	// each frame and gates collection; msgLog holds the tail of game-stream
	// messages tapped via engine.OnStreamMsg (written by consumer goroutines).
	// msgGroupOf assigns each atomic batch (transaction) a stable ordinal that
	// picks its row tint, msgGroupSeq is the counter behind it and
	// msgGroupSeen the insertion-ordered id list that bounds the map.
	msgShow      bool
	msgLog       []streamMsg
	msgGroupOf   map[string]int
	msgGroupSeen []string
	msgGroupSeq  int

	// --- UI-goroutine-only widgets ---
	loginEd      widget.Editor
	loginBtn     widget.Clickable
	collisionYes widget.Clickable
	collisionNo  widget.Clickable
	// Connection page widgets. connTab picks the tab (the server browser or
	// LAN mode); the browser is a collapsible tree of sections — FAVORITES
	// (the persisted bookmarks, with an inline add form and per-row delete),
	// CONTEXTS (the NATS CLI contexts) and, when --server was given and isn't
	// a favorite, COMMAND LINE — whose selected row is connSel (an entry key,
	// "url:<url>" or "ctx:<name>"). LAN mode has the IP + port editors.
	connTab        string
	connTabBtns    [2]widget.Clickable          // the two tab chips
	connSel        string                       // selected browser entry key
	favorites      []prefs.Favorite             // FAVORITES rows, in display order
	connSecClosed  map[string]bool              // section title → collapsed (absent = expanded)
	connSecBtns    map[string]*widget.Clickable // section header toggles, by title
	connRowBtns    map[string]*widget.Clickable // browser rows, by entry key
	connDelBtns    map[string]*widget.Clickable // favorites' ✕ buttons, by entry key
	connBrowserLst widget.List                  // the scrollable browser tree
	connAddOpen    bool                         // the add-favorite form is expanded
	connAddScroll  bool                         // scroll the browser to the add form on the next frame (set when it opens)
	connAddRowBtn  widget.Clickable             // "+ Add a NATS URL…" row
	connAddLabelEd widget.Editor                // add form: label (optional)
	connAddURLEd   widget.Editor                // add form: the URL
	connAddBtn     widget.Clickable             // add form: Add
	connAddCancel  widget.Clickable             // add form: Cancel
	connNameEd     widget.Editor                // LAN mode: the server's name (pre-set to config.DefaultEmbeddedName; empty = that again)
	connHostEd     widget.Editor                // LAN mode: IP entry (pre-set to the detected lanIP; empty = auto-detect again)
	connPortEd     widget.Editor                // LAN mode: NATS port entry (pre-set to config.DefaultEmbeddedPort)
	connWSPortEd   widget.Editor                // LAN mode: WebSocket port entry (pre-set to config.DefaultEmbeddedWSPort)
	connHTTPPortEd widget.Editor                // LAN mode: HTTP port entry (pre-set to config.DefaultEmbeddedHTTPPort)
	lanIP          string                       // this machine's auto-detected LAN address, resolved once (seeds the IP field and backs the shareable-URL lines)
	connRefreshAll widget.Clickable             // browser: the list's "↻ Refresh all servers" row
	connCheckBtn   widget.Clickable             // LAN mode: Check embedded server
	// FAVORITES' "Remove <n> outdated servers" row (cleanupRow, listed only
	// while some favorite is prefs.Outdated) and its trailing "Reset
	// favorites…" row with its confirmation modal (connResetOpen while it is
	// up): Yes puts prefs.DefaultFavorites back in place of whatever the
	// list holds.
	connCleanupBtn  widget.Clickable
	connResetRowBtn widget.Clickable
	connResetOpen   bool
	connResetYes    widget.Clickable
	connResetNo     widget.Clickable
	scrimTag        int // address used as the modal scrim's pointer-area tag (login screen)

	// The lobby's Show QR code (LAN mode, lanqr.go): the modal that puts the
	// party's join link on the screen as a QR code, up while qrOpen; qrCode
	// is the encoding of qrLink, kept from frame to frame.
	qrShowBtn widget.Clickable
	qrOKBtn   widget.Clickable
	qrOpen    bool
	qrLink    string
	qrCode    *qr.Code

	// The key bindings (keymap.go): the scheme in play, and the lobby's
	// dialog on one line of the KEYS legend — up while keysLine is a line's
	// index, waiting on a key while keysSlot is a slot button's, keysErr the
	// word on the last press it refused. keysTag is the dialog's key target.
	keys         keyBinds
	keysLineBtns [keyLineCount]widget.Clickable
	keysSlotBtns [keySlotButtonCount]widget.Clickable
	keysDoneBtn  widget.Clickable
	keysResetBtn widget.Clickable
	keysLine     int
	keysSlot     int
	keysErr      string
	keysTag      int

	// The replay screen's PIN and SHARE (share.go): the pin toggles the
	// lobby KV entry that keeps the replay for good; Share puts the replay's
	// link up as a QR code with a Copy link button, up while shareOpen —
	// shareLink/shareWhy/shareCode are what openShare built for it, and
	// shareCopiedAt when the link was last copied (the button reads COPIED
	// for shareCopiedFor after). UI goroutine only. linkedReplay is the
	// game a share link (or --replay) asked to open on landing in the lobby,
	// taken once (takeLinkedReplay); guarded by mu.
	replayPinBtn   widget.Clickable
	replayShareBtn widget.Clickable
	// The game-over box's own Pin and Share (gameOverActions), for the game
	// just played; gameOverNote is the line under them (a refused pin),
	// cleared with the rest of the game state (lifecycle.go).
	gameOverPinBtn   widget.Clickable
	gameOverShareBtn widget.Clickable
	gameOverNote     string
	shareOKBtn       widget.Clickable
	shareCopyBtn     widget.Clickable
	shareOpen        bool
	shareLink        string
	shareWhy         string
	shareCode        *qr.Code
	shareCopiedAt    time.Time
	shareCopyOK      bool // whether the last copy reached the clipboard (copyText)
	linkedReplay     string

	// connPicked: the player clicked a browser row since the page-opening
	// refresh started, so its result must not move the selection. favOrder
	// is the favorites' display order — indices into favorites, fastest ping
	// first — set by the last refresh round (nil = the list's own order).
	// UI goroutine only.
	connPicked bool
	favOrder   []int

	// Create-game wizard: the lobby's single "Create a new game" button
	// (createBtn) opens a modal that walks through the game's attributes one
	// step at a time — 1: game type + seats, 2: the play rules (the Guideline
	// preset, or custom: preview, ghost, hold, bag, garbage), 3: open vs
	// invite-only, 4: agent policy (open games only; an invite-only game
	// finishes at step 3 and hands off to the invitee picker). createWizStep
	// is the current step, 0 while the wizard is closed.
	createBtn      widget.Clickable
	createWizStep  int
	createJoinEnum widget.Enum                        // wizard step 3: "open" or "invite"
	boardsEnum     widget.Enum                        // wizard step 1: "single" (one shared playfield) or "multiple" (a team on each of several)
	singleKindEnum widget.Enum                        // wizard step 1, single playfield: "coop" (scored together) or "competitive" (each seat on its own — config.ScoringIndividual)
	lengthEnum     widget.Enum                        // wizard step 2: "topout" (until someone tops out) or "lines" (the lineGoalEd number of lines — config.GameSpec.LineGoal)
	lineGoalEd     widget.Editor                      // wizard step 2: the line goal (blank = config.DefaultLineGoal)
	wizBackBtn     widget.Clickable                   // wizard: back one step
	wizNextBtn     widget.Clickable                   // wizard: Next / Choose players… / Create game
	wizCancelBtn   widget.Clickable                   // wizard: close without creating
	wizList        widget.List                        // wizard: the step's body, scrolling when the step is taller than the window leaves it (the custom rules step in a garbage mode at the minimum window height)
	countEd        widget.Editor                      // wizard step 1: the players (per playfield, when there are several)
	teamNameEds    [config.MaxTeamCount]widget.Editor // wizard step 3, teams: what each playfield's team is called (the piece colours unless renamed — config.DefaultTeamNames)
	// The board-growth sliders of wizard step 2's custom rules, shown for a
	// playfield with company: extraCols is how many columns every seat
	// beyond the first adds to the board's standard 10
	// (config.MinExtraColumns..config.MaxExtraColumns), extraRows how many
	// rows it adds below the standard 20
	// (config.MinExtraRows..config.MaxExtraRows); the Floats are the
	// sliders' positions, snapped to the whole-number detents. The Guideline
	// preset keeps both at their defaults.
	extraColsFloat widget.Float
	extraCols      int
	extraRowsFloat widget.Float
	extraRows      int
	// The playfield-count slider of wizard step 1, shown for a
	// multi-playfield game: playfieldCount is how many boards the game is
	// played on (config.MinTeamCount..config.MaxTeamCount, default two —
	// Team A vs Team B), playfieldsFloat the slider's position, snapped to
	// the whole-number detents. The seat editor beside it is per playfield,
	// so the game's total is playfieldCount × that.
	playfieldsFloat widget.Float
	playfieldCount  int
	// splitPiecesCb is wizard step 2's "distribute the pieces" checkbox
	// (custom rules), drawn for a playfield with company: the seven piece
	// types are dealt out between the players of a playfield, every seat
	// playing only its own ration (config.GameMeta.SplitPieces). On by
	// default, and on in the Guideline preset.
	splitPiecesCb widget.Bool
	allowAgentsCb widget.Bool   // wizard agents step: allow idle agents to take seats
	maxAgentsEd   widget.Editor // wizard agents step: how many seats agents may take
	rulesEnum     widget.Enum   // wizard step 2: "guideline" (config.GuidelineRules, read-only) or "custom" (the editors below)
	holdCb        widget.Bool   // wizard (custom rules): the Guideline hold queue
	headroomCb    widget.Bool   // wizard (custom rules): the hidden headroom rows drawn above the playfield, behind smoked glass (config.GameMeta.ShowHeadroom)
	bagEnum       widget.Enum   // wizard (custom rules): the piece randomizer — "single" (the 7-bag), "double" (the double bag) or "none" (no bag; config.Bag)
	nextCountEd   widget.Editor // wizard: how many upcoming pieces the game reveals (0..config.MaxNextCount)
	holesEd       widget.Editor // wizard: holes per garbage row in competitive/teams (0..config.MaxGarbageHoles; 0 = solid, unclearable rows)
	randomHolesCb widget.Bool   // wizard: every garbage row draws its own hole columns (off = the rows of one attack share a draw)
	guidelineCb   widget.Bool   // wizard: attacks follow the Guideline table (1→0, 2→1, 3→2, 4→4 rows) instead of one row per line
	quitBtn       widget.Clickable
	chatEd        widget.Editor
	chatBtn       widget.Clickable
	playerList    widget.List
	gameList      widget.List
	archiveLst    widget.List
	// Game-history controls: sort selector ("score"/"date") and the
	// show-games-with-agents filter (checked = shown).
	histSortEnum     widget.Enum
	histHumansCb     widget.Bool // history filter: list all-human games ("Players only")
	histMixedCb      widget.Bool // history filter: list mixed human/agent games ("Agents and players")
	histAgentsOnlyCb widget.Bool // history filter: list agent-vs-agent games ("Agents only")
	histPinnedCb     widget.Bool // history filter: list only the games whose replay is pinned ("Pinned only"; off = every game)
	chatList         widget.List
	gameBtns         map[string]*gameRowBtns
	// uninviteBtns are the per-invitation Uninvite/Dismiss buttons on the
	// creator's invite-only game rows, keyed "<gameID>|<inviteeID>".
	uninviteBtns map[string]*widget.Clickable
	// confirmDeleteID is the abandoned game whose row currently shows the
	// "Are you sure you want to delete this game?" confirmation ("" = none).
	confirmDeleteID string
	// confirmLeave is true while the game screen asks "Are you sure you want
	// to leave?" (leaving an in-progress game needs confirmation; the seat is
	// kept and the lobby offers Rejoin).
	confirmLeave   bool
	leaveFreesSeat bool // the leave being confirmed is out of a running open game: the piece is vacated and the seat freed
	leaveYesBtn    widget.Clickable
	leaveNoBtn     widget.Clickable

	// lobbyErr surfaces a failed game creation as a red strip under the lobby
	// banner (guarded by a.mu — createGame runs off the UI goroutine).
	// Without it the wizard silently drops back to the lobby with the reason
	// buried in the log (e.g. a server that refuses new streams). Cleared when
	// the wizard reopens or a create succeeds.
	lobbyErr string

	// Invite-only create flow, entered from the create wizard's "Invite only"
	// choice. While invitePickerGameID is non-empty the invitee-picker overlay
	// is open for that just-created game; invitePicker holds one row of widget
	// state per selectable player (keyed by player ID). Selecting a player
	// sends their invitation IMMEDIATELY (deselecting retracts it) — there is
	// no send button. The creator appears as a pinned first row, pre-selected:
	// selecting yourself means playing (a seat is taken right away — creating
	// an invitation game implies accepting your own invitation), deselecting
	// frees the seat and you'll spectate instead once the game fills.
	invitePickerGameID string
	invitePicker       map[string]*inviteChoice
	invitePickerErr    string          // capacity-guard message shown in the picker
	invitePickerMode   config.GameMode // captured when the picker opens
	invitePickerPC     int             // playerCount of the game being invited to
	invitePickerTS     int             // teamSize (teams mode)
	invitePickerTC     int             // teamCount (teams mode)
	invitePickerNames  []string        // the teams' names (teams mode; nil = the letters)
	inviteSelfSel      widget.Bool     // the pinned "You" row (non-teams): checked = playing
	inviteSelfTeam     widget.Enum     // the pinned "You" row (teams): "" or the team index as a string
	inviteSelfLastSel  bool            // last self intent applied (non-teams)
	inviteSelfLastTeam string          // last self intent applied (teams)
	inviteList         widget.List
	inviteMeasure      widget.Bool      // never drawn: measures checkbox widths for the picker's control column
	inviteCloseBtn     widget.Clickable // keep the game (and its invites), just close the overlay
	inviteCancelBtn    widget.Clickable // abandon: retract the invites and delete the game
	// Incoming-invitation pop-up (reads lobby.MyInvite at draw time).
	inviteAcceptBtn  widget.Clickable
	inviteDeclineBtn widget.Clickable

	// The How to play tour (tutorial.go): the lobby's button that opens it,
	// and the tour itself. UI goroutine only.
	tutBtn widget.Clickable
	tut    tutorial

	readyBtn widget.Clickable
	backBtn  widget.Clickable
	showMsgs widget.Bool // "Show NATS messages" checkbox
	// The LAB switch (lab.go): labEnum is labSync (Pessimistic sync ☹, the
	// classic game) or labAsync (Optimistic async ☺, the default).
	labEnum widget.Enum
	// ghostCb is the create wizard's "Show ghost piece" checkbox, ON by
	// default like every custom rule the Guideline preset has on
	// (setCustomRules): whether the game being created renders the hard-drop
	// landing preview. A per-GAME rule stored in the meta (GameMeta.NoGhost,
	// inverted), not a per-player view toggle — the creator decides once and
	// every player gets the same aid, like the piece preview.
	ghostCb widget.Bool
	msgList widget.List
	// NATS-panel resize handle: msgDrag is the divider's drag gesture,
	// msgPanelDp the user-chosen panel height in dp (0 = the window-reactive
	// default) and msgGrabY the press offset inside the divider, so the panel
	// edge tracks the grab point instead of jumping to the cursor.
	msgDrag    gesture.Drag
	msgPanelDp float32
	msgGrabY   float32
	boardTag   int // address used as the key-input focus tag; its pointer area is the whole game screen
	chatTag    int // address used as the chat panel's pointer-area tag (a press inside hands the keys to the chat)
	fieldTag   int // address used as the playfield's pointer-area tag: the touch-gesture surface (gesture.go)
	// playingSeen is last frame's "the keys drive the piece" state; its
	// false→true edge (game start, rejoin) hands keyboard focus to the board.
	// playingSeenEng is the engine it was observed for — a new engine (every
	// game entry makes one) starts the observation over. UI goroutine only.
	playingSeen    bool
	playingSeenEng *engine.Engine

	// On-screen arcade control pad (mouse and touch play, controls.go): the
	// D-pad's four arms (padUp rotates clockwise, like the ↑ key), the face
	// buttons — rotate CCW/CW, hard drop, and in games with the hold rule
	// hold, which the HOLD box beside the playfield also triggers when
	// tapped (holdBoxBtn). Clicks are dispatched by handlePadClicks — bar
	// the ← → ↓ arms', which move on the press and repeat when held
	// (handlePadShift).
	padUp, padLeft, padDown, padRight, padCCW, padCW, padDrop, padHold widget.Clickable
	holdBoxBtn                                                         widget.Clickable
	// touchUI: the player is on a touch screen, so the pad is laid out at
	// thumb size (padTouch). Set by the browser build from the page's media
	// queries (view_js.go) and, everywhere, by the first touch press on the
	// game screen (handleGameFocus). UI goroutine only.
	touchUI bool
	// Form factor (formfactor.go): deviceHint is the browser's reading of
	// the page — the device the game is on, hinted once at startup
	// (view_js.go); form is the current frame's, re-derived at the top of
	// every game frame from it and the window's own size. UI goroutine only.
	deviceHint   deviceKind
	deviceHinted bool
	form         screenForm
	// The game screen's switches (panels.go), each the player's standing
	// answer on one thing that costs the playfield room: hudShown is the menu
	// column (hudVisible), padShown the on-screen pad (padVisible), oppShown
	// the opponents' playfields beside the board (oppVisible) and chatShown
	// the chat strip under it (chatVisible). All four start on and every flip
	// saves the set (persistPanels). UI goroutine only.
	hudShown, padShown, oppShown, chatShown bool
	// Its chrome: the bar's menu / pad / boards / chat switches. Every one of
	// them is a switch and nothing more — no scrim, no close button.
	barHudBtn, barPadBtn, barChatBtn, barOppBtn widget.Clickable
	// The voice chat (voice.go): the game screen's session, guarded by mu
	// (startVoice sets it, stopVoice clears it); voiceDevice builds the
	// platform's audio for each (voice.NewDevice; the tests hand in a
	// voice.FakeDevice); voicePrefs is the saved gate and "Play voice" as
	// a new session starts from them, guarded by mu since the join runs
	// off the UI goroutine. The rest is the menu's VOICE section and the
	// bar's mic button: UI goroutine only.
	voice          *voice.Session
	voiceRoom      string // the room a.voice is in: a game ID, config.LobbyVoiceRoom, or "" (guarded by mu)
	voiceStarting  bool   // a lobby session is on its way (reconcileVoice; guarded by mu)
	voiceDevice    func() voice.Device
	voicePrefs     prefs.Voice
	barMicBtn      widget.Clickable
	voiceGateDb    int
	voiceGateFloat widget.Float
	voiceDirty     bool
	voiceListenCb  widget.Bool
	voiceListenWas bool
	voiceChanEnum  widget.Enum
	hudTag         int // pointer-area tag of a menu column drawn OVER the board: its presses are its own, not the gesture surface's
	chatSeen       int // messages the chat panel last showed: the bar's unread dot
	// screenEng is the engine the screen state above belongs to; a new one
	// (every game entry makes one) shuts the menu and forgets what was read.
	screenEng *engine.Engine
	// The lobby screen's switches (lobby.go), the same idiom one screen over:
	// lobbyMenuShown is the menu column (lobbyMenuVisible), lobbyPlayersShown
	// the players column beside the panel (lobbyPlayersVisible) and
	// lobbyChatShown the chat strip under it (lobbyChatVisible). They start on
	// and save with the game's, but they are the lobby's OWN and not the
	// game's: the two screens stand different things beside their content.
	// UI goroutine only.
	lobbyMenuShown, lobbyPlayersShown, lobbyChatShown    bool
	barLobbyMenuBtn, barLobbyPlayersBtn, barLobbyChatBtn widget.Clickable
	lobbyMenuTag                                         int // pointer-area tag of a lobby menu column drawn OVER the panel
	lobbyChatSeen                                        int // lobby messages the strip last showed: the bar's unread dot
	// The panel's three tabs (lobbyTabGames / lobbyTabHistory / lobbyTabLog)
	// and their chips: the games on offer now, the games already played,
	// and the server log.
	lobbyTab     string
	lobbyTabBtns [3]widget.Clickable
	logLst       widget.List // scrolls the server log tab
	// playerStripLst scrolls the players strip where the column has moved
	// under the panel and there are more players than its lines hold
	// (lobbyPlayersStrip).
	playerStripLst widget.List
	// Touch diagnostic (browser build, view_js.go; the page's ?touchdebug=1):
	// touchDebug switches it on, touchPresses counts the touch presses that
	// reached the game screen (handleGameFocus) and frames the frames laid
	// out, both reported to the page every frame. UI goroutine only.
	touchDebug           bool
	touchPresses, frames int
	// casFlashes counts the CAS-failure flashes received (pumpEngine): each
	// is a move of ours the server rejected and the engine dropped. Written
	// by the pump, read by the diagnostic on the UI goroutine.
	casFlashes atomic.Int64
	// gest is the playfield's touch-gesture recognizer (gesture.go): swipes,
	// taps, drags and flicks on the board, fed by handleGestures every frame.
	// UI goroutine only.
	gest boardGesture
	// shift and soft are the keyboard's auto-repeat machines, one per axis
	// (autoshift.go): ← → on the DAS and ARR knobs (dasMs, arrMs — ms,
	// 0..maxHandlingMs) and ↓ on neither, falling at sdf times the level's
	// gravity (minSDF..maxSDF, maxSDF instant) with no charge at all. The
	// three are mirrored by the HANDLING sliders' positions (dasFloat,
	// arrFloat, sdfFloat); handlingDirty marks a slider change not yet
	// persisted (persistHandling, once the drag ends). UI goroutine only.
	// padLeftWas/padRightWas/padDownWas is the pad arms' Pressed() reading of
	// the last frame — what handlePadShift detects press/release edges
	// against. dropHeld is the space bar's physical state: a hard drop fires
	// on its false→true edge only, so holding it drops once and not once per
	// OS auto-repeat.
	//
	// holdArmed is the Shift key's: Shift holds the piece on its RELEASE, not
	// its press, so that a shifted Tab — still the board/chat switch — moves
	// the keys without also spending the hold. The press arms; the release
	// spends it; a Tab, or losing the keys, disarms it unspent. (C, the
	// other hold key, is not a modifier and holds on the press as ever.)
	//
	// dropGuard is the accidental-drop guard (dropguard.go), the section's
	// fourth knob (dropGuardMs, dropGuardFloat — ms, 0..maxHandlingMs, 0
	// off): it watches the pieces go by and refuses the hard drop for the
	// knob's window around a lock the player did not make.
	shift, soft                         autoShift
	dasMs, arrMs, sdf                   int
	dasFloat, arrFloat, sdfFloat        widget.Float
	handlingDirty                       bool
	padLeftWas, padRightWas, padDownWas bool
	dropHeld                            bool
	holdArmed                           bool
	dropGuard                           dropGuard
	dropGuardMs                         int
	dropGuardFloat                      widget.Float
	// heldMoves are gesture moves made while the board had no piece (the
	// lock-to-spawn gap), dispatched the moment the next piece appears
	// (handleGestures). UI goroutine only. pieceGapStart/spawnGapLast/
	// spawnGapMax time the lock-to-spawn gap as the frames see it and
	// heldPeak is the most moves held at once — the touch diagnostic's
	// (view_js.go).
	heldMoves                 []engine.MoveType
	pieceGapStart             time.Time
	spawnGapLast, spawnGapMax time.Duration
	heldPeak                  int
	// Move-buffer strip animation state (UI goroutine only): the queue length
	// last laid out and when it last grew (drives the newest chip's pop-in).
	bufN      int
	bufGrewAt time.Time

	// game-screen chat panel (game chat + folded-in lobby messages)
	gameChatEd   widget.Editor
	gameChatBtn  widget.Clickable
	gameChatList widget.List
	// hudList and lobbyMenuList scroll the game screen's menu column
	// (hudColumn) and the lobby's (lobbyMenuColumn): each taller than a short
	// window, whatever the screen.
	hudList       widget.List
	lobbyMenuList widget.List
	// loginList scrolls the login screen's column (layoutLogin), and
	// loginColH is the column's height of the frame before, which places it
	// in the window: a phone with its keyboard up has a third of its height.
	loginList widget.List
	loginColH int
	// bv is the browser page's side of the window (view_js.go): the hidden
	// text element the on-screen keyboard is up for. Empty on the desktop.
	bv browserView

	// archive (history) viewer
	archiveSel      *config.ArchiveRecord // the finished game whose boards are being shown
	archiveBtns     []widget.Clickable    // one per history row (indexed by list position)
	archiveBackBtn  widget.Clickable      // "Back to Lobby" from the archive viewer
	archiveChatList widget.List
	// archiveColLst scrolls the archive viewer's stacked column on a compact
	// screen, where the boards, the roster and the chat cannot stand side by
	// side (layoutArchive).
	archiveColLst widget.List // the record's preserved chat history

	// game replay: the history rows' Replay buttons — a click opens the
	// replay straight away — and the replay session shown on screenReplay
	// (replayView's load fields are written by the loader goroutine — guarded
	// by mu). The transport is a tape deck: five keys, a speed selector, and
	// the scrub drag, all of which do nothing but move replayView.head.
	replayBtns      []widget.Clickable  // one per history row (indexed by list position)
	replayBackBtn   widget.Clickable    // "Back to Lobby" from the replay screen
	replayPlayBtn   widget.Clickable    // transport: play / pause
	replayStartBtn  widget.Clickable    // transport: back to the start
	replayRewBtn    widget.Clickable    // transport: back ten recorded seconds
	replayFwdBtn    widget.Clickable    // transport: on ten recorded seconds
	replayEndBtn    widget.Clickable    // transport: jump to the end (the ending reveal)
	replaySpeedBtns [5]widget.Clickable // transport: one per replaySpeeds entry
	replayScrub     gesture.Drag        // transport: the scrub slider's drag
	replayTag       int                 // address used as the replay screen's key-input focus tag
	replayView      *replayView         // the active replay session (nil = none)

	// Horizontal board strips that scroll when the boards together exceed the
	// window width (spectator multi-board views, the archive final playfield,
	// and the replay screen).
	specBoardsList     widget.List
	specTeamBoardsList widget.List
	archiveBoardsList  widget.List
	replayBoardsList   widget.List
}

// New builds the App. The window is created later, in Run, on the UI goroutine.
func New(js jetstream.JetStream, kv jetstream.KeyValue) *App {
	a := &App{
		js:              js,
		kv:              kv,
		screen:          screenLogin,
		countdown:       -1,
		flash:           map[[2]int]time.Time{},
		casWant:         map[[2]int]time.Time{},
		specFlash:       map[int]map[[2]int]time.Time{},
		rowStrobes:      map[int]rowStrobe{},
		specRowStrobes:  map[int]map[int]rowStrobe{},
		specGarbageRows: map[int]int{},
		gameBtns:        map[string]*gameRowBtns{},
		uninviteBtns:    map[string]*widget.Clickable{},
		msgGroupOf:      map[string]int{},
	}
	// The custom rules open at the Guideline preset's settings (they change
	// only what the creator changes), so switching the radio to custom is a
	// starting point and not a step back to the classic game.
	a.setCustomRules(config.GuidelineRules())
	a.setExtraColumns(config.DefaultExtraColumns)
	a.setExtraRows(config.DefaultExtraRows)
	a.setPlayfieldCount(config.DefaultTeamCount)
	a.splitPiecesCb.Value = true // the pieces are dealt out between the players of a playfield unless the creator says otherwise
	a.labEnum.Value = labAsync   // Optimistic async, the default
	a.SetHandling(defaultDASMs, defaultARRMs, defaultSDF, defaultDropGuardMs)
	a.SetKeymap(prefs.DefaultKeymap())
	a.keysLine, a.keysSlot = -1, -1
	a.setDefaultPanels() // every panel on until a saved set says otherwise
	a.SetVoice(prefs.DefaultVoice())
	a.voiceDevice = voice.NewDevice
	a.voiceChanEnum.Value = voiceChanTeam
	// The name is one word: a plain keyboard on a phone, no corrections or
	// predictions over it (hintPlain). The numeric fields get a number pad,
	// and the URL field the address keyboard; a chat line keeps the phone's
	// prose keyboard, corrections and all.
	a.loginEd.SingleLine = true
	a.loginEd.Submit = true
	a.loginEd.InputHint = hintPlain
	a.chatEd.SingleLine = true
	a.chatEd.Submit = true
	a.countEd.SingleLine = true
	a.countEd.InputHint = key.HintNumeric
	a.countEd.SetText("2") // the wizard opens on co-op: a crew of two unless the creator changes it
	a.lineGoalEd.SingleLine = true
	a.lineGoalEd.Filter = "0123456789"
	a.lineGoalEd.InputHint = key.HintNumeric
	a.lineGoalEd.SetText(strconv.Itoa(config.DefaultLineGoal))
	for t, name := range config.DefaultTeamNames(config.MaxTeamCount) {
		a.teamNameEds[t].SingleLine = true
		a.teamNameEds[t].InputHint = hintPlain
		a.teamNameEds[t].SetText(name)
	}
	a.maxAgentsEd.SingleLine = true
	a.maxAgentsEd.Filter = "0123456789"
	a.maxAgentsEd.InputHint = key.HintNumeric
	a.maxAgentsEd.SetText("1")
	a.nextCountEd.SingleLine = true
	a.nextCountEd.Filter = "0123456789"
	a.nextCountEd.InputHint = key.HintNumeric
	a.holesEd.SingleLine = true
	a.holesEd.Filter = "0123456789"
	a.holesEd.InputHint = key.HintNumeric
	a.boardsEnum.Value = "single" // one shared playfield, scored together, until the creator says otherwise
	a.singleKindEnum.Value = "coop"
	a.lengthEnum.Value = "topout"     // until someone tops out
	a.rulesEnum.Value = "guideline"   // the Guideline preset until the creator asks for custom rules
	a.createJoinEnum.Value = "invite" // invite-only by default; open games are the opt-in
	a.histSortEnum.Value = "score"
	// Every crew composition is listed by default; each box hides its class.
	a.histHumansCb.Value = true
	a.histMixedCb.Value = true
	a.histAgentsOnlyCb.Value = true
	a.inviteList.Axis = layout.Vertical
	a.wizList.Axis = layout.Vertical
	a.playerList.Axis = layout.Vertical
	a.gameList.Axis = layout.Vertical
	a.archiveLst.Axis = layout.Vertical
	a.archiveChatList.Axis = layout.Vertical
	a.archiveChatList.ScrollToEnd = true
	a.archiveColLst.Axis = layout.Vertical
	a.chatList.Axis = layout.Vertical
	// The lobby chat is a strip like the game's now, so it follows the
	// conversation like the game's: a message arriving while it is up shows
	// without the reader scrolling for it.
	a.chatList.ScrollToEnd = true
	a.lobbyTab = lobbyTabGames
	a.logLst.Axis = layout.Vertical
	a.playerStripLst.Axis = layout.Vertical
	a.msgList.Axis = layout.Vertical
	a.msgList.ScrollToEnd = true
	a.gameChatEd.SingleLine = true
	a.gameChatEd.Submit = true
	a.gameChatList.Axis = layout.Vertical
	a.gameChatList.ScrollToEnd = true
	a.hudList.Axis = layout.Vertical
	a.lobbyMenuList.Axis = layout.Vertical
	a.loginList.Axis = layout.Vertical
	a.specBoardsList.Axis = layout.Horizontal
	a.specTeamBoardsList.Axis = layout.Horizontal
	a.archiveBoardsList.Axis = layout.Horizontal
	a.replayBoardsList.Axis = layout.Horizontal
	return a
}

// NewWithPicker builds the App for the single combined login screen: name
// entry, the two-tab connection page (NATS server browser / LAN mode) and the
// Play button. favorites are the browser's bookmarks (the caller loads them:
// prefs.LoadFavorites). The app dials NATS itself when the player hits Play,
// and quitting the lobby returns to this same screen (disconnected). CLI
// flags only seed the browser's selection: --server selects that URL (listed
// under COMMAND LINE unless it is already a favorite); --context selects that
// context (added to the list if it isn't among the discovered ones); with
// neither flag the first favorite starts selected (see the precedence below);
// --user/--password ride along in connCfg and apply to URL connects.
// cfg.PlayerName (--name, or the browser page's ?player=) is the exception
// that does connect: it fills the name field and arms the screen's
// one-shot auto-submit (App.autoLogin), so the player lands in the lobby
// without seeing the screen — unless something fails, when they see it with
// the error and their name still in place. A replay link (cfg.ReplayGameID:
// --replay, or ?replay=) with no name does the same under a dealt
// Watcher_ name: nobody is asked who they are on the way to a replay.
func NewWithPicker(cfg config.Config, contexts []string, selected string, favorites []prefs.Favorite) *App {
	a := New(nil, nil)
	a.needConn = true
	a.connCfg = cfg
	// A name given up front fills the field and arms the auto-submit; the
	// screen is still drawn (and still shown on failure), it just doesn't
	// wait to be told what it already knows.
	if cfg.PlayerName == "" && cfg.ReplayGameID != "" {
		cfg.PlayerName = dealName(watcherPrefix)
	}
	if cfg.PlayerName != "" {
		a.loginEd.SetText(cfg.PlayerName)
		a.autoLogin = true
	}
	a.linkedReplay = cfg.ReplayGameID
	a.connContexts = append([]string(nil), contexts...)
	a.connSelected = selected
	a.favorites = append([]prefs.Favorite(nil), favorites...)
	a.favSave = prefs.SaveFavorites
	a.handlingSave = prefs.SaveHandling
	a.keymapSave = prefs.SaveKeymap
	a.panelsSave = prefs.SavePanels
	a.voiceSave = prefs.SaveVoice
	a.connProbes = map[string]probeResult{}
	a.connProbing = map[string]bool{}
	a.connRound = map[string]bool{}
	a.connSecClosed = map[string]bool{}
	a.connSecBtns = map[string]*widget.Clickable{}
	a.connRowBtns = map[string]*widget.Clickable{}
	a.connDelBtns = map[string]*widget.Clickable{}
	a.connTab = connTabBrowser
	a.connBrowserLst.Axis = layout.Vertical
	a.connAddLabelEd.SingleLine = true
	a.connAddLabelEd.Submit = true
	a.connAddLabelEd.InputHint = hintPlain
	a.connAddURLEd.SingleLine = true
	a.connAddURLEd.Submit = true
	a.connAddURLEd.InputHint = key.HintURL
	for _, p := range []struct {
		ed  *widget.Editor
		def int
	}{{&a.connPortEd, config.DefaultEmbeddedPort}, {&a.connWSPortEd, config.DefaultEmbeddedWSPort}, {&a.connHTTPPortEd, config.DefaultEmbeddedHTTPPort}} {
		p.ed.SingleLine = true
		p.ed.Submit = true
		p.ed.Filter = "0123456789"
		p.ed.InputHint = key.HintNumeric
		p.ed.SetText(strconv.Itoa(p.def))
	}
	a.lanIP = natspkg.LanIP()
	a.connNameEd.SingleLine = true
	a.connNameEd.Submit = true
	a.connNameEd.InputHint = hintPlain
	a.connNameEd.SetText(config.DefaultEmbeddedName)
	a.connHostEd.SingleLine = true
	a.connHostEd.Submit = true
	a.connHostEd.InputHint = hintPlain
	// Pre-filled with the auto-detected address so the player sees what will
	// be shared and can correct it (multi-homed hosts, VPNs, containers where
	// the detected interface is not the one friends can reach).
	a.connHostEd.SetText(a.lanIP)

	if cfg.NATSContext != "" && !slices.Contains(a.connContexts, cfg.NATSContext) {
		a.connContexts = append(a.connContexts, cfg.NATSContext)
		sort.Strings(a.connContexts)
	}
	a.connCtxURLs = map[string]string{}
	for _, c := range a.connContexts {
		a.connCtxURLs[c] = natspkg.ContextURL(c)
	}

	// Default selection precedence: --server, then --context, then the first
	// favorite — the bookmarks are the player's own list, so its head is the
	// server they most likely want, ahead of whatever the nats CLI happens to
	// have current — then the CLI's current context, then the first context
	// (a machine with neither starts with nothing selected). A URL this build
	// cannot dial (a nats:// one in the browser) is skipped: it is listed,
	// greyed out, but never selected.
	switch {
	case cfg.NATSURL != "" && dialable(cfg.NATSURL):
		a.connSel = urlKey(cfg.NATSURL)
	case cfg.NATSContext != "":
		a.connSel = ctxKey(cfg.NATSContext)
	case a.firstDialableFavorite() != "":
		a.connSel = a.firstDialableFavorite()
	case selected != "":
		a.connSel = ctxKey(selected)
	case len(a.connContexts) > 0:
		a.connSel = ctxKey(a.connContexts[0])
	}
	return a
}

// dialable reports whether this build can dial a URL (natspkg.Dialable: on
// the desktop everything, in the browser only ws:// and wss://). The server
// browser lists the others greyed out and never selects them. A variable so
// the desktop tests can play the browser.
var dialable = natspkg.Dialable

// firstDialableFavorite is the selection key of the first favorite this
// build can dial, "" when there is none. An outdated one (prefs.Outdated: an
// official server of an earlier release, kept in the list until the player
// cleans it up) is passed over, as by every automatic selection.
func (a *App) firstDialableFavorite() string {
	for _, f := range a.favorites {
		if dialable(f.URL) && !prefs.Outdated(f.URL) {
			return urlKey(f.URL)
		}
	}
	return ""
}

// Shutdown is the process's exit from outside the window: it tears the app
// down as a closed window would (teardown — presence deleted, departure
// journaled, the connection drained), so a Ctrl-C in the terminal leaves the
// lobby as cleanly as a Quit. Safe to call at any time, and more than once.
func (a *App) Shutdown() {
	a.teardown()
}

// pickerActive reports whether the login screen includes the connection
// chooser. True for every NewWithPicker app — the combined screen is the only
// login screen; false only for bare New(js, kv) apps (tests).
func (a *App) pickerActive() bool { return a.needConn }

// newUITheme builds the app's material theme: the 8-bit palette over a shaper
// carrying the Go faces plus the pixel face. Shared by Run and the layout
// tests so snapshots render exactly what the window shows.
func newUITheme() *material.Theme {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(uiFontCollection()))
	th.Palette.Bg = colBg
	th.Palette.Fg = colFg
	th.Palette.ContrastBg = colAccent
	th.Palette.ContrastFg = colBg
	return th
}

// minWinW/minWinH is the smallest size the OS lets the window shrink to:
// wide enough for the HUD column (≥200 dp) plus the move-buffer strip under
// the board (~400 dp) — or the control pad flanking a minimum-cell playfield,
// scaled down to its floor — and the window insets; tall enough for a
// minimum-cell playfield (20 visible rows at the 14 dp fitCellPx floor) plus
// the move-buffer strip and the chat panel (fitBoardAndPad moves the pad
// beside the board before it would cost the board its rows). Below this the
// playfield and controls could no longer be displayed whole.
const (
	minWinW = unit.Dp(760)
	minWinH = unit.Dp(720)
)

// Run creates the window and pumps its event loop until the window is closed.
// It must run on a goroutine other than the one that calls app.Main().
func (a *App) Run(ctx context.Context) error {
	a.ctx = ctx
	a.win = new(app.Window)
	a.win.Option(app.Title("Jetris"), app.Size(unit.Dp(1280), unit.Dp(820)), app.MinSize(minWinW, minWinH))

	a.th = newUITheme()

	var ops op.Ops
	for {
		switch e := a.win.Event().(type) {
		case app.DestroyEvent:
			a.teardown()
			return e.Err
		case app.ViewEvent:
			a.attachView(e) // browser build: keep the keyboard on the game (view_js.go)
		case app.FrameEvent:
			// frameBegin/frameEnd bracket the frame for the browser page
			// (view_js.go): input the browser delivers while the frame is
			// parked on an engine lock must wait for the frame to end.
			a.frameBegin()
			gtx := app.NewContext(&ops, e)
			a.layout(gtx)
			// The tour reads where its parts landed off the frame just laid
			// out (tutorial.go), before the window takes it.
			a.tutorialObserve(gtx.Ops)
			e.Frame(gtx.Ops)
			a.frameEnd(gtx)
		}
	}
}

func (a *App) layout(gtx C) D {
	// Stretch the whole frame to the display (see scale.go) before any
	// screen measures a dp or an sp.
	gtx = scaledContext(gtx)
	// The frame's form factor (formfactor.go): which device, which way up,
	// and how much room there is. Every screen reads it off a.form — the
	// game screen to choose its shape, the others to trim what a phone has
	// no width for.
	a.form = a.formOf(gtx)
	// The voice chat's session for this screen (voice.go): the lobby's
	// while the lobby is up, none on the login, archive and replay screens
	// — a game's is the game screen's own affair.
	a.reconcileVoice()
	// The How to play tour's input and scene (tutorial.go), ahead of the
	// screen it stands over, so the frame draws the step as it is after a
	// click on Next and not before.
	a.tutorialUpdate(gtx)
	paint.Fill(gtx.Ops, colBg)
	a.frames++
	a.touchDebugFrame()
	var d D
	tourGame := a.tutorialScene() == tutSceneGame
	// The share modal belongs to the replay screen and the game-over box;
	// a screen change under it (the game torn down, say) leaves it closed
	// rather than waiting for the next of those screens.
	if s := a.getScreen(); a.shareOpen && s != screenReplay && s != screenGame {
		a.shareOpen = false
	}
	switch a.getScreen() {
	case screenLogin:
		d = a.layoutLogin(gtx)
	case screenLobby:
		if tourGame {
			// The tour's last scene: the game screen over its own engine,
			// the lobby still the screen underneath.
			d = a.layoutGameEngine(gtx, a.tut.gameEngine())
		} else {
			d = a.layoutLobby(gtx)
		}
	case screenGame:
		d = a.layoutGame(gtx)
	case screenArchive:
		d = a.layoutArchive(gtx)
	case screenReplay:
		d = a.layoutReplay(gtx)
	}
	// Build version, top-right corner of every screen — except the game
	// screen, whose top-right corner is the bar's chat button: there the
	// plate rides in the HUD panel, beside the NATS tag (gameHUD).
	if a.getScreen() != screenGame && !tourGame {
		a.versionBadge(gtx)
	}
	// The tour's layer over the screen: the scrim, the lit part, the callout.
	a.tutorialOverlay(gtx)
	// CRT overlay over the whole frame, screens and chrome alike. Deferred —
	// op.Defer runs after everything else, first in first out — so it also
	// covers what a screen paints late: the crown a winning player's board
	// floats over the victory fireworks (crownBoardOnTop).
	macro := op.Record(gtx.Ops)
	scanlines(gtx)
	op.Defer(gtx.Ops, macro.Stop())
	return d
}

// --- locked accessors ---

func (a *App) getScreen() screenKind {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.screen
}

func (a *App) getLobby() *lobby.Lobby {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lobby
}

func (a *App) getEngine() *engine.Engine {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.eng
}

// embeddedAddr returns the shareable address of the embedded server while the
// current connection is to it, "" otherwise (gates the lobby's YOUR SERVER
// lines).
func (a *App) embeddedAddr() string {
	nats, _, _ := a.lanAddrs()
	return nats
}

// lanAddrs is every address the LAN party hands out while the current
// connection is to the embedded server — the NATS one for desktop builds and
// agents, its WebSocket listener for the browser build, and the page the
// browser build is served from — all "<host>:<port>", all "" otherwise.
func (a *App) lanAddrs() (nats, ws, http string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.usingEmbedded {
		return "", "", ""
	}
	return a.embAddr, a.embWSAddr, a.embHTTPAddr
}

func (a *App) snapshotGamePlayers() []lobby.PlayerSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]lobby.PlayerSummary(nil), a.gamePlayers...)
}

// invalidate asks the window for a frame from OUTSIDE the UI goroutine: the
// engine/lobby pumps, the lifecycle goroutines, a timer. Layout code that
// keeps an effect moving must use animate instead. Gio arms window.Invalidate
// only once the UI goroutine has gone idle (app/window.go, mayInvalidate), so
// a call made DURING a frame that an external wake-up started is dropped —
// and with it the next frame, ending the animation right there. On the
// desktop backends the OS event thread re-arms it between frames so the drop
// never shows; in the browser everything runs on one thread and every NATS
// update or timer starts such a frame, which left every animation crawling
// at one frame per wake-up.
func (a *App) invalidate() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

// animate requests the next frame from within the current one — the in-frame
// way to keep an animation running: op.InvalidateCmd rides the frame's own
// state (Router.WakeupTime → Window.setNextFrame), so it is honoured on
// every backend no matter what started this frame.
func animate(gtx C) {
	gtx.Execute(op.InvalidateCmd{})
}
