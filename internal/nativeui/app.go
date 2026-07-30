// Package nativeui is the native (Gio) player front end for Jetricks. It drives
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
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
	natspkg "jetricks/internal/nats"
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
	colBg        = color.NRGBA{R: 0x0d, G: 0x0d, B: 0x16, A: 0xff}
	colPanel     = color.NRGBA{R: 0x16, G: 0x16, B: 0x24, A: 0xff}
	colBorder    = color.NRGBA{R: 0x2c, G: 0x2c, B: 0x44, A: 0xff} // panel frames
	colShadow    = color.NRGBA{A: 0x8c}                            // hard offset shadow under buttons/dialogs
	colFg        = color.NRGBA{R: 0xe6, G: 0xe6, B: 0xe6, A: 0xff}
	colMuted     = color.NRGBA{R: 0x8a, G: 0x8a, B: 0x9e, A: 0xff}
	colAccent    = color.NRGBA{R: 0x27, G: 0xaa, B: 0xe1, A: 0xff} // NATS brand blue
	colNATSGreen = color.NRGBA{R: 0x8d, G: 0xc6, B: 0x3f, A: 0xff} // NATS brand green
	colErr       = color.NRGBA{R: 0xff, G: 0x55, B: 0x55, A: 0xff}
	colGold      = color.NRGBA{R: 0xff, G: 0xcc, B: 0x00, A: 0xff} // countdown numbers (matches web)
	colGo        = color.NRGBA{R: 0x00, G: 0xff, B: 0x88, A: 0xff} // countdown "GO!" (matches web)
	colWarn      = color.NRGBA{R: 0xff, G: 0xdd, B: 0x00, A: 0xff} // RTT warning start (yellow, at 75 ms)
	colOrange    = color.NRGBA{R: 0xff, G: 0x8c, B: 0x00, A: 0xff} // RTT warning end (orange, at 150 ms)
	colLobby     = color.NRGBA{R: 0x7f, G: 0xb2, B: 0xff, A: 0xff} // lobby messages shown inside a game's chat (@lobby)
)

