package nativeui

// Form factor — which device the game is being played on, which way it is
// held, and how much room there is to spend.
//
// There is ONE game screen (gamescreen.go): a slim bar across the top
// carrying the score and the switches for everything that costs the playfield
// room, and the board with all the rest. Every switch is just that — the menu
// column, the opponents' boards, the chat strip, the on-screen pad show or
// they do not, and none of them takes the game away while it is up. A phone
// and a desktop get the same screen; what changes between them is what that
// screen can afford, which is what this file works out.
//
// `compact` is that affordability, not a second layout: a phone always, a
// tablet held portrait, and any window too small to spend freely — the
// desktop app dragged down to its minimum size included. Under it the
// move-buffer chips shrink, captions run smaller, the pads move to the
// playfield's bottom edge where the thumbs are, and the boxes that sit
// beside the board stack under it instead.
//
// The device itself is the browser's to say (view_js.go reads the page's
// media queries and screen size and hints it here); everywhere else it is
// inferred from the touch flag and the window's own dp size, which is what a
// headless test drives.

// deviceKind is the machine the game is being played on.
type deviceKind uint8

const (
	deviceDesktop deviceKind = iota // a mouse and a keyboard, in a window
	deviceTablet                    // a coarse pointer on a big screen
	devicePhone                     // a coarse pointer on a small one
)

func (d deviceKind) String() string {
	switch d {
	case deviceTablet:
		return "tablet"
	case devicePhone:
		return "phone"
	}
	return "desktop"
}

const (
	// phoneShortSideDp is the short side (dp) up to which a touch screen is
	// a phone rather than a tablet: every current phone in either
	// orientation is under it (a 6.7" one is 430 dp across), every tablet
	// over it (the smallest iPad is 744 dp across).
	phoneShortSideDp = 560
	// compactW/compactH are the window (dp) the FULL game screen wants: the
	// HUD column (200 dp at its floor) beside a playfield worth the name and
	// the opponent thumbnails past it; the chat strip under 24 rows of
	// board. Below either, the columns and the strip cost the playfield more
	// than they are worth and the compact screen takes over.
	compactW = 900
	compactH = 700
)

// screenForm is one frame's form factor: the device, the window in dp, the
// way it is held, and whether the compact game screen is the one to draw.
// App.form holds the current frame's (set at the top of layoutGame — the UI
// goroutine's, like every other layout field).
type screenForm struct {
	device   deviceKind
	w, h     int // the frame in dp, at the frame's own (display-stretched) metric
	portrait bool
	compact  bool
}

// formOf reads the frame's form factor. gtx is the SCALED context (scale.go),
// so its dp are the ones every other screen measures itself in: a display big
// enough to stretch the metric reports about the design window's size and
// stays on the full layout, as it should.
func (a *App) formOf(gtx C) screenForm {
	f := screenForm{device: deviceDesktop}
	if m := gtx.Metric.PxPerDp; m > 0 {
		f.w, f.h = int(float32(gtx.Constraints.Max.X)/m), int(float32(gtx.Constraints.Max.Y)/m)
	}
	f.portrait = f.h > f.w
	switch {
	case a.deviceHinted:
		f.device = a.deviceHint // the browser's own reading of the page (view_js.go)
	case a.touchUI:
		f.device = deviceTablet
		if min(f.w, f.h) <= phoneShortSideDp {
			f.device = devicePhone
		}
	}
	f.compact = f.device == devicePhone ||
		(f.device == deviceTablet && f.portrait) ||
		f.w < compactW || f.h < compactH
	return f
}

// padVisible reports whether the on-screen control pad is laid out at all.
// It is the player's call (the game screen's pad button flips padPref, which
// then holds for the session); until they make one, the default is the one
// that leaves the playfield biggest: everywhere but a phone held portrait the
// pad is free — the board is bound by the screen's height, and the pad sits
// beside it in room the playfield could not have used anyway — while in
// portrait it sits UNDER the board and takes a third of its rows, which the
// touch gestures (gesture.go) already play without.
func (a *App) padVisible() bool {
	switch {
	case a.padPref > 0:
		return true
	case a.padPref < 0:
		return false
	}
	return !(a.form.device == devicePhone && a.form.portrait)
}

// oppVisible reports whether the opponents' playfields show beside the
// board. As with the pad, it is the player's call once they make one (the
// bar's boards button sets oppPref, which then holds for the session); until
// then it follows the room. A screen with width to spare shows them, as the
// game always has on a desktop; a narrow one does not, because there every
// dp of that column is dp the playfield does not get.
func (a *App) oppVisible() bool {
	switch {
	case a.oppPref > 0:
		return true
	case a.oppPref < 0:
		return false
	}
	return !a.narrowWells()
}

// chatVisible reports whether the chat strip shows under the board. The
// bar's chat button is the player's say (chatPref) and holds for the
// session; until they use it the screen decides, as it does for the pad and
// the opponents: a screen with room keeps the conversation up, the way this
// game always has on a desktop, and a narrow one gives those rows to the
// playfield until asked.
func (a *App) chatVisible() bool {
	switch {
	case a.chatPref > 0:
		return true
	case a.chatPref < 0:
		return false
	}
	return !a.form.compact
}

// hudVisible reports whether the menu column (the stats, the controls legend,
// the lab switches, Back to Lobby) stands beside the board. The bar's menu
// button is the player's say (hudPref) and holds for the session; until they
// use it the screen decides, as it does for the others. A screen with room
// keeps the column up — it is where the lab switches live and the game plays
// on beside it, which is how this game looked on a desktop before the column
// went behind a button — while a compact one starts without it, because there
// the menu has to be drawn OVER the board (hudBeside) and the game would
// begin with the playfield covered.
func (a *App) hudVisible() bool {
	switch {
	case a.hudPref > 0:
		return true
	case a.hudPref < 0:
		return false
	}
	return !a.form.compact
}
