package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type Config struct {
	NATSContext  string
	NATSURL      string
	NATSUser     string
	NATSPassword string
	RunEmbedded  bool // run an in-process JetStream-enabled nats-server and connect to it
	EmbeddedPort int  // port for the embedded server (0 = DefaultEmbeddedPort)
}

// Embedded-server settings for the login screen's "LAN mode (embedded NATS
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

// HasAgents reports whether any seat in the archived game was played by an
// agent (the lobby's history filter; records from before the agent flag simply
// read as all-human).
func (r ArchiveRecord) HasAgents() bool {
	for _, p := range r.Players {
		if p.Agent {
			return true
		}
	}
	return false
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

func MetaSubject(gameID string) string {
	return "jetris.game." + gameID + ".meta"
}

func RosterSubject(gameID, playerID string) string {
	return "jetris.game." + gameID + ".roster." + playerID
}

func EventsSubject(gameID string) string {
	return "jetris.game." + gameID + ".events"
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
