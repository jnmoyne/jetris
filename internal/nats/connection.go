package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/natscontext"

	"jetris/internal/config"
)

// Connect establishes a NATS connection using the named NATS context.
// An empty contextName uses the currently selected context.
func Connect(contextName string, opts ...nats.Option) (*nats.Conn, jetstream.JetStream, natscontext.Settings, error) {
	opts = append(linkOptions(), opts...)
	nc, settings, err := natscontext.Connect(contextName, opts...)
	if err != nil {
		return nil, nil, settings, err
	}
	js, err := newJetStream(nc, settings.JSDomain)
	if err != nil {
		nc.Close()
		return nil, nil, settings, err
	}
	return nc, js, settings, nil
}

// linkOptions tune nats.go for a game played over a flaky link (a tablet's
// WiFi): reconnect quickly and for as long as it takes rather than after a
// 2 s wait and 60 tries, and notice a dead socket within seconds rather
// than after nats.go's default minutes of missed pings. Every move waits
// behind a stalled publish (its ack times out after jetstream's 5 s
// default), so the sooner the link is back the shorter the freeze. Callers'
// options come after these and win.
func linkOptions() []nats.Option {
	return []nats.Option{
		nats.ReconnectWait(500 * time.Millisecond),
		nats.ReconnectJitter(100*time.Millisecond, 500*time.Millisecond),
		nats.MaxReconnects(-1),
		nats.PingInterval(5 * time.Second),
		nats.MaxPingsOutstanding(3),
	}
}

// ConnectURL establishes a NATS connection directly to the given URL with
// optional username/password credentials. Use this when no NATS context is
// available.
func ConnectURL(url, user, password string, opts ...nats.Option) (*nats.Conn, jetstream.JetStream, error) {
	opts = append(linkOptions(), opts...)
	if user != "" {
		opts = append(opts, nats.UserInfo(user, password))
	}
	// The browser build swaps TCP for a WebSocket here; the desktop build is
	// a pass-through (see transport_*.go).
	url, transport := transportOptions(url)
	opts = append(opts, transport...)
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, nil, err
	}
	js, err := newJetStream(nc, "")
	if err != nil {
		nc.Close()
		return nil, nil, err
	}
	return nc, js, nil
}

