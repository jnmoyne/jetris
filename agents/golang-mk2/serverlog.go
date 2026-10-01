package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// The server log (the game's config.LogStream): the journal the GUI clients
// append to as they connect and disconnect and as games are created and
// started, read back into the lobby's SERVER LOG tab. Agents are not
// journaled — they come and go by the dozen and create no games of their
// own — with one exception: a game's start is the game's event, and the
// agent that ran its countdown (it readied up last) is the one peer placed
// to write it, in the GUI's format (config.LogEntry). The stream is created
// the same way the GUI creates it, so whichever reaches a server first
// leaves it as the other expects.
const (
	logStream          = "JETRIS_LOG"
	logSubjectPrefix   = "jetris.log."
	logMaxAge          = 100 * 24 * time.Hour
	logDuplicateWindow = 2 * time.Minute

	logGameStarted = "game.started"
)

func (a *Agent) ensureLogStream(ctx context.Context) error {
	_, err := a.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       logStream,
		Subjects:   []string{logSubjectPrefix + ">"},
		Storage:    jetstream.FileStorage,
		Replicas:   1,
		MaxAge:     logMaxAge,
		Duplicates: logDuplicateWindow,
	})
	return err
}

// journal appends one game entry to the server log: the game, its mode and
// its seat count. Fire-and-forget: a lost entry is a missing line in a tab,
// nothing more.
func (a *Agent) journal(ctx context.Context, kind, gameID string, mode, players int) {
	e := map[string]any{"kind": kind, "player_id": a.name, "name": a.name, "agent": true, "time": nowRFC(),
		"game_id": gameID, "mode": mode, "player_count": players}
	b, _ := json.Marshal(e)
	if _, err := a.js.Publish(ctx, logSubjectPrefix+kind, b); err != nil {
		log.Printf("server log %s: %v", kind, err)
	}
}
