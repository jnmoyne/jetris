package nativeui

// The replay ending: the winner reveal that crowns the winning board and
// the summary line that keeps the ending back until then.

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/render"
)

func TestTrophyTierFor(t *testing.T) {
	want := map[int]trophyTier{1: tierLegendary, 2: tierEpic, 3: tierEpic, 4: tierRare, 10: tierRare, 11: tierPlain, 40: tierPlain}
	for rank, tier := range want {
		if got := trophyTierFor(rank); got != tier {
			t.Errorf("rank %d: tier %d, want %d", rank, got, tier)
		}
	}
	// Every tier has a look; only the plain cup goes uncaptioned.
	for tier := tierPlain; tier <= tierLegendary; tier++ {
		st := tier.style()
		if st.metal.A == 0 || st.caption.A == 0 {
			t.Errorf("tier %d has no metal or caption color", tier)
		}
		if (st.name == "") != (tier == tierPlain) {
			t.Errorf("tier %d caption %q", tier, st.name)
		}
	}
}

func TestReplayWinnersAndVerdict(t *testing.T) {
	// Competitive: the surviving player's board (boards are in sorted-ID order).
	rv := newReplayView(sampleReplayRecord(), true)
	won, banner := replayWinners(rv)
	if !won[rv.byPlayer["alice"]] || len(won) != 1 || banner != "WINNER" {
		t.Errorf("competitive: won %v banner %q, want alice's board alone, WINNER", won, banner)
	}
	if v := replayVerdict(rv.rec); v != "ALICE WINS!" {
		t.Errorf("competitive verdict %q", v)
	}
	// Teams: the winning team's board.
	rv = newReplayView(sampleTeamsReplayRecord(), true)
	won, banner = replayWinners(rv)
	if !won[1] || len(won) != 1 || banner != "WINNERS" {
		t.Errorf("teams: won %v banner %q, want team B's board alone, WINNERS", won, banner)
	}
	if v := replayVerdict(rv.rec); v != "TEAM B WINS!" {
		t.Errorf("teams verdict %q", v)
	}
	// Cooperative: the one shared board, the run's score as the verdict.
	rv = newReplayView(sampleCoopReplayRecord(), true)
	won, banner = replayWinners(rv)
	if !won[0] || len(won) != 1 || banner != "GAME OVER" {
		t.Errorf("co-op: won %v banner %q", won, banner)
	}
	if v := replayVerdict(rv.rec); v != "FINAL SCORE 5200" {
		t.Errorf("co-op verdict %q", v)
	}
	// Draws crown nobody.
	rec := sampleReplayRecord()
	rec.Players[0].Winner = false
	rv = newReplayView(rec, true)
	if won, _ := replayWinners(rv); len(won) != 0 {
		t.Errorf("competitive draw crowned %v", won)
	}
	if v := replayVerdict(rec); v != "DRAW" {
		t.Errorf("competitive draw verdict %q", v)
	}
	rec = sampleTeamsReplayRecord()
	rec.WinningTeam = -1
	rv = newReplayView(rec, true)
	if won, _ := replayWinners(rv); len(won) != 0 {
		t.Errorf("teams draw crowned %v", won)
	}
	if v := replayVerdict(rec); v != "DRAW" {
		t.Errorf("teams draw verdict %q", v)
	}
	// Two survivors (a simultaneous finish that still names winners) share the verdict.
	rec = sampleReplayRecord()
	rec.Players[1].Winner = true
	if v := replayVerdict(rec); v != "ALICE & BOB WIN!" {
		t.Errorf("two-winner verdict %q", v)
	}
}

// joinSpans flattens a summary line's text for substring checks.
func joinSpans(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}
	return b.String()
}

func findSpan(spans []span, sub string) (span, bool) {
	for _, s := range spans {
		if strings.Contains(s.text, sub) {
			return s, true
		}
	}
	return span{}, false
}