// gameRowBtns are the per-game-listing action buttons (rebuilt lazily per game).
type gameRowBtns struct {
	join     widget.Clickable
	joinA    widget.Clickable // teams mode: join team A
	joinB    widget.Clickable // teams mode: join team B
	spectate widget.Clickable
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
	// connects.
	needConn     bool
	connContexts []string
	connSelected string
	connCfg      config.Config

	// "Check connection" result state (written by doCheckConn; guarded by mu)
	connChecking bool
	connCheckOK  bool
	connCheckMsg string

	// Embedded server ("LAN mode (embedded NATS server)" option; guarded by
	// mu). The server starts on the first embedded login and runs until the
	// window closes — quitting to the login screen leaves it up for connected
	// friends; picking a different port on a later login restarts it there.
	// usingEmbedded marks the CURRENT connection as being to it, which is
	// what gates the lobby's shareable-address line.
	embSrv        *natsserver.Server
	embAddr       string // shareable "<lan-ip>:<port>"
	usingEmbedded bool

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
	loggingIn      bool
	loginErr       string
	loginCollision bool

	// game render snapshot (written by pumpEngine)
	score        int
	level        int
	teamScores   [config.TeamCount]int // teams: live per-team scores
	teamLevels   [config.TeamCount]int // teams: live per-team levels
	rtt          time.Duration         // latest publish→echo round trip from the engine
	gameStatus   string
	countdown    int       // -1 none, 0 GO!, >0 seconds remaining
	countdownAt  time.Time // when the current countdown number arrived (for the pop animation)
	gameOver     bool
	won          bool
	myReady      bool
	readyPlayers []lobby.PlayerSummary
	flash        map[[2]int]time.Time
	// specFlash holds CAS-failure flashes for SPECTATOR boards, broadcast
	// by players over core NATS. Keyed by board index — the flashing
	// player's global index (competitive) or team (teams). A player's own
	// board flash lives in `flash`, not here.
	specFlash map[int]map[[2]int]time.Time
	fireworks *fireworksShow // victory fireworks show; nil until a competitive/teams win

	// chat log (written by pumpLobby)
	chatLog []lobby.ChatMessage

	// NATS message panel: msgShow mirrors the "Show NATS messages" checkbox
	// each frame and gates collection; msgLog holds the tail of game-stream
	// messages tapped via engine.OnStreamMsg (written by consumer goroutines).
	msgShow bool
	msgLog  []streamMsg

	// --- UI-goroutine-only widgets ---
	loginEd        widget.Editor
	loginBtn       widget.Clickable
	collisionYes   widget.Clickable
	collisionNo    widget.Clickable
	connEnum       widget.Enum        // connection choice: "context" for the context pull-down, "url" for the URL row
	connCtx        string             // context chosen in the pull-down (seeded from --context or the CLI's selected context)
	connDropOpen   bool               // whether the context pull-down list is expanded
	connDropBtn    widget.Clickable   // the pull-down button itself
	connOptBtns    []widget.Clickable // one per context row in the expanded pull-down
	connURLEd      widget.Editor      // NATS URL entry (pre-set to the demo server or --server)
	connURLSeeded  bool               // swallow the ChangeEvent queued by the constructor's SetText (it isn't a user edit)
	connPortEd     widget.Editor      // LAN-mode port entry (pre-set to config.DefaultEmbeddedPort)
	connPortSeeded bool               // same SetText-ChangeEvent swallow as connURLSeeded
	lanIP          string             // this machine's LAN address, resolved once for the shareable-URL lines
	connList       widget.List        // scrollable pull-down option list
	connCheckBtn   widget.Clickable   // "Check connection" (connect + ping, no side effects)

	createBtn     widget.Clickable
	modeEnum      widget.Enum
	countEd       widget.Editor
	allowAgentsCb widget.Bool   // competitive create: allow idle agents to take seats
	maxAgentsEd   widget.Editor // competitive create: how many seats agents may take
	quitBtn       widget.Clickable
	chatEd        widget.Editor
	chatBtn       widget.Clickable
	playerList    widget.List
	gameList      widget.List
	archiveLst    widget.List
	// Game-history controls: sort selector ("score"/"date") and the
	// show-games-with-agents filter (checked = shown).
	histSortEnum widget.Enum
	histAgentsCb widget.Bool
	chatList     widget.List
	gameBtns     map[string]*gameRowBtns
	// uninviteBtns are the per-invitation Uninvite/Dismiss buttons on the
	// creator's invite-only game rows, keyed "<gameID>|<inviteeID>".
	uninviteBtns map[string]*widget.Clickable
	// confirmDeleteID is the abandoned game whose row currently shows the
	// "Are you sure you want to delete this game?" confirmation ("" = none).
	confirmDeleteID string
	// confirmLeave is true while the game screen asks "Are you sure you want
	// to leave?" (leaving an in-progress game needs confirmation; the seat is
	// kept and the lobby offers Rejoin).
	confirmLeave bool
	leaveYesBtn  widget.Clickable
	leaveNoBtn   widget.Clickable

	// Invite-only create flow. inviteOnlyCb toggles it on the create row.
	// While invitePickerGameID is non-empty the invitee-picker overlay is
	// open for that just-created game; invitePicker holds one row of widget
	// state per selectable player (keyed by player ID). Selecting a player
	// sends their invitation IMMEDIATELY (deselecting retracts it) — there is
	// no send button. The creator appears as a pinned first row, pre-selected:
	// selecting yourself means playing (a seat is taken right away — creating
	// an invitation game implies accepting your own invitation), deselecting
	// frees the seat and you'll spectate instead once the game fills.
	inviteOnlyCb       widget.Bool
	invitePickerGameID string
	invitePicker       map[string]*inviteChoice
	invitePickerErr    string          // capacity-guard message shown in the picker
	invitePickerMode   config.GameMode // captured when the picker opens
	invitePickerPC     int             // playerCount of the game being invited to
	invitePickerTS     int             // teamSize (teams mode)
	inviteSelfSel      widget.Bool     // the pinned "You" row (non-teams): checked = playing
	inviteSelfTeam     widget.Enum     // the pinned "You" row (teams): "", "0" or "1"
	inviteSelfLastSel  bool            // last self intent applied (non-teams)
	inviteSelfLastTeam string          // last self intent applied (teams)
	inviteList         widget.List
	inviteCloseBtn     widget.Clickable // keep the game (and its invites), just close the overlay
	inviteCancelBtn    widget.Clickable // abandon: retract the invites and delete the game
	// Incoming-invitation pop-up (reads lobby.MyInvite at draw time).
	inviteAcceptBtn  widget.Clickable
	inviteDeclineBtn widget.Clickable

	readyBtn widget.Clickable
	backBtn  widget.Clickable
	showMsgs widget.Bool // "Show NATS messages" checkbox
	msgList  widget.List
	boardTag int // address used as the key-input focus tag

	// game-screen chat panel (game chat + folded-in lobby messages)
	gameChatEd   widget.Editor
	gameChatBtn  widget.Clickable
	gameChatList widget.List

	// archive (history) viewer
	archiveSel      *config.ArchiveRecord // the finished game whose boards are being shown
	archiveBtns     []widget.Clickable    // one per history row (indexed by list position)
	archiveBackBtn  widget.Clickable      // "Back to Lobby" from the archive viewer
	archiveChatList widget.List           // the record's preserved chat history
}

