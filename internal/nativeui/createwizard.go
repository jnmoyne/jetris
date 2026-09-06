package nativeui

import (
	"fmt"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
)

// The create-game wizard's steps, in order. An open game visits all four; an
// invite-only game ends at wizStepJoin (the invitee picker takes over the
// agent question — an invite-only game's agent policy is per-invite).
const (
	wizStepMode   = 1 // game type + seat count
	wizStepNext   = 2 // the play rules: the Guideline preset, or custom (preview, ghost, hold, bag, garbage)
	wizStepJoin   = 3 // open vs invite-only
	wizStepAgents = 4 // agent policy (open games only)
)

// handleCreateWizard drives the create-game wizard each frame: Cancel closes
// it, Back returns one step, and Next advances — on the last step it reads
// the widgets and launches the game. Returns true while the wizard is open
// (so the caller draws the modal and suppresses background clicks).
func (a *App) handleCreateWizard(gtx C) bool {
	if a.createWizStep == 0 {
		return false
	}
	if a.wizCancelBtn.Clicked(gtx) {
		a.createWizStep = 0
		return false
	}
	if a.wizBackBtn.Clicked(gtx) && a.createWizStep > wizStepMode {
		a.createWizStep--
	}
	if a.wizNextBtn.Clicked(gtx) {
		switch a.createWizStep {
		case wizStepMode, wizStepNext:
			a.createWizStep++
		case wizStepJoin:
			if a.createJoinEnum.Value == "invite" {
				a.finishCreateWizard()
			} else {
				a.createWizStep = wizStepAgents
			}
		default:
			a.finishCreateWizard()
		}
	}
	return a.createWizStep != 0
}

// wizardMode is the game type step 1 currently has picked.
func (a *App) wizardMode() config.GameMode {
	switch a.modeEnum.Value {
	case "competitive":
		return config.ModeCompetitive
	case "teams":
		return config.ModeTeams
	default:
		return config.ModeCooperative
	}
}

// wizardCount reads step 1's seat-count editor for a game of mode: players
// PER TEAM in teams mode, the total player count otherwise. Blank, junk or
// too few falls back to the smallest game the mode can hold
// (config.MinPlayerCount — a co-op game can be played alone, for the high
// score), so the step's live readouts and the create itself always agree.
func (a *App) wizardCount(mode config.GameMode) int {
	count, err := strconv.Atoi(strings.TrimSpace(a.countEd.Text()))
	if floor := config.MinPlayerCount(mode); err != nil || count < floor {
		return floor
	}
	return count
}

// extraColsRange maps the board-width slider's 0..1 position to the whole
// number of columns one seat adds, and back to that column's detent.
var extraColsRange = knobRange{config.MinExtraColumns, config.MaxExtraColumns, 1}

// setExtraColumns sets the board-width knob — how many columns a shared
// board gains per seat beyond the first — and mirrors it onto its slider.
func (a *App) setExtraColumns(v int) {
	a.extraCols = min(max(v, config.MinExtraColumns), config.MaxExtraColumns)
	a.extraColsFloat.Value = extraColsRange.pos(a.extraCols)
}

// teamCountRange maps the team-count slider's 0..1 position to the whole
// number of teams a teams game is played between, and back to that detent.
var teamCountRange = knobRange{config.MinTeamCount, config.MaxTeamCount, 1}

// setTeamCount sets the team-count knob — how many teams a teams game is
// played between — and mirrors it onto its slider.
func (a *App) setTeamCount(v int) {
	a.teamCount = config.NormalizeTeamCount(v)
	a.teamCountFloat.Value = teamCountRange.pos(a.teamCount)
}

// wizardTeamCount reads step 1's team-count knob for a game of mode: the
// number of teams a teams game is played between, and 0 in every other mode,
// which has no teams at all.
func (a *App) wizardTeamCount(mode config.GameMode) int {
	if mode != config.ModeTeams {
		return 0
	}
	return config.NormalizeTeamCount(a.teamCount)
}

