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

// The create-game wizard: three steps, one choice at a time — the game's
// name and type (a single playfield everyone shares, scored together or each
// on their own, or several playfields with a team on each), its rules (how
// long it runs, and the Guideline preset or every rule by hand), and its
// players (by invitation, or open to anyone at any time, agents included).
// Every step reads into one config.GameSpec (wizardSpec), which the create
// paths normalize and store.
const (
	wizStepType    = 1 // game type: one playfield or several, and the seats
	wizStepRules   = 2 // the game's length, and the play rules: the Guideline preset, or custom
	wizStepPlayers = 3 // invite-only or open (with the agent policy)
	wizStepCount   = 3
)

// handleCreateWizard drives the create-game wizard each frame: Cancel closes
// it, Back returns one step, and Next advances — on the last step it reads
// the widgets and launches the game. Returns true while the wizard is open
// (so the caller draws the modal and suppresses background clicks).
func (a *App) handleCreateWizard(gtx C) bool {
	if a.createWizStep == 0 {
		a.wizNameErr = ""
		return false
	}
	if a.wizCancelBtn.Clicked(gtx) {
		a.createWizStep = 0
		return false
	}
	// A name this game cannot have — one another game holds, or the lobby's
	// own — is read once a frame here, both to say so under the editor and
	// to hold the wizard on the step until the creator picks another.
	a.wizNameErr = a.wizardNameErr(a.wizardName())
	if a.wizBackBtn.Clicked(gtx) && a.createWizStep > wizStepType {
		a.createWizStep--
	}
	if a.wizNextBtn.Clicked(gtx) && a.wizNameErr == "" {
		if a.createWizStep < wizStepPlayers {
			a.createWizStep++
		} else {
			a.finishCreateWizard()
		}
	}
	return a.createWizStep != 0
}

// wizardName reads step 1's name editor as the game would take it: what a
// game called this would be named, and be identified by everywhere
// (config.GameName). Blank — nothing typed, or nothing usable typed — is a
// game that goes by a generated ID.
func (a *App) wizardName() string {
	return config.GameName(a.gameNameEd.Text())
}

// wizardNameErr is why a game cannot be called name, in the words the
// creator reads under the editor: the lobby keeps "lobby" for its own chat
// and voice, and a name another game already holds is not free — a name is
// the game's ID, so no two games share one. Blank means the name is this
// game's for the taking. (The check is the lobby as this client sees it; the
// create itself is the arbiter, and says the same thing if it loses a race.)
func (a *App) wizardNameErr(name string) string {
	if name == "" {
		return ""
	}
	if config.GameNameReserved(name) {
		return fmt.Sprintf("%q is the lobby's own name — pick another.", name)
	}
	lb := a.getLobby()
	if lb == nil {
		return ""
	}
	if _, taken := lb.Games()[name]; taken {
		return fmt.Sprintf("A game called %s is already on the list — pick another name.", name)
	}
	return ""
}

// wizardMultiple reports whether step 1 has several playfields picked.
func (a *App) wizardMultiple() bool { return a.boardsEnum.Value == "multiple" }

// wizardIndividual reports whether step 1 has the single playfield scored
// per seat (its "Competitive" kind) — a choice only a board with company
// offers: one player on one playfield is a solo game, scored as itself.
func (a *App) wizardIndividual() bool {
	return !a.wizardMultiple() && a.wizardCount() > 1 && a.singleKindEnum.Value == "competitive"
}

// wizardCount reads step 1's seat-count editor: players PER PLAYFIELD when
// there are several, the total player count on a single one. Blank, junk or
// less than one falls back to one — a solo run on a single playfield, a
// board of their own per playfield — so the step's live readouts and the
// create itself always agree.
func (a *App) wizardCount() int {
	count, err := strconv.Atoi(strings.TrimSpace(a.countEd.Text()))
	if err != nil || count < 1 {
		return 1
	}
	return count
}

