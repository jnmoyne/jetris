package nativeui

import (
	"context"
	"fmt"
	"sort"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetricks/internal/config"
	"jetricks/internal/lobby"
)

// inviteChoice is one selectable player in the invitee picker. For competitive
// and cooperative games `sel` is a plain include toggle; for teams `team`
// picks which team to invite them to ("" = not invited, "0" = A, "1" = B).
type inviteChoice struct {
	playerID string
	name     string
	agent    bool
	sel      widget.Bool
	team     widget.Enum
}

// openInvitePicker creates the invite-only game and opens the invitee picker
// over the lobby. The picker is populated with every OTHER player currently
// idle in the lobby (players already in a game cannot be invited). Runs off
// the UI goroutine (create does a NATS round trip).
func (a *App) openInvitePicker(mode config.GameMode, count int) {
	gameID := a.createGame(mode, count, 0, true) // invite-only: agent policy is per-invite
	if gameID == "" {
		return
	}
	lb := a.getLobby()
	if lb == nil {
		return
	}
	// Derive capacity from the same count the create used (teams count is
	// per-team, like createGame): playerCount and teamSize.
	playerCount, teamSize := count, 0
	if mode == config.ModeTeams {
		teamSize = count
		playerCount = config.TeamCount * count
	}
	picker := make(map[string]*inviteChoice)
	reconcileInvitePicker(picker, lb.Players(), lb.PlayerID())
	a.mu.Lock()
	a.invitePickerGameID = gameID
	a.invitePicker = picker
	a.invitePickerMode = mode
	a.invitePickerPC = playerCount
	a.invitePickerTS = teamSize
	a.invitePickerErr = ""
	a.mu.Unlock()
	a.invalidate()
}

// reconcileInvitePicker updates the candidate map in place against the live
// lobby presence: it adds every newly-eligible player (in the lobby, not in a
// game, not yourself), drops anyone who left or joined a game, and PRESERVES
// the selection widgets of players who remain. Pure so it can be tested.
func reconcileInvitePicker(picker map[string]*inviteChoice, live map[string]lobby.PlayerPresence, self string) {
	for id, p := range live {
		if id == self || p.Status != lobby.StatusInLobby {
			continue
		}
		if _, ok := picker[id]; !ok {
			picker[id] = &inviteChoice{playerID: id, name: p.Name, agent: p.Agent}
		}
	}
	for id := range picker {
		if p, ok := live[id]; !ok || p.Status != lobby.StatusInLobby {
			delete(picker, id)
		}
	}
}

// syncInvitePickerCandidates keeps the open picker's list reactive: called
// each frame while the overlay is up, it folds in players who joined the lobby
// and removes those who left or entered a game.
func (a *App) syncInvitePickerCandidates() {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	self, live := lb.PlayerID(), lb.Players()
	a.mu.Lock()
	if a.invitePicker != nil {
		reconcileInvitePicker(a.invitePicker, live, self)
	}
	a.mu.Unlock()
}

// handleInvitePicker dispatches the picker's Send/Cancel buttons. Returns true
// while the overlay is open (so the caller can suppress background clicks).
func (a *App) handleInvitePicker(gtx C) bool {
	a.mu.Lock()
	gameID := a.invitePickerGameID
	a.mu.Unlock()
	if gameID == "" {
		return false
	}
	a.syncInvitePickerCandidates() // reactive: add joiners, drop leavers

	a.mu.Lock()
	picker := a.invitePicker
	a.mu.Unlock()

	if a.inviteCancelBtn.Clicked(gtx) {
		// Abandon the game nobody was invited to yet.
		go a.deleteGame(gameID)
		a.closeInvitePicker()
		return true
	}
	if a.inviteSendBtn.Clicked(gtx) {
		a.mu.Lock()
		mode, pc, ts := a.invitePickerMode, a.invitePickerPC, a.invitePickerTS
		a.mu.Unlock()
		teams := mode == config.ModeTeams
		if msg := pickerCapacityError(picker, pc, ts, teams); msg != "" {
			a.mu.Lock()
			a.invitePickerErr = msg
			a.mu.Unlock()
			return true // keep the overlay open so the creator can fix the selection
		}
		type target struct {
			id   string
			team int
		}
		var targets []target
		for id, c := range picker {
			if teams {
				switch c.team.Value {
				case "0":
					targets = append(targets, target{id, 0})
				case "1":
					targets = append(targets, target{id, 1})
				}
			} else if c.sel.Value {
				targets = append(targets, target{id, 0})
			}
		}
		go func() {
			lb := a.getLobby()
			if lb == nil {
				return
			}
			for _, t := range targets {
				if err := lb.Invite(context.Background(), t.id, gameID, t.team); err != nil {
					logInvite(err)
				}
			}
		}()
		a.closeInvitePicker()
		return true
	}
	return true
}

