from helpers import event, make_game, seat

from python_mk2.consts import MODE_COOPERATIVE


async def test_fold_individual():
    # A board scored per seat: another seat's line_clear folds the crew's
    # lines (the shared level) but never their points; the ranking names the
    # top scorer(s): ourselves from our own score, a topped-out player from
    # their game_over, everyone else from the last total their events
    # carried.
    g = make_game(mode=MODE_COOPERATIVE, individual=True)
    await g.fold_line_clear(event(kind="line_clear", player_id="other", total_score=300, total_lines=2))
    assert g.shared_score == 0 and g.total_lines == 2
    shared = make_game(mode=MODE_COOPERATIVE)
    await shared.fold_line_clear(event(kind="line_clear", player_id="other", total_score=300, total_lines=2))
    assert shared.shared_score == 300  # a shared board folds the points

    g.roster = [seat("me"), seat("other"), seat("third")]
    g.score = 250
    g.results["third"] = event(kind="game_over", player_id="third", total_score=300)
    assert g.top_scorers_locked() == {"other", "third"}  # tied at 300
    g.score = 301
    assert g.individual_winner_locked()
    for r in await g.player_results(None, None):
        assert ("winner" in r) == (r["player_id"] == "me"), r["player_id"]
