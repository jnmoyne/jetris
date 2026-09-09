package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/natscontext"
)

// Protocol constants (jetris-agent-guide.md §4, gameplays §2/§7).
const (
	codename        = "golang-mk1"
	lobbyBucket     = "JETRIS_LOBBY"
	archiveStream   = "JETRIS_ARCHIVE"
	archiveSubject  = "jetris.archive"
	chatStream      = "JETRIS_CHAT"
	modeCompetitive = 1
	headroom        = 4 // hidden spawn rows 0..3
	presenceEvery   = 5 * time.Second
	presenceTTL     = 5 * time.Minute // per-message TTL on presence writes: a crashed peer's entry self-deletes (config.PresenceTTL)
	inviteTTL       = 120 * time.Second
	gravity         = 1000 * time.Millisecond // fixed level-0 competitive gravity (gameplays §7)
)

// Atomic-batch publish headers (guide §4.3).
const (
	hBatchID     = "Nats-Batch-Id"
	hBatchSeq    = "Nats-Batch-Sequence"
	hBatchCommit = "Nats-Batch-Commit"
	hExpectLast  = "Nats-Expected-Last-Subject-Sequence"
)

// connChoice is how to reach the NATS server, with the same precedence as the
// game and the nats CLI: an explicit --server URL (with optional --user /
// --password) wins over --context, and an empty context name means the
// currently selected one.
type connChoice struct {
	server   string // NATS URL; "" = connect via context instead
	context  string // NATS context name; "" = the selected context
	user     string // used with server
	password string // used with server
}

// hosting is the --create configuration: what kind of game to host, how big,
// and how agent-friendly. nil on an Agent means "never host".
type hosting struct {
	gameName   string // what to call the game: the name IS its ID, so its stream is JETRIS_GAME_<name> and lobbies list it by name ("" = a generated UUID). Cut to what a stream name, a subject and a KV key all take by gameName()
	mode       int    // modeCooperative / modeCompetitive / modeTeams
	players    int    // seat count (per TEAM in teams mode, like the GUI's editor; min 2, teams min 1)
	teams      int    // teams mode: how many teams play each other (clamped 2..6; the total seat count is teams × players)
	extraCols  int    // shared boards: columns every seat beyond the first adds to the standard 10 (clamped 4..10)
	maxAgents  int    // agent seats, this agent included — on each team of a teams game, in the whole game elsewhere (<=0 = all seats)
	next       int    // revealed upcoming pieces (clamped 0..maxNextCount)
	holes      int    // holes per garbage row (clamped 0..4; 0 = solid, permanent rows)
	random     bool   // every garbage row draws its own hole columns (off = one draw per raise)
	guideline  bool   // attacks follow the Guideline table (0/1/2/4 rows for 1/2/3/4 lines)
	hold       bool   // the Guideline hold queue (this agent never holds; humans in the game may)
	split      bool   // shared playfields with company: deal the seven piece types out between the seats of a playfield, each playing only its ration (meta split_pieces)
	bag        string // the piece randomizer (meta bag): "" the 7-bag, "double" the double bag, "none" no bag
	extraRows  int    // shared boards: rows every seat beyond the first adds below the standard 20 (clamped 0..10; meta extra_rows)
	lineGoal   int    // the game's length in lines (meta line_goal; 0 = until top out)
	single     bool   // cooperative: score every seat on its own, the top score wins (meta scoring "individual")
	pauseAlone bool   // open games: an agent left as the only player waits for company instead of playing on (listing agents_pause_alone)
}

// Agent is one connected peer: lobby plumbing plus the game loop it runs when
// it joins. It plays all three modes — cooperative, competitive, and teams —
// as an ordinary peer over the wire protocol.
type Agent struct {
	conn       connChoice
	name       string
	difficulty string
	tn         tuning
	pub        int // publish discipline for move batches: pubSync / pubAsync / pubOptimistic (pipeline.go)
	joinID     string
	inviteTeam int           // teams: the team the current invitation names (-1 = none)
	host       *hosting      // non-nil: create one game first, then play it
	wait       time.Duration // max wait for a joined game to fill and start before un-joining
	once       bool
	autoJoin   bool

	nc     *nats.Conn
	js     jetstream.JetStream
	bucket string // the lobby KV bucket: lobbyBucket, or a private one under test
	kv     jetstream.KeyValue

	mu       sync.Mutex
	listings map[string]obj // gameID -> listing
	invites  map[string]obj // gameID -> our invitation
	streams  map[string]jetstream.Stream

	stateMu        sync.Mutex
	presenceStatus int
	currentGame    string
	game           *Game // the game being played, for the lobby watch's roster pushes (nil between games)

	rng      *mrand.Rand
	stopCh   chan struct{}
	stopOnce sync.Once
	connErr  error // set when nats.go closed the connection for good (under stateMu)
}

// newAgent builds an agent with a fresh instance id and per-difficulty tuning.
func newAgent(conn connChoice, stem, difficulty, joinID string, once, autoJoin bool, host *hosting, wait time.Duration, pub int) (*Agent, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s-%s-%s", stem, hex.EncodeToString(b[:]), difficulty)
	if len(name) > 32 {
		return nil, fmt.Errorf("agent name %q exceeds 32 characters", name)
	}
	var seed [32]byte
	_, _ = rand.Read(seed[:])
	src := mrand.NewChaCha8(seed)
	return &Agent{
		conn: conn, name: name, difficulty: difficulty, tn: difficultyTuning(difficulty), pub: pub,
		joinID: joinID, host: host, wait: wait, once: once, autoJoin: autoJoin,
		bucket:   lobbyBucket,
		listings: map[string]obj{}, invites: map[string]obj{}, streams: map[string]jetstream.Stream{},
		rng:    mrand.New(src),
		stopCh: make(chan struct{}),
	}, nil
}

