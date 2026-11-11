"""Our own piece off the stream: adopted whole or not at all, and the strays
an adoption leaves swept with the next write."""

from helpers import make_game

from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.path import Active
from python_mk2.wire import WireCell


def crew_seat_game():
    """A crew game's seat 1 on a board of its own, its cells folded off the
    stream by hand."""
    return make_game(idx=1, mode=MODE_COOPERATIVE, w=14, h=24)


class Folder:
    """Folds a piece's cells, or the given ones of them, as seat g.idx's
    active cells at fresh sequences."""

    def __init__(self, g):
        self.g, self.seq = g, 0

    def fold(self, p, cells=None):
        for c in cells if cells is not None else p.cells():
            self.seq += 1
            self.g.fold_cell(c, WireCell(t=p.pt, a=True, r=p.orient, ar=p.row, ac=p.col, pi=self.g.idx), self.seq)


async def test_adopt_own_piece_whole_only():
    # A committed transform of a crewmate's (a clear shifting it down) moves
    # our piece: we adopt the anchor it put our piece at, once the stream
    # holds all four of its cells. Never from a part of a piece: adopting a
    # partial copy would orphan the piece, its cells left as a ghost.
    # Whatever an adoption leaves behind is a stray, for the next write to
    # sweep. And nothing is adopted while our own batches are in flight.
    g = crew_seat_game()
    f = Folder(g)
    a = Active(pt=2, orient=0, row=5, col=4)
    f.fold(a)
    assert g.piece == a

    # Three cells of another anchor: a part of a piece, not adopted, and
    # strays, all three.
    b = Active(pt=2, orient=0, row=8, col=4)
    bc = b.cells()
    f.fold(b, bc[:3])
    assert g.piece == a, "adopted a piece in part"
    assert len(g.stray_own_cells()) == 3

    # The fourth: the piece is whole there, and adopted; the old anchor's
    # cells are the strays now.
    f.fold(b, bc[3:])
    assert g.piece == b, "a whole piece at a new anchor not adopted"
    strays = g.stray_own_cells()
    assert len(strays) == 4 and set(strays) == set(a.cells())

    # A batch of ours in flight: the echo is the stream catching up to our
    # projection, nothing is adopted off it.
    g.inflight = 1
    f.fold(Active(pt=2, orient=0, row=11, col=4))
    assert g.piece == b, "adopted with a batch in flight"


async def test_move_batch_sweeps_strays():
    # A write of the piece vacates the cells it leaves (the piece's old
    # cells AND every stray of ours on the stream) in the one batch that
    # moves the piece.
    g = crew_seat_game()
    f = Folder(g)
    a = Active(pt=2, orient=0, row=5, col=4)
    b = Active(pt=2, orient=0, row=8, col=4)
    f.fold(a)
    f.fold(b)
    assert g.piece == b

    to = b._replace(col=b.col - 1)
    cells, old = g.move_batch(to)
    vacated = {u.at for u in cells if u.c is None}
    written = {u.at for u in cells if u.c is not None and u.c.a and u.c.ar == to.row and u.c.ac == to.col}
    new_set = set(to.cells())
    assert new_set <= written, "the move does not write the piece's new cells"
    assert all(c in new_set or c in vacated for c in b.cells()), "the move leaves one of the piece's old cells"
    assert set(a.cells()) <= vacated, "the move leaves a stray"
    # The cells the move leaves (what flashes if it is lost): the piece's
    # four and the four strays.
    assert len(old) == 8
