package nativeui

import (
	"context"
	"fmt"
	"sort"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/config"
	"jetris/internal/lobby"
)

// inviteChoice is one selectable player in the invitee picker. Selection IS
// the action: the moment a player is selected their invitation is sent, and
// deselecting retracts it. For competitive and cooperative games `sel` is the
// toggle; for teams `team` picks which team to invite them to ("" = not
// invited, "0" = A, "1" = B). lastSel/lastTeam remember the intent already
// applied so the per-frame handler only acts on actual changes (and can put a
// widget back when a selection is refused by the capacity guard or undone by
// a decline).
type inviteChoice struct {
	playerID string
	name     string
	agent    bool
	sel      widget.Bool
	team     widget.Enum
	lastSel  bool
	lastTeam string
}

// inviteRowHeight is the fixed content height (dp) of every picker row. It
// matches the natural height of the Invite control (a material checkbox is
// Size 26 + 2dp inset each side = 30dp) so a row doesn't shrink when the
// control disappears the moment its player joins.
const inviteRowHeight = 30

// inviteRowStatus is the live state of one picker row, derived from the
// game's roster and the invitations sent (pickerRowStatus).
type inviteRowStatus int

const (
	rowNone     inviteRowStatus = iota // not invited (selectable)
	rowPending                         // invited, no answer yet (deselect = retract)
	rowDeclined                        // declined; selecting again re-invites
	rowJoined                          // accepted: on the roster
	rowReady                           // on the roster AND ready
)

// pickerRowStatus derives a player's row state from the listing and the
// game's live invitations.
func pickerRowStatus(g lobby.GameListing, invites []lobby.Invitation, playerID string) inviteRowStatus {
	for _, p := range g.Players {
		if p.PlayerID == playerID {
			if p.Ready {
				return rowReady
			}
			return rowJoined
		}
	}
	for _, inv := range invites {
		if inv.InviteeID == playerID {
			if inv.Declined {
				return rowDeclined
			}
			return rowPending
		}
	}
	return rowNone
}

// inviteSeatUsage counts the seats already spoken for — roster members plus
// pending (not declined) invitations — per team in teams mode, in [0]
// otherwise. The capacity guard refuses selections beyond the free seats.
func inviteSeatUsage(g lobby.GameListing, invites []lobby.Invitation, teams bool) (n [config.TeamCount]int) {
	for _, p := range g.Players {
		if teams {
			if p.Team >= 0 && p.Team < config.TeamCount {
				n[p.Team]++
			}
		} else {
			n[0]++
		}
	}
	for _, inv := range invites {
		if inv.Declined {
			continue
		}
		if teams {
			if inv.Team >= 0 && inv.Team < config.TeamCount {
				n[inv.Team]++
			}
		} else {
			n[0]++
		}
	}
	return n
}

// openInvitePicker creates the invite-only game and opens the invitee picker
// over the lobby. The creator starts UNSELECTED — hosting without playing, so
// they'll spectate once the game fills; select yourself to also take a seat.
// No seat is taken at creation. Runs off the UI goroutine (create does a NATS
// round trip).
func (a *App) openInvitePicker(mode config.GameMode, count, nextCount int) {
	gameID := a.createGame(mode, count, 0, nextCount, true) // invite-only: agent policy is per-invite
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
	reconcileInvitePicker(picker, lb.Players(), lb.PlayerID(), nil)

	// The creator takes no seat by default: they host and will spectate unless
	// they select themselves (which fires selfSeat via the per-frame handler).
	a.mu.Lock()
	a.invitePickerGameID = gameID
	a.invitePicker = picker
	a.invitePickerMode = mode
	a.invitePickerPC = playerCount
	a.invitePickerTS = teamSize
	a.invitePickerErr = ""
	a.inviteSelfSel.Value = false
	a.inviteSelfLastSel = false
	a.inviteSelfTeam.Value = ""
	a.inviteSelfLastTeam = ""
	a.mu.Unlock()
	a.invalidate()
}

