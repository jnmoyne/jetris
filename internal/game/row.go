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
	Adversarial bool      `json:"g,omitempty"`  // permanent adversarial cell (competitive shrink); row can never be completed
}

// Row represents a single row of cells.
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

// CloneRows returns deep copies of the given rows, safe to read concurrently
// with mutation of the originals.
func CloneRows(rows []Row) []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = r.Clone()
	}
	return out
}

// Marshal encodes the row as JSON.
func (r Row) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

// UnmarshalRow decodes a row from JSON.
func UnmarshalRow(data []byte) (Row, error) {
	var r Row
	err := json.Unmarshal(data, &r)
	return r, err
}

// IsFull returns true if every cell in the row is occupied (locked, not active)
// and the row contains no adversarial cells (adversarial rows can never be completed).
func (r Row) IsFull() bool {
	for _, c := range r.Cells {
		if c.Adversarial {
			return false // adversarial rows are permanent, never completable
		}
		if !c.Occupied || c.Active {
			return false
		}
	}
	return len(r.Cells) > 0
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