// finishCreateWizard reads the wizard's widgets, clamps them to legal values,
// closes the wizard, and launches the game: an invite-only game is created and
// hands off to the invitee picker, an open game is created directly with its
// agent policy.
func (a *App) finishCreateWizard() {
	mode := a.wizardMode()
	count := a.wizardCount(mode)
	teamCount := a.wizardTeamCount(mode)
	// The board-width knob only shapes a shared board; a competitive game
	// gives every player a standard 10-column board of their own.
	extraCols := 0
	if mode != config.ModeCompetitive {
		extraCols = a.extraCols
	}
	splitPieces := a.wizardSplit(mode, count)
	// The play rules: the Guideline preset as chosen on step 2, or the
	// custom editors' read-out; either way clamped for the mode (a
	// cooperative game records no garbage rules — no misleading tag).
	rules := config.GuidelineRules()
	if a.rulesEnum.Value == "custom" {
		rules = a.customRules()
	}
	rules = rules.Normalized(mode)
	a.createWizStep = 0
	if a.createJoinEnum.Value == "invite" {
		go a.openInvitePicker(mode, count, teamCount, extraCols, splitPieces, rules)
		return
	}
	// Agent policy: how many seats idle agent players may take.
	// Unchecked = 0 = agents may not join. Clamped to the game's total
	// player count (the count editor is per-team in teams mode).
	maxAgents := 0
	if a.allowAgentsCb.Value {
		total := count
		if mode == config.ModeTeams {
			total = teamCount * count
		}
		n, err := strconv.Atoi(strings.TrimSpace(a.maxAgentsEd.Text()))
		if err != nil || n < 1 {
			n = 1
		}
		maxAgents = min(n, total)
	}
	go func() { a.createGame(mode, count, teamCount, extraCols, maxAgents, splitPieces, rules, false) }()
}

// setCustomRules loads the wizard's custom-rules widgets from a rules bundle
// — the Guideline preset at startup (App.New), so custom rules begin as the
// preset and the creator changes only what they mean to change.
func (a *App) setCustomRules(r config.GameRules) {
	a.nextCountEd.SetText(strconv.Itoa(r.NextCount))
	a.ghostCb.Value = r.Ghost
	a.holdCb.Value = r.Hold
	a.bagEnum.Value = bagRadio(r.Bag)
	a.headroomCb.Value = r.ShowHeadroom
	a.holesEd.SetText(strconv.Itoa(r.GarbageHoles))
	a.randomHolesCb.Value = r.RandomGarbageHoles
	a.guidelineCb.Value = r.GuidelineGarbage
}

// bagRadio is the piece-bag radio's value for a bag kind (wizardBag read
// backwards).
func bagRadio(bag config.Bag) string {
	switch bag.Normalized() {
	case config.BagDouble:
		return "double"
	case config.BagNone:
		return "none"
	default:
		return "single"
	}
}

// customRules reads the wizard's custom-rules widgets. The upcoming-piece
// preview is how many next pieces the game reveals to everyone (players,
// spectators, agents): blank or junk falls back to the Guideline preset's
// count. The ghost, the hold and the hidden rows are per-game rules like the
// preview — the creator's checkboxes decide them for every seat — and so is
// the bag the pieces are dealt from (wizardBag). Garbage holes are how many
// empty cells every garbage row is raised with in the modes that raise
// garbage (blank or junk: the preset's one hole), with the random-positions
// and Guideline-attack-table checkboxes beside it. Ranges are clamped by
// GameRules.Normalized.
func (a *App) customRules() config.GameRules {
	preset := config.GuidelineRules()
	nextCount, err := strconv.Atoi(strings.TrimSpace(a.nextCountEd.Text()))
	if err != nil {
		nextCount = preset.NextCount
	}
	holes, err := strconv.Atoi(strings.TrimSpace(a.holesEd.Text()))
	if err != nil {
		holes = preset.GarbageHoles
	}
	return config.GameRules{
		NextCount:          nextCount,
		Ghost:              a.ghostCb.Value,
		Hold:               a.holdCb.Value,
		Bag:                a.wizardBag(),
		ShowHeadroom:       a.headroomCb.Value,
		GarbageHoles:       holes,
		RandomGarbageHoles: a.randomHolesCb.Value,
		GuidelineGarbage:   a.guidelineCb.Value,
	}
}