// New builds the App. The window is created later, in Run, on the UI goroutine.
func New(js jetstream.JetStream, kv jetstream.KeyValue) *App {
	a := &App{
		js:           js,
		kv:           kv,
		screen:       screenLogin,
		countdown:    -1,
		flash:        map[[2]int]time.Time{},
		specFlash:    map[int]map[[2]int]time.Time{},
		gameBtns:     map[string]*gameRowBtns{},
		uninviteBtns: map[string]*widget.Clickable{},
	}
	a.loginEd.SingleLine = true
	a.loginEd.Submit = true
	a.chatEd.SingleLine = true
	a.chatEd.Submit = true
	a.countEd.SingleLine = true
	a.countEd.SetText("2")
	a.maxAgentsEd.SingleLine = true
	a.maxAgentsEd.Filter = "0123456789"
	a.maxAgentsEd.SetText("1")
	a.modeEnum.Value = "cooperative"
	a.histSortEnum.Value = "score"
	a.histAgentsCb.Value = true // agent games shown by default
	a.inviteList.Axis = layout.Vertical
	a.playerList.Axis = layout.Vertical
	a.gameList.Axis = layout.Vertical
	a.archiveLst.Axis = layout.Vertical
	a.archiveChatList.Axis = layout.Vertical
	a.archiveChatList.ScrollToEnd = true
	a.chatList.Axis = layout.Vertical
	a.msgList.Axis = layout.Vertical
	a.msgList.ScrollToEnd = true
	a.gameChatEd.SingleLine = true
	a.gameChatEd.Submit = true
	a.gameChatList.Axis = layout.Vertical
	a.gameChatList.ScrollToEnd = true
	return a
}

// DefaultNATSURL pre-fills the login screen's NATS URL field.
const DefaultNATSURL = "nats://demo.nats.io:4222"

