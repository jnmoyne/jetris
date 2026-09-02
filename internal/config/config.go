package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	NATSContext  string
	NATSURL      string
	NATSUser     string
	NATSPassword string
	// PlayerName is a name given before the login screen is ever drawn
	// (--name, or the browser page's ?player=, which the join page sets):
	// the screen plays its own Play button with it and goes straight to the
	// lobby. Empty means the usual "type your name" login.
	PlayerName string
	// ServerLabel names NATSURL for the player (the browser page's ?name=):
	// the server browser's row shows it instead of "--server", and the lobby
	// header reads "<label> (<url>)" the way a favorite's does.
	ServerLabel  string
	RunEmbedded  bool   // run an in-process JetStream-enabled nats-server and connect to it
	EmbeddedHost string // address the embedded server is advertised and dialed on ("" = auto-detected LAN IP); it always LISTENS on every interface, so this only overrides a wrong auto-detection
	EmbeddedPort int    // port for the embedded server (0 = DefaultEmbeddedPort)
}

// Embedded-server settings for the login screen's "LAN party mode (embedded NATS
// server)" option: the default port the in-process server listens on (all
// interfaces; the player can override it in the picker) and the local
// directory holding its JetStream storage.
const (
	DefaultEmbeddedPort = 4222
	EmbeddedStoreDir    = "jetstream-data"
)

// ValidatePlayerName checks that a player name is valid for use as a
// NATS subject token (and thus as a player ID). It rejects empty names,
// names longer than 32 characters, and names containing characters that
// are not allowed in NATS subject tokens.
func ValidatePlayerName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("player name cannot be empty")
	}
	if len(name) > 32 {
		return fmt.Errorf("player name cannot be longer than 32 characters")
	}
	for _, c := range name {
		switch {
		case c == '.' || c == ' ' || c == '*' || c == '>' || c == '\t' || c == '\n' || c == '\r' || c == 0:
			return fmt.Errorf("player name cannot contain %q", c)
		}
	}
	return nil
}

type GameMode int

const (
	ModeCooperative GameMode = iota
	ModeCompetitive
	ModeTeams
)

func (m GameMode) String() string {
	switch m {
	case ModeCooperative:
		return "cooperative"
	case ModeCompetitive:
		return "competitive"
	case ModeTeams:
		return "teams"
	default:
		return "unknown"
	}
}

// The number of teams a teams-mode game is played between. A game records
// its own count in GameMeta.TeamCount; two — Team A vs Team B — is the
// default and what every meta written before the field reads as. Team
// indices run 0..count-1 and are named by their letter (TeamLetter).
const (
	DefaultTeamCount = 2
	MinTeamCount     = 2
	MaxTeamCount     = 6
)

// NormalizeTeamCount reads a recorded team count: absent (the zero value, and
// every meta or listing written before the field) is the historical two, and
// anything out of range is clamped into MinTeamCount..MaxTeamCount.
func NormalizeTeamCount(n int) int {
	if n <= 0 {
		return DefaultTeamCount
	}
	return min(max(n, MinTeamCount), MaxTeamCount)
}

// TeamLetter names a team index the way every screen shows it: A, B, C, …
// (the index itself once past the letters, which MaxTeamCount never allows).
func TeamLetter(team int) string {
	if team < 0 || team >= 26 {
		return strconv.Itoa(team)
	}
	return string(rune('A' + team))
}

type GameStatus string

const (
	GameStatusCreated    GameStatus = "created"
	GameStatusStarting   GameStatus = "starting"
	GameStatusInProgress GameStatus = "in_progress"
	GameStatusFinished   GameStatus = "finished"
	GameStatusArchived   GameStatus = "archived"
	GameStatusCancelled  GameStatus = "cancelled"
)