// reopenInvitePicker re-opens the invitee picker for an invite-only game the
// creator already made — so more players can be invited after the creator has
// joined and gone "Back to Lobby" (the picker only opens on its own at
// creation). Unlike openInvitePicker it neither creates the game nor forces a
// seat: it seeds the picker from the existing listing and mirrors the current
// state — pending invitations show checked, the creator's own row reflects
// whether they presently hold a seat — so no spurious retract/join fires.
func (a *App) reopenInvitePicker(g lobby.GameListing) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	mode := g.Mode
	teams := mode == config.ModeTeams
	playerCount, teamSize := g.PlayerCount, 0
	if teams {
		teamSize = g.TeamSize
	}
	invites := lb.SentInvites(g.GameID)

	// Keep everyone involved with THIS game listed (roster members show as
	// "joined", pending invitees as "invited") even when their presence reads
	// "in game".
	keep := make(map[string]bool, len(g.Players)+len(invites))
	for _, p := range g.Players {
		keep[p.PlayerID] = true
	}
	for _, inv := range invites {
		keep[inv.InviteeID] = true
	}
	picker := make(map[string]*inviteChoice)
	reconcileInvitePicker(picker, lb.Players(), lb.PlayerID(), keep)

	// Mirror each still-pending invitation onto its candidate widget so the row
	// renders checked and the per-frame handler sees no change (declined ones
	// stay unchecked — the declined marker shows, re-selecting re-invites).
	for _, inv := range invites {
		c, ok := picker[inv.InviteeID]
		if !ok || inv.Declined {
			continue
		}
		if teams {
			c.team.Value = fmt.Sprintf("%d", inv.Team)
			c.lastTeam = c.team.Value
		} else {
			c.sel.Value, c.lastSel = true, true
		}
	}

	// The creator's own row: checked only if they currently hold a seat.
	me := lb.PlayerID()
	selfTeam := ""
	for _, p := range g.Players {
		if p.PlayerID == me {
			selfTeam = fmt.Sprintf("%d", p.Team)
		}
	}

	a.mu.Lock()
	a.invitePickerGameID = g.GameID
	a.invitePicker = picker
	a.invitePickerMode = mode
	a.invitePickerPC = playerCount
	a.invitePickerTS = teamSize
	a.invitePickerErr = ""
	a.inviteSelfSel.Value = selfTeam != ""
	a.inviteSelfLastSel = selfTeam != ""
	a.inviteSelfTeam.Value = selfTeam
	a.inviteSelfLastTeam = selfTeam
	a.mu.Unlock()
	a.invalidate()
}

// reconcileInvitePicker updates the candidate map in place against the live
// lobby presence: it adds every newly-eligible player (in the lobby, not in a
// game, not yourself), drops anyone who left or joined some OTHER game, and
// PRESERVES the selection widgets of players who remain. keep holds player
// IDs involved with THIS game (roster members and invitees) — they stay
// listed while their presence says "in game", so the creator watches their
// invitation play out. Pure so it can be tested.
func reconcileInvitePicker(picker map[string]*inviteChoice, live map[string]lobby.PlayerPresence, self string, keep map[string]bool) {
	for id, p := range live {
		if id == self || (p.Status != lobby.StatusInLobby && !keep[id]) {
			continue
		}
		if _, ok := picker[id]; !ok {
			picker[id] = &inviteChoice{playerID: id, name: p.Name, agent: p.Agent}
		}
	}
	for id := range picker {
		if keep[id] {
			continue
		}
		if p, ok := live[id]; !ok || p.Status != lobby.StatusInLobby {
			delete(picker, id)
		}
	}
}

// syncInvitePickerCandidates keeps the open picker's list reactive: called
// each frame while the overlay is up, it folds in players who joined the lobby
// and removes those who left or entered another game.
func (a *App) syncInvitePickerCandidates(g lobby.GameListing, invites []lobby.Invitation) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	keep := make(map[string]bool, len(g.Players)+len(invites))
	for _, p := range g.Players {
		keep[p.PlayerID] = true
	}
	for _, inv := range invites {
		keep[inv.InviteeID] = true
	}
	self, live := lb.PlayerID(), lb.Players()
	a.mu.Lock()
	if a.invitePicker != nil {
		reconcileInvitePicker(a.invitePicker, live, self, keep)
	}
	a.mu.Unlock()
}

