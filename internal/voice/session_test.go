package voice

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"jetris/internal/config"
	"jetris/internal/testutil"
)

// A session on a real server, on a fake device, for the tests below.
type rig struct {
	t    *testing.T
	nc   *nats.Conn
	dev  *FakeDevice
	sess *Session
}

func startRig(t *testing.T, url string, cfg Config, dev *FakeDevice) *rig {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	if dev == nil {
		dev = &FakeDevice{}
	}
	s := New(cfg, dev)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.Start(ctx, nc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, nc: nc, dev: dev, sess: s}
}

// speak feeds n frames of a tone through the rig's microphone, a frame
// every few milliseconds so the far side's jitter buffer sees a stream.
func (r *rig) speak(frames [][]int16) {
	for _, f := range frames {
		r.dev.Feed(f)
		time.Sleep(4 * time.Millisecond)
	}
}

// hears reports whether pulling n frames from the rig's speakers yields
// anything above -40 dBFS.
func (r *rig) hears(n int) bool {
	for i := 0; i < n; i++ {
		if LevelDb(r.dev.Pull()) > -40 {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func speakerIDs(s Snapshot) []string {
	var ids []string
	for _, sp := range s.Speakers {
		ids = append(ids, sp.ID)
	}
	return ids
}

// Alice talks with an open microphone; bob hears her, lists her as speaking,
// and alice never lists herself. Every packet but the first carries no start
// flag.
func TestSessionAliceToBob(t *testing.T) {
	url, _ := testutil.StartServer(t)
	game := "game-1"
	alice := startRig(t, url, Config{GameID: game, PlayerID: "alice", Team: -1, GateDb: 0, Listen: true}, nil)
	bob := startRig(t, url, Config{GameID: game, PlayerID: "bob", Team: -1, GateDb: 12, Listen: true}, nil)

	var packets, starts atomic.Int32
	tap, err := bob.nc.Subscribe(config.VoiceAnySubjectFilter(game), func(m *nats.Msg) {
		p, err := Unmarshal(m.Data)
		if err != nil {
			t.Errorf("bad packet on %s: %v", m.Subject, err)
			return
		}
		packets.Add(1)
		if p.Flags&FlagStart != 0 {
			starts.Add(1)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Unsubscribe()
	if err := bob.nc.Flush(); err != nil {
		t.Fatal(err)
	}

	alice.sess.SetMuted(false)
	if alice.sess.Muted() || !alice.dev.Capturing() {
		t.Fatal("unmuting did not open the fake microphone")
	}
	alice.speak(SineFrames(440, -20, 50))

	eventually(t, "bob to receive alice's packets", func() bool { return packets.Load() >= 50 })
	if got := starts.Load(); got != 1 {
		t.Fatalf("%d packets carried the start flag, want exactly the first", got)
	}
	if !bob.hears(60) {
		t.Fatal("bob's speakers stayed silent while alice talked")
	}
	if ids := speakerIDs(bob.sess.Snapshot()); len(ids) != 1 || ids[0] != "alice" {
		t.Fatalf("bob's speakers = %v, want [alice]", ids)
	}
	if sp := bob.sess.Snapshot().Speakers[0]; sp.Team != -1 {
		t.Fatalf("alice was heard in room %d, want the all room (-1)", sp.Team)
	}
	if ids := speakerIDs(alice.sess.Snapshot()); len(ids) != 0 {
		t.Fatalf("alice lists %v as speaking — her own frames came back", ids)
	}
	if snap := alice.sess.Snapshot(); !snap.Talking || !snap.Capturing {
		t.Fatalf("alice's snapshot = %+v, want talking on an open mic", snap)
	}
	eventually(t, "the speaker mark to expire", func() bool { return len(bob.sess.Snapshot().Speakers) == 0 })
}

// With the gate up, silence publishes nothing; the onset goes out with the
// two frames before it (the pre-roll), the first of them opening the spurt.
func TestSessionGateAndPreRoll(t *testing.T) {
	url, _ := testutil.StartServer(t)
	game := "game-2"
	alice := startRig(t, url, Config{GameID: game, PlayerID: "alice", Team: -1, GateDb: 12, Listen: true}, nil)

	var mu chanPackets
	tap, err := alice.nc.Subscribe(config.VoiceSubjectFilter(game), func(m *nats.Msg) {
		p, err := Unmarshal(m.Data)
		if err != nil {
			return
		}
		mu.add(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Unsubscribe()
	if err := alice.nc.Flush(); err != nil {
		t.Fatal(err)
	}

	alice.sess.SetMuted(false)
	// A quiet room, long enough for the floor to settle; then a voice.
	alice.speak(SineFrames(200, -70, 40))
	time.Sleep(50 * time.Millisecond)
	if n := mu.count(); n != 0 {
		t.Fatalf("%d packets published from a quiet room", n)
	}
	if alice.sess.Snapshot().Talking {
		t.Fatal("the gate opened on room noise")
	}
	alice.speak(SineFrames(440, -20, 20))
	eventually(t, "the onset to publish", func() bool { return mu.count() >= 20 })
	got := mu.all()
	if got[0].Flags&FlagStart == 0 {
		t.Fatal("the first published packet does not open the spurt")
	}
	// The first packet is a pre-roll frame: two sequence numbers before the
	// loud frame's, and quiet.
	if got[2].Seq != got[0].Seq+2 {
		t.Fatalf("pre-roll seqs %d,%d,%d are not consecutive", got[0].Seq, got[1].Seq, got[2].Seq)
	}
	var pcm [FrameSamples]int16
	DecodeFrame(CodecState{Predictor: got[0].Predictor, StepIndex: got[0].StepIndex}, got[0].Data[:], pcm[:])
	if LevelDb(pcm[:]) > -50 {
		t.Fatalf("the first published packet is loud (%.1f dBFS): not a pre-roll frame", LevelDb(pcm[:]))
	}
	DecodeFrame(CodecState{Predictor: got[2].Predictor, StepIndex: got[2].StepIndex}, got[2].Data[:], pcm[:])
	if LevelDb(pcm[:]) < -30 {
		t.Fatalf("the third published packet is quiet (%.1f dBFS): not the onset", LevelDb(pcm[:]))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Flags&FlagStart != 0 {
			t.Fatalf("packet %d carries a second start flag", i)
		}
	}
}

// Teams: alice on team 0 in her team's room reaches her teammate carol and
// the spectator dave, not bob on team 1 — until she switches to everyone.
func TestSessionTeamRooms(t *testing.T) {
	url, _ := testutil.StartServer(t)
	game := "game-3"
	alice := startRig(t, url, Config{GameID: game, PlayerID: "alice", Team: 0, GateDb: 0, Listen: true}, nil)
	bob := startRig(t, url, Config{GameID: game, PlayerID: "bob", Team: 1, GateDb: 0, Listen: true}, nil)
	carol := startRig(t, url, Config{GameID: game, PlayerID: "carol", Team: 0, GateDb: 0, Listen: true}, nil)
	dave := startRig(t, url, Config{GameID: game, PlayerID: "dave", Team: -1, Spectator: true, GateDb: 0, Listen: true}, nil)

	if alice.sess.Snapshot().Channel != ChannelTeam {
		t.Fatal("a seated player in a teams game does not start in the team room")
	}
	alice.sess.SetMuted(false)
	alice.speak(SineFrames(440, -20, 30))
	eventually(t, "carol to hear alice", func() bool { return len(carol.sess.Snapshot().Speakers) == 1 })
	if sp := carol.sess.Snapshot().Speakers[0]; sp.ID != "alice" || sp.Team != 0 {
		t.Fatalf("carol hears %+v, want alice in team 0's room", sp)
	}
	if !dave.hears(40) {
		t.Fatal("the spectator did not hear the team room")
	}
	if ids := speakerIDs(bob.sess.Snapshot()); len(ids) != 0 {
		t.Fatalf("bob on the other team hears %v", ids)
	}

	alice.sess.SetChannel(ChannelAll)
	eventually(t, "the team-room mark to expire", func() bool { return len(carol.sess.Snapshot().Speakers) == 0 })
	alice.speak(SineFrames(440, -20, 30))
	eventually(t, "bob to hear alice in the all room", func() bool {
		s := bob.sess.Snapshot()
		return len(s.Speakers) == 1 && s.Speakers[0].Team == -1
	})
	if !bob.hears(40) {
		t.Fatal("bob's speakers stayed silent on the all room")
	}
	// A spectator's own frames go to everyone.
	dave.sess.SetMuted(false)
	dave.speak(SineFrames(440, -20, 30))
	eventually(t, "alice to hear the spectator", func() bool {
		_, ok := alice.sess.Snapshot().Speaking("dave")
		return ok
	})
}

// Offline (no connection): a microphone that will not open leaves the player
// muted with the reason; "Play voice" off silences the speakers but keeps
// the mark; a device that will not open at all is reported and the rest
// still works.
func TestSessionOffline(t *testing.T) {
	dev := &FakeDevice{CaptureErr: errors.New("microphone permission denied")}
	s := New(Config{GameID: "g", PlayerID: "alice", Team: -1, GateDb: 12, Listen: true}, dev)
	if err := s.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if !s.Muted() {
		t.Fatal("a new session is not muted")
	}
	s.SetMuted(false)
	if !s.Muted() {
		t.Fatal("a refused microphone left the player unmuted")
	}
	if snap := s.Snapshot(); snap.Err != "microphone permission denied" || snap.Opening || snap.Capturing {
		t.Fatalf("snapshot after a refusal = %+v", snap)
	}

	// A packet handed in by hand: heard, marked, and silenced by Listen.
	var p Packet
	p.Header = Header{VerCodec: VerCodec, Flags: FlagStart}
	tone := SineFrames(440, -12, 4)
	var st CodecState
	now := time.Now()
	for i, f := range tone {
		p.Seq = uint16(i)
		p.Predictor, p.StepIndex = st.Predictor, st.StepIndex
		EncodeFrame(&st, f, p.Data[:])
		s.Receive(config.VoiceSubject("g", "bob"), p.Marshal(nil), now)
		p.Flags = 0
	}
	s.Receive(config.VoiceSubject("g", "alice"), p.Marshal(nil), now) // her own: dropped
	s.Receive("jetris.flash.g.bob", p.Marshal(nil), now)              // not a voice subject: dropped
	s.Receive(config.VoiceSubject("g", "eve"), []byte("junk"), now)   // not a packet: dropped
	snap := s.Snapshot()
	if ids := speakerIDs(snap); len(ids) != 1 || ids[0] != "bob" {
		t.Fatalf("speakers = %v, want [bob]", ids)
	}
	if snap.NextExpiry.IsZero() {
		t.Fatal("a speaker without an expiry")
	}
	heard := false
	for i := 0; i < 4; i++ {
		if LevelDb(dev.Pull()) > -40 {
			heard = true
		}
	}
	if !heard {
		t.Fatal("bob's frames were not rendered")
	}
	s.SetListen(false)
	for i, f := range tone {
		p.Seq = uint16(4 + i)
		p.Predictor, p.StepIndex = st.Predictor, st.StepIndex
		EncodeFrame(&st, f, p.Data[:])
		s.Receive(config.VoiceSubject("g", "bob"), p.Marshal(nil), time.Now())
	}
	for i := 0; i < 4; i++ {
		if LevelDb(dev.Pull()) > -90 {
			t.Fatal("Play voice off still rendered bob")
		}
	}
	if _, ok := s.Snapshot().Speaking("bob"); !ok {
		t.Fatal("Play voice off dropped the speaker mark")
	}

	none := New(Config{GameID: "g", PlayerID: "alice", Team: -1}, &FakeDevice{OpenErr: ErrUnavailable})
	if err := none.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	defer none.Stop()
	none.SetMuted(false)
	if snap := none.Snapshot(); snap.Err != ErrUnavailable.Error() || !snap.Muted {
		t.Fatalf("snapshot without audio = %+v", snap)
	}
}

// Stopping is idempotent, and the context it was started under stops it too.
func TestSessionStop(t *testing.T) {
	dev := &FakeDevice{}
	s := New(Config{GameID: "g", PlayerID: "alice", Team: -1}, dev)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	s.SetMuted(false)
	cancel()
	eventually(t, "the session to stop on its context", func() bool { return !dev.Capturing() })
	s.Stop()
	s.Stop()
	s.SetMuted(false)
	if dev.Capturing() {
		t.Fatal("unmuting a stopped session opened the microphone")
	}
}

// chanPackets collects packets under a lock, in arrival order.
type chanPackets struct {
	mu   sync.Mutex
	pkts []Packet
}

func (c *chanPackets) add(p Packet) { c.mu.Lock(); c.pkts = append(c.pkts, p); c.mu.Unlock() }
func (c *chanPackets) count() int   { c.mu.Lock(); defer c.mu.Unlock(); return len(c.pkts) }
func (c *chanPackets) all() []Packet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Packet(nil), c.pkts...)
}
