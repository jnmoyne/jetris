package nativeui

import (
	"image"
	"reflect"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	"jetris/internal/prefs"
)

// TestKeyLines pins the legend's shape to the constants the dialog is sized
// by: keyLineCount lines, the hold line at keyHoldLine, every action on
// exactly one line, no line over keyLineActionsMax actions and no action's
// defaults over keySlotsMax keys.
func TestKeyLines(t *testing.T) {
	if len(keyLines) != keyLineCount {
		t.Fatalf("keyLines has %d lines, keyLineCount is %d", len(keyLines), keyLineCount)
	}
	if got := keyLines[keyHoldLine].actions; !reflect.DeepEqual(got, []prefs.KeyAction{prefs.KeyHold}) {
		t.Fatalf("keyLines[keyHoldLine] is %v, want the hold line", got)
	}
	seen := map[prefs.KeyAction]int{}
	def := prefs.DefaultKeymap()
	for _, line := range keyLines {
		if len(line.actions) > keyLineActionsMax {
			t.Errorf("line %q has %d actions, over keyLineActionsMax", line.title, len(line.actions))
		}
		for _, act := range line.actions {
			seen[act]++
			if len(def[act]) > keySlotsMax {
				t.Errorf("%s has %d default keys, over keySlotsMax", act, len(def[act]))
			}
		}
	}
	for _, act := range prefs.KeyActions {
		if seen[act] != 1 {
			t.Errorf("action %s is on %d lines, want 1", act, seen[act])
		}
	}
}

// The legend prints the scheme in play: the defaults as they always read,
// the keys of a move together — and a rebound key in its place. The
// lobby's KEYS rows are buttons under a note that says so; the game's are
// plain, and carry the hold line only for a game with the hold queue.
func TestLegendFollowsTheScheme(t *testing.T) {
	a := newTestApp()
	want := []string{"← → A D", "↓ S", "↑ W X", "Z CTRL", "SPACE", "C SHIFT"}
	for i, w := range want {
		if got := a.keys.lineKeys(i); got != w {
			t.Errorf("default line %d reads %q, want %q", i, got, w)
		}
	}
	km := prefs.DefaultKeymap()
	km[prefs.KeyMoveLeft] = []string{"J", "A"}
	km[prefs.KeyHardDrop] = []string{"⏎"}
	a.SetKeymap(km)
	if got := a.keys.lineKeys(0); got != "J → A D" {
		t.Errorf("rebound move line reads %q, want %q", got, "J → A D")
	}
	if got := a.keys.lineKeys(4); got != "⏎" {
		t.Errorf("rebound hard drop line reads %q, want %q", got, "⏎")
	}

	lobbyKeys := a.controlsSections(false, true)[0]
	if lobbyKeys.header != "KEYS" || lobbyKeys.note == "" {
		t.Fatalf("the lobby's KEYS section: header %q, note %q; want KEYS with a note", lobbyKeys.header, lobbyKeys.note)
	}
	moves := func(rows []controlsRow) []string {
		var ms []string
		for _, r := range rows {
			ms = append(ms, r.move)
		}
		return ms
	}
	if got := moves(lobbyKeys.rows); !reflect.DeepEqual(got, []string{"move · hold slides", "soft drop · hold falls", "rotate CW", "rotate CCW", "hard drop", "chat / board"}) {
		t.Errorf("the lobby's KEYS rows: %v (no hold line, Tab last)", got)
	}
	for _, r := range lobbyKeys.rows {
		if (r.btn == nil) != (r.move == "chat / board") {
			t.Errorf("row %q: button %v; want one on every row but Tab's", r.move, r.btn != nil)
		}
	}
	gameKeys := a.controlsSections(true, false)[0]
	if gameKeys.note != "" {
		t.Errorf("the game's KEYS section carries the lobby's note %q", gameKeys.note)
	}
	if got := moves(gameKeys.rows); got[5] != "hold" || len(got) != 7 {
		t.Errorf("the game's KEYS rows: %v (hold before Tab)", got)
	}
	for _, r := range gameKeys.rows {
		if r.btn != nil {
			t.Errorf("row %q of the game's legend is a button", r.move)
		}
	}
}

