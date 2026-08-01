package nativeui

import (
	"testing"
	"time"

	"jetris/internal/config"
)

// The archived-game viewer includes the game's preserved chat history: line
// formatting marks spectators and stamps local wall-clock time, and the
// screen renders with and without a conversation.
func TestArchiveChatLine(t *testing.T) {
	ts := time.Date(2026, 7, 23, 14, 3, 0, 0, time.Local)
	got := archiveChatLine(config.ChatLine{Name: "alice", Text: "gg", Timestamp: ts})
	if got != "14:03 alice: gg" {
		t.Errorf("archiveChatLine = %q", got)
	}
	got = archiveChatLine(config.ChatLine{Name: "bob", Text: "nice", Spectator: true})
	if got != "bob (spec): nice" {
		t.Errorf("spectator line = %q (want no timestamp, (spec) marker)", got)
	}
}

func TestArchiveViewRendersChat(t *testing.T) {
	a := newTestApp()
	rec := config.ArchiveRecord{
		GameID: "g-chat",
		Mode:   config.ModeCompetitive,
		Players: []config.PlayerResult{
			{PlayerID: "alice", Score: 10, Winner: true},
			{PlayerID: "bob", Score: 5},
		},
		Chat: []config.ChatLine{
			{Name: "alice", Text: "good luck!", Timestamp: time.Now()},
			{Name: "bob", Text: "you too"},
			{Name: "carol", Text: "go alice", Spectator: true},
		},
	}
	a.openArchive(rec)
	renderOnce(t, a) // with chat: boards + GAME CHAT panel

	rec.Chat = nil
	a.openArchive(rec)
	renderOnce(t, a) // without chat (old records): boards only
}
