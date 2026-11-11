"""The agent policy of a listing (guide §2)."""

from python_mk2.agent import joinable, pick_team
from python_mk2.consts import MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.wire import player_summary


def test_agent_policy_per_team():
    """max_agents caps the agents of EACH team of a teams game: a team whose
    agent seats are taken is full for an agent while a seat stays free for a
    human, and pick_team steers round it; an invited join is exempt.
    Elsewhere the cap is on the whole game."""
    g = {"mode": MODE_TEAMS, "status": "created", "team_count": 2, "team_size": 2, "player_count": 4,
         "max_agents": 1, "players": [player_summary("bot", "", seat=0, team=0, agent=True)]}
    assert joinable(g), "a 2v2 with one agent seat per team and team B empty is not joinable"
    assert pick_team(g, -1, True) == 1, "team A's agent seat is taken"
    assert pick_team(g, 0, True) == -1, "team A's agent seat is taken"
    assert pick_team(g, 0, False) == 0, "the invitation is the permission"
    g["players"] = [player_summary("bot", "", seat=0, team=0, agent=True),
                    player_summary("hal", "", seat=2, team=1, agent=True)]
    assert not joinable(g), "an agent on each team (max 1 per team) is joinable with two seats free"
    assert pick_team(g, -1, False) >= 0, "an invited agent found no team with a free seat"
    g["max_agents"] = 2
    assert joinable(g) and pick_team(g, -1, True) >= 0, "two agent seats per team, one taken on each: not joinable"
    g["max_agents"] = 0
    assert not joinable(g), "a game closed to agents is joinable"
    # Elsewhere the cap is on the whole game.
    coop = {"mode": MODE_COOPERATIVE, "status": "created", "player_count": 3, "max_agents": 1,
            "players": [player_summary("bot", "", seat=0, agent=True)]}
    assert not joinable(coop), "a co-op game with its one agent seat taken is joinable"
    coop["max_agents"] = 2
    assert joinable(coop), "a co-op game with an agent seat free is not joinable"


def _agent():
    from python_mk2.agent import Agent, ConnChoice

    return Agent(ConnChoice(server="nats://unused"), "t", "hard")


def test_fresh_invites():
    """Pending invitations, oldest first: not the declined, the stale or the
    undated ones."""
    from datetime import datetime, timedelta, timezone

    def stamp(seconds_ago):
        return (datetime.now(timezone.utc) - timedelta(seconds=seconds_ago)).isoformat().replace("+00:00", "Z")

    a = _agent()
    a.invites = {
        "new": {"game_id": "new", "created_at": stamp(5)},
        "old": {"game_id": "old", "created_at": stamp(60), "team": 1},
        "stale": {"game_id": "stale", "created_at": stamp(121)},
        "declined": {"game_id": "declined", "created_at": stamp(1), "declined": True},
        "undated": {"game_id": "undated"},
    }
    assert [gid for gid, _ in a.fresh_invites()] == ["old", "new"]


async def test_select_game_order():
    """An explicit --join first; then a fresh invitation to a listed game
    (its team noted); then, with --auto-join, the oldest joinable listing,
    the game ID breaking ties."""
    from python_mk2.wire import now_rfc

    a = _agent()
    a.join_id = "wanted"
    assert await a.select_game() == ("wanted", False)

    def open_game(created, agents=0, max_agents=1):
        return {"mode": MODE_COOPERATIVE, "status": "created", "player_count": 3, "max_agents": max_agents,
                "created_at": created,
                "players": [player_summary(f"p{i}", "", seat=i, agent=True) for i in range(agents)]}

    a.listings = {"zz": open_game("2026-01-01T00:00:02Z"), "bb": open_game("2026-01-01T00:00:01Z"),
                  "aa": open_game("2026-01-01T00:00:01Z"), "full": open_game("2026-01-01T00:00:00Z", agents=1)}
    a.auto_join = True
    assert await a.select_game() == ("aa", False)
    a.invites = {"zz": {"game_id": "zz", "created_at": now_rfc(), "team": 1}}
    assert await a.select_game() == ("zz", True) and a.invite_team == 1
    a.invites = {"zz": {"game_id": "zz", "created_at": now_rfc()}}
    assert await a.select_game() == ("zz", True) and a.invite_team == -1
    a.stop()
    a.invites, a.auto_join = {}, False
    assert await a.select_game() == ("", False)
