"""One write a cell, in a transform's batch, against a live JetStream
server: a collapse or a raise moves the falling pieces with the stack, and a
piece moves INTO cells the batch also empties."""

from conftest import needs_server
from server_helpers import ServerEnv

from python_mk2.consts import MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.path import Active
from python_mk2.wire import CellUpd, WireCell

pytestmark = needs_server


async def seed_board(g):
    """Write the replica's board to the stream (the settled cells, our piece
    and the teammates') as the board a transform starts from, and take the
    cells' sequences from the write. Caller holds g.mu."""
    cells = [CellUpd(at, wc) for at, wc in g.locked.items()]
    for pi, p in g.others_piece.items():
        wc = WireCell(t=p.pt, a=True, r=p.orient, ar=p.row, ac=p.col, pi=pi)
        cells += [CellUpd(c, wc) for c in p.cells()]
    if g.piece is not None:
        cells += [CellUpd(c, g.active_payload(g.piece)) for c in g.piece.cells()]
    await g.publish_batch(cells, True)
    g.reindex_others()


def assert_piece_on_stream(snap, owner, p):
    """The stream holds the piece whole, as owner's: each of its four cells
    an active cell of that seat, at that anchor."""
    for c in p.cells():
        wc = snap[c][1] if c in snap else None
        assert wc is not None and wc.a and wc.pi == owner and (wc.ar, wc.ac) == (p.row, p.col), \
            f"cell {c}: want seat {owner}'s piece at ({p.row},{p.col}), the stream has {wc}"


def stack_under_a_piece(g):
    """Five full rows, the top one short of a cell (four clear, one stays),
    and a teammate's O a row above the stack: shifted four rows down, it
    lands on cells the stack held."""
    h = g.height()
    for r in range(h - 5, h):
        for c in range(g.w):
            g.locked[(r, c)] = WireCell(o=True, t=1)
    del g.locked[(h - 5, 0)]
    theirs = Active(1, 0, h - 8, 6)
    g.others_piece[1] = theirs
    return theirs


async def test_coop_clear_shifts_a_piece_onto_the_stack():
    """A collapse carries the teammates' falling pieces down with the stack,
    and a piece that hung just over it shifts into cells the stack held:
    emptied by the settled cells' diff, taken by the piece. Written twice in
    the one batch, the cell's second write could never meet its expectation:
    the clear was refused every time it was tried, by every seat, and the
    rows stood full. One write a cell: the clear commits, the piece whole."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        async with g.mu:
            h = g.height()
            theirs = stack_under_a_piece(g)
            await seed_board(g)
            rows = g.completed_rows_shared()
            assert len(rows) == 4, f"completed rows: {rows}, want the bottom four"
            assert await g.clear_rows_coop(rows) == 4
            snap = await g.fetch_board()
            shifted = Active(theirs.pt, theirs.orient, theirs.row + 4, theirs.col)
            assert_piece_on_stream(snap, 1, shifted)
            own = set(shifted.cells())
            for at, (_, wc) in snap.items():
                assert not wc.a or at in own, f"stray active cell {at}: {wc}"
                assert not wc.o or (at[0] == h - 1 and at[1] != 0), f"settled cell {at} left by the clear: {wc}"
            for c in range(1, g.w):
                assert snap[(h - 1, c)][1].o, f"cell {(h - 1, c)}: the row that stays is not there"


async def test_gated_clear_shifts_a_piece_onto_the_stack():
    """The same collapse behind the board's gate (a team's board, a crew's
    whose floor rises): there the teammate's cells are guarded and the
    settled cells are not, and the cell written twice (the piece's, then the
    empty one the diff ordered last) left the piece on the wire short of the
    cells it had shifted into."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_TEAMS, 0, 2)
        async with g.mu:
            theirs = stack_under_a_piece(g)
            await seed_board(g)
            await g.clear_rows(g.completed_rows_shared(), 0)
            assert g.lines == 4
            snap = await g.fetch_board()
            shifted = Active(theirs.pt, theirs.orient, theirs.row + 4, theirs.col)
            assert_piece_on_stream(snap, 1, shifted)
            own = set(shifted.cells())
            for at, (_, wc) in snap.items():
                assert not wc.a or at in own, f"stray active cell {at}: {wc}"


async def test_garbage_lifts_a_piece_into_anothers_cells():
    """A raise lifts every falling piece clear of the risen stack, and the
    lifts cascade: ours, resting on the floor, rises into the cells the
    teammate's piece right above it leaves. Those cells came twice in the
    batch (taken by our piece, vacated by theirs) and the empty one, ordered
    last, either failed its expectation (the raise refused, every time) or
    won (our piece on the wire short of its top row). One write a cell: both
    pieces whole."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_TEAMS, 0, 2)
        g.holes = 1
        async with g.mu:
            h = g.height()
            ours = Active(1, 0, h - 2, 4)    # an O resting on the floor
            theirs = Active(1, 0, h - 4, 4)  # right above it
            g.piece = ours
            g.others_piece[1] = theirs
            await seed_board(g)
            g.garbage_owed = 1
        await g.apply_owed_garbage()
        async with g.mu:
            assert g.txn_applied == 1, "the raise did not commit"
            snap = await g.fetch_board()
            assert_piece_on_stream(snap, 0, Active(ours.pt, ours.orient, ours.row - 1, ours.col))
            assert_piece_on_stream(snap, 1, Active(theirs.pt, theirs.orient, theirs.row - 1, theirs.col))
            assert sum(1 for _, wc in snap.values() if wc.a) == 8, "want the two pieces' 8 active cells"
