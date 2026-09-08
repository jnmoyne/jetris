package nativeui

import (
	"reflect"
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
	if got := a.wizardSpec().Rules; !got.IsGuideline(config.ModeCooperative) {
		t.Fatalf("the guideline radio produced %+v", got)
	}
	custom := a.customRules()
	if custom.IsGuideline(config.ModeCompetitive) {
		t.Fatal("next 2 / holes 3 is not the Guideline preset")
	}
	a.rulesEnum.Value = "custom"
	a.boardsEnum.Value = "multiple"
	a.countEd.SetText("1")
	if got := a.wizardSpec(); got.Mode != config.ModeCompetitive || got.Rules != custom.Normalized(config.ModeCompetitive) {
		t.Fatalf("the custom radio produced %+v, want the editors' %+v", got.Rules, custom)
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

// TestGuidelineSummaryPerShape: the read-only preset list shows the garbage
// rules only where several playfields raise garbage at each other, and the
// board and the deal only where a playfield has company.
func TestGuidelineSummaryPerShape(t *testing.T) {
	solo := guidelineSummary(config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1}.Normalized())
	crew := guidelineSummary(config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 3}.Normalized())
	comp := guidelineSummary(config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2}.Normalized())
	teams := guidelineSummary(config.GameSpec{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2}.Normalized())
	names := func(rows [][2]string) map[string]bool {
		m := map[string]bool{}
		for _, r := range rows {
			m[r[0]] = true
		}
		return m
	}
	if n := names(solo); n["Garbage"] || n["Attacks"] || n["Board"] || n["Pieces"] {
		t.Errorf("a solo game's summary lists garbage or the board: %v", solo)
	}
	if n := names(crew); n["Garbage"] || !n["Board"] || !n["Pieces"] {
		t.Errorf("a crew's summary: %v", crew)
	}
	if n := names(comp); !n["Garbage"] || !n["Attacks"] || n["Board"] || n["Pieces"] {
		t.Errorf("a competitive summary: %v", comp)
	}
	if n := names(teams); !n["Garbage"] || !n["Board"] || !n["Pieces"] {
		t.Errorf("a teams summary: %v", teams)
	}
	if len(comp) != len(solo)+2 {
		t.Fatalf("competitive summary has %d rows, solo %d; want two garbage rows more", len(comp), len(solo))
	}
}

// TestWizardSpecMapping pins how step 1's game types map onto the modes: a
// single playfield is a cooperative-mode board, scored together or per seat;
// several playfields with one player each are competitive, with more a
// teams game; and the seat count is per playfield when there are several.
func TestWizardSpecMapping(t *testing.T) {
	for _, tc := range []struct {
		name         string
		boards, kind string
		playfields   int
		count        string
		want         config.GameSpec
	}{
		{"single co-op", "single", "coop", 2, "3", config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 3}},
		{"single competitive", "single", "competitive", 2, "3", config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 3, Scoring: config.ScoringIndividual}},
		{"solo", "single", "coop", 2, "1", config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1}},
		{"solo is solo whatever the kind radio says", "single", "competitive", 2, "1", config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1}},
		{"multiple × 1", "multiple", "coop", 3, "1", config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 3}},
		{"multiple × 2", "multiple", "coop", 3, "2", config.GameSpec{Mode: config.ModeTeams, TeamCount: 3, TeamSize: 2, PlayerCount: 6, TeamNames: []string{"Cyan", "Yellow", "Purple"}}},
	} {
		a := newTestApp()
		a.boardsEnum.Value, a.singleKindEnum.Value = tc.boards, tc.kind
		a.setPlayfieldCount(tc.playfields)
		a.countEd.SetText(tc.count)
		got := a.wizardSpec()
		want := tc.want
		want.InviteOnly = true // the wizard's default
		want.Rules = config.GuidelineRules()
		want.ExtraColumns, want.ExtraRows, want.SplitPieces = config.DefaultExtraColumns, config.DefaultExtraRows, false
		want = want.Normalized()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: wizardSpec() = %+v, want %+v", tc.name, got, want)
		}
	}
}