func TestReplaySummaryKeepsTheEndingBack(t *testing.T) {
	rec := sampleReplayRecord()

	// Before the reveal: the players in their board colors, no scores, no trophy.
	hidden := replaySummary(rec, false)
	txt := joinSpans(hidden)
	if strings.Contains(txt, winnerMark) || strings.Contains(txt, "4200") || strings.Contains(txt, "lvl") {
		t.Fatalf("summary before the reveal spoils the ending: %q", txt)
	}
	if !strings.Contains(txt, "competitive") {
		t.Errorf("summary lacks the mode: %q", txt)
	}
	alice, ok := findSpan(hidden, "alice")
	if !ok || alice.col != render.PlayerColorRGBA(0) || alice.emph {
		t.Errorf("alice before the reveal = %+v, want her board color, no emphasis", alice)
	}
	bob, _ := findSpan(hidden, "bob")
	if bob.col != render.PlayerColorRGBA(1) {
		t.Errorf("bob before the reveal = %+v, want his board color", bob)
	}

	// Revealed: the winner gold, bold italic and trophied with the scores
	// out; the beaten player keeps their color.
	shown := replaySummary(rec, true)
	alice, _ = findSpan(shown, "alice")
	if !strings.HasPrefix(alice.text, winnerMark) || !strings.Contains(alice.text, "4200 (lvl 4)") || alice.col != colGold || !alice.emph {
		t.Errorf("alice revealed = %+v, want a gold bold-italic trophied span with her score", alice)
	}
	bob, _ = findSpan(shown, "bob")
	if bob.col != render.PlayerColorRGBA(1) || bob.emph || !strings.Contains(bob.text, "3100") {
		t.Errorf("bob revealed = %+v, want his board color, plain, with his score", bob)
	}

	// Teams: the team headers carry the reveal; members ride their team's color.
	teams := sampleTeamsReplayRecord()
	txt = joinSpans(replaySummary(teams, false))
	if strings.Contains(txt, winnerMark) || strings.Contains(txt, "4200") {
		t.Fatalf("teams summary before the reveal spoils the ending: %q", txt)
	}
	shown = replaySummary(teams, true)
	b, _ := findSpan(shown, "TEAM B")
	if !strings.HasPrefix(b.text, winnerMark) || !strings.Contains(b.text, "4200 (lvl 4)") || b.col != colGold || !b.emph {
		t.Errorf("TEAM B revealed = %+v, want gold bold-italic with the trophy and score", b)
	}
	if a, _ := findSpan(shown, "TEAM A"); a.col != render.PlayerColorRGBA(0) || a.emph {
		t.Errorf("TEAM A revealed = %+v, want its team color, plain", a)
	}
	if members, ok := findSpan(shown, "carol [agent], dave"); !ok || members.col != colGold {
		t.Errorf("team B's members = %+v (found %v), want them listed in gold", members, ok)
	}
}

// renderAt lays the app out at a given frame time (renderOnce leaves the
// clock at zero, which parks the show before its first frame).
func renderAt(t *testing.T, a *App, now time.Time) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1200, 820)),
		Now:         now,
	}
	if d := a.layout(gtx); d.Size.X == 0 || d.Size.Y == 0 {
		t.Fatalf("layout produced zero dimensions: %+v", d.Size)
	}
}

// The ending renders through every tier and mode — including a draw, and
// the frame before the show starts — without panicking.
func TestReplayEndingRenders(t *testing.T) {
	draw := sampleTeamsReplayRecord()
	draw.WinningTeam = -1
	recs := []config.ArchiveRecord{sampleReplayRecord(), sampleTeamsReplayRecord(), sampleCoopReplayRecord(), draw}
	for _, rec := range recs {
		for rank := 1; rank <= 12; rank += 3 {
			a := newTestApp()
			rv := newReplayView(rec, false)
			rv.rank, rv.of = rank, 12
			rv.done, rv.doneAt = true, time.Now()
			a.replayView = rv
			a.screen = screenReplay
			for _, at := range []time.Duration{-time.Second, 0, 200 * time.Millisecond, 3 * time.Second, time.Minute} {
				renderAt(t, a, rv.doneAt.Add(at))
			}
		}
	}
}