func (a *Agent) stop() { a.stopOnce.Do(func() { close(a.stopCh) }) }
func (a *Agent) stopping() bool {
	select {
	case <-a.stopCh:
		return true
	default:
		return false
	}
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// gameStreamName is the per-game stream all its subjects live under.
func gameStreamName(id string) string { return "JETRIS_GAME_" + id }
func metaSubject(id string) string    { return "jetris.game." + id + ".meta" }

// stream returns a cached handle to a game's stream.
func (a *Agent) stream(ctx context.Context, gameID string) (jetstream.Stream, error) {
	a.mu.Lock()
	s := a.streams[gameID]
	a.mu.Unlock()
	if s != nil {
		return s, nil
	}
	s, err := a.js.Stream(ctx, gameStreamName(gameID))
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.streams[gameID] = s
	a.mu.Unlock()
	return s, nil
}

// ---- connection & lobby --------------------------------------------------

func (a *Agent) connect(ctx context.Context) error {
	var (
		nc       *nats.Conn
		jsDomain string
		err      error
	)
	// A vanished server is nats.go's job: it reconnects in the background
	// (2 s apart, forever) while every goroutine here just blocks on its
	// subscription, costing no CPU. The default gives up after 60 attempts
	// and CLOSES the connection, which closes every subscription — and a
	// resident agent with a dead connection is useless.
	opts := []nats.Option{
		nats.Name(a.name),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil && !a.stopping() {
				log.Printf("nats: disconnected (%v); reconnecting in the background", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("nats: reconnected to %s", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			// The lobby watcher's heartbeat check raises "consumer not
			// active" every few seconds of an outage; nats.go rebuilds that
			// consumer itself once reconnected, so it is noise until then.
			if errors.Is(err, nats.ErrConsumerNotActive) && !nc.IsConnected() {
				return
			}
			if sub != nil {
				log.Printf("%v (subscription on %q)", err, sub.Subject)
			} else {
				log.Print(err)
			}
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			// With unlimited reconnects nats.go only closes on a fatal
			// error (an authorization failure on reconnect, say) — there is
			// nothing left to wait for, so stop and let run report it.
			if a.stopping() {
				return
			}
			err := nc.LastError()
			if err == nil {
				err = nats.ErrConnectionClosed
			}
			a.stateMu.Lock()
			a.connErr = err
			a.stateMu.Unlock()
			a.stop()
		}),
	}
	if a.conn.server != "" {
		if a.conn.user != "" {
			opts = append(opts, nats.UserInfo(a.conn.user, a.conn.password))
		}
		nc, err = nats.Connect(a.conn.server, opts...)
	} else {
		// NATS-CLI-compatible contexts (credentials, TLS, JS domain included);
		// an empty name is the currently selected context.
		var settings natscontext.Settings
		nc, settings, err = natscontext.Connect(a.conn.context, opts...)
		jsDomain = settings.JSDomain
	}
	if err != nil {
		if a.conn.server == "" {
			return fmt.Errorf("connect via NATS context %q: %w (pass --server or --context)", a.conn.context, err)
		}
		return err
	}
	a.nc = nc
	if jsDomain != "" {
		a.js, err = jetstream.NewWithDomain(nc, jsDomain)
	} else {
		a.js, err = jetstream.New(nc)
	}
	if err != nil {
		return err
	}
	if a.kv, err = a.ensureLobbyBucket(ctx); err != nil {
		return err
	}
	if _, err := a.js.Stream(ctx, archiveStream); err != nil {
		if _, err := a.js.CreateStream(ctx, jetstream.StreamConfig{
			Name: archiveStream, Subjects: []string{archiveSubject}, Storage: jetstream.FileStorage}); err != nil {
			return err
		}
	}
	if err := a.ensureLogStream(ctx); err != nil {
		return err
	}
	if err := a.ensureReplayStream(ctx); err != nil {
		return err
	}
	return nil
}

// ensureLobbyBucket creates or updates the lobby bucket exactly as the game
// does: LimitMarkerTTL enables the per-message-TTL presence writes AND makes
// expiries emit watchable delete markers, so every lobby learns a vanished
// player is gone without polling. Update (not bind) also converges a plain
// bucket some earlier client may have left behind. Idempotent, so it serves
// both connect and the lobby watcher's recovery of a bucket that was deleted
// underneath a running agent.
func (a *Agent) ensureLobbyBucket(ctx context.Context) (jetstream.KeyValue, error) {
	return a.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: a.bucket, Storage: jetstream.FileStorage, LimitMarkerTTL: presenceTTL})
}

// done reports whether this connection's work is over: the agent stopped, its
// context cancelled, or the connection closed (or draining) for good.
func (a *Agent) done(ctx context.Context) bool {
	return ctx.Err() != nil || a.stopping() || a.nc.IsClosed() || a.nc.IsDraining()
}

func (a *Agent) publishPresence(ctx context.Context) error {
	a.stateMu.Lock()
	status, game := a.presenceStatus, a.currentGame
	a.stateMu.Unlock()
	p := map[string]any{"player_id": a.name, "name": a.name, "status": status, "agent": true, "last_seen": nowRFC()}
	if game != "" {
		p["game_id"] = game
	}
	b, _ := json.Marshal(p)
	// A presence write is a plain KV put carrying a per-message TTL, exactly
	// like the game's: if this agent dies without its clean exit, the entry
	// self-deletes after presenceTTL instead of haunting the lobby. The KV
	// client's Put drops TTL headers, so publish straight to the KV subject.
	_, err := a.js.Publish(ctx, "$KV."+a.bucket+".players."+a.name, b, jetstream.WithMsgTTL(presenceTTL))
	return err
}

func (a *Agent) setPresence(status int, game string) {
	a.stateMu.Lock()
	a.presenceStatus, a.currentGame = status, game
	a.stateMu.Unlock()
}

