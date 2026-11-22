"""One write a cell: the fold of a transform's batch (wire.merge_cells)."""

from python_mk2.wire import CellUpd, WireCell, merge_cells


def test_merge_cells_one_write_a_cell():
    """One update a cell, in the order the cells first came; a write beats a
    vacate whichever came first, and a guard on either is kept."""
    piece, stack = WireCell(t=2, a=True, pi=1), WireCell(o=True, t=1)
    out = merge_cells([
        CellUpd((3, 3)),               # emptied by the settled cells' diff...
        CellUpd((5, 5), stack),        # (a cell written once is left alone)
        CellUpd((3, 3), piece, True),  # ...and taken by the shifted piece
        CellUpd((4, 4), piece),        # taken by one piece...
        CellUpd((4, 4), None, True),   # ...and vacated by the one that left it
    ])
    assert out == [CellUpd((3, 3), piece, True), CellUpd((5, 5), stack), CellUpd((4, 4), piece, True)]
