package voice

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"jetris/internal/config"
)

// Channel is the room a player's own frames go to: everyone on the game's
// screen, or their team's (a teams game's TEAM / ALL switch).
type Channel int

const (
	ChannelAll Channel = iota
	ChannelTeam
)

// Config is what a Session is started with. Team is the seat's team index
// in a teams game and -1 otherwise (a co-op or competitive player, or a
// spectator); Spectator hears every room. GateDb and Listen are the saved
// preferences' way in (prefs.Voice); OnChange is the UI's wake-up, called
// off the UI goroutine on edges only — the microphone opening or closing,
// the gate opening or shutting, a player starting or stopping, an error —
// and never per packet.
type Config struct {
	GameID, PlayerID string
	Team             int
	Spectator        bool
	GateDb           int
	Listen           bool
	OnChange         func()
}

// Speaker is a player heard within speakerHold: who, and in which room
// (Team -1 is the "all" room).
type Speaker struct {
	ID   string
	Team int
}

// Snapshot is the session as the UI draws it, taken under the session's own
// lock — never the App's — so a frame costs the audio thread nothing.
type Snapshot struct {
	// Muted is the player's answer; Opening that an unmute is waiting on the
	// microphone (the platform's permission prompt); Capturing that the
	// microphone is open; Talking that the gate is open and frames are going
	// out. Listen is whether the others are played.
	Muted, Opening, Capturing, Talking, Listen bool
	// Channel is the room the player's own frames go to; HasTeam whether
	// there is a team room to send to at all.
	Channel Channel
	HasTeam bool
	// The meter: the last frame's level, the noise floor the gate tracks,
	// and the level the gate opens at, all in dBFS (LevelDb is -100 while
	// the microphone is closed).
	LevelDb, FloorDb, ThresholdDb float32
	GateDb                        int
	// Speakers is everyone heard within speakerHold, sorted by ID;
	// NextExpiry the earliest moment one of them drops off it (zero when
	// none), for the frame that repaints it.
	Speakers   []Speaker
	NextExpiry time.Time
	// Err is the one line the VOICE section shows in red: why the
	// microphone could not open, that voice is not in this build, or the
	// browser's "tap anywhere to enable audio". "" when all is well.
	Err string
}

// Speaking reports whether id is among the speakers.
func (s Snapshot) Speaking(id string) (Speaker, bool) {
	for _, sp := range s.Speakers {
		if sp.ID == id {
			return sp, true
		}
	}
	return Speaker{}, false
}

const (
	// speakerHold is how long after a player's last packet they still count
	// as speaking: the hangover on the sender's gate is 300 ms, so a silence
	// this long means the spurt has ended rather than a packet gone missing.
	speakerHold = 250 * time.Millisecond
	// senderIdle is how long a silent sender's jitter buffer is kept.
	senderIdle = 5 * time.Second
	// zeroFramesErr is how many all-zero frames in a row (two seconds) mean
	// the microphone is open but delivering nothing — what macOS does when
	// its privacy setting denies an app, rather than failing the open.
	zeroFramesErr = 100
	// notifyMin is the least time between two OnChange calls; a change
	// inside the window is delivered at its end rather than dropped.
	notifyMin = 50 * time.Millisecond
	// preRollFrames is how many of the frames just before the gate opened
	// go out ahead of the one that opened it, so a word's onset is not
	// clipped.
	preRollFrames = 2
)

// ErrNoSignal is the zero-signal detector's complaint.
var ErrNoSignal = errors.New("no microphone signal — check the system's microphone privacy setting")

// Session is one game screen's voice: the microphone's frames gated,
// encoded and published, and every other player's frames received, buffered
// and mixed into the speakers. Built by New, started by Start, stopped by
// Stop when the screen is left. It starts muted, every time.
type Session struct {
	cfg Config
	dev Device

	nc     *nats.Conn
	subs   []*nats.Subscription
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	capCh  chan [FrameSamples]int16
	once   sync.Once
	stop   sync.Once

	// The player's answers, read every frame by the encode loop and the
	// render callback: no lock.
	muted   atomic.Bool
	listen  atomic.Bool
	gateDb  atomic.Int32
	channel atomic.Int32

	// mu guards everything below, for microseconds at a time: the audio
	// thread's Render, the subscription's Receive and the UI's Snapshot all
	// take it and none of them holds it across anything slow.
	mu        sync.Mutex
	senders   map[string]*Jitter
	bufs      [][]int16
	scratch   [FrameSamples]int32
	opening   bool
	capturing bool
	talking   bool
	level     float32
	floor     float32
	thresh    float32
	err       string // the last SetCapture failure, or ErrNoSignal
	devErr    string // Open's failure: the build has no audio
	notice    string // the device's status line (browser)
	zeroRun   int
	zeroErr   bool

	// The notify throttle's own lock, so OnChange is never called with mu
	// held.
	nmu           sync.Mutex
	lastNotify    time.Time
	notifyPending bool

	// The encode loop's own state; no lock, it is the only goroutine on it.
	vad     VAD
	enc     CodecState
	seq     uint16
	preRoll [preRollFrames]Packet
	preN    int
	pubBuf  []byte
}

