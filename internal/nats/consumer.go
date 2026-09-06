package nats

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"
)

// OrderedConsumerConfig configures an ordered consumer.
type OrderedConsumerConfig struct {
	Stream        string
	FilterSubject string
	StartSeq      uint64 // 0 = from beginning
	// PullMaxMessages / PullMaxBytes size the pull requests behind the
	// channel; zero keeps nats.go's defaults (500 messages, refilled at 250).
	// A whole-stream read to a remote server (a replay load) is bounded by
	// one round trip per batch at the defaults — a bigger batch is more of
	// the read in flight at once.
	PullMaxMessages int
	PullMaxBytes    int
}

// NewOrderedConsumer creates an ordered consumer and returns a channel of messages.
// The returned cancel func tears it down cleanly — including while the stream
// is quiet (a watchdog stops the iterator on cancellation, so the pump never
// hangs in Next waiting for a message that will never come).
func NewOrderedConsumer(
	ctx context.Context,
	js jetstream.JetStream,
	cfg OrderedConsumerConfig,
) (<-chan jetstream.Msg, context.CancelFunc, error) {
	consumerCfg := jetstream.OrderedConsumerConfig{}

	if cfg.FilterSubject != "" {
		consumerCfg.FilterSubjects = []string{cfg.FilterSubject}
	}
	if cfg.StartSeq > 0 {
		consumerCfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		consumerCfg.OptStartSeq = cfg.StartSeq
	} else {
		consumerCfg.DeliverPolicy = jetstream.DeliverAllPolicy
	}

	cons, err := js.OrderedConsumer(ctx, cfg.Stream, consumerCfg)
	if err != nil {
		return nil, nil, err
	}

	cctx, cancel := context.WithCancel(ctx)
	ch := make(chan jetstream.Msg, 64)

	var pullOpts []jetstream.PullMessagesOpt
	if cfg.PullMaxMessages > 0 {
		pullOpts = append(pullOpts, jetstream.PullMaxMessages(cfg.PullMaxMessages))
	}
	if cfg.PullMaxBytes > 0 {
		pullOpts = append(pullOpts, jetstream.PullMaxBytes(cfg.PullMaxBytes))
	}
	iter, err := cons.Messages(pullOpts...)
	if err != nil {
		cancel()
		return nil, nil, err
	}

	go func() {
		defer close(ch)
		defer iter.Stop()
		for {
			msg, err := iter.Next()
			if err != nil {
				// Context cancelled or consumer error
				return
			}
			select {
			case ch <- msg:
			case <-cctx.Done():
				return
			}
		}
	}()

	go func() {
		<-cctx.Done()
		iter.Stop()
	}()

	return ch, cancel, nil
}