type GameMeta struct {
	GameID             string     `json:"game_id"`
	Mode               GameMode   `json:"mode"`
	PlayerCount        int        `json:"player_count"`
	TeamCount          int        `json:"team_count,omitempty"`           // teams mode: how many teams play each other (MinTeamCount..MaxTeamCount). Absent — the zero value, and every meta written before the field — reads as DefaultTeamCount, the historical Team A vs Team B (see Teams)
	TeamSize           int        `json:"team_size,omitempty"`            // teams mode: players per team (PlayerCount = TeamCount*TeamSize)
	ExtraColumns       int        `json:"extra_columns,omitempty"`        // shared boards (cooperative, teams): columns every seat beyond the first adds to the board's standard 10 (MinExtraColumns..MaxExtraColumns), and the spacing between neighbouring spawn points. Absent — every meta written before the field — reads as MaxExtraColumns: the historical full section per player (see ExtraColumnsPerPlayer). Meaningless in competitive, where each player has a board of their own
	NextCount          int        `json:"next_count"`                     // how many upcoming pieces are shown (0..MaxNextCount); bounds lookahead for humans and agents alike
	NoGhost            bool       `json:"no_ghost,omitempty"`             // hard-drop ghost preview disabled for this game; inverted so the zero value — and metas written before the field — keep the ghost SHOWN (the default). Meta, not listing: like NextCount it is one rule for every player
	Hold               bool       `json:"hold,omitempty"`                 // the Guideline hold queue is on: a player may swap the falling piece for a held one, once per piece, the piece coming out re-entering at the spawn point (see GameRules.Hold). Unset — the default, and every meta written before the field — no hold. One rule for every seat, like NextCount; agents may use it or ignore it
	GarbageHoles       int        `json:"garbage_holes,omitempty"`        // holes punched in every garbage row a raise lands (0..MaxGarbageHoles; competitive/teams). 0 — the zero value, and every meta written before the field — raises solid rows that never clear; with holes, a garbage row clears like any other line once its holes are filled
	RandomGarbageHoles bool       `json:"random_garbage_holes,omitempty"` // every garbage row draws its own hole columns ("messy" garbage); unset — the default, and every meta written before the field — every row of one raise shares a single draw, so its holes line up into a well ("clean" garbage). Moot at GarbageHoles 0
	GuidelineGarbage   bool       `json:"guideline_garbage,omitempty"`    // attack strength follows the Tetris Guideline table — a single sends no garbage, a double 1 row, a triple 2, a Tetris 4 (game.AttackRows); unset — the default, and every meta written before the field — every cleared line sends one row
	SplitPieces        bool       `json:"split_pieces,omitempty"`         // teams mode: the seven piece types are dealt out between the teammates (rng.PieceSets), every seat drawing only from its own ration and the whole bag present across the team. Unset — the default, and every meta written before the field — every seat runs the full 7-bag. Structural like TeamSize, not a play rule: the deal follows Seed, so both teams' slot N hold the same ration (see SplitsPieces)
	Seed               uint64     `json:"seed"`
	Status             GameStatus `json:"status"`
	CreatorID          string     `json:"creator_id"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          time.Time  `json:"started_at,omitempty"`
	FinishedAt         time.Time  `json:"finished_at,omitempty"`
	Abandoned          bool       `json:"abandoned,omitempty"`
	PieceIdx           uint64     `json:"piece_idx"`
}

// GameRules are the per-game PLAY rules a creator picks in the create wizard,
// bundled for the create paths (lobby.CreateGame and the UI's createGame /
// openInvitePicker): every field lands in GameMeta — the rule book every
// engine reads at Start — and is mirrored on the lobby listing for the row's
// tags. The zero value is the pre-attribute Jetris game (no preview, no hold,
// solid garbage, one row per line) except for Ghost, which the wizard defaults
// on — the meta stores it inverted (NoGhost) for the same reason.
type GameRules struct {
	NextCount          int  // upcoming pieces the game reveals (0..MaxNextCount)
	Ghost              bool // the hard-drop ghost preview (GameMeta.NoGhost, inverted)
	Hold               bool // the Guideline hold queue (GameMeta.Hold): swap the falling piece for a held one, once per piece
	GarbageHoles       int  // holes per garbage row in the modes that raise garbage (0..MaxGarbageHoles; 0 = solid rows that never clear)
	RandomGarbageHoles bool // every garbage row draws its own hole columns (off: the rows of one attack share a draw)
	GuidelineGarbage   bool // attack strength by the Guideline table — 0/1/2/4 rows for 1/2/3/4 lines (off: one row per line)
}

// GuidelineRules is the create wizard's "Guideline" preset: every rule at the
// setting closest to the Tetris Guideline this game can offer — the longest
// next queue the game reveals (MaxNextCount), the ghost piece, the hold queue,
// and Guideline-style garbage: one hole per row, the rows of one attack
// sharing it (clean garbage that digs out as a well), attacks by the
// Guideline table.
func GuidelineRules() GameRules {
	return GameRules{
		NextCount:        MaxNextCount,
		Ghost:            true,
		Hold:             true,
		GarbageHoles:     1,
		GuidelineGarbage: true,
	}
}

// Normalized returns the rules clamped to their legal ranges as a game of
// mode stores them: NextCount and GarbageHoles within their caps, random
// holes only meaningful with holes — and, since a cooperative game raises no
// garbage, its garbage rules zeroed so no listing tag misleads.
func (r GameRules) Normalized(mode GameMode) GameRules {
	r.NextCount = min(max(r.NextCount, 0), MaxNextCount)
	if mode == ModeCooperative {
		r.GarbageHoles, r.RandomGarbageHoles, r.GuidelineGarbage = 0, false, false
	}
	r.GarbageHoles = min(max(r.GarbageHoles, 0), MaxGarbageHoles)
	r.RandomGarbageHoles = r.RandomGarbageHoles && r.GarbageHoles > 0
	return r
}

// IsGuideline reports whether the rules are exactly the Guideline preset as a
// game of mode stores it (a cooperative game has no garbage rules to match).
func (r GameRules) IsGuideline(mode GameMode) bool {
	return r.Normalized(mode) == GuidelineRules().Normalized(mode)
}

// Rules is the play-rule bundle of a game's meta record.
func (m GameMeta) Rules() GameRules {
	return GameRules{
		NextCount:          m.NextCount,
		Ghost:              !m.NoGhost,
		Hold:               m.Hold,
		GarbageHoles:       m.GarbageHoles,
		RandomGarbageHoles: m.RandomGarbageHoles,
		GuidelineGarbage:   m.GuidelineGarbage,
	}
}

// Teams is the number of teams this game is played between, normalized:
// a teams game's recorded TeamCount (two when the meta predates the field),
// and 0 in every other mode, which has no teams at all.
func (m GameMeta) Teams() int {
	if m.Mode != ModeTeams {
		return 0
	}
	return NormalizeTeamCount(m.TeamCount)
}

// SplitsPieces reports whether this game deals its piece types out between
// teammates: the SplitPieces setting, which only a teams game of at least two
// per team can honour (a team of one would be dealt the whole bag anyway, and
// no other mode has teammates to split between). The one place the rule is
// decided — engines, the lobby row and the HUD all ask here.
func (m GameMeta) SplitsPieces() bool {
	return m.SplitPieces && m.Mode == ModeTeams && m.TeamSize > 1
}

// PlayerResult captures per-player stats at game end.
type PlayerResult struct {
	PlayerID   string `json:"player_id"`
	Score      int    `json:"score"`
	Level      int    `json:"level,omitempty"` // level achieved at game end (from the player's line total)
	PieceCount uint64 `json:"piece_count"`
	Winner     bool   `json:"winner,omitempty"`
	Team       int    `json:"team,omitempty"`  // teams mode: 0 = A, 1 = B
	Agent      bool   `json:"agent,omitempty"` // seat was played by an agent (from the roster at archive time)
}

// ArchiveRecord is published to the archive stream when a game finishes.
type ArchiveRecord struct {
	GameID       string         `json:"game_id"`
	Mode         GameMode       `json:"mode"`
	PlayerCount  int            `json:"player_count"`
	Players      []PlayerResult `json:"players"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   time.Time      `json:"finished_at"`
	TotalScore   int            `json:"total_score,omitempty"`   // cooperative
	FinalLevel   int            `json:"final_level,omitempty"`   // cooperative: shared level at game end
	TeamCount    int            `json:"team_count,omitempty"`    // teams mode: how many teams played (GameMeta.TeamCount); absent — every record written before the field — reads as DefaultTeamCount (see Teams)
	TeamSize     int            `json:"team_size,omitempty"`     // teams mode
	ExtraColumns int            `json:"extra_columns,omitempty"` // shared boards: columns per seat beyond the first (GameMeta.ExtraColumns) — what the replay rebuilds the board's width from
	BoardRows    int            `json:"board_rows,omitempty"`    // the boards' height (headroom + visible) the game was played on — what the replay rebuilds them at; absent reads as the pre-fixed-height board (see BoardHeight)
	WinningTeam  int            `json:"winning_team"`            // teams mode: the winning team's index; -1 = draw or not a team game
	TeamScores   []int          `json:"team_scores,omitempty"`   // teams mode: final score per team (indexed by team)
	TeamLevels   []int          `json:"team_levels,omitempty"`   // teams mode: final level per team (indexed by team)
	Boards       []BoardPicture `json:"boards,omitempty"`        // end-of-game playfield snapshot(s) for the lobby's history view
	Chat         []ChatLine     `json:"chat,omitempty"`          // the game's chat history (last ArchiveChatCap lines), captured before the chat purge
}

