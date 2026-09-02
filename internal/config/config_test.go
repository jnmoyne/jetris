package config

import (
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

// Height is fixed: 20 visible rows over 4 of headroom, whatever the mode, the
// player count or the number of opponents that can send garbage.
func TestBoardHeightIsFixed(t *testing.T) {
	if VisibleRows != 20 {
		t.Errorf("VisibleRows = %d, want 20", VisibleRows)
	}
	if TotalRows != HeadroomRows+VisibleRows {
		t.Errorf("TotalRows = %d, want %d", TotalRows, HeadroomRows+VisibleRows)
	}
	if VisibleRowStart != HeadroomRows {
		t.Errorf("VisibleRowStart = %d, want %d", VisibleRowStart, HeadroomRows)
	}
}

// An archived game replays at the board it was PLAYED on: a record written
// since every board became TotalRows tall says so, and one written before it
// means the board of its day — 24 visible rows plus one per player that could
// attack it.
func TestArchiveBoardHeight(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  ArchiveRecord
		want int
	}{
		{"recorded", ArchiveRecord{Mode: ModeCompetitive, PlayerCount: 3, BoardRows: TotalRows}, TotalRows},
		{"legacy competitive", ArchiveRecord{Mode: ModeCompetitive, PlayerCount: 3}, 4 + 24 + 3},
		{"legacy cooperative", ArchiveRecord{Mode: ModeCooperative, PlayerCount: 3}, 4 + 24},
		{"legacy duel", ArchiveRecord{Mode: ModeTeams, TeamCount: 2, TeamSize: 2}, 4 + 24 + 2},
		{"legacy three-way", ArchiveRecord{Mode: ModeTeams, TeamCount: 3, TeamSize: 2}, 4 + 24 + 4},
		{"legacy teams, no count", ArchiveRecord{Mode: ModeTeams, TeamSize: 2}, 4 + 24 + 2},
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
	keep := ReplayKeepSet(recs)
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
	keep = ReplayKeepSet(recs)
	if !keep["coop-low"] || ReplayTopRanked(recs)["coop-low"] {
		t.Error("a fresh low score must be kept for recency only")
	}
	if keep[oldestRecent] {
		t.Errorf("%s should have aged out of the recent set", oldestRecent)
	}
	if !keep["coop-"+strconv.Itoa(total)] {
		t.Error("the bucket's #1 must stay kept")
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