func (a *Agent) presenceLoop(ctx context.Context) {
	t := time.NewTicker(presenceEvery)
	defer t.Stop()
	for {
		// While nats.go is reconnecting a JetStream publish could only sit
		// out its ack timeout: skip the tick — the first one after the
		// reconnect restores the entry, which outlives any short outage.
		if a.nc.IsConnected() {
			if err := a.publishPresence(ctx); err != nil {
				log.Printf("presence: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-t.C:
		}
	}
}

// lobbyWatch mirrors the lobby KV into memory: game listings and our invitations.
//
// A server outage never reaches here: the watcher's subscription survives the
// reconnect and its ordered consumer resumes where it stopped. What ends a
// watch is its subscription closing underneath it — at shutdown, should the
// connection ever close for good, or when the bucket itself is deleted, as a
// shared server (demo.nats.io purges streams at will) can do to a running
// agent: the consumer's heartbeats stop, nats.go fails to rebuild it on a
// stream that is gone, and closes the subscription. A finished watch is either
// the end, or something to re-establish after a pause — recreating a vanished
// bucket first, as at connect. Never spin on it: the pause doubles while
// re-watching keeps failing, and a failure that repeats is logged once.
func (a *Agent) lobbyWatch(ctx context.Context) {
	delay, last := time.Second, ""
	for {
		started := time.Now()
		msg := "lobby watch ended; re-watching"
		if err := a.watchLobby(ctx, last != ""); err != nil {
			msg = fmt.Sprintf("lobby watch: %v", err)
		}
		if a.done(ctx) {
			return
		}
		if time.Since(started) > watchSettled {
			delay, last = time.Second, "" // it had been up: start afresh
		}
		if msg != last {
			last = msg
			log.Printf("%s (repeats not logged)", msg)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, watchRetryMax)
	}
}

const (
	watchRetryMax = 30 * time.Second // ceiling for the doubling pause between failed re-watches
	watchSettled  = 30 * time.Second // a watch up this long before ending: the next retry starts afresh
)

// watchLobby runs one lobby watcher until it ends: nil once a watch that was
// established has closed underneath it, else the error that kept one from
// being established. The watch opens with the bucket's current state and then
// a nil marker; the mirror is rebuilt aside and swapped in whole at the marker,
// so a re-watch neither shows an empty lobby meanwhile nor keeps the entries
// that vanished with the old bucket or during the gap. With report set, the
// re-established watch is logged.
func (a *Agent) watchLobby(ctx context.Context, report bool) error {
	w, err := a.kv.WatchAll(ctx)
	if err != nil {
		recreated, rerr := a.recreateLobbyBucket(ctx)
		if rerr != nil {
			return rerr
		}
		if recreated {
			w, err = a.kv.WatchAll(ctx)
		}
	}
	if err != nil {
		return err
	}
	defer w.Stop()
	invitePrefix := "invites." + a.name + "."
	listings, invites := map[string]obj{}, map[string]obj{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.stopCh:
			return nil
		case e, ok := <-w.Updates():
			if !ok {
				// nats.go closes the channel when the subscription is
				// closed: a receive would return instantly forever.
				return nil
			}
			if e == nil { // end-of-initial-data marker
				a.mu.Lock()
				a.listings, a.invites = listings, invites
				a.mu.Unlock()
				if report {
					log.Printf("lobby watch re-established: %d games, %d invitations", len(listings), len(invites))
				}
				continue
			}
			key := e.Key()
			deleted := e.Operation() == jetstream.KeyValueDelete || e.Operation() == jetstream.KeyValuePurge
			// Private maps until the marker, the shared mirror after it:
			// the lock is only needed then, but costs nothing before.
			a.mu.Lock()
			switch {
			case len(key) > 6 && key[:6] == "games.":
				gid := key[6:]
				if deleted {
					delete(listings, gid)
				} else {
					listings[gid] = toObj(e.Value())
					// The game being played: its roster follows the
					// listing (an open game's seats come and go), for the
					// deal and the verdict (game.go onRoster).
					a.stateMu.Lock()
					g := a.game
					a.stateMu.Unlock()
					if g != nil && g.id == gid {
						g.onRoster(listings[gid].players())
					}
				}
			case len(key) > len(invitePrefix) && key[:len(invitePrefix)] == invitePrefix:
				gid := key[len(invitePrefix):]
				if deleted {
					delete(invites, gid)
				} else {
					invites[gid] = toObj(e.Value())
				}
			}
			a.mu.Unlock()
		}
	}
}

// recreateLobbyBucket tells whether the lobby bucket is what a failed watch
// is missing and, if so, recreates it as at connect and reports true. Our
// presence entry went with the old bucket, so it is republished at once
// rather than on the next tick. The KeyValue handle is left alone: it holds
// nothing but names, so it works against the new bucket (same configuration)
// exactly as against the old, and the other goroutines using it need no
// coordination.
func (a *Agent) recreateLobbyBucket(ctx context.Context) (bool, error) {
	if _, err := a.js.KeyValue(ctx, a.bucket); !errors.Is(err, jetstream.ErrBucketNotFound) {
		return false, nil
	}
	if _, err := a.ensureLobbyBucket(ctx); err != nil {
		return false, fmt.Errorf("lobby bucket %s is gone and recreating it failed: %w", a.bucket, err)
	}
	log.Printf("lobby bucket %s was deleted underneath us; recreated it", a.bucket)
	_ = a.publishPresence(ctx)
	return true, nil
}

// freshInvites returns pending (not declined, not stale) invitations, oldest
// first — one KV key per game.
func (a *Agent) freshInvites() [][2]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	type inv struct {
		created string
		gid     string
		o       obj
	}
	var out []inv
	for gid, o := range a.invites {
		if o.boolv("declined") {
			continue
		}
		created := o.str("created_at")
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			if time.Since(t) > inviteTTL {
				continue
			}
		} else {
			continue
		}
		out = append(out, inv{created, gid, o})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].created < out[j].created })
	res := make([][2]any, len(out))
	for i, v := range out {
		res[i] = [2]any{v.gid, v.o}
	}
	return res
}