// TestWizardTeamNames pins step 3's team names: the piece colours by
// default, an edited name kept (trimmed, capped), a blank one the colour,
// and only a teams game carries any — a board each, or a single playfield,
// has no teams to name.
func TestWizardTeamNames(t *testing.T) {
	a := newTestApp()
	a.boardsEnum.Value = "multiple"
	a.setPlayfieldCount(3)
	a.countEd.SetText("2")
	if got := a.wizardSpec().TeamNames; !reflect.DeepEqual(got, []string{"Cyan", "Yellow", "Purple"}) {
		t.Fatalf("a fresh wizard names the teams %v, want the piece colours", got)
	}
	a.teamNameEds[0].SetText("  Sharks ")
	a.teamNameEds[1].SetText("")
	a.teamNameEds[2].SetText("A very long team name indeed")
	got := a.wizardSpec().TeamNames
	if len(got) != 3 || got[0] != "Sharks" || got[1] != "Yellow" || len([]rune(got[2])) != config.MaxTeamNameLen {
		t.Errorf("edited names = %v", got)
	}
	a.countEd.SetText("1")
	if got := a.wizardSpec().TeamNames; got != nil {
		t.Errorf("a board each carries team names %v", got)
	}
	a.boardsEnum.Value = "single"
	a.countEd.SetText("3")
	if got := a.wizardSpec().TeamNames; got != nil {
		t.Errorf("a single playfield carries team names %v", got)
	}
	// Step 3 lays out with the editors for a teams game.
	a.boardsEnum.Value = "multiple"
	a.countEd.SetText("2")
	a.createWizStep = wizStepPlayers
	for _, size := range [][2]int{{360, 640}, {1200, 800}} {
		if d := a.createWizardOverlay(testCtx(size[0], size[1])); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("step 3 with team names laid out empty at %dx%d", size[0], size[1])
		}
	}
}

// TestWizardCountFloor pins step 1's seat editor's floor: one, always — a
// solo game on a single playfield, a board of their own per playfield.
// Blank or junk reads as one too. The co-op/competitive choice is a board
// with company's: alone, the kind radio means nothing.
func TestWizardCountFloor(t *testing.T) {
	a := newTestApp()
	for _, tc := range []struct {
		boards, kind string
		text         string
		want         int
	}{
		{"single", "coop", "1", 1}, {"single", "coop", "0", 1}, {"single", "coop", "", 1}, {"single", "coop", "x", 1}, {"single", "coop", "3", 3},
		{"single", "competitive", "1", 1}, {"single", "competitive", "", 1}, {"single", "competitive", "4", 4},
		{"multiple", "coop", "0", 1}, {"multiple", "coop", "1", 1}, {"multiple", "coop", "", 1}, {"multiple", "coop", "2", 2},
	} {
		a.boardsEnum.Value, a.singleKindEnum.Value = tc.boards, tc.kind
		a.countEd.SetText(tc.text)
		if got := a.wizardCount(); got != tc.want {
			t.Errorf("%s/%s with %q: wizardCount = %d, want %d", tc.boards, tc.kind, tc.text, got, tc.want)
		}
	}
	a.boardsEnum.Value, a.singleKindEnum.Value = "single", "competitive"
	a.countEd.SetText("1")
	if a.wizardIndividual() || a.wizardSpec().IndividualScoring() {
		t.Error("one player alone was scored per seat")
	}
	a.countEd.SetText("2")
	if !a.wizardIndividual() {
		t.Error("two players with the competitive kind were not scored per seat")
	}
}

// TestWizardPlayfieldCount pins step 1's playfield-count knob: it defaults
// to the usual two, clamps to the legal range, reads back only for a
// multi-playfield game, and its slider position round-trips through every
// detent so a dragged slider always rests on a whole number of playfields.
func TestWizardPlayfieldCount(t *testing.T) {
	a := newTestApp()
	if got := a.wizardPlayfieldCount(); got != 1 {
		t.Errorf("a single playfield reads %d playfields, want 1", got)
	}
	a.boardsEnum.Value = "multiple"
	if got := a.wizardPlayfieldCount(); got != config.DefaultTeamCount {
		t.Errorf("a fresh wizard offers %d playfields, want %d", got, config.DefaultTeamCount)
	}
	for _, tc := range []struct{ set, want int }{
		{1, config.MinTeamCount}, {2, 2}, {4, 4},
		{config.MaxTeamCount, config.MaxTeamCount}, {config.MaxTeamCount + 3, config.MaxTeamCount},
	} {
		a.setPlayfieldCount(tc.set)
		if got := a.wizardPlayfieldCount(); got != tc.want {
			t.Errorf("setPlayfieldCount(%d) → %d playfields, want %d", tc.set, got, tc.want)
		}
		if got := playfieldsRange.value(a.playfieldsFloat.Value); got != tc.want {
			t.Errorf("setPlayfieldCount(%d): slider reads %d, want %d", tc.set, got, tc.want)
		}
	}
	for n := config.MinTeamCount; n <= config.MaxTeamCount; n++ {
		if got := playfieldsRange.value(playfieldsRange.pos(n)); got != n {
			t.Errorf("playfield-count detent %d round-tripped to %d", n, got)
		}
	}
}

