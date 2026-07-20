package nativeui

import "testing"

func TestPickerCapacityError(t *testing.T) {
	mk := func() map[string]*inviteChoice {
		return map[string]*inviteChoice{"a": {playerID: "a"}, "b": {playerID: "b"}, "c": {playerID: "c"}}
	}

	// Competitive: over the seat count is an error; at/under is fine.
	p := mk()
	p["a"].sel.Value, p["b"].sel.Value, p["c"].sel.Value = true, true, true
	if pickerCapacityError(p, 3, 0, false) != "" {
		t.Error("3 selected into 3 seats should be fine")
	}
	if pickerCapacityError(p, 2, 0, false) == "" {
		t.Error("3 selected into 2 seats should error")
	}

	// Teams: per-team cap. Three on team A of a 2v2 → error; balanced → fine.
	p = mk()
	p["a"].team.Value, p["b"].team.Value, p["c"].team.Value = "0", "0", "0"
	if pickerCapacityError(p, 4, 2, true) == "" {
		t.Error("3 on team A (size 2) should error")
	}
	p["c"].team.Value = "1"
	if pickerCapacityError(p, 4, 2, true) != "" {
		t.Error("2A/1B within a 2v2 should be fine")
	}
}
