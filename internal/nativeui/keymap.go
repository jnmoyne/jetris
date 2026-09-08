package nativeui

import (
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/engine"
	"jetris/internal/prefs"
)

// The key bindings: which keys make which move. The scheme is the player's
// (prefs.Keymap, kept across launches): every line of the lobby's KEYS legend
// opens a small dialog on the keys it lists, and any of them can be swapped
// for another key — one no other move has. The touch gestures are fixed.

// keyLine is one line of the KEYS legend: the move it names and the actions
// behind it — one, or the two halves of "move".
type keyLine struct {
	title   string // the dialog's title
	move    string // the legend's move column
	actions []prefs.KeyAction
}

// keyLines is the legend, in order. The index into it is what a line's
// button and the dialog carry (keysLine).
var keyLines = []keyLine{
	{"MOVE", "move · hold slides", []prefs.KeyAction{prefs.KeyMoveLeft, prefs.KeyMoveRight}},
	{"SOFT DROP", "soft drop · hold falls", []prefs.KeyAction{prefs.KeySoftDrop}},
	{"ROTATE CW", "rotate CW", []prefs.KeyAction{prefs.KeyRotateCW}},
	{"ROTATE CCW", "rotate CCW", []prefs.KeyAction{prefs.KeyRotateCCW}},
	{"HARD DROP", "hard drop", []prefs.KeyAction{prefs.KeyHardDrop}},
	{"HOLD", "hold", []prefs.KeyAction{prefs.KeyHold}},
}

// keyLineCount is how many lines the legend has (the size of the lobby's
// line buttons, keysLineBtns), and keyHoldLine the index of the hold line,
// left out of a legend for a game without the hold queue (controlsSections).
// Both are pinned to keyLines by TestKeyLines.
const (
	keyLineCount = 6
	keyHoldLine  = 5
)

// keySlotsMax is the most keys one action has (rotate CW's ↑ W X), and
// keyLineActionsMax the most actions one line has (move's two halves):
// together they size the dialog's slot buttons (keysSlotBtns).
const (
	keySlotsMax        = 3
	keyLineActionsMax  = 2
	keySlotButtonCount = keySlotsMax * keyLineActionsMax
)

// keyActionLabels is how the dialog names each action — and how a conflict
// says whose key it is.
var keyActionLabels = map[prefs.KeyAction]string{
	prefs.KeyMoveLeft:  "move left",
	prefs.KeyMoveRight: "move right",
	prefs.KeySoftDrop:  "soft drop",
	prefs.KeyRotateCW:  "rotate CW",
	prefs.KeyRotateCCW: "rotate CCW",
	prefs.KeyHardDrop:  "hard drop",
	prefs.KeyHold:      "hold",
}

// keyLabel is a key's name as the legend prints it: Gio's names, in
// capitals — the letters already are, and SPACE, CTRL and SHIFT join them;
// the arrows are their own glyphs.
func keyLabel(name string) string { return strings.ToUpper(name) }

// keyBinds is the scheme in play: the map, and the reverse of it — which
// action a key makes — for the dispatch (handleKeys).
type keyBinds struct {
	km     prefs.Keymap
	action map[key.Name]prefs.KeyAction
}

func newKeyBinds(km prefs.Keymap) keyBinds {
	b := keyBinds{km: km.Normalize(), action: map[key.Name]prefs.KeyAction{}}
	for _, act := range prefs.KeyActions {
		for _, n := range b.km[act] {
			b.action[key.Name(n)] = act
		}
	}
	return b
}

// actionFor is the move name makes; ok is false for a key no move has.
func (b keyBinds) actionFor(name key.Name) (prefs.KeyAction, bool) {
	act, ok := b.action[name]
	return act, ok
}

// names is every bound key, for the board's filters (boardKeyFilters).
func (b keyBinds) names() []key.Name {
	var ns []key.Name
	for _, act := range prefs.KeyActions {
		for _, n := range b.km[act] {
			ns = append(ns, key.Name(n))
		}
	}
	return ns
}

// lineKeys is the legend's key column for line i: the actions' keys slot by
// slot across the actions — move's first slots are ← →, its second A D —
// so the defaults read "← → A D".
func (b keyBinds) lineKeys(i int) string {
	var parts []string
	for slot := 0; slot < keySlotsMax; slot++ {
		for _, act := range keyLines[i].actions {
			if keys := b.km[act]; slot < len(keys) {
				parts = append(parts, keyLabel(keys[slot]))
			}
		}
	}
	return strings.Join(parts, " ")
}

