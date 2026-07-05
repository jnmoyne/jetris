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
	Team       int    `json:"team,omitempty"` // teams mode: 0 = A, 1 = B
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
// CoopCellSubject is the subject one cell (row, col) of the shared cooperative
// board is published to. The board is shared by the whole game, so the subject
// carries NO player token — every player publishes to and consumes from the
// same cell subjects, and per-cell ownership lives in the payload via
// Cell.PlayerIdx (coop never filters cells by player).
func CoopCellSubject(gameID string, row, col int) string {
	return "jetricks.game." + gameID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// CoopCellSubjectFilter is the wildcard filter matching every cell of the
// shared cooperative board.
func CoopCellSubjectFilter(gameID string) string {
	return "jetricks.game." + gameID + ".playfield.cell.>"
}

// CompetitiveCellSubject is the subject one cell (row, col) of one competitive
// player's private board is published to. Each player owns a separate board
// scoped by player ID.
func CompetitiveCellSubject(gameID, playerID string, row, col int) string {
	return "jetricks.game." + gameID + ".player." + playerID + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// CompetitiveCellSubjectFilter is the wildcard filter matching every cell of
// one competitive player's board.
func CompetitiveCellSubjectFilter(gameID, playerID string) string {
	return "jetricks.game." + gameID + ".player." + playerID + ".playfield.cell.>"
}

// TeamCellSubject is the subject one cell (row, col) of one team's shared
// board is published to in teams mode. Like the cooperative scheme the subject
// carries no player token — all teammates publish to and consume from the same
// cell subjects and per-cell ownership lives in the payload via Cell.PlayerIdx —
// but the board is scoped by team index so the two teams' boards are disjoint.
func TeamCellSubject(gameID string, team, row, col int) string {
	return "jetricks.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell." + strconv.Itoa(row) + "." + strconv.Itoa(col)
}

// TeamCellSubjectFilter is the wildcard filter matching every cell of one
// team's shared board.
func TeamCellSubjectFilter(gameID string, team int) string {
	return "jetricks.game." + gameID + ".team." + strconv.Itoa(team) + ".playfield.cell.>"
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

// Lobby chat and per-game chat share the SAME stream (LobbyChatStream) and are
// distinguished purely by subject: lobby messages on LobbyChatSubject, a
// game's messages on GameChatSubject(gameID). Game chat cannot live on the
// game stream because game streams keep only the latest message per subject.

// GameChatSubject is the subject one game's chat messages are published to.
func GameChatSubject(gameID string) string {
	return LobbyChatSubject + ".game." + gameID
}

// GameChatSubjectFilter matches every game's chat subject (stream config).
const GameChatSubjectFilter = LobbyChatSubject + ".game.*"

// GameIDFromChatSubject extracts the game ID from a chat-stream subject; it
// returns "" for the lobby chat subject.
func GameIDFromChatSubject(subject string) string {
	const prefix = LobbyChatSubject + ".game."
	if len(subject) > len(prefix) && subject[:len(prefix)] == prefix {
		return subject[len(prefix):]
	}
	return ""
}

func LobbyPlayerKey(playerID string) string {
	return "players." + playerID
}

func LobbyGameKey(gameID string) string {
	return "games." + gameID
}