// handleInvitePicker drives the picker each frame: it applies selection
// changes (send/retract invitations, take/free the creator's own seat),
// dispatches Close/Cancel, and — once the game has filled — closes the
// overlay and moves the creator to the game (playing if they kept their own
// seat, spectating otherwise). Returns true while the overlay is open (so the
// caller can suppress background clicks).
func (a *App) handleInvitePicker(gtx C) bool {
	a.mu.Lock()
	gameID := a.invitePickerGameID
	mode, pc, ts := a.invitePickerMode, a.invitePickerPC, a.invitePickerTS
	a.mu.Unlock()
	if gameID == "" {
		return false
	}
	lb := a.getLobby()
	if lb == nil {
		a.closeInvitePicker()
		return false
	}
	me := lb.PlayerID()
	teams := mode == config.ModeTeams
	g, haveListing := lb.Games()[gameID]
	invites := lb.SentInvites(gameID)

	// Enough players joined → the game is starting without further inviting:
	// move the creator to their seat (ready screen) or to the spectator view.
	if haveListing && g.PlayerCount > 0 && len(g.Players) >= g.PlayerCount {
		playing := rosterHas(g, me)
		team := 0
		for _, p := range g.Players {
			if p.PlayerID == me {
				team = p.Team
			}
		}
		a.closeInvitePicker()
		if playing {
			go a.joinGame(gameID, team)
		} else {
			go a.spectateGame(gameID)
		}
		return false
	}

	a.syncInvitePickerCandidates(g, invites) // reactive: add joiners, drop leavers

	if a.inviteCancelBtn.Clicked(gtx) {
		// Abandon the game: retract every outstanding invitation (their
		// pop-ups disappear with the keys), then delete the game itself.
		go a.cancelInviteGame(gameID, invites)
		a.closeInvitePicker()
		return true
	}
	if a.inviteCloseBtn.Clicked(gtx) {
		// Keep the game and its invitations; the lobby row carries the same
		// live status and the seat (if taken) stays reserved.
		a.closeInvitePicker()
		return true
	}

	usage := inviteSeatUsage(g, invites, teams)
	seatFree := func(team int) bool {
		if !haveListing {
			return true // listing not seen yet; JoinGame/accept still enforces
		}
		if teams {
			return usage[team] < ts
		}
		return usage[0] < pc
	}
	setErr := func(msg string) {
		a.mu.Lock()
		a.invitePickerErr = msg
		a.mu.Unlock()
	}

	// The creator's own seat (the pinned "You" row).
	if teams {
		if v := a.inviteSelfTeam.Value; v != a.inviteSelfLastTeam {
			if v != "" && !seatFree(int(v[0]-'0')) {
				a.inviteSelfTeam.Value = a.inviteSelfLastTeam // refused: seat spoken for
				setErr(fmt.Sprintf("Team %s is full (joined + invited).", teamName(int(v[0]-'0'))))
			} else {
				a.inviteSelfLastTeam = v
				setErr("")
				go a.selfSeat(gameID, v)
			}
		}
	} else {
		if v := a.inviteSelfSel.Value; v != a.inviteSelfLastSel {
			if v && !seatFree(0) {
				a.inviteSelfSel.Value = false
				setErr("All seats are taken (joined + invited).")
			} else {
				a.inviteSelfLastSel = v
				setErr("")
				sel := ""
				if v {
					sel = "0"
				}
				go a.selfSeat(gameID, sel)
			}
		}
	}

	// Everyone else: selection changes send/retract invitations immediately.
	a.mu.Lock()
	picker := a.invitePicker
	a.mu.Unlock()
	for id, c := range picker {
		st := pickerRowStatus(g, invites, id)
		switch st {
		case rowJoined, rowReady:
			// The seat answers for them now; neutralize the widget so that if
			// they later un-join they reappear unselected (re-invitable).
			c.sel.Value, c.lastSel = false, false
			c.team.Value, c.lastTeam = "", ""
			continue
		case rowDeclined:
			// A decline undoes the selection from THEIR side: uncheck the row
			// (it renders the declined marker); selecting again re-invites.
			if c.lastSel || c.lastTeam != "" {
				c.sel.Value, c.lastSel = false, false
				c.team.Value, c.lastTeam = "", ""
			}
		}
		if teams {
			v := c.team.Value
			if v == c.lastTeam {
				continue
			}
			if v == "" {
				c.lastTeam = ""
				go a.retractInvite(gameID, id)
				continue
			}
			t := int(v[0] - '0')
			if !seatFree(t) {
				c.team.Value = c.lastTeam
				setErr(fmt.Sprintf("Team %s is full (joined + invited).", teamName(t)))
				continue
			}
			c.lastTeam = v
			setErr("")
			go a.sendInvite(gameID, id, t) // a team change re-invites to the new team
		} else {
			v := c.sel.Value
			if v == c.lastSel {
				continue
			}
			if !v {
				c.lastSel = false
				go a.retractInvite(gameID, id)
				continue
			}
			if !seatFree(0) {
				c.sel.Value = false
				setErr("All seats are taken (joined + invited).")
				continue
			}
			c.lastSel = true
			setErr("")
			go a.sendInvite(gameID, id, 0)
		}
	}
	return true
}