// consumeInvite deletes an invitation key (accept or drop-stale): the deletion
// is what tells the inviter it was handled.
func (a *Agent) consumeInvite(ctx context.Context, gameID string) {
	a.mu.Lock()
	delete(a.invites, gameID)
	a.mu.Unlock()
	_ = a.kv.Delete(ctx, "invites."+a.name+"."+gameID)
}

// declineInvite rewrites the key with declined=true so the inviter sees the
// refusal until they dismiss it.
func (a *Agent) declineInvite(ctx context.Context, gameID string) {
	a.mu.Lock()
	o := a.invites[gameID]
	delete(a.invites, gameID)
	a.mu.Unlock()
	if o == nil {
		return
	}
	o.set("declined", true)
	_, _ = a.kv.Put(ctx, "invites."+a.name+"."+gameID, o.bytes())
}

// joinable reports an open game (any mode) with a free seat this agent may
// take under the game's agent policy (guide §2): max_agents caps the agents
// of the whole game — or, in teams, of EACH team, so a free seat on at
// least one team whose agent seats are not all taken.
func joinable(g obj) bool {
	players := g.players()
	// An open game takes joiners before it starts and while it runs (its
	// seats are anyone's at any time); the countdown is the one moment it
	// does not (guide §5). An invite game takes nobody uninvited.
	switch g.str("status") {
	case "created", "in_progress":
	default:
		return false
	}
	if g.boolv("invite_only") || len(players) >= g.int("player_count") {
		return false
	}
	if g.int("mode") == modeTeams {
		return pickTeam(g, -1, true) >= 0
	}
	return g.int("max_agents") > agentsAmong(players)
}

// agentsAmong counts the roster seats agents hold.
func agentsAmong(players []playerSummary) int {
	n := 0
	for _, p := range players {
		if p.Agent {
			n++
		}
	}
	return n
}

// freeSeat is the lowest free seat of a listing — and, in teams, the lowest
// free slot of the given team, the seat being team × size + slot — as the
// GUI's lobby assigns them (guide §5.2): a seat a departed player freed is
// the next one taken, and nobody else's seat moves. ok is false when full.
func freeSeat(g obj, team int) (seat, slot int, ok bool) {
	taken := map[int]bool{}
	for _, p := range g.players() {
		taken[p.Seat] = true
	}
	if g.int("mode") == modeTeams {
		size := g.int("team_size")
		if size <= 0 {
			size = g.int("player_count") / normalizeTeamCount(g.int("team_count"))
		}
		size = max(size, 1)
		for slot := 0; slot < size; slot++ {
			if seat := team*size + slot; !taken[seat] {
				return seat, slot, true
			}
		}
		return 0, 0, false
	}
	for seat := 0; seat < g.int("player_count"); seat++ {
		if !taken[seat] {
			return seat, 0, true
		}
	}
	return 0, 0, false
}

// readyToStart is the start rule (guide §5.3): an invite game's table is
// ready when every seat is filled and everyone is ready; an open game's as
// soon as every playfield has a ready player — the crew's one board, every
// team's, every competitive board — whoever else is seated and not ready.
func readyToStart(g obj) bool {
	players := g.players()
	if len(players) == 0 {
		return false
	}
	if g.boolv("invite_only") {
		for _, p := range players {
			if !p.Ready {
				return false
			}
		}
		return len(players) >= g.int("player_count")
	}
	switch g.int("mode") {
	case modeTeams:
		for t := 0; t < normalizeTeamCount(g.int("team_count")); t++ {
			if readyOn(players, func(p playerSummary) bool { return p.Team == t }) == 0 {
				return false
			}
		}
		return true
	case modeCompetitive:
		for seat := 0; seat < g.int("player_count"); seat++ {
			if readyOn(players, func(p playerSummary) bool { return p.Seat == seat }) == 0 {
				return false
			}
		}
		return true
	default:
		return readyOn(players, func(playerSummary) bool { return true }) > 0
	}
}

// readyOn counts the ready players among those on a playfield (on: the
// team's members, a competitive board's one seat, everyone on the crew's).
func readyOn(players []playerSummary, on func(playerSummary) bool) int {
	n := 0
	for _, p := range players {
		if p.Ready && on(p) {
			n++
		}
	}
	return n
}

// pickTeam returns the team an agent should join: the invited team when the
// invitation names one (want >= 0), else the least-populated team with room —
// over however many teams the game is played between (listing team_count;
// absent is the historical two). With policy set, a team also needs a free
// AGENT seat: the listing's max_agents caps the agents of each team (guide
// §2); an invitation is the permission, so an invited join passes false.
// Returns -1 when no team has a seat for us.
func pickTeam(g obj, want int, policy bool) int {
	teamCount := normalizeTeamCount(g.int("team_count"))
	teamSize := g.int("team_size")
	if teamSize <= 0 {
		teamSize = g.int("player_count") / teamCount
	}
	counts, agents := make([]int, teamCount), make([]int, teamCount)
	for _, p := range g.players() {
		if p.Team >= 0 && p.Team < teamCount {
			counts[p.Team]++
			if p.Agent {
				agents[p.Team]++
			}
		}
	}
	room := func(t int) bool {
		return counts[t] < teamSize && (!policy || agents[t] < g.int("max_agents"))
	}
	if want >= 0 && want < teamCount {
		if room(want) {
			return want
		}
		return -1
	}
	best, bestCount := -1, teamSize
	for t := 0; t < teamCount; t++ {
		if room(t) && counts[t] < bestCount {
			best, bestCount = t, counts[t]
		}
	}
	return best
}

