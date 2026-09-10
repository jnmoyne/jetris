package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSubjectBuilders(t *testing.T) {
	gameID := "550e8400-e29b-41d4-a716-446655440000"
	playerID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

	tests := []struct {
		name   string
		got    string
		expect string
	}{
		{"GameStream", GameStream(gameID), "JETRIS_GAME_550e8400-e29b-41d4-a716-446655440000"},
		{"GameSubjectFilter", GameSubjectFilter(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.>"},
		{"CompetitiveCellSubject", CompetitiveCellSubject(gameID, playerID, 12, 7), "jetris.game.550e8400-e29b-41d4-a716-446655440000.player.a1b2c3d4-e5f6-7890-abcd-ef1234567890.playfield.cell.12.7"},
		{"CoopCellSubject", CoopCellSubject(gameID, 12, 7), "jetris.game.550e8400-e29b-41d4-a716-446655440000.playfield.cell.12.7"},
		{"CompetitiveCellSubjectFilter", CompetitiveCellSubjectFilter(gameID, playerID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.player.a1b2c3d4-e5f6-7890-abcd-ef1234567890.playfield.cell.>"},
		{"CoopCellSubjectFilter", CoopCellSubjectFilter(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.playfield.cell.>"},
		{"TeamCellSubject", TeamCellSubject(gameID, 1, 12, 7), "jetris.game.550e8400-e29b-41d4-a716-446655440000.team.1.playfield.cell.12.7"},
		{"TeamCellSubjectFilter", TeamCellSubjectFilter(gameID, 0), "jetris.game.550e8400-e29b-41d4-a716-446655440000.team.0.playfield.cell.>"},
		{"MetaSubject", MetaSubject(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.meta"},
		{"RosterSubject", RosterSubject(gameID, playerID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.roster.a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"EventKindSubject", EventKindSubject(gameID, "line_clear", playerID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.events.line_clear.a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"EventsSubjectFilter", EventsSubjectFilter(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.events.>"},
		{"CountdownSubject", CountdownSubject(gameID), "jetris.game.550e8400-e29b-41d4-a716-446655440000.countdown"},
		{"GameChatSubject", GameChatSubject(gameID), "jetris.chat.550e8400-e29b-41d4-a716-446655440000"},
		{"LobbyChatSubject", LobbyChatSubject, "jetris.chat.lobby"},
		{"LobbyPlayerKey", LobbyPlayerKey(playerID), "players.a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
		{"LobbyGameKey", LobbyGameKey(gameID), "games.550e8400-e29b-41d4-a716-446655440000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expect {
				t.Errorf("got %q, want %q", tt.got, tt.expect)
			}
		})
	}
}

func TestTeamDimensions(t *testing.T) {
	for _, teamSize := range []int{1, 2, 3} {
		// A record with no extra-columns setting — every game archived
		// before the board-width slider — keeps the historical board: one
		// full standard section per teammate.
		if got, want := TeamBoardWidth(teamSize, 0), teamSize*StandardWidth; got != want {
			t.Errorf("TeamBoardWidth(%d, 0) = %d, want %d", teamSize, got, want)
		}
	}
}

// The headroom is fixed — 4 rows over the 20-row Guideline playfield — and
// so is a competitive board's height; only a SHARED board grows, by the
// game's extra-rows setting per seat beyond the first, and it grows
// downwards: the visible playfield still starts right below the headroom.
func TestBoardHeightPerSeat(t *testing.T) {
	if VisibleRows != 20 {
		t.Errorf("VisibleRows = %d, want 20", VisibleRows)
	}
	if TotalRows != HeadroomRows+VisibleRows {
		t.Errorf("TotalRows = %d, want %d", TotalRows, HeadroomRows+VisibleRows)
	}
	if VisibleRowStart != HeadroomRows {
		t.Errorf("VisibleRowStart = %d, want %d", VisibleRowStart, HeadroomRows)
	}
	if SharedBoardHeight(1, MaxExtraRows) != TotalRows {
		t.Errorf("a lone seat's board = %d rows, want %d", SharedBoardHeight(1, MaxExtraRows), TotalRows)
	}
	if got := SharedBoardHeight(3, 5); got != 34 {
		t.Errorf("SharedBoardHeight(3, 5) = %d, want 34", got)
	}
	for _, m := range []GameMeta{
		{Mode: ModeCompetitive, PlayerCount: 4, ExtraRows: 10},
		{Mode: ModeCooperative, PlayerCount: 4},
		{Mode: ModeTeams, TeamCount: 2, TeamSize: 3, PlayerCount: 6},
	} {
		if got := m.BoardHeight(); got != TotalRows {
			t.Errorf("%+v: BoardHeight() = %d, want the standard %d", m, got, TotalRows)
		}
	}
	if got := (GameMeta{Mode: ModeTeams, TeamCount: 2, TeamSize: 3, PlayerCount: 6, ExtraRows: 2}).BoardHeight(); got != 28 {
		t.Errorf("a 3-seat team board at 2 extra rows = %d, want 28", got)
	}
}

// BoardHeight is the record's own word on its boards' height, and a fallback
// only: what the archiver wrote, or today's board when it wrote nothing. It
// never guesses from the mode or the player count — the replay stream holds
// the cells, and the replay measures the height off those.
func TestArchiveBoardHeight(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  ArchiveRecord
		want int
	}{
		{"recorded", ArchiveRecord{Mode: ModeCompetitive, PlayerCount: 3, BoardRows: TotalRows}, TotalRows},
		{"recorded taller", ArchiveRecord{Mode: ModeCooperative, PlayerCount: 2, BoardRows: 31}, 31},
		{"unrecorded competitive", ArchiveRecord{Mode: ModeCompetitive, PlayerCount: 3}, TotalRows},
		{"unrecorded cooperative", ArchiveRecord{Mode: ModeCooperative, PlayerCount: 3}, TotalRows},
		{"unrecorded teams", ArchiveRecord{Mode: ModeTeams, TeamCount: 3, TeamSize: 2}, TotalRows},
	} {
		if got := tc.rec.BoardHeight(); got != tc.want {
			t.Errorf("%s: BoardHeight() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The team count a game records: absent is the historical two, and anything
// out of range is clamped rather than trusted. GameMeta.Teams and
// ArchiveRecord.Teams read it back, and answer 0 where a game has no teams
// at all.
func TestTeamCountNormalization(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, DefaultTeamCount}, {-3, DefaultTeamCount},
		{1, MinTeamCount}, {2, 2}, {4, 4}, {MaxTeamCount, MaxTeamCount},
		{MaxTeamCount + 1, MaxTeamCount}, {99, MaxTeamCount},
	} {
		if got := NormalizeTeamCount(tc.in); got != tc.want {
			t.Errorf("NormalizeTeamCount(%d) = %d, want %d", tc.in, got, tc.want)
		}
		if got := (GameMeta{Mode: ModeTeams, TeamCount: tc.in}).Teams(); got != tc.want {
			t.Errorf("GameMeta{TeamCount: %d}.Teams() = %d, want %d", tc.in, got, tc.want)
		}
		if got := (ArchiveRecord{Mode: ModeTeams, TeamCount: tc.in}).Teams(); got != tc.want {
			t.Errorf("ArchiveRecord{TeamCount: %d}.Teams() = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, m := range []GameMode{ModeCooperative, ModeCompetitive} {
		if got := (GameMeta{Mode: m, TeamCount: 4}).Teams(); got != 0 {
			t.Errorf("GameMeta{Mode: %v}.Teams() = %d, want 0", m, got)
		}
		if got := (ArchiveRecord{Mode: m, TeamCount: 4}).Teams(); got != 0 {
			t.Errorf("ArchiveRecord{Mode: %v}.Teams() = %d, want 0", m, got)
		}
	}
	// A record written before the field: the per-team totals say how many
	// teams played.
	if got := (ArchiveRecord{Mode: ModeTeams, TeamScores: []int{1, 2, 3}}).Teams(); got != 3 {
		t.Errorf("pre-field record with 3 team scores: Teams() = %d, want 3", got)
	}
	if got := (ArchiveRecord{Mode: ModeTeams}).Teams(); got != DefaultTeamCount {
		t.Errorf("record with neither: Teams() = %d, want %d", got, DefaultTeamCount)
	}
}

// TeamLetter names a team the way every screen shows it.
func TestTeamLetter(t *testing.T) {
	for i, want := range []string{"A", "B", "C", "D", "E", "F"} {
		if got := TeamLetter(i); got != want {
			t.Errorf("TeamLetter(%d) = %q, want %q", i, got, want)
		}
	}
}

// The board-width setting: a shared board is the standard 10 columns for its
// first seat and extraCols more for every seat after it, with the seats'
// spawn points one extraCols step apart — so the last seat's spawn box ends
// exactly on the board's last column, whatever the setting.
func TestSharedBoardWidth(t *testing.T) {
	for _, tc := range []struct{ players, extra, want int }{
		{1, 4, 10}, {2, 4, 14}, {3, 4, 18}, {4, 4, 22},
		{2, 10, 20}, {3, 10, 30}, // the maximum: a full section per player
		{2, 0, 20}, {3, 0, 30}, // absent: the historical board, as if 10
		{2, -3, 20}, {2, 1, 14}, {2, 99, 20}, // out of range: clamped
	} {
		if got := SharedBoardWidth(tc.players, tc.extra); got != tc.want {
			t.Errorf("SharedBoardWidth(%d, %d) = %d, want %d", tc.players, tc.extra, got, tc.want)
		}
	}
	for _, extra := range []int{MinExtraColumns, 7, MaxExtraColumns, 0} {
		for _, players := range []int{1, 2, 3, 5} {
			w := SharedBoardWidth(players, extra)
			if got, want := SharedSpawnOffset(0, extra), 0; got != want {
				t.Errorf("SharedSpawnOffset(0, %d) = %d, want %d", extra, got, want)
			}
			// The last seat's 10-wide spawn box has to end on the board.
			if got := SharedSpawnOffset(players-1, extra) + StandardWidth; got != w {
				t.Errorf("last spawn box of %d players at extra %d ends at %d, board is %d wide", players, extra, got, w)
			}
		}
	}
}

func TestExtraColumnsPerPlayer(t *testing.T) {
	for in, want := range map[int]int{0: MaxExtraColumns, -1: MaxExtraColumns, 1: MinExtraColumns, 4: 4, 7: 7, 10: 10, 11: MaxExtraColumns} {
		if got := ExtraColumnsPerPlayer(in); got != want {
			t.Errorf("ExtraColumnsPerPlayer(%d) = %d, want %d", in, got, want)
		}
	}
	if DefaultExtraColumns != MinExtraColumns {
		t.Errorf("DefaultExtraColumns = %d, want the slider's low end %d", DefaultExtraColumns, MinExtraColumns)
	}
}

func TestModeTeamsString(t *testing.T) {
	if got := ModeTeams.String(); got != "teams" {
		t.Errorf("ModeTeams.String() = %q, want %q", got, "teams")
	}
}

func TestGameStreamValidName(t *testing.T) {
	name := GameStream("550e8400-e29b-41d4-a716-446655440000")
	if strings.ContainsAny(name, " .>*") {
		t.Errorf("stream name %q contains invalid characters", name)
	}
}

func TestGameIDFromChatSubject(t *testing.T) {
	if got := GameIDFromChatSubject(LobbyChatSubject); got != "" {
		t.Errorf("lobby subject: got %q, want empty", got)
	}
	if got := GameIDFromChatSubject(GameChatSubject("g-42")); got != "g-42" {
		t.Errorf("game subject: got %q, want g-42", got)
	}
	if got := GameIDFromChatSubject("jetris.chat."); got != "" {
		t.Errorf("empty game token: got %q, want empty", got)
	}
}

// TestArchiveRecordRanking pins the shared "By score" ordering (HeadlineScore
// per mode, then RankBefore's tie-breaks) that both the history list and the
// replay archiver's top-N cut rely on.
func TestArchiveRecordRanking(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// HeadlineScore: shared total (coop), best team (teams), best player (competitive).
	coop := ArchiveRecord{Mode: ModeCooperative, TotalScore: 40,
		Players: []PlayerResult{{PlayerID: "a", Score: 99}}}
	if got := coop.HeadlineScore(); got != 40 {
		t.Errorf("coop headline = %d, want 40", got)
	}
	teams := ArchiveRecord{Mode: ModeTeams, TeamScores: []int{10, 30}}
	if got := teams.HeadlineScore(); got != 30 {
		t.Errorf("teams headline = %d, want 30", got)
	}
	comp := ArchiveRecord{Mode: ModeCompetitive,
		Players: []PlayerResult{{PlayerID: "a", Score: 7}, {PlayerID: "b", Score: 12}}}
	if got := comp.HeadlineScore(); got != 12 {
		t.Errorf("competitive headline = %d, want 12", got)
	}

	// RankBefore: score desc, then shorter duration, then newer finish, then
	// game ID — a total order, so every client computes the identical top N.
	mk := func(id string, score int, dur time.Duration, fin time.Time) ArchiveRecord {
		return ArchiveRecord{GameID: id, Mode: ModeCooperative, TotalScore: score,
			StartedAt: fin.Add(-dur), FinishedAt: fin}
	}
	hi, lo := mk("a", 20, time.Minute, t0), mk("b", 10, time.Minute, t0)
	if !hi.RankBefore(lo) || lo.RankBefore(hi) {
		t.Error("higher score must rank first")
	}
	fast, slow := mk("a", 10, time.Minute, t0), mk("b", 10, 2*time.Minute, t0)
	if !fast.RankBefore(slow) || slow.RankBefore(fast) {
		t.Error("shorter game must break a score tie")
	}
	newer, older := mk("a", 10, time.Minute, t0.Add(time.Hour)), mk("b", 10, time.Minute, t0)
	if !newer.RankBefore(older) || older.RankBefore(newer) {
		t.Error("newer finish must break a duration tie")
	}
	x, y := mk("a", 10, time.Minute, t0), mk("b", 10, time.Minute, t0)
	if !x.RankBefore(y) || y.RankBefore(x) {
		t.Error("game ID must totally order full ties")
	}

	// SameReplayBucket: mode and agent presence both have to match.
	human := ArchiveRecord{Mode: ModeCooperative, Players: []PlayerResult{{PlayerID: "a"}}}
	agent := ArchiveRecord{Mode: ModeCooperative, Players: []PlayerResult{{PlayerID: "a", Agent: true}}}
	otherMode := ArchiveRecord{Mode: ModeTeams, Players: []PlayerResult{{PlayerID: "a"}}}
	if !human.SameReplayBucket(human) {
		t.Error("same mode + same crew should share a bucket")
	}
	if human.SameReplayBucket(agent) || human.SameReplayBucket(otherMode) {
		t.Error("agent presence and mode must both split buckets")
	}
}

// The replay keep set is the union of each bucket's top ReplayTopN (ranked by
// RankBefore) and the ReplayRecentN most recent finishes across all buckets
// (RecentBefore) — and every part of it is a pure function of the records, so
// every archiver cuts the same set.
func TestReplayKeepSet(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string, mode GameMode, score int, n int, agent bool) ArchiveRecord {
		r := ArchiveRecord{GameID: id, Mode: mode,
			StartedAt: t0.Add(time.Duration(n) * time.Minute), FinishedAt: t0.Add(time.Duration(n)*time.Minute + 30*time.Second),
			Players: []PlayerResult{{PlayerID: "p", Score: score, Agent: agent}}}
		if mode == ModeCooperative {
			r.TotalScore = score
		}
		return r
	}

	// Coop/human bucket: ReplayTopN + ReplayRecentN games finishing in order,
	// score rising with finish order — so the top N are also the newest.
	var recs []ArchiveRecord
	total := ReplayTopN + ReplayRecentN
	for i := 1; i <= total; i++ {
		recs = append(recs, mk("coop-"+strconv.Itoa(i), ModeCooperative, i*100, i, false))
	}
	top := ReplayTopRanked(recs)
	recent := ReplayRecent(recs)
	keep := ReplayKeepSet(recs, nil)
	if len(top) != ReplayTopN || len(recent) != ReplayRecentN {
		t.Fatalf("top %d recent %d, want %d/%d", len(top), len(recent), ReplayTopN, ReplayRecentN)
	}
	for i := 1; i <= total; i++ {
		id := "coop-" + strconv.Itoa(i)
		wantTop := i > total-ReplayTopN
		wantRecent := i > total-ReplayRecentN
		if top[id] != wantTop || recent[id] != wantRecent || keep[id] != (wantTop || wantRecent) {
			t.Errorf("%s: top=%v recent=%v keep=%v, want top=%v recent=%v", id, top[id], recent[id], keep[id], wantTop, wantRecent)
		}
	}

	// The history's TOP 10 mark is the same cut, but only in buckets where
	// it IS a cut: with ReplayTopN or fewer games nothing is marked.
	if cut := ReplayTopRankedCut(recs); len(cut) != ReplayTopN || !cut["coop-"+strconv.Itoa(total)] {
		t.Errorf("TOP 10 cut of a full bucket = %d games, want %d with the #1 in it", len(cut), ReplayTopN)
	}
	if cut := ReplayTopRankedCut(recs[:ReplayTopN]); len(cut) != 0 {
		t.Errorf("a bucket of exactly ReplayTopN games must mark nothing, got %d", len(cut))
	}
	if cut := ReplayTopRankedCut(recs[:ReplayTopN+1]); len(cut) != ReplayTopN || cut["coop-1"] {
		t.Errorf("one game over ReplayTopN must mark the top N and not the #%d", ReplayTopN+1)
	}

	// A newer low score is recent (kept) but not top; it pushes the oldest
	// recent-only game out of the keep set while the top N are untouched.
	oldestRecent := "coop-" + strconv.Itoa(total-ReplayRecentN+1)
	recs = append(recs, mk("coop-low", ModeCooperative, 1, total+1, false))
	keep = ReplayKeepSet(recs, nil)
	if !keep["coop-low"] || ReplayTopRanked(recs)["coop-low"] {
		t.Error("a fresh low score must be kept for recency only")
	}
	if keep[oldestRecent] {
		t.Errorf("%s should have aged out of the recent set", oldestRecent)
	}
	if !keep["coop-"+strconv.Itoa(total)] {
		t.Error("the bucket's #1 must stay kept")
	}

	// A pin keeps a game out of both sets in the keep set all the same —
	// and only while it is set: a pin map entry of false is no pin.
	pinned := map[string]bool{oldestRecent: true, "coop-1": true, "coop-2": false}
	keep = ReplayKeepSet(recs, pinned)
	if !keep[oldestRecent] || !keep["coop-1"] {
		t.Error("pinned games must be kept whatever their rank or age")
	}
	if keep["coop-2"] {
		t.Error("a false pin entry must not keep a game")
	}
	if ReplayTopRanked(recs)[oldestRecent] || ReplayRecent(recs)[oldestRecent] {
		t.Error("a pin must not change the ranking or the recent set")
	}

	// Buckets are per (mode, agents): a low score in a fresh bucket is its
	// top game, and the recent set spans buckets.
	recs = append(recs, mk("comp-1", ModeCompetitive, 1, total+2, false), mk("coop-agent", ModeCooperative, 1, total+3, true))
	top = ReplayTopRanked(recs)
	if !top["comp-1"] || !top["coop-agent"] {
		t.Error("a fresh bucket's only game is its top game")
	}
	if top["coop-low"] {
		t.Error("a low score in a full bucket is not top-ranked")
	}
	recent = ReplayRecent(recs)
	if !recent["comp-1"] || !recent["coop-agent"] || !recent["coop-low"] {
		t.Error("the recent set spans every bucket")
	}

	// Duplicate records (a re-published game) count once, the later copy
	// winning — and records without a game ID are ignored.
	dup := append([]ArchiveRecord(nil), recs...)
	dup = append(dup, mk("coop-low", ModeCooperative, 999999, total+1, false), ArchiveRecord{})
	if !ReplayTopRanked(dup)["coop-low"] {
		t.Error("the later duplicate should supersede the earlier record")
	}
	if n := len(ReplayRecent(dup)); n != ReplayRecentN {
		t.Errorf("recent set with duplicates has %d entries, want %d", n, ReplayRecentN)
	}

	// RecentBefore is a total order: newer finish first, game ID on ties.
	a, b := mk("a", ModeCooperative, 1, 1, false), mk("b", ModeCooperative, 1, 1, false)
	if !a.RecentBefore(b) || b.RecentBefore(a) {
		t.Error("game ID must totally order equal finish times")
	}
	if !mk("z", ModeCooperative, 1, 2, false).RecentBefore(a) {
		t.Error("a newer finish is more recent regardless of ID")
	}
}

// The fewest players a game can be created for, per mode: a co-op game can
// be played alone (for the high score), a team can be a team of one, and a
// competitive game needs an opponent for someone to be the last standing.
func TestMinPlayerCount(t *testing.T) {
	for _, tc := range []struct {
		mode GameMode
		want int
	}{{ModeCooperative, 1}, {ModeTeams, 1}, {ModeCompetitive, 2}} {
		if got := MinPlayerCount(tc.mode); got != tc.want {
			t.Errorf("MinPlayerCount(%v) = %d, want %d", tc.mode, got, tc.want)
		}
	}
	// A solo co-op board is the standard one whatever the width setting.
	if got := SharedBoardWidth(MinPlayerCount(ModeCooperative), MaxExtraColumns); got != StandardWidth {
		t.Errorf("a solo co-op board is %d columns, want %d", got, StandardWidth)
	}
}

// TestBagNormalized: the two named bag kinds read as themselves and
// everything else — absent, the zero value, a kind this build does not know
// — as the standard 7-bag, the rule a meta from before the field (or from a
// newer build) is read by; and every kind has the label the lobby row tags
// it with.
func TestBagNormalized(t *testing.T) {
	for in, want := range map[Bag]Bag{"": BagSingle, BagDouble: BagDouble, BagNone: BagNone, "triple": BagSingle, "7": BagSingle, "Double": BagSingle} {
		if got := in.Normalized(); got != want {
			t.Errorf("Bag(%q).Normalized() = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[Bag]string{BagSingle: "7-bag", BagDouble: "double bag", BagNone: "no bag", "triple": "7-bag"} {
		if got := in.Label(); got != want {
			t.Errorf("Bag(%q).Label() = %q, want %q", in, got, want)
		}
	}
}

// TestRulesBag: the bag rides the rules bundle — the meta's mirror hands it
// back, normalizing folds an unknown kind into the 7-bag — and the Guideline
// preset deals the 7-bag, so the preset with any other bag is custom rules
// (the lobby row then tags the bag instead of "modern").
func TestRulesBag(t *testing.T) {
	if got := (GameMeta{Bag: BagNone}).Rules().Bag; got != BagNone {
		t.Errorf("meta bag none reads back as %q", got)
	}
	if got := (GameRules{Bag: "triple"}).Normalized(ModeCooperative).Bag; got != BagSingle {
		t.Errorf("an unknown kind normalized to %q, want the 7-bag", got)
	}
	if ModernRules().Bag != BagSingle || !ModernRules().IsModern(ModeCompetitive) {
		t.Fatal("the Modern preset should deal the 7-bag")
	}
	for _, bag := range []Bag{BagDouble, BagNone} {
		r := ModernRules()
		r.Bag = bag
		if r.IsModern(ModeCompetitive) || r.IsModern(ModeCooperative) {
			t.Errorf("the preset with bag %q still reads as the Modern preset", bag)
		}
	}
	r := ModernRules()
	r.Bag = "triple"
	if !r.IsModern(ModeCompetitive) {
		t.Error("the preset with an unknown kind — the 7-bag once normalized — should still be the preset")
	}
}

// TestRulesShowHeadroom: the hidden-rows setting rides the rules bundle —
// the meta's mirror hands it back, normalizing leaves it alone in every mode
// — and the Modern preset hides the rows, so the preset with them shown
// is custom rules (the lobby row then tags "hidden rows" instead of
// "modern").
func TestRulesShowHeadroom(t *testing.T) {
	if !(GameMeta{ShowHeadroom: true}).Rules().ShowHeadroom {
		t.Error("a meta showing its headroom reads back hidden")
	}
	if (GameMeta{}).Rules().ShowHeadroom {
		t.Error("a meta without the field shows its headroom")
	}
	for _, mode := range []GameMode{ModeCooperative, ModeCompetitive, ModeTeams} {
		if !(GameRules{ShowHeadroom: true}).Normalized(mode).ShowHeadroom {
			t.Errorf("%s: normalizing dropped the hidden-rows setting", mode)
		}
	}
	if ModernRules().ShowHeadroom {
		t.Fatal("the Modern preset should hide the headroom")
	}
	r := ModernRules()
	r.ShowHeadroom = true
	if r.IsModern(ModeCompetitive) || r.IsModern(ModeCooperative) {
		t.Error("the preset with the hidden rows shown still reads as the Modern preset")
	}
}

// The creation spec is normalized once, for every create path: teams only
// in teams mode (and the total is teams × size), the agent policy within the
// seat count, the shared-board growth, the deal and the scoring only where a
// shared board has seats to share it, the line goal and the rules clamped.
func TestGameSpecNormalized(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   GameSpec
		want GameSpec
	}{
		{"competitive drops the shared-board settings",
			GameSpec{Mode: ModeCompetitive, PlayerCount: 3, ExtraColumns: 6, ExtraRows: 4, Scoring: ScoringIndividual, SplitPieces: true, TeamCount: 2, TeamSize: 1, Rules: GameRules{GarbageHoles: 2}},
			GameSpec{Mode: ModeCompetitive, PlayerCount: 3, Rules: GameRules{GarbageHoles: 2}}},
		{"solo co-op has nobody to split with or rank against",
			GameSpec{Mode: ModeCooperative, PlayerCount: 1, ExtraColumns: 4, ExtraRows: 3, Scoring: ScoringIndividual, SplitPieces: true},
			GameSpec{Mode: ModeCooperative, PlayerCount: 1, ExtraColumns: 4, ExtraRows: 3}},
		{"a co-op crew keeps the split and the scoring, garbage zeroed",
			GameSpec{Mode: ModeCooperative, PlayerCount: 3, ExtraColumns: 5, ExtraRows: 2, Scoring: ScoringIndividual, SplitPieces: true, LineGoal: 40, Rules: GameRules{GarbageHoles: 2, GuidelineGarbage: true}},
			GameSpec{Mode: ModeCooperative, PlayerCount: 3, ExtraColumns: 5, ExtraRows: 2, Scoring: ScoringIndividual, SplitPieces: true, LineGoal: 40}},
		{"teams: the total follows teams × size, a team of one cannot split, the teams are named",
			GameSpec{Mode: ModeTeams, PlayerCount: 99, TeamCount: 3, TeamSize: 1, SplitPieces: true, Scoring: ScoringIndividual, ExtraRows: 11},
			GameSpec{Mode: ModeTeams, PlayerCount: 3, TeamCount: 3, TeamSize: 1, ExtraRows: MaxExtraRows, TeamNames: []string{"Green", "Blue", "Amber"}}},
		{"teams of two split, the team count is normalized, a renamed team keeps its name",
			GameSpec{Mode: ModeTeams, TeamCount: 0, TeamSize: 2, SplitPieces: true, TeamNames: []string{"  Sharks ", "", "Ignored"}},
			GameSpec{Mode: ModeTeams, PlayerCount: 4, TeamCount: DefaultTeamCount, TeamSize: 2, SplitPieces: true, TeamNames: []string{"Sharks", "Blue"}}},
		{"no teams, no team names",
			GameSpec{Mode: ModeCooperative, PlayerCount: 2, TeamNames: []string{"Sharks"}},
			GameSpec{Mode: ModeCooperative, PlayerCount: 2}},
		{"the agent policy is clamped to the seats, the line goal to its cap",
			GameSpec{Mode: ModeCooperative, PlayerCount: 2, MaxAgents: 7, LineGoal: 5000, Scoring: "bogus"},
			GameSpec{Mode: ModeCooperative, PlayerCount: 2, MaxAgents: 2, LineGoal: MaxLineGoal}},
		{"a negative line goal is until top out, a negative agent cap none",
			GameSpec{Mode: ModeCompetitive, PlayerCount: 2, MaxAgents: -1, LineGoal: -40},
			GameSpec{Mode: ModeCompetitive, PlayerCount: 2}},
		{"the agent policy is per team in teams mode: clamped to the players per team",
			GameSpec{Mode: ModeTeams, TeamCount: 2, TeamSize: 2, MaxAgents: 3},
			GameSpec{Mode: ModeTeams, PlayerCount: 4, TeamCount: 2, TeamSize: 2, MaxAgents: 2, TeamNames: []string{"Green", "Blue"}}},
		{"agents pause when alone only in an open game",
			GameSpec{Mode: ModeCooperative, PlayerCount: 2, MaxAgents: 1, AgentsPauseAlone: true, InviteOnly: true},
			GameSpec{Mode: ModeCooperative, PlayerCount: 2, MaxAgents: 1, InviteOnly: true}},
		{"an open game keeps the pause-when-alone rule",
			GameSpec{Mode: ModeCooperative, PlayerCount: 2, MaxAgents: 1, AgentsPauseAlone: true},
			GameSpec{Mode: ModeCooperative, PlayerCount: 2, MaxAgents: 1, AgentsPauseAlone: true}},
	} {
		if got := tc.in.Normalized(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Normalized() = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Team names: the piece colours by default, in piece order; a recorded name
// stands, a blank or a missing one is the colour, an overlong one is cut;
// and every screen that has no names — an old game, the history's tally
// across games — falls back to the letters.
func TestTeamNames(t *testing.T) {
	if got := DefaultTeamNames(MaxTeamCount); !reflect.DeepEqual(got, []string{"Green", "Blue", "Amber", "Violet", "Teal", "Coral"}) {
		t.Errorf("DefaultTeamNames(6) = %v", got)
	}
	if got := DefaultTeamNames(0); len(got) != 0 {
		t.Errorf("DefaultTeamNames(0) = %v", got)
	}
	for _, tc := range []struct {
		names []string
		t     int
		want  string
	}{
		{nil, 0, "A"}, {nil, 2, "C"}, {[]string{"Sharks", "Jets"}, 1, "Jets"}, {[]string{"Sharks", "Jets"}, 2, "C"},
		{[]string{"", "Jets"}, 0, "A"}, {[]string{" Sharks "}, 0, "Sharks"}, {nil, -1, "-1"},
	} {
		if got := TeamName(tc.names, tc.t); got != tc.want {
			t.Errorf("TeamName(%v, %d) = %q, want %q", tc.names, tc.t, got, tc.want)
		}
	}
	long := strings.Repeat("x", MaxTeamNameLen+5)
	got := NormalizeTeamNames([]string{"", long, "Jets"}, 4)
	want := []string{"Green", strings.Repeat("x", MaxTeamNameLen), "Jets", "Violet"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeTeamNames = %v, want %v", got, want)
	}
	if NormalizeTeamNames([]string{"Jets"}, 0) != nil {
		t.Error("no teams, but names")
	}
	meta := GameSpec{Mode: ModeTeams, TeamCount: 2, TeamSize: 1, TeamNames: []string{"Sharks"}}.Normalized().Meta("g", "c", 1, time.Time{})
	if meta.TeamName(0) != "Sharks" || meta.TeamName(1) != "Blue" {
		t.Errorf("meta names = %q / %q", meta.TeamName(0), meta.TeamName(1))
	}
	if rec := (ArchiveRecord{Mode: ModeTeams, TeamNames: []string{"Sharks", "Jets"}}); rec.TeamName(1) != "Jets" || (ArchiveRecord{}).TeamName(1) != "B" {
		t.Error("archive names")
	}
}

// The game's shape as the spec and the meta both derive it: how many boards,
// and how many seats share one.
func TestGameSpecShape(t *testing.T) {
	for _, tc := range []struct {
		spec              GameSpec
		playfields, seats int
	}{
		{GameSpec{Mode: ModeCooperative, PlayerCount: 3}, 1, 3},
		{GameSpec{Mode: ModeCooperative, PlayerCount: 1}, 1, 1},
		{GameSpec{Mode: ModeCompetitive, PlayerCount: 4}, 4, 1},
		{GameSpec{Mode: ModeTeams, TeamCount: 3, TeamSize: 2, PlayerCount: 6}, 3, 2},
		{GameSpec{Mode: ModeTeams, TeamCount: 0, TeamSize: 1, PlayerCount: 2}, DefaultTeamCount, 1},
	} {
		if got := tc.spec.Playfields(); got != tc.playfields {
			t.Errorf("%+v: Playfields() = %d, want %d", tc.spec, got, tc.playfields)
		}
		if got := tc.spec.SeatsPerPlayfield(); got != tc.seats {
			t.Errorf("%+v: SeatsPerPlayfield() = %d, want %d", tc.spec, got, tc.seats)
		}
		meta := tc.spec.Meta("g", "c", 1, time.Time{})
		if got := meta.Playfields(); got != tc.playfields {
			t.Errorf("%+v: meta Playfields() = %d, want %d", tc.spec, got, tc.playfields)
		}
		if got := meta.SeatsPerPlayfield(); got != tc.seats {
			t.Errorf("%+v: meta SeatsPerPlayfield() = %d, want %d", tc.spec, got, tc.seats)
		}
	}
}

// The meta a spec writes carries every setting, and reads back as the spec
// it was written from (less the lobby-only agent policy and invitation). The
// name comes back with it: a named game's ID IS its name, so the ID the meta
// was written under is what Spec() reads the name off.
func TestGameSpecMetaRoundTrip(t *testing.T) {
	spec := GameSpec{
		Name: "game",
		Mode: ModeCooperative, PlayerCount: 3, ExtraColumns: 6, ExtraRows: 5, LineGoal: 40,
		Scoring: ScoringIndividual, SplitPieces: true, MaxAgents: 2, InviteOnly: true,
		Rules: GameRules{NextCount: 3, Ghost: false, Hold: true, Bag: BagDouble, ShowHeadroom: true},
	}.Normalized()
	meta := spec.Meta("game", "creator", 42, time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC))
	if meta.Status != GameStatusCreated || meta.Seed != 42 || meta.CreatorID != "creator" || meta.GameID != "game" {
		t.Errorf("meta header = %+v", meta)
	}
	if !meta.IndividualScoring() || !meta.SplitsPieces() || meta.LineGoal != 40 || meta.ExtraRows != 5 {
		t.Errorf("meta lost a setting: %+v", meta)
	}
	if got, want := meta.BoardWidth(), SharedBoardWidth(3, 6); got != want {
		t.Errorf("BoardWidth() = %d, want %d", got, want)
	}
	if got, want := meta.BoardHeight(), SharedBoardHeight(3, 5); got != want {
		t.Errorf("BoardHeight() = %d, want %d", got, want)
	}
	back := meta.Spec()
	spec.MaxAgents, spec.InviteOnly = 0, false
	if !reflect.DeepEqual(back, spec) {
		t.Errorf("meta.Spec() = %+v, want %+v", back, spec)
	}
	// A competitive meta answers the standard board whatever it recorded.
	comp := GameSpec{Mode: ModeCompetitive, PlayerCount: 2}.Normalized().Meta("g", "c", 1, time.Time{})
	if comp.BoardWidth() != StandardWidth || comp.BoardHeight() != StandardHeight {
		t.Errorf("competitive board = %d×%d, want %d×%d", comp.BoardWidth(), comp.BoardHeight(), StandardWidth, StandardHeight)
	}
}

// Individual scoring is a single shared playfield's choice: only a
// cooperative-mode board with company honours it, however the record reads.
func TestScoringNormalized(t *testing.T) {
	if got := Scoring("bogus").Normalized(); got != ScoringShared {
		t.Errorf("bogus scoring reads as %q, want shared", got)
	}
	if got := ScoringIndividual.Normalized(); got != ScoringIndividual {
		t.Errorf("individual reads as %q", got)
	}
	for _, tc := range []struct {
		meta GameMeta
		want bool
	}{
		{GameMeta{Mode: ModeCooperative, PlayerCount: 2, Scoring: ScoringIndividual}, true},
		{GameMeta{Mode: ModeCooperative, PlayerCount: 1, Scoring: ScoringIndividual}, false},
		{GameMeta{Mode: ModeCooperative, PlayerCount: 2}, false},
		{GameMeta{Mode: ModeCompetitive, PlayerCount: 2, Scoring: ScoringIndividual}, false},
		{GameMeta{Mode: ModeTeams, PlayerCount: 4, TeamCount: 2, TeamSize: 2, Scoring: ScoringIndividual}, false},
	} {
		if got := tc.meta.IndividualScoring(); got != tc.want {
			t.Errorf("%+v: IndividualScoring() = %v, want %v", tc.meta, got, tc.want)
		}
	}
}

// The extra-rows setting: a shared board is the standard height for its
// first seat and extraRows more for every seat after it; zero — the default,
// and every meta written before the field — adds nothing, unlike the columns.
func TestSharedBoardHeight(t *testing.T) {
	for _, tc := range []struct{ players, extra, want int }{
		{1, 0, 24}, {2, 0, 24}, {5, 0, 24}, // the default: the Guideline board whatever the crew
		{1, 10, 24}, {2, 5, 29}, {3, 5, 34}, {4, 4, 36},
		{2, 10, 34}, {3, 10, 44},
		{2, -3, 24}, {2, 99, 34}, {0, 5, 24}, // out of range: clamped
	} {
		if got := SharedBoardHeight(tc.players, tc.extra); got != tc.want {
			t.Errorf("SharedBoardHeight(%d, %d) = %d, want %d", tc.players, tc.extra, got, tc.want)
		}
	}
	if TeamBoardHeight(2, 6) != SharedBoardHeight(2, 6) {
		t.Error("a team board grows like the cooperative one")
	}
	if StandardHeight != TotalRows {
		t.Errorf("StandardHeight = %d, want %d", StandardHeight, TotalRows)
	}
}

func TestExtraRowsPerPlayer(t *testing.T) {
	for in, want := range map[int]int{0: 0, -1: 0, 1: 1, 4: 4, 10: 10, 11: MaxExtraRows} {
		if got := ExtraRowsPerPlayer(in); got != want {
			t.Errorf("ExtraRowsPerPlayer(%d) = %d, want %d", in, got, want)
		}
	}
	if DefaultExtraRows != MinExtraRows || MinExtraRows != 0 {
		t.Errorf("DefaultExtraRows = %d, MinExtraRows = %d, want both 0", DefaultExtraRows, MinExtraRows)
	}
}

func TestNormalizeLineGoal(t *testing.T) {
	for in, want := range map[int]int{0: 0, -5: 0, 1: 1, 40: 40, MaxLineGoal: MaxLineGoal, MaxLineGoal + 1: MaxLineGoal} {
		if got := NormalizeLineGoal(in); got != want {
			t.Errorf("NormalizeLineGoal(%d) = %d, want %d", in, got, want)
		}
	}
	if DefaultLineGoal != 40 {
		t.Errorf("DefaultLineGoal = %d, want the sprint's 40", DefaultLineGoal)
	}
}

// A shared board whose seats were scored on their own ranks by its best
// player, like a competitive game, and never in the same replay bucket as a
// crew's shared total.
func TestArchiveIndividualScoring(t *testing.T) {
	shared := ArchiveRecord{Mode: ModeCooperative, TotalScore: 40, Players: []PlayerResult{{PlayerID: "a", Score: 99}, {PlayerID: "b", Score: 12}}}
	individual := shared
	individual.Scoring = ScoringIndividual
	if got := shared.HeadlineScore(); got != 40 {
		t.Errorf("shared headline = %d, want 40", got)
	}
	if got := individual.HeadlineScore(); got != 99 {
		t.Errorf("individual headline = %d, want 99", got)
	}
	if shared.SameReplayBucket(individual) || !individual.SameReplayBucket(individual) {
		t.Error("individual and shared co-op must not share a replay bucket")
	}
	if !shared.IndividualScoring() == false || individual.IndividualScoring() != true {
		t.Error("IndividualScoring() misreads the record")
	}
	comp := ArchiveRecord{Mode: ModeCompetitive, Scoring: ScoringIndividual}
	if comp.IndividualScoring() {
		t.Error("only a cooperative-mode board scores individually")
	}
}
