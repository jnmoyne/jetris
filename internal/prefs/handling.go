package prefs

import "encoding/json"

// The handling preferences: the keyboard auto-shift's two knobs
// (nativeui/autoshift.go), tuned in the game menu and kept across launches.

// Handling knob bounds and defaults — the single source both this store and
// the menu's sliders clamp to.
const (
	MaxHandlingMs = 150
	DefaultDASMs  = 75
	DefaultARRMs  = 20
)

// Handling is the keyboard auto-shift tuning: DAS is how long a held ← →
// waits before repeating, ARR how often it repeats after that (0: the piece
// slides to the first thing it collides with at once). Milliseconds, clamped
// to 0..MaxHandlingMs.
type Handling struct {
	DASMs int `json:"das_ms"`
	ARRMs int `json:"arr_ms"`
}

// DefaultHandling is a fresh install's tuning.
func DefaultHandling() Handling {
	return Handling{DASMs: DefaultDASMs, ARRMs: DefaultARRMs}
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
	return Handling{DASMs: clamp(h.DASMs), ARRMs: clamp(h.ARRMs)}
}