// wizardTeamNames reads step 3's team-name editors for n teams: each trimmed
// and capped (config.NormalizeTeamNames), a blank one the team's default
// piece colour.
func (a *App) wizardTeamNames(n int) []string {
	names := make([]string, 0, n)
	for t := 0; t < n && t < len(a.teamNameEds); t++ {
		names = append(names, a.teamNameEds[t].Text())
	}
	return config.NormalizeTeamNames(names, n)
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

// extraRowsRange maps the board-height slider's 0..1 position to the whole
// number of rows one seat adds, and back to that row's detent.
var extraRowsRange = knobRange{config.MinExtraRows, config.MaxExtraRows, 1}

// setExtraRows sets the board-height knob — how many rows a shared board
// gains per seat beyond the first — and mirrors it onto its slider.
func (a *App) setExtraRows(v int) {
	a.extraRows = config.ExtraRowsPerPlayer(v)
	a.extraRowsFloat.Value = extraRowsRange.pos(a.extraRows)
}

// playfieldsRange maps the playfield-count slider's 0..1 position to the
// whole number of playfields a multi-playfield game has, and back.
var playfieldsRange = knobRange{config.MinTeamCount, config.MaxTeamCount, 1}

// setPlayfieldCount sets the playfield-count knob — how many boards a
// multi-playfield game is played on — and mirrors it onto its slider.
func (a *App) setPlayfieldCount(v int) {
	a.playfieldCount = config.NormalizeTeamCount(v)
	a.playfieldsFloat.Value = playfieldsRange.pos(a.playfieldCount)
}

// wizardPlayfieldCount reads step 1's playfield-count knob: how many boards
// a multi-playfield game is played on, and 1 for a single playfield.
func (a *App) wizardPlayfieldCount() int {
	if !a.wizardMultiple() {
		return 1
	}
	return config.NormalizeTeamCount(a.playfieldCount)
}

// wizardLineGoal reads step 2's game-length choice: 0 for "until top out",
// else the lines editor's number — blank or junk is the classic forty.
func (a *App) wizardLineGoal() int {
	if a.lengthEnum.Value != "lines" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(a.lineGoalEd.Text()))
	if err != nil || n < 1 {
		n = config.DefaultLineGoal
	}
	return config.NormalizeLineGoal(n)
}

// wizardSpec reads every step into the game's creation spec, normalized —
// the one read-out the create paths, the step readouts and the tests all
// share. The name is passed as typed and cut by the normalization
// (config.GameName): what comes out is the game's name AND its ID, or
// nothing at all, in which case the game is dealt a generated one. The game types map onto the modes like this: a single playfield is
// a cooperative-mode board (scored together, or per seat — Scoring), several
// playfields with one player each are competitive (a board each, the last
// standing wins), with more a teams game. The Guideline preset keeps the
// board's growth and the deal at their defaults; custom rules set them.
func (a *App) wizardSpec() config.GameSpec {
	spec := config.GameSpec{
		Name:       a.gameNameEd.Text(),
		LineGoal:   a.wizardLineGoal(),
		InviteOnly: a.createJoinEnum.Value == "invite",
	}
	count := a.wizardCount()
	if a.wizardMultiple() {
		n := a.wizardPlayfieldCount()
		if count == 1 {
			spec.Mode, spec.PlayerCount = config.ModeCompetitive, n
		} else {
			spec.Mode, spec.TeamCount, spec.TeamSize, spec.PlayerCount = config.ModeTeams, n, count, n*count
			spec.TeamNames = a.wizardTeamNames(n)
		}
	} else {
		spec.Mode, spec.PlayerCount = config.ModeCooperative, count
		if a.wizardIndividual() {
			spec.Scoring = config.ScoringIndividual
		}
	}
	if a.rulesEnum.Value == "custom" {
		spec.Rules = a.customRules()
		spec.ExtraColumns, spec.ExtraRows, spec.SplitPieces = a.extraCols, a.extraRows, a.splitPiecesCb.Value
	} else {
		spec.Rules = config.GuidelineRules()
		spec.ExtraColumns, spec.ExtraRows, spec.SplitPieces = config.DefaultExtraColumns, config.DefaultExtraRows, false
	}
	// Agent policy (open games): how many seats idle agent players may take
	// — on each team of a teams game, in the whole game elsewhere — and
	// whether an agent left as the only player waits for company. Unchecked
	// = 0 = agents may not join; the count is clamped to the seats it counts
	// over by the normalization. An invite-only game's policy is per
	// invitation.
	if !spec.InviteOnly && a.allowAgentsCb.Value {
		n, err := strconv.Atoi(strings.TrimSpace(a.maxAgentsEd.Text()))
		if err != nil || n < 1 {
			n = 1
		}
		spec.MaxAgents = n
		spec.AgentsPauseAlone = a.pauseAgentsCb.Value
	}
	return spec.Normalized()
}

