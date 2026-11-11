from helpers import event, make_game

from python_mk2.consts import MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS


async def test_playfield_lines():
    # The goal rule: the crew's board counts every seat's lines (ours
    # included), a team's board its members', a competitive board its
    # player's; and the crossing decides the verdict once: the crew wins
    # together, a team by membership, a competitive board by ownership.
    crew = make_game(mode=MODE_COOPERATIVE, line_goal=3, lines=1, sender_totals={"a": (10, 1), "b": (20, 1)})
    assert crew.playfield_lines_locked(event(player_id="a")) == 3
    await crew.check_line_goal(event(kind="line_clear", player_id="a", total_lines=1))
    assert crew.goal_decided and crew.goal_won and crew.is_ended()

    team = make_game(mode=MODE_TEAMS, team=0, line_goal=3, lines=2,
                     sender_totals={"b0": (0, 2), "b1": (0, 1), "mate": (0, 5)},
                     sender_teams={"b0": 1, "b1": 1, "mate": 0})
    assert team.playfield_lines_locked(event(player_id="b1", team=1)) == 3
    assert team.playfield_lines_locked(event(player_id="mate", team=0)) == 7  # ours included
    await team.check_line_goal(event(kind="line_clear", player_id="b1", team=1, total_lines=1))
    assert team.goal_decided and not team.goal_won  # a loss to team B
    await team.check_line_goal(event(kind="line_clear", player_id="mate", team=0, total_lines=5))
    assert not team.goal_won, "a later crossing moved the verdict"

    comp = make_game(mode=MODE_COMPETITIVE, line_goal=2, lines=2, sender_totals={"rival": (0, 1)})
    await comp.check_line_goal(event(kind="line_clear", player_id="rival", total_lines=1))
    assert not comp.goal_decided, "the rival's 1 line decided a goal of 2"
    await comp.check_line_goal(event(kind="line_clear", player_id="me", total_lines=2))
    assert comp.goal_decided and comp.goal_won
    assert await comp.win_check(), "win_check ignores the goal's verdict"
