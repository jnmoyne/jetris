"""The piece sequence, the split and the bag rule, pinned against the values
the game's own generator produces."""

from python_mk2.pieces import piece_cells
from python_mk2.rng import (BAG_DOUBLE, BAG_NONE, BAG_SINGLE, piece_at, piece_at_bag, piece_at_in, piece_set_for,
                            piece_sets)


def test_piece_parity():
    expected = {
        42: [2, 3, 4, 1, 0, 5, 6, 6, 0, 5, 4, 2, 3, 1, 2, 1, 4, 3, 6, 5, 0],
        12345: [6, 3, 2, 1, 0, 4, 5, 0, 1, 5, 3, 2, 6, 4, 3, 6, 4, 2, 5, 1, 0],
    }
    for seed, want in expected.items():
        assert [piece_at(seed, i) for i in range(len(want))] == want
        # Each 7-bag is a permutation of the seven pieces.
        assert sorted(piece_at(seed, i) for i in range(7)) == list(range(7))


def test_piece_cells():
    # The I piece spawned at (2,3) fills the row-3 horizontal.
    assert piece_cells(0, 0, 2, 3) == ((3, 3), (3, 4), (3, 5), (3, 6))


def test_split_parity():
    fixtures = [
        (42, 2, [[0, 1, 4], [2, 3, 5, 6]],
         [[0, 4, 1, 1, 4, 0, 4, 1, 0, 1, 0, 4], [3, 5, 6, 2, 6, 2, 3, 5, 5, 3, 2, 6]]),
        (42, 3, [[0, 4, 5], [2, 3], [1, 6]],
         [[5, 4, 0, 0, 5, 4, 4, 0, 5, 0, 5, 4], [3, 2, 3, 2, 2, 3, 2, 3, 2, 3, 2, 3],
          [1, 6, 6, 1, 6, 1, 1, 6, 1, 6, 1, 6]]),
        (42, 7, [[5], [0], [6], [1], [4], [3], [2]], None),
        (12345, 2, [[0, 1, 2, 5], [3, 4, 6]],
         [[5, 0, 2, 1, 0, 5, 1, 2, 5, 2, 0, 1], [4, 6, 3, 6, 4, 3, 3, 6, 4, 6, 4, 3]]),
        (12345, 3, [[1, 2], [0, 5, 6], [3, 4]],
         [[2, 1, 1, 2, 1, 2, 1, 2, 1, 2, 1, 2], [5, 0, 6, 6, 5, 0, 6, 5, 0, 5, 6, 0],
          [3, 4, 4, 3, 3, 4, 4, 3, 4, 3, 4, 3]]),
        (12345, 9, [[1], [3], [4], [6], [2], [0], [5], [1], [4]], None),
    ]
    for seed, seats, sets, seqs in fixtures:
        got = piece_sets(seed, seats)
        assert got == sets, (seed, seats)
        for slot, want in enumerate(sets):
            # piece_set_for is the accessor the game itself uses.
            assert piece_set_for(seed, seats, slot) == want
        for slot, want in enumerate(seqs or []):
            assert [piece_at_in(seed, got[slot], i) for i in range(len(want))] == want, (seed, seats, slot)


def test_split_covers_the_bag():
    # Whatever the seed and however many seats, every one of the seven types
    # is dealt to somebody and no seat is left empty-handed.
    for seats in range(1, 13):
        for seed in (1, 42, 12345, 999999):
            seen = set()
            for slot, ration in enumerate(piece_sets(seed, seats)):
                assert ration, f"seed {seed}, {seats} seats: slot {slot} was dealt nothing"
                seen.update(ration)
            assert seen == set(range(7)), (seed, seats)


def test_bag_parity():
    full = {
        BAG_DOUBLE: {
            42: [6, 5, 2, 3, 0, 3, 2, 4, 1, 4, 0, 1, 6, 5, 4, 5, 4, 1, 3, 1, 3, 6, 0, 2, 5, 6, 0, 2],
            12345: [3, 5, 4, 0, 3, 0, 6, 1, 6, 1, 5, 2, 2, 4, 4, 4, 1, 1, 0, 2, 0, 3, 5, 6, 6, 5, 3, 2],
        },
        BAG_NONE: {
            42: [6, 1, 0, 4, 1, 2, 2, 5, 4, 6, 6, 5, 0, 0, 5, 0, 3, 0, 0, 0, 0],
            12345: [5, 4, 0, 5, 5, 0, 2, 1, 4, 4, 1, 6, 2, 1, 5, 2, 1, 6, 5, 5, 5],
        },
    }
    for kind, by_seed in full.items():
        for seed, want in by_seed.items():
            assert [piece_at_bag(seed, None, kind, i) for i in range(len(want))] == want, (kind, seed)
    # Every fourteen of a double bag are two of each type.
    for i in range(0, 42, 14):
        count = [0] * 7
        for j in range(14):
            count[piece_at_bag(42, None, BAG_DOUBLE, i + j)] += 1
        assert count == [2] * 7
    # A ration under each kind: seed 42 dealt to two seats ({I O Z}, {T S J L}).
    sets = piece_sets(42, 2)
    rations = {
        BAG_DOUBLE: [[1, 0, 4, 1, 0, 4, 0, 4, 4, 1, 1, 0], [6, 6, 5, 3, 3, 5, 2, 2, 6, 3, 5, 2]],
        BAG_NONE: [[1, 0, 0, 4, 1, 0, 1, 4, 1, 0, 4, 4], [2, 5, 6, 2, 6, 2, 2, 2, 5, 3, 6, 3]],
    }
    for kind, by_slot in rations.items():
        for slot, want in enumerate(by_slot):
            assert [piece_at_bag(42, sets[slot], kind, i) for i in range(len(want))] == want, (kind, slot)
    # The standard bag through piece_at_bag is piece_at / piece_at_in
    # unchanged, and an unknown kind reads as the standard bag.
    for i in range(30):
        assert piece_at_bag(42, None, BAG_SINGLE, i) == piece_at(42, i)
        assert piece_at_bag(42, None, "triple", i) == piece_at(42, i)
        assert piece_at_bag(42, sets[0], BAG_SINGLE, i) == piece_at_in(42, sets[0], i)