// Teams is the number of teams the archived game was played between,
// normalized the way GameMeta.Teams is: the recorded TeamCount, falling back
// to the length of the per-team totals for a record written before the field
// (and to DefaultTeamCount for one that carries neither), and 0 outside teams
// mode.
func (r ArchiveRecord) Teams() int {
	if r.Mode != ModeTeams {
		return 0
	}
	if r.TeamCount <= 0 && len(r.TeamScores) > 0 {
		return NormalizeTeamCount(len(r.TeamScores))
	}
	return NormalizeTeamCount(r.TeamCount)
}

// legacyVisibleRows is the visible height a board had before every board
// became VisibleRows tall: this many rows plus one for each player that could
// send it garbage. Nothing is played on such a board any more — it survives
// only to rebuild an archived game at the board it was actually played on.
const legacyVisibleRows = 24

// BoardHeight is the total rows (headroom + visible) of the boards the
// archived game was played on, which is what its replay rebuilds them at. A
// record written since every board became the same height carries it; an
// older one carries nothing, and means the board of its day — the visible
// rows of the time plus one per player that could attack it (every opponent
// in competitive, every seat on every OTHER team in teams, none in
// cooperative) — so an old game still replays at its own board.
func (r ArchiveRecord) BoardHeight() int {
	if r.BoardRows > 0 {
		return r.BoardRows
	}
	switch r.Mode {
	case ModeCompetitive:
		return HeadroomRows + legacyVisibleRows + r.PlayerCount
	case ModeTeams:
		return HeadroomRows + legacyVisibleRows + max(r.Teams()-1, 1)*r.TeamSize
	default:
		return HeadroomRows + legacyVisibleRows
	}
}

// ChatLine is one chat message preserved in an ArchiveRecord. The game's chat
// is purged from the shared chat stream when the game is archived, so the
// archiver copies the conversation into the record first — it is the only
// place the history survives for the lobby's archived-game viewer. (Records
// from before this field simply have no chat.)
type ChatLine struct {
	Name      string    `json:"name"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"ts,omitempty"`
	Spectator bool      `json:"spectator,omitempty"`
}

// ArchiveChatCap bounds the chat lines embedded in one ArchiveRecord (the
// most recent lines win; matches the in-memory lobby chat cap so nothing a
// client still holds is ever dropped).
const ArchiveChatCap = 200

// Replay retention. A finished game keeps a full replay archive (its game
// stream copied to the file-backed replay stream before deletion) while it is
// in the KEEP SET — see ReplayKeepSet — which is the union of:
//
//   - the top ReplayTopN games of its replay bucket — one bucket per (mode,
//     with/without agents) pair, ranked by RankBefore — the all-time
//     showcase; and
//   - the ReplayRecentN most recently finished games overall (RecentBefore),
//     so everyone can watch their own game again right after playing it even
//     when it never ranked.
//
// Every finishing game is by definition among the most recent, so every game
// gets a replay at first; it survives the next ReplayRecentN finishes only by
// ranking in its bucket's top N. Games that fall out of both sets lose their
// replay (the archiver purges them — see archive.maybeArchiveReplay).
const (
	ReplayTopN    = 10
	ReplayRecentN = 25
)

