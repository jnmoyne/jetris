package nativeui

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
)

// seedReplayMsgs gives a loaded replay a recording to show: a transaction
// every 400 ms from 2.4 s before the playhead to 2.4 s after it — locks of
// four cells and steps of seven in batches, a line_clear event and a meta
// on their own — cut into transactions the way the loader cuts them.
func seedReplayMsgs(rv *replayView) {
	tl, id := rv.tl, rv.rec.GameID
	cell := func(r, c int) string {
		return config.ReplayCopySubject(id, config.CompetitiveCellSubject(id, "alice", r, c))
	}
	add := func(off time.Duration, subject, payload, batch string) {
		tl.msgs = append(tl.msgs, replayMsg{off: off, subject: subject, payload: payload, batch: batch})
	}
	n := 0
	for off := rv.head - 2400*time.Millisecond; off <= rv.head+2400*time.Millisecond; off += 400 * time.Millisecond {
		n++
		batch := fmt.Sprintf("b%02d", n)
		switch n % 4 {
		case 0:
			add(off, config.ReplayCopySubject(id, config.EventKindSubject(id, string(engine.EventLineClear), "alice")),
				`{"player_id":"alice","lines_cleared":1,"total_score":1200,"total_lines":9}`, "")
		case 2:
			add(off, replayMetaSubject(id), `{"status":"in_progress","level":2}`, "")
		case 1:
			for c := 3; c < 7; c++ {
				add(off+time.Duration(c)*time.Microsecond, cell(14+n%3, c), `{"a":true,"t":2,"r":0,"ar":14,"ac":3}`, batch)
			}
		default:
			for c := 0; c < 4; c++ {
				add(off+time.Duration(c)*time.Microsecond, cell(8, c+n%3), `{"a":true,"t":5,"r":1,"ar":8,"ac":1}`, batch)
			}
			for c := 0; c < 3; c++ {
				add(off+time.Duration(4+c)*time.Microsecond, cell(7, c+n%3), `{}`, batch)
			}
		}
	}
	tl.txns = groupTxns(tl.msgs)
	tl.origin = rv.rec.StartedAt
}

// The NATS MSGS key of the deck and the N key both open the transactions
// panel over the deck and close it again; the panel is a window around the
// playhead: the last transactions at or before it, the first after it.
func TestReplayMsgsPanelToggles(t *testing.T) {
	g := newReplayRig(t)
	g.rv.head = g.rv.tl.dur / 2 // the rig opens at the start; the window wants a middle
	seedReplayMsgs(g.rv)
	g.rv.playing = false
	g.frame(0)
	if _, ok := semanticBounds(g.r, txnWindowLabel); ok {
		t.Fatal("the transactions panel is up before anyone asked for it")
	}
	g.tap(t, replayMsgsLabel)
	if !g.a.replayMsgsShown {
		t.Fatal("the NATS MSGS key did not open the panel")
	}
	panel, ok := semanticBounds(g.r, txnWindowLabel)
	if !ok {
		t.Fatal("no transactions panel on screen with the switch on")
	}
	deck, ok := semanticBounds(g.r, replayScrubLabel)
	if !ok {
		t.Fatal("no scrub track on the replay screen")
	}
	if panel.Max.Y > deck.Min.Y {
		t.Fatalf("the panel %v runs into the deck %v", panel, deck)
	}
	if panel.Dy() < msgPanelMinH {
		t.Fatalf("the panel is %d px tall, under its %d dp floor", panel.Dy(), msgPanelMinH)
	}
	before, after := g.rv.tl.txnsAround(g.rv.head, txnWindowN)
	if len(before) != txnWindowN || len(after) != txnWindowN {
		t.Fatalf("%d transactions before the playhead and %d after, want %d each", len(before), len(after), txnWindowN)
	}
	if before[len(before)-1].off > g.rv.head || after[0].off <= g.rv.head {
		t.Fatalf("the window straddles the wrong point: last before at %v, first after at %v, playhead %v",
			before[len(before)-1].off, after[0].off, g.rv.head)
	}

	g.press(replayMsgsKey)
	if g.a.replayMsgsShown {
		t.Fatal("N did not close the panel")
	}
	if _, ok := semanticBounds(g.r, txnWindowLabel); ok {
		t.Fatal("the transactions panel is still up with the switch off")
	}
	g.press(replayMsgsKey)
	if !g.a.replayMsgsShown {
		t.Fatal("N did not open the panel again")
	}
	// Scrubbed to the start, nothing is before the playhead and the panel
	// still stands; at the end, nothing is after it.
	g.press(key.NameHome)
	if before, _ := g.rv.tl.txnsAround(g.rv.head, txnWindowN); len(before) != 0 {
		t.Fatalf("%d transactions before the start", len(before))
	}
	if _, ok := semanticBounds(g.r, txnWindowLabel); !ok {
		t.Fatal("the panel went away at the start of the recording")
	}
	g.press(key.NameEnd)
	if _, after := g.rv.tl.txnsAround(g.rv.head, txnWindowN); len(after) != 0 {
		t.Fatalf("%d transactions after the end", len(after))
	}
	if _, ok := semanticBounds(g.r, txnWindowLabel); !ok {
		t.Fatal("the panel went away at the end of the recording")
	}
}