// selectGame picks the next game: an explicit --join, a --create host, a fresh
// invitation, or the first joinable open game. Returns (gameID, invited) or
// ("", false) when stopping.
func (a *Agent) selectGame(ctx context.Context) (string, bool) {
	if a.joinID != "" {
		gid := a.joinID
		a.joinID = ""
		return gid, false
	}
	if a.host != nil {
		host := a.host
		a.host = nil // host once; afterwards fall back to resident selection
		gid, err := a.createGame(ctx, host)
		if err != nil {
			log.Printf("create game: %v", err)
			return "", false
		}
		return gid, false
	}
	for !a.stopping() {
		handled := false
		for _, e := range a.freshInvites() {
			gid, o := e[0].(string), e[1].(obj)
			a.mu.Lock()
			_, known := a.listings[gid]
			a.mu.Unlock()
			if known {
				a.inviteTeam = -1
				if o.has("team") {
					a.inviteTeam = o.int("team")
				}
				return gid, true
			}
			a.consumeInvite(ctx, gid) // game gone: drop the stale invite
			handled = true
		}
		if handled {
			continue
		}
		if a.autoJoin {
			a.mu.Lock()
			ids := make([]string, 0, len(a.listings))
			for id := range a.listings {
				ids = append(ids, id)
			}
			listings := make(map[string]obj, len(a.listings))
			for id, g := range a.listings {
				listings[id] = g
			}
			a.mu.Unlock()
			// Oldest first (id as the tiebreak), so waiting games fill in
			// creation order and every scanning agent converges on the same one.
			sort.Slice(ids, func(i, j int) bool {
				ci, cj := listings[ids[i]].str("created_at"), listings[ids[j]].str("created_at")
				if ci != cj {
					return ci < cj
				}
				return ids[i] < ids[j]
			})
			for _, id := range ids {
				if joinable(listings[id]) {
					return id, false
				}
			}
		}
		select {
		case <-a.stopCh:
		case <-ctx.Done():
			return "", false
		case <-time.After(time.Second):
		}
	}
	return "", false
}

// joinGame CAS-appends us to the KV listing and announces our roster seat
// (guide §5.2). Returns our player index, or -1 if the game can't be joined.
func (a *Agent) joinGame(ctx context.Context, gameID string, invited bool) int {
	for {
		entry, err := a.kv.Get(ctx, "games."+gameID)
		if err != nil {
			return -1
		}
		g := toObj(entry.Value())
		players := g.players()
		for _, p := range players {
			if p.PlayerID == a.name {
				return p.Seat // already joined: our seat
			}
		}
		if g.boolv("invite_only") && !invited && g.str("creator_id") != a.name {
			return -1
		}
		// A game that is over takes nobody; an invite game's roster is
		// frozen once it starts; an open game's seats are anyone's while it
		// runs (not during the countdown).
		switch g.str("status") {
		case "created", "starting":
		case "in_progress":
			if g.boolv("invite_only") {
				return -1
			}
			// The listing says in_progress until the archiver deletes it:
			// the meta is what says an open game is over.
			if meta, _, err := a.fetchMeta(ctx, gameID); err == nil {
				if s := meta.str("status"); s == "finished" || s == "archived" || s == "cancelled" {
					return -1
				}
			}
		default:
			return -1
		}
		// The agent policy (guide §2), checked here inside the CAS loop so
		// agents racing for the last agent seat never over-fill it: agents
		// may not join at all at max_agents 0, and past that the cap is on
		// the agents of each TEAM in teams mode (pickTeam), of the whole
		// game elsewhere. An invitation IS the permission (bypasses it).
		if !invited && g.int("max_agents") <= 0 {
			return -1
		}
		if len(players) >= g.int("player_count") {
			return -1
		}
		team := 0
		if g.int("mode") == modeTeams {
			want := -1
			if invited {
				want = a.inviteTeam
			}
			if team = pickTeam(g, want, !invited); team < 0 {
				return -1 // the invited (or every) team is full, or its agent seats are
			}
		} else if !invited && agentsAmong(players) >= g.int("max_agents") {
			return -1 // the game's agent seats are taken
		}
		// The seat: the lowest free one (freeSeat) — stable, so a cell's
		// player index names one seat for the whole game.
		seat, slot, ok := freeSeat(g, team)
		if !ok {
			return -1
		}
		summary := playerSummary{PlayerID: a.name, Name: a.name, Agent: true, Seat: seat}
		if g.int("mode") == modeTeams {
			summary.Team, summary.TeamSlot = team, slot
		}
		players = append(players, summary)
		g.set("players", players)
		// An invite game full → starting; an open game starts on readiness
		// (toggleReady elects the countdown) whatever seats stay free.
		full := len(players) >= g.int("player_count") && g.boolv("invite_only") && g.str("status") == "created"
		if full {
			g.set("status", "starting")
		}
		if _, err := a.kv.Update(ctx, "games."+gameID, g.bytes(), entry.Revision()); err != nil {
			time.Sleep(50 * time.Millisecond)
			continue // CAS conflict: retry from a fresh read
		}
		sb, _ := json.Marshal(summary)
		if _, err := a.js.Publish(ctx, "jetris.game."+gameID+".roster."+a.name, sb); err != nil {
			log.Printf("roster publish: %v", err)
		}
		if full {
			a.transitionMeta(ctx, gameID, "starting")
		}
		a.setPresence(1, gameID)
		_ = a.publishPresence(ctx)
		if invited {
			a.consumeInvite(ctx, gameID)
		}
		return seat
	}
}