// sendInvite / retractInvite dispatch one invitation action off the UI
// goroutine (also used when a selection changes team: Invite overwrites).
func (a *App) sendInvite(gameID, playerID string, team int) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	if err := lb.Invite(context.Background(), playerID, gameID, team); err != nil {
		logInvite(err)
	}
	a.invalidate()
}

func (a *App) retractInvite(gameID, playerID string) {
	lb := a.getLobby()
	if lb == nil {
		return
	}
	if err := lb.Uninvite(context.Background(), playerID, gameID); err != nil {
		logInvite(err)
	}
	a.invalidate()
}

// cancelInviteGame abandons a just-created invitation game: every outstanding
// invitation is retracted (deleting the keys closes the invitees' pop-ups),
// then the game and its stream/listing are deleted.
func (a *App) cancelInviteGame(gameID string, invites []lobby.Invitation) {
	lb := a.getLobby()
	if lb != nil {
		for _, inv := range invites {
			if err := lb.Uninvite(context.Background(), inv.InviteeID, gameID); err != nil {
				logInvite(err)
			}
		}
	}
	a.deleteGame(gameID)
}

func (a *App) closeInvitePicker() {
	a.mu.Lock()
	a.invitePickerGameID = ""
	a.invitePicker = nil
	a.invitePickerErr = ""
	a.invitePickerPC, a.invitePickerTS = 0, 0
	a.inviteSelfSel.Value, a.inviteSelfLastSel = false, false
	a.inviteSelfTeam.Value, a.inviteSelfLastTeam = "", ""
	a.mu.Unlock()
	a.invalidate()
}

// invitePickerOverlay renders the modal invitee picker. All actions are
// dispatched by handleInvitePicker; this only draws.
func (a *App) invitePickerOverlay(gtx C) D {
	a.mu.Lock()
	gameID := a.invitePickerGameID
	picker := a.invitePicker
	mode, pc, ts := a.invitePickerMode, a.invitePickerPC, a.invitePickerTS
	pickErr := a.invitePickerErr
	a.mu.Unlock()
	if gameID == "" {
		return D{}
	}
	teams := mode == config.ModeTeams

	var g lobby.GameListing
	var invites []lobby.Invitation
	selfName := ""
	if lb := a.getLobby(); lb != nil {
		g = lb.Games()[gameID]
		invites = lb.SentInvites(gameID)
		selfName = lb.PlayerName()
	}

	ids := make([]string, 0, len(picker))
	for id := range picker {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	title := "INVITE PLAYERS"
	if teams {
		title = "INVITE PLAYERS TO TEAMS"
	}
	// Seat tally, broken out so the header shows joined / invited / open at a
	// glance (usage counts joined + pending together; joined is the roster).
	usage := inviteSeatUsage(g, invites, teams)
	var joined [config.TeamCount]int
	for _, p := range g.Players {
		t := 0
		if teams {
			t = p.Team
		}
		if t >= 0 && t < config.TeamCount {
			joined[t]++
		}
	}
	var capLines []string
	if teams {
		for t := 0; t < config.TeamCount; t++ {
			capLines = append(capLines, fmt.Sprintf("Team %s  %d/%d seats — %d joined · %d invited · %d open",
				teamName(t), usage[t], ts, joined[t], usage[t]-joined[t], ts-usage[t]))
		}
	} else {
		capLines = append(capLines, fmt.Sprintf("%d/%d seats filled — %d joined · %d invited · %d open",
			usage[0], pc, joined[0], usage[0]-joined[0], pc-usage[0]))
	}

	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(500)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colAccent, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(a.pixel(unit.Sp(13), title, colFg).Layout),
							layout.Rigid(spacer(4)),
							layout.Rigid(a.body("Selecting a player invites them on the spot; deselecting retracts. You're hosting as a spectator — select yourself to take a seat and play too.", colMuted)),
							layout.Rigid(spacer(6)),
							layout.Rigid(func(gtx C) D {
								// The seat tally, rendered larger and bold so it's
								// the header's most obvious line.
								var kids []layout.FlexChild
								for i, ln := range capLines {
									ln := ln
									if i > 0 {
										kids = append(kids, layout.Rigid(spacer(3)))
									}
									kids = append(kids, layout.Rigid(func(gtx C) D {
										l := material.Label(a.th, unit.Sp(16), ln)
										l.Color = colAccent
										l.Font.Weight = font.Bold
										return l.Layout(gtx)
									}))
								}
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
							}),
							layout.Rigid(spacer(2)),
							layout.Rigid(func(gtx C) D {
								if pickErr == "" {
									return D{}
								}
								return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, a.body(pickErr, colErr))
							}),
							layout.Rigid(spacer(10)),
							layout.Rigid(func(gtx C) D { return a.inviteSelfRow(gtx, g, selfName, teams) }),
							layout.Rigid(spacer(4)),
							layout.Rigid(func(gtx C) D {
								if len(ids) == 0 {
									return a.body("No other players are in the lobby right now.", colMuted)(gtx)
								}
								gtx.Constraints.Max.Y = gtx.Dp(240)
								// Overlay the scrollbar instead of reserving a lane for
								// it (Occupy): list rows then span the full width, so
								// their Invite checkboxes right-align with the pinned
								// self row's Play checkbox above.
								l := material.List(a.th, &a.inviteList)
								l.AnchorStrategy = material.Overlay
								return l.Layout(gtx, len(ids), func(gtx C, i int) D {
									c := picker[ids[i]]
									return a.inviteRow(gtx, c, pickerRowStatus(g, invites, c.playerID), teams)
								})
							}),
							layout.Rigid(spacer(14)),
							layout.Rigid(func(gtx C) D {
								// The hint is the Flexed child so it wraps if space is
								// tight; the buttons stay at their natural width.
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(1, a.body("The game starts on its own once every seat is filled and ready.", colMuted)),
									layout.Rigid(hSpacer(8)),
									layout.Rigid(func(gtx C) D { return a.dangerButton(gtx, &a.inviteCancelBtn, "Cancel game") }),
									layout.Rigid(hSpacer(8)),
									layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.inviteCloseBtn, "Close") }),
								)
							}),
						)
					})
				})
			})
		})
	})
}

