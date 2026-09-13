package nativeui

import (
	"context"
	"fmt"
	"image"
	"log"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/nats-io/nats.go"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	"jetris/internal/prefs"
	"jetris/internal/render"
	"jetris/internal/voice"
)

// The voice chat (internal/voice) — the App's half of it.
//
// A session lives exactly as long as a screen with a room: the game screen's
// is started by startGameScreen (startVoice) and stopped by Back to Lobby
// and the window's close (stopVoice); the lobby's — the room
// config.LobbyVoiceRoom, everyone on the server who is in the lobby — comes
// and goes with the lobby screen, decided every frame by reconcileVoice
// (none on the login, archive and replay screens). The rooms never overlap:
// a screen's session replaces the last one, and the old one is stopped —
// its subscriptions dropped, its device closed — BEFORE the new one opens,
// so nothing said in the lobby is heard on a game screen, nor the other way
// round (TestVoiceRoomsDoNotLeak, voice/session_test.go).
//
// The microphone follows the player from room to room: the mic button's
// answer (micOn) is the player's standing one for this visit to the server,
// and every session started — a game joined or spectated, the lobby come
// back to — opens the microphone again if it was open on the last screen.
// A fresh connection to a lobby starts MUTED, whatever the last visit ended
// on (initLobby): a microphone is nothing to leave open by default. The
// bar's mic button (micButton, beside the menu button) is the one switch:
// muted, open, or lit green while the gate is open and frames are going
// out. The menu's
// VOICE section (voiceSection) holds the rest: the status line, the level
// meter, the GATE slider — how much louder than the room the player must be
// before the microphone publishes, OPEN at 0 — "Play voice", and in a teams
// game the TEAM / ALL switch for the room the player's own frames go to.
//
// Who is talking shows in three places: a speaker mark beside the name in
// the menu's legend (and the ready list, and a spectator's boards), the same
// mark on an opponent's board label, and — with the menu put away — a strip
// over the bottom-left corner of the board area (voiceStrip) naming whoever
// is heard right now. Paint only, over the board: it shifts nothing and
// takes no press.
//
// Threading: a.voice is set and cleared under a.mu; the layout takes a
// per-frame Snapshot under the session's own lock and pushes the slider,
// the checkbox and the switch into the session as atomics, so neither the
// audio thread nor the NATS callback ever waits on the UI. The session wakes
// the UI through a.invalidate on edges only; the repaint while someone
// speaks is scheduled from the frame (layoutGame).

// The TEAM / ALL switch's values (voiceChanEnum).
const (
	voiceChanTeam = "team"
	voiceChanAll  = "all"
)

// gateRange maps the GATE slider onto prefs.Voice.GateDb: 0..60 dB, a dB a
// detent.
var gateRange = knobRange{prefs.MinGateDb, prefs.MaxGateDb, 1}

// SetVoice applies the saved voice settings: the loaded preferences' way in
// (cmd/jetris/main.go), before Run.
func (a *App) SetVoice(p prefs.Voice) {
	a.voiceGateDb = min(max(p.GateDb, prefs.MinGateDb), prefs.MaxGateDb)
	a.voiceGateFloat.Value = gateRange.pos(a.voiceGateDb)
	a.voiceListenCb.Value = p.Listen
	a.voiceListenWas = p.Listen
	a.mu.Lock()
	a.voicePrefs = prefs.Voice{GateDb: a.voiceGateDb, Listen: p.Listen}
	a.mu.Unlock()
}

// persistVoice saves the gate and "Play voice". A failure is silent, as the
// handling knobs' is (persistHandling). The mute is not saved: it lasts the
// visit to the server (micOn) and no longer.
func (a *App) persistVoice() {
	p := prefs.Voice{GateDb: a.voiceGateDb, Listen: a.voiceListenCb.Value}
	a.mu.Lock()
	a.voicePrefs = p
	a.mu.Unlock()
	if a.voiceSave == nil {
		return
	}
	_ = a.voiceSave(p)
}

// getVoice is the game screen's session, nil between games.
func (a *App) getVoice() *voice.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.voice
}

// voiceChannel is the room the TEAM / ALL switch is on.
func (a *App) voiceChannel() voice.Channel {
	if a.voiceChanEnum.Value == voiceChanAll {
		return voice.ChannelAll
	}
	return voice.ChannelTeam
}