// wizardBag reads step 2's piece-bag radio (custom rules): the randomizer
// every seat's sequence is dealt with — the standard 7-bag unless the creator
// picked the double bag or no bag at all (config.Bag). The Guideline preset
// never asks: the Guideline's randomizer is the 7-bag.
func (a *App) wizardBag() config.Bag {
	switch a.bagEnum.Value {
	case "double":
		return config.BagDouble
	case "none":
		return config.BagNone
	default:
		return config.BagSingle
	}
}

// bagHint describes the bag the radio has picked in the terms a player feels
// it: whether two of a kind can come back to back, and how long a drought of
// one type can last.
func bagHint(bag config.Bag) string {
	switch bag {
	case config.BagDouble:
		return "Two of each type in every fourteen pieces, shuffled together: fair over a longer stretch, so two of a kind can come back to back and a drought can last up to twenty-four pieces."
	case config.BagNone:
		return "Pure chance, the old-school way: every piece is drawn on its own, any type as likely as any other — three S's in a row and a forty-piece I drought are both fair game."
	default:
		return "The Guideline randomizer: every seven pieces are the seven types shuffled, so every type turns up in every seven and a drought never lasts more than twelve."
	}
}

// createWizardOverlay renders the modal create-game wizard. All actions are
// dispatched by handleCreateWizard; this only draws.
func (a *App) createWizardOverlay(gtx C) D {
	step := a.createWizStep
	if step == 0 {
		return D{}
	}
	invite := a.createJoinEnum.Value == "invite"

	// An invite-only game has no agents step, so the step count the header
	// advertises follows the current choice.
	total := 4
	if invite {
		total = 3
	}
	stepTitle := ""
	var body layout.Widget
	switch step {
	case wizStepMode:
		stepTitle = "GAME TYPE & PLAYERS"
		body = a.wizardModeStep
	case wizStepNext:
		stepTitle = "GAME RULES"
		body = a.wizardNextStep
	case wizStepJoin:
		stepTitle = "WHO CAN JOIN"
		body = a.wizardJoinStep
	default:
		stepTitle = "AGENTS"
		body = a.wizardAgentsStep
	}
	nextLabel := "Next"
	switch {
	case step == wizStepJoin && invite:
		nextLabel = "Choose players…"
	case step == wizStepAgents:
		nextLabel = "Create game"
	}

	// The body scrolls when the step is taller than the window leaves it
	// (the custom rules step of a garbage mode at the minimum window
	// height): the window height less the modal's header, footer and
	// chrome is what it may take before scrolling.
	bodyMaxY := max(gtx.Constraints.Max.Y-gtx.Dp(200), gtx.Dp(120))
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = modalW(gtx, 480)
		return a.tutMark(gtx, tutWizard, func(gtx C) D {
			return a.createWizardBox(gtx, step, total, stepTitle, nextLabel, bodyMaxY, body)
		})
	})
}

// createWizardBox is the wizard's dialog: the title, the step line, the
// step's body in its scrolling slot, and the buttons under it.
func (a *App) createWizardBox(gtx C, step, total int, stepTitle, nextLabel string, bodyMaxY int, body layout.Widget) D {
	return hardShadow(gtx, func(gtx C) D {
		return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
			return background(gtx, colBg, func(gtx C) D {
				return layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(a.pixel(unit.Sp(13), "CREATE A NEW GAME", colFg).Layout),
						layout.Rigid(spacer(6)),
						layout.Rigid(a.pixel(unit.Sp(9), fmt.Sprintf("STEP %d OF %d — %s", step, total, stepTitle), colAccent).Layout),
						layout.Rigid(spacer(14)),
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Min.Y = 0
							gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, bodyMaxY)
							return material.List(a.th, &a.wizList).Layout(gtx, 1, func(gtx C, _ int) D { return body(gtx) })
						}),
						layout.Rigid(spacer(18)),
						layout.Rigid(func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D { return a.dangerButton(gtx, &a.wizCancelBtn, "Cancel") }),
								layout.Flexed(1, func(gtx C) D { return D{Size: gtx.Constraints.Min} }),
								layout.Rigid(func(gtx C) D {
									if step == wizStepMode {
										return D{}
									}
									return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx C) D {
										return a.secondaryButton(gtx, &a.wizBackBtn, "Back")
									})
								}),
								layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.wizNextBtn, nextLabel) }),
							)
						}),
					)
				})
			})
		})
	})
}

