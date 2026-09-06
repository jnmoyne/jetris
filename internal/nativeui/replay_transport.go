package nativeui

import (
	"image"
	"math"
	"time"

	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/render"
)

// The replay's transport: the deck of a tape machine for a recorded game.
// Every control here does exactly one thing — move the playhead, or change
// how fast it moves — because the recording is already in memory
// (replay_timeline.go) and the picture on screen is nothing but the boards
// seeked to wherever the playhead stands.

const (
	replayTicksH   = 22 // dp: the band of clear markers above the slider
	replaySliderH  = 16 // dp: the scrub slider under them
	replayTrackPad = 7  // dp: track inset, so the thumb never hangs off either end
	replayThumbW   = 8  // dp
	replayTickW    = 3  // dp
	// replayScrubLabel is the scrubber's semantic label — how a screen reader
	// (and the tests) find the track on screen.
	replayScrubLabel = "replay timeline"
	// replayScrubMaxW caps the scrubber's width: past this the track stops
	// growing and stays centered under the boards.
	replayScrubMaxW = 1000
)

// replayTransportEvents drains the transport's input before the frame is laid
// out, so the boards this frame paints are already at the playhead the click
// or the drag just asked for. The drag is measured against the track as it was
// laid out LAST frame (replayScrubber records it) — the same one-frame-old
// geometry every drag in the app works from.
func (a *App) replayTransportEvents(gtx C, rv *replayView) {
	dur := rv.tl.dur
	if a.replayPlayBtn.Clicked(gtx) {
		if rv.head >= dur {
			rv.head = 0 // play again, from the top
		}
		rv.playing = !rv.playing
		rv.anchor = gtx.Now
	}
	if a.replayStartBtn.Clicked(gtx) {
		rv.cue(0, gtx.Now)
	}
	if a.replayRewBtn.Clicked(gtx) {
		rv.cue(rv.head-replaySkip, gtx.Now)
	}
	if a.replayFwdBtn.Clicked(gtx) {
		rv.cue(rv.head+replaySkip, gtx.Now)
	}
	if a.replayEndBtn.Clicked(gtx) {
		rv.cue(dur, gtx.Now)
	}
	for i := range a.replaySpeedBtns {
		if i < len(replaySpeeds) && a.replaySpeedBtns[i].Clicked(gtx) {
			rv.speed = replaySpeeds[i]
			rv.anchor = gtx.Now
		}
	}
	if rv.trackW <= 0 {
		return // not laid out yet: nothing to measure a drag against
	}
	for {
		ev, ok := a.replayScrub.Update(gtx.Metric, gtx.Source, gesture.Horizontal)
		if !ok {
			break
		}
		switch ev.Kind {
		case pointer.Press, pointer.Drag:
			// Cueing does not stop playback: grab the slider mid-play and the
			// replay carries on from wherever it is dropped.
			f := clampF(float64(ev.Position.X-float32(rv.trackX0))/float64(rv.trackW), 0, 1)
			rv.cue(time.Duration(f*float64(dur)), gtx.Now)
		}
	}
}

// replayKeys are the transport's keyboard, the shortcuts a tape deck has
// always had: the space bar plays and pauses, the arrows move the playhead —
// left and right by a skip, up and down through the speeds — HOME and END run
// it to either end, and ESC leaves. They are the same actions the keys of the
// deck take, and they are drained in the same place, so a replay can be driven
// entirely from either.
//
// The leading key.FocusFilter is REQUIRED: a tag only becomes a focusable key
// target when one is registered for it that frame (the same rule the board's
// filters carry, boardKeyFilters). Nothing else on this screen wants the keys,
// so the tag simply takes them back whenever a click on a button has borrowed
// them — otherwise the space bar would press the button it left focused
// instead of pausing the replay.
func replayKeyFilters(tag event.Tag) []event.Filter {
	fs := []event.Filter{key.FocusFilter{Target: tag}}
	for _, n := range []key.Name{
		key.NameSpace,
		key.NameLeftArrow, key.NameRightArrow, key.NameUpArrow, key.NameDownArrow,
		key.NameHome, key.NameEnd, key.NameEscape,
	} {
		fs = append(fs, key.Filter{Focus: tag, Name: n})
	}
	return fs
}

