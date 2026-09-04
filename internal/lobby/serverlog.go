package lobby

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// The server log (config.LogStream) is the lobby's journal: every client
// appends an entry as it connects, disconnects, or comes back from a dropped
// connection, and as it creates a game or starts one (the countdown's end).
// Agents are not journaled (see journal). The lobby reads the stream's tail
// into memory for the SERVER LOG tab.

// logCap bounds the entries kept in memory (and replayed at start: the
// consumer begins logCap entries from the stream's end, not at its start).
const logCap = 500

// LogEntries returns a snapshot of the server log entries read so far, oldest
// first.
func (l *Lobby) LogEntries() []config.LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]config.LogEntry, len(l.logEntries))
	copy(out, l.logEntries)
	return out
}

// journal appends one entry to the server log. Fire-and-forget: the log is a
// journal, not state, so a lost entry costs a line in a tab, and the callers
// are on paths (start, quit, a countdown's end) with nothing to do about a
// failure anyway. msgID, when given, deduplicates against the stream's window
// (see the expiry departures in handlePlayerUpdate). An agent's own entries
// are dropped here, except a game's start: agents are not journaled.
func (l *Lobby) journal(ctx context.Context, e config.LogEntry, msgID string) {
	if e.Agent && e.Kind != config.LogKindGameStarted {
		return
	}
	e.Time = time.Now()
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	var opts []jetstream.PublishOpt
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}
	if _, err := l.js.Publish(ctx, config.LogSubject(e.Kind), data, opts...); err != nil {
		log.Printf("server log %s: %v", e.Kind, err)
	}
}

// selfEntry is a log entry about this player.
func (l *Lobby) selfEntry(kind string) config.LogEntry {
	return config.LogEntry{Kind: kind, PlayerID: l.playerID, Name: l.name, Agent: l.isAgent}
}

// journalDeparture writes this player's disconnection, once: a clean quit
// (Leave) and the heartbeat's shutdown delete both come through here, and
// only the first of them is a departure. Called under presenceMu.
func (l *Lobby) journalDepartureLocked(ctx context.Context) {
	if l.departed {
		return
	}
	l.departed = true
	l.journal(ctx, l.selfEntry(config.LogKindDisconnected), "")
}

// runLogConsumer reads the server log's tail into logEntries and follows it.
// It creates the stream if need be — a lobby can be the first client to reach
// a server since the stream was introduced.
func (l *Lobby) runLogConsumer(ctx context.Context) {
	if err := natspkg.EnsureLogStream(ctx, l.js); err != nil {
		log.Printf("server log stream: %v", err)
		return
	}
	// Start logCap entries from the end: the tab shows the recent past, and
	// a hundred days of a busy server's journal is not worth replaying for it.
	var startSeq uint64
	if s, err := l.js.Stream(ctx, config.LogStream); err == nil {
		if info, err := s.Info(ctx); err == nil {
			if info.State.LastSeq > logCap {
				startSeq = info.State.LastSeq - logCap + 1
			}
			if startSeq < info.State.FirstSeq {
				startSeq = info.State.FirstSeq
			}
		}
	}
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, l.js, natspkg.OrderedConsumerConfig{
		Stream:        config.LogStream,
		FilterSubject: config.LogSubjectFilter,
		StartSeq:      startSeq,
	})
	if err != nil {
		log.Printf("server log consumer error: %v", err)
		return
	}
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var e config.LogEntry
			if err := json.Unmarshal(msg.Data(), &e); err != nil {
				continue
			}
			if md, err := msg.Metadata(); err == nil {
				e.Seq = md.Sequence.Stream
			}
			l.mu.Lock()
			l.logEntries = append(l.logEntries, e)
			if len(l.logEntries) > logCap {
				l.logEntries = l.logEntries[len(l.logEntries)-logCap:]
			}
			l.mu.Unlock()
			l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateLog})
		}
	}
}

// runConnWatch follows the NATS connection's status and tells the chat when
// it drops and when it is back — the player's own comings and goings, which
// the KV watcher cannot report (it is on the same connection). A return also
// refreshes presence at once (the key may have expired during a long outage,
// so other lobbies read this as a rejoin) and journals the reconnection.
func (l *Lobby) runConnWatch(ctx context.Context) {
	conn := l.js.Conn()
	if conn == nil {
		return
	}
	ch := conn.StatusChanged(nats.CONNECTED, nats.RECONNECTING, nats.DISCONNECTED, nats.CLOSED)
	lost := false
	for {
		select {
		case <-ctx.Done():
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			switch st {
			case nats.RECONNECTING, nats.DISCONNECTED:
				if lost {
					continue
				}
				lost = true
				l.systemChat("Connection to the lobby lost — reconnecting…")
			case nats.CONNECTED:
				if !lost {
					continue // the initial connection, told by the presence watcher
				}
				lost = false
				l.systemChat(fmt.Sprintf("You are back in the lobby as %s", l.name))
				pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				l.publishPresence(pctx)
				l.journal(pctx, l.selfEntry(config.LogKindReconnected), "")
				cancel()
			case nats.CLOSED:
				return
			}
		}
	}
}

// systemChat appends one system line to the lobby chat and pings the UI.
func (l *Lobby) systemChat(text string) {
	l.mu.Lock()
	cm := l.appendChatLocked(ChatMessage{Text: text, Timestamp: time.Now(), System: true})
	l.mu.Unlock()
	l.emitUpdate(LobbyUpdate{Kind: LobbyUpdateChat, ChatMsg: &cm})
}