// wizardRadio is one full-width wizard choice: a radio button whose label
// carries the choice and a short dash description.
func (a *App) wizardRadio(enum *widget.Enum, value, label string) layout.Widget {
	return func(gtx C) D {
		rb := material.RadioButton(a.th, enum, value, label)
		rb.Color = colFg
		return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, rb.Layout)
	}
}

// wizardModeStep is step 1: pick the game type, how many players it seats
// (players per team in teams mode) and — for the modes whose players share
// one board — how wide that board grows per seat.
func (a *App) wizardModeStep(gtx C) D {
	mode := a.wizardMode()
	teams := mode == config.ModeTeams
	countLabel := "Players:"
	switch {
	case teams:
		countLabel = "Players per team:"
	case mode == config.ModeCooperative:
		// A co-op crew has no ceiling: one player runs for the solo high
		// score, and any number may share the board.
		countLabel = "Players (1–∞):"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.wizardRadio(&a.modeEnum, "cooperative", "Co-op — everyone plays one shared board, one shared score")),
		layout.Rigid(a.wizardRadio(&a.modeEnum, "competitive", "Competitive — own board each, last player standing wins")),
		layout.Rigid(a.wizardRadio(&a.modeEnum, "teams", "Teams — team against team, each team a shared board")),
		layout.Rigid(func(gtx C) D {
			if !teams {
				return D{}
			}
			return a.wizardTeamCountKnob(gtx)
		}),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body(countLabel, colMuted)),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = gtx.Dp(48)
					gtx.Constraints.Min.X = gtx.Dp(48)
					// The hint is what a blank editor creates: the
					// smallest game the mode can hold.
					return a.editorBox(gtx, &a.countEd, strconv.Itoa(config.MinPlayerCount(mode)))
				}),
				layout.Rigid(func(gtx C) D {
					note := ""
					switch {
					case teams:
						note = fmt.Sprintf("(total seats = %d × per team = %d)",
							a.wizardTeamCount(mode), a.wizardTeamCount(mode)*a.wizardCount(mode))
					case mode == config.ModeCooperative && a.wizardCount(mode) == 1:
						// A crew of one: the shared board and score are
						// theirs alone, and the game is a run at the solo
						// co-op high score.
						note = "(solo — you play for the high score)"
					}
					if note == "" {
						return D{}
					}
					return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, a.body(note, colMuted))
				}),
			)
		}),
		layout.Rigid(func(gtx C) D {
			if mode == config.ModeCompetitive {
				return D{} // a board each, always the standard 10 columns
			}
			if a.wizardCount(mode) < 2 {
				return D{} // one seat is the standard 10 columns: no width to set
			}
			return a.wizardBoardWidth(gtx, mode)
		}),
		layout.Rigid(func(gtx C) D {
			// Splitting the pieces only means something between teammates, so
			// the box is only offered to a team that has some.
			seats := a.wizardCount(mode)
			if !teams || seats < 2 {
				return D{}
			}
			return a.wizardSplitPieces(gtx, seats)
		}),
	)
}

// wizardTeamCountKnob is step 1's team-count slider, drawn for a teams game:
// how many teams the game is played between. Two — Team A vs Team B — is the
// default and the game teams mode has always been; past two every team still
// gets a shared board of its own, an attack still weighs one raise, and each
// raise lands on one opposing team in turn rather than on all of them, so a
// six-way game is a longer game and not a faster death. The last team
// standing wins.
func (a *App) wizardTeamCountKnob(gtx C) D {
	if a.teamCountFloat.Update(gtx) {
		a.teamCount = teamCountRange.value(a.teamCountFloat.Value)
		a.teamCountFloat.Value = teamCountRange.pos(a.teamCount) // rest on the detent
	}
	n := a.wizardTeamCount(config.ModeTeams)
	names := make([]string, n)
	for t := range names {
		names[t] = config.TeamLetter(t)
	}
	hint := fmt.Sprintf("Team %s — each on its own shared board, each attacking the others in turn. The last team standing wins.", strings.Join(names, " vs Team "))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(spacer(12)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body(fmt.Sprintf("Teams (%d–%d):", config.MinTeamCount, config.MaxTeamCount), colMuted)),
				layout.Rigid(hSpacer(8)),
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Max.Y = gtx.Dp(20) // a row, not a touch target
					s := material.Slider(a.th, &a.teamCountFloat)
					s.Color = colAccent
					return s.Layout(gtx)
				}),
				layout.Rigid(hSpacer(8)),
				layout.Rigid(a.body(strconv.Itoa(n), colFg)),
			)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(hint, colMuted)),
	)
}

