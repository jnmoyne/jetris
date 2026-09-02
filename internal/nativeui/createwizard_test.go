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