// New builds a session on a device. Nothing opens until Start.
func New(cfg Config, dev Device) *Session {
	s := &Session{
		cfg:     cfg,
		dev:     dev,
		senders: map[string]*Jitter{},
		capCh:   make(chan [FrameSamples]int16, 8),
		done:    make(chan struct{}),
		level:   -100,
		floor:   -100,
		thresh:  -100,
		pubBuf:  make([]byte, 0, PacketBytes),
	}
	s.muted.Store(true)
	s.listen.Store(cfg.Listen)
	s.gateDb.Store(int32(cfg.GateDb))
	if cfg.Team >= 0 {
		s.channel.Store(int32(ChannelTeam))
	}
	return s
}

// Config is what the session was built with.
func (s *Session) Config() Config { return s.cfg }

// Start opens the speakers, subscribes to the rooms this player hears and
// starts the encode loop. nc may be nil — an offline session (the UI's
// tests, a preview): the microphone, the gate and the meter all work,
// nothing is published and nothing arrives. A device that cannot open is
// not an error here: the session runs without audio and says so in
// Snapshot().Err, so the screen still shows who is talking. Cancelling ctx
// stops the session as Stop does.
func (s *Session) Start(ctx context.Context, nc *nats.Conn) error {
	var err error
	s.once.Do(func() {
		s.ctx, s.cancel = context.WithCancel(ctx)
		s.nc = nc
		if oerr := s.dev.Open(DeviceIO{
			Capture:      s.onCapture,
			Render:       s.Render,
			CaptureState: s.onCaptureState,
			Notice:       s.onNotice,
		}); oerr != nil {
			s.mu.Lock()
			s.devErr = oerr.Error()
			s.mu.Unlock()
		}
		if nc != nil {
			err = s.subscribe(nc)
		}
		go s.encodeLoop()
		go func() {
			<-s.ctx.Done()
			s.Stop()
		}()
	})
	return err
}

// subscribe joins the rooms: everyone hears "all"; a seated player in a
// teams game their team's room too; a spectator every room there is.
func (s *Session) subscribe(nc *nats.Conn) error {
	var filters []string
	switch {
	case s.cfg.Spectator:
		filters = []string{config.VoiceAnySubjectFilter(s.cfg.GameID)}
	case s.cfg.Team >= 0:
		filters = []string{config.VoiceSubjectFilter(s.cfg.GameID), config.VoiceTeamSubjectFilter(s.cfg.GameID, s.cfg.Team)}
	default:
		filters = []string{config.VoiceSubjectFilter(s.cfg.GameID)}
	}
	for _, f := range filters {
		sub, err := nc.Subscribe(f, func(m *nats.Msg) { s.Receive(m.Subject, m.Data, time.Now()) })
		if err != nil {
			return err
		}
		// A stalled receiver drops audio rather than hoarding seconds of
		// it: old speech is worth nothing.
		_ = sub.SetPendingLimits(256, 256*PacketBytes)
		s.subs = append(s.subs, sub)
	}
	return nil
}

// Stop ends the session: the loop, the subscriptions, the device. Safe to
// call more than once, and after the context it was started under ended.
func (s *Session) Stop() {
	s.stop.Do(func() {
		if s.cancel == nil {
			close(s.done)
			return
		}
		s.cancel()
		for _, sub := range s.subs {
			_ = sub.Unsubscribe()
		}
		s.dev.Close()
		<-s.done
	})
}

// SetMuted is the bar's mic button. Unmuting opens the microphone — the
// platform's permission prompt, where it has one, happens now, which is
// why it is the caller's goroutine (the browser wants the click's own
// moment); the outcome comes back through the device, and a refusal leaves
// the player muted with the reason in Snapshot().Err. Muting closes the
// microphone, so the platform's "in use" light goes out.
func (s *Session) SetMuted(m bool) {
	if s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if m {
		s.muted.Store(true)
		s.opening = false
		s.talking = false
		s.mu.Unlock()
		s.dev.SetCapture(false)
	} else {
		if s.devErr != "" {
			s.mu.Unlock()
			s.notify()
			return
		}
		s.muted.Store(false)
		s.opening = true
		s.err = ""
		s.zeroErr = false
		s.mu.Unlock()
		s.dev.SetCapture(true)
	}
	s.notify()
}