// wizardSplit reads step 1's piece-split checkbox for a game of mode with
// count seats (per team): the seven piece types are dealt out between the
// teammates only in a teams game that HAS teammates — a team of one would be
// dealt the whole bag anyway, and no other mode has anyone to split with, so
// the box is neither drawn nor honoured there.
func (a *App) wizardSplit(mode config.GameMode, count int) bool {
	return mode == config.ModeTeams && count > 1 && a.splitPiecesCb.Value
}

// wizardSplitPieces is step 1's piece-split checkbox, drawn for a teams game
// with teammates to split between: instead of every seat running the same
// full 7-bag, the seven piece types are dealt out among the seats — every
// type going to somebody, nobody left empty-handed — and each teammate plays
// only their own. The team still has the whole bag; it just has to co-operate
// to use it, since the player holding the I is the only one who can hand the
// board an I. The deal follows the game's seed, so both teams get the same
// hands and the match stays fair.
func (a *App) wizardSplitPieces(gtx C, seats int) D {
	hint := fmt.Sprintf("The seven piece types are dealt out between the %d teammates — every type to somebody, at least one type each — and each of them only ever plays their own. Both teams are dealt the same hands.", seats)
	if !a.splitPiecesCb.Value {
		hint = "Off: every teammate plays the same full 7-bag, as always."
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(spacer(12)),
		layout.Rigid(func(gtx C) D {
			cb := material.CheckBox(a.th, &a.splitPiecesCb, "Split the pieces between teammates")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(hint, colMuted)),
	)
}

// wizardBoardWidth is step 1's board-width slider, drawn for the modes whose
// players share a board (cooperative, and teams within each team): how many
// columns every seat beyond the first adds to the board's standard 10. At the
// default of 4 the seats sit shoulder to shoulder — two players on 14
// columns, three on 18 — and at the maximum of 10 every player gets a full
// standard section of their own, the board Jetris had before the slider. The
// same step spaces the spawn points, so a wider board is also a roomier one
// to spawn into. Competitive never sees it: each player has their own board.
// Nor does a shared board of one seat (a solo co-op game, a team of one):
// that is the standard 10 columns whatever the setting, so the step leaves
// the slider out until there is a second seat to widen the board for.
func (a *App) wizardBoardWidth(gtx C, mode config.GameMode) D {
	if a.extraColsFloat.Update(gtx) {
		a.extraCols = extraColsRange.value(a.extraColsFloat.Value)
		a.extraColsFloat.Value = extraColsRange.pos(a.extraCols) // rest on the detent
	}
	seats := a.wizardCount(mode)
	hint := fmt.Sprintf("The shared board is %d columns for the first player and %d more for every player after — %d columns for %d players.",
		config.StandardWidth, a.extraCols, config.SharedBoardWidth(seats, a.extraCols), seats)
	if mode == config.ModeTeams {
		hint = fmt.Sprintf("Each team's board is %d columns for the first teammate and %d more for every teammate after — %d columns for teams of %d.",
			config.StandardWidth, a.extraCols, config.TeamBoardWidth(seats, a.extraCols), seats)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(spacer(12)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body(fmt.Sprintf("Extra columns per player (%d–%d):", config.MinExtraColumns, config.MaxExtraColumns), colMuted)),
				layout.Rigid(hSpacer(8)),
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Max.Y = gtx.Dp(20) // a row, not a touch target
					s := material.Slider(a.th, &a.extraColsFloat)
					s.Color = colAccent
					return s.Layout(gtx)
				}),
				layout.Rigid(hSpacer(8)),
				layout.Rigid(a.body(strconv.Itoa(a.extraCols), colFg)),
			)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(hint, colMuted)),
	)
}

