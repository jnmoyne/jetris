package lobby

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
)

// A named game IS its name: CreateGame gives it the name as its ID, so the
// lobby lists it under the name and its stream is JETRIS_GAME_<name> —
// identifiable at a glance beside the UUID-named streams of unnamed games.
func TestCreateGameByName(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.GameSpec{
		Name: "Friday night!", Mode: config.ModeCooperative, PlayerCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	// "Friday night!" is not a stream name, a subject token or a KV key, so
	// the name the game takes is the one that is all three (config.GameName).
	if gameID != "Friday-night" {
		t.Fatalf("game ID = %q, want the name Friday-night", gameID)
	}
	if _, err := js.Stream(ctx, config.GameStream(gameID)); err != nil {
		t.Fatalf("stream %s: %v", config.GameStream(gameID), err)
	}
	if _, err := lb.kv.Get(ctx, config.LobbyGameKey(gameID)); err != nil {
		t.Fatalf("listing for %s: %v", gameID, err)
	}
}

// An unnamed game is dealt a generated ID, as it always was.
func TestCreateGameWithoutName(t *testing.T) {
	lb, _ := setupLobby(t)

	gameID, err := lb.CreateGame(context.Background(), config.GameSpec{
		Mode: config.ModeCooperative, PlayerCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.IsGameName(gameID) {
		t.Fatalf("game ID %q reads as a name; want a generated one", gameID)
	}
}

// A name is the game's ID, so two games can no more share one than they
// could share an ID: the second create is refused, and the first game's
// stream is left alone.
func TestCreateGameNameTaken(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	spec := config.GameSpec{Name: "tournament", Mode: config.ModeCooperative, PlayerCount: 2}
	if _, err := lb.CreateGame(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.CreateGame(ctx, spec); !errors.Is(err, ErrGameNameTaken) {
		t.Fatalf("second create: err = %v, want ErrGameNameTaken", err)
	}
	if _, err := js.Stream(ctx, config.GameStream("tournament")); err != nil {
		t.Fatalf("the first game's stream is gone: %v", err)
	}
}

// The name is free again once the game holding it is deleted — its stream and
// its listing go together (DeleteGame), and both are what the name is claimed
// with.
func TestCreateGameNameFreedByDelete(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	spec := config.GameSpec{Name: "rematch", Mode: config.ModeCooperative, PlayerCount: 2}
	if _, err := lb.CreateGame(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := lb.DeleteGame(ctx, "rematch"); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.CreateGame(ctx, spec); err != nil {
		t.Fatalf("re-create after delete: %v", err)
	}
}

// The name's real claim is the meta on the game's stream: a game holding a
// name is refused a second creator even where its lobby row has gone, so a
// create can never land on the stream a game is being played on.
func TestCreateGameNameHeldByStreamAlone(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	spec := config.GameSpec{Name: "in-progress", Mode: config.ModeCooperative, PlayerCount: 2}
	if _, err := lb.CreateGame(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := lb.kv.Delete(ctx, config.LobbyGameKey("in-progress")); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.CreateGame(ctx, spec); !errors.Is(err, ErrGameNameTaken) {
		t.Fatalf("create onto a live game's stream: err = %v, want ErrGameNameTaken", err)
	}
}

// A listing under the name holds it even where the stream has gone — an
// archived game whose row is still up — so the create is refused before it
// would overwrite that row.
func TestCreateGameNameHeldByListingAlone(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	spec := config.GameSpec{Name: "leftovers", Mode: config.ModeCooperative, PlayerCount: 2}
	if _, err := lb.CreateGame(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := js.DeleteStream(ctx, config.GameStream("leftovers")); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.CreateGame(ctx, spec); !errors.Is(err, ErrGameNameTaken) {
		t.Fatalf("create over a standing listing: err = %v, want ErrGameNameTaken", err)
	}
}

// Two creators reaching for one name at the same moment: exactly one gets it,
// and the loser is told the name is taken rather than landing on the winner's
// game. Neither has a listing to go on yet — the claim is the meta's CAS.
func TestCreateGameNameRace(t *testing.T) {
	lb, js := setupLobby(t)
	other := New(js, lb.kv, "player-2", "Bob")

	const name = "photo-finish"
	spec := config.GameSpec{Name: name, Mode: config.ModeCooperative, PlayerCount: 2}
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, creator := range []*Lobby{lb, other} {
		go func() {
			<-start
			id, err := creator.CreateGame(context.Background(), spec)
			results <- result{id, err}
		}()
	}
	close(start)

	won, lost := 0, 0
	for range 2 {
		r := <-results
		switch {
		case r.err == nil && r.id == name:
			won++
		case errors.Is(r.err, ErrGameNameTaken):
			lost++
		default:
			t.Fatalf("create = (%q, %v), want the name or ErrGameNameTaken", r.id, r.err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("%d creators got the name and %d were refused; want 1 and 1", won, lost)
	}
}

// "lobby" is what the chat stream and the voice rooms call the lobby's own
// channels, so no game may be named it.
func TestCreateGameReservedName(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	_, err := lb.CreateGame(ctx, config.GameSpec{
		Name: "Lobby", Mode: config.ModeCooperative, PlayerCount: 2,
	})
	if !errors.Is(err, ErrGameNameReserved) {
		t.Fatalf("err = %v, want ErrGameNameReserved", err)
	}
	if _, err := js.Stream(ctx, config.GameStream("Lobby")); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Errorf("a refused name left a stream behind: %v", err)
	}
}