// inviteSelfRow is the pinned first row of the picker: the creator's own
// participation. Selected (the default) means a seat is taken and you play;
// deselected means you host and will spectate once the game fills.
func (a *App) inviteSelfRow(gtx C, g lobby.GameListing, selfName string, teams bool) D {
	joined := false
	ready := false
	if lb := a.getLobby(); lb != nil {
		for _, p := range g.Players {
			if p.PlayerID == lb.PlayerID() {
				joined, ready = true, p.Ready
			}
		}
	}
	status, statusCol := "spectating when the game starts", colMuted
	switch {
	case ready:
		status, statusCol = "joined · ready ✓", colNATSGreen
	case joined:
		status, statusCol = "joined ✓", colNATSGreen
	}
	return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.Y = gtx.Dp(inviteRowHeight) // match the candidate rows' fixed height
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(a.body("You ("+selfName+")", colFg)),
					layout.Rigid(hSpacer(8)),
					layout.Rigid(a.body(status, statusCol)),
				)
			}),
			layout.Rigid(func(gtx C) D {
				if !teams {
					cb := material.CheckBox(a.th, &a.inviteSelfSel, "Play")
					cb.Color = colFg
					cb.IconColor = colAccent
					return cb.Layout(gtx)
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.teamRadio(&a.inviteSelfTeam, "", "—")),
					layout.Rigid(hSpacer(6)),
					layout.Rigid(a.teamRadio(&a.inviteSelfTeam, "0", "A")),
					layout.Rigid(hSpacer(6)),
					layout.Rigid(a.teamRadio(&a.inviteSelfTeam, "1", "B")),
				)
			}),
		)
	})
}