// wizardNextStep is step 2: the game's play rules, fixed at creation, one
// setting for every seat. A single radio picks the Guideline preset — every
// rule at the setting closest to the Tetris Guideline, listed read-only — or
// custom rules: the upcoming-piece preview count, the bag the pieces are
// dealt from, whether the hard-drop ghost shows, whether the hold queue is
// on, and (in the modes that raise garbage) how strong an attack is and how
// many holes every garbage row comes with, each row drawing its own or not.
func (a *App) wizardNextStep(gtx C) D {
	custom := a.rulesEnum.Value == "custom"
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.wizardRadio(&a.rulesEnum, "guideline", "Guideline — every rule at its Tetris Guideline setting")),
		layout.Rigid(a.wizardRadio(&a.rulesEnum, "custom", "Custom — set each rule yourself")),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			if custom {
				return a.wizardCustomRules(gtx)
			}
			return a.wizardGuidelineRules(gtx)
		}),
	)
}

// wizardGuidelineRules is the read-only view of the Guideline preset
// (config.GuidelineRules) for the game type being created: one line per
// rule, so the creator sees exactly what the game will play by.
func (a *App) wizardGuidelineRules(gtx C) D {
	mode := a.wizardMode()
	kids := []layout.FlexChild{
		layout.Rigid(a.body("The settings closest to the Tetris Guideline this game can offer:", colMuted)),
		layout.Rigid(spacer(8)),
	}
	for _, row := range guidelineSummary(mode) {
		label, value := row[0], row[1]
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Start}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Dp(120)
						return a.body(label, colAccent)(gtx)
					}),
					layout.Flexed(1, a.body(value, colFg)),
				)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// guidelineSummary lists the Guideline preset's rules as (rule, setting)
// pairs for a game of mode — the garbage rules only for the modes that raise
// garbage.
func guidelineSummary(mode config.GameMode) [][2]string {
	r := config.GuidelineRules().Normalized(mode)
	rows := [][2]string{
		{"Next pieces", fmt.Sprintf("%d — the NEXT well, and how far agents may look ahead", r.NextCount)},
		{"Ghost piece", "on — the landing preview of every player's piece"},
		{"Hold", "on — C or the HOLD button sets the falling piece aside for later, once per piece"},
		{"Piece bag", "the 7-bag — every seven pieces are the seven types, shuffled"},
		{"Hidden rows", "hidden — the board is the twenty-row playfield, nothing above it"},
	}
	if mode != config.ModeCooperative {
		rows = append(rows,
			[2]string{"Garbage", fmt.Sprintf("%d hole per row, the rows of one attack lined up into a well", r.GarbageHoles)},
			[2]string{"Attacks", "the Guideline table — a single sends nothing, a double 1 row, a triple 2, a Tetris 4"},
		)
	}
	return rows
}