// HeadlineScore is the score a finished game is ranked (and listed) by: the
// shared total for cooperative, the best team's total for teams, and the best
// player's score for competitive.
func (r ArchiveRecord) HeadlineScore() int {
	switch r.Mode {
	case ModeCooperative:
		return r.TotalScore
	case ModeTeams:
		if len(r.TeamScores) > 0 {
			best := r.TeamScores[0]
			for _, s := range r.TeamScores[1:] {
				if s > best {
					best = s
				}
			}
			return best
		}
	}
	best := 0
	for _, p := range r.Players {
		if p.Score > best {
			best = p.Score
		}
	}
	return best
}

// Duration is how long the game lasted; zero for records missing either
// timestamp (or with a clock skew that made finish precede start).
func (r ArchiveRecord) Duration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	if d := r.FinishedAt.Sub(r.StartedAt); d > 0 {
		return d
	}
	return 0
}

// RankBefore reports whether r outranks o in the "By score" game ordering
// shared by the lobby's history list and the replay archiver's top-N cut:
// higher headline score first, a shorter game breaking a score tie, then the
// more recent finish — with the game ID as a final total-order tie-break so
// every client computes the identical top N.
func (r ArchiveRecord) RankBefore(o ArchiveRecord) bool {
	if si, sj := r.HeadlineScore(), o.HeadlineScore(); si != sj {
		return si > sj
	}
	if di, dj := r.Duration(), o.Duration(); di != dj {
		return di < dj
	}
	if !r.FinishedAt.Equal(o.FinishedAt) {
		return r.FinishedAt.After(o.FinishedAt)
	}
	return r.GameID < o.GameID
}

// RecentBefore reports whether r finished more recently than o — the "most
// recent games" total order (finish time, newest first; game ID as the
// tie-break so every archiver cuts the identical ReplayRecentN).
func (r ArchiveRecord) RecentBefore(o ArchiveRecord) bool {
	if !r.FinishedAt.Equal(o.FinishedAt) {
		return r.FinishedAt.After(o.FinishedAt)
	}
	return r.GameID < o.GameID
}

// SameReplayBucket reports whether two records compete for the same replay
// top-N: same mode, and both with (or both without) agent seats.
func (r ArchiveRecord) SameReplayBucket(o ArchiveRecord) bool {
	return r.Mode == o.Mode && r.HasAgents() == o.HasAgents()
}

// replayBucketKey identifies a record's replay bucket (see SameReplayBucket).
type replayBucketKey struct {
	mode   GameMode
	agents bool
}

// uniqueRecords collapses recs to one record per game ID (the last occurrence
// wins, matching an archive-stream drain where a re-published record
// supersedes the earlier copy), in first-seen order.
func uniqueRecords(recs []ArchiveRecord) []ArchiveRecord {
	idx := make(map[string]int, len(recs))
	out := make([]ArchiveRecord, 0, len(recs))
	for _, r := range recs {
		if r.GameID == "" {
			continue
		}
		if i, dup := idx[r.GameID]; dup {
			out[i] = r
			continue
		}
		idx[r.GameID] = len(out)
		out = append(out, r)
	}
	return out
}

// rankedReplayBuckets groups recs (one record per game ID) into their replay
// buckets, each ordered by RankBefore.
func rankedReplayBuckets(recs []ArchiveRecord) map[replayBucketKey][]ArchiveRecord {
	buckets := make(map[replayBucketKey][]ArchiveRecord)
	for _, r := range uniqueRecords(recs) {
		k := replayBucketKey{r.Mode, r.HasAgents()}
		buckets[k] = append(buckets[k], r)
	}
	for _, b := range buckets {
		sort.SliceStable(b, func(i, j int) bool { return b[i].RankBefore(b[j]) })
	}
	return buckets
}

// ReplayTopRanked returns the game IDs that rank in the top ReplayTopN of
// their replay bucket among recs — the first half of the replay keep set.
func ReplayTopRanked(recs []ArchiveRecord) map[string]bool {
	top := make(map[string]bool)
	for _, b := range rankedReplayBuckets(recs) {
		for i, r := range b {
			if i >= ReplayTopN {
				break
			}
			top[r.GameID] = true
		}
	}
	return top
}

// ReplayTopRankedCut returns the game IDs the history marks as TOP 10: the
// top ReplayTopN of every bucket that holds MORE than ReplayTopN games. In a
// bucket of ten or fewer every game is trivially in its top ten — the mark
// would light up the whole list and say nothing — so it appears only once
// the ranking is an actual cut, for the games that beat others to stay in.
// (Retention uses ReplayTopRanked, where the trivial case is exactly right.)
func ReplayTopRankedCut(recs []ArchiveRecord) map[string]bool {
	top := make(map[string]bool)
	for _, b := range rankedReplayBuckets(recs) {
		if len(b) <= ReplayTopN {
			continue
		}
		for _, r := range b[:ReplayTopN] {
			top[r.GameID] = true
		}
	}
	return top
}

// ReplayRank is where rec stands in the "By score" ranking of its replay
// bucket (same mode, with/without agents) among recs — the order behind the
// history's TOP 10 mark and the replay top-N cut: rank 1 is the bucket's best
// game, of is the bucket's size. rec itself always counts, whether or not
// recs already holds it (its archive round-trip may still be in flight), so
// a game is ranked against everything before it plus itself.
func ReplayRank(recs []ArchiveRecord, rec ArchiveRecord) (rank, of int) {
	rank, of = 1, 1
	for _, r := range uniqueRecords(recs) {
		if r.GameID == rec.GameID || !r.SameReplayBucket(rec) {
			continue
		}
		of++
		if r.RankBefore(rec) {
			rank++
		}
	}
	return rank, of
}

