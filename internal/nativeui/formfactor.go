package nativeui

// Form factor — which device the game is being played on, which way it is
// held, and how much room there is to spend.
//
// There is ONE game screen (gamescreen.go): a slim bar across the top
// carrying the score and the switches for everything that costs the playfield
// room, and the board with all the rest. Every switch is just that — the menu
// column, the opponents' boards, the chat strip, the on-screen pad show or
// they do not, and none of them takes the game away while it is up. Whether
// each one is showing is the player's standing choice and lives in panels.go;
// what this file works out is what the screen can AFFORD — a phone and a
// desktop get the same screen and the same panels, and what changes between
// them is where those panels go.
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