// replayKeys drains the replay screen's keyboard and reports whether it asked
// to leave (ESC), which the caller does OUTSIDE the lock this takes — closing
// the replay locks it too.
func (a *App) replayKeys(gtx C, rv *replayView) (leave bool) {
	tag := &a.replayTag
	if !gtx.Source.Focused(tag) {
		gtx.Source.Execute(key.FocusCmd{Tag: tag})
	}
	filters := replayKeyFilters(tag)
	a.mu.Lock()
	defer a.mu.Unlock()
	for {
		ev, ok := gtx.Source.Event(filters...)
		if !ok {
			return leave
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue // releases and focus changes drive nothing here
		}
		if e.Name == key.NameEscape {
			leave = true
			continue
		}
		if rv.tl == nil || a.shareOpen {
			continue // still loading (no playhead to move yet), or the share modal has the keys
		}
		switch e.Name {
		case key.NameSpace:
			if rv.head >= rv.tl.dur {
				rv.head = 0 // play again, from the top
			}
			rv.playing = !rv.playing
			rv.anchor = gtx.Now
		case key.NameLeftArrow:
			rv.cue(rv.head-replaySkip, gtx.Now)
		case key.NameRightArrow:
			rv.cue(rv.head+replaySkip, gtx.Now)
		case key.NameUpArrow:
			rv.speed = replayStepSpeed(rv.speed, 1)
			rv.anchor = gtx.Now
		case key.NameDownArrow:
			rv.speed = replayStepSpeed(rv.speed, -1)
			rv.anchor = gtx.Now
		case key.NameHome:
			rv.cue(0, gtx.Now)
		case key.NameEnd:
			rv.cue(rv.tl.dur, gtx.Now)
		}
	}
}

// replayStepSpeed moves one step along replaySpeeds from the rate nearest sp,
// stopping at either end.
func replayStepSpeed(sp float64, step int) float64 {
	at := 0
	for i, s := range replaySpeeds {
		if math.Abs(s-sp) < math.Abs(replaySpeeds[at]-sp) {
			at = i
		}
	}
	return replaySpeeds[min(max(at+step, 0), len(replaySpeeds)-1)]
}

// replayKeyHint is the one-line legend under the deck — the shortcuts spelled
// out, so the keyboard is discoverable without a manual. A touch screen has no
// keyboard to spell out and gets the keys of the deck instead.
func (a *App) replayKeyHint(gtx C) D {
	return a.keyHintLine(gtx, "SPACE PLAY/PAUSE · ← → SKIP 10s · ↑ ↓ SPEED · HOME END · ESC LOBBY")
}

// keyHintLine centers one line of shortcut legend under whatever it belongs
// to — nothing at all on a touch screen, which has no keyboard to spell out.
func (a *App) keyHintLine(gtx C, txt string) D {
	if a.touchUI {
		return D{}
	}
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = 0
		return a.pixel(unit.Sp(8), txt, colMuted).Layout(gtx)
	})
}

// replayTransport is the whole deck: the scrubber (clear timeline over scrub
// slider), then the buttons — the five transport keys, the speed selector,
// Pin (Unpin while the replay is pinned — pinned says which; share.go), Share,
// and the way back to the lobby. On a compact screen the row splits in three
// rather than shrinking the keys.
func (a *App) replayTransport(gtx C, rv *replayView, pinned bool) D {
	keys := func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.transportButton(gtx, &a.replayStartBtn, "|<", false) }),
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D { return a.transportButton(gtx, &a.replayRewBtn, "<< 10s", false) }),
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D {
				label := "PLAY"
				if rv.playing {
					label = "PAUSE"
				}
				return a.transportButton(gtx, &a.replayPlayBtn, label, true)
			}),
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D { return a.transportButton(gtx, &a.replayFwdBtn, "10s >>", false) }),
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D { return a.transportButton(gtx, &a.replayEndBtn, ">|", false) }),
		)
	}
	speeds := func(gtx C) D {
		kids := []layout.FlexChild{
			layout.Rigid(a.pixel(unit.Sp(8), "SPEED", colMuted).Layout),
			layout.Rigid(hSpacer(8)),
		}
		for i, sp := range replaySpeeds {
			if i >= len(a.replaySpeedBtns) {
				break
			}
			i, sp := i, sp
			kids = append(kids,
				layout.Rigid(func(gtx C) D {
					return a.transportButton(gtx, &a.replaySpeedBtns[i], speedLabel(sp), sp == rv.speed)
				}),
				layout.Rigid(hSpacer(4)),
			)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
	back := func(gtx C) D { return a.secondaryButton(gtx, &a.replayBackBtn, "Back to Lobby") }
	pin := func(gtx C) D {
		label := "Pin"
		if pinned {
			label = "Unpin"
		}
		return a.secondaryButton(gtx, &a.replayPinBtn, label)
	}
	share := func(gtx C) D { return a.secondaryButton(gtx, &a.replayShareBtn, "Share") }
	// The three actions that are about the recording rather than the
	// playhead, kept together at the end of the deck.
	actions := func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(pin),
			layout.Rigid(hSpacer(8)),
			layout.Rigid(share),
			layout.Rigid(hSpacer(8)),
			layout.Rigid(back),
		)
	}

	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			// Wide enough that a second of a long game is still a pixel or
			// two of track, capped so it does not run off across a big
			// screen away from the boards it belongs to.
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(replayScrubMaxW))
			return a.replayScrubber(gtx, rv)
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			if a.form.compact {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(keys),
					layout.Rigid(spacer(8)),
					layout.Rigid(speeds),
					layout.Rigid(spacer(8)),
					layout.Rigid(actions),
				)
			}
			return layout.Center.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(keys),
					layout.Rigid(hSpacer(20)),
					layout.Rigid(speeds),
					layout.Rigid(hSpacer(20)),
					layout.Rigid(actions),
				)
			})
		}),
	)
}

