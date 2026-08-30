package nativeui

import (
	"image"
	"testing"
)

// TestFormOf pins the form-factor reading (formfactor.go): a mouse is a
// desktop whatever the window; a touch screen is a phone or a tablet by its
// short side; the browser's own hint overrides the guess; and the compact
// game screen is chosen for phones, for tablets held portrait, and for any
// window too small for the full three-column one — the desktop app dragged
// down to its minimum size included.
func TestFormOf(t *testing.T) {
	for _, c := range []struct {
		name       string
		sz         image.Point
		touch      bool
		hint       deviceKind
		hinted     bool
		wantDevice deviceKind
		portrait   bool
		compact    bool
	}{
		{name: "desktop window", sz: image.Pt(1280, 820), wantDevice: deviceDesktop},
		{name: "desktop at its minimum", sz: image.Pt(760, 720), wantDevice: deviceDesktop, compact: true},
		{name: "short desktop window", sz: image.Pt(1400, 640), wantDevice: deviceDesktop, compact: true},
		{name: "tablet landscape", sz: image.Pt(1180, 740), touch: true, wantDevice: deviceTablet},
		{name: "tablet portrait", sz: image.Pt(820, 1180), touch: true, wantDevice: deviceTablet, portrait: true, compact: true},
		{name: "phone portrait", sz: image.Pt(390, 844), touch: true, wantDevice: devicePhone, portrait: true, compact: true},
		{name: "phone landscape", sz: image.Pt(844, 390), touch: true, wantDevice: devicePhone, compact: true},
		// The browser's hint wins: a phone's window is nearly a tablet's
		// size in landscape, and a tablet's split-screen half is narrow.
		{name: "hinted phone in a wide window", sz: image.Pt(1000, 760), touch: true,
			hint: devicePhone, hinted: true, wantDevice: devicePhone, compact: true},
		{name: "hinted tablet, narrow window", sz: image.Pt(500, 900), touch: true,
			hint: deviceTablet, hinted: true, wantDevice: deviceTablet, portrait: true, compact: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newTestApp()
			a.touchUI, a.deviceHint, a.deviceHinted = c.touch, c.hint, c.hinted
			f := a.formOf(testCtx(c.sz.X, c.sz.Y))
			if f.device != c.wantDevice {
				t.Errorf("device = %v, want %v", f.device, c.wantDevice)
			}
			if f.portrait != c.portrait {
				t.Errorf("portrait = %v, want %v", f.portrait, c.portrait)
			}
			if f.compact != c.compact {
				t.Errorf("compact = %v, want %v", f.compact, c.compact)
			}
			if f.w != c.sz.X || f.h != c.sz.Y {
				t.Errorf("dp size = %dx%d, want %v", f.w, f.h, c.sz)
			}
		})
	}
}

// TestPadVisibleDefaults: the on-screen pad starts off only where it would
// cost the playfield its rows — a phone held portrait, where it stacks under
// the board — and one tap on the bar's pad button settles it either way for
// the rest of the session, whatever the device then does.
func TestPadVisibleDefaults(t *testing.T) {
	for _, c := range []struct {
		name string
		form screenForm
		want bool
	}{
		{"desktop", screenForm{device: deviceDesktop}, true},
		{"tablet portrait", screenForm{device: deviceTablet, portrait: true}, true},
		{"phone landscape", screenForm{device: devicePhone}, true},
		{"phone portrait", screenForm{device: devicePhone, portrait: true}, false},
	} {
		a := newTestApp()
		a.form = c.form
		if got := a.padVisible(); got != c.want {
			t.Errorf("%s: padVisible = %v, want %v", c.name, got, c.want)
		}
	}
	a := newTestApp()
	a.form = screenForm{device: devicePhone, portrait: true}
	a.padPref = 1
	if !a.padVisible() {
		t.Error("phone portrait, pad switched on: still hidden")
	}
	a.form = screenForm{device: deviceDesktop}
	a.padPref = -1
	if a.padVisible() {
		t.Error("desktop, pad switched off: still shown")
	}
}

// TestDrawerOpen: either panel counts, and both closed is a reachable board.
func TestDrawerOpen(t *testing.T) {
	a := newTestApp()
	if a.drawerOpen() {
		t.Fatal("a fresh screen has a panel open")
	}
	a.hudDrawer = true
	if !a.drawerOpen() {
		t.Fatal("the HUD panel is open and drawerOpen says otherwise")
	}
	a.hudDrawer, a.chatDrawer = false, true
	if !a.drawerOpen() {
		t.Fatal("the chat panel is open and drawerOpen says otherwise")
	}
}