// TestWizardSplitPieces pins the distribute-the-pieces box: off by default,
// honoured once checked on every playfield with company — a crew of two, a
// team of two — and ignored where nobody shares a playfield, so no solo or
// board-each game is ever created advertising a split that cannot happen.
// The Guideline preset never deals the pieces out, whatever the box says.
func TestWizardSplitPieces(t *testing.T) {
	a := newTestApp()
	if a.splitPiecesCb.Value {
		t.Fatal("a fresh wizard distributes the pieces")
	}
	a.splitPiecesCb.Value = true
	a.rulesEnum.Value = "custom"
	a.boardsEnum.Value = "multiple"
	a.countEd.SetText("2")
	if !a.wizardSpec().SplitsPieces() {
		t.Error("a checked box in a 2v2 did not split the pieces")
	}
	a.countEd.SetText("1")
	if a.wizardSpec().SplitsPieces() {
		t.Error("a team of one split its pieces")
	}
	a.boardsEnum.Value = "single"
	a.countEd.SetText("3")
	if !a.wizardSpec().SplitsPieces() {
		t.Error("a checked box in a crew of three did not split the pieces")
	}
	a.countEd.SetText("1")
	if a.wizardSpec().SplitsPieces() {
		t.Error("a solo game split its pieces")
	}
	a.countEd.SetText("3")
	a.splitPiecesCb.Value = false
	if a.wizardSpec().SplitsPieces() {
		t.Error("an unchecked box split the pieces")
	}
	a.splitPiecesCb.Value = true
	a.rulesEnum.Value = "guideline"
	if a.wizardSpec().SplitsPieces() {
		t.Error("the Guideline preset dealt the pieces out")
	}
}

// TestWizardLineGoal pins step 2's game length: until top out is no goal,
// a number of lines the editor's number — blank or junk the classic forty,
// clamped to the cap.
func TestWizardLineGoal(t *testing.T) {
	a := newTestApp()
	if a.lengthEnum.Value != "topout" || a.wizardLineGoal() != 0 || a.wizardSpec().LineGoal != 0 {
		t.Fatalf("a fresh wizard runs to %d lines, want until top out", a.wizardLineGoal())
	}
	if a.lineGoalEd.Text() != "40" {
		t.Errorf("the lines editor opens at %q, want 40", a.lineGoalEd.Text())
	}
	a.lengthEnum.Value = "lines"
	for text, want := range map[string]int{"40": 40, "": config.DefaultLineGoal, "x": config.DefaultLineGoal, "0": config.DefaultLineGoal, "1": 1, "150": 150, "5000": config.MaxLineGoal} {
		a.lineGoalEd.SetText(text)
		if got := a.wizardLineGoal(); got != want {
			t.Errorf("lines %q: wizardLineGoal = %d, want %d", text, got, want)
		}
		if got := a.wizardSpec().LineGoal; got != want {
			t.Errorf("lines %q: spec line goal = %d, want %d", text, got, want)
		}
	}
	a.lengthEnum.Value = "topout"
	if a.wizardSpec().LineGoal != 0 {
		t.Error("until top out kept a line goal")
	}
}

// TestWizardExtraRows pins step 2's extra-rows knob: zero by default (the
// Guideline playfield whatever the crew), clamped to the legal range, its
// slider resting on whole rows, honoured on a shared playfield with company
// under custom rules and dropped for a board each; the Guideline preset
// keeps the default.
func TestWizardExtraRows(t *testing.T) {
	a := newTestApp()
	if a.extraRows != config.DefaultExtraRows {
		t.Fatalf("a fresh wizard adds %d rows, want %d", a.extraRows, config.DefaultExtraRows)
	}
	for _, tc := range []struct{ set, want int }{{-1, 0}, {0, 0}, {5, 5}, {10, 10}, {11, 10}} {
		a.setExtraRows(tc.set)
		if a.extraRows != tc.want || extraRowsRange.value(a.extraRowsFloat.Value) != tc.want {
			t.Errorf("setExtraRows(%d) → %d (slider %d), want %d", tc.set, a.extraRows, extraRowsRange.value(a.extraRowsFloat.Value), tc.want)
		}
	}
	for n := config.MinExtraRows; n <= config.MaxExtraRows; n++ {
		if got := extraRowsRange.value(extraRowsRange.pos(n)); got != n {
			t.Errorf("extra-rows detent %d round-tripped to %d", n, got)
		}
	}
	a.setExtraRows(5)
	a.setExtraColumns(6)
	a.rulesEnum.Value = "custom"
	a.countEd.SetText("3")
	if spec := a.wizardSpec(); spec.ExtraRows != 5 || spec.ExtraColumns != 6 {
		t.Errorf("custom rules produced %d extra rows, %d columns; want 5 and 6", spec.ExtraRows, spec.ExtraColumns)
	}
	a.boardsEnum.Value = "multiple"
	a.countEd.SetText("1")
	if spec := a.wizardSpec(); spec.ExtraRows != 0 || spec.ExtraColumns != 0 {
		t.Errorf("a board each kept %d extra rows, %d columns", spec.ExtraRows, spec.ExtraColumns)
	}
	a.boardsEnum.Value = "single"
	a.countEd.SetText("3")
	a.rulesEnum.Value = "guideline"
	if spec := a.wizardSpec(); spec.ExtraRows != config.DefaultExtraRows || spec.ExtraColumns != config.DefaultExtraColumns {
		t.Errorf("the Guideline preset produced %d extra rows, %d columns; want the defaults", spec.ExtraRows, spec.ExtraColumns)
	}
}

