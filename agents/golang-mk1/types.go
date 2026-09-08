package main

import "encoding/json"

// Wire types and helpers (jetris-agent-guide.md §4). The lobby KV and meta are
// carefully preserved on read-modify-write: we decode into `obj` (a map of raw
// field bytes) so unknown fields — and, crucially, the 64-bit seed's exact
// digits — survive a CAS rewrite untouched.

type obj map[string]json.RawMessage

func toObj(b []byte) obj {
	o := obj{}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &o)
	}
	return o
}

func (o obj) has(k string) bool { _, ok := o[k]; return ok }
func (o obj) str(k string) string {
	var s string
	_ = json.Unmarshal(o[k], &s)
	return s
}
func (o obj) u64(k string) uint64 {
	var v uint64
	_ = json.Unmarshal(o[k], &v)
	return v
}
func (o obj) int(k string) int {
	var v int
	_ = json.Unmarshal(o[k], &v)
	return v
}
func (o obj) boolv(k string) bool {
	var v bool
	_ = json.Unmarshal(o[k], &v)
	return v
}
func (o obj) set(k string, v any) {
	b, _ := json.Marshal(v)
	o[k] = b
}
func (o obj) bytes() []byte {
	b, _ := json.Marshal(o)
	return b
}

// playerSummary is one roster seat in a KV game listing / roster announcement.
type playerSummary struct {
	PlayerID string `json:"player_id"`
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Seat     int    `json:"seat"` // the stable seat (the index the player's cells carry); listings written before the field carry zeros — normalizeSeats reads those by position
	Team     int    `json:"team"`
	TeamSlot int    `json:"team_slot"`
	Agent    bool   `json:"agent"`
}

// normalizeSeats fills every seat in: the recorded seats when they are
// unique (every listing written since the field), the roster positions
// otherwise — the assignment every game used before the field.
func normalizeSeats(ps []playerSummary) []playerSummary {
	out := append([]playerSummary(nil), ps...)
	seen := map[int]bool{}
	for _, p := range out {
		if seen[p.Seat] {
			for i := range out {
				out[i].Seat = i
			}
			return out
		}
		seen[p.Seat] = true
	}
	return out
}

// players decodes a listing's players array.
func (o obj) players() []playerSummary {
	var ps []playerSummary
	if raw, ok := o["players"]; ok {
		_ = json.Unmarshal(raw, &ps)
	}
	return normalizeSeats(ps)
}

// wireCell is one playfield cell payload. Every field is omitempty, so an empty
// cell marshals to "{}" (vacate) and a zero-valued field is dropped — matching
// the game's own cell encoding and example-python's "if v" filter exactly.
type wireCell struct {
	T  int  `json:"t,omitempty"`  // piece type (0 = I, dropped)
	O  bool `json:"o,omitempty"`  // occupied/locked
	A  bool `json:"a,omitempty"`  // active (falling)
	R  int  `json:"r,omitempty"`  // orientation
	Ar int  `json:"ar,omitempty"` // active anchor row
	Ac int  `json:"ac,omitempty"` // active anchor col
	Pi int  `json:"pi,omitempty"` // owning player index
	G  bool `json:"g,omitempty"`  // adversarial garbage (a solid garbage row is permanent; one raised with holes clears once they are filled)
}

func (c wireCell) bytes() []byte {
	b, _ := json.Marshal(c)
	return b
}

// pubAck is the JetStream publish acknowledgement (or error) returned on the
// reply of a cell/meta publish request.
type pubAck struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Duplicate bool   `json:"duplicate"`
	Error     *struct {
		Code        int    `json:"code"`
		ErrCode     int    `json:"err_code"`
		Description string `json:"description"`
	} `json:"error"`
}

// isCASConflict reports whether an ack error is a wrong-expected-sequence
// rejection (the two err_codes the server uses for the plain and batch cases).
func (a pubAck) isCASConflict() bool {
	return a.Error != nil && (a.Error.ErrCode == 10071 || a.Error.ErrCode == 10164)
}

// event is a per-player game event: game_over (elimination/outcome data) and
// line_clear (the sender's CUMULATIVE totals, folded as deltas by receivers).
type event struct {
	Kind       string `json:"kind"`
	PlayerID   string `json:"player_id"`
	PlayerIdx  int    `json:"player_idx"`
	Score      int    `json:"score"`
	Level      int    `json:"level"`
	PieceCount int    `json:"piece_count"`
	Team       int    `json:"team"`
	TotalScore int    `json:"total_score"`
	TotalLines int    `json:"total_lines"`
}