// pickerCapacityError validates the current selection against the game's open
// seats, returning "" when it fits or a message to show otherwise. Under-
// filling is allowed (the creator may invite more later or take a seat); over-
// filling is not (excess invitees would be turned away at the door).
func pickerCapacityError(picker map[string]*inviteChoice, playerCount, teamSize int, teams bool) string {
	n := pickerCounts(picker, teams)
	if teams {
		for t := 0; t < config.TeamCount; t++ {
			if n[t] > teamSize {
				return fmt.Sprintf("Team %s has %d seats — you selected %d.", teamName(t), teamSize, n[t])
			}
		}
		return ""
	}
	if n[0] > playerCount {
		return fmt.Sprintf("This game has %d seats — you selected %d.", playerCount, n[0])
	}
	return ""
}

func (a *App) closeInvitePicker() {
	a.mu.Lock()
	a.invitePickerGameID = ""
	a.invitePicker = nil
	a.invitePickerErr = ""
	a.invitePickerPC, a.invitePickerTS = 0, 0
	a.mu.Unlock()
	a.invalidate()
}

// pickerCounts tallies the current picker selections. For teams it returns the
// per-team counts; for other modes the count sits in n[0].
func pickerCounts(picker map[string]*inviteChoice, teams bool) (n [config.TeamCount]int) {
	for _, c := range picker {
		if teams {
			switch c.team.Value {
			case "0":
				n[0]++
			case "1":
				n[1]++
			}
		} else if c.sel.Value {
			n[0]++
		}
	}
	return n
}

// invitePickerOverlay renders the modal invitee picker. gameID/picker are read
// by handleInvitePicker; this only draws.
func (a *App) invitePickerOverlay(gtx C) D {
	a.mu.Lock()
	gameID := a.invitePickerGameID
	picker := a.invitePicker
	a.mu.Unlock()
	if gameID == "" {
		return D{}
	}
	a.mu.Lock()
	mode, pc, ts := a.invitePickerMode, a.invitePickerPC, a.invitePickerTS
	pickErr := a.invitePickerErr
	a.mu.Unlock()
	teams := mode == config.ModeTeams

	ids := make([]string, 0, len(picker))
	for id := range picker {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	title := "INVITE PLAYERS"
	if teams {
		title = "INVITE PLAYERS TO TEAMS"
	}
	n := pickerCounts(picker, teams)
	capLine := fmt.Sprintf("%d of %d seats selected.", n[0], pc)
	if teams {
		capLine = fmt.Sprintf("Team A: %d/%d · Team B: %d/%d selected.", n[0], ts, n[1], ts)
	}

	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(460)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), title, colFg).Layout),
							layout.Rigid(spacer(4)),
							layout.Rigid(a.body("Only players currently in the lobby can be invited.", colMuted)),
							layout.Rigid(spacer(2)),
							layout.Rigid(a.body(capLine, colAccent)),
							layout.Rigid(func(gtx C) D {
								if pickErr == "" {
									return D{}
								}
								return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, a.body(pickErr, colErr))
							}),
							layout.Rigid(spacer(10)),
							layout.Rigid(func(gtx C) D {
								if len(ids) == 0 {
									return a.body("No other players are in the lobby right now.", colMuted)(gtx)
								}
								gtx.Constraints.Max.Y = gtx.Dp(240)
								return material.List(a.th, &a.inviteList).Layout(gtx, len(ids), func(gtx C, i int) D {
									return a.inviteRow(gtx, picker[ids[i]], teams)
								})
							}),
							layout.Rigid(spacer(14)),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(1, func(gtx C) D { return D{} }),
									layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.inviteCancelBtn, "Cancel") }),
									layout.Rigid(spacer(8)),
									layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.inviteSendBtn, "Send invites") }),
								)
							}),
						)
					})
				})
			})
		})
	})
}