// ReplayRecent returns the game IDs of the ReplayRecentN most recently
// finished games among recs (all buckets together) — the second half of the
// replay keep set.
func ReplayRecent(recs []ArchiveRecord) map[string]bool {
	all := uniqueRecords(recs)
	sort.SliceStable(all, func(i, j int) bool { return all[i].RecentBefore(all[j]) })
	recent := make(map[string]bool, ReplayRecentN)
	for i, r := range all {
		if i >= ReplayRecentN {
			break
		}
		recent[r.GameID] = true
	}
	return recent
}

// ReplayKeepSet returns the game IDs whose replay is retained given the full
// set of archive records: ReplayTopRanked ∪ ReplayRecent. A pure function of
// the records, so every archiver — GUI or agent — computes the same set and
// purges the same displaced replays.
func ReplayKeepSet(recs []ArchiveRecord) map[string]bool {
	keep := ReplayTopRanked(recs)
	for id := range ReplayRecent(recs) {
		keep[id] = true
	}
	return keep
}

// HasAgents reports whether any seat in the archived game was played by an
// agent (records from before the agent flag simply read as all-human).
func (r ArchiveRecord) HasAgents() bool {
	for _, p := range r.Players {
		if p.Agent {
			return true
		}
	}
	return false
}

// Agent-composition classes for the history's primary sort order, most
// "interesting" first: a pure agent-vs-agent match, then a mixed human/agent
// game, then an all-human game.
const (
	AgentClassAgentsOnly = iota // every seat an agent
	AgentClassMixed             // at least one agent and at least one human
	AgentClassHumansOnly        // no agent seats (also empty/legacy rosters)
)

// AgentClass buckets the game by who played it (AgentClassAgentsOnly <
// AgentClassMixed < AgentClassHumansOnly) so the history can group agent
// showcases ahead of mixed games ahead of all-human games.
func (r ArchiveRecord) AgentClass() int {
	agents, humans := 0, 0
	for _, p := range r.Players {
		if p.Agent {
			agents++
		} else {
			humans++
		}
	}
	switch {
	case agents > 0 && humans == 0:
		return AgentClassAgentsOnly
	case agents > 0:
		return AgentClassMixed
	default:
		return AgentClassHumansOnly
	}
}

// BoardPicture is a saved snapshot of one board as it stood when the game
// ended: the latest cell messages from the (now-deleted) game stream for the
// board's visible region. It is embedded in an ArchiveRecord so the lobby can
// redraw the final playfield. There is one picture for cooperative, one per
// player for competitive, and one per team for teams mode.
type BoardPicture struct {
	Label  string      `json:"label,omitempty"` // player ID, "Team A"/"Team B"/…, or "" (cooperative)
	Idx    int         `json:"idx"`             // player/team index for coloring; -1 if not applicable
	Width  int         `json:"w"`               // board width in cells
	Height int         `json:"h"`               // visible row count stored (row 0 = first visible row)
	Cells  []BoardCell `json:"cells,omitempty"` // sparse: only the non-empty cells
}

// BoardCell is one non-empty cell of a BoardPicture. Data is the raw cell
// message exactly as it was published to the game stream (see game.Cell), so a
// renderer reconstructs the cell with the same unmarshal path used live.
type BoardCell struct {
	Row  int             `json:"r"` // 0-based within the stored visible region
	Col  int             `json:"c"`
	Data json.RawMessage `json:"d"`
}

const (
	// Every board in every mode is the same height: 20 visible rows — the
	// Guideline playfield — above which sit the hidden headroom rows a piece
	// spawns in. Neither the player count nor the number of opponents that
	// can send garbage changes it.
	HeadroomRows    = 4
	VisibleRows     = 20
	VisibleRowStart = HeadroomRows
	TotalRows       = HeadroomRows + VisibleRows
	StandardWidth   = 10

	// MaxNextCount caps GameMeta.NextCount, the per-game number of upcoming
	// pieces shown to players (0 = none). The same bound applies to agents:
	// an agent may look ahead at most NextCount pieces in the sequence.
	MaxNextCount = 6

	// MaxGarbageHoles caps GameMeta.GarbageHoles, the per-game number of empty
	// cells punched in every garbage row a raise lands (0 = solid, permanent
	// rows). The holes of one raise share their columns on every row it lands.
	MaxGarbageHoles = 4

	// A shared board (cooperative, or one team's board) is StandardWidth
	// columns for its first player and GameMeta.ExtraColumns more for every
	// player after them — the create wizard's board-width slider, between
	// MinExtraColumns and MaxExtraColumns. The extra columns are also the
	// spacing between neighbouring spawn points (SharedSpawnOffset), so at
	// the minimum of 4 the seats sit shoulder to shoulder (a 4-wide I still
	// clears its neighbour's spawn box) and at the maximum of 10 every player
	// gets a full standard section of their own — the board Jetris had before
	// the slider, which is why a meta without the field reads as 10.
	MinExtraColumns     = 4
	MaxExtraColumns     = StandardWidth
	DefaultExtraColumns = MinExtraColumns

	// LockDelay is the Guideline lock delay: a piece that lands on the stack
	// (or the floor) locks this long after landing, unless a successful shift
	// or rotation restarts the timer — at most LockDelayMoveResets times per
	// piece, the allowance renewed whenever the piece falls to a new lowest
	// row. Only a hard drop locks at once. Each engine times its own piece;
	// nothing about the delay is on the wire.
	LockDelay           = 500 * time.Millisecond
	LockDelayMoveResets = 15

	LobbyKVBucket     = "JETRIS_LOBBY"
	ChatStream        = "JETRIS_CHAT"
	LobbyChatGameID   = "lobby" // reserved chat "game ID" for the lobby chat (real game IDs are UUIDs, so no collision)
	LobbyChatSubject  = chatSubjectPrefix + LobbyChatGameID
	ArchiveStream     = "JETRIS_ARCHIVE"
	ArchiveSubject    = "jetris.archive"
	ChatMaxAge        = 7 * 24 * time.Hour
	PresenceHeartbeat = 30 * time.Second
	// PresenceTTL is the per-key expiry on lobby presence entries: a player's
	// presence key self-deletes this long after its last heartbeat, so a client
	// that crashes or drops off is removed from the lobby by the server (a KV
	// TTL-expiry delete event) without any client watching last-seen timestamps.
	// Comfortably longer than PresenceHeartbeat so a live client is never
	// dropped between beats.
	PresenceTTL = 5 * time.Minute
)

