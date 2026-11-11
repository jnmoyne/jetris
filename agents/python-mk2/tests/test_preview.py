from python_mk2.difficulty import MAX_NEXT_COUNT, difficulty_tuning
from python_mk2.planner import revealed_pieces
from python_mk2.rng import BAG_SINGLE, piece_at, piece_at_in, piece_sets


def test_lookahead_never_exceeds_game_preview():
    # The fair-visibility contract's preview rule (jetris-agent-guide.md
    # §1): the planner's lookahead is the pieces the GAME reveals (its
    # meta's next_count, the same ones a human sees in the NEXT well) and
    # nothing on the agent's side can reach past it. Every difficulty is
    # checked against every preview size, plus a meta without the field (0)
    # and an out-of-range one.
    seed, piece_idx = 42, 7
    for d in ("easy", "medium", "hard"):
        tn = difficulty_tuning(d)
        assert tn.lookahead <= MAX_NEXT_COUNT, d
        for next_count in range(MAX_NEXT_COUNT + 1):
            got = revealed_pieces(seed, [], BAG_SINGLE, piece_idx, next_count, tn.lookahead)
            assert len(got) == min(next_count, tn.lookahead), (d, next_count)
            # What it does see is the seat's own sequence, in play order.
            assert got == [piece_at(seed, piece_idx + 1 + i) for i in range(len(got))], (d, next_count)
    # No preview means no lookahead at ANY difficulty.
    assert revealed_pieces(seed, [], BAG_SINGLE, piece_idx, 0, MAX_NEXT_COUNT) == []
    # A foreign host writing nonsense can't open the horizon past what the
    # difficulty is allowed to use, and a negative count reveals nothing.
    assert len(revealed_pieces(seed, [], BAG_SINGLE, piece_idx, 99, MAX_NEXT_COUNT)) == MAX_NEXT_COUNT
    assert revealed_pieces(seed, [], BAG_SINGLE, piece_idx, -1, MAX_NEXT_COUNT) == []


def test_split_pieces_preview():
    # The preview of a seat playing a RATION (split_pieces): the pieces it
    # is shown are its own sequence's, so every one of them is a type the
    # seat actually holds: a planner never sees, and so never plans on, a
    # piece only a teammate can play.
    seed = 42
    for slot, ration in enumerate(piece_sets(seed, 3)):
        assert ration, f"slot {slot} was dealt nothing"
        got = revealed_pieces(seed, ration, BAG_SINGLE, 7, MAX_NEXT_COUNT, MAX_NEXT_COUNT)
        assert len(got) == MAX_NEXT_COUNT
        for i, pt in enumerate(got):
            assert pt in ration, (slot, i)
            assert pt == piece_at_in(seed, ration, 7 + 1 + i), (slot, i)