func (a *App) inviteRow(gtx C, c *inviteChoice, teams bool) D {
	return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, a.body(agentName(c.name, c.agent), colFg)),
			layout.Rigid(func(gtx C) D {
				if !teams {
					cb := material.CheckBox(a.th, &c.sel, "Invite")
					cb.Color = colFg
					cb.IconColor = colAccent
					return cb.Layout(gtx)
				}
				// Teams: a three-way — not invited / team A / team B.
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.teamRadio(&c.team, "", "—")),
					layout.Rigid(spacer(6)),
					layout.Rigid(a.teamRadio(&c.team, "0", "A")),
					layout.Rigid(spacer(6)),
					layout.Rigid(a.teamRadio(&c.team, "1", "B")),
				)
			}),
		)
	})
}

func (a *App) teamRadio(enum *widget.Enum, value, label string) layout.Widget {
	return func(gtx C) D {
		rb := material.RadioButton(a.th, enum, value, label)
		rb.Color = colFg
		return rb.Layout(gtx)
	}
}

// incomingInviteOverlay renders the pop-up shown to an invited player. Reads
// the pending invitation live; the Accept/Decline buttons are dispatched in
// handleIncomingInvite.
func (a *App) incomingInviteOverlay(gtx C, inv *lobby.Invitation) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(420)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colNATSGreen, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), "YOU'RE INVITED", colNATSGreen).Layout),
							layout.Rigid(spacer(12)),
							layout.Rigid(a.body(inviteMessage(inv), colFg)),
							layout.Rigid(spacer(18)),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.inviteDeclineBtn, "Decline") }),
									layout.Rigid(spacer(10)),
									layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.inviteAcceptBtn, "Accept & Play") }),
								)
							}),
						)
					})
				})
			})
		})
	})
}

// inviteMessage is the pop-up's body line: who invited you to what.
func inviteMessage(inv *lobby.Invitation) string {
	switch inv.Mode {
	case config.ModeTeams:
		return fmt.Sprintf("%s invited you to a teams game — Team %s.", inv.FromName, teamName(inv.Team))
	case config.ModeCooperative:
		return fmt.Sprintf("%s invited you to a co-op game.", inv.FromName)
	default:
		return fmt.Sprintf("%s invited you to a competitive game.", inv.FromName)
	}
}

// handleIncomingInvite dispatches the pop-up's Accept/Decline. Returns true
// while a pending invitation exists (so the caller draws the overlay and
// suppresses background clicks).
func (a *App) handleIncomingInvite(gtx C) (*lobby.Invitation, bool) {
	lb := a.getLobby()
	if lb == nil {
		return nil, false
	}
	inv := lb.MyInvite()
	if inv == nil {
		return nil, false
	}
	if a.inviteAcceptBtn.Clicked(gtx) {
		gameID, team := inv.GameID, inv.Team
		go a.joinGame(gameID, team) // joining consumes the invitation
		return nil, false
	}
	if a.inviteDeclineBtn.Clicked(gtx) {
		go func() { _, _ = lb.RespondInvite(context.Background(), false) }()
		return nil, false
	}
	return inv, true
}