// Abandoned-game detection: every client re-checks the lobby's games on a
// timer. A started (in-progress) game is abandoned once its stream has seen no
// messages for AbandonedIdleTimeout; a game that was created but never started
// is abandoned AbandonedUnstartedTimeout after creation. Abandoned games grow
// a Delete button in the lobby.
const (
	AbandonedCheckInterval    = 1 * time.Minute
	AbandonedIdleTimeout      = 1 * time.Minute
	AbandonedUnstartedTimeout = 15 * time.Minute
)

// ExtraColumnsPerPlayer clamps a game's extra-columns setting to its legal
// range. Zero — the value every meta, listing and archive record written
// before the field carries — means the historical board: a full StandardWidth
// section per player, which is exactly MaxExtraColumns, so an old game still
// reconstructs at the width it was played on.
func ExtraColumnsPerPlayer(extraCols int) int {
	if extraCols <= 0 {
		return MaxExtraColumns
	}
	return min(max(extraCols, MinExtraColumns), MaxExtraColumns)
}

// SharedBoardWidth returns the width of a board shared by players seats — the
// cooperative board (players = PlayerCount) or one team's board (players =
// TeamSize): the standard 10 columns the first seat needs, plus extraCols for
// every seat after it. So a game created with the slider's default of 4 seats
// two players on 14 columns, three on 18, four on 22.
func SharedBoardWidth(players, extraCols int) int {
	return StandardWidth + max(players-1, 0)*ExtraColumnsPerPlayer(extraCols)
}

// SharedSpawnOffset returns the column a shared board's section'th seat
// spawns its pieces at, relative to the standard spawn column: seats are
// spaced one extraCols step apart, so the first spawns at the board's left
// edge and the last exactly StandardWidth columns short of its right edge —
// every spawn box lands wholly on the board (SharedBoardWidth).
func SharedSpawnOffset(section, extraCols int) int {
	return max(section, 0) * ExtraColumnsPerPlayer(extraCols)
}

// TeamBoardWidth returns the width of one team's shared board: the standard
// 10 columns plus extraCols per teammate beyond the first, like the
// cooperative board.
func TeamBoardWidth(teamSize, extraCols int) int {
	return SharedBoardWidth(teamSize, extraCols)
}

func GameStream(gameID string) string {
	return "JETRIS_GAME_" + gameID
}

func GameSubjectFilter(gameID string) string {
	return "jetris.game." + gameID + ".>"
}

// The replay archive is ONE shared file-backed stream (ReplayStream) holding
// the full copy of every top-ranked finished game's stream. Each copied
// message is republished under the game-scoped prefix
// "jetris.replay.<gameID>.<original tail>" — the game ID right after the
// prefix, so one game is exactly one subject subspace: a consumer filter
// ReplayFilter(gameID) replays it and a Purge with the same filter deletes a
// displaced game. Republishing means the stream stamps COPY-time timestamps,
// so each copied message carries its ORIGINAL timestamp in the ReplayTsHeader
// header instead — the replay viewer paces an original-speed playback from it.
// The copy ends with a marker message on ReplayMarkerSubject; the marker is
// what makes a replay complete and listable (ReplayMarkerFilter), so an
// in-flight copy is never surfaced.
const (
	ReplayStream = "JETRIS_REPLAY"
	// ReplaySubjectFilter matches everything in the replay stream (stream config).
	ReplaySubjectFilter = replaySubjectPrefix + ">"
	// ReplayMarkerFilter matches every game's copy-complete marker (listing).
	ReplayMarkerFilter = replaySubjectPrefix + "*." + replayMarkerToken
	// ReplayTsHeader carries a copied message's ORIGINAL stream timestamp
	// (integer nanoseconds since the Unix epoch).
	ReplayTsHeader = "Jetris-Ts"

	replaySubjectPrefix = "jetris.replay."
	replayMarkerToken   = "done"
)

// ReplayFilter matches one archived game's whole replay subspace — the copied
// messages plus its marker. Used as the replay consumer's filter and as the
// purge filter when the game is displaced.
func ReplayFilter(gameID string) string {
	return replaySubjectPrefix + gameID + ".>"
}