// startVoice starts the session for a game screen just entered, on the
// team's room if the seat has one, with the microphone as the player left
// it on the last screen (micOn). Runs off the UI goroutine and without a.mu
// (opening the speakers takes tens of milliseconds); nc is the app's
// connection, nil on a transport-less screen (the tests), which makes an
// offline session. A device that will not open is not a failure of the
// join: the session says so in its status line and the screen carries on.
//
// The screen's last session — the lobby's, or an earlier game's — is
// stopped FIRST, and only then the new one opened: two sessions up at once
// would be two rooms heard at once, and two open microphones on the one
// device.
func (a *App) startVoice(e *engine.Engine, ctx context.Context, nc *nats.Conn) {
	team := -1
	if e.GameMode() == config.ModeTeams && e.InitialMode() == engine.ModePlayer {
		team = e.TeamIdx()
	}
	a.mu.Lock()
	p, on := a.voicePrefs, a.micOn
	old := a.voice
	a.voice, a.voiceRoom = nil, ""
	a.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	s := voice.New(voice.Config{
		GameID:    e.GameID(),
		PlayerID:  e.PlayerID(),
		Team:      team,
		Spectator: e.InitialMode() == engine.ModeSpectator,
		GateDb:    p.GateDb,
		Listen:    p.Listen,
		OnChange:  a.invalidate,
	}, a.voiceDevice())
	if err := s.Start(ctx, nc); err != nil {
		log.Printf("voice: %v", err)
	}
	a.mu.Lock()
	a.voice, a.voiceRoom = s, e.GameID()
	a.mu.Unlock()
	if on {
		s.SetMuted(false)
	}
}

// stopVoice ends the screen's session, if one is up.
func (a *App) stopVoice() {
	a.mu.Lock()
	v := a.voice
	a.voice, a.voiceRoom = nil, ""
	a.mu.Unlock()
	if v != nil {
		v.Stop()
	}
}

// reconcileVoice is the lobby's session's lifecycle, run every frame on the
// UI goroutine: the lobby screen up with a lobby and no session means one
// is started (off the goroutine — it opens the speakers), and a lobby
// session on a screen that is not the lobby's nor a game's (a replay, the
// archive, the login screen) is stopped. The game screen's session is
// startGameScreen's and returnToLobby's; a game session found here is
// left alone.
func (a *App) reconcileVoice() {
	a.mu.Lock()
	screen, lb, v, room, starting := a.screen, a.lobby, a.voice, a.voiceRoom, a.voiceStarting
	switch {
	case screen == screenLobby && lb != nil && v == nil && !starting:
		a.voiceStarting = true
		a.mu.Unlock()
		go a.startLobbyVoice()
	case screen != screenLobby && screen != screenGame && v != nil && room == config.LobbyVoiceRoom:
		a.mu.Unlock()
		go a.stopVoice()
	default:
		a.mu.Unlock()
	}
}

// startLobbyVoice starts the lobby's session in the lobby room, with the
// microphone as the player left it (micOn: muted on a fresh connection,
// open again on the way back from a game where it was open). Off the UI
// goroutine; installed only if the lobby is still the screen and nothing
// else has taken the slot meanwhile (a game joined while the speakers were
// opening), else stopped again.
func (a *App) startLobbyVoice() {
	a.mu.Lock()
	lb, nc, p, on := a.lobby, a.nc, a.voicePrefs, a.micOn
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.voiceStarting = false
		a.mu.Unlock()
	}()
	if lb == nil {
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s := voice.New(voice.Config{
		GameID:   config.LobbyVoiceRoom,
		PlayerID: lb.PlayerID(),
		Team:     -1,
		GateDb:   p.GateDb,
		Listen:   p.Listen,
		OnChange: a.invalidate,
	}, a.voiceDevice())
	if err := s.Start(ctx, nc); err != nil {
		log.Printf("lobby voice: %v", err)
	}
	a.mu.Lock()
	install := a.screen == screenLobby && a.lobby == lb && a.voice == nil
	if install {
		a.voice, a.voiceRoom = s, config.LobbyVoiceRoom
	}
	a.mu.Unlock()
	if !install {
		s.Stop()
		return
	}
	if on {
		s.SetMuted(false)
	}
	a.invalidate()
}

// toggleMic is the bar's mic button's press, on either screen: the
// session's switch flipped, and the outcome kept as the player's standing
// answer (micOn) for the sessions that follow — the game joined next, the
// lobby come back to. Runs in the click's own frame, because the browser
// opens the microphone only on the player's gesture. Nothing to flip while
// the session is still on its way (the speakers opening).
func (a *App) toggleMic() {
	v := a.getVoice()
	if v == nil {
		return
	}
	v.SetMuted(!v.Muted())
	a.mu.Lock()
	a.micOn = !v.Muted()
	a.mu.Unlock()
}