// The live log is cut into transactions on the group each message was
// filed under (msgGroup): a batch's run is one, a single publish its own,
// the last n of them oldest first.
func TestLiveTxns(t *testing.T) {
	log := tutorialStreamMsgs() // a 7-cell batch, a meta, a 7-cell batch
	txns := liveTxns(log, 10)
	if len(txns) != 3 || len(txns[0]) != 7 || len(txns[1]) != 1 || len(txns[2]) != 7 {
		sizes := make([]int, len(txns))
		for i, x := range txns {
			sizes[i] = len(x)
		}
		t.Fatalf("transactions = %v messages each, want 7, 1, 7", sizes)
	}
	if txns[0][0].group != log[0].group || txns[2][0].group != log[len(log)-1].group {
		t.Fatal("the transactions are not in the log's order")
	}
	if got := liveTxns(log, 2); len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 7 {
		t.Fatalf("the last two: %d transactions, want the meta and the second batch", len(got))
	}
	if got := liveTxns(nil, 5); len(got) != 0 {
		t.Fatalf("an empty log cut into %d transactions", len(got))
	}
}

// A spectator's panel lays out — the stream's transactions over a NOW marker
// at the live edge, and the waiting line before any message has arrived —
// at the strip height the handle setting gives it, within the window.
func TestSpectatorTxnPanelLaysOut(t *testing.T) {
	players := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob"}}
	for _, logged := range []bool{false, true} {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "spec", "", config.ModeCompetitive, engine.ModeSpectator, 0, 0, 0)
		a.gamePlayers = players
		a.screen = screenGame
		a.showMsgs.Value = true
		if logged {
			a.msgLog = tutorialStreamMsgs()
		}
		r := new(input.Router)
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1200, 820)), Source: r.Source()}
		if d := a.layout(gtx); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("spectator screen (log %v) rendered zero-size", logged)
		}
		r.Frame(&ops)
		panel, ok := semanticBounds(r, txnWindowLabel)
		if !ok {
			t.Fatalf("no transactions panel on the spectator's screen (log %v)", logged)
		}
		if panel.Max.Y > 820 || panel.Min.Y < 0 {
			t.Fatalf("spectator panel %v runs off the window", panel)
		}
	}
}

// The recording's clock the panel stamps its rows with: m:ss.mmm, the hour
// shown once there is one, never negative.
func TestReplayClockMs(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00.000"}, {37412 * time.Millisecond, "0:37.412"}, {-time.Second, "0:00.000"},
		{61*time.Minute + 2*time.Second + 3*time.Millisecond, "1:01:02.003"},
	} {
		if got := replayClockMs(tc.d); got != tc.want {
			t.Errorf("replayClockMs(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
