"""Shared-board play: everything cooperative and teams mode add on top of the
competitive machinery in game.py (jetris-gameplays.md §3/§5, guide §4).

Cooperative: ONE board, every player's falling piece on it, per-cell CAS with
merge-retry, no garbage (unless the floor rises), game over on any top-out.
Teams: one coop-style board per team plus the garbage ledger between the
boards, txn-gated transforms, and team-level outcomes.
"""

import time

from . import aio, jsx
from .consts import HEADROOM, MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from .coord import REPLAN_COLLAPSE
from .path import Active
from .pieces import SPAWN_COL, WIDTH, piece_cells
from .rng import piece_set_for
from .rules import (DEFAULT_TEAM_COUNT, MAX_TEAM_COUNT, MIN_TEAM_COUNT, extra_columns, extra_rows,  # noqa: F401
                    normalize_team_count, team_names)
from .scoring import SPIN_NONE, PlayerStats
from .wire import (CASError, CellUpd, WireCell, dumps, event_clear, log, o_bool, o_int, parse_cell, to_obj,
                   txn_payload)

# A shared board is STANDARD_HEIGHT rows (headroom included) for its first
# seat and the game's extra_rows more for every seat after it (rules.py). The
# headroom stays the top rows: the board grows downwards, so the spawn rows
# and the top-out rule are the same on every board.
STANDARD_HEIGHT = HEADROOM + 20

# The level and the speed curve (gameplays §7,
# https://harddrop.com/wiki/Tetris_Worlds), the same in every mode: the level
# starts at 1 and rises every ten lines, 15 at most.
MIN_LEVEL = 1
MAX_LEVEL = 15
LINES_PER_LEVEL = 10


def shared_width(seats, extra):
    """The width of a board shared by `seats` players."""
    return WIDTH + max(seats - 1, 0) * extra


def shared_height(seats, extra):
    """The height of a board shared by `seats` players."""
    return STANDARD_HEIGHT + max(seats - 1, 0) * extra_rows(extra)


def team_letter(t):
    """A team index the way every screen shows it: A, B, C, …"""
    if t < 0 or t >= 26:
        return str(t)
    return chr(ord("A") + t)