// wizardCustomRules is the custom half of step 2: every rule as its own
// editor or checkbox, garbage rules for the modes that raise garbage only.
func (a *App) wizardCustomRules(gtx C) D {
	garbage := a.modeEnum.Value != "cooperative"
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body(fmt.Sprintf("Next pieces (0–%d):", config.MaxNextCount), colMuted)),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = gtx.Dp(40)
					gtx.Constraints.Min.X = gtx.Dp(40)
					return a.editorBox(gtx, &a.nextCountEd, strconv.Itoa(config.GuidelineRules().NextCount))
				}),
			)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body("The NEXT well every player sees — and exactly how far agents may look ahead. 0 hides it: nobody sees what's coming.", colMuted)),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.body("Piece bag:", colMuted)),
		layout.Rigid(a.wizardRadio(&a.bagEnum, "single", "7-bag — every seven pieces are the seven types, shuffled")),
		layout.Rigid(a.wizardRadio(&a.bagEnum, "double", "Double bag — every fourteen pieces are two of each type, shuffled together")),
		layout.Rigid(a.wizardRadio(&a.bagEnum, "none", "No bag — every piece is a fresh random draw")),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(bagHint(a.wizardBag()), colMuted)),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			cb := material.CheckBox(a.th, &a.ghostCb, "Show ghost piece")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body("Previews where each player's piece would hard-drop. Off, everyone eyeballs their drops.", colMuted)),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			cb := material.CheckBox(a.th, &a.holdCb, "Hold piece")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body("The Guideline hold: C or the HOLD button sets the falling piece aside and plays the next one, or swaps it back in later — once per piece.", colMuted)),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			cb := material.CheckBox(a.th, &a.headroomCb, "Show hidden rows")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(fmt.Sprintf("The %d rows above the playfield, where every piece appears, drawn behind smoked glass on every board: see a piece the moment it spawns and a stack about to top out. Off, the board is the playfield alone.", config.HeadroomRows), colMuted)),
		layout.Rigid(func(gtx C) D {
			if !garbage {
				return D{}
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(spacer(10)),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.body(fmt.Sprintf("Garbage holes (0–%d):", config.MaxGarbageHoles), colMuted)),
						layout.Rigid(hSpacer(6)),
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Max.X = gtx.Dp(40)
							gtx.Constraints.Min.X = gtx.Dp(40)
							return a.editorBox(gtx, &a.holesEd, strconv.Itoa(config.GuidelineRules().GarbageHoles))
						}),
					)
				}),
				layout.Rigid(spacer(4)),
				layout.Rigid(a.body("Empty cells in every garbage row an attack sends. 0 raises solid rows that never clear; a holed row clears like any line once its holes are filled.", colMuted)),
				layout.Rigid(spacer(10)),
				layout.Rigid(func(gtx C) D {
					cb := material.CheckBox(a.th, &a.randomHolesCb, "Random hole positions")
					cb.Color = colFg
					cb.IconColor = colAccent
					return cb.Layout(gtx)
				}),
				layout.Rigid(spacer(4)),
				layout.Rigid(a.body("Off: the rows of one attack share their hole columns, lining up into a well. On: every row draws its own — harder to dig out.", colMuted)),
				layout.Rigid(spacer(10)),
				layout.Rigid(func(gtx C) D {
					cb := material.CheckBox(a.th, &a.guidelineCb, "Guideline garbage")
					cb.Color = colFg
					cb.IconColor = colAccent
					return cb.Layout(gtx)
				}),
				layout.Rigid(spacer(4)),
				layout.Rigid(a.body("Off: every cleared line sends one garbage row. On: the Guideline table — a single sends nothing, a double 1 row, a triple 2, a Tetris 4.", colMuted)),
			)
		}),
	)
}

// wizardJoinStep is step 3: open game or invite-only.
func (a *App) wizardJoinStep(gtx C) D {
	hint := "Anyone in the lobby can take a seat. Next you can decide whether agents may join too."
	if a.createJoinEnum.Value == "invite" {
		hint = "Next you'll pick the players to invite; the game starts once every seat is filled and ready."
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.wizardRadio(&a.createJoinEnum, "invite", "Invite only — you choose who gets invited")),
		layout.Rigid(a.wizardRadio(&a.createJoinEnum, "open", "Open game — anyone in the lobby can join")),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.body(hint, colMuted)),
	)
}

// wizardAgentsStep is step 4 (open games only): whether idle agent
// players may take seats, and at most how many.
func (a *App) wizardAgentsStep(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			cb := material.CheckBox(a.th, &a.allowAgentsCb, "Allow agents to join")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(func(gtx C) D {
			if !a.allowAgentsCb.Value {
				return a.body("Agents will not take seats in this game.", colMuted)(gtx)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(a.body("Max agents:", colMuted)),
						layout.Rigid(hSpacer(6)),
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Max.X = gtx.Dp(40)
							gtx.Constraints.Min.X = gtx.Dp(40)
							return a.editorBox(gtx, &a.maxAgentsEd, "1")
						}),
					)
				}),
				layout.Rigid(spacer(8)),
				layout.Rigid(a.body("Idle agent players may take up to this many seats (capped at the game's seat count).", colMuted)),
			)
		}),
	)
}
