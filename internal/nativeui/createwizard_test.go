package nativeui

import (
	"testing"

	"jetris/internal/config"
)

// TestWizardRulesPreset pins the wizard's rules radio: "guideline" yields the
// Guideline preset whatever the custom editors hold, "custom" reads them —
// the hold checkbox included — and a cooperative game drops the garbage
// rules either way.
func TestWizardRulesPreset(t *testing.T) {
	a := newTestApp()
	a.nextCountEd.SetText("2")
	a.holesEd.SetText("3")
	a.holdCb.Value = true
	a.guidelineCb.Value = true

	a.rulesEnum.Value = "guideline"
	if got, want := a.customRules().Normalized(config.ModeCompetitive), (config.GameRules{NextCount: 2, Ghost: true, Hold: true, GarbageHoles: 3, GuidelineGarbage: true}); got != want {
		t.Fatalf("custom read-out = %+v, want %+v", got, want)
	}
	if !config.GuidelineRules().IsGuideline(config.ModeCompetitive) || !config.GuidelineRules().IsGuideline(config.ModeCooperative) {
		t.Fatal("the Guideline preset should match itself in every mode")
	}
	if config.GuidelineRules().Normalized(config.ModeCooperative).GarbageHoles != 0 {
		t.Fatal("a cooperative game should store no garbage rules")
	}
	custom := a.customRules()
	if custom.IsGuideline(config.ModeCompetitive) {
		t.Fatal("next 2 / holes 3 is not the Guideline preset")
	}
	// The custom read-out with the preset's own values IS the preset — the
	// lobby row then tags it "guideline" like a preset-created game.
	a.nextCountEd.SetText("6")
	a.holesEd.SetText("1")
	if !a.customRules().IsGuideline(config.ModeCompetitive) {
		t.Fatalf("custom rules %+v should match the Guideline preset", a.customRules())
	}
	a.holdCb.Value = false
	if a.customRules().IsGuideline(config.ModeCompetitive) {
		t.Fatal("without the hold the rules are not the Guideline preset")
	}
}

// TestGuidelineSummaryPerMode: the read-only preset list shows the garbage
// rules only for the modes that raise garbage.
func TestGuidelineSummaryPerMode(t *testing.T) {
	coop, comp := guidelineSummary(config.ModeCooperative), guidelineSummary(config.ModeCompetitive)
	if len(comp) != len(coop)+2 {
		t.Fatalf("competitive summary has %d rows, cooperative %d; want two garbage rows more", len(comp), len(coop))
	}
	for _, row := range coop {
		if row[0] == "Garbage" || row[0] == "Attacks" {
			t.Fatalf("cooperative summary lists a garbage rule: %v", row)
		}
	}
}

// TestWizardSplitPieces pins step 1's piece-split box: it is a teams setting
// with teammates in it. Checked in a 2v2 it splits; the same box checked for
// a team of one, or for any other game type, is ignored — so no co-op or
// solo-team game is ever created advertising a split that cannot happen.
func TestWizardSplitPieces(t *testing.T) {
	a := newTestApp()
	if a.wizardSplit(config.ModeTeams, 2) {
		t.Error("an unchecked box split the pieces")
	}
	a.splitPiecesCb.Value = true
	if !a.wizardSplit(config.ModeTeams, 2) {
		t.Error("a checked box in a 2v2 did not split the pieces")
	}
	if !a.wizardSplit(config.ModeTeams, 4) {
		t.Error("a checked box in a 4v4 did not split the pieces")
	}
	if a.wizardSplit(config.ModeTeams, 1) {
		t.Error("a team of one split its pieces")
	}
	for _, mode := range []config.GameMode{config.ModeCooperative, config.ModeCompetitive} {
		if a.wizardSplit(mode, 4) {
			t.Errorf("a %s game split its pieces", mode)
		}
	}
}