// createGame hosts a new competitive game — the creator's side of the
// lifecycle (guide §5), mirroring what the GUI's create row writes: the
// per-game stream (last-value-per-subject, atomic batches enabled), the
// initial meta (CAS "subject must be empty", so a colliding id fails loudly),
// the lobby KV listing others discover, and the transient game.created event.
// The caller then joins its own game like any other player. Returns the new
// game id.
func (a *Agent) createGame(ctx context.Context, h *hosting) (string, error) {
	players, teamSize, teamCount := h.players, 0, 0
	if h.mode == modeTeams {
		// The count is players PER TEAM, like the GUI's editor, and the game
		// seats one such team per team the creator asked for.
		if players < 1 {
			players = 1
		}
		teamCount = normalizeTeamCount(h.teams)
		teamSize = players
		players = teamCount * teamSize
	} else {
		// A competitive game needs an opponent — the last player standing
		// wins — while a cooperative game can be played alone, for the
		// high score, on the standard 10-column board.
		floor := 1
		if h.mode == modeCompetitive {
			floor = 2
		}
		if players < floor {
			players = floor
		}
	}
	// The agent policy: how many seats agents may take — on each team of a
	// teams game, in the whole game elsewhere (guide §2). Agent-hosted
	// games are agent-friendly by default: every seat.
	policySeats := players
	if h.mode == modeTeams {
		policySeats = teamSize
	}
	maxAgents := h.maxAgents
	if maxAgents <= 0 || maxAgents > policySeats {
		maxAgents = policySeats
	}
	// A shared board's width setting; competitive boards are always the
	// standard 10 columns, so its meta records none.
	extra := 0
	if h.mode != modeCompetitive {
		extra = extraColumns(h.extraCols)
	}
	next := min(max(h.next, 0), maxNextCount)
	holes := min(max(h.holes, 0), maxGarbageHoles)
	random := h.random && holes > 0

	// A named game IS its name: the name is the game's ID, so the stream
	// below is JETRIS_GAME_<name> and every lobby lists the game by name. The
	// claim on it is the meta publish further down — expected sequence 0
	// lands only on a stream with no meta on it — since CreateStream is
	// idempotent for an identical config and two games of one name have
	// exactly that.
	gameID := uuidV4()
	if h.gameName != "" {
		gameID = h.gameName
	}
	// The stream config every game runs on (guide §4.1): full game history
	// retained in memory (no per-subject cap — ordered consumers then never
	// skip a trimmed write, and spectators replay the game from the start),
	// with atomic publishes for the CAS batches and direct gets for
	// last-per-subject reads.
	if _, err := a.js.CreateStream(ctx, jetstream.StreamConfig{
		Name:               gameStreamName(gameID),
		Subjects:           []string{"jetris.game." + gameID + ".>"},
		AllowAtomicPublish: true,
		AllowDirect:        true,
		Storage:            jetstream.MemoryStorage,
		Retention:          jetstream.LimitsPolicy,
	}); err != nil {
		if h.gameName != "" && errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
			return "", fmt.Errorf("a game called %s already exists", gameID)
		}
		return "", fmt.Errorf("create stream: %w", err)
	}

	meta := obj{}
	meta.set("game_id", gameID)
	meta.set("mode", h.mode)
	meta.set("player_count", players)
	if teamSize > 0 {
		meta.set("team_count", teamCount)
		meta.set("team_size", teamSize)
		meta.set("team_names", teamNames(teamCount)) // the piece colours, as the GUI names them by default (guide §4.2)
	}
	if extra > 0 {
		meta.set("extra_columns", extra)
	}
	meta.set("next_count", next)
	if holes > 0 {
		meta.set("garbage_holes", holes) // omitted at 0 like the GUI's omitempty field
	}
	if random {
		meta.set("random_garbage_holes", true)
	}
	if h.guideline {
		meta.set("guideline_garbage", true)
	}
	if h.hold {
		meta.set("hold", true)
	}
	seatsOnPF := players
	if teamSize > 0 {
		seatsOnPF = teamSize
	} else if h.mode == modeCompetitive {
		seatsOnPF = 1
	}
	if h.split && seatsOnPF > 1 {
		meta.set("split_pieces", true) // only a playfield with company has pieces to split
	}
	if bag := normalizeBag(h.bag); bag != bagSingle {
		meta.set("bag", bag) // omitted for the 7-bag like the GUI's omitempty field
	}
	rows := 0
	if h.mode != modeCompetitive {
		rows = extraRows(h.extraRows)
	}
	if rows > 0 {
		meta.set("extra_rows", rows)
	}
	goal := max(h.lineGoal, 0)
	if goal > 0 {
		meta.set("line_goal", goal)
	}
	single := h.single && h.mode == modeCooperative && players > 1
	if single {
		meta.set("scoring", "individual")
	}
	meta.set("seed", uint64(time.Now().UnixNano()))
	meta.set("status", "created")
	meta.set("creator_id", a.name)
	meta.set("created_at", nowRFC())
	meta.set("piece_idx", 0)
	if _, conflict, err := a.metaPublish(ctx, gameID, meta.bytes(), 0); err != nil {
		return "", fmt.Errorf("publish meta: %w", err)
	} else if conflict {
		if h.gameName != "" {
			return "", fmt.Errorf("a game called %s already exists", gameID)
		}
		return "", fmt.Errorf("game id %s already has a meta", gameID)
	}

	listing := obj{}
	listing.set("game_id", gameID)
	listing.set("mode", h.mode)
	listing.set("status", "created")
	listing.set("player_count", players)
	if teamSize > 0 {
		listing.set("team_count", teamCount)
		listing.set("team_size", teamSize)
		listing.set("team_names", teamNames(teamCount))
	}
	if extra > 0 {
		listing.set("extra_columns", extra)
	}
	listing.set("max_agents", maxAgents)
	if h.pauseAlone {
		listing.set("agents_pause_alone", true)
	}
	listing.set("next_count", next)
	if holes > 0 {
		listing.set("garbage_holes", holes)
	}
	if random {
		listing.set("random_garbage_holes", true)
	}
	if h.guideline {
		listing.set("guideline_garbage", true)
	}
	if h.hold {
		listing.set("hold", true)
	}
	if h.split && seatsOnPF > 1 {
		listing.set("split_pieces", true)
	}
	if bag := normalizeBag(h.bag); bag != bagSingle {
		listing.set("bag", bag)
	}
	if rows > 0 {
		listing.set("extra_rows", rows)
	}
	if goal > 0 {
		listing.set("line_goal", goal)
	}
	if single {
		listing.set("scoring", "individual")
	}
	listing.set("creator_id", a.name)
	listing.set("players", []playerSummary(nil)) // no seats taken yet — everyone joins, the creator included
	listing.set("created_at", nowRFC())
	if _, err := a.kv.Put(ctx, "games."+gameID, listing.bytes()); err != nil {
		return "", fmt.Errorf("write listing: %w", err)
	}

	// Transient courtesy event (core NATS, no stream): lobbies beep/refresh on
	// it but discover the game from the KV either way.
	ev, _ := json.Marshal(map[string]any{
		"kind": "game.created", "game_id": gameID, "player_id": a.name, "time": nowRFC(),
	})
	_ = a.nc.Publish("jetris.lobby.event.game.created", ev)

	shape := ""
	policy := fmt.Sprintf("max %d agents", maxAgents)
	if teamCount > 0 {
		shape = fmt.Sprintf(" in %d teams of %d", teamCount, teamSize)
		policy += " per team"
	}
	if h.pauseAlone {
		policy += ", pausing when alone"
	}
	log.Printf("created %s game %s for %d players%s (%s, next %d, garbage holes %d, random %v, guideline garbage %v, hold %v, %s) — waiting for opponents",
		map[int]string{modeCooperative: "cooperative", modeCompetitive: "competitive", modeTeams: "teams"}[h.mode],
		gameID, players, shape, policy, next, holes, random, h.guideline, h.hold, bagLabel(h.bag))
	return gameID, nil
}

