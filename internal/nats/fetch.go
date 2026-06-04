package nats

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"

	"jetricks/internal/config"
)

// PlayfieldRowMsg holds the fetched state for a single row.
type PlayfieldRowMsg struct {
	Row     int
	Payload []byte
	Seq     uint64
}

// FetchPlayfieldState retrieves the current state of the given row subjects for
// a game in a single round trip. The caller builds the subjects using the
// mode-appropriate scheme (coop or competitive), so this function stays
// subject-agnostic. The returned rows are keyed by row index parsed from the
// subject, so the result is independent of the subject shape.
func FetchPlayfieldState(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
	subjects []string,
) ([]PlayfieldRowMsg, error) {
	msgs, err := jetstreamext.GetLastMsgsFor(ctx, js, config.GameStream(gameID), subjects)
	if err != nil {
		// If no messages exist yet, return empty
		if errors.Is(err, jetstreamext.ErrNoMessages) {
			return nil, nil
		}
		return nil, err
	}

	var result []PlayfieldRowMsg
	for msg, err := range msgs {
		if err != nil {
			if errors.Is(err, jetstreamext.ErrNoMessages) {
				continue
			}
			return nil, err
		}
		row := ParseRowFromSubject(msg.Subject)
		if row < 0 {
			continue
		}
		result = append(result, PlayfieldRowMsg{
			Row:     row,
			Payload: msg.Data,
			Seq:     msg.Sequence,
		})
	}
	return result, nil
}

// FetchGameMeta retrieves the latest game metadata message.
func FetchGameMeta(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
) (config.GameMeta, uint64, error) {
	stream, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		return config.GameMeta{}, 0, err
	}
	msg, err := stream.GetLastMsgForSubject(ctx, config.MetaSubject(gameID))
	if err != nil {
		return config.GameMeta{}, 0, err
	}
	var meta config.GameMeta
	if err := json.Unmarshal(msg.Data, &meta); err != nil {
		return config.GameMeta{}, 0, err
	}
	return meta, msg.Sequence, nil
}

// ParseRowFromSubject extracts the row number from a row subject string.
func ParseRowFromSubject(subject string) int {
	parts := strings.Split(subject, ".")
	if len(parts) == 0 {
		return -1
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return -1
	}
	return n
}
