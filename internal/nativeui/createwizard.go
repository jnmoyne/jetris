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
	wizStepNext   = 2 // next-piece preview count
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

// finishCreateWizard reads the wizard's widgets, clamps them to legal values,
// closes the wizard, and launches the game: an invite-only game is created and
// hands off to the invitee picker, an open game is created directly with its
// agent policy.
func (a *App) finishCreateWizard() {
	mode := config.ModeCooperative
	switch a.modeEnum.Value {
	case "competitive":
		mode = config.ModeCompetitive
	case "teams":
		mode = config.ModeTeams
	}
	count, err := strconv.Atoi(strings.TrimSpace(a.countEd.Text()))
	if mode == config.ModeTeams {
		// For teams the count editor means players PER TEAM.
		if err != nil || count < 1 {
			count = 1
		}
	} else if err != nil || count < 2 {
		count = 2
	}
	// Upcoming-piece preview: how many next pieces the game reveals to
	// everyone (players, spectators, agents). Blank or junk falls back to
	// the default of 1; clamped to 0..config.MaxNextCount.
	nextCount, err := strconv.Atoi(strings.TrimSpace(a.nextCountEd.Text()))
	if err != nil {
		nextCount = 1
	}
	if nextCount < 0 {
		nextCount = 0
	}
	if nextCount > config.MaxNextCount {
		nextCount = config.MaxNextCount
	}
	a.createWizStep = 0
	if a.createJoinEnum.Value == "invite" {
		go a.openInvitePicker(mode, count, nextCount)
		return
	}
	// Agent policy: how many seats idle jetris-agent players may take.
	// Unchecked = 0 = agents may not join. Clamped to the game's total
	// player count (the count editor is per-team in teams mode).
	maxAgents := 0
	if a.allowAgentsCb.Value {
		total := count
		if mode == config.ModeTeams {
			total = config.TeamCount * count
		}
		maxAgents, err = strconv.Atoi(strings.TrimSpace(a.maxAgentsEd.Text()))
		if err != nil || maxAgents < 1 {
			maxAgents = 1
		}
		if maxAgents > total {
			maxAgents = total
		}
	}
	go func() { a.createGame(mode, count, maxAgents, nextCount, false) }()
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
		stepTitle = "PIECE PREVIEW"
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

	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(480)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), "CREATE A NEW GAME", colFg).Layout),
							layout.Rigid(spacer(6)),
							layout.Rigid(a.pixel(unit.Sp(9), fmt.Sprintf("STEP %d OF %d — %s", step, total, stepTitle), colAccent).Layout),
							layout.Rigid(spacer(14)),
							layout.Rigid(body),
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

// wizardModeStep is step 1: pick the game type and how many players it seats
// (players per team in teams mode).
func (a *App) wizardModeStep(gtx C) D {
	teams := a.modeEnum.Value == "teams"
	countLabel := "Players:"
	if teams {
		countLabel = "Players per team:"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.wizardRadio(&a.modeEnum, "cooperative", "Co-op — everyone plays one shared board, one shared score")),
		layout.Rigid(a.wizardRadio(&a.modeEnum, "competitive", "Competitive — own board each, last player standing wins")),
		layout.Rigid(a.wizardRadio(&a.modeEnum, "teams", "Teams — Team A vs Team B, each team a shared board")),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body(countLabel, colMuted)),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = gtx.Dp(48)
					gtx.Constraints.Min.X = gtx.Dp(48)
					return a.editorBox(gtx, &a.countEd, "2")
				}),
				layout.Rigid(func(gtx C) D {
					if !teams {
						return D{}
					}
					return layout.Inset{Left: unit.Dp(10)}.Layout(gtx,
						a.body(fmt.Sprintf("(total seats = %d × per team)", config.TeamCount), colMuted))
				}),
			)
		}),
	)
}

// wizardNextStep is step 2: how many upcoming pieces the game reveals.
func (a *App) wizardNextStep(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.body("How many upcoming pieces the game reveals — the NEXT panel every player sees, and exactly how far agents may look ahead.", colMuted)),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body(fmt.Sprintf("Next pieces (0–%d):", config.MaxNextCount), colMuted)),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = gtx.Dp(40)
					gtx.Constraints.Min.X = gtx.Dp(40)
					return a.editorBox(gtx, &a.nextCountEd, "1")
				}),
			)
		}),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.body("0 hides the preview entirely — nobody (human or agent) sees what's coming.", colMuted)),
	)
}

// wizardJoinStep is step 3: open game or invite-only.
func (a *App) wizardJoinStep(gtx C) D {
	hint := "Anyone in the lobby can take a seat. Next you can decide whether agents may join too."
	if a.createJoinEnum.Value == "invite" {
		hint = "Next you'll pick the players to invite; the game starts once every seat is filled and ready."
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.wizardRadio(&a.createJoinEnum, "open", "Open game — anyone in the lobby can join")),
		layout.Rigid(a.wizardRadio(&a.createJoinEnum, "invite", "Invite only — you choose who gets invited")),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.body(hint, colMuted)),
	)
}

// wizardAgentsStep is step 4 (open games only): whether idle jetris-agent
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
				layout.Rigid(a.body("Idle jetris-agent players may take up to this many seats (capped at the game's seat count).", colMuted)),
			)
		}),
	)
}
