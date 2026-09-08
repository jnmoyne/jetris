package prefs

import "encoding/json"

// The key bindings: which keys make which move (nativeui/input.go), changed
// from the lobby's KEYS legend (nativeui/keymap.go) and kept across launches.

// KeyAction is one move the keyboard can make. The touch gestures are not
// here: they are fixed.
type KeyAction string

const (
	KeyMoveLeft  KeyAction = "move_left"
	KeyMoveRight KeyAction = "move_right"
	KeySoftDrop  KeyAction = "soft_drop"
	KeyRotateCW  KeyAction = "rotate_cw"
	KeyRotateCCW KeyAction = "rotate_ccw"
	KeyHardDrop  KeyAction = "hard_drop"
	KeyHold      KeyAction = "hold"
)

// KeyActions is every action, in the order the legend lists them — and the
// order a conflict is settled in when a saved file names one key twice: the
// earlier action keeps it.
var KeyActions = []KeyAction{KeyMoveLeft, KeyMoveRight, KeySoftDrop, KeyRotateCW, KeyRotateCCW, KeyHardDrop, KeyHold}

// Keymap is the bindings: for each action the keys that make it, by Gio's
// key names ("A", "←", "Space", "Shift", …), in the order the legend shows
// them. Every action has at least one key and no key makes two moves.
type Keymap map[KeyAction][]string

// DefaultKeymap is the Guideline's scheme with the WASD twins: ← → (A D)
// move, ↓ (S) soft-drops, ↑ (W X) rotates clockwise, Z or Ctrl counter-
// clockwise, Space hard-drops, C or Shift holds.
func DefaultKeymap() Keymap {
	return Keymap{
		KeyMoveLeft:  {"←", "A"},
		KeyMoveRight: {"→", "D"},
		KeySoftDrop:  {"↓", "S"},
		KeyRotateCW:  {"↑", "W", "X"},
		KeyRotateCCW: {"Z", "Ctrl"},
		KeyHardDrop:  {"Space"},
		KeyHold:      {"C", "Shift"},
	}
}

// reservedKeys are the keys no move may take: Tab moves the keys between the
// board and the chat and Escape brings them back, and the rest are the
// platform's — ⌘W is not a game key.
var reservedKeys = map[string]bool{
	"Tab": true, "⎋": true, "⌘": true, "Alt": true, "Super": true, "Back": true,
}

// KeyBindable reports whether name is a key a move may be bound to.
func KeyBindable(name string) bool {
	return name != "" && !reservedKeys[name]
}

// Clone is a copy that shares nothing with k.
func (k Keymap) Clone() Keymap {
	c := make(Keymap, len(k))
	for act, keys := range k {
		c[act] = append([]string(nil), keys...)
	}
	return c
}

// ActionOf is the action name makes, "" when it makes none.
func (k Keymap) ActionOf(name string) KeyAction {
	for _, act := range KeyActions {
		for _, n := range k[act] {
			if n == name {
				return act
			}
		}
	}
	return ""
}

// LoadKeymap reads the saved bindings — from ~/.config/jetris on the
// desktop, from the browser's localStorage in the wasm build. A missing
// store is not an error: a fresh install starts at DefaultKeymap. Whatever
// is read is normalized (Normalize).
func LoadKeymap() (Keymap, error) {
	data, found, err := loadKeymapData()
	if err != nil || !found {
		return DefaultKeymap(), err
	}
	var k Keymap
	if err := json.Unmarshal(data, &k); err != nil {
		return DefaultKeymap(), err
	}
	return k.Normalize(), nil
}

// SaveKeymap writes the bindings, normalized.
func SaveKeymap(k Keymap) error {
	data, err := json.MarshalIndent(k.Normalize(), "", "  ")
	if err != nil {
		return err
	}
	return saveKeymapData(append(data, '\n'))
}

// Normalize is k made whole, for a file written by hand or by an earlier
// release: every action has the slots its default has — a slot the file
// does not fill, or fills with a reserved key or one an earlier action
// already holds, takes the default key when that one is free and is dropped
// otherwise — and an action the file does not name at all keeps its
// defaults. A file that leaves some action with no key at all (its keys
// handed out to other actions, defaults included) is no scheme: the defaults
// come back whole.
func (k Keymap) Normalize() Keymap {
	def := DefaultKeymap()
	out := make(Keymap, len(def))
	used := map[string]bool{}
	for _, act := range KeyActions {
		saved := k[act]
		var keys []string
		for i, dflt := range def[act] {
			name := ""
			if i < len(saved) {
				name = saved[i]
			}
			if !KeyBindable(name) || used[name] {
				name = dflt
			}
			if used[name] {
				continue
			}
			used[name] = true
			keys = append(keys, name)
		}
		if len(keys) == 0 {
			return def
		}
		out[act] = keys
	}
	return out
}