// moveForAction maps an action to the engine call it makes. Pure, so the
// scheme can be unit-tested. ok is false for an action with no plain call:
// handleKeys runs the moves with a machine of their own (← → ↓, the hard
// drop and hold) itself, and this is the table for the rest.
func moveForAction(act prefs.KeyAction) (func(*engine.Engine), bool) {
	switch act {
	case prefs.KeyMoveLeft:
		return (*engine.Engine).MoveLeft, true
	case prefs.KeyMoveRight:
		return (*engine.Engine).MoveRight, true
	case prefs.KeySoftDrop:
		return (*engine.Engine).MoveDown, true
	case prefs.KeyRotateCW:
		return (*engine.Engine).RotateCW, true
	case prefs.KeyRotateCCW:
		return (*engine.Engine).RotateCCW, true
	case prefs.KeyHardDrop:
		return (*engine.Engine).HardDrop, true
	case prefs.KeyHold:
		return (*engine.Engine).Hold, true
	}
	return nil, false
}

// SetKeymap puts the scheme in play: the loaded preferences' way in
// (cmd/jetris/main.go), before Run.
func (a *App) SetKeymap(km prefs.Keymap) {
	a.keys = newKeyBinds(km)
}

// persistKeymap saves the scheme. A failure is silent: the in-memory
// bindings still drive this session.
func (a *App) persistKeymap() {
	if a.keymapSave == nil {
		return
	}
	_ = a.keymapSave(a.keys.km)
}

// --- the dialog ---

