package nativeui

import (
	"image"
	"os"
	"testing"

	"gioui.org/gpu/headless"

	"jetris/internal/lobby"
)

// TestWizardSnapshots renders the three-step create wizard for inspection:
// step 1 single and multiple (and named, and with a name the lobby's own),
// step 2 the Guideline list and the custom rules with the line goal, step 3
// open with the agent policy. Opt-in: set
// FW_SNAPSHOT_DIR (needs a GPU); the PNGs are for eyes, not for comparison.
func TestWizardSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render wizard snapshots")
	}
	size := image.Pt(1280, 820)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()
	stage := func(prep func(a *App)) *App {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		prep(a)
		return a
	}
	for _, c := range []struct {
		name string
		prep func(a *App)
	}{
		{"wizard_step1_single", func(a *App) { a.createWizStep = wizStepType; a.countEd.SetText("3") }},
		{"wizard_step1_single_competitive", func(a *App) {
			a.createWizStep, a.singleKindEnum.Value = wizStepType, "competitive"
			a.countEd.SetText("3")
		}},
		{"wizard_step1_multiple", func(a *App) {
			a.createWizStep, a.boardsEnum.Value = wizStepType, "multiple"
			a.setPlayfieldCount(3)
			a.countEd.SetText("2")
		}},
		{"wizard_step2_modern", func(a *App) { a.createWizStep = wizStepRules; a.countEd.SetText("3") }},
		{"wizard_step2_custom_lines", func(a *App) {
			a.createWizStep, a.rulesEnum.Value, a.lengthEnum.Value, a.boardsEnum.Value = wizStepRules, "custom", "lines", "multiple"
			a.countEd.SetText("2")
			a.setExtraRows(4)
		}},
		{"wizard_step3_open_agents", func(a *App) {
			a.createWizStep, a.createJoinEnum.Value, a.allowAgentsCb.Value = wizStepPlayers, "open", true
		}},
		{"wizard_step3_invite", func(a *App) { a.createWizStep, a.createJoinEnum.Value = wizStepPlayers, "invite" }},
		{"wizard_step1_solo", func(a *App) { a.createWizStep = wizStepType; a.countEd.SetText("1") }},
		{"wizard_step1_named", func(a *App) {
			a.createWizStep = wizStepType
			a.countEd.SetText("3")
			a.gameNameEd.SetText("Friday night!")
		}},
		{"wizard_step1_bad_name", func(a *App) {
			a.createWizStep = wizStepType
			a.countEd.SetText("3")
			a.gameNameEd.SetText("lobby")
			a.wizNameErr = a.wizardNameErr(a.wizardName())
		}},
		{"wizard_step3_teams_names", func(a *App) {
			a.createWizStep, a.boardsEnum.Value, a.createJoinEnum.Value = wizStepPlayers, "multiple", "open"
			a.setPlayfieldCount(3)
			a.countEd.SetText("2")
			a.teamNameEds[1].SetText("Sharks")
		}},
	} {
		a := stage(c.prep)
		snapshotPNGSized(t, w, dir, c.name, size, func(gtx C) { a.layout(gtx) })
	}
}