// voiceFrame is a screen's per-frame read of its session: the menu's knobs
// go in as atomics, the snapshot comes out under the session's own lock —
// and, with no session, a muted one, which is what the session will say.
func (a *App) voiceFrame(channel voice.Channel) voice.Snapshot {
	v := a.getVoice()
	if v == nil {
		return voice.Snapshot{Muted: true}
	}
	v.SetGateDb(a.voiceGateDb)
	v.SetListen(a.voiceListenCb.Value)
	v.SetChannel(channel)
	return v.Snapshot()
}

// voiceRepaints asks for the frames a voice snapshot needs: the one that
// drops a speaker mark, at its own time, and — while the microphone is open
// and the meter on screen — the next one.
func voiceRepaints(gtx C, v voice.Snapshot, meterShown bool) {
	if !v.NextExpiry.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: v.NextExpiry})
	}
	if v.Capturing && meterShown {
		animate(gtx)
	}
}

// micButton is the bar's voice switch: the microphone with a red slash
// through it while muted — the mark every call app puts on a muted
// microphone, so a player who has never seen this one knows what the button
// is and that a tap opens it (greyed when there is no microphone to be
// had); the microphone alone while open; and lit green — the countdown's
// GO — while the gate is open and the player's voice is going out. The
// icon is drawn (micIcon, controls.go), not a bitmap: the bar's other
// switches are blocky by design, but this one has to be read by strangers.
func (a *App) micButton(gtx C, v voice.Snapshot) D {
	bg, fg, slash := colPanel, colAccent, colErr
	switch {
	case v.Opening:
		fg, slash = colMuted, colTransparent
	case !v.Muted && v.Talking:
		bg, fg, slash = colGo, colBg, colTransparent
	case !v.Muted:
		slash = colTransparent
	case v.Err != "":
		fg = colMuted
	}
	return a.barButtonIcon(gtx, &a.barMicBtn, bg, func(gtx C) D {
		return micIcon(gtx, gtx.Metric.PxToDp(gtx.Constraints.Max.X*3/5), fg, slash)
	})
}

// speakerMark is the mark beside a name while that player is heard: the
// speaker glyph, green from the team's room and blue from everyone's.
// Nothing while they are silent.
func (a *App) speakerMark(v voice.Snapshot, id string, size unit.Dp) layout.Widget {
	return func(gtx C) D {
		sp, ok := v.Speaking(id)
		if !ok {
			return D{}
		}
		return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, glyphWidget(glyphSpeaker, size, speakerColor(sp)))
	}
}

func speakerColor(sp voice.Speaker) colorN {
	if sp.Team >= 0 {
		return colGo
	}
	return colAccent
}

// voiceRoomName names the room a game screen's frames go to, for the
// status line.
func (a *App) voiceRoomName(eng *engine.Engine, v voice.Snapshot) string {
	if v.HasTeam && v.Channel == voice.ChannelTeam {
		return "TEAM " + eng.TeamName(eng.TeamIdx())
	}
	return "ALL"
}

