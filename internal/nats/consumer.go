package nats

import (
	"context"

	"github.com/nats-io/nats.go/jetstream"
)

// OrderedConsumerConfig configures an ordered consumer.
type OrderedConsumerConfig struct {
	Stream         string
	FilterSubject  string
	StartSeq       uint64 // 0 = from beginning
	ReplayOriginal bool
}

// NewOrderedConsumer creates an ordered consumer and returns a channel of messages.
// The returned cancel func tears it down cleanly.
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
	if cfg.ReplayOriginal {
		consumerCfg.ReplayPolicy = jetstream.ReplayOriginalPolicy
	}

	cons, err := js.OrderedConsumer(ctx, cfg.Stream, consumerCfg)
	if err != nil {
		return nil, nil, err
	}

	cctx, cancel := context.WithCancel(ctx)
	ch := make(chan jetstream.Msg, 64)

	iter, err := cons.Messages()
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

	return ch, cancel, nil
}
