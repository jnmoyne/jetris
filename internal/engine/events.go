package engine

// UpdateKind identifies the type of engine update sent to the UI.
type UpdateKind int

const (
	UpdatePlayfield UpdateKind = iota
	UpdatePieceLocked
	UpdateLineClear
	UpdateGameOver
	UpdateOpponentField
	UpdateOpponentShrink
	UpdateScore
	UpdateLevel
	UpdateGameStatus
	UpdateCountdown
	UpdatePlayerEliminated // competitive: a player was eliminated
	UpdateCASFlash         // a CAS failure flash should be rendered
)

// EngineUpdate is the event sent from engine to UI.
type EngineUpdate struct {
	Kind               UpdateKind
	ChangedRows        []int
	Score              int
	Level              int
	GameStatus         string
	Countdown          int      // seconds remaining (0 = GO!)
	Won                bool     // competitive: true if this player won
	EliminatedPlayerID string   // competitive: which player was eliminated
	OpponentID         string   // which opponent's board changed (UpdateOpponentField)
	FlashCells         [][2]int // cells to flash (UpdateCASFlash)
	FlashPlayerIdx     int      // player index for flash color
}

// EventKind identifies the type of game event published to the events subject.
type EventKind string

const (
	EventLineClear     EventKind = "line_clear"
	EventShrink        EventKind = "shrink"
	EventGameOver      EventKind = "game_over"
	EventCoopLineClear EventKind = "coop_line_clear"
)

// GameEvent is the JSON payload published to the events subject.
//
// Note: CAS-failure feedback is intentionally NOT modelled as a published
// event. A CAS failure that drops a step (player move, gravity tick, or spawn)
// is local information for that player; it surfaces as an UpdateCASFlash on the
// local engine's Updates channel only and never round-trips through NATS.
type GameEvent struct {
	Kind         EventKind `json:"kind"`
	PlayerID     string    `json:"player_id"`
	LinesCleared int       `json:"lines_cleared,omitempty"`
	TargetPlayer string    `json:"target_player,omitempty"`
	RowsRemoved  int       `json:"rows_removed,omitempty"`
	ClearedRows  []int     `json:"cleared_rows,omitempty"`
	Score        int       `json:"score,omitempty"`
	PieceCount   uint64    `json:"piece_count,omitempty"`
	PlayerIdx    int       `json:"player_idx,omitempty"` // causer's index for EventShrink
}

// MoveType represents a player move.
type MoveType int

const (
	MoveLeft MoveType = iota
	MoveRight
	MoveDown
	RotateCW
	RotateCCW
	MoveHardDrop
)
