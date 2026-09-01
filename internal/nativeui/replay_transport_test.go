package nativeui

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
)

// replayRig drives the replay screen through a real Gio router, so the
// transport's keys and its scrub track are hit-tested exactly as they are in
// the app — and the frame clock is the test's, so playback can be advanced a
// second at a time without waiting one.
type replayRig struct {
	a   *App
	rv  *replayView
	r   *input.Router
	sz  image.Point
	now time.Time
}

func newReplayRig(t *testing.T) *replayRig {
	t.Helper()
	a := newTestApp()
	rv := loadedReplay(sampleReplayRecord())
	rv.head = 0
	a.replayView = rv
	a.screen = screenReplay
	g := &replayRig{a: a, rv: rv, r: new(input.Router), sz: image.Pt(1200, 820),
		now: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)}
	g.frame(0)
	return g
}

// frame lays the screen out through the router after d of wall time, the way
// the window loop does.
func (g *replayRig) frame(d time.Duration) {
	g.now = g.now.Add(d)
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(g.sz),
		Source:      g.r.Source(),
		Now:         g.now,
	}
	g.a.layout(gtx)
	g.r.Frame(ops)
}

// tap presses and releases the transport key carrying a label.
func (g *replayRig) tap(t *testing.T, label string) {
	t.Helper()
	r, ok := buttonBounds(g.r, label)
	if !ok {
		t.Fatalf("no %q key on the replay screen", label)
	}
	c := r.Min.Add(r.Size().Div(2))
	touch(g.r, pointer.Press, 1, float32(c.X), float32(c.Y), 0)
	touch(g.r, pointer.Release, 1, float32(c.X), float32(c.Y), 60*time.Millisecond)
	g.frame(0)
	g.frame(0)
}

// scrubTo presses the scrub track at a fraction of its width, the way a thumb
// or a mouse cues the replay.
func (g *replayRig) scrubTo(t *testing.T, f float64) {
	t.Helper()
	r, ok := semanticBounds(g.r, replayScrubLabel)
	if !ok {
		t.Fatal("no scrub track on the replay screen")
	}
	pad := replayTrackPad // the rig runs at 1 px per dp
	x := float32(r.Min.X+pad) + float32(f)*float32(r.Dx()-2*pad)
	y := float32(r.Min.Y + r.Dy()/2)
	touch(g.r, pointer.Press, 1, x, y, 0)
	touch(g.r, pointer.Release, 1, x, y, 60*time.Millisecond)
	g.frame(0)
	g.frame(0)
}

// buttonBounds is the window-px rectangle of the semantic BUTTON whose label
// text is s. The label itself is a child node whose bounds are the text's own
// (unclipped, and so useless for a tap); the button around it is the widget.
func buttonBounds(r *input.Router, s string) (image.Rectangle, bool) {
	var labelled func([]input.SemanticNode) bool
	labelled = func(ns []input.SemanticNode) bool {
		for _, n := range ns {
			if n.Desc.Label == s || labelled(n.Children) {
				return true
			}
		}
		return false
	}
	var find func([]input.SemanticNode) (image.Rectangle, bool)
	find = func(ns []input.SemanticNode) (image.Rectangle, bool) {
		for _, n := range ns {
			if n.Desc.Class == semantic.Button && labelled(n.Children) {
				return n.Desc.Bounds, true
			}
			if b, ok := find(n.Children); ok {
				return b, true
			}
		}
		return image.Rectangle{}, false
	}
	return find(r.AppendSemantics(nil))
}

