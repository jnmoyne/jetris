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

// PlayfieldCellMsg holds the fetched state for a single cell.
type PlayfieldCellMsg struct {
	Row     int
	Col     int
	Payload []byte
	Seq     uint64
}

// fetchChunkSize bounds the number of subjects per multi-last direct get. The
// server caps a single request at 1024 responses (413 Too Many Results, no
// pagination), so large boards are fetched in chunks bounded to a common
// stream sequence for a consistent snapshot.
const fetchChunkSize = 512

// FetchPlayfieldState retrieves the current state of the given cell subjects
// for a game in one round trip (or a few, for boards above fetchChunkSize
// cells). The caller builds the subjects using the mode-appropriate scheme
// (coop or competitive), so this function stays subject-agnostic. The returned
// cells are keyed by the (row, col) parsed from the subject, so the result is
// independent of the subject shape. Cells that have never been written have no
// last message and are simply absent from the result (empty cell).
func FetchPlayfieldState(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
	subjects []string,
) ([]PlayfieldCellMsg, error) {
	var opts []jetstreamext.GetLastForOpt
	if len(subjects) > fetchChunkSize {
		// Bound every chunk to the stream's current last sequence so the
		// combined snapshot is consistent at one point in the stream; anything
		// newer is replayed by the caller's consumer (startSeq = maxSeq+1).
		stream, err := js.Stream(ctx, config.GameStream(gameID))
		if err != nil {
			return nil, err
		}
		opts = append(opts, jetstreamext.GetLastMsgsUpToSeq(stream.CachedInfo().State.LastSeq))
	}

	var result []PlayfieldCellMsg
	for start := 0; start < len(subjects); start += fetchChunkSize {
		end := min(start+fetchChunkSize, len(subjects))
		msgs, err := jetstreamext.GetLastMsgsFor(ctx, js, config.GameStream(gameID), subjects[start:end], opts...)
		if err != nil {
			// If no messages exist yet, the board is empty so far
			if errors.Is(err, jetstreamext.ErrNoMessages) {
				continue
			}
			return nil, err
		}

		for msg, err := range msgs {
			if err != nil {
				if errors.Is(err, jetstreamext.ErrNoMessages) {
					continue
				}
				return nil, err
			}
			row, col := ParseCellFromSubject(msg.Subject)
			if row < 0 {
				continue
			}
			result = append(result, PlayfieldCellMsg{
				Row:     row,
				Col:     col,
				Payload: msg.Data,
				Seq:     msg.Sequence,
			})
		}
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

// ParseCellFromSubject extracts the (row, col) position from a cell subject
// string — the last two tokens. Returns (-1, -1) if the subject doesn't end in
// two numeric tokens.
func ParseCellFromSubject(subject string) (int, int) {
	parts := strings.Split(subject, ".")
	if len(parts) < 2 {
		return -1, -1
	}
	row, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		return -1, -1
	}
	col, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return -1, -1
	}
	return row, col
}
