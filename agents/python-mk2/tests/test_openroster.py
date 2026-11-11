"""Open rosters: who may join when, which seat they get, and the start rule."""

from python_mk2.agent import free_seat, game_name, joinable, ready_to_start
from python_mk2.consts import MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.wire import player_summary


def seat(pid, seat=0, team=0, slot=0, ready=False):
    return player_summary(pid, "", seat=seat, team=team, team_slot=slot, ready=ready)


def listing(status, invite_only, players):
    return {"mode": MODE_COMPETITIVE, "status": status, "player_count": 3, "max_agents": 2,
            "invite_only": invite_only, "players": players}


def test_joinable_in_progress():
    """An open game takes joiners before it starts and while it runs (never
    during the countdown, never when full or closed to agents) and an invite
    game takes nobody uninvited."""
    one = [seat("a", 0)]
    assert joinable(listing("created", False, one)) and joinable(listing("in_progress", False, one))
    assert not joinable(listing("starting", False, one)) and not joinable(listing("finished", False, one))
    assert not joinable(listing("in_progress", True, one)), "a running invite game is joinable"
    full = [seat("a", 0), seat("b", 1), seat("c", 2)]
    assert not joinable(listing("in_progress", False, full)), "a full game is joinable"


def test_free_seat():
    """The lowest free seat: a departed middle seat is the next one taken."""
    s, _, ok = free_seat(listing("in_progress", False, [seat("a", 0), seat("c", 2)]), 0)
    assert ok and s == 1
    full = [seat("a", 0), seat("b", 1), seat("c", 2)]
    assert not free_seat(listing("in_progress", False, full), 0)[2], "a full game has a free seat"
    teams = {"mode": MODE_TEAMS, "team_count": 2, "team_size": 2, "player_count": 4,
             "players": [seat("a", 2, team=1, slot=0)]}
    assert free_seat(teams, 1) == (3, 1, True)
    assert free_seat(teams, 0) == (0, 0, True)


def test_ready_to_start():
    def ready(pid, s, team):
        return seat(pid, s, team=team, ready=True)

    assert ready_to_start(listing("created", False, [ready("a", 0, 0), ready("b", 1, 0), ready("c", 2, 0)]))
    assert not ready_to_start(listing("created", False, [ready("a", 0, 0)])), "a board empty, yet ready"
    assert not ready_to_start(listing("created", False, [ready("a", 0, 0), ready("b", 1, 0), seat("c", 2)]))
    coop = listing("created", False, [ready("a", 0, 0)])
    coop["mode"] = MODE_COOPERATIVE
    assert ready_to_start(coop), "an open co-op game with one ready player is not ready"
    coop["players"] = [ready("a", 0, 0), seat("b", 1)]
    assert ready_to_start(coop), "one ready player and one not: not ready"
    coop["players"] = [seat("a"), seat("b", 1)]
    assert not ready_to_start(coop), "nobody ready, yet ready"
    invite = listing("created", True, [ready("a", 0, 0)])
    invite["mode"] = MODE_COOPERATIVE
    assert not ready_to_start(invite), "an invite game short of its seats is ready"
    teams = {"mode": MODE_TEAMS, "team_count": 2, "team_size": 2, "player_count": 4}
    teams["players"] = [ready("a", 2, 1)]
    assert not ready_to_start(teams), "team A empty, yet ready"
    teams["players"] = [ready("a", 2, 1), seat("b", 0, team=0)]
    assert not ready_to_start(teams), "team A seated but not ready, yet ready"
    teams["players"] = [ready("a", 2, 1), ready("b", 0, 0)]
    assert ready_to_start(teams), "a ready player per team: not ready"
    teams["players"] = [ready("a", 2, 1), ready("b", 0, 0), seat("c", 1, team=0, slot=1)]
    assert ready_to_start(teams), "a ready player per team and a teammate not ready: not ready"


def test_game_name():
    assert game_name("friday-night") == "friday-night"
    assert game_name("  Friday  Night!! ") == "Friday-Night"
    assert game_name("a.b/c") == "a-b-c"
    assert game_name("under_score") == "under_score"
    assert game_name("!!!") == ""
    assert game_name("x" * 40) == "x" * 24
    assert game_name("é") == ""
    assert len(game_name("abcdefghijklmnopqrstuvw yz")) <= 24
