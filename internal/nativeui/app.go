// Package nativeui is the native (Gio) player front end for Jetricks. It drives
// the lobby and engine business logic, draws directly into an OS window, and
// reads engine.Updates / lobby.Updates straight off their Go channels, so a
// NATS update reaches the screen within one display frame.
package nativeui

import (
	"context"
	"image/color"
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

	"github.com/nats-io/nats.go/jetstream"

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
)

// gameRowBtns are the per-game-listing action buttons (rebuilt lazily per game).
type gameRowBtns struct {
	join     widget.Clickable
	spectate widget.Clickable
}

// App holds all native-UI state. Fields read or written by more than one
// goroutine (the engine/lobby pumps plus the UI goroutine) are guarded by mu.
// Gio widget values are touched only by the UI goroutine and need no lock.
type App struct {
	js jetstream.JetStream
	kv jetstream.KeyValue

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
	ping         time.Duration // latest publish→echo round trip from the engine
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

	// --- UI-goroutine-only widgets ---
	loginEd      widget.Editor
	loginBtn     widget.Clickable
	collisionYes widget.Clickable
	collisionNo  widget.Clickable

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
	boardTag int // address used as the key-input focus tag
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
	return a
}

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