// inviteRow renders one candidate row: name, live invitation status, and the
// selection control (hidden once the player has joined — the roster line
// answers for them).
func (a *App) inviteRow(gtx C, c *inviteChoice, st inviteRowStatus, teams bool) D {
	status, statusCol := "", colMuted
	switch st {
	case rowPending:
		status, statusCol = "✉ invited — waiting…", colGold
	case rowDeclined:
		status, statusCol = "✕ declined", colErr
	case rowJoined:
		status, statusCol = "joined ✓", colNATSGreen
	case rowReady:
		status, statusCol = "joined · ready ✓", colNATSGreen
	}
	return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx C) D {
		// Pin a fixed row height so a row keeps the same height once the player
		// joins and its Invite control (a ~30dp checkbox/radio) disappears —
		// otherwise the row shrinks and the rows below jump up.
		gtx.Constraints.Min.Y = gtx.Dp(inviteRowHeight)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(a.body(agentName(c.name, c.agent), colFg)),
					layout.Rigid(func(gtx C) D {
						if status == "" {
							return D{}
						}
						return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, a.body(status, statusCol))
					}),
				)
			}),
			layout.Rigid(func(gtx C) D {
				if st == rowJoined || st == rowReady {
					return D{} // seated: nothing to select or retract
				}
				if !teams {
					cb := material.CheckBox(a.th, &c.sel, "Invite")
					cb.Color = colFg
					cb.IconColor = colAccent
					return cb.Layout(gtx)
				}
				// Teams: a three-way — not invited / team A / team B.
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(a.teamRadio(&c.team, "", "—")),
					layout.Rigid(hSpacer(6)),
					layout.Rigid(a.teamRadio(&c.team, "0", "A")),
					layout.Rigid(hSpacer(6)),
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

// incomingInviteOverlay renders the pop-up shown to an invited player: who
// invited them to what, plus the game's current roster so they can see who
// they would be playing with. Reads the pending invitation live; the
// Accept/Decline buttons are dispatched in handleIncomingInvite.
func (a *App) incomingInviteOverlay(gtx C, inv *lobby.Invitation) D {
	// Current roster of the inviting game ("who's already in").
	var rosterLines []string
	seats := ""
	if lb := a.getLobby(); lb != nil {
		if g, ok := lb.Games()[inv.GameID]; ok {
			seats = fmt.Sprintf("%d/%d seats filled", len(g.Players), g.PlayerCount)
			for _, p := range g.Players {
				line := "• " + agentName(p.Name, p.Agent)
				if g.Mode == config.ModeTeams {
					line += " — team " + teamName(p.Team)
				}
				if p.Ready {
					line += " (ready ✓)"
				}
				rosterLines = append(rosterLines, line)
			}
		}
	}

	return layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(420)
		return hardShadow(gtx, func(gtx C) D {
			return widget.Border{Color: colNATSGreen, Width: unit.Dp(3)}.Layout(gtx, func(gtx C) D {
				return background(gtx, colBg, func(gtx C) D {
					return layout.UniformInset(unit.Dp(22)).Layout(gtx, func(gtx C) D {
						children := []layout.FlexChild{
							layout.Rigid(a.pixel(unit.Sp(13), "YOU'RE INVITED", colNATSGreen).Layout),
							layout.Rigid(spacer(12)),
							layout.Rigid(a.body(inviteMessage(inv), colFg)),
						}
						if seats != "" {
							children = append(children,
								layout.Rigid(spacer(10)),
								layout.Rigid(a.body(seats, colAccent)))
							if len(rosterLines) == 0 {
								children = append(children,
									layout.Rigid(spacer(4)),
									layout.Rigid(a.body("No one has joined yet.", colMuted)))
							}
							for _, line := range rosterLines {
								children = append(children,
									layout.Rigid(spacer(4)),
									layout.Rigid(a.body(line, colMuted)))
							}
						}
						children = append(children,
							layout.Rigid(spacer(18)),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return a.secondaryButton(gtx, &a.inviteDeclineBtn, "Decline") }),
									layout.Rigid(hSpacer(10)),
									layout.Rigid(func(gtx C) D { return a.primaryButton(gtx, &a.inviteAcceptBtn, "Accept & Play") }),
								)
							}))
						return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, children...)
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

// handleIncomingInvite dispatches the pop-up's Accept/Decline. A player may
// hold invitations to several games at once; the pop-up shows the OLDEST
// pending one, and answering (or the inviter retracting it) surfaces the
// next. Returns true while a pending invitation exists (so the caller draws
// the overlay and suppresses background clicks).
func (a *App) handleIncomingInvite(gtx C) (*lobby.Invitation, bool) {
	lb := a.getLobby()
	if lb == nil {
		return nil, false
	}
	invs := lb.MyInvites()
	if len(invs) == 0 {
		return nil, false
	}
	inv := invs[0]
	if a.inviteAcceptBtn.Clicked(gtx) {
		gameID, team := inv.GameID, inv.Team
		go a.joinGame(gameID, team) // joining consumes the invitation
		return nil, false
	}
	if a.inviteDeclineBtn.Clicked(gtx) {
		gameID := inv.GameID
		go func() {
			// The declined marker stays in the KV so the inviter sees it; if
			// the game vanished meanwhile just drop the stale invitation.
			if _, ok := lb.Games()[gameID]; ok {
				_ = lb.DeclineInvite(context.Background(), gameID)
			} else {
				_ = lb.DismissInvite(context.Background(), gameID)
			}
		}()
		return nil, false
	}
	return &inv, true
}