// maxGameNameLen caps a game's name, in characters — the GUI's
// config.MaxGameNameLen. A name is the game's ID, so it lands in the game's
// stream name, in every subject it writes and in its lobby KV key.
const maxGameNameLen = 24

// gameName cuts what the user typed to a name a game can carry — and so to
// the game's ID, since a named game's ID IS its name (config.GameName in the
// game's own code, and the same rule here): letters, digits and the
// underscore survive, every other run becomes one dash, the ends are trimmed
// and the whole is cut to maxGameNameLen. It is what a NATS stream name, a
// subject token and a KV key all accept, those being what a game ID has to
// be. Nothing usable in it comes back empty, and the game keeps a UUID.
func gameName(s string) string {
	name := make([]byte, 0, maxGameNameLen)
	sep := false
	for _, r := range s {
		usable := r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !usable {
			sep = true
			continue
		}
		if sep && len(name) > 0 {
			if len(name)+2 > maxGameNameLen {
				break
			}
			name = append(name, '-')
		}
		if len(name) >= maxGameNameLen {
			break
		}
		sep = false
		name = append(name, byte(r))
	}
	return string(name)
}

// uuidV4 returns a random RFC-4122 v4 UUID string — the game-id format every
// other client uses.
func uuidV4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// unjoinGame walks away from a game that never started (guide §5.6): CAS-remove
// our seat from the listing (reverting a full "starting" roster to "created" so
// the freed seat is joinable again), then purge our roster announcement so late
// joiners don't discover a ghost opponent. Pre-start only — the meta is the
// authoritative started check, and once play begins the roster is frozen.
func (a *Agent) unjoinGame(ctx context.Context, gameID string) {
	for {
		entry, err := a.kv.Get(ctx, "games."+gameID)
		if err != nil {
			break
		}
		g := toObj(entry.Value())
		open := !g.boolv("invite_only")
		if s := g.str("status"); s != "created" && s != "starting" && !(open && s == "in_progress") {
			return
		}
		if !open {
			if meta, _, err := a.fetchMeta(ctx, gameID); err == nil {
				if s := meta.str("status"); s != "created" && s != "starting" {
					return
				}
			}
		}
		players := g.players()
		kept := make([]playerSummary, 0, len(players))
		for _, p := range players {
			if p.PlayerID != a.name {
				kept = append(kept, p)
			}
		}
		if len(kept) == len(players) {
			break // not on the roster; nothing to remove
		}
		g.set("players", kept)
		g2 := toObj(g.bytes())
		if g.str("status") == "starting" && !readyToStart(g2) {
			g.set("status", "created")
		}
		if _, err := a.kv.Update(ctx, "games."+gameID, g.bytes(), entry.Revision()); err != nil {
			time.Sleep(50 * time.Millisecond)
			continue // CAS conflict: retry from a fresh read
		}
		if s, err := a.stream(ctx, gameID); err == nil {
			_ = s.Purge(ctx, jetstream.WithPurgeSubject("jetris.game."+gameID+".roster."+a.name))
		}
		ev, _ := json.Marshal(map[string]any{
			"kind": "game.left", "game_id": gameID, "player_id": a.name, "time": nowRFC(),
		})
		_ = a.nc.Publish("jetris.lobby.event.game.left", ev)
		break
	}
	a.setPresence(0, "")
	_ = a.publishPresence(ctx)
}

// toggleReady CAS-sets our ready flag; returns true if our toggle completed the
// set (all seats filled, everyone ready) — then WE run the countdown.
func (a *Agent) toggleReady(ctx context.Context, gameID string) bool {
	for {
		entry, err := a.kv.Get(ctx, "games."+gameID)
		if err != nil {
			return false
		}
		g := toObj(entry.Value())
		if g.str("status") == "in_progress" {
			return false
		}
		players := g.players()
		for i := range players {
			if players[i].PlayerID == a.name {
				players[i].Ready = true
			}
		}
		g.set("players", players)
		// The start rule (readyToStart, guide §5.3). An invite game's table
		// is ready when full and ready — the toggle that completes it runs
		// the countdown. An open game's is ready as soon as every playfield
		// has a ready player: the one write that moves the listing from
		// created to starting is elected to run the countdown.
		ready := readyToStart(g)
		open := !g.boolv("invite_only")
		elected := ready && !open
		if ready && open && g.str("status") == "created" {
			g.set("status", "starting")
			elected = true
		}
		if _, err := a.kv.Update(ctx, "games."+gameID, g.bytes(), entry.Revision()); err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		return elected
	}
}

func (a *Agent) fetchMeta(ctx context.Context, gameID string) (obj, uint64, error) {
	s, err := a.stream(ctx, gameID)
	if err != nil {
		return nil, 0, err
	}
	raw, err := s.GetLastMsgForSubject(ctx, metaSubject(gameID))
	if err != nil {
		return nil, 0, err
	}
	return toObj(raw.Data), raw.Sequence, nil
}

