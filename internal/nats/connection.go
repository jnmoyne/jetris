package nats

import (
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/natscontext"
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

func newJetStream(nc *nats.Conn, domain string) (jetstream.JetStream, error) {
	if domain != "" {
		return jetstream.NewWithDomain(nc, domain)
	}
	return jetstream.New(nc)
}
