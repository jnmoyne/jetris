"""The path a piece takes to its plan: round the stack, round a crewmate's
piece, and out of a teammate's spawn box."""

from helpers import lock_column, path_test_game

from python_mk2.path import MOVE_CW, MOVE_DOWN, MOVE_LEFT, MOVE_RIGHT, Active, SpawnBox
from python_mk2.pieces import SPAWN_ROW, piece_cells
from python_mk2.planner import Placement
from python_mk2.wire import WireCell


def replay_path(p, moves):
    """Every state the moves take the piece through, the start left out."""
    row, col, orient = p.row, p.col, p.orient
    out = []
    for m in moves:
        if m == MOVE_LEFT:
            col -= 1
        elif m == MOVE_RIGHT:
            col += 1
        elif m == MOVE_DOWN:
            row += 1
        elif m == MOVE_CW:
            orient = (orient + 1) & 3
        out.append((row, col, orient))
    return out


async def test_path_reaches_the_goal_on_a_free_board():
    # On a free board the path is the rotations and the shifts, no more, and
    # it ends at the goal on the row it started.
    g = path_test_game()
    p = Active(2, 0, SPAWN_ROW, g.spawn_c)
    res = g.find_path(p, Placement(orient=1, col=g.spawn_c + 3), [])
    assert res.reached and res.end == Active(2, 1, SPAWN_ROW, g.spawn_c + 3)
    assert len(res.moves) == 4 and res.downs == 0 and MOVE_DOWN not in res.moves


async def test_path_stopped_by_the_stack():
    # A goal the stack walls off is out of reach for good: not transient,
    # and the waiting spot is the reachable state nearest it.
    g = path_test_game()
    lock_column(g, 8, 0)
    p = Active(1, 0, SPAWN_ROW, g.spawn_c)
    res = g.find_path(p, Placement(orient=0, col=11), [])
    assert not res.reached and not res.blocked_by_transient
    assert res.end.col == 6  # cells 6-7, the wall at 8
    # Boxed in on every side: nowhere to go at all.
    g = path_test_game()
    lock_column(g, g.spawn_c - 1, 0)
    lock_column(g, g.spawn_c + 2, 0)
    for c in range(g.spawn_c, g.spawn_c + 2):
        g.locked[(SPAWN_ROW + 2, c)] = WireCell(o=True, t=1)
    res = g.find_path(p, Placement(orient=0, col=11), [])
    assert not res.reached and res.end == p and not res.moves and not res.blocked_by_transient


async def test_path_blocked_by_a_teammates_piece():
    # A crewmate's falling piece across the way, with no corridor round it,
    # is a transient block: the path waits as near the goal as it can get.
    g = path_test_game()
    theirs = Active(0, 1, 0, 6)  # vertical I in column 8 (rows 0-3)…
    for c in theirs.cells():
        g.others_act[c] = 1
    g.others_piece[1] = theirs
    wall = theirs.cells()[0][1]
    lock_column(g, wall, 4)  # …on a stack filling the column below it
    p = Active(1, 0, SPAWN_ROW, g.spawn_c)
    res = g.find_path(p, Placement(orient=0, col=11), [])
    assert not res.reached and res.blocked_by_transient
    assert res.end.col == wall - 2  # right up against the piece


async def test_path_keeps_out_of_an_imminent_spawn_box():
    # A teammate about to spawn: the path ducks under their spawn box rather
    # than crossing it.
    g = path_test_game()
    boxes = g.teammate_boxes()
    assert len(boxes) == 1 and boxes[0].imminent and boxes[0].seat == 1
    p = Active(0, 0, SPAWN_ROW, g.spawn_c)
    res = g.find_path(p, Placement(orient=0, col=10), boxes)
    assert res.reached
    assert MOVE_DOWN in res.moves and res.downs > 0
    for row, col, orient in replay_path(p, res.moves):
        assert not boxes[0].covers(piece_cells(p.pt, orient, row, col)), (row, col, orient)
    # With the teammate's piece on the board the box is not imminent, and
    # the straight walk is the path.
    g.others_piece[1] = Active(2, 0, 10, 8)
    res = g.find_path(p, Placement(orient=0, col=10), g.teammate_boxes())
    assert res.reached and res.downs == 0


async def test_waiting_spot_stays_out_of_the_spawn_box():
    # Waiting for a crewmate's piece happens outside a teammate's imminent
    # spawn box, even where that is further from the goal.
    g = path_test_game()
    theirs = Active(0, 1, 0, 8)  # vertical I in column 10, rows 0-3
    for c in theirs.cells():
        g.others_act[c] = 2
    g.others_piece[2] = theirs
    wall = theirs.cells()[0][1]
    lock_column(g, wall, 4)
    boxes = [SpawnBox(seat=1, c0=5, c1=8, imminent=True)]
    p = Active(1, 0, SPAWN_ROW, 3)
    res = g.find_path(p, Placement(orient=0, col=11), boxes)
    assert not res.reached and res.blocked_by_transient
    assert not boxes[0].covers(piece_cells(p.pt, res.end.orient, res.end.row, res.end.col))
    assert res.end.col == wall - 2 and res.end.row >= SPAWN_ROW + 2
