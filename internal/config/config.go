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

// TeamCount is the number of teams in a teams-mode game. Team indices are
// 0 ("A") and 1 ("B").
const TeamCount = 2

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
	GameID      string     `json:"game_id"`
	Mode        GameMode   `json:"mode"`
	PlayerCount int        `json:"player_count"`
	TeamSize    int        `json:"team_size,omitempty"` // teams mode: players per team (PlayerCount = TeamCount*TeamSize)
	NextCount   int        `json:"next_count"`          // how many upcoming pieces are shown (0..MaxNextCount); bounds lookahead for humans and agents alike
	NoGhost     bool       `json:"no_ghost,omitempty"`  // hard-drop ghost preview disabled for this game; inverted so the zero value — and metas written before the field — keep the ghost SHOWN (the default). Meta, not listing: like NextCount it is one rule for every player
	Seed        uint64     `json:"seed"`
	Status      GameStatus `json:"status"`
	CreatorID   string     `json:"creator_id"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   time.Time  `json:"started_at,omitempty"`
	FinishedAt  time.Time  `json:"finished_at,omitempty"`
	Abandoned   bool       `json:"abandoned,omitempty"`
	PieceIdx    uint64     `json:"piece_idx"`
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
	GameID      string         `json:"game_id"`
	Mode        GameMode       `json:"mode"`
	PlayerCount int            `json:"player_count"`
	Players     []PlayerResult `json:"players"`
	StartedAt   time.Time      `json:"started_at"`
	FinishedAt  time.Time      `json:"finished_at"`
	TotalScore  int            `json:"total_score,omitempty"` // cooperative
	FinalLevel  int            `json:"final_level,omitempty"` // cooperative: shared level at game end
	TeamSize    int            `json:"team_size,omitempty"`   // teams mode
	WinningTeam int            `json:"winning_team"`          // teams mode: 0 or 1; -1 = draw or not a team game
	TeamScores  []int          `json:"team_scores,omitempty"` // teams mode: final score per team (indexed by team)
	TeamLevels  []int          `json:"team_levels,omitempty"` // teams mode: final level per team (indexed by team)
	Boards      []BoardPicture `json:"boards,omitempty"`      // end-of-game playfield snapshot(s) for the lobby's history view
	Chat        []ChatLine     `json:"chat,omitempty"`        // the game's chat history (last ArchiveChatCap lines), captured before the chat purge
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
	Label  string      `json:"label,omitempty"` // player ID, "Team A"/"Team B", or "" (cooperative)
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
	TotalRows       = 48 // max rows (supports competitive with many players: 24 + playerCount visible + 4 headroom)
	HeadroomRows    = 4
	VisibleRows     = 24 // base visible rows (cooperative and single mode)
	VisibleRowStart = 4  // base visible row start (for cooperative; competitive adjusts per game)
	StandardWidth   = 10

	// MaxNextCount caps GameMeta.NextCount, the per-game number of upcoming
	// pieces shown to players (0 = none). The same bound applies to agents:
	// an agent may look ahead at most NextCount pieces in the sequence.
	MaxNextCount = 4

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

// CompetitiveVisibleRows returns the visible rows for a competitive game.
// Each player adds one extra row to the playfield height.
func CompetitiveVisibleRows(playerCount int) int {
	return VisibleRows + playerCount
}

// CompetitiveTotalRows returns the total rows (headroom + visible) for a competitive game.
func CompetitiveTotalRows(playerCount int) int {
	return HeadroomRows + CompetitiveVisibleRows(playerCount)
}

// CompetitiveVisibleRowStart returns the first visible row index for a competitive game.
// Always equals HeadroomRows (headroom is constant regardless of player count).
func CompetitiveVisibleRowStart(playerCount int) int {
	return HeadroomRows
}

// TeamBoardWidth returns the width of one team's shared board: one standard
// 10-column section per teammate, like the cooperative board.
func TeamBoardWidth(teamSize int) int {
	return teamSize * StandardWidth
}

// TeamVisibleRows returns the visible rows for a team board. Like competitive,
// the board grows one row per garbage-producing player on the opposing team
// (which has teamSize players), leaving room for adversarial rows.
func TeamVisibleRows(teamSize int) int {
	return VisibleRows + teamSize
}

// TeamTotalRows returns the total rows (headroom + visible) for a team board.
func TeamTotalRows(teamSize int) int {
	return HeadroomRows + TeamVisibleRows(teamSize)
}

// TeamVisibleRowStart returns the first visible row index for a team board.
func TeamVisibleRowStart(teamSize int) int {
	return HeadroomRows
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
