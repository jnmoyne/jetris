"""Wire types and helpers (jetris-agent-guide.md §4).

Everything on the wire is JSON. The lobby listing and the game meta are
read-modify-written as plain dicts, so unknown fields survive a CAS rewrite
untouched (and Python ints keep the 64-bit seed's exact digits).
"""

import json
import logging
import re
import secrets
from datetime import datetime, timezone
from typing import NamedTuple

log = logging.getLogger("python-mk2")


def dumps(v):
    """Compact JSON bytes, as every other peer writes it."""
    return json.dumps(v, separators=(",", ":"), ensure_ascii=False).encode()


def to_obj(b):
    """Decode a JSON object; anything else (empty, malformed) is {}."""
    if not b:
        return {}
    try:
        o = json.loads(b)
    except (ValueError, UnicodeDecodeError):
        return {}
    return o if isinstance(o, dict) else {}


def o_str(o, k):
    v = o.get(k)
    return v if isinstance(v, str) else ""


def o_int(o, k):
    v = o.get(k)
    if isinstance(v, bool) or not isinstance(v, int):
        return 0
    return v


def o_bool(o, k):
    return o.get(k) is True


def now_rfc():
    """The current time as every peer stamps it: RFC 3339, UTC."""
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def parse_rfc(s):
    """Parse an RFC 3339 timestamp (the fraction may be 9 digits). None when
    it does not parse."""
    if not isinstance(s, str) or not s:
        return None
    s = re.sub(r"(\.\d{6})\d+", r"\1", s)
    if s.endswith(("Z", "z")):
        s = s[:-1] + "+00:00"
    try:
        t = datetime.fromisoformat(s)
    except ValueError:
        return None
    if t.tzinfo is None:
        t = t.replace(tzinfo=timezone.utc)
    return t


# ---- roster seats -----------------------------------------------------------


def player_summary(player_id, name, seat=0, team=0, team_slot=0, ready=False, agent=False):
    """One roster seat in a KV game listing / roster announcement."""
    return {"player_id": player_id, "name": name, "ready": ready, "seat": seat,
            "team": team, "team_slot": team_slot, "agent": agent}


def _summary(p):
    """A decoded roster seat with every field present and typed."""
    if not isinstance(p, dict):
        p = {}
    out = dict(p)
    out["player_id"] = o_str(p, "player_id")
    out["name"] = o_str(p, "name")
    out["ready"] = o_bool(p, "ready")
    out["seat"] = o_int(p, "seat")
    out["team"] = o_int(p, "team")
    out["team_slot"] = o_int(p, "team_slot")
    out["agent"] = o_bool(p, "agent")
    return out


def normalize_seats(ps):
    """Fill every seat in: the recorded seats when they are unique (every
    listing written since the field), the roster positions otherwise (the
    assignment every game used before the field)."""
    out = [dict(p) for p in ps]
    seen = set()
    for p in out:
        if p["seat"] in seen:
            for i, q in enumerate(out):
                q["seat"] = i
            return out
        seen.add(p["seat"])
    return out


def players_of(o):
    """A listing's players array, decoded and seat-normalized."""
    raw = o.get("players")
    if not isinstance(raw, list):
        return []
    return normalize_seats([_summary(p) for p in raw])


# ---- playfield cells --------------------------------------------------------


class WireCell(NamedTuple):
    """One playfield cell payload. A zero-valued field is dropped on the wire,
    so an empty cell marshals to "{}" (the vacate payload)."""

    t: int = 0       # piece type (0 = I, dropped)
    o: bool = False  # occupied/locked
    a: bool = False  # active (falling)
    r: int = 0       # orientation
    ar: int = 0      # active anchor row
    ac: int = 0      # active anchor col
    pi: int = 0      # owning player index
    g: bool = False  # adversarial garbage (a solid garbage row is permanent; one raised with holes clears once they are filled)

    def to_bytes(self):
        return dumps({k: v for k, v in zip(self._fields, self) if v})


EMPTY_CELL = WireCell()


