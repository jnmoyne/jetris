"""Tetromino geometry.

PIECES[type][orientation] is the four (row_off, col_off) cell offsets from the
piece's anchor; orientation 0 is the spawn shape. Types: I O T S Z J L = 0..6.
These offsets are the game's canonical shapes (jetris-gameplays.md §2) and
match every other peer's engine: the whole point of the deterministic protocol
is that everyone computes the same cells.
"""

PIECES = (
    (  # I
        ((1, 0), (1, 1), (1, 2), (1, 3)), ((0, 2), (1, 2), (2, 2), (3, 2)),
        ((2, 0), (2, 1), (2, 2), (2, 3)), ((0, 1), (1, 1), (2, 1), (3, 1)),
    ),
    (  # O (all four orientations identical)
        ((0, 0), (0, 1), (1, 0), (1, 1)), ((0, 0), (0, 1), (1, 0), (1, 1)),
        ((0, 0), (0, 1), (1, 0), (1, 1)), ((0, 0), (0, 1), (1, 0), (1, 1)),
    ),
    (  # T
        ((0, 1), (1, 0), (1, 1), (1, 2)), ((0, 1), (1, 1), (1, 2), (2, 1)),
        ((1, 0), (1, 1), (1, 2), (2, 1)), ((0, 1), (1, 0), (1, 1), (2, 1)),
    ),
    (  # S
        ((0, 1), (0, 2), (1, 0), (1, 1)), ((0, 1), (1, 1), (1, 2), (2, 2)),
        ((1, 1), (1, 2), (2, 0), (2, 1)), ((0, 0), (1, 0), (1, 1), (2, 1)),
    ),
    (  # Z
        ((0, 0), (0, 1), (1, 1), (1, 2)), ((0, 2), (1, 1), (1, 2), (2, 1)),
        ((1, 0), (1, 1), (2, 1), (2, 2)), ((0, 1), (1, 0), (1, 1), (2, 0)),
    ),
    (  # J
        ((0, 0), (1, 0), (1, 1), (1, 2)), ((0, 1), (0, 2), (1, 1), (2, 1)),
        ((1, 0), (1, 1), (1, 2), (2, 2)), ((0, 1), (1, 1), (2, 0), (2, 1)),
    ),
    (  # L
        ((0, 2), (1, 0), (1, 1), (1, 2)), ((0, 1), (1, 1), (2, 1), (2, 2)),
        ((1, 0), (1, 1), (1, 2), (2, 0)), ((0, 0), (0, 1), (1, 1), (2, 1)),
    ),
)

# The standard board: ten cells wide (a shared board adds the game's extra
# columns per seat beyond the first, shared_width).
WIDTH = 10

# Spawn position every peer uses for a new piece: two rows into the headroom,
# horizontally centered (WIDTH-4)/2.
SPAWN_ROW = 2
SPAWN_COL = (WIDTH - 4) // 2

PIECE_NAMES = "IOTSZJL"


def piece_cells(pt, orient, row, col):
    """The four absolute (row, col) cells of piece pt at orientation orient
    anchored at (row, col)."""
    o = PIECES[pt][orient & 3]
    return (
        (row + o[0][0], col + o[0][1]),
        (row + o[1][0], col + o[1][1]),
        (row + o[2][0], col + o[2][1]),
        (row + o[3][0], col + o[3][1]),
    )
