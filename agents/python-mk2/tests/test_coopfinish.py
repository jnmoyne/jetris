from helpers import event, make_game

from python_mk2.consts import MODE_COMPETITIVE, MODE_COOPERATIVE


async def test_peer_coop_game_over_finishes():
    # A peer's top-out ends the crew's game for everyone, and the finish is
    # every player's to make (the topper's own may never land: its
    # connection went with it), so the agent finishes the game too once its
    # play loop returns. A rival's top-out in a competitive game, or our own
    # echo, is not that.
    crew = make_game(mode=MODE_COOPERATIVE)
    await crew.on_game_over(event(kind="game_over", player_id="other", total_score=10, total_lines=1))
    assert crew.is_ended() and await crew.finish_on_end()
    own = make_game(mode=MODE_COOPERATIVE)
    await own.on_game_over(event(kind="game_over", player_id="me"))
    assert not await own.finish_on_end(), "our own echo is the topper's finish, not a peer's"
    comp = make_game(mode=MODE_COMPETITIVE)
    await comp.on_game_over(event(kind="game_over", player_id="rival"))
    assert not comp.is_ended() and not await comp.finish_on_end()
