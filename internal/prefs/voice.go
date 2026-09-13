package prefs

import "encoding/json"

// The voice preferences: the microphone gate — how much louder than the room
// the player must be for their microphone to publish — and whether other
// players' voices are played (nativeui/voice.go). The mute itself is NOT
// here: it lasts one visit to a server and no longer — every fresh
// connection to a lobby starts muted, by design, whatever the last one
// ended on.

// Voice knob bounds and defaults — the single source both this store and
// the menu's slider clamp to. The gate is in dB above the room's measured
// noise floor (or above -70 dBFS in a room quieter than that, so every
// setting means something); 0 is an open microphone that publishes
// whenever it is unmuted.
const (
	MinGateDb = 0
	// 60 dB reaches -10 dBFS from the quiet reference: a shout over a bar's
	// noise, for the environments a floor cannot follow.
	MaxGateDb     = 60
	DefaultGateDb = 12
)

// Voice is the standing answer for the two voice settings.
type Voice struct {
	GateDb int  `json:"gate_db"`
	Listen bool `json:"listen"`
}

// DefaultVoice is a fresh install's voice: the default gate, and everyone
// else's voice played.
func DefaultVoice() Voice {
	return Voice{GateDb: DefaultGateDb, Listen: true}
}

// LoadVoice reads the saved settings — from ~/.config/jetris on the desktop,
// from the browser's localStorage in the wasm build. A missing store is not
// an error: a fresh install starts at DefaultVoice. Whatever is read is
// clamped to the knob's range.
func LoadVoice() (Voice, error) {
	data, found, err := loadVoiceData()
	if err != nil || !found {
		return DefaultVoice(), err
	}
	// Unmarshalled ONTO the defaults, so a key the file does not carry keeps
	// its default instead of reading as a zero — a gate of 0 and a listen of
	// false are both values of their own (handling.go has the same rule).
	v := DefaultVoice()
	if err := json.Unmarshal(data, &v); err != nil {
		return DefaultVoice(), err
	}
	return clampVoice(v), nil
}

// SaveVoice writes the settings, clamped.
func SaveVoice(v Voice) error {
	data, err := json.MarshalIndent(clampVoice(v), "", "  ")
	if err != nil {
		return err
	}
	return saveVoiceData(append(data, '\n'))
}

func clampVoice(v Voice) Voice {
	v.GateDb = min(max(v.GateDb, MinGateDb), MaxGateDb)
	return v
}