// Muted reports the player's answer.
func (s *Session) Muted() bool { return s.muted.Load() }

// SetChannel picks the room the player's own frames go to. A team room only
// exists for a seated player in a teams game; asked for otherwise, it is the
// "all" room.
func (s *Session) SetChannel(c Channel) {
	if c == ChannelTeam && s.cfg.Team < 0 {
		c = ChannelAll
	}
	s.channel.Store(int32(c))
}

// SetGateDb is the menu's GATE slider: dB above the noise floor, 0 an open
// microphone.
func (s *Session) SetGateDb(db int) { s.gateDb.Store(int32(db)) }

// SetListen is the menu's "Play voice": off, the others are still heard of
// (the speaker marks) but not heard.
func (s *Session) SetListen(on bool) { s.listen.Store(on) }

// Receive is the subscription's callback, exported so a test can hand a
// session a packet without a server: the sender and the room come off the
// subject, the sender's own frames are dropped, anything that is not a
// packet is dropped, and the rest goes to the sender's jitter buffer.
func (s *Session) Receive(subject string, data []byte, now time.Time) {
	pid, team, ok := config.ParseVoiceSubject(subject)
	if !ok || pid == s.cfg.PlayerID {
		return
	}
	p, err := Unmarshal(data)
	if err != nil {
		return
	}
	s.mu.Lock()
	j := s.senders[pid]
	if j == nil {
		j = &Jitter{}
		s.senders[pid] = j
	}
	wasActive := now.Sub(j.LastHeard) < speakerHold
	j.Team = team
	j.Push(p, now)
	s.mu.Unlock()
	if !wasActive {
		s.notify()
	}
}

// Render is the speakers' callback: one frame from every sender that has
// one, mixed. Senders silent for senderIdle are forgotten here.
func (s *Session) Render(out []int16) {
	now := time.Now()
	s.mu.Lock()
	n := 0
	for id, j := range s.senders {
		if now.Sub(j.LastHeard) > senderIdle {
			delete(s.senders, id)
			continue
		}
		if n == len(s.bufs) {
			s.bufs = append(s.bufs, make([]int16, FrameSamples))
		}
		if j.Pop(s.bufs[n]) {
			n++
		}
	}
	if s.listen.Load() {
		Mix(out, &s.scratch, s.bufs[:n]...)
	} else {
		clear(out)
	}
	s.mu.Unlock()
}

// Snapshot is the session for one frame of the UI.
func (s *Session) Snapshot() Snapshot {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Muted:       s.muted.Load(),
		Opening:     s.opening,
		Capturing:   s.capturing,
		Talking:     s.talking,
		Listen:      s.listen.Load(),
		Channel:     Channel(s.channel.Load()),
		HasTeam:     s.cfg.Team >= 0,
		LevelDb:     s.level,
		FloorDb:     s.floor,
		ThresholdDb: s.thresh,
		GateDb:      int(s.gateDb.Load()),
	}
	for id, j := range s.senders {
		if now.Sub(j.LastHeard) < speakerHold {
			snap.Speakers = append(snap.Speakers, Speaker{ID: id, Team: j.Team})
			if exp := j.LastHeard.Add(speakerHold); snap.NextExpiry.IsZero() || exp.Before(snap.NextExpiry) {
				snap.NextExpiry = exp
			}
		}
	}
	sort.Slice(snap.Speakers, func(i, k int) bool { return snap.Speakers[i].ID < snap.Speakers[k].ID })
	switch {
	case s.devErr != "":
		snap.Err = s.devErr
	case s.err != "":
		snap.Err = s.err
	default:
		snap.Err = s.notice
	}
	return snap
}

// onCapture is the microphone's callback: a copy and a non-blocking hand-off
// to the encode loop. A full channel means the loop is behind; the frame is
// dropped rather than the audio thread held.
func (s *Session) onCapture(pcm []int16) {
	var f [FrameSamples]int16
	copy(f[:], pcm)
	select {
	case s.capCh <- f:
	default:
	}
}

