from datetime import datetime, timedelta, timezone

from helpers import event, make_game, seat

from python_mk2.consts import MODE_COMPETITIVE
from python_mk2.scoring import ClearInfo, PlayerStats
from python_mk2.wire import dumps


async def test_stats_tally():
    # Every seat's tally: a peer's line_clear counts the clear by its names
    # once (a replay of one already folded counts nothing), the attack
    # follows the game's rule, a game_over adds nothing but the seat's
    # pieces, our own locks are counted as we score them, and the record's
    # `stats` carries the tally with the time each seat played: to its
    # game_over, or to the finish.
    g = make_game(mode=MODE_COMPETITIVE, guideline=True)
    quad = event(kind="line_clear", player_id="rival", lines_cleared=4, score=800, total_score=800, total_lines=4,
                 piece_count=12)
    await g.fold_line_clear(quad)
    await g.fold_line_clear(quad)  # the ordered stream read again
    await g.fold_line_clear(event(kind="line_clear", player_id="rival", lines_cleared=2, t_spin=2, back_to_back=True,
                                  combo=1, score=500, total_score=1300, total_lines=6, piece_count=15))
    await g.fold_line_clear(event(kind="line_clear", player_id="rival", t_spin=1, score=100, total_score=1400,
                                  total_lines=6, piece_count=16))
    await g.fold_line_clear(event(kind="game_over", player_id="rival", total_score=1400, total_lines=6,
                                  piece_count=18))
    rival = g.sender_stats.get("rival")
    assert rival is not None, "no tally for the rival"
    # The quad (4 rows), the Back-to-Back T-spin double (4+2), the Mini
    # T-spin that cleared nothing.
    assert rival == PlayerStats(quads=1, doubles=1, t_spins=1, mini_t_spins=1, back_to_backs=1, max_combo=1,
                                attack=10)
    assert g.sender_pieces["rival"] == 18  # the game_over's

    # Our own lock, counted as scored: a plain triple sends two rows.
    async with g.mu:
        g.stats_for(g.a.name).tally(ClearInfo(lines=3), 0, True, True)
    me = g.sender_stats.get("me")
    assert me is not None and me.triples == 1 and me.attack == 2

    # The record: the rival played to their game_over, we to the finish, and
    # a seat nobody heard from carries a time and nothing else.
    started = datetime(2026, 9, 1, 10, 0, 0, tzinfo=timezone.utc)
    g.out_at["rival"] = started + timedelta(seconds=90)
    g.results["rival"] = event(kind="game_over", player_id="rival", total_score=1400, total_lines=6, piece_count=18)
    g.eliminated["rival"] = True
    g.roster = [seat("me"), seat("rival"), seat("quiet")]
    g.piece_idx = 40
    rows = await g.player_results(started, started + timedelta(minutes=2))
    by_id = {r["player_id"]: r for r in rows}
    st = by_id["rival"]["stats"]
    assert st["played_ms"] == 90_000 and st["quads"] == 1 and by_id["rival"]["piece_count"] == 18
    st = by_id["me"]["stats"]
    assert st["played_ms"] == 120_000 and st["triples"] == 1 and by_id["me"]["piece_count"] == 40
    assert by_id["quiet"]["stats"] == {"played_ms": 120_000}  # the time played alone
    # Verdicts ride along: the rival is out, the rest are not.
    assert "winner" not in by_id["rival"] and by_id["me"].get("winner") is True
    # The JSON carries the field names every reader of the record expects.
    b = dumps(by_id["rival"]["stats"]).decode()
    for key in ('"quads":1', '"doubles":1', '"t_spins":1', '"mini_t_spins":1', '"back_to_backs":1', '"max_combo":1',
                '"attack":10', '"played_ms":90000'):
        assert key in b, f"stats JSON {b} lacks {key}"
    # Nothing played, no start: no time at all, and an empty tally.
    rows = await g.player_results(None, None)
    assert {r["player_id"]: r for r in rows}["quiet"]["stats"] == {}


async def test_stats_line():
    # The game's account in one greppable line: the fields the benchmark
    # script reads off it, in the reference agent's format.
    from python_mk2.consts import MODE_COOPERATIVE

    g = make_game(mode=MODE_COOPERATIVE, idx=1, piece_idx=30, lines=7, score=1200, shared_score=4100)
    g.st.cas_losses, g.st.walk_waits, g.st.walk_wait_ms = 3, 2, 450
    g.st.deferred_spawns, g.st.deferred_ms = 1, 120
    g.st.claims_yielded, g.st.dep_waits, g.st.dep_wait_ms = 4, 5, 900
    g.st.note_replan("droprow")
    g.st.note_replan("droprow")
    g.st.note_replan("collapse")
    async with g.mu:
        line = g.stats_line()
    assert line.startswith("stats: game=g mode=coop seat=1 coordinate=false pieces=30 lines=7 score=1200 "
                           "shared_score=4100 cas_losses=3 walk_waits=2/450ms deferred_spawns=1/120ms "
                           "spawn_unblocks=0 idle_vacates=0 lock_retries=0 locks_dropped=0 "
                           "replans=collapse:1,droprow:2 claims_yielded=4 dep_waits=5/900ms echo_timeouts=0 "
                           "duration=0.0s"), line
    quiet = make_game(mode=MODE_COMPETITIVE, score=50)
    async with quiet.mu:
        line = quiet.stats_line()
    assert " mode=competitive " in line and " shared_score=50 " in line and " replans=none " in line
