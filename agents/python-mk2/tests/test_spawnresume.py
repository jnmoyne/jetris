from helpers import make_game

from python_mk2.path import Active


async def test_spawn_resumes_adopted_piece():
    # spawn NEVER creates a new piece while we already own a live one. A
    # piece can be handed back to us between cycles (adopted off the wire
    # when a committed transform moved it, or re-adopted by a resync) and
    # spawning over it would orphan its cells on the stream as a frozen
    # ghost no client ever vacates. The caller must instead re-plan the
    # piece we already hold.
    adopted = Active(pt=1, orient=0, row=5, col=4)
    g = make_game(piece=adopted)
    async with g.mu:
        spawn_t, placed, topped = await g.spawn()
    assert placed and not topped
    assert spawn_t, "spawn should return a usable spawn time for the resumed piece"
    assert g.piece == adopted, "spawn must not replace the adopted piece"
