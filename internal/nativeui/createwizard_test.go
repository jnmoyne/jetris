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
	a.nextCountEd.SetText("4")
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