// ReplayMarkerSubject is the copy-complete marker, published last. "done" can
// never collide with a copied subject: game-stream tails all continue past
// their first token (meta and countdown are single tokens, but neither is
// "done").
func ReplayMarkerSubject(gameID string) string {
	return replaySubjectPrefix + gameID + "." + replayMarkerToken
}

// ReplayCopySubject maps one game-stream subject to its home in the replay
// stream: "jetris.game.<id>.<tail>" → "jetris.replay.<id>.<tail>". Returns ""
// for a subject not under the game's prefix (never the case for messages read
// off the game's own stream).
func ReplayCopySubject(gameID, gameSubject string) string {
	prefix := "jetris.game." + gameID + "."
	tail, ok := strings.CutPrefix(gameSubject, prefix)
	if !ok || tail == "" {
		return ""
	}
	return replaySubjectPrefix + gameID + "." + tail
}

// GameIDFromReplayMarker extracts the game ID from a marker subject listed by
// ReplayMarkerFilter ("" if the subject is not a marker).
func GameIDFromReplayMarker(subject string) string {
	tail, ok := strings.CutPrefix(subject, replaySubjectPrefix)
	if !ok {
		return ""
	}
	id, ok := strings.CutSuffix(tail, "."+replayMarkerToken)
	if !ok || strings.Contains(id, ".") {
		return ""
	}
	return id
}