// finishCreateWizard reads the wizard's widgets into the spec, closes the
// wizard, and launches the game: an invite-only game is created and hands
// off to the invitee picker, an open game is created directly.
func (a *App) finishCreateWizard() {
	spec := a.wizardSpec()
	a.createWizStep = 0
	if spec.InviteOnly {
		go a.openInvitePicker(spec)
		return
	}
	go func() { a.createGame(spec) }()
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
// empty cells every garbage row is raised with in the games that raise
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
	stepTitle := ""
	var body layout.Widget
	switch step {
	case wizStepType:
		stepTitle = "GAME TYPE"
		body = a.wizardTypeStep
	case wizStepRules:
		stepTitle = "GAME RULES"
		body = a.wizardRulesStep
	default:
		stepTitle = "PLAYERS"
		body = a.wizardPlayersStep
	}
	nextLabel := "Next"
	if step == wizStepPlayers {
		nextLabel = "Create game"
		if a.createJoinEnum.Value == "invite" {
			nextLabel = "Choose players…"
		}
	}

	// The body scrolls when the step is taller than the window leaves it
	// (the custom rules step at the minimum window height): the window
	// height less the modal's header, footer and chrome is what it may take
	// before scrolling.
	bodyMaxY := max(gtx.Constraints.Max.Y-gtx.Dp(200), gtx.Dp(120))
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = modalW(gtx, 480)
		return a.tutMark(gtx, tutWizard, func(gtx C) D {
			return a.createWizardBox(gtx, step, wizStepCount, stepTitle, nextLabel, bodyMaxY, body)
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
									if step == wizStepType {
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

// wizardSubRadio is a wizardRadio indented under the choice it refines.
func (a *App) wizardSubRadio(enum *widget.Enum, value, label string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Left: unit.Dp(24)}.Layout(gtx, a.wizardRadio(enum, value, label))
	}
}

// wizardCheckBox is one wizard setting: a checkbox and, under it, the line
// that says what it does in the terms a player feels it.
func (a *App) wizardCheckBox(cb *widget.Bool, label, hint string) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				c := material.CheckBox(a.th, cb, label)
				c.Color = colFg
				c.IconColor = colAccent
				return c.Layout(gtx)
			}),
			layout.Rigid(spacer(4)),
			layout.Rigid(a.body(hint, colMuted)),
		)
	}
}

// wizardSlider is one wizard knob: its label, the slider snapped to the
// range's whole-number detents (the value mirrored into v), the value beside
// it, and under them the line that says what the value means.
func (a *App) wizardSlider(label string, f *widget.Float, rng knobRange, v *int, hint string) layout.Widget {
	return func(gtx C) D {
		if f.Update(gtx) {
			*v = rng.value(f.Value)
			f.Value = rng.pos(*v) // rest on the detent
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.body(label, colMuted)),
					layout.Rigid(hSpacer(8)),
					layout.Flexed(1, func(gtx C) D {
						gtx.Constraints.Max.Y = gtx.Dp(20) // a row, not a touch target
						s := material.Slider(a.th, f)
						s.Color = colAccent
						return s.Layout(gtx)
					}),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(a.body(strconv.Itoa(*v), colFg)),
				)
			}),
			layout.Rigid(spacer(4)),
			layout.Rigid(a.body(hint, colMuted)),
		)
	}
}

// wizardNumber is one wizard number: its label and a short editor beside it,
// the hint what a blank editor means.
func (a *App) wizardNumber(label string, ed *widget.Editor, hint string, note layout.Widget) layout.Widget {
	return func(gtx C) D {
		kids := []layout.FlexChild{
			layout.Rigid(a.body(label, colMuted)),
			layout.Rigid(hSpacer(6)),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Max.X = gtx.Dp(48)
				gtx.Constraints.Min.X = gtx.Dp(48)
				return a.editorBox(gtx, ed, hint)
			}),
		}
		if note != nil {
			kids = append(kids, layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, note)
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
}