def level_of(lines):
    """The level a line total has reached."""
    return min(MIN_LEVEL + max(lines, 0) // LINES_PER_LEVEL, MAX_LEVEL)


def gravity_interval(level):
    """The speed curve, exactly: seconds per row = (0.8 − (L − 1) × 0.007)^(L
    − 1) at level L: 1000 ms at level 1 down to 7 ms at level 15, the top two
    levels faster than a 60 Hz frame (execute moves the rows a deadline owes
    as one batch). A level outside the range reads as the nearer bound."""
    level = min(max(level, MIN_LEVEL), MAX_LEVEL)
    n = float(level - 1)
    return (0.8 - n * 0.007) ** n


def parse_cell_subject(subject):
    """(row, col) off a …playfield.cell.<r>.<c> subject, None for anything
    else."""
    i = subject.find(".playfield.cell.")
    if i < 0:
        return None
    r, dot, c = subject[i + len(".playfield.cell."):].partition(".")
    if not dot:
        return None
    try:
        return int(r), int(c)
    except ValueError:
        return None


class SharedMixin:
    # ---- teams -------------------------------------------------------------

    def teams(self):
        """How many teams this game is played between (2 unless the meta says
        otherwise)."""
        if self.team_count < MIN_TEAM_COUNT:
            return DEFAULT_TEAM_COUNT
        return min(self.team_count, MAX_TEAM_COUNT)

    def team_size(self):
        return self.player_count // self.teams()

    # ---- level and collision -----------------------------------------------

    def level(self):
        """The level gravity falls at and the next clear is scored at: this
        seat's own lines in competitive, every clear on the board in
        cooperative, the OWN team's in teams."""
        if self.mode == MODE_COOPERATIVE:
            return level_of(self.total_lines)
        if self.mode == MODE_TEAMS:
            return level_of(self.team_lines[self.team])
        return level_of(self.lines)

    def gravity_now(self):
        return gravity_interval(self.level())

    def shared(self):
        """Whether this game's board is shared with other pieces."""
        return self.mode != MODE_COMPETITIVE

    def shared_blocked(self, cells):
        """Whether any of the cells is held by ANOTHER player's falling
        piece: the transient obstacle class that never locks or tops out."""
        others = self.others_act
        for c in cells:
            if c in others:
                return True
        return False

    def can_move(self, cells):
        """The full collision rule for this board: bounds + locked cells
        (can_place) + other players' active pieces on shared boards."""
        return self.can_place(cells) and not self.shared_blocked(cells)

    def drop_row_shared(self, a):
        """drop_row_locked honoring other players' pieces: the hard-drop row
        (a piece resting on another ACTIVE piece does not lock there: gravity
        resumes when the obstacle falls away)."""
        row = a.row
        while self.can_move(piece_cells(a.pt, a.orient, row + 1, a.col)):
            row += 1
        return row

    # ---- the shared-board consumer -----------------------------------------

    def board_filter(self):
        """The consumer filter covering this game's OWN board: cells plus the
        board's garbage and txn registers."""
        if self.mode == MODE_COOPERATIVE:
            return f"jetris.game.{self.id}.playfield.>"
        if self.mode == MODE_TEAMS:
            return f"jetris.game.{self.id}.team.{self.team}.playfield.>"
        return ""

    async def handle_board_msg(self, m):
        """Fold one delivered board message into local state. Own echoes
        no-op via the strictly-higher-sequence rule (write-through already
        recorded them); everything newer (teammates' moves and locks, another
        clearer's collapse, a garbage cascade) updates the settled map, the
        other-active map, and even OUR OWN piece (an external transform may
        legitimately shift or lift it; we adopt the committed position)."""
        subject = m.subject
        try:
            seq = m.metadata.sequence.stream
        except Exception:
            seq = 0
        self.note_stream_seq(seq)
        if subject.endswith(".playfield.garbage"):
            reg = to_obj(m.data)
            async with self.mu:
                if o_int(reg, "total") > self.garbage_owed:
                    self.garbage_owed = o_int(reg, "total")
                self.garbage_by = o_int(reg, "by")
                if seq > self.garbage_seq:
                    self.garbage_seq, self.survival_raises = seq, o_int(reg, "raises")
                need = self.garbage_owed > self.txn_applied and not self.dead
            if need and (self.mode == MODE_TEAMS or self.survival):
                # Any alive member may apply; the txn gate admits exactly one.
                aio.spawn(self.tasks, self.apply_owed_garbage())
            if self.survival:
                # A raise landed (ours or a crewmate's): the clock re-arms from it.
                self.survival_kick.set()
            return
        if subject.endswith(".playfield.txn"):
            t = to_obj(m.data)
            async with self.mu:
                if seq > self.txn_seq:
                    self.txn_seq = seq
                    if o_int(t, "applied") > self.txn_applied:
                        self.txn_applied = o_int(t, "applied")
                    topped = t.get("topped")
                    if isinstance(topped, list) and self.idx in topped:
                        # A garbage cascade pushed our piece off the top: the
                        # zero-active edge is an elimination, not a lock-in.
                        self.dead = True
                        self.piece = None
                    if o_bool(t, "full"):
                        self.dead = True  # locked rows past the top: the whole board is out
            return
        at = parse_cell_subject(subject)
        if at is None:
            return
        wc = parse_cell(m.data)
        if wc is None:
            wc = WireCell()
        async with self.mu:
            self.fold_cell(at, wc, seq)

    def fold_cell(self, at, wc, seq):
        """Fold one delivered cell into the board. Caller holds mu."""
        if seq <= self.seqs.get(at, 0):
            return  # our own write-through (or older): already accounted
        self.seqs[at] = seq
        if wc.a:
            self.locked.pop(at, None)
            p = Active(wc.t, wc.r, wc.ar, wc.ac)
            if not self.shared() or wc.pi == self.idx:
                self.own_act[at] = p
                self.others_act.pop(at, None)
                self.forget_peer_cell(at)
                self.adopt_own_piece(p)
            else:
                self.own_act.pop(at, None)
                self.others_act[at] = wc.pi
                self.others_piece[wc.pi] = p
                self.note_peer_cell(at, wc, seq, time.monotonic())  # its idle clock (sweep.py)
        elif wc.o:
            self.own_act.pop(at, None)
            self.drop_owner(at)
            self.locked[at] = wc
            self.forget_peer_cell(at)
        else:
            self.own_act.pop(at, None)
            self.locked.pop(at, None)
            self.drop_owner(at)
            self.forget_peer_cell(at)

    def adopt_own_piece(self, p):
        """Adopt p as our piece once the stream holds it WHOLE: a committed
        transform of a crewmate's (a clear shifting it down, a cascade
        lifting it) moved our piece, and we play on from where it put it. Not
        while our own batches are in flight: then the echo is just the stream
        catching up to the projection, and adopting it would snap the piece
        back mid-pipeline (a real external transform rejects an in-flight
        batch, and the repair adopts its truth). And never from a part of a
        piece: a stale collapse, merged cell by cell, can copy some cells of
        our piece beside its live four; adopting the copy would orphan the
        piece, its cells left on the board as a ghost for the rest of the
        game. Whatever the adoption leaves behind is a stray, swept with our
        next write (stray_own_cells). Caller holds mu."""
        if self.inflight > 0 or self.piece == p:
            return
        own = self.own_act
        for c in p.cells():
            if own.get(c) != p:
                return
        was = self.piece
        self.piece = p
        if was is None:
            return
        d = p.row - was.row
        if d > 0 and p.col == was.col and p.orient == was.orient and p.pt == was.pt:
            # A crewmate's clear collapsed rows under the piece we were
            # playing: its plan moves down with it (execute, plan_shift).
            self.plan_shift += d
        else:
            # Lifted by a rising stack, or moved some other way: the rows its
            # plan was made on are not there. Plan it again (coord.py).
            self.request_replan(REPLAN_COLLAPSE)

    def drop_owner(self, at):
        """Remove a cell from the other-actives map; when that was the
        owner's LAST active cell (their piece locked, vacated, or was
        squeezed out), the reconstructed piece goes with it. Caller holds
        mu."""
        pi = self.others_act.pop(at, None)
        if pi is None:
            return
        if pi in self.others_act.values():
            return
        self.others_piece.pop(pi, None)

    async def resync_shared(self):
        """Rebuild the whole shared board from one snapshot of the stream
        after a dropped write. It IS resync (fold_snapshot handles both board
        kinds); the name survives at the shared-board call sites."""
        await self.resync()

    # ---- clears on shared boards -------------------------------------------

    def completed_rows_shared(self):
        """Full-width, all settled, not solid garbage, and no ACTIVE cell
        anywhere in the row (a row a falling piece crosses cannot clear)."""
        others = self.others_act
        return [r for r in self.completed_rows() if not any((r, c) in others for c in range(self.w))]

    def clear_projection(self, rows):
        """The post-clear board: settled cells collapse down, and every OTHER
        player's falling piece shifts down with the stack (its owner keeps
        dropping, gameplays §3: the batch's active-first ordering keeps it
        continuously present). Returns the new settled map and each shifted
        piece keyed by owner."""
        removed = set(rows)
        new_locked = {}
        shift = 0
        locked, w = self.locked, self.w
        for r in range(self.height() - 1, -1, -1):
            if r in removed:
                shift += 1
                continue
            for c in range(w):
                cc = locked.get((r, c))
                if cc is not None:
                    new_locked[(r + shift, c)] = cc
        # Every other falling piece keeps dropping, shifted down with the
        # stack: by the number of cleared rows below its lowest cell (rows
        # above it collapse nothing under it; a cleared row can never cross a
        # piece: a row with an active cell is not complete).
        moved = {}
        for pi in sorted(self.others_piece):
            p = self.others_piece[pi]
            bottom = max(p.row, max(r for r, _ in p.cells()))
            down = sum(1 for r in rows if r > bottom)
            moved[pi] = Active(p.pt, p.orient, p.row + down, p.col)
        return new_locked, moved

    async def clear_rows_coop(self, rows):
        """Collapse completed rows on the cooperative board with the
        shared-board CAS merge-retry: every changed cell carries its
        per-subject expectation, so a teammate's simultaneous move atomically
        rejects the whole batch; we refetch, recompute (the rows may no
        longer be complete), and retry a bounded number of times. Returns the
        number of rows cleared."""
        for attempt in range(5):
            if attempt > 0:
                await self.resync_shared()
                rows = self.completed_rows_shared()
                if not rows:
                    return 0
            new_locked, moved = self.clear_projection(rows)
            diff = self.diff_cells(new_locked)
            for pi, np in moved.items():
                op = self.others_piece[pi]
                old_cells = set(op.cells())
                new_set = set(np.cells())
                pl = WireCell(t=np.pt, a=True, r=np.orient, ar=np.row, ac=np.col, pi=pi)
                diff += [CellUpd(c, pl) for c in sorted(new_set)]
                diff += [CellUpd(c) for c in sorted(old_cells) if c not in new_set and c not in new_locked]
            try:
                await self.publish_batch(diff, True)
            except CASError:
                continue
            except Exception as e:
                log.info("coop clear: %s", e)
                return 0
            self.locked = new_locked
            for pi, np in moved.items():
                self.others_piece[pi] = np
            self.reindex_others()
            return len(rows)
        return 0

    def reindex_others(self):
        """Rebuild the cell→owner map from the per-owner pieces, and the
        cells' idle clocks with it (a transform of ours rewrote the moved
        cells; their echoes are ignored, so their clocks start here)."""
        self.others_act = {}
        for pi, p in self.others_piece.items():
            for c in p.cells():
                self.others_act[c] = pi
        self.rebuild_peer_cells(time.monotonic())

    # ---- events ------------------------------------------------------------

    async def publish_line_clear(self, c, pts, rows):
        """Announce a lock that scored (a clear, or on a shared board a
        drop's points alone, lines_cleared 0) with our CUMULATIVE totals
        (receivers fold deltas, so a retention-trimmed intermediate event is
        subsumed) and the clear's names."""
        ev = {
            "kind": "line_clear", "player_id": self.a.name, "player_idx": self.idx,
            "lines_cleared": c.lines, "cleared_rows": rows, "score": pts,
            "team": self.team, "total_score": self.score, "total_lines": self.lines,
            # Our pieces so far, the locking one included: every seat's count
            # is read off its last event.
            "piece_count": self.piece_idx,
        }
        if c.spin != SPIN_NONE:
            ev["t_spin"] = int(c.spin)
        if c.back_to_back:
            ev["back_to_back"] = True
        if c.combo > 0:
            ev["combo"] = c.combo
        if c.perfect:
            ev["perfect"] = True
        try:
            await jsx.js_publish(self.a.nc, f"jetris.game.{self.id}.events.line_clear.{self.a.name}", dumps(ev))
        except Exception:
            pass

    async def fold_line_clear(self, ev):
        """Fold another player's cumulative totals (a line_clear event's, or
        the ones its game_over carries) into the shared/team scoreboards, as
        deltas against the last totals seen from that sender."""
        pid = ev["player_id"]
        if pid == self.a.name:
            return  # our own echo: already folded (and tallied) at publish time
        async with self.mu:
            last = self.sender_totals.get(pid, (0, 0))
            ds, dl = ev["total_score"] - last[0], ev["total_lines"] - last[1]
            if ds < 0 or dl < 0:
                return
            self.sender_totals[pid] = (ev["total_score"], ev["total_lines"])
            # The sender's tally (stats_for): a line_clear that moves their
            # totals is a lock to count (one that moves nothing is a replay
            # of one counted) and their piece count is the highest their
            # events announced.
            self.sender_pieces[pid] = max(self.sender_pieces.get(pid, 0), ev["piece_count"])
            if ev["kind"] == "line_clear" and (ds > 0 or dl > 0):
                self.stats_for(pid).tally(event_clear(ev), ev["t_spin"], self.mode != MODE_COOPERATIVE,
                                          self.guideline)
            self.sender_teams[pid] = ev["team"]
            if self.mode == MODE_COOPERATIVE:
                # A board scored per seat folds the crew's lines (the shared
                # level) but never their points.
                if not self.individual:
                    self.shared_score += ds
                self.total_lines += dl
            elif self.mode == MODE_TEAMS:
                if 0 <= ev["team"] < len(self.team_scores):
                    self.team_scores[ev["team"]] += ds
                    self.team_lines[ev["team"]] += dl

    def stats_for(self, pid):
        """A seat's tally, made on first use. Caller holds mu."""
        st = self.sender_stats.get(pid)
        if st is None:
            st = self.sender_stats[pid] = PlayerStats()
        return st

    # ---- outcomes ----------------------------------------------------------

    def team_dead(self, t):
        """Whether every roster member of team t is eliminated. Caller holds
        mu."""
        n = 0
        for p in self.roster:
            if p["team"] != t:
                continue
            n += 1
            if not self.eliminated.get(p["player_id"]):
                return False
        # Every member out, or, in an open game, every member gone: a team
        # nobody holds a seat on any more is out (the game started with one).
        return n > 0 or self.had_rivals

    def others_dead(self):
        """Whether every team but our own is fully out: the moment we have
        won. With two teams that is simply the other one; with more it is the
        last-team-standing rule the whole game plays by. Caller holds mu."""
        return all(t == self.team or self.team_dead(t) for t in range(self.teams()))

    def winning_team(self):
        """The game's verdict: the one team still standing, or -1 while more
        than one is (and on a draw, every team out together). Caller holds
        mu."""
        alive = -1
        for t in range(self.teams()):
            if self.team_dead(t):
                continue
            if alive >= 0:
                return -1  # more than one team left: undecided
            alive = t
        return alive

    def next_garbage_target(self):
        """The opposing team our next attack lands on: the other team in a
        duel, and past that the rotation's next, so consecutive raises spread
        over the opponents instead of every one of them taking the full
        raise. Caller holds mu (the whole lock/clear/attack path runs under
        it)."""
        n = self.teams()
        t = (self.team + 1 + self.attack_rotor % max(n - 1, 1)) % n
        self.attack_rotor += 1
        return t

    async def vacate_own_piece(self):
        """Remove our dead piece from the team board as a txn-gated transform
        (op "vacate"), so a racing garbage application can never resurrect it
        from a stale snapshot. Caller holds mu.

        It retries persistently: on a busy board the gate keeps losing to
        teammate moves and other transforms, and a dead piece that is never
        vacated becomes a ghost obstacle every falling piece hovers on
        forever (nobody ever locks against another player's "active" cells).
        Each retry resyncs, so a cascade that already vacated us ends the
        loop with no piece."""
        await self.settle_pipeline()  # barrier: converge before the gated vacate
        max_attempts = 10
        for attempt in range(max_attempts):
            if attempt > 0:
                # Refresh from converged state: the winner's transform may
                # have moved our cells (or already vacated us).
                await self.resync_shared()
                await self.refresh_registers()
            if self.piece is None:
                return
            diff = [CellUpd(c) for c in self.active_cells()]
            try:
                await self.publish_gated_batch(txn_payload(self.txn_applied, "vacate", self.idx), diff)
            except CASError:
                continue
            except Exception as e:
                log.info("vacate: %s", e)
            self.piece = None
            return
        log.info("vacate: gave up after %d attempts, the dead piece may linger until the next board transform",
                 max_attempts)
        self.piece = None

    async def wait_for_verdict(self):
        """The teams endgame for an eliminated (or winning) member: stay
        connected until every team but one is fully dead, then report whether
        OUR team is the one left. The winning side archives
        (CAS-deduplicated)."""
        deadline = time.monotonic() + 600.0
        while True:
            async with self.mu:
                enemy_dead = self.others_dead()
                own_dead = self.team_dead(self.team)
            if enemy_dead:
                await self.transition_finished_and_archive()
                return True
            if own_dead:
                await self.wait_for_end()
                return False
            which = await aio.wait_any([self.ended, self.a.stop_ev], 0.5)
            if which == 0:
                async with self.mu:
                    return self.others_dead()
            if which == 1 or time.monotonic() >= deadline:
                return False

    # ---- the deal ----------------------------------------------------------

    def my_slot(self):
        """This seat's slot on its playfield: the seat itself on the crew's
        board, the slot within the team on a team's."""
        return self.team_slot if self.mode == MODE_TEAMS else self.idx

    def spawn_column(self, present):
        """Our spawn column among the slots PRESENT on our playfield
        (gameplays §3/§5): the seats present, ranked in slot order, spawn one
        extra-columns step apart, and the group is centred on the board, so
        alone we spawn in the middle of it, two seats sit either side of the
        middle, and a full house is slot × extra + SPAWN_COL. A slot not in
        the roster (ours, before the listing caught up with our join) counts
        as present; no roster at all is every seat. Competitive boards, and a
        shared board of one seat, spawn at the standard column."""
        return self.spawn_column_for(self.my_slot(), present)

    def spawn_column_for(self, slot, present):
        """spawn_column for any slot of our playfield: a teammate's spawn box
        is where THEIR next piece appears among the seats present, which the
        path a piece of ours takes keeps out of (path.py)."""
        seats = self.seats_on_pf
        if seats <= 1:
            return SPAWN_COL
        if slot < 0 or slot >= seats:
            return max(slot, 0) * self.extra + SPAWN_COL
        ranked = sorted({s for s in present if 0 <= s < seats})
        if not ranked:
            return slot * self.extra + SPAWN_COL
        if slot not in ranked:
            ranked = sorted(ranked + [slot])
        # The group's spawn boxes span the standard width plus a step per
        # seat after the first; the board's columns beyond that (a step per
        # empty seat) are split evenly to either side.
        base = (seats - len(ranked)) * self.extra // 2
        return base + ranked.index(slot) * self.extra + SPAWN_COL

    def present_slots(self, roster):
        """The slots seated on our playfield per a listing's roster: every
        seat on the crew's board, our team's slots on a team's."""
        if self.mode == MODE_COOPERATIVE:
            return sorted(p["seat"] for p in roster)
        if self.mode == MODE_TEAMS:
            return sorted(p["team_slot"] for p in roster if p["team"] == self.team)
        return []

    def redeal_locked(self, present):
        """In a game that splits its pieces, the seven types are dealt out
        between the slots PRESENT on our playfield (the seats an open game
        has right now) ranked in slot order, and this seat draws from its
        rank's ration. Every slot present (or no roster at all) is the deal
        at creation. Only our own sequence depends on it, so nobody has to
        agree; the piece index stands. Returns whether the ration changed. mu
        held."""
        if not self.split or self.seats_on_pf <= 1:
            return False
        slots = sorted({s for s in present if 0 <= s < self.seats_on_pf})
        if not slots:
            slots = list(range(self.seats_on_pf))
            self.present_pf = None
        elif slots == self.present_pf:
            return False
        else:
            self.present_pf = slots
        rank = slots.index(self.my_slot()) if self.my_slot() in slots else 0  # not in the deal: the first ration
        self.ration = piece_set_for(self.meta_seed, len(slots), rank)
        return True

    # ---- the line goal -----------------------------------------------------

    def playfield_lines_locked(self, ev):
        """The lines the playfield ev's sender plays on has cleared, as the
        stream told us so far, our own included: every seat's on the crew's
        board, our team's seats' on a team's board, the sender's own on a
        competitive board. mu held."""
        if self.mode == MODE_COOPERATIVE:
            return self.lines + sum(t[1] for t in self.sender_totals.values())
        if self.mode == MODE_TEAMS:
            n = self.lines if ev["team"] == self.team else 0
            for pid, t in self.sender_totals.items():
                if self.sender_teams.get(pid, 0) == ev["team"]:
                    n += t[1]
            return n
        if ev["player_id"] == self.a.name:
            return self.lines
        return self.sender_totals.get(ev["player_id"], (0, 0))[1]

    async def check_line_goal(self, ev):
        """Runs on every line_clear consumed (our own echo too): when the
        sender's playfield has reached the goal the game is decided (the crew
        wins together, a board scored per seat by its top scorer(s), a team
        by its members, a competitive board by its player) and the play loop
        ends; run finishes the meta (guide §5)."""
        if self.line_goal <= 0:
            return
        async with self.mu:
            if self.goal_decided or self.playfield_lines_locked(ev) < self.line_goal:
                return
            self.goal_decided = True
            if self.mode == MODE_COOPERATIVE:
                self.goal_won = not self.individual or self.individual_winner_locked()
            elif self.mode == MODE_TEAMS:
                self.goal_won = ev["team"] == self.team
            else:
                self.goal_won = ev["player_id"] == self.a.name
            log.info("line goal reached by %s's playfield: %s", ev["player_id"],
                     "we win" if self.goal_won else "we lose")
            self.mark_ended()