// TestWizardAgentsPolicy pins step 3's agent policy: only an open game has
// one, unchecked means none, the count is clamped to the seats, blank or
// junk reads as one.
func TestWizardAgentsPolicy(t *testing.T) {
	a := newTestApp()
	a.countEd.SetText("3")
	a.allowAgentsCb.Value = true
	a.maxAgentsEd.SetText("9")
	if spec := a.wizardSpec(); !spec.InviteOnly || spec.MaxAgents != 0 {
		t.Errorf("an invite-only game carries an agent policy: %+v", spec)
	}
	a.createJoinEnum.Value = "open"
	if spec := a.wizardSpec(); spec.InviteOnly || spec.MaxAgents != 3 {
		t.Errorf("an open game's policy = %d, want 3 (clamped to the seats)", spec.MaxAgents)
	}
	a.maxAgentsEd.SetText("")
	if spec := a.wizardSpec(); spec.MaxAgents != 1 {
		t.Errorf("a blank max-agents reads as %d, want 1", spec.MaxAgents)
	}
	a.allowAgentsCb.Value = false
	if spec := a.wizardSpec(); spec.MaxAgents != 0 {
		t.Errorf("an unchecked box let %d agents in", spec.MaxAgents)
	}
}

// The create wizard's step 1 draws for a multi-playfield game at every
// legal playfield count — the radios, the slider (whose hint names every
// team), the per-playfield seat editor — in one modal that must lay out in
// the narrowest window Jetris runs in; and for a solo game on a single one.
func TestWizardTypeStepRenders(t *testing.T) {
	for n := config.MinTeamCount; n <= config.MaxTeamCount; n++ {
		a := newTestApp()
		a.boardsEnum.Value = "multiple"
		a.setPlayfieldCount(n)
		a.countEd.SetText("2")
		a.createWizStep = wizStepType
		for _, size := range [][2]int{{360, 640}, {1200, 800}} {
			if d := a.createWizardOverlay(testCtx(size[0], size[1])); d.Size.X == 0 || d.Size.Y == 0 {
				t.Fatalf("%d playfields at %dx%d: wizard laid out empty", n, size[0], size[1])
			}
		}
	}
	for _, kind := range []string{"coop", "competitive"} {
		a := newTestApp()
		a.singleKindEnum.Value = kind
		a.countEd.SetText("1")
		a.createWizStep = wizStepType
		for _, size := range [][2]int{{360, 640}, {1200, 800}} {
			if d := a.createWizardOverlay(testCtx(size[0], size[1])); d.Size.X == 0 || d.Size.Y == 0 {
				t.Fatalf("single %s at %dx%d: wizard laid out empty", kind, size[0], size[1])
			}
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
	for _, row := range guidelineSummary(config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1}) {
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
	for _, row := range guidelineSummary(config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1}) {
		if row[0] == "Hidden rows" {
			found = true
		}
	}
	if !found {
		t.Error("the Guideline preset's list does not name the hidden rows")
	}
}

// TestWizardStepsAndLabels pins the wizard's shape: three steps, Next on
// the first two, "Choose players…" or "Create game" on the last by the
// join choice, Back everywhere but the first.
func TestWizardStepsAndLabels(t *testing.T) {
	if wizStepCount != 3 || wizStepType != 1 || wizStepRules != 2 || wizStepPlayers != 3 {
		t.Fatalf("the wizard has %d steps (%d/%d/%d), want three", wizStepCount, wizStepType, wizStepRules, wizStepPlayers)
	}
	a := newTestApp()
	for _, step := range []int{wizStepType, wizStepRules, wizStepPlayers} {
		a.createWizStep = step
		for _, join := range []string{"invite", "open"} {
			a.createJoinEnum.Value = join
			if d := a.createWizardOverlay(testCtx(1200, 800)); d.Size.X == 0 || d.Size.Y == 0 {
				t.Fatalf("step %d (%s) laid out empty", step, join)
			}
		}
	}
}