// wizardTypeStep is step 1: what the game is called, then one playfield
// everyone shares — how many players, and with company whether they score
// together (co-op) or each seat on its own (competitive); one player alone
// is a solo game — or several playfields, a team on each: how many
// playfields and how many players on each.
func (a *App) wizardTypeStep(gtx C) D {
	multiple := a.wizardMultiple()
	spec := a.wizardSpec()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.wizardNameField),
		layout.Rigid(spacer(14)),
		layout.Rigid(a.wizardRadio(&a.boardsEnum, "single", "Single playfield — everyone plays one shared board")),
		layout.Rigid(func(gtx C) D {
			if multiple {
				return D{}
			}
			return layout.Inset{Left: unit.Dp(24)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(spacer(4)),
					layout.Rigid(a.wizardNumber("Players:", &a.countEd, "1", func(gtx C) D {
						if spec.PlayerCount != 1 {
							return D{}
						}
						// A crew of one: the shared board and score are
						// theirs alone, and the game is a run at the solo
						// high score — nothing to score together or apart.
						return a.body("(solo — you play for the high score)", colMuted)(gtx)
					})),
					layout.Rigid(func(gtx C) D {
						if spec.PlayerCount < 2 {
							return D{}
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(spacer(4)),
							layout.Rigid(a.wizardRadio(&a.singleKindEnum, "coop", "Co-op — all players score together")),
							layout.Rigid(a.wizardRadio(&a.singleKindEnum, "competitive", "Competitive — each player scored individually, the top score wins")),
						)
					}),
				)
			})
		}),
		layout.Rigid(spacer(6)),
		layout.Rigid(a.wizardRadio(&a.boardsEnum, "multiple", "Multiple playfields — a team on each, every team scores together")),
		layout.Rigid(func(gtx C) D {
			if !multiple {
				return D{}
			}
			n := a.wizardPlayfieldCount()
			names := make([]string, n)
			for t := range names {
				names[t] = config.TeamLetter(t)
			}
			hint := fmt.Sprintf("Team %s — each on a board of its own, each attacking the others in turn. The last playfield standing wins.", strings.Join(names, " vs Team "))
			return layout.Inset{Left: unit.Dp(24)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(spacer(6)),
					layout.Rigid(a.wizardSlider(fmt.Sprintf("Playfields (%d–%d):", config.MinTeamCount, config.MaxTeamCount), &a.playfieldsFloat, playfieldsRange, &a.playfieldCount, hint)),
					layout.Rigid(spacer(10)),
					layout.Rigid(a.wizardNumber("Players per playfield:", &a.countEd, "1", func(gtx C) D {
						note := fmt.Sprintf("(%d seats: %d playfields × %d)", spec.PlayerCount, spec.Playfields(), spec.SeatsPerPlayfield())
						if spec.Mode == config.ModeCompetitive {
							note = "(a board each — the last player standing wins)"
						}
						return a.body(note, colMuted)(gtx)
					})),
				)
			})
		}),
	)
}

// wizardNameField is step 1's name: the editor, and under it what the name
// does — the game's ID, so the lobby lists it by name, its link carries the
// name and its stream is JETRIS_GAME_<name>. What a game called this would
// actually be named is spelt out as it is typed (config.GameName cuts a name
// to what a stream name, a subject and a KV key all take), and a name this
// game cannot have (wizardNameErr) is said in red — the wizard holds the
// step until it is changed.
func (a *App) wizardNameField(gtx C) D {
	name := a.wizardName()
	hint, col := "Unnamed: the game is listed and shared by an ID of its own.", colMuted
	if name != "" {
		hint = fmt.Sprintf("The game is called %s: the lobby lists it by that name, and its stream is %s.", name, config.GameStream(name))
	}
	if a.wizNameErr != "" {
		hint, col = a.wizNameErr, colErr
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.body("Name:", colMuted)),
				layout.Rigid(hSpacer(6)),
				layout.Flexed(1, func(gtx C) D {
					return a.editorBox(gtx, &a.gameNameEd, "optional")
				}),
			)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(hint, col)),
	)
}

