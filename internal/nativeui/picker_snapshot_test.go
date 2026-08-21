package nativeui

// Opt-in visual verification for the login screen's connection page: renders
// the server browser (with probe results, a collapsed section, the add form)
// and the LAN-mode tab via a headless GPU window and writes PNGs for
// inspection. Skipped unless FW_SNAPSHOT_DIR is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestPickerSnapshots

import (
	"image"
	"image/png"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/prefs"
)

func TestPickerSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render picker snapshots")
	}
	w, err := headless.NewWindow(1200, 820)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	a := NewWithPicker(config.Config{}, []string{"alpha", "beta", "demo", "prod-cluster"}, "beta",
		append(prefs.DefaultFavorites(), prefs.Favorite{Label: "home lab", URL: "nats://192.168.1.20:4222"}))
	a.th = newTestApp().th
	a.connCtxURLs["beta"] = "nats://beta.example.com:4222"
	a.connProbes[urlKey(prefs.DemoFavorite.URL)] = probeResult{ok: true, msg: "✓ nats://demo.nats.io:4222 · Core NATS ping 38 ms · 3 players online", rtt: 38 * time.Millisecond, players: 3, lobby: true}
	a.connProbes[urlKey("nats://192.168.1.20:4222")] = probeResult{msg: "✗ dial tcp 192.168.1.20:4222: connection refused"}

	for _, st := range []struct {
		name  string
		setup func()
	}{
		{"browser", func() { a.connSel = urlKey(prefs.DemoFavorite.URL) }},
		{"browser_add", func() { a.connAddOpen = true; a.connSecClosed[secContexts] = true }},
		{"lan", func() { a.connAddOpen = false; a.connSecClosed[secContexts] = false; a.connTab = connTabLAN }},
		{"browser_update", func() {
			a.connTab = connTabBrowser
			a.NotifyUpdate("v0.6.0", "https://github.com/jnmoyne/jetris/releases/tag/v0.6.0")
		}},
	} {
		st.setup()
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1200, 820)),
		}
		a.layout(gtx)
		if err := w.Frame(&ops); err != nil {
			t.Fatalf("frame %s: %v", st.name, err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 1200, 820))
		if err := w.Screenshot(img); err != nil {
			t.Fatalf("screenshot %s: %v", st.name, err)
		}
		f, err := os.Create(dir + "/picker_" + st.name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
