package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// An open game's listing outlives its finish — for the archive's grace
// period, or for good when the archiver's client went away — and the
// listing alone says in_progress. The meta is what says the game is over,
// and a game that is over takes nobody.
func TestJoinRefusesFinishedOpenGame(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()
	gameID, err := a.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 4, LineGoal: 100, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	a.StartGame(ctx, gameID)
	waitListing(t, b, gameID, "the start", func(g GameListing) bool { return g.Status == config.GameStatusInProgress })

	// The finish lands on the meta; the listing lingers.
	meta, seq, err := natspkg.FetchGameMeta(ctx, a.GetJS(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	meta.Status = config.GameStatusFinished
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, a.GetJS(), gameID, data, seq); err != nil {
		t.Fatal(err)
	}

	if _, err := b.JoinGame(ctx, gameID, 0); !errors.Is(err, ErrGameOver) {
		t.Fatalf("joining a finished open game: err = %v, want ErrGameOver", err)
	}
	if g := b.Games()[gameID]; len(g.Players) != 1 {
		t.Fatalf("the finished game's roster grew to %d", len(g.Players))
	}
}