// voiceSection is the menu's VOICE section: what the microphone is doing,
// the level meter while it is open, the GATE slider, "Play voice", and in a
// teams game the TEAM / ALL switch. The slider is a widget.Float (drag-only)
// for the reason the handling knobs are: tuning never takes the keys from
// the board.
func (a *App) voiceSection(gtx C, v voice.Snapshot, room string) D {
	status, col := "muted · tap the mic to talk", colMuted
	switch {
	case v.Err != "":
		status, col = v.Err, colErr
	case v.Opening:
		status, col = "opening the microphone…", colMuted
	case v.Talking:
		status, col = "TRANSMITTING → "+room, colGo
	case !v.Muted:
		status, col = "mic open → "+room, colFg
	}

	if a.voiceGateFloat.Update(gtx) {
		a.voiceGateDb = gateRange.value(a.voiceGateFloat.Value)
		a.voiceGateFloat.Value = gateRange.pos(a.voiceGateDb) // the detent, thumb included
		a.voiceDirty = true
	}
	// A finished drag persists the gate once; a flipped checkbox at once.
	if a.voiceDirty && !a.voiceGateFloat.Dragging() {
		a.voiceDirty = false
		a.persistVoice()
	}
	if a.voiceListenCb.Value != a.voiceListenWas {
		a.voiceListenWas = a.voiceListenCb.Value
		a.persistVoice()
	}

	children := []layout.FlexChild{
		layout.Rigid(a.header("VOICE")),
		layout.Rigid(func(gtx C) D { return a.pixelLabelFit(gtx, unit.Sp(8), status, col) }),
	}
	if v.Capturing {
		children = append(children,
			layout.Rigid(spacer(6)),
			layout.Rigid(func(gtx C) D { return voiceMeter(gtx, v) }),
		)
	}
	gate := func(db int) string {
		if db <= 0 {
			return "  OPEN" // no gate: the microphone publishes whenever it is unmuted
		}
		return fmt.Sprintf("%2d dB", db)
	}
	children = append(children,
		layout.Rigid(spacer(6)),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Dp(50) // the handling knobs' label column
					return a.pixelLabelFit(gtx, unit.Sp(10), "GATE", colFg)
				}),
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Max.Y = gtx.Dp(20) // a row, not a touch target
					s := material.Slider(a.th, &a.voiceGateFloat)
					s.Color = colAccent
					return s.Layout(gtx)
				}),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					return a.pixelLabelFit(gtx, unit.Sp(10), gate(a.voiceGateDb), colMuted)
				}),
			)
		}),
		layout.Rigid(spacer(4)),
		layout.Rigid(func(gtx C) D {
			cb := material.CheckBox(a.th, &a.voiceListenCb, "Play voice")
			cb.Color = colFg
			cb.IconColor = colAccent
			return cb.Layout(gtx)
		}),
	)
	if v.HasTeam {
		row := func(value, label string) layout.Widget {
			return func(gtx C) D {
				rb := material.RadioButton(a.th, &a.voiceChanEnum, value, label)
				rb.Color = colFg
				rb.IconColor = colAccent
				rb.TextSize = unit.Sp(12)
				rb.Size = unit.Dp(18)
				return rb.Layout(gtx)
			}
		}
		children = append(children,
			layout.Rigid(spacer(4)),
			layout.Rigid(row(voiceChanTeam, "Team only")),
			layout.Rigid(row(voiceChanAll, "Everyone")),
		)
	}
	// The whole section is one part to the tour (tutorial.go).
	return a.tutMark(gtx, tutVoice, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// voiceMeter is the level bar: the frame's level on a -80..0 dBFS scale
// (the gate's threshold ranges from -69 up, so its whole travel shows)
// over the panel-frame track, green while the gate is open and muted while
// it is shut, with a gold tick at the level the gate opens at — the thing
// the GATE slider moves.
func voiceMeter(gtx C, v voice.Snapshot) D {
	w, h := gtx.Constraints.Max.X, gtx.Dp(10)
	pos := func(db float32) int {
		return int(min(max((db+80)/80, 0), 1) * float32(w))
	}
	fillRect(gtx.Ops, image.Rect(0, 0, w, h), colBorder)
	fill := colMuted
	if v.Talking {
		fill = colGo
	}
	fillRect(gtx.Ops, image.Rect(0, 0, pos(v.LevelDb), h), fill)
	tick := pos(v.ThresholdDb)
	fillRect(gtx.Ops, image.Rect(max(tick-gtx.Dp(1), 0), 0, tick+gtx.Dp(1), h), colGold)
	return D{Size: image.Pt(w, h)}
}

// gameSpeakerName names a speaker on the game screen: the roster's name in
// the player's own board colour, a stranger's (a spectator's) ID in the
// muted one.
func gameSpeakerName(view gameView) func(id string) (string, colorN) {
	return func(id string) (string, colorN) {
		for i, p := range view.players {
			if p.PlayerID == id {
				return agentName(p.Name, p.Agent), render.PlayerColorRGBA(i)
			}
		}
		return id, colMuted
	}
}

// lobbySpeakerName names a speaker in the lobby: the presence list's name,
// in the plain foreground — the lobby has no board colours.
func lobbySpeakerName(players []lobby.PlayerPresence) func(id string) (string, colorN) {
	return func(id string) (string, colorN) {
		for _, p := range players {
			if p.PlayerID == id {
				return agentName(p.Name, p.Agent), colFg
			}
		}
		return id, colMuted
	}
}

// voiceStrip names whoever is heard right now, for the screen with the
// players put away: one row a speaker, the mark in the room's colour and
// the name as nameOf says. Nothing while the room is silent, so the corner
// is the content's.
func (a *App) voiceStrip(gtx C, v voice.Snapshot, nameOf func(id string) (string, colorN)) D {
	if len(v.Speakers) == 0 {
		return D{}
	}
	gtx.Constraints.Min = image.Point{}
	return background(gtx, withAlpha(colPanel, 0.85), func(gtx C) D {
		return layout.UniformInset(unit.Dp(6)).Layout(gtx, func(gtx C) D {
			var rows []layout.FlexChild
			for _, sp := range v.Speakers {
				name, col := nameOf(sp.ID)
				rows = append(rows, layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(glyphWidget(glyphSpeaker, unit.Dp(10), speakerColor(sp))),
							layout.Rigid(hSpacer(4)),
							layout.Rigid(a.pixel(unit.Sp(8), name, col).Layout),
						)
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	})
}