// handleKeysModal dispatches the legend's lines and the dialog's own parts,
// and reports whether the dialog is up. A line's press opens the dialog on
// that line — unless another modal is up, when the press is spent and not
// answered, as the bar's are (handleLobbyBarClicks). In the dialog a key
// button's press arms its slot (keysSlot), and the next key pressed goes
// into it: any key that is not reserved and makes no other move, or the
// dialog says whose it is and waits on. Escape disarms the slot, and closes
// the dialog when none is armed; Done closes it; Defaults puts the line's
// keys back, those that are free.
//
// While the dialog is up it holds the keyboard (keysTag): the filter names
// no key, which in Gio matches every key not matched by another filter, so
// the one pressed arrives whatever it is — bar ⌘ and Alt, whose chords are
// the platform's and are neither tolerated nor bindable.
func (a *App) handleKeysModal(gtx C, modal bool) bool {
	for i := range a.keysLineBtns {
		if a.keysLineBtns[i].Clicked(gtx) && !modal {
			a.keysLine, a.keysSlot, a.keysErr = i, -1, ""
		}
	}
	if a.keysLine < 0 {
		return false
	}
	if a.keysDoneBtn.Clicked(gtx) {
		a.keysLine = -1
		return false
	}
	if a.keysResetBtn.Clicked(gtx) {
		a.resetLineKeys(a.keysLine)
	}
	for i := range a.keysSlotBtns {
		if a.keysSlotBtns[i].Clicked(gtx) {
			a.keysSlot, a.keysErr = i, ""
		}
	}
	tag := &a.keysTag
	if !gtx.Source.Focused(tag) {
		gtx.Source.Execute(key.FocusCmd{Tag: tag})
	}
	for {
		ev, ok := gtx.Source.Event(
			key.FocusFilter{Target: tag},
			key.Filter{Focus: tag, Optional: key.ModShift | key.ModCtrl},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press || a.keysLine < 0 {
			continue
		}
		switch {
		case ke.Name == key.NameEscape && a.keysSlot >= 0:
			a.keysSlot, a.keysErr = -1, ""
		case ke.Name == key.NameEscape:
			a.keysLine = -1
		case a.keysSlot >= 0:
			a.bindSlot(a.keysLine, a.keysSlot, string(ke.Name))
		}
	}
	return a.keysLine >= 0
}

// slotAction is the action and key index a dialog slot button stands for,
// on line i; ok is false for a button the line does not use.
func slotAction(i, slot int) (act prefs.KeyAction, k int, ok bool) {
	j, k := slot/keySlotsMax, slot%keySlotsMax
	if j >= len(keyLines[i].actions) {
		return "", 0, false
	}
	return keyLines[i].actions[j], k, true
}

// bindSlot puts name into slot of line i. A reserved key, or one another
// move has, is refused with a word on why, and the slot stays armed for the
// next press; the slot's own key pressed again changes nothing and disarms
// it. A change is saved at once.
func (a *App) bindSlot(i, slot int, name string) {
	act, k, ok := slotAction(i, slot)
	if !ok || k >= len(a.keys.km[act]) {
		a.keysSlot = -1
		return
	}
	if !prefs.KeyBindable(name) {
		a.keysErr = keyLabel(name) + " is reserved"
		return
	}
	if other := a.keys.km.ActionOf(name); other != "" {
		if other == act && a.keys.km[act][k] == name {
			a.keysSlot, a.keysErr = -1, ""
			return
		}
		a.keysErr = keyLabel(name) + " is already used for " + keyActionLabels[other]
		return
	}
	km := a.keys.km.Clone()
	km[act][k] = name
	a.keys = newKeyBinds(km)
	a.keysSlot, a.keysErr = -1, ""
	a.persistKeymap()
}

// resetLineKeys puts line i's default keys back. A default key that some
// move OUTSIDE the line has meanwhile is left where it is, and that slot
// keeps its current key: the reset is this line's, not the whole scheme's.
// (A kept key that a restored default then doubles — a scheme only a
// hand-edited file makes — is settled the way a saved file is, by
// newKeyBinds' normalizing.)
func (a *App) resetLineKeys(i int) {
	inLine := map[prefs.KeyAction]bool{}
	for _, act := range keyLines[i].actions {
		inLine[act] = true
	}
	km := a.keys.km.Clone()
	def := prefs.DefaultKeymap()
	for _, act := range keyLines[i].actions {
		for k, dflt := range def[act] {
			if k >= len(km[act]) {
				break
			}
			if other := km.ActionOf(dflt); other == "" || inLine[other] {
				km[act][k] = dflt
			}
		}
	}
	a.keys = newKeyBinds(km)
	a.keysSlot, a.keysErr = -1, ""
	a.persistKeymap()
}

// keysOverlay is the dialog: the line's title, a row per action — its name
// and a button per key, the armed one saying PRESS A KEY — the word on what
// the dialog is waiting for or refusing, and Defaults and Done. It fills
// the window (the scrim under it is the lobby's, lobby.go), the box in the
// middle of it.
func (a *App) keysOverlay(gtx C) D {
	line := keyLines[a.keysLine]
	// The dialog is its key target's area: a tag holds the keys only while
	// the frame's ops carry it (pointerArea's event.Op), as the board's
	// does on the game screen.
	return pointerArea(gtx, &a.keysTag, func(gtx C) D {
		return a.keysDialog(gtx, line)
	})
}

// keysDialog is the dialog's box, centered.
func (a *App) keysDialog(gtx C, line keyLine) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = modalW(gtx, 460)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						kids := []layout.FlexChild{
							layout.Rigid(a.pixel(unit.Sp(13), line.title+" KEYS", colAccent).Layout),
							layout.Rigid(spacer(8)),
							layout.Rigid(a.body("Click a key, then press the one to use in its place. Any key that makes no other move will do.", colMuted)),
							layout.Rigid(spacer(14)),
						}
						// The action names in a column of their own, the
						// widest setting it, so the key buttons line up.
						nameW := 0
						for _, act := range line.actions {
							nameW = max(nameW, a.pixelWidth(gtx, unit.Sp(9), keyActionLabels[act]))
						}
						for j, act := range line.actions {
							j, act := j, act
							kids = append(kids, layout.Rigid(func(gtx C) D {
								return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
									row := []layout.FlexChild{
										layout.Rigid(func(gtx C) D {
											gtx.Constraints.Min.X = nameW + gtx.Dp(12)
											return a.pixel(unit.Sp(9), keyActionLabels[act], colFg).Layout(gtx)
										}),
									}
									for k, name := range a.keys.km[act] {
										slot := j*keySlotsMax + k
										label := keyLabel(name)
										armed := slot == a.keysSlot
										if armed {
											label = "PRESS A KEY"
										}
										row = append(row,
											layout.Rigid(hSpacer(8)),
											layout.Rigid(func(gtx C) D { return a.keyButton(gtx, &a.keysSlotBtns[slot], label, armed) }),
										)
									}
									return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
								})
							}))
						}
						status, col := "", colMuted
						switch {
						case a.keysErr != "":
							status, col = a.keysErr, colErr
						case a.keysSlot >= 0:
							status = "Waiting for a key… Esc cancels."
						}
						kids = append(kids,
							layout.Rigid(spacer(10)),
							layout.Rigid(func(gtx C) D {
								if status == "" {
									return D{}
								}
								return a.body(status, col)(gtx)
							}),
							layout.Rigid(spacer(16)),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.keysResetBtn, "Defaults") }),
									layout.Rigid(hSpacer(10)),
									layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.keysDoneBtn, "Done") }),
								)
							}),
						)
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
					})
				})
			})
		})
	})
}

// keyButton is one key of the dialog: the secondary chrome at the legend's
// scale, gold while it is the armed slot.
func (a *App) keyButton(gtx C, btn *widget.Clickable, label string, armed bool) D {
	col := colAccent
	if armed {
		col = colGold
	}
	return widget.Border{Color: col, Width: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
		b := pixelize(material.Button(a.th, btn, label))
		b.Background = colPanel
		b.Color = col
		b.TextSize = unit.Sp(9)
		b.Inset = layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(10), Right: unit.Dp(10)}
		return b.Layout(gtx)
	})
}