// TestKeysDialogRebinds drives the lobby through a Gio router: a press on
// the legend's move line opens the dialog on it; a press on its A button
// arms that slot, and the next key goes into it — saved, and in the legend
// at once; a key another move has is refused with its name and leaves the
// slot armed, as is a reserved one; Escape disarms, then closes; Defaults
// puts the line's keys back.
func TestKeysDialogRebinds(t *testing.T) {
	g := newLobbyRig(t, image.Pt(1280, 820), deviceDesktop)
	a := g.a
	var saved prefs.Keymap
	a.keymapSave = func(k prefs.Keymap) error { saved = k.Clone(); return nil }
	tapButton := func(label string) {
		t.Helper()
		b, ok := buttonBounds(g.r, label)
		if !ok {
			t.Fatalf("no button labelled %q on screen", label)
		}
		c := b.Min.Add(b.Size().Div(2))
		g.tap(float32(c.X), float32(c.Y))
	}
	press := func(name key.Name) {
		g.r.Queue(key.Event{Name: name, State: key.Press})
		g.r.Queue(key.Event{Name: name, State: key.Release})
		g.frame()
		g.frame()
	}

	tapButton("move · hold slides")
	if a.keysLine != 0 {
		t.Fatalf("after the move line: keysLine = %d, want 0", a.keysLine)
	}
	if !g.r.Source().Focused(&a.keysTag) {
		t.Fatal("the dialog did not take the keys")
	}
	tapButton("A")
	if a.keysSlot != 1 {
		t.Fatalf("after the A button: keysSlot = %d, want 1 (move left's second key)", a.keysSlot)
	}
	press("Q")
	if got := a.keys.km[prefs.KeyMoveLeft]; !reflect.DeepEqual(got, []string{"←", "Q"}) {
		t.Fatalf("after Q: move left = %v, want [← Q]", got)
	}
	if a.keysSlot != -1 || a.keysErr != "" {
		t.Errorf("after a good key: slot %d, err %q; want disarmed and clear", a.keysSlot, a.keysErr)
	}
	if saved == nil || !reflect.DeepEqual(saved[prefs.KeyMoveLeft], []string{"←", "Q"}) {
		t.Errorf("the change was not saved: %v", saved)
	}
	if got := a.keys.lineKeys(0); got != "← → Q D" {
		t.Errorf("the legend reads %q, want %q", got, "← → Q D")
	}
	if act, ok := a.keys.actionFor("Q"); !ok || act != prefs.KeyMoveLeft {
		t.Errorf("Q makes %q, %v; want move left", act, ok)
	}
	if _, ok := a.keys.actionFor("A"); ok {
		t.Error("A still makes a move after being replaced")
	}

	// D, move right's: Q is refused as move left's, Tab as reserved, and
	// the slot stays armed through both.
	tapButton("D")
	if a.keysSlot != keySlotsMax+1 {
		t.Fatalf("after the D button: keysSlot = %d, want %d", a.keysSlot, keySlotsMax+1)
	}
	press("Q")
	if a.keysErr != "Q is already used for move left" || a.keysSlot != keySlotsMax+1 {
		t.Errorf("Q on move right: err %q, slot %d; want the refusal with the slot armed", a.keysErr, a.keysSlot)
	}
	press(key.NameTab)
	if a.keysErr != "TAB is reserved" || a.keysSlot != keySlotsMax+1 {
		t.Errorf("Tab on move right: err %q, slot %d; want the refusal with the slot armed", a.keysErr, a.keysSlot)
	}
	if got := a.keys.km[prefs.KeyMoveRight]; !reflect.DeepEqual(got, []string{"→", "D"}) {
		t.Errorf("move right changed under a refused key: %v", got)
	}
	press(key.NameEscape)
	if a.keysSlot != -1 || a.keysLine != 0 {
		t.Errorf("Escape on an armed slot: slot %d, line %d; want disarmed, dialog up", a.keysSlot, a.keysLine)
	}

	// Defaults: A comes back.
	tapButton("Defaults")
	if got := a.keys.km[prefs.KeyMoveLeft]; !reflect.DeepEqual(got, []string{"←", "A"}) {
		t.Errorf("after Defaults: move left = %v, want [← A]", got)
	}
	press(key.NameEscape)
	if a.keysLine != -1 {
		t.Errorf("Escape with no slot armed left the dialog up (line %d)", a.keysLine)
	}
	if _, ok := buttonBounds(g.r, "Done"); ok {
		t.Error("the dialog is still drawn after closing")
	}
}

// A default key another move has is left to it when a line's defaults come
// back: the reset is the line's, not the scheme's.
func TestResetLineKeysLeavesOthersKeys(t *testing.T) {
	a := newTestApp()
	km := prefs.DefaultKeymap()
	km[prefs.KeyMoveLeft] = []string{"J", "K"}
	km[prefs.KeyRotateCW] = []string{"A", "W", "X"} // rotate CW took move left's A
	a.SetKeymap(km)
	a.resetLineKeys(0)
	if got := a.keys.km[prefs.KeyMoveLeft]; !reflect.DeepEqual(got, []string{"←", "K"}) {
		t.Errorf("after the move line's reset: move left = %v, want [← K] (A is rotate CW's)", got)
	}
	if got := a.keys.km[prefs.KeyRotateCW]; !reflect.DeepEqual(got, []string{"A", "W", "X"}) {
		t.Errorf("the move line's reset touched rotate CW: %v", got)
	}
}

// A rebound key drives the piece through the same router the defaults go
// through: Q as rotate CW rotates, and A, no longer a key, does nothing.
// (Move left keeps two keys: the scheme holds every action to its default
// number of slots, a slot left empty coming back as its default.)
func TestReboundKeyDrivesThePiece(t *testing.T) {
	a := newTestApp()
	a.SetHandling(60, 0, defaultSDF, defaultDropGuardMs)
	km := prefs.DefaultKeymap()
	km[prefs.KeyRotateCW] = []string{"↑", "Q", "X"}
	km[prefs.KeyMoveLeft] = []string{"←", "J"}
	a.SetKeymap(km)
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	at := time.Unix(4000, 0)
	gameFrameAt(a, &r, at)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys")
	}
	for _, k := range []key.Name{"Q", "A", "D"} {
		at = at.Add(10 * time.Millisecond)
		r.Queue(key.Event{Name: k, State: key.Press})
		gameFrameAt(a, &r, at)
		at = at.Add(10 * time.Millisecond)
		r.Queue(key.Event{Name: k, State: key.Release})
		gameFrameAt(a, &r, at)
	}
	want := []engine.MoveType{engine.RotateCW, engine.MoveRight}
	if got := a.eng.BufferedMoves(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after tapping Q A D: BufferedMoves = %v, want %v", got, want)
	}
	if strings.Contains(a.keys.lineKeys(0), "A") {
		t.Errorf("the legend still lists A: %q", a.keys.lineKeys(0))
	}
}
