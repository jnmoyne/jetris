package nats

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/natscontext"

	"jetricks/internal/config"
)

// Connect establishes a NATS connection using the named NATS context.
// An empty contextName uses the currently selected context.
func Connect(contextName string, opts ...nats.Option) (*nats.Conn, jetstream.JetStream, natscontext.Settings, error) {
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

// ConnectURL establishes a NATS connection directly to the given URL with
// optional username/password credentials. Use this when no NATS context is
// available.
func ConnectURL(url, user, password string, opts ...nats.Option) (*nats.Conn, jetstream.JetStream, error) {
	if user != "" {
		opts = append(opts, nats.UserInfo(user, password))
	}
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
	return nc, js, kv, nil
}

// CheckConnection dials per cfg (NATSURL wins over NATSContext, like
// Bootstrap), measures the server round-trip time with a flush ping, and
// closes the connection. It provisions nothing — used by the login screen's
// "Check connection" button to validate a picker choice before playing.
func CheckConnection(cfg config.Config) (serverURL string, rtt time.Duration, err error) {
	var nc *nats.Conn
	if cfg.NATSURL != "" {
		nc, _, err = ConnectURL(cfg.NATSURL, cfg.NATSUser, cfg.NATSPassword, nats.Timeout(5*time.Second))
	} else {
		nc, _, _, err = Connect(cfg.NATSContext)
	}
	if err != nil {
		return "", 0, err
	}
	defer nc.Close()
	rtt, err = nc.RTT()
	if err != nil {
		return "", 0, err
	}
	return nc.ConnectedUrl(), rtt, nil
}

func newJetStream(nc *nats.Conn, domain string) (jetstream.JetStream, error) {
	if domain != "" {
		return jetstream.NewWithDomain(nc, domain)
	}
	return jetstream.New(nc)
}