// Bootstrap connects per cfg — NATSURL wins over NATSContext, matching the CLI
// flag precedence — and provisions everything the app needs: the lobby chat
// stream, the lobby KV bucket, and the archive stream. On any failure after
// the connection is up, nc is closed before returning, so callers never
// receive a live connection together with an error. Used by both launch paths
// (flags in main, and the login screen's connection picker).
func Bootstrap(ctx context.Context, cfg config.Config) (*nats.Conn, jetstream.JetStream, jetstream.KeyValue, error) {
	var (
		nc  *nats.Conn
		js  jetstream.JetStream
		err error
	)
	if cfg.NATSURL != "" {
		// Explicit timeout so a black-holed address fails the picker's
		// "Connecting…" state promptly instead of hanging.
		nc, js, err = ConnectURL(cfg.NATSURL, cfg.NATSUser, cfg.NATSPassword, nats.Timeout(5*time.Second))
	} else {
		nc, js, _, err = Connect(cfg.NATSContext)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if err := EnsureChatStream(ctx, js); err != nil {
		nc.Close()
		return nil, nil, nil, fmt.Errorf("ensure lobby chat stream: %w", err)
	}
	kv, err := EnsureLobbyKV(ctx, js)
	if err != nil {
		nc.Close()
		return nil, nil, nil, fmt.Errorf("ensure lobby KV: %w", err)
	}
	if err := EnsureArchiveStream(ctx, js); err != nil {
		nc.Close()
		return nil, nil, nil, fmt.Errorf("ensure archive stream: %w", err)
	}
	if err := EnsureReplayStream(ctx, js); err != nil {
		nc.Close()
		return nil, nil, nil, fmt.Errorf("ensure replay stream: %w", err)
	}
	if err := EnsureLogStream(ctx, js); err != nil {
		nc.Close()
		return nil, nil, nil, fmt.Errorf("ensure server log stream: %w", err)
	}
	return nc, js, kv, nil
}

// CheckResult reports what a CheckConnection probe found: the server it
// actually reached (a URL list or a context may resolve to any of several),
// that server's ID (so callers can tell an embedded server from a stranger
// squatting the same port), the measured core NATS round trip, and how busy
// that server's Jetris lobby is: Players is the number of presence entries in
// its lobby KV bucket (humans and agents currently connected), Lobby whether
// the bucket exists at all (false = nobody has ever played there).
type CheckResult struct {
	ServerURL string
	ServerID  string
	RTT       time.Duration
	Players   int // human players in the server's lobby right now
	Agents    int // agent players (golang-mk1 and the like) in it
	Lobby     bool
}

// CheckConnection dials per cfg (NATSURL wins over NATSContext, like
// Bootstrap), measures the core NATS round-trip time, peeks at the lobby's
// player count, and closes the connection. It provisions nothing — used by
// the login screen's server browser (a row click, "Refresh all servers") and
// LAN-mode check to size up a server before playing on it.
func CheckConnection(cfg config.Config) (CheckResult, error) {
	const timeout = 5 * time.Second
	var (
		nc     *nats.Conn
		domain string
		err    error
	)
	if cfg.NATSURL != "" {
		nc, _, err = ConnectURL(cfg.NATSURL, cfg.NATSUser, cfg.NATSPassword, nats.Timeout(timeout))
	} else {
		var settings natscontext.Settings
		nc, _, settings, err = Connect(cfg.NATSContext, nats.Timeout(timeout))
		domain = settings.JSDomain
	}
	if err != nil {
		return CheckResult{}, err
	}
	defer nc.Close()
	rtt, err := CoreNATSPing(nc, timeout)
	if err != nil {
		return CheckResult{}, err
	}
	res := CheckResult{ServerURL: nc.ConnectedUrl(), ServerID: nc.ConnectedServerId(), RTT: rtt}
	js, err := newJetStream(nc, domain)
	if err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res.Players, res.Agents, res.Lobby, err = LobbyPlayerCounts(ctx, js)
	if err != nil {
		return res, fmt.Errorf("lobby: %w", err)
	}
	return res, nil
}

// LobbyPlayerCounts counts the presence entries in the server's lobby KV
// bucket — the players currently connected to that server's Jetris lobby —
// humans and agents apart (each entry says which it is: lobby.PlayerPresence's
// agent flag). Presence entries carry a per-key TTL (see PutLobbyPresence), so
// a client that vanished drops out of the count within config.PresenceTTL. A
// server nobody has ever played on has no bucket yet: that is reported as
// (0, 0, false, nil), not as an error. One watch delivers every current value
// and then a nil marker, so the count costs one round trip whatever the
// lobby's size.
func LobbyPlayerCounts(ctx context.Context, js jetstream.JetStream) (players, agents int, found bool, err error) {
	kv, err := js.KeyValue(ctx, config.LobbyKVBucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	w, err := kv.WatchAll(ctx, jetstream.IgnoreDeletes())
	if err != nil {
		return 0, 0, true, err
	}
	defer func() { _ = w.Stop() }()
	for {
		select {
		case <-ctx.Done():
			return players, agents, true, ctx.Err()
		case entry, ok := <-w.Updates():
			if !ok || entry == nil {
				return players, agents, true, nil
			}
			if !strings.HasPrefix(entry.Key(), lobbyPlayersPrefix) {
				continue
			}
			var p struct {
				Agent bool `json:"agent"`
			}
			if json.Unmarshal(entry.Value(), &p) == nil && p.Agent {
				agents++
			} else {
				players++
			}
		}
	}
}

// LobbyPlayerCount is LobbyPlayerCounts' head count, humans and agents
// together.
func LobbyPlayerCount(ctx context.Context, js jetstream.JetStream) (count int, found bool, err error) {
	players, agents, found, err := LobbyPlayerCounts(ctx, js)
	return players + agents, found, err
}

// lobbyPlayersPrefix is the key prefix of presence entries in the lobby KV
// (config.LobbyPlayerKey builds "players.<id>").
const lobbyPlayersPrefix = "players."

// CoreNATSPing measures a core NATS round trip the way the game itself uses
// the connection: subscribe to a fresh inbox, publish a message carrying the
// send timestamp to it, and wait for the server to deliver it back. That is a
// full publish → server → subscription path, unlike (*nats.Conn).RTT, which
// times the client's protocol-level PING/PONG instead.
func CoreNATSPing(nc *nats.Conn, timeout time.Duration) (time.Duration, error) {
	inbox := nats.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return 0, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	// Land the SUB (and any connection-setup writes still buffered) before
	// starting the clock, so the measurement covers only the publish leg.
	if err := nc.FlushTimeout(timeout); err != nil {
		return 0, err
	}
	if err := nc.Publish(inbox, []byte(strconv.FormatInt(time.Now().UnixNano(), 10))); err != nil {
		return 0, err
	}
	msg, err := sub.NextMsg(timeout)
	if err != nil {
		return 0, fmt.Errorf("core NATS ping: %w", err)
	}
	sentNanos, err := strconv.ParseInt(string(msg.Data), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("core NATS ping: malformed payload %q", msg.Data)
	}
	return time.Since(time.Unix(0, sentNanos)), nil
}

func newJetStream(nc *nats.Conn, domain string) (jetstream.JetStream, error) {
	if domain != "" {
		return jetstream.NewWithDomain(nc, domain)
	}
	return jetstream.New(nc)
}