// TestWizardTeamCount pins step 1's team-count knob: it defaults to the usual
// two, clamps to the legal range, reads back only for a teams game (no other
// mode has teams at all), and its slider position round-trips through every
// detent so a dragged slider always rests on a whole number of teams.
func TestWizardTeamCount(t *testing.T) {
	a := newTestApp()
	if got := a.wizardTeamCount(config.ModeTeams); got != config.DefaultTeamCount {
		t.Errorf("a fresh wizard offers %d teams, want %d", got, config.DefaultTeamCount)
	}
	for _, mode := range []config.GameMode{config.ModeCooperative, config.ModeCompetitive} {
		if got := a.wizardTeamCount(mode); got != 0 {
			t.Errorf("a %s game reads %d teams, want 0", mode, got)
		}
	}
	for _, tc := range []struct{ set, want int }{
		{1, config.MinTeamCount}, {2, 2}, {4, 4},
		{config.MaxTeamCount, config.MaxTeamCount}, {config.MaxTeamCount + 3, config.MaxTeamCount},
	} {
		a.setTeamCount(tc.set)
		if got := a.wizardTeamCount(config.ModeTeams); got != tc.want {
			t.Errorf("setTeamCount(%d) → %d teams, want %d", tc.set, got, tc.want)
		}
		// The knob and its slider agree: the stored position reads back as
		// the same count.
		if got := teamCountRange.value(a.teamCountFloat.Value); got != tc.want {
			t.Errorf("setTeamCount(%d): slider reads %d teams, want %d", tc.set, got, tc.want)
		}
	}
	for n := config.MinTeamCount; n <= config.MaxTeamCount; n++ {
		if got := teamCountRange.value(teamCountRange.pos(n)); got != n {
			t.Errorf("team-count detent %d round-tripped to %d", n, got)
		}
	}
}

// The create wizard's step 1 draws for a teams game at every legal team
// count: the mode radios, the Teams slider (whose hint names every team), the
// per-team seat editor, the board-width slider and the piece-split box, all
// in one modal that must lay out in the narrowest window Jetris runs in.
func TestWizardModeStepRendersEveryTeamCount(t *testing.T) {
	for n := config.MinTeamCount; n <= config.MaxTeamCount; n++ {
		a := newTestApp()
		a.modeEnum.Value = "teams"
		a.setTeamCount(n)
		a.countEd.SetText("2")
		a.splitPiecesCb.Value = true
		a.createWizStep = wizStepMode
		for _, size := range [][2]int{{360, 640}, {1200, 800}} {
			if d := a.createWizardOverlay(testCtx(size[0], size[1])); d.Size.X == 0 || d.Size.Y == 0 {
				t.Fatalf("%d teams at %dx%d: wizard laid out empty", n, size[0], size[1])
			}
		}
	}
}

// TestWizardCountFloor pins step 1's seat editor's floor per mode: a co-op
// game goes down to one player (a solo game, played for the high score), a
// team to one member, while a competitive game keeps needing an opponent.
// Blank or junk reads as the floor too, and the floor is the editor's hint.
func TestWizardCountFloor(t *testing.T) {
	a := newTestApp()
	for _, tc := range []struct {
		mode string
		text string
		want int
	}{
		{"cooperative", "1", 1}, {"cooperative", "0", 1}, {"cooperative", "", 1}, {"cooperative", "x", 1}, {"cooperative", "3", 3},
		{"competitive", "1", 2}, {"competitive", "", 2}, {"competitive", "4", 4},
		{"teams", "0", 1}, {"teams", "1", 1}, {"teams", "", 1}, {"teams", "2", 2},
	} {
		a.modeEnum.Value = tc.mode
		a.countEd.SetText(tc.text)
		if got := a.wizardCount(a.wizardMode()); got != tc.want {
			t.Errorf("%s with %q: wizardCount = %d, want %d", tc.mode, tc.text, got, tc.want)
		}
	}
}

// A solo co-op game's step 1 — the co-op radio, a count of one, its solo
// note and no board-width slider (one seat is the standard board) — lays out
// in the narrowest window Jetris runs in, like the rest of the wizard.
func TestWizardModeStepRendersSoloCoop(t *testing.T) {
	a := newTestApp()
	a.modeEnum.Value = "cooperative"
	a.countEd.SetText("1")
	a.createWizStep = wizStepMode
	for _, size := range [][2]int{{360, 640}, {1200, 800}} {
		if d := a.createWizardOverlay(testCtx(size[0], size[1])); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("solo co-op at %dx%d: wizard laid out empty", size[0], size[1])
		}
	}
}

