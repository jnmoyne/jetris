package prefs

import "encoding/json"

// The handling preferences: the keyboard auto-shift's three knobs
// (nativeui/autoshift.go), tuned in the game menu and kept across launches.

// Handling knob bounds and defaults — the single source both this store and
// the menu's sliders clamp to.
const (
	MaxHandlingMs = 150
	DefaultDASMs  = 75
	DefaultARRMs  = 20
	// The soft drop factor's range. Unlike DAS and ARR it is not a duration:
	// it is a multiple of the level's gravity, the Guideline's way of putting
	// soft drop ("20 times the normal fall speed" — hence the default), which
	// keeps ↓ faster than the piece is already falling at every level.
	// MaxSDF means instant: the piece goes to the floor at once and rests
	// there unlocked, so MaxSDF-1 is the fastest finite factor.
	MinSDF     = 1
	MaxSDF     = 40
	DefaultSDF = 20
)

// Handling is the keyboard tuning: DAS is how long a held ← → waits before
// repeating and ARR how often it repeats after that (0: the piece slides to
// the first thing it collides with at once), both in milliseconds clamped to
// 0..MaxHandlingMs. SDF is the soft drop's own rate — ↓ takes neither of the
// other two — as a multiple of gravity, clamped to MinSDF..MaxSDF.
type Handling struct {
	DASMs int `json:"das_ms"`
	ARRMs int `json:"arr_ms"`
	SDF   int `json:"sdf"`
}

// DefaultHandling is a fresh install's tuning.
func DefaultHandling() Handling {
	return Handling{DASMs: DefaultDASMs, ARRMs: DefaultARRMs, SDF: DefaultSDF}
}

// LoadHandling reads the saved tuning — from ~/.config/jetris on the desktop,
// from the browser's localStorage in the wasm build. A missing store is not
// an error: a fresh install starts at DefaultHandling. Whatever is read is
// clamped to the knobs' range.
func LoadHandling() (Handling, error) {
	data, found, err := loadHandlingData()
	if err != nil || !found {
		return DefaultHandling(), err
	}
	var h Handling
	if err := json.Unmarshal(data, &h); err != nil {
		return DefaultHandling(), err
	}
	return clampHandling(h), nil
}

// SaveHandling writes the tuning, clamped.
func SaveHandling(h Handling) error {
	data, err := json.MarshalIndent(clampHandling(h), "", "  ")
	if err != nil {
		return err
	}
	return saveHandlingData(append(data, '\n'))
}

func clampHandling(h Handling) Handling {
	clamp := func(v int) int { return min(max(v, 0), MaxHandlingMs) }
	sdf := min(max(h.SDF, MinSDF), MaxSDF)
	if h.SDF <= 0 {
		// Absent, not zero: a file written before the knob existed (and a
		// hand-edited 0, which has no meaning for a multiplier) takes the
		// default rather than the slowest setting.
		sdf = DefaultSDF
	}
	return Handling{DASMs: clamp(h.DASMs), ARRMs: clamp(h.ARRMs), SDF: sdf}
}