// wizardRulesStep is step 2: how long the game runs — until someone tops
// out, or until a playfield has cleared a number of lines — and the play
// rules, fixed at creation, one setting for every seat: a single radio
// picks the Guideline preset (every rule at the setting closest to the
// Guideline, listed read-only) or custom rules, each its own control.
func (a *App) wizardRulesStep(gtx C) D {
	custom := a.rulesEnum.Value == "custom"
	spec := a.wizardSpec()
	lengthHint := "The game runs until a playfield tops out."
	if spec.LineGoal > 0 {
		switch {
		case spec.Playfields() > 1:
			lengthHint = fmt.Sprintf("The first playfield to clear %d lines wins.", spec.LineGoal)
		case spec.IndividualScoring():
			lengthHint = fmt.Sprintf("The game ends once the board has cleared %d lines; the top score wins.", spec.LineGoal)
		default:
			lengthHint = fmt.Sprintf("The game ends once the crew has cleared %d lines.", spec.LineGoal)
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.body("Game length:", colMuted)),
		layout.Rigid(a.wizardRadio(&a.lengthEnum, "topout", "Until top out")),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(a.wizardRadio(&a.lengthEnum, "lines", "A number of lines:")),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = gtx.Dp(56)
					gtx.Constraints.Min.X = gtx.Dp(56)
					return a.editorBox(gtx, &a.lineGoalEd, strconv.Itoa(config.DefaultLineGoal))
				}),
			)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(lengthHint, colMuted)),
		layout.Rigid(spacer(12)),
		layout.Rigid(a.wizardRadio(&a.rulesEnum, "guideline", "Guideline — every rule at its Guideline setting")),
		layout.Rigid(a.wizardRadio(&a.rulesEnum, "custom", "Custom — set each rule yourself")),
		layout.Rigid(spacer(10)),
		layout.Rigid(func(gtx C) D {
			if custom {
				return a.wizardCustomRules(gtx, spec)
			}
			return a.wizardGuidelineRules(gtx, spec)
		}),
	)
}

