package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Every seat's tally: a peer's line_clear counts the clear by its names
// once (a replay of one already folded counts nothing), the attack follows
// the game's rule, a game_over adds nothing but the seat's pieces, our own
// locks are counted as we score them, and the record's `stats` carries the
// tally with the time each seat played — to its game_over, or to the finish.
func TestStatsTally(t *testing.T) {
	g := &Game{mode: modeCompetitive, guideline: true, senderTotals: map[string][2]int{}, results: map[string]event{},
		senderStats: map[string]*playerStats{}, senderPieces: map[string]int{}, outAt: map[string]time.Time{}, eliminated: map[string]bool{}}
	g.a = &Agent{name: "me"}
	quad := event{Kind: "line_clear", PlayerID: "rival", LinesCleared: 4, Score: 800, TotalScore: 800, TotalLines: 4, PieceCount: 12}
	g.foldLineClear(quad)
	g.foldLineClear(quad) // the ordered stream read again
	g.foldLineClear(event{Kind: "line_clear", PlayerID: "rival", LinesCleared: 2, TSpin: 2, BackToBack: true, Combo: 1, Score: 500, TotalScore: 1300, TotalLines: 6, PieceCount: 15})
	g.foldLineClear(event{Kind: "line_clear", PlayerID: "rival", TSpin: 1, Score: 100, TotalScore: 1400, TotalLines: 6, PieceCount: 16})
	g.foldLineClear(event{Kind: "game_over", PlayerID: "rival", TotalScore: 1400, TotalLines: 6, PieceCount: 18})
	rival := g.senderStats["rival"]
	if rival == nil {
		t.Fatal("no tally for the rival")
	}
	// The quad (4 rows), the Back-to-Back T-spin double (4+2), the Mini
	// T-spin that cleared nothing.
	want := playerStats{Quads: 1, Doubles: 1, TSpins: 1, MiniTSpins: 1, BackToBacks: 1, MaxCombo: 1, Attack: 10}
	if *rival != want {
		t.Errorf("rival's tally = %+v\nwant          %+v", *rival, want)
	}
	if g.senderPieces["rival"] != 18 {
		t.Errorf("rival's pieces = %d, want the game_over's 18", g.senderPieces["rival"])
	}

	// Our own lock, counted as scored: a plain triple sends two rows.
	g.mu.Lock()
	g.statsFor(g.a.name).tally(clearInfo{lines: 3}, 0, true, true)
	g.mu.Unlock()
	if me := g.senderStats["me"]; me == nil || me.Triples != 1 || me.Attack != 2 {
		t.Errorf("own tally = %+v, want the triple and its 2 rows", me)
	}

	// The record: the rival played to their game_over, we to the finish,
	// and a seat nobody heard from carries a time and nothing else.
	started := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	g.outAt["rival"] = started.Add(90 * time.Second)
	g.results["rival"] = event{Kind: "game_over", PlayerID: "rival", TotalScore: 1400, TotalLines: 6, PieceCount: 18}
	g.eliminated["rival"] = true
	g.roster = []playerSummary{{PlayerID: "me"}, {PlayerID: "rival"}, {PlayerID: "quiet"}}
	g.pieceIdx = 40
	rows := g.playerResults(started, started.Add(2*time.Minute))
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[r["player_id"].(string)] = r
	}
	stats := func(id string) playerStats {
		b, _ := json.Marshal(byID[id]["stats"])
		var st playerStats
		_ = json.Unmarshal(b, &st)
		return st
	}
	if st := stats("rival"); st.PlayedMs != 90_000 || st.Quads != 1 || byID["rival"]["piece_count"] != 18 {
		t.Errorf("rival's record row = %+v / %v pieces, want 90s played, the tally, 18 pieces", st, byID["rival"]["piece_count"])
	}
	if st := stats("me"); st.PlayedMs != 120_000 || st.Triples != 1 || byID["me"]["piece_count"] != 40 {
		t.Errorf("own record row = %+v / %v pieces, want 120s played, the triple, 40 pieces", st, byID["me"]["piece_count"])
	}
	if st := stats("quiet"); st.PlayedMs != 120_000 || st != (playerStats{PlayedMs: 120_000}) {
		t.Errorf("a silent seat's record row = %+v, want the time played alone", st)
	}
	// The JSON carries the GUI's field names.
	b, _ := json.Marshal(byID["rival"]["stats"])
	for _, key := range []string{`"quads":1`, `"doubles":1`, `"t_spins":1`, `"mini_t_spins":1`, `"back_to_backs":1`, `"max_combo":1`, `"attack":10`, `"played_ms":90000`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("stats JSON %s lacks %s", b, key)
		}
	}
}