def parse_cell(b):
    """Decode a cell payload; None when it is not a cell at all."""
    if not b:
        return EMPTY_CELL
    try:
        o = json.loads(b)
    except (ValueError, UnicodeDecodeError):
        return None
    if not isinstance(o, dict):
        return None
    try:
        return WireCell(
            t=_i(o.get("t")), o=o.get("o") is True, a=o.get("a") is True, r=_i(o.get("r")),
            ar=_i(o.get("ar")), ac=_i(o.get("ac")), pi=_i(o.get("pi")), g=o.get("g") is True)
    except TypeError:
        return None


def _i(v):
    if v is None:
        return 0
    if isinstance(v, bool) or not isinstance(v, int):
        raise TypeError("not an int")
    return v


# ---- events -----------------------------------------------------------------


def parse_event(b):
    """A per-player game event (game_over, line_clear) with every field
    present and typed: the sender's CUMULATIVE totals, folded as deltas by
    receivers, and the clear's names."""
    o = to_obj(b)
    rows = o.get("cleared_rows")
    return {
        "kind": o_str(o, "kind"), "player_id": o_str(o, "player_id"), "player_idx": o_int(o, "player_idx"),
        "score": o_int(o, "score"), "level": o_int(o, "level"), "piece_count": o_int(o, "piece_count"),
        "team": o_int(o, "team"), "total_score": o_int(o, "total_score"), "total_lines": o_int(o, "total_lines"),
        "cleared_rows": [r for r in rows if isinstance(r, int)] if isinstance(rows, list) else [],
        "lines_cleared": o_int(o, "lines_cleared"), "t_spin": o_int(o, "t_spin"),
        "back_to_back": o_bool(o, "back_to_back"), "combo": o_int(o, "combo"), "perfect": o_bool(o, "perfect"),
    }


def event_clear(ev):
    """The event's clear as the scoring tables value it (a 180 spin scores as
    a full T-spin, so it is one to the tables)."""
    from .scoring import SPIN_FULL, SPIN_MINI, SPIN_NONE, ClearInfo

    spin = {1: SPIN_MINI, 2: SPIN_FULL, 3: SPIN_FULL}.get(ev["t_spin"], SPIN_NONE)
    return ClearInfo(lines=ev["lines_cleared"], spin=spin, back_to_back=ev["back_to_back"],
                     combo=ev["combo"], perfect=ev["perfect"])


# ---- batches ----------------------------------------------------------------


class CASError(Exception):
    """A rejected CAS/gated write (wrong expected sequence): the caller
    flashes and resynchronizes rather than treating it as a hard error."""


class CellUpd(NamedTuple):
    """One cell write of a batch: c None vacates; guard makes a gated batch
    carry this cell's CAS expectation anyway (the teammate-piece guard)."""

    at: tuple
    c: object = None
    guard: bool = False


def category(c):
    if c is not None and c.a:
        return 0
    if c is not None and c.o:
        return 1
    return 2


def order_cells(cells):
    """Order a batch active → locked → empty (then by row and column), in
    place: a relocating piece never transiently vanishes on ordered
    consumers."""
    cells.sort(key=lambda u: (category(u.c), u.at[0], u.at[1]))


def payload_bytes(c):
    return b"{}" if c is None else c.to_bytes()


def garbage_payload(causer):
    return WireCell(o=True, t=1, g=True, pi=causer)


def txn_payload(applied, op="", by=0, topped=None, full=False):
    """The per-board transaction register that gates bulk transforms."""
    t = {"applied": applied}
    if op:
        t["op"] = op
    if by:
        t["by"] = by
    if topped:
        t["topped"] = list(topped)
    if full:
        t["full"] = True
    return t


def garbage_reg(total, by, raises=0):
    """A board's garbage register: the rows owed in all, who caused the last
    of them, and (survival) how many times the floor has risen."""
    reg = {"total": total, "by": by}
    if raises:
        reg["raises"] = raises
    return reg


def rand_id(n):
    """n random hex chars (batch ids)."""
    return secrets.token_hex((n + 1) // 2)[:n]
