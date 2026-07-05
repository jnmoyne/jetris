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
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
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
// countdownBaseSp is the settled font size of the centered number.
const (
	countdownAnimDur = 450 * time.Millisecond
	countdownBaseSp  = 132.0
)

// UI chrome colors (the board itself uses internal/render).
var (
	colBg     = color.NRGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}
	colPanel  = color.NRGBA{R: 0x1c, G: 0x1c, B: 0x1c, A: 0xff}
	colFg     = color.NRGBA{R: 0xe6, G: 0xe6, B: 0xe6, A: 0xff}
	colMuted  = color.NRGBA{R: 0x88, G: 0x88, B: 0x88, A: 0xff}
	colAccent = color.NRGBA{R: 0x00, G: 0xcc, B: 0xcc, A: 0xff}
	colErr    = color.NRGBA{R: 0xff, G: 0x55, B: 0x55, A: 0xff}
	colGold   = color.NRGBA{R: 0xff, G: 0xcc, B: 0x00, A: 0xff} // countdown numbers (matches web)
	colGo     = color.NRGBA{R: 0x00, G: 0xff, B: 0x88, A: 0xff} // countdown "GO!" (matches web)
	colWarn   = color.NRGBA{R: 0xff, G: 0xdd, B: 0x00, A: 0xff} // RTT warning start (yellow, at 75 ms)
	colOrange = color.NRGBA{R: 0xff, G: 0x8c, B: 0x00, A: 0xff} // RTT warning end (orange, at 150 ms)
	colLobby  = color.NRGBA{R: 0x7f, G: 0xb2, B: 0xff, A: 0xff} // lobby messages shown inside a game's chat (@lobby)
)

// gameRowBtns are the per-game-listing action buttons (rebuilt lazily per game).
type gameRowBtns struct {
	join     widget.Clickable
	joinA    widget.Clickable // teams mode: join team A
	joinB    widget.Clickable // teams mode: join team B
	spectate widget.Clickable
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

	// chat log (written by pumpLobby)
	chatLog []lobby.ChatMessage

	// NATS message panel: msgShow mirrors the "Show NATS messages" checkbox
	// each frame and gates collection; msgLog holds the tail of game-stream
	// messages tapped via engine.OnStreamMsg (written by consumer goroutines).
	msgShow bool
	msgLog  []streamMsg

	// --- UI-goroutine-only widgets ---
	loginEd      widget.Editor
	loginBtn     widget.Clickable
	collisionYes widget.Clickable
	collisionNo  widget.Clickable
	connEnum     widget.Enum      // connection choice: "ctx:<name>" per context, "url" for the URL row
	connURLEd    widget.Editor    // NATS URL entry (pre-set to the demo server or --server)
	connList     widget.List      // scrollable context radio list
	connCheckBtn widget.Clickable // "Check connection" (connect + ping, no side effects)

	createBtn  widget.Clickable
	modeEnum   widget.Enum
	countEd    widget.Editor
	quitBtn    widget.Clickable
	chatEd     widget.Editor
	chatBtn    widget.Clickable
	playerList widget.List
	gameList   widget.List
	archiveLst widget.List
	chatList   widget.List
	gameBtns   map[string]*gameRowBtns

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
	archiveSel     *config.ArchiveRecord // the finished game whose boards are being shown
	archiveBtns    []widget.Clickable    // one per history row (indexed by list position)
	archiveBackBtn widget.Clickable      // "Back to Lobby" from the archive viewer
}

// New builds the App. The window is created later, in Run, on the UI goroutine.
func New(js jetstream.JetStream, kv jetstream.KeyValue) *App {
	a := &App{
		js:        js,
		kv:        kv,
		screen:    screenLogin,
		countdown: -1,
		flash:     map[[2]int]time.Time{},
		gameBtns:  map[string]*gameRowBtns{},
	}
	a.loginEd.SingleLine = true
	a.loginEd.Submit = true
	a.chatEd.SingleLine = true
	a.chatEd.Submit = true
	a.countEd.SingleLine = true
	a.countEd.SetText("2")
	a.modeEnum.Value = "cooperative"
	a.playerList.Axis = layout.Vertical
	a.gameList.Axis = layout.Vertical
	a.archiveLst.Axis = layout.Vertical
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
// entry plus a CONNECT TO section (one radio per known NATS CLI context plus a
// NATS URL entry). The app dials NATS itself when the player hits Play, and
// quitting the lobby returns to this same screen (disconnected). CLI flags
// only seed the picker's defaults: --server pre-fills the URL field and makes
// the URL option the starting choice; --context preselects that context radio
// (added to the list if it isn't among the discovered ones); --user/--password
// ride along in connCfg and apply to URL connects.
func NewWithPicker(cfg config.Config, contexts []string, selected string) *App {
	a := New(nil, nil)
	a.needConn = true
	a.connCfg = cfg
	a.connContexts = contexts
	a.connSelected = selected
	a.connURLEd.SingleLine = true
	a.connURLEd.Submit = true
	a.connURLEd.SetText(DefaultNATSURL)
	a.connList.Axis = layout.Vertical

	// Default choice precedence: --server, then --context, then the CLI's
	// currently selected context, then the always-available URL option.
	switch {
	case cfg.NATSURL != "":
		a.connURLEd.SetText(cfg.NATSURL)
		a.connEnum.Value = "url"
	case cfg.NATSContext != "":
		if !slices.Contains(a.connContexts, cfg.NATSContext) {
			a.connContexts = append(a.connContexts, cfg.NATSContext)
			sort.Strings(a.connContexts)
		}
		a.connEnum.Value = "ctx:" + cfg.NATSContext
	case selected != "":
		a.connEnum.Value = "ctx:" + selected
	default:
		a.connEnum.Value = "url"
	}
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

// Run creates the window and pumps its event loop until the window is closed.
// It must run on a goroutine other than the one that calls app.Main().
func (a *App) Run(ctx context.Context) error {
	a.ctx = ctx
	a.win = new(app.Window)
	a.win.Option(app.Title("Jetricks"), app.Size(unit.Dp(1200), unit.Dp(820)))

	a.th = material.NewTheme()
	a.th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	a.th.Palette.Bg = colBg
	a.th.Palette.Fg = colFg
	a.th.Palette.ContrastBg = colAccent
	a.th.Palette.ContrastFg = colBg

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
	switch a.getScreen() {
	case screenLogin:
		return a.layoutLogin(gtx)
	case screenLobby:
		return a.layoutLobby(gtx)
	case screenGame:
		return a.layoutGame(gtx)
	case screenArchive:
		return a.layoutArchive(gtx)
	}
	return D{}
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