// onCaptureState is the device's answer to SetCapture. A refusal puts the
// player back on mute with the reason; a microphone that opened after the
// player muted again (the browser's prompt outlasting their patience) is
// closed straight back.
func (s *Session) onCaptureState(open bool, err error) {
	s.mu.Lock()
	s.opening = false
	s.capturing = open
	s.zeroRun, s.zeroErr = 0, false
	if err != nil {
		s.err = err.Error()
		s.muted.Store(true)
	}
	if !open {
		s.talking = false
		s.level = -100
	}
	closeAgain := open && s.muted.Load()
	s.mu.Unlock()
	if closeAgain {
		go s.dev.SetCapture(false)
	}
	s.notify()
}

// onNotice is the device's status line.
func (s *Session) onNotice(msg string) {
	s.mu.Lock()
	changed := s.notice != msg
	s.notice = msg
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// encodeLoop is the microphone's consumer: the gate, the codec and the
// publish, one frame at a time, on its own goroutine so the audio thread
// never waits on the network.
func (s *Session) encodeLoop() {
	defer close(s.done)
	for {
		select {
		case <-s.ctx.Done():
			return
		case f := <-s.capCh:
			s.encodeFrame(f[:])
		}
	}
}

func (s *Session) encodeFrame(pcm []int16) {
	s.vad.GateDb = float32(s.gateDb.Load())
	level := LevelDb(pcm)
	open, start := s.vad.Update(level)
	muted := s.muted.Load()
	if muted {
		open = false
	}

	if !muted {
		var p Packet
		p.Header = Header{VerCodec: VerCodec, Seq: s.seq, Predictor: s.enc.Predictor, StepIndex: s.enc.StepIndex}
		s.seq++
		EncodeFrame(&s.enc, pcm, p.Data[:])
		if open {
			if start {
				// The onset: the frames the gate was still shut on go out
				// first, the first of them opening the spurt on the far side.
				first := true
				for i := 0; i < s.preN; i++ {
					q := s.preRoll[i]
					if first {
						q.Flags |= FlagStart
						first = false
					}
					s.publish(&q)
				}
				if first {
					p.Flags |= FlagStart
				}
				s.preN = 0
			}
			s.publish(&p)
		} else {
			// Shut: keep the last few frames for the next onset.
			if s.preN == preRollFrames {
				copy(s.preRoll[:], s.preRoll[1:])
				s.preN--
			}
			s.preRoll[s.preN] = p
			s.preN++
		}
	}

	zero := true
	for _, v := range pcm {
		if v != 0 {
			zero = false
			break
		}
	}
	s.mu.Lock()
	s.level, s.floor, s.thresh = level, s.vad.FloorDb(), s.vad.ThresholdDb()
	changed := s.talking != open
	s.talking = open
	if zero {
		s.zeroRun++
		if s.zeroRun == zeroFramesErr && s.err == "" {
			s.err, s.zeroErr = ErrNoSignal.Error(), true
			changed = true
		}
	} else {
		s.zeroRun = 0
		if s.zeroErr {
			s.err, s.zeroErr = "", false
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// publish sends one packet to the room the switch is on — only while the
// link is up: nats.go would otherwise buffer the frames and play seconds of
// stale speech into the room on reconnect.
func (s *Session) publish(p *Packet) {
	if s.nc == nil || !s.nc.IsConnected() {
		return
	}
	subject := config.VoiceSubject(s.cfg.GameID, s.cfg.PlayerID)
	if Channel(s.channel.Load()) == ChannelTeam && s.cfg.Team >= 0 {
		subject = config.VoiceTeamSubject(s.cfg.GameID, s.cfg.Team, s.cfg.PlayerID)
	}
	s.pubBuf = p.Marshal(s.pubBuf[:0])
	_ = s.nc.Publish(subject, s.pubBuf)
}

// notify wakes the UI: at once, or — inside notifyMin of the last wake-up —
// at the end of that window, so a burst of edges costs one frame and the
// last of them is never lost.
func (s *Session) notify() {
	if s.cfg.OnChange == nil {
		return
	}
	s.nmu.Lock()
	if s.notifyPending {
		s.nmu.Unlock()
		return
	}
	now := time.Now()
	if d := now.Sub(s.lastNotify); d < notifyMin {
		s.notifyPending = true
		s.nmu.Unlock()
		time.AfterFunc(notifyMin-d, func() {
			s.nmu.Lock()
			s.notifyPending = false
			s.lastNotify = time.Now()
			s.nmu.Unlock()
			s.cfg.OnChange()
		})
		return
	}
	s.lastNotify = now
	s.nmu.Unlock()
	s.cfg.OnChange()
}
