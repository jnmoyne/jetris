package nativeui

// Form factor — which device the game is being played on, which way it is
// held, and how much of the screen the playfield may therefore claim.
//
// The game screen comes in two shapes. The FULL one is the three-column
// arcade cabinet this app was designed as: the HUD column, the board column,
// the opponent thumbnails, and the chat strip across the bottom — a desktop
// window, or a tablet held landscape. The COMPACT one (compact.go) is
// everything else, phones above all: one slim top bar carrying the HOLD box,
// the score and the NEXT pieces, the playfield under it with the whole rest
// of the screen to itself, and the HUD and the chat behind a tap — panels
// that slide over the board only while they are wanted, so nothing
// permanently owns space the playfield could have.
//
// Which shape a frame gets is decided here, per frame, from the device and
// the window: a phone always, a tablet held portrait (its short side spends
// too much of itself on side columns), and any window too small for the full
// layout to be worth it — the desktop app dragged down to its minimum size
// included. The device itself is the browser's to say (view_js.go reads the
// page's media queries and screen size and hints it here); everywhere else it
// is inferred from the touch flag and the window's own dp size, which is what
// a headless test drives.

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

// drawerOpen reports whether one of the compact screen's panels is over the
// board — the frame's presses belong to it, not to the game (the pad's
// clicks and the playfield's gestures are gated on this).
func (a *App) drawerOpen() bool { return a.hudDrawer || a.chatDrawer }