// wizardGuidelineRules is the read-only view of the Guideline preset
// (config.GuidelineRules) for the game being created: one line per rule, so
// the creator sees exactly what the game will play by.
func (a *App) wizardGuidelineRules(gtx C, spec config.GameSpec) D {
	kids := []layout.FlexChild{
		layout.Rigid(a.body("The settings closest to the Guideline this game can offer:", colMuted)),
		layout.Rigid(spacer(8)),
	}
	for _, row := range guidelineSummary(spec) {
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
// pairs for the game being created — the board and the deal for a shared
// playfield with company, the garbage rules only where several playfields
// raise garbage at each other.
func guidelineSummary(spec config.GameSpec) [][2]string {
	r := config.GuidelineRules().Normalized(spec.Mode)
	rows := [][2]string{
		{"Next pieces", fmt.Sprintf("%d — the NEXT well, and how far agents may look ahead", r.NextCount)},
		{"Ghost piece", "on — the landing preview of every player's piece"},
		{"Hold", "on — C or the HOLD button sets the falling piece aside for later, once per piece"},
		{"Piece bag", "the 7-bag — every seven pieces are the seven types, shuffled"},
		{"Hidden rows", "hidden — the board is the twenty-row playfield, nothing above it"},
	}
	if seats := spec.SeatsPerPlayfield(); seats > 1 {
		rows = append(rows,
			[2]string{"Board", fmt.Sprintf("%d columns +%d per player, %d rows — %d×%d for %d players",
				config.StandardWidth, config.DefaultExtraColumns, config.VisibleRows,
				config.SharedBoardWidth(seats, config.DefaultExtraColumns), config.VisibleRows, seats)},
			[2]string{"Pieces", fmt.Sprintf("the full bag for each of the %d players of a playfield — not dealt out between them", seats)},
		)
	}
	if spec.Playfields() > 1 {
		rows = append(rows,
			[2]string{"Garbage", fmt.Sprintf("%d hole per row, the rows of one attack lined up into a well", r.GarbageHoles)},
			[2]string{"Attacks", "the Guideline table — a single sends nothing, a double 1 row, a triple 2, a Jetris 4"},
		)
	}
	return rows
}

// wizardCustomRules is the custom half of step 2: every rule as its own
// control — the shared board's growth per player and the deal where a
// playfield has company, the garbage rules where several playfields raise
// garbage at each other.
func (a *App) wizardCustomRules(gtx C, spec config.GameSpec) D {
	seats := spec.SeatsPerPlayfield()
	garbage := spec.Playfields() > 1
	kids := []layout.FlexChild{}
	if seats > 1 {
		colsHint := fmt.Sprintf("The shared board is %d columns for the first player and %d more for every player after — %d columns for %d players.",
			config.StandardWidth, a.extraCols, config.SharedBoardWidth(seats, a.extraCols), seats)
		rowsHint := fmt.Sprintf("The playfield is %d rows for the first player and %d more for every player after — %d rows for %d players.",
			config.VisibleRows, a.extraRows, config.VisibleRows+(seats-1)*a.extraRows, seats)
		splitHint := fmt.Sprintf("The seven piece types are dealt out between the %d players of a playfield — every type to somebody, at least one type each — and each of them only ever plays their own. Every playfield is dealt the same hands.", seats)
		if !a.splitPiecesCb.Value {
			splitHint = "Off: every player plays the same full bag, as always."
		}
		kids = append(kids,
			layout.Rigid(a.wizardSlider(fmt.Sprintf("Extra columns per player (%d–%d):", config.MinExtraColumns, config.MaxExtraColumns), &a.extraColsFloat, extraColsRange, &a.extraCols, colsHint)),
			layout.Rigid(spacer(10)),
			layout.Rigid(a.wizardSlider(fmt.Sprintf("Extra rows per player (%d–%d):", config.MinExtraRows, config.MaxExtraRows), &a.extraRowsFloat, extraRowsRange, &a.extraRows, rowsHint)),
			layout.Rigid(spacer(10)),
			layout.Rigid(a.wizardCheckBox(&a.splitPiecesCb, "Distribute the pieces between the players of a playfield", splitHint)),
			layout.Rigid(spacer(10)),
		)
	}
	kids = append(kids,
		layout.Rigid(a.wizardNumber(fmt.Sprintf("Next pieces (0–%d):", config.MaxNextCount), &a.nextCountEd, strconv.Itoa(config.GuidelineRules().NextCount), nil)),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body("The NEXT well every player sees — and exactly how far agents may look ahead. 0 hides it: nobody sees what's coming.", colMuted)),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.wizardCheckBox(&a.holdCb, "Hold piece", "The Guideline hold: C or the HOLD button sets the falling piece aside and plays the next one, or swaps it back in later — once per piece.")),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.wizardCheckBox(&a.ghostCb, "Show ghost piece", "Previews where each player's piece would hard-drop. Off, everyone eyeballs their drops.")),
	)
	if garbage {
		kids = append(kids,
			layout.Rigid(spacer(10)),
			layout.Rigid(a.wizardCheckBox(&a.guidelineCb, "Guideline garbage", "Off: every cleared line sends one garbage row. On: the Guideline table — a single sends nothing, a double 1 row, a triple 2, a Jetris 4.")),
			layout.Rigid(spacer(10)),
			layout.Rigid(a.wizardNumber(fmt.Sprintf("Garbage holes (0–%d):", config.MaxGarbageHoles), &a.holesEd, strconv.Itoa(config.GuidelineRules().GarbageHoles), nil)),
			layout.Rigid(spacer(4)),
			layout.Rigid(a.body("Empty cells in every garbage row an attack sends. 0 raises solid rows that never clear; a holed row clears like any line once its holes are filled.", colMuted)),
			layout.Rigid(spacer(10)),
			layout.Rigid(a.wizardCheckBox(&a.randomHolesCb, "Random hole positions", "Off: the rows of one attack share their hole columns, lining up into a well. On: every row draws its own — harder to dig out.")),
		)
	}
	kids = append(kids,
		layout.Rigid(spacer(10)),
		layout.Rigid(a.body("Piece bag:", colMuted)),
		layout.Rigid(a.wizardRadio(&a.bagEnum, "single", "7-bag — every seven pieces are the seven types, shuffled")),
		layout.Rigid(a.wizardRadio(&a.bagEnum, "double", "Double bag — every fourteen pieces are two of each type, shuffled together")),
		layout.Rigid(a.wizardRadio(&a.bagEnum, "none", "No bag — every piece is a fresh random draw")),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(bagHint(a.wizardBag()), colMuted)),
		layout.Rigid(spacer(10)),
		layout.Rigid(a.wizardCheckBox(&a.headroomCb, "Show hidden rows", fmt.Sprintf("The %d rows above the playfield, where every piece appears, drawn behind smoked glass on every board: see a piece the moment it spawns and a stack about to top out. Off, the board is the playfield alone.", config.HeadroomRows))),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// wizardPlayersStep is step 3: for a multi-playfield game with teams, what