// NewWithPicker builds the App for the single combined login screen: name
// entry plus a CONNECT TO section (a "Context:" pull-down over the known NATS
// CLI contexts plus a NATS URL entry). The app dials NATS itself when the
// player hits Play, and quitting the lobby returns to this same screen
// (disconnected). CLI flags only seed the picker's defaults: --server
// pre-fills the URL field and makes the URL option the starting choice;
// --context presets the pull-down (added to the list if it isn't among the
// discovered ones); --user/--password ride along in connCfg and apply to URL
// connects.
func NewWithPicker(cfg config.Config, contexts []string, selected string) *App {
	a := New(nil, nil)
	a.needConn = true
	a.connCfg = cfg
	a.connContexts = contexts
	a.connSelected = selected
	a.connURLEd.SingleLine = true
	a.connURLEd.Submit = true
	a.connURLEd.SetText(DefaultNATSURL)
	a.connURLSeeded = true // the SetText above queues a ChangeEvent; don't let it pick the URL option
	a.connPortEd.SingleLine = true
	a.connPortEd.Submit = true
	a.connPortEd.Filter = "0123456789"
	a.connPortEd.SetText(strconv.Itoa(config.DefaultEmbeddedPort))
	a.connPortSeeded = true // same swallow as connURLSeeded
	a.lanIP = natspkg.LanIP()
	a.connList.Axis = layout.Vertical

	// Default choice precedence: --server, then --context, then the CLI's
	// currently selected context, then the always-available URL option. The
	// pull-down itself is preset to --context / the CLI's selected context
	// (falling back to the first known context) whichever option starts out.
	a.connCtx = selected
	switch {
	case cfg.NATSURL != "":
		a.connURLEd.SetText(cfg.NATSURL)
		a.connEnum.Value = "url"
	case cfg.NATSContext != "":
		if !slices.Contains(a.connContexts, cfg.NATSContext) {
			a.connContexts = append(a.connContexts, cfg.NATSContext)
			sort.Strings(a.connContexts)
		}
		a.connCtx = cfg.NATSContext
		a.connEnum.Value = "context"
	case selected != "":
		a.connEnum.Value = "context"
	default:
		a.connEnum.Value = "url"
	}
	if a.connCtx == "" && len(a.connContexts) > 0 {
		a.connCtx = a.connContexts[0]
	}
	a.connOptBtns = make([]widget.Clickable, len(a.connContexts))
	return a
}

// DrainConn drains the app-owned NATS connection, if any. Safe to call at any
// point in the lifecycle (a.nc is nil until the player connects).
func (a *App) DrainConn() {
	a.mu.Lock()
	nc := a.nc
	a.mu.Unlock()
	if nc != nil {
		nc.Drain()
	}
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

// Run creates the window and pumps its event loop until the window is closed.
// It must run on a goroutine other than the one that calls app.Main().
func (a *App) Run(ctx context.Context) error {
	a.ctx = ctx
	a.win = new(app.Window)
	a.win.Option(app.Title("Jetricks"), app.Size(unit.Dp(1280), unit.Dp(820)))

	a.th = newUITheme()

	var ops op.Ops
	for {
		switch e := a.win.Event().(type) {
		case app.DestroyEvent:
			a.teardown()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (a *App) layout(gtx C) D {
	paint.Fill(gtx.Ops, colBg)
	var d D
	switch a.getScreen() {
	case screenLogin:
		d = a.layoutLogin(gtx)
	case screenLobby:
		d = a.layoutLobby(gtx)
	case screenGame:
		d = a.layoutGame(gtx)
	case screenArchive:
		d = a.layoutArchive(gtx)
	}
	scanlines(gtx) // CRT overlay over the whole frame, screens and chrome alike
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
// current connection is to it, "" otherwise (gates the lobby's YOUR SERVER line).
func (a *App) embeddedAddr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.usingEmbedded {
		return ""
	}
	return a.embAddr
}

func (a *App) snapshotGamePlayers() []lobby.PlayerSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]lobby.PlayerSummary(nil), a.gamePlayers...)
}

func (a *App) invalidate() {
	if a.win != nil {
		a.win.Invalidate()
	}
}
