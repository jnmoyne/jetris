package game

import "encoding/json"

// Cell represents a single cell in a row.
type Cell struct {
	Occupied    bool      `json:"o,omitempty"`
	PieceType   PieceType `json:"t,omitempty"`
	Active      bool      `json:"a,omitempty"`
	Orientation int       `json:"r,omitempty"`
	AnchorRow   int       `json:"ar,omitempty"`
	AnchorCol   int       `json:"ac,omitempty"`
	PlayerIdx   int       `json:"pi,omitempty"` // which player's active piece (cooperative mode)
	Adversarial bool      `json:"g,omitempty"`  // adversarial garbage cell (competitive/teams shrink); a row of nothing but these is permanent, one whose holes a player filled clears like any line
}

// Marshal encodes the cell as JSON. An empty cell encodes as "{}" (every field
// is omitempty), which is the payload published to vacate a cell.
func (c Cell) Marshal() ([]byte, error) {
	return json.Marshal(c)
}

// UnmarshalCell decodes a cell from JSON.
func UnmarshalCell(data []byte) (Cell, error) {
	var c Cell
	err := json.Unmarshal(data, &c)
	return c, err
}

// CellPos identifies one cell of the playfield by position. Used as the key of
// per-cell projection diffs and publish batches.
type CellPos struct {
	Row int
	Col int
}

// Row represents a single row of cells. Rows are the in-memory representation
// only — the playfield is stored in NATS as one message per cell.
type Row struct {
	Cells []Cell `json:"cells"`
}

// NewRow creates an empty row with the given width.
func NewRow(width int) Row {
	return Row{Cells: make([]Cell, width)}
}

// Clone returns a deep copy of the row with an independent Cells slice. Cell is
// all scalar fields, so a slice copy is a full deep copy.
func (r Row) Clone() Row {
	cells := make([]Cell, len(r.Cells))
	copy(cells, r.Cells)
	return Row{Cells: cells}
}

// Equal reports whether two rows have identical cells. Cell is all scalar
// fields, so a direct comparison is a full value compare.
func (r Row) Equal(other Row) bool {
	if len(r.Cells) != len(other.Cells) {
		return false
	}
	for i := range r.Cells {
		if r.Cells[i] != other.Cells[i] {
			return false
		}
	}
	return true
}

// CloneRows returns deep copies of the given rows, safe to read concurrently
// with mutation of the originals.
func CloneRows(rows []Row) []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = r.Clone()
	}
	return out
}

// IsFull reports whether the row is complete: every cell locked (occupied,
// not active) and at least one of them a player's. A solid garbage row —
// nothing but adversarial cells, what a 0-hole raise lands — is permanent and
// never completes; a garbage row raised with holes clears like any other
// line once a player's locked cells have filled every hole.
func (r Row) IsFull() bool {
	player := false
	for _, c := range r.Cells {
		if !c.Occupied || c.Active {
			return false
		}
		if !c.Adversarial {
			player = true
		}
	}
	return player
}

// IsEmpty returns true if no cell in the row is occupied.
func (r Row) IsEmpty() bool {
	for _, c := range r.Cells {
		if c.Occupied {
			return false
		}
	}
	return true
}