// each team is called (the piece colours unless renamed); then invite-only,
// or open — and for an open game, whether idle agent players may take seats,
// at most how many (on each team, where there are teams), and whether an
// agent left as the only player waits for company.
func (a *App) wizardPlayersStep(gtx C) D {
	open := a.createJoinEnum.Value == "open"
	hint := "Next you'll pick the players to invite; the game starts once every seat is filled and ready."
	if open {
		hint = "Anyone in the lobby can take a free seat, and players can enter and leave the game at any time — before it starts and while it runs. The game starts as soon as every playfield has a player who is ready."
	}
	spec := a.wizardSpec()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			if spec.Mode != config.ModeTeams {
				return D{}
			}
			return a.wizardTeamNameEditors(gtx, spec)
		}),
		layout.Rigid(a.wizardRadio(&a.createJoinEnum, "invite", "Invite only — you choose who gets invited")),
		layout.Rigid(a.wizardRadio(&a.createJoinEnum, "open", "Open game — players can enter and leave at any time")),
		layout.Rigid(spacer(8)),
		layout.Rigid(a.body(hint, colMuted)),
		layout.Rigid(func(gtx C) D {
			if !open {
				return D{}
			}
			agentsHint := "Agents will not take seats in this game."
			maxLabel := "Max agents:"
			if a.allowAgentsCb.Value {
				agentsHint = "Idle agent players may take seats — up to the number below (capped at the game's seat count)."
				if spec.Mode == config.ModeTeams {
					agentsHint = "Idle agent players may take seats — up to the number below on each team (capped at the players per team)."
					maxLabel = "Max agents per team:"
				}
			}
			pauseHint := "An agent left as the only player in the game plays on by itself."
			if a.pauseAgentsCb.Value {
				pauseHint = "An agent left as the only player in the game stops playing until someone joins it."
			}
			return layout.Inset{Left: unit.Dp(24)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(spacer(12)),
					layout.Rigid(a.wizardCheckBox(&a.allowAgentsCb, "Allow agents to join", agentsHint)),
					layout.Rigid(func(gtx C) D {
						if !a.allowAgentsCb.Value {
							return D{}
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(spacer(8)),
							layout.Rigid(a.wizardNumber(maxLabel, &a.maxAgentsEd, "1", nil)),
							layout.Rigid(spacer(8)),
							layout.Rigid(a.wizardCheckBox(&a.pauseAgentsCb, "Agents pause when alone", pauseHint)),
						)
					}),
				)
			})
		}),
	)
}

// wizardTeamNameEditors is step 3's team-name editors, one per playfield of
// a teams game: each team is called after a piece colour — Cyan, Yellow,
// Purple, Green, Red, Blue — unless the creator types another name (up to
// config.MaxTeamNameLen letters; a blank one keeps the colour).
func (a *App) wizardTeamNameEditors(gtx C, spec config.GameSpec) D {
	n := spec.TeamCount
	kids := []layout.FlexChild{
		layout.Rigid(a.body("Team names:", colMuted)),
		layout.Rigid(spacer(4)),
	}
	defaults := config.DefaultTeamNames(n)
	for t := 0; t < n && t < len(a.teamNameEds); t++ {
		t := t
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Dp(90)
						return a.body(fmt.Sprintf("Playfield %d:", t+1), colMuted)(gtx)
					}),
					layout.Rigid(hSpacer(6)),
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Max.X = gtx.Dp(160)
						gtx.Constraints.Min.X = gtx.Dp(160)
						return a.editorBox(gtx, &a.teamNameEds[t], defaults[t])
					}),
				)
			})
		}))
	}
	kids = append(kids,
		layout.Rigid(spacer(4)),
		layout.Rigid(a.body(fmt.Sprintf("Each playfield's team, named after a piece colour unless you rename it (%d letters at most).", config.MaxTeamNameLen), colMuted)),
		layout.Rigid(spacer(12)),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}