// The transport drives the playhead and nothing else: the keys move it, the
// speed multiplies the frame clock, and the ending is wherever the playhead
// happens to be — reached by playing to the end, and gone again the moment it
// is scrubbed away from.
func TestReplayTransportDrivesThePlayhead(t *testing.T) {
	g := newReplayRig(t)
	rv, dur := g.rv, g.rv.tl.dur

	// Loaded and playing: a second of wall clock is a second of game.
	if !rv.playing {
		t.Fatal("a loaded replay does not open playing")
	}
	g.frame(time.Second)
	if got := rv.head; got < 900*time.Millisecond || got > 1100*time.Millisecond {
		t.Fatalf("after a second at 1x the playhead is at %v, want ≈1s", got)
	}

	// PAUSE stops the clock dead.
	g.tap(t, "PAUSE")
	if rv.playing {
		t.Fatal("PAUSE did not pause")
	}
	held := rv.head
	g.frame(2 * time.Second)
	if rv.head != held {
		t.Fatalf("a paused playhead moved from %v to %v", held, rv.head)
	}

	// Skipping works paused, forwards and back, and never leaves the tape.
	g.tap(t, "10s >>")
	if want := held + replaySkip; rv.head != want {
		t.Fatalf("skip forward left the playhead at %v, want %v", rv.head, want)
	}
	g.tap(t, "<< 10s")
	if rv.head != held {
		t.Fatalf("skip back left the playhead at %v, want %v", rv.head, held)
	}
	g.tap(t, "<< 10s")
	if rv.head != 0 {
		t.Fatalf("skipping back past the start left the playhead at %v, want 0", rv.head)
	}

	// The slider cues straight to a moment, forwards or backwards.
	for _, f := range []float64{0.5, 0.25, 0.9, 0} {
		g.scrubTo(t, f)
		want := time.Duration(f * float64(dur))
		if d := rv.head - want; d > 3*time.Second || d < -3*time.Second {
			t.Errorf("cueing to %.2f put the playhead at %v, want ≈%v", f, rv.head, want)
		}
	}

	// Speed multiplies the clock: at 4× a second of wall time is four of game.
	g.scrubTo(t, 0)
	g.tap(t, "4x")
	if rv.speed != 4 {
		t.Fatalf("speed = %v, want 4", rv.speed)
	}
	g.tap(t, "PLAY")
	g.frame(time.Second)
	if got := rv.head; got < 3500*time.Millisecond || got > 4500*time.Millisecond {
		t.Errorf("after a second at 4x the playhead is at %v, want ≈4s", got)
	}

	// The end key runs the tape out: the playhead stops at the end, playback
	// stops with it, and the ending is revealed.
	g.tap(t, ">|")
	if rv.head != dur || rv.playing {
		t.Fatalf("the end key left the playhead at %v (playing=%v), want %v stopped", rv.head, rv.playing, dur)
	}
	if !rv.done {
		t.Fatal("the playhead at the end did not reveal the ending")
	}
	// ... and scrubbing back into the game packs it away again.
	g.scrubTo(t, 0.5)
	if rv.done {
		t.Fatal("the ending outlived the playhead being scrubbed off it")
	}
	// PLAY from the end starts over rather than sitting on the last frame.
	g.tap(t, ">|")
	g.tap(t, "PLAY")
	if rv.head != 0 || !rv.playing {
		t.Fatalf("PLAY at the end left the playhead at %v (playing=%v), want it playing from 0", rv.head, rv.playing)
	}
}

// Playing past the end stops there — the tape does not run off its reel. And
// no single frame carries it more than a second, so a window that stopped
// painting for a minute comes back where it was, not at the end.
func TestReplayPlaybackStopsAtTheEnd(t *testing.T) {
	g := newReplayRig(t)
	rv := g.rv
	rv.head = rv.tl.dur - 5*time.Second
	g.frame(time.Minute) // a long stall carries a second, no more
	if got, want := rv.head, rv.tl.dur-4*time.Second; got != want || !rv.playing {
		t.Fatalf("after a stalled frame the playhead is at %v (playing=%v), want %v still playing", got, rv.playing, want)
	}
	for i := 0; i < 6; i++ {
		g.frame(time.Second)
	}
	if rv.head != rv.tl.dur || rv.playing {
		t.Fatalf("playhead %v playing=%v, want %v stopped", rv.head, rv.playing, rv.tl.dur)
	}
}

// The countdown is drawn from the timeline as the playhead crosses it, at
// whatever speed and in whichever direction it crosses — including scrubbed
// backwards into it from the middle of the game.
func TestReplayCountdownFollowsThePlayhead(t *testing.T) {
	g := newReplayRig(t)
	rv := g.rv
	for _, tc := range []struct {
		head time.Duration
		want int
	}{
		{0, 5}, {2500 * time.Millisecond, 3}, {5 * time.Second, 0},
		{time.Minute, -1}, {1500 * time.Millisecond, 4},
	} {
		rv.head, rv.playing = tc.head, false
		g.frame(0)
		if rv.shownN != tc.want {
			t.Errorf("at %v the countdown shows %d, want %d", tc.head, rv.shownN, tc.want)
		}
	}
}

// A replay of a game whose recording is one long moment still lays out, and
// its transport still cues: a zero-length timeline is a division waiting to
// happen in the scrubber.
func TestReplayZeroLengthTimeline(t *testing.T) {
	a := newTestApp()
	rv := newReplayView(config.ArchiveRecord{GameID: "g", Mode: config.ModeCooperative, PlayerCount: 1})
	rv.tl = &replayTimeline{}
	a.replayView = rv
	a.screen = screenReplay
	renderOnce(t, a)
	rv.cue(time.Minute, time.Now())
	if rv.head != 0 {
		t.Fatalf("cueing a zero-length replay put the playhead at %v, want 0", rv.head)
	}
}