// Cooperative and competitive modes use entirely separate playfield subject
// schemes — they are not parameterisations of a single layout and are free to
// diverge. A given game is one mode or the other, so an engine only ever uses
// one scheme.
//
// CoopCellSubject is the subject one cell (row, col) of the shared cooperative
// board is published to. The board is shared by the whole game, so the subject
// carries NO player token — every player publishes to and consumes from the
// same cell subjects, and per-cell ownership lives in the payload via
// Cell.PlayerIdx (coop never filters cells by player).
func CoopCellSubject(gameID string, row, col int) string {
	return "jetris.game." + gameID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// CoopCellSubjectFilter is the wildcard filter matching every cell of the
// shared cooperative board.
func CoopCellSubjectFilter(gameID string) string {
	return "jetris.game." + gameID + ".playfield.cell.>"
}

// CompetitiveCellSubject is the subject one cell (row, col) of one competitive
// player's private board is published to. Each player owns a separate board
// scoped by player ID.
func CompetitiveCellSubject(gameID, playerID string, row, col int) string {
	return "jetris.game." + gameID + ".player." + playerID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// CompetitiveCellSubjectFilter is the wildcard filter matching every cell of
// one competitive player's board.
func CompetitiveCellSubjectFilter(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + ".playfield.cell.>"
}

// TeamCellSubject is the subject one cell (row, col) of one team's shared
// board is published to in teams mode. Like the cooperative scheme the subject
// carries no player token — all teammates publish to and consume from the same
// cell subjects and per-cell ownership lives in the payload via Cell.PlayerIdx —
// but the board is scoped by team index so the two teams' boards are disjoint.
func TeamCellSubject(gameID string, team, row, col int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// TeamCellSubjectFilter is the wildcard filter matching every cell of one
// team's shared board.
func TeamCellSubjectFilter(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell.>"
}

// Per-board registers. Every competitive/team board carries two single-subject
// "registers" under its playfield prefix, so the board's consumers and snapshot
// fetches cover them with one widened filter (…playfield.>):
//
//   - The GARBAGE register holds the cumulative number of garbage rows OWED to
//     the board since game start. It is advanced by ATTACKERS (read-add-publish
//     with per-subject CAS and a bounded retry), so simultaneous attacks
//     serialize and converge to the exact sum. Only the latest total matters —
//     the register is a monotonic counter, so a newer value subsumes every
//     older one, and a late joiner or reconnecting client recovers the full
//     amount owed from the last-per-subject snapshot fetch.
//   - The TXN register is the exactly-once gate for every bulk board transform
//     (garbage application, line-clear collapse, teams elimination vacate): it
//     rides as the FIRST message of the transform's atomic batch with a
//     per-subject CAS expectation, so of several racing appliers exactly one
//     commits — a stale gate atomically rejects the loser's entire batch. Its
//     payload records the cumulative rows APPLIED; the board's deficit is
//     garbage.Total − txn.Applied.
//
// Cooperative boards have no registers: coop has no garbage, and its clears
// keep the merge-retry path.
const (
	garbageSubjectSuffix = ".playfield.garbage"
	txnSubjectSuffix     = ".playfield.txn"
)

// CompetitiveGarbageSubject is one competitive player's garbage (rows-owed)
// register subject.
func CompetitiveGarbageSubject(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + garbageSubjectSuffix
}

// CompetitiveTxnSubject is one competitive player's txn (transform gate)
// register subject.
func CompetitiveTxnSubject(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + txnSubjectSuffix
}

// CompetitivePlayfieldFilter matches one competitive player's whole playfield
// namespace: every cell plus the garbage/txn registers.
func CompetitivePlayfieldFilter(gameID, playerID string) string {
	return "jetris.game." + gameID + ".player." + playerID + ".playfield.>"
}

// TeamGarbageSubject is one team board's garbage (rows-owed) register subject.
func TeamGarbageSubject(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + garbageSubjectSuffix
}

// TeamTxnSubject is one team board's txn (transform gate) register subject.
func TeamTxnSubject(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + txnSubjectSuffix
}

// TeamPlayfieldFilter matches one team board's whole playfield namespace:
// every cell plus the garbage/txn registers.
func TeamPlayfieldFilter(gameID string, team int) string {
	return "jetris.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.>"
}

// IsGarbageSubject reports whether a delivered subject is a board's garbage
// register. Consumers branch on the register helpers BEFORE the cell parse.
func IsGarbageSubject(subject string) bool {
	return strings.HasSuffix(subject, garbageSubjectSuffix)
}

// IsTxnSubject reports whether a delivered subject is a board's txn register.
func IsTxnSubject(subject string) bool {
	return strings.HasSuffix(subject, txnSubjectSuffix)
}

func MetaSubject(gameID string) string {
	return "jetris.game." + gameID + ".meta"
}

func RosterSubject(gameID, playerID string) string {
	return "jetris.game." + gameID + ".roster." + playerID
}

// Game events are published to PER-KIND, PER-PLAYER subjects, so no event can
// ever overwrite an unrelated one, and the payloads stay correct under any
// retention or replay: line_clear carries the sender's CUMULATIVE totals (a
// newer total subsumes any older one, and a full-history replay — e.g. a
// spectator joining mid-game — folds to the same numbers) and each player
// publishes at most one game_over. Stream order across subjects is total, so
// every engine sees the same verdict order.
func EventKindSubject(gameID, kind, playerID string) string {
	return "jetris.game." + gameID + ".events." + kind + "." + playerID
}

// EventsSubjectFilter matches every event of a game, all kinds and senders.
func EventsSubjectFilter(gameID string) string {
	return "jetris.game." + gameID + ".events.>"
}

func CountdownSubject(gameID string) string {
	return "jetris.game." + gameID + ".countdown"
}

// FlashSubject and FlashSubjectFilter are CORE NATS subjects (deliberately
// OUTSIDE the "jetris.game.<id>.>" filter the game stream captures) used to
// broadcast a player's transient CAS-failure flash to spectators. A flash is
// ephemeral UI feedback — it must NOT be persisted in the stream or replayed
// on join — so it travels as fire-and-forget core pub/sub, not JetStream.
func FlashSubject(gameID, playerID string) string {
	return "jetris.flash." + gameID + "." + playerID
}

// FlashSubjectFilter matches every player's flash subject for a game.
func FlashSubjectFilter(gameID string) string {
	return "jetris.flash." + gameID + ".*"
}

// Lobby chat and per-game chat share the SAME stream (ChatStream) and are
// distinguished purely by the game-ID token of the subject
// ("jetris.chat.<gameID>"): the lobby chat uses the reserved game ID
// LobbyChatGameID ("lobby"), a game's messages use its own ID. Game chat
// cannot live on the game stream because game streams keep only the latest
// message per subject.

const chatSubjectPrefix = "jetris.chat."

// GameChatSubject is the subject one game's chat messages are published to.
func GameChatSubject(gameID string) string {
	return chatSubjectPrefix + gameID
}

// ChatSubjectFilter matches every chat subject, lobby and per-game alike
// (stream config).
const ChatSubjectFilter = chatSubjectPrefix + "*"

// GameIDFromChatSubject extracts the game ID from a chat-stream subject; it
// returns "" for the lobby chat subject.
func GameIDFromChatSubject(subject string) string {
	if len(subject) > len(chatSubjectPrefix) && subject[:len(chatSubjectPrefix)] == chatSubjectPrefix {
		if id := subject[len(chatSubjectPrefix):]; id != LobbyChatGameID {
			return id
		}
	}
	return ""
}

func LobbyPlayerKey(playerID string) string {
	return "players." + playerID
}

func LobbyGameKey(gameID string) string {
	return "games." + gameID
}

// LobbyInviteKey is the KV key holding one player's invitation to one game.
// A player may hold invitations to several games at once — one key per game.
// Every lobby's KV watcher sees the whole invites.* space, so the invitee's
// client surfaces an invitation the moment it is written AND the inviter can
// watch its state: the key is deleted when the invitee joins (accepted) or the
// inviter retracts it, and rewritten with Declined set when the invitee
// declines (kept so the inviter sees the refusal until they dismiss it).
// Player IDs cannot contain '.' (ValidatePlayerName) and game IDs are UUIDs,
// so the two tokens parse back out unambiguously.
func LobbyInviteKey(inviteeID, gameID string) string {
	return LobbyInvitePrefix(inviteeID) + gameID
}

// LobbyInvitePrefix is the KV key prefix under which all of one player's
// pending invitations live ("invites.<playerID>.").
func LobbyInvitePrefix(inviteeID string) string {
	return "invites." + inviteeID + "."
}

// InviteTTL is how long a pending invitation stays valid; older invites are
// ignored (the invitee may have been away from the lobby screen).
const InviteTTL = 2 * time.Minute

// Lobby events are transient core NATS notifications (deliberately NOT
// captured by any stream — real-time signals, not state) published so every
// lobby, human or agent, hears about lobby activity the instant it happens:
// a game created, a player joining or leaving a game's roster, an invitation
// sent, retracted, or declined. State still lives in the KV (listings,
// presence, invites); the events are the low-latency "look now" pings that
// keep player lists and invite pop-ups current between KV watcher deliveries.

// LobbyEventSubject is the subject one kind of lobby event is published to,
// e.g. "jetris.lobby.event.game.joined".
func LobbyEventSubject(kind string) string {
	return "jetris.lobby.event." + kind
}

// LobbyEventsFilter matches every lobby event subject (core NATS subscription).
const LobbyEventsFilter = "jetris.lobby.event.>"
