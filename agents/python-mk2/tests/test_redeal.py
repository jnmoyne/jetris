from helpers import make_game, seat

from python_mk2.consts import MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.rng import piece_set_for
from python_mk2.wire import normalize_seats


async def test_redeal_rank_parity():
    # The re-deal among the seats present: the seven types dealt out between
    # the slots present, ranked in slot order, this seat drawing from its
    # rank's ration; the same roster twice re-deals nothing, every slot
    # present is the deal at creation, and a game that does not split
    # ignores the roster.
    g = make_game(mode=MODE_COOPERATIVE, idx=2, split=True, seats_on_pf=3, meta_seed=42)
    g.ration = piece_set_for(42, 3, 2)
    assert g.redeal_locked([0, 2]), "seat 1 gone: no re-deal"
    assert g.ration == piece_set_for(42, 2, 1)  # the second of the two-way deal
    assert not g.redeal_locked([2, 0]), "the same seats, in another order, re-dealt"
    assert g.redeal_locked([0, 1, 2]), "the full roster back: no re-deal"
    assert g.ration == piece_set_for(42, 3, 2)  # the creation deal's
    assert g.redeal_locked([]) or g.present_pf is None  # no roster: the deal at creation
    teams = make_game(mode=MODE_TEAMS, team=1, team_slot=0, split=True, seats_on_pf=2, meta_seed=7)
    roster = [seat("a", team=0, team_slot=0), seat("b", team=1, team_slot=0), seat("c", team=1, team_slot=1)]
    assert teams.present_slots(roster) == [0, 1]
    plain = make_game(mode=MODE_COOPERATIVE, seats_on_pf=3)
    assert not plain.redeal_locked([0]) and not plain.ration
    # Seats read by position for a listing written before the field.
    legacy = normalize_seats([seat("a"), seat("b")])
    assert legacy[1]["seat"] == 1