// replayScrubber draws the timeline and the slider that cues it, and takes the
// drag that does the cueing.
//
// The upper band is the game's shape: one marker per line clear, standing on
// the timeline at the moment it happened, in the color of the board (and so
// the player) that cleared — the taller the marker, the more lines went at
// once, so a Jetris reads across the room. The lower band is the slider: a
// groove with the pre-game countdown dimmed at its head, the played part
// filled, and a chunky square thumb on the playhead.
func (a *App) replayScrubber(gtx C, rv *replayView) D {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(replayTrackPad)
	ticksH, sliderH := gtx.Dp(replayTicksH), gtx.Dp(replaySliderH)
	gap := gtx.Dp(4)
	h := ticksH + gap + sliderH
	track := max(w-2*pad, 1)
	rv.trackX0, rv.trackW = pad, track

	dur := max(rv.tl.dur, time.Millisecond)
	atX := func(off time.Duration) int {
		return pad + int(clampF(float64(off)/float64(dur), 0, 1)*float64(track))
	}

	// The timeline itself: the rule the markers stand on.
	baseY := ticksH - gtx.Dp(2)
	fillRect(gtx.Ops, image.Rect(pad, baseY, pad+track, baseY+gtx.Dp(2)), colBorder)

	tickW := gtx.Dp(replayTickW)
	for _, m := range rv.tl.marks {
		mh := gtx.Dp(unit.Dp(float32(5 + 3*min(max(m.lines, 1), 4))))
		x := atX(m.off)
		col := colMuted
		if m.color >= 0 {
			col = render.PlayerColorRGBA(m.color)
		}
		fillRect(gtx.Ops, image.Rect(x-tickW/2, baseY-mh, x-tickW/2+tickW, baseY), col)
	}

	// The slider: groove, the pre-game head dimmed, the played part filled.
	gy := ticksH + gap + sliderH/2
	gh := gtx.Dp(4)
	groove := image.Rect(pad, gy-gh/2, pad+track, gy+gh/2)
	fillRect(gtx.Ops, groove, colBorder)
	if rv.tl.startOff > 0 {
		fillRect(gtx.Ops, image.Rect(pad, groove.Min.Y, atX(rv.tl.startOff), groove.Max.Y), colMuted)
	}
	head := atX(rv.head)
	if head > pad {
		fillRect(gtx.Ops, image.Rect(pad, groove.Min.Y, head, groove.Max.Y), colAccent)
	}

	// The playhead: a hairline up through the markers, and the thumb on the
	// slider — gold while it is being dragged, so the grab is unmistakable.
	fillRect(gtx.Ops, image.Rect(head, 0, head+max(gtx.Dp(1), 1), baseY), withAlpha(colFg, 0.35))
	thumbCol := colAccent
	if a.replayScrub.Pressed() {
		thumbCol = colGold
	}
	tw := gtx.Dp(replayThumbW)
	fillRect(gtx.Ops, image.Rect(head-tw/2, ticksH+gap, head-tw/2+tw, ticksH+gap+sliderH), thumbCol)

	// The whole bar takes the drag, so a press anywhere on it cues.
	defer clip.Rect(image.Rect(0, 0, w, h)).Push(gtx.Ops).Pop()
	a.replayScrub.Add(gtx.Ops)
	semantic.LabelOp(replayScrubLabel).Add(gtx.Ops)
	return D{Size: image.Pt(w, h)}
}

// transportButton is a compact pixel key of the deck: accent-bordered like
// secondaryButton, but tight enough to sit five in a row. on fills it — the
// PLAY/PAUSE key and the chosen speed.
func (a *App) transportButton(gtx C, btn *widget.Clickable, label string, on bool) D {
	b := pixelize(material.Button(a.th, btn, label))
	b.TextSize = unit.Sp(9)
	b.Inset = layout.Inset{Top: unit.Dp(7), Bottom: unit.Dp(7), Left: unit.Dp(10), Right: unit.Dp(10)}
	if !on {
		b.Background, b.Color = colPanel, colAccent
	}
	return widget.Border{Color: colAccent, Width: unit.Dp(2)}.Layout(gtx, b.Layout)
}

// replayProgressBar is the load's progress: a bordered groove filled to frac.
func (a *App) replayProgressBar(gtx C, frac float64) D {
	w := max(gtx.Constraints.Min.X, gtx.Dp(120))
	h := gtx.Dp(14)
	fillRect(gtx.Ops, image.Rect(0, 0, w, h), colBorder)
	in := gtx.Dp(2)
	fillRect(gtx.Ops, image.Rect(in, in, w-in, h-in), colPanel)
	if fw := int(clampF(frac, 0, 1) * float64(w-2*in)); fw > 0 {
		fillRect(gtx.Ops, image.Rect(in, in, in+fw, h-in), colNATSGreen)
	}
	return D{Size: image.Pt(w, h)}
}