// TestWizardCustomDefaults: the custom rules open at the Guideline preset —
// a fresh wizard's custom read-out IS the preset in every mode, so a creator
// who switches the radio to custom starts from the Guideline and changes
// only what they mean to.
func TestWizardCustomDefaults(t *testing.T) {
	a := newTestApp()
	for _, mode := range []config.GameMode{config.ModeCooperative, config.ModeCompetitive, config.ModeTeams} {
		if got, want := a.customRules().Normalized(mode), config.GuidelineRules().Normalized(mode); got != want {
			t.Errorf("%s: a fresh wizard's custom rules = %+v, want the Guideline preset %+v", mode, got, want)
		}
	}
	if a.nextCountEd.Text() != "6" || a.holesEd.Text() != "1" || !a.holdCb.Value || !a.guidelineCb.Value || a.bagEnum.Value != "single" {
		t.Errorf("the custom widgets do not show the preset: next %q, holes %q, hold %v, guideline garbage %v, bag %q",
			a.nextCountEd.Text(), a.holesEd.Text(), a.holdCb.Value, a.guidelineCb.Value, a.bagEnum.Value)
	}
	// Blank editors fall back to the preset too.
	a.nextCountEd.SetText("")
	a.holesEd.SetText("")
	if r := a.customRules(); r.NextCount != config.MaxNextCount || r.GarbageHoles != 1 {
		t.Errorf("blank editors read as next %d, holes %d; want the preset's 6 and 1", r.NextCount, r.GarbageHoles)
	}
	// setCustomRules round-trips every rule, the bag radio included.
	want := config.GameRules{NextCount: 2, Ghost: false, Hold: false, Bag: config.BagNone, ShowHeadroom: true, GarbageHoles: 3, RandomGarbageHoles: true}
	a.setCustomRules(want)
	if got := a.customRules(); got != want {
		t.Errorf("setCustomRules → customRules = %+v, want %+v", got, want)
	}
}

// TestWizardBag pins step 2's piece-bag radio: "single" — the default — is
// the 7-bag, "double" and "none" the other two kinds, junk the 7-bag; the
// custom read-out carries it, the Guideline preset's read-only list names
// the 7-bag, and every kind explains itself in its own words.
func TestWizardBag(t *testing.T) {
	a := newTestApp()
	if got := a.wizardBag(); got != config.BagSingle {
		t.Fatalf("a fresh wizard deals %q, want the 7-bag", got)
	}
	for value, want := range map[string]config.Bag{"double": config.BagDouble, "none": config.BagNone, "single": config.BagSingle, "junk": config.BagSingle} {
		a.bagEnum.Value = value
		if got := a.customRules().Bag; got != want {
			t.Errorf("radio %q: custom rules deal %q, want %q", value, got, want)
		}
	}
	found := false
	for _, row := range guidelineSummary(config.ModeCooperative) {
		if row[0] == "Piece bag" {
			found = true
		}
	}
	if !found {
		t.Error("the Guideline preset's list does not name its bag")
	}
	hints := map[string]bool{}
	for _, bag := range []config.Bag{config.BagSingle, config.BagDouble, config.BagNone} {
		hints[bagHint(bag)] = true
	}
	if len(hints) != 3 {
		t.Errorf("the three bag kinds share a hint: %v", hints)
	}
}

// TestWizardHiddenRows pins step 2's "Show hidden rows" checkbox: off in a
// fresh wizard, its value reaches the custom rules, the Guideline preset
// never shows the rows whatever the box says, and the preset's read-only
// list says so.
func TestWizardHiddenRows(t *testing.T) {
	a := newTestApp()
	if a.headroomCb.Value || a.customRules().ShowHeadroom {
		t.Fatal("a fresh wizard shows the hidden rows; they are off by default")
	}
	a.headroomCb.Value = true
	if !a.customRules().ShowHeadroom {
		t.Error("the checkbox does not reach the custom rules")
	}
	if config.GuidelineRules().ShowHeadroom {
		t.Error("the Guideline preset shows the hidden rows")
	}
	found := false
	for _, row := range guidelineSummary(config.ModeCooperative) {
		if row[0] == "Hidden rows" {
			found = true
		}
	}
	if !found {
		t.Error("the Guideline preset's list does not name the hidden rows")
	}
}