// transitionMeta CAS-advances the meta to a new status; never regresses a
// completed game.
func (a *Agent) transitionMeta(ctx context.Context, gameID, status string) bool {
	for i := 0; i < 5; i++ {
		meta, seq, err := a.fetchMeta(ctx, gameID)
		if err != nil {
			// A blip: the next attempt may see the stream again.
			log.Printf("transition %s to %s: fetch meta: %v", gameID, status, err)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		cur := meta.str("status")
		if (cur == "finished" || cur == "archived" || cur == "cancelled") && status != "archived" {
			return false
		}
		if cur == status {
			return true
		}
		meta.set("status", status)
		if status == "in_progress" {
			meta.set("started_at", nowRFC())
		}
		if status == "finished" {
			meta.set("finished_at", nowRFC())
		}
		_, conflict, err := a.metaPublish(ctx, gameID, meta.bytes(), seq)
		if err == nil && !conflict {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// metaPublish publishes the meta with an expected-last-subject-sequence CAS.
func (a *Agent) metaPublish(ctx context.Context, gameID string, payload []byte, expectLast uint64) (uint64, bool, error) {
	return a.casRequest(ctx, metaSubject(gameID), payload, nats.Header{hExpectLast: []string{fmt.Sprint(expectLast)}})
}

// casRequest publishes one message as a JetStream request (headers may carry a
// CAS expectation or batch markers) and interprets the ack: (seq, casConflict,
// err).
func (a *Agent) casRequest(ctx context.Context, subject string, payload []byte, headers nats.Header) (uint64, bool, error) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := a.nc.RequestMsgWithContext(rctx, &nats.Msg{Subject: subject, Data: payload, Header: headers})
	if err != nil {
		return 0, false, err
	}
	var ack pubAck
	if len(resp.Data) > 0 {
		_ = json.Unmarshal(resp.Data, &ack)
	}
	if ack.Error != nil {
		if ack.isCASConflict() {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("publish %s: %s", subject, ack.Error.Description)
	}
	return ack.Seq, false, nil
}

// runCountdown publishes the 5→0 countdown, then transitions the meta to
// in_progress.
func (a *Agent) runCountdown(ctx context.Context, gameID string) {
	subject := "jetris.game." + gameID + ".countdown"
	for i := 5; i > 0; i-- {
		b, _ := json.Marshal(map[string]int{"seconds": i})
		_, _ = a.js.Publish(ctx, subject, b)
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-time.After(time.Second):
		}
	}
	b, _ := json.Marshal(map[string]int{"seconds": 0})
	_, _ = a.js.Publish(ctx, subject, b)
	time.Sleep(700 * time.Millisecond)
	if a.transitionMeta(ctx, gameID, "in_progress") {
		// The countdown ran out and the game is on: journal it, with the
		// game's shape from its meta, and mirror the status onto the listing
		// — where the lobby rows, the join rules and every agent read that
		// the game is on (an open game's free seats are joinable from here).
		if meta, _, err := a.fetchMeta(ctx, gameID); err == nil {
			a.journal(ctx, logGameStarted, gameID, meta.int("mode"), meta.int("player_count"))
		}
		a.setListingStatus(ctx, gameID, "in_progress")
	}
}

// randID returns n random hex chars (batch ids).
func randID(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// run is the agent's main loop: connect, keep presence + lobby mirror alive,
// and play games as they are selected.
func (a *Agent) run(ctx context.Context) error {
	if err := a.connect(ctx); err != nil {
		return err
	}
	log.Printf("%s connected to %s", a.name, a.nc.ConnectedUrl())
	_ = a.publishPresence(ctx)
	go a.presenceLoop(ctx)
	go a.lobbyWatch(ctx)
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = a.kv.Delete(dctx, "players."+a.name)
		a.stop() // our own close: the closed handler must not mistake it for a loss
		_ = a.nc.Drain()
	}()
	for !a.stopping() {
		gameID, invited := a.selectGame(ctx)
		if gameID == "" {
			break
		}
		idx := a.joinGame(ctx, gameID, invited)
		if idx < 0 {
			if invited {
				a.declineInvite(ctx, gameID)
			}
			select {
			case <-a.stopCh:
			case <-time.After(time.Second):
			}
			continue
		}
		log.Printf("joined game %s as player %d", gameID, idx)
		won := a.playGame(ctx, gameID, idx)
		log.Printf("game %s over: %s", gameID, map[bool]string{true: "won", false: "lost"}[won])
		if a.once {
			break
		}
	}
	a.stateMu.Lock()
	connErr := a.connErr
	a.stateMu.Unlock()
	if connErr != nil {
		return fmt.Errorf("NATS connection closed: %w", connErr)
	}
	return nil
}

// playGame runs one game and restores lobby presence afterward.
func (a *Agent) playGame(ctx context.Context, gameID string, idx int) bool {
	g := newGame(a, gameID, idx)
	a.stateMu.Lock()
	a.game = g
	a.stateMu.Unlock()
	won := g.run(ctx)
	a.stateMu.Lock()
	a.game = nil
	a.stateMu.Unlock()
	a.setPresence(0, "")
	_ = a.publishPresence(ctx)
	return won
}

// setListingStatus mirrors a status the meta reached onto the lobby listing
// (a CAS loop; a listing already there, or further along, is left alone).
func (a *Agent) setListingStatus(ctx context.Context, gameID, status string) {
	for attempt := 0; attempt < 5; attempt++ {
		entry, err := a.kv.Get(ctx, "games."+gameID)
		if err != nil {
			return
		}
		g := toObj(entry.Value())
		switch g.str("status") {
		case "created", "starting":
		default:
			return // there already, or past it
		}
		g.set("status", status)
		if _, err := a.kv.Update(ctx, "games."+gameID, g.bytes(), entry.Revision()); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
