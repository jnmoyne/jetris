package config

import (
	"fmt"
	"strconv"
	"time"
)

type Config struct {
	NATSContext  string
	NATSURL      string
	NATSUser     string
	NATSPassword string
	Port         int
	Webview      bool
	Web          bool // use the web browser UI instead of the native window
}

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
)

func (m GameMode) String() string {
	switch m {
	case ModeCooperative:
		return "cooperative"
	case ModeCompetitive:
		return "competitive"
	default:
		return "unknown"
	}
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
	GameID      string     `json:"game_id"`
	Mode        GameMode   `json:"mode"`
	PlayerCount int        `json:"player_count"`
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
	PieceCount uint64 `json:"piece_count"`
	Winner     bool   `json:"winner,omitempty"`
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
}

const (
	TotalRows       = 48 // max rows (supports competitive with many players: 24 + playerCount visible + 4 headroom)
	HeadroomRows    = 4
	VisibleRows     = 24 // base visible rows (cooperative and single mode)
	VisibleRowStart = 4  // base visible row start (for cooperative; competitive adjusts per game)
	StandardWidth   = 10

	LobbyKVBucket     = "JETRICKS_LOBBY"
	LobbyChatStream   = "JETRICKS_LOBBY_CHAT"
	LobbyChatSubject  = "jetricks.lobby.chat"
	ArchiveStream     = "JETRICKS_ARCHIVE"
	ArchiveSubject    = "jetricks.archive"
	LobbyChatMaxAge   = 7 * 24 * time.Hour
	PresenceHeartbeat = 5 * time.Second
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

func GameStream(gameID string) string {
	return "JETRICKS_GAME_" + gameID
}

func GameSubjectFilter(gameID string) string {
	return "jetricks.game." + gameID + ".>"
}

// Cooperative and competitive modes use entirely separate playfield subject
// schemes — they are not parameterisations of a single layout and are free to
// diverge. A given game is one mode or the other, so an engine only ever uses
// one scheme.
//
// CoopRowSubject is the subject a row of the shared cooperative board is
// published to. The board is shared by the whole game, so the subject carries
// NO player token — every player publishes to and consumes from the same row
// subjects, and per-cell ownership lives in the payload via Cell.PlayerIdx
// (coop never filters rows by player).
func CoopRowSubject(gameID string, row int) string {
	return "jetricks.game." + gameID + ".playfield.row." + strconv.Itoa(row)
}

// CoopRowSubjectFilter is the wildcard filter matching every row of the shared
// cooperative board.
func CoopRowSubjectFilter(gameID string) string {
	return "jetricks.game." + gameID + ".playfield.row.>"
}

// CompetitiveRowSubject is the subject a row of one competitive player's private
// board is published to. Each player owns a separate board scoped by player ID.
func CompetitiveRowSubject(gameID, playerID string, row int) string {
	return "jetricks.game." + gameID + ".player." + playerID + ".playfield.row." + strconv.Itoa(row)
}

// CompetitiveRowSubjectFilter is the wildcard filter matching every row of one
// competitive player's board.
func CompetitiveRowSubjectFilter(gameID, playerID string) string {
	return "jetricks.game." + gameID + ".player." + playerID + ".playfield.row.>"
}

func MetaSubject(gameID string) string {
	return "jetricks.game." + gameID + ".meta"
}

func RosterSubject(gameID, playerID string) string {
	return "jetricks.game." + gameID + ".roster." + playerID
}

func EventsSubject(gameID string) string {
	return "jetricks.game." + gameID + ".events"
}

func CountdownSubject(gameID string) string {
	return "jetricks.game." + gameID + ".countdown"
}

func ChatSubject(gameID string) string {
	return "jetricks.game." + gameID + ".chat"
}

func LobbyPlayerKey(playerID string) string {
	return "players." + playerID
}

func LobbyGameKey(gameID string) string {
	return "games." + gameID
}
