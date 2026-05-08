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
	var js jetstream.JetStream
	if settings.JSDomain != "" {
		js, err = jetstream.NewWithDomain(nc, settings.JSDomain)
	} else {
		js, err = jetstream.New(nc)
	}
	if err != nil {
		nc.Close()
		return nil, nil, settings, err
	}
	return nc, js, settings, nil
}
