"""One game: this agent's engine for its own board.

There is no game server (guide §4): every peer is the engine for the pieces
it plays. Game holds the board as the stream has it, publishes this seat's
moves, locks, clears and garbage as atomic batches, and follows the game's
meta, events and (on a shared board) every other seat's cells through
ordered consumers. All board state is guarded by `mu`, held across the round
trips that must see a board nobody else changes underneath them; a method
documented "caller holds mu" must never take it again (the lock is not
reentrant).
"""

import asyncio
import random
import time

from . import aio, jsx
from .blackboard import (ECHO_TIMEOUT, PHASE_LOCKED, PHASE_PLANNED, BlackboardMixin, Coordinator, board_name,
                         claim_key)
from .consts import (ARCHIVE_SUBJECT, CHAT_STREAM, HEADROOM, MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS, PUB_SYNC,
                     game_stream_name, meta_subject)
from .coord import (CONFLICT_OVERLAP, LOCK_DELAY, PLAN_STALE, REPLAN_DEP_TIMEOUT, REPLAN_DROP_ROW,
                    REPLAN_LOCK_DROPPED, REPLAN_NONE, REPLAN_STALE, REPLAN_UNREACHABLE, RETHINK_DELAY, CoordMixin,
                    replan_name)
from .engine import Grid
from .path import Active
from .pieces import PIECE_NAMES, SPAWN_COL, SPAWN_ROW, WIDTH, piece_cells
from .pipeline import PipelineMixin, resolve_publish
from .planner import (Placement, choose, lookahead_job, plan_finish, plan_placements, plan_roots,
                      revealed_pieces)
from .rng import BAG_SINGLE, bag_label, normalize_bag, piece_at_bag, piece_set_for
from .rules import MAX_GARBAGE_HOLES
from .scoring import ClearInfo, drop_points
from .shared import (STANDARD_HEIGHT, SharedMixin, extra_columns, extra_rows, level_of, normalize_team_count,
                     parse_cell_subject, shared_height, shared_width, team_letter)
from .stats import CoordStats, StatsMixin
from .survival import MIN_SURVIVAL_HOLES, SurvivalMixin, normalize_survival, survival_holes
from .sweep import PeerCell, SpawnBlocker, SweepMixin
from .wire import (EMPTY_CELL, CASError, CellUpd, WireCell, dumps, garbage_payload, garbage_reg, log, now_rfc, o_bool,
                   o_int, o_str, order_cells, parse_cell, parse_event, parse_rfc, payload_bytes, players_of, rand_id,
                   to_obj, txn_payload)

# The subjects per multi-subject direct get: the server answers at most 1024
# results per request (no pagination), so larger boards go in chunks.
FETCH_CHUNK = 512

# The CAS lock's merge-retry bound on a shared board.
LOCK_RETRIES = 5

# How long after the finish the game stream and listing survive, so every
# peer receives the final events before they are deleted. Only the
# destructive steps wait: the record is published right away so every lobby
# shows the finished game immediately.
ARCHIVE_GRACE = 5.0

# What a turn of execute comes to.
_CONTINUE, _LOCKED, _DONE = range(3)


class _Exec:
    """execute's account of the piece it is driving, across its turns."""

    __slots__ = ("plan", "last_progress", "last_pos", "stale_since", "waiting_since")

    def __init__(self, plan):
        self.plan = plan
        self.last_progress = time.monotonic()
        self.last_pos = None
        self.stale_since = None    # when the plan first read as stale (check_plan)
        self.waiting_since = None  # the current wait on a crewmate's piece, for the stats


class PendingClaim:
    """A claim posted and not yet confirmed: the placement's ranking and
    projection, for the yields confirm_plan may have to make."""

    __slots__ = ("c", "rev", "ranked", "proj", "eta")

    def __init__(self, c, rev, ranked, proj, eta):
        self.c, self.rev, self.ranked, self.proj, self.eta = c, rev, ranked, proj, eta


def clear_of(ranked, cells):
    """The ranking without the placements whose landing meets any of the
    cells."""
    return [pl for pl in ranked if not any(d in cells for d in pl.dest)]


def ration_names(ration):
    """A ration spelled out in piece letters, for the log line that tells the
    operator which hand this agent was dealt."""
    return " ".join(PIECE_NAMES[pt] for pt in ration)


class Game(SharedMixin, PipelineMixin, BlackboardMixin, CoordMixin, SweepMixin, SurvivalMixin, StatsMixin):
    def __init__(self, a, game_id, idx):
        self.a = a
        self.id = game_id
        self.idx = idx

        self.mode = MODE_COMPETITIVE
        self.team = 0          # teams: 0 = A, 1 = B, …
        self.team_count = 0    # teams: how many teams the game is played between (meta team_count)
        self.team_slot = 0     # teams: section index on the team board
        self.w = WIDTH         # board width (competitive 10; a shared board 10 + extra_columns per seat beyond the first)
        self.h = 0             # board height, headroom included (competitive 24; a shared board + extra_rows per seat beyond the first)
        self.spawn_c = SPAWN_COL  # our spawn column: among the seats PRESENT on a shared board, refreshed on every roster change
        self.extra = 0         # shared boards: the extra columns per seat, the step between neighbouring spawn points
        self.pub = resolve_publish(a.pub, False)  # run resolves it again once the board is known

        self.mu = asyncio.Lock()
        self.locked = {}        # settled cells (stack + garbage): (r, c) → WireCell
        self.seqs = {}          # per-cell CAS expectation (last stream seq)
        self.piece = None       # the falling piece we hold (Active)
        self.own_act = {}       # our active cells as the stream has them, by the anchor each carries
        self.others_act = {}    # shared boards: other players' active cells → owner idx
        self.others_piece = {}  # shared boards: other players' pieces by owner idx
        self.piece_idx = 0
        self.score = 0          # OWN cumulative score (line_clear events carry it)
        self.lines = 0          # OWN cumulative cleared lines
        # The scoring's account of our play (scoring.py, score_lock):
        # combo_run counts the consecutive locks that cleared lines (0: none
        # running; the combo count is one less), b2b_chain says the last clear
        # was a difficult one (a quad), so the next one is Back-to-Back.
        self.combo_run = 0
        self.b2b_chain = False

        self.shared_score = 0     # coop: the one shared score (all senders folded)
        self.individual = False   # coop: every seat scored on its own (meta scoring "individual")
        self.total_lines = 0      # coop: all cleared lines (level source)
        self.team_scores = []     # teams: per-team scoreboard
        self.team_lines = []      # teams: per-team line totals (level source)
        self.sender_totals = {}   # last cumulative (score, lines) folded per sender
        self.sender_teams = {}    # the team each sender announced, for the line goal's per-playfield count
        self.sender_stats = {}    # every seat's tally of its locks: the record's per-player stats
        self.sender_pieces = {}   # every seat's piece count as its events last announced it
        self.out_at = {}          # when each seat's game_over landed, as the stream stamped it (datetime)
        self.line_goal = 0        # meta line_goal: the game ends when a playfield has cleared this many lines
        self.survival = ""        # meta survival, cooperative only: the rising floor's tier
        self.goal_decided = False  # the goal was reached (the verdict is in goal_won)
        self.goal_won = False      # we are on the playfield that reached it (or the top scorer of a board scored per seat)

        self.eliminated = {}
        self.results = {}
        self.roster = []
        self.had_rivals = False   # the roster once held someone besides us
        self.peer_ended = False   # coop: a peer's top-out ended the game
        self.pause_alone = False  # the listing's agents_pause_alone, in an open game

        # Which opposing team our next attack lands on: past two teams a
        # clear cannot go to "the other" team, so targets rotate.
        self.attack_rotor = 0

        self.garbage_owed = 0
        self.garbage_by = 0
        self.garbage_seq = 0       # the own board's garbage register's last stream sequence
        self.survival_raises = 0   # survival: the register's count of raises
        self.survival_kick = asyncio.Event()  # survival: a raise landed on the register: the clock re-arms from it
        self.txn_applied = 0
        self.txn_seq = 0

        # The gravity clock (execute): next_gravity is when the piece's next
        # row falls due, set by the spawn, then advanced a step per row owed,
        # so a late turn of the loop owes every row the schedule passed;
        # gravity_debt is the rows a lost batch did not deliver, owed again
        # at once.
        self.next_gravity = 0.0
        self.gravity_debt = 0

        # The shared board's housekeeping (sweep.py).
        self.peer_cells = {}
        self.spawn_block = SpawnBlocker()
        self.spawn_deferred_at = None

        self.soft_dropped = 0      # rows the piece in play soft-dropped along its paths, a point a cell at the lock
        self.resting_since = None  # shared boards: since when gravity has been blocked by the stack at the piece's position
        self.plan_shift = 0        # rows a crewmate's collapse moved the piece in play down by since its plan was made
        self.replan = REPLAN_NONE  # set by whoever finds the plan no longer the thing to do

        self.st = CoordStats()     # the game's account of its traffic (stats.py)

        # The blackboard (blackboard.py, coord.py).
        self.coord = None
        self.claims = {}
        self.bb_rev = 0
        self.bb_notify = asyncio.Event()
        self.bb_epoch = 0
        self.my_claim = None
        self.my_rev = 0
        self.bb_logged = False
        self.deps = []             # the claims the piece in play must let land first
        self.dep_since = None      # since when the drop has been held for them
        self.ignored_claims = {}   # seat → the revision of a claim waited on past its time

        self.meta_seed = 0
        self.player_count = 0
        self.next_count = 0
        self.holes = 0             # holes punched in every garbage row this board raises
        self.random_holes = False  # every garbage row draws its own hole columns
        self.guideline = False     # attacks follow the Modern table (meta guideline_garbage)
        self.ration = []           # meta split_pieces: the piece types THIS seat draws from; empty = the full bag
        self.split = False         # meta split_pieces on a playfield with seatmates
        self.seats_on_pf = 0       # seats sharing our playfield
        self.present_pf = None     # the slots the last deal was made among (sorted); None = every slot
        self.bag = BAG_SINGLE      # meta bag
        self.dead = False

        # The batch pipeline (pipeline.py, guide §4.3).
        self.inflight = 0
        self.inflight_cells = {}
        self.inflight_seq = {}
        self.pipe_predicted_end = 0
        self.pipe_broken = False
        self.pipe_cond = asyncio.Condition(self.mu)
        self.stream_seq_seen = 0

        self.stream = game_stream_name(game_id)
        self.started = asyncio.Event()
        self.ended = asyncio.Event()
        self.subs = []
        self.tasks = set()

    def mark_started(self):
        self.started.set()

    def mark_ended(self):
        self.ended.set()

    def is_ended(self):
        return self.ended.is_set()

    async def wait(self, seconds):
        """Sleep, unless the game or the agent ends first: False then."""
        if seconds <= 0:
            seconds = 0.001
        return await aio.wait_any([self.ended, self.a.stop_ev], seconds) < 0

    def height(self):
        """4 headroom + 20 visible for one seat, and the game's extra_rows
        more for every seat beyond the first on a shared board. The headroom
        is the top rows of every board whatever its height: a taller board
        grows downwards."""
        return self.h if self.h > 0 else STANDARD_HEIGHT

    # ---- subjects ----------------------------------------------------------

    def cell_subject(self, at):
        r, c = at
        if self.mode == MODE_COOPERATIVE:
            return f"jetris.game.{self.id}.playfield.cell.{r}.{c}"
        if self.mode == MODE_TEAMS:
            return f"jetris.game.{self.id}.team.{self.team}.playfield.cell.{r}.{c}"
        return f"jetris.game.{self.id}.player.{self.a.name}.playfield.cell.{r}.{c}"

    def garbage_subject(self, pid):
        """A victim board's garbage register, per player (competitive)."""
        return f"jetris.game.{self.id}.player.{pid}.playfield.garbage"

    def team_garbage_subject(self, t):
        return f"jetris.game.{self.id}.team.{t}.playfield.garbage"

    def own_garbage_subject(self):
        """OUR board's garbage register: our private board's in competitive,
        our team's in teams, the crew's in a survival game (the one its clock
        advances, guide §4.4)."""
        if self.mode == MODE_COOPERATIVE:
            return f"jetris.game.{self.id}.playfield.garbage"
        if self.mode == MODE_TEAMS:
            return self.team_garbage_subject(self.team)
        return self.garbage_subject(self.a.name)

    def txn_subject(self):
        if self.mode == MODE_COOPERATIVE:
            return f"jetris.game.{self.id}.playfield.txn"
        if self.mode == MODE_TEAMS:
            return f"jetris.game.{self.id}.team.{self.team}.playfield.txn"
        return f"jetris.game.{self.id}.player.{self.a.name}.playfield.txn"

    # ---- cell payloads -----------------------------------------------------

    def active_payload(self, p):
        return WireCell(t=p.pt, a=True, r=p.orient, ar=p.row, ac=p.col, pi=self.idx)

    def locked_payload(self, pt):
        return WireCell(o=True, t=pt, pi=self.idx)

    def garbage_hole_columns(self):
        """Draw one set of hole columns: self.holes distinct random columns,
        always leaving at least one adversarial cell in the row. Empty when
        the game raises solid rows. A raise draws once for all its rows (the
        holes line up into a well) unless the game's random_garbage_holes is
        set, in which case every row draws its own."""
        n = max(min(self.holes, self.w - 1), 0)
        return set(random.sample(range(self.w), n))

    def can_place(self, cells):
        h, w, locked = self.height(), self.w, self.locked
        for at in cells:
            r, c = at
            if r < 0 or r >= h or c < 0 or c >= w or at in locked:
                return False
        return True

    def can_place_in(self, locked, cells):
        """can_place against an explicit locked map (used mid-transform)."""
        h, w = self.height(), self.w
        for at in cells:
            r, c = at
            if r < 0 or r >= h or c < 0 or c >= w or at in locked:
                return False
        return True

    def drop_row_locked(self, p):
        row = p.row
        while self.can_place(piece_cells(p.pt, p.orient, row + 1, p.col)):
            row += 1
        return row

    # ---- atomic CAS batch publishing (guide §4.3) --------------------------

    async def publish_batch(self, cells, cas):
        """Publish cell updates as ONE atomic batch, active→locked→empty
        ordered so a relocating piece never transiently vanishes on ordered
        consumers. With cas, each message carries its per-subject
        expected-last sequence; a mismatch drops the whole batch (CASError).
        On success per-cell sequences advance by write-through."""
        order_cells(cells)
        n = len(cells)
        if n == 0:
            return
        nc = self.a.nc
        if n == 1:
            u = cells[0]
            h = {jsx.H_EXPECT_LAST: str(self.seqs.get(u.at, 0))} if cas else None
            seq, conflict = await self.a.cas_request(self.cell_subject(u.at), payload_bytes(u.c), h)
            if conflict:
                raise CASError()
            self.note_stream_seq(seq)
            self.seqs[u.at] = seq
            self.note_own_writes(cells)
            return
        batch_id = rand_id(22)
        commit_seq = 0
        for i, u in enumerate(cells):
            subj = self.cell_subject(u.at)
            h = {jsx.H_BATCH_ID: batch_id, jsx.H_BATCH_SEQ: str(i + 1)}
            if cas:
                h[jsx.H_EXPECT_LAST] = str(self.seqs.get(u.at, 0))
            last = i == n - 1
            if last:
                h[jsx.H_BATCH_COMMIT] = "1"
            if i == 0 or last:  # first (flow control) and commit are requests
                seq, conflict = await self.a.cas_request(subj, payload_bytes(u.c), h)
                if conflict:
                    raise CASError()
                if last:
                    commit_seq = seq
            else:
                await nc.publish(subj, payload_bytes(u.c), headers=h)
        self.note_stream_seq(commit_seq)
        for i, u in enumerate(cells):  # write-through
            self.seqs[u.at] = commit_seq - (n - 1 - i)
        self.note_own_writes(cells)

    def note_own_writes(self, cells):
        """Write a batch of ours through to own_act: the cells it made active
        cells of ours are ours there now, the rest (locked, vacated) no
        longer. Caller holds mu."""
        shared = self.shared()
        for u in cells:
            c = u.c
            if c is not None and c.a and (not shared or c.pi == self.idx):
                self.own_act[u.at] = Active(c.t, c.r, c.ar, c.ac)
            else:
                self.own_act.pop(u.at, None)

    def own_active_cells(self):
        """Every active cell of ours as the stream has it (the piece we hold,
        and any stray of ours) for a write of the piece to vacate whatever it
        leaves: the piece's old cells, and the strays with them."""
        return self.active_cells() + self.stray_own_cells()

    def stray_own_cells(self):
        """The active cells of ours on the stream that are no part of the
        piece we hold (every one of them while we hold none): a copy of the
        piece a stale collapse stranded beside it. The cells an un-acked
        batch is still writing are not strays: theirs is the batch's to
        settle. Ordered top to bottom, left to right."""
        own = set(self.piece.cells()) if self.piece is not None else ()
        return sorted(at for at in self.own_act if at not in own and self.inflight_cells.get(at, 0) <= 0)

    def best_own_piece(self):
        """The piece our active cells on the stream make: the anchor the most
        of them carry (a whole piece over a copy of it in part), ties to the
        top-most; None while we have none. Caller holds mu."""
        counts = {}
        for p in self.own_act.values():
            counts[p] = counts.get(p, 0) + 1
        best = None
        for p, n in counts.items():
            if best is None or n > counts[best] or (n == counts[best] and (p.row, p.col) < (best.row, best.col)):
                best = p
        return best

    async def publish_gated_batch(self, txn, cells):
        """Publish one bulk transform (garbage application or line-clear
        collapse) as a single gated atomic batch: message 1 is the txn
        register carrying the per-subject CAS gate; every cell after it
        carries no expectation (a guard cell excepted). A stale gate
        atomically rejects the WHOLE batch (CASError) and nothing is stored."""
        order_cells(cells)
        n = len(cells) + 1
        batch_id = rand_id(22)
        h = {jsx.H_BATCH_ID: batch_id, jsx.H_BATCH_SEQ: "1", jsx.H_EXPECT_LAST: str(self.txn_seq)}
        if n == 1:
            h[jsx.H_BATCH_COMMIT] = "1"
        seq, conflict = await self.a.cas_request(self.txn_subject(), dumps(txn), h)
        if conflict:
            raise CASError()
        commit_seq = seq
        nc = self.a.nc
        for i, u in enumerate(cells):
            hh = {jsx.H_BATCH_ID: batch_id, jsx.H_BATCH_SEQ: str(i + 2)}
            if u.guard:
                hh[jsx.H_EXPECT_LAST] = str(self.seqs.get(u.at, 0))
            if i == len(cells) - 1:
                hh[jsx.H_BATCH_COMMIT] = "1"
                s, conflict = await self.a.cas_request(self.cell_subject(u.at), payload_bytes(u.c), hh)
                if conflict:
                    raise CASError()
                commit_seq = s
            else:
                await nc.publish(self.cell_subject(u.at), payload_bytes(u.c), headers=hh)
        self.note_stream_seq(commit_seq)
        self.txn_seq = commit_seq - (n - 1)
        self.txn_applied = txn["applied"]
        for i, u in enumerate(cells):
            self.seqs[u.at] = commit_seq - (n - 1 - (i + 1))

    # ---- board writes ------------------------------------------------------

    async def flash(self, cells):
        """Broadcast a dropped write so spectators see it (core NATS, never
        the stream: guide §4.3), and count it."""
        payload = dumps({"pi": self.idx, "c": [[r, c] for r, c in cells]})
        try:
            await self.a.nc.publish(f"jetris.flash.{self.id}.{self.a.name}", payload)
        except Exception:
            pass
        self.st.cas_losses += 1

    async def fetch_board(self):
        """Fetch the last message of every cell of OUR board with the
        multi-subject direct get (guide §4.5): one round trip per FETCH_CHUNK
        cells instead of one per cell. A fetch that needs several chunks is
        bound to the stream's current last sequence so the whole snapshot is
        consistent at one point in the stream; the board consumer's
        strictly-higher-sequence rule folds anything newer. Cells never
        written are absent from the result: (r, c) → (seq, WireCell)."""
        h = self.height()
        subjects = [self.cell_subject((r, c)) for r in range(h) for c in range(self.w)]
        up_to = 0
        if len(subjects) > FETCH_CHUNK:
            info = await self.a.js.stream_info(self.stream)
            up_to = info.state.last_seq
        out = {}
        for start in range(0, len(subjects), FETCH_CHUNK):
            msgs = await jsx.get_last_msgs_for(self.a.nc, self.a.js, self.stream,
                                               subjects[start:start + FETCH_CHUNK], up_to_seq=up_to)
            for m in msgs:
                at = parse_cell_subject(m.subject)
                if at is None:
                    continue
                wc = self._parse_cell(m.data)
                self.note_stream_seq(m.seq)
                out[at] = (m.seq, wc)
        return out

    @staticmethod
    def _parse_cell(data):
        wc = parse_cell(data)
        return EMPTY_CELL if wc is None else wc

    async def resync(self):
        """Refetch our own board from the stream after a dropped write. A
        fetch that fails outright leaves the current state in place (and
        logs) rather than wiping the board to empty."""
        try:
            snap = await self.fetch_board()
        except Exception as e:
            log.info("resync: %s", e)
            return
        self.fold_snapshot(snap)

    def fold_snapshot(self, snap):
        """Replace the local board state (settled cells, our own piece, on
        shared boards the other players' pieces, and every per-cell sequence)
        with one consistent stream snapshot. Caller holds mu."""
        self.locked, self.seqs, self.own_act = {}, {}, {}
        old_clocks = self.peer_cells
        now = time.monotonic()
        shared = self.shared()
        if shared:
            self.others_act, self.others_piece, self.peer_cells = {}, {}, {}
        for at, (seq, wc) in snap.items():
            self.seqs[at] = seq
            if wc.a and (not shared or wc.pi == self.idx):
                self.own_act[at] = Active(wc.t, wc.r, wc.ar, wc.ac)
            elif wc.a:
                p = Active(wc.t, wc.r, wc.ar, wc.ac)
                self.others_act[at] = wc.pi
                self.others_piece[wc.pi] = p
                # The cell's idle clock survives the snapshot where its
                # sequence did: a resync restarts no clock (sweep.py).
                since = now
                cur = old_clocks.get(at)
                if cur is not None and cur.seq == seq:
                    since = cur.at
                self.peer_cells[at] = PeerCell(pi=wc.pi, p=p, seq=seq, at=since)
            elif wc.o:
                self.locked[at] = wc
        # Our piece is the anchor most of our cells carry: a copy of it in
        # part, stranded beside it, is a stray for the next write to sweep.
        self.piece = self.best_own_piece()

    async def _last(self, subject):
        """The last message on a subject of the game stream, None when there
        is none (or it could not be read)."""
        try:
            return await jsx.get_last_msg(self.a.js, self.stream, subject)
        except Exception:
            return None

    async def refresh_registers(self):
        if self.mode == MODE_COOPERATIVE and not self.survival:
            return  # the crew's board carries registers only when its floor rises
        raw = await self._last(self.txn_subject())
        if raw is not None:
            self.txn_seq = raw.seq
            self.txn_applied = o_int(to_obj(raw.data), "applied")
        else:
            self.txn_seq, self.txn_applied = 0, 0
        raw = await self._last(self.own_garbage_subject())
        if raw is not None:
            reg = to_obj(raw.data)
            if o_int(reg, "total") > self.garbage_owed:
                self.garbage_owed = o_int(reg, "total")
            self.garbage_by = o_int(reg, "by")
            if raw.seq > self.garbage_seq:
                self.garbage_seq, self.survival_raises = raw.seq, o_int(reg, "raises")

    async def publish_piece_move(self, to):
        """CAS-batch the diff between the current active cells and `to`.
        Pipelined modes (--publish async/optimistic) send the batch and move
        on: a loss surfaces later and settles the pipeline (pipeline.py).
        Sync mode blocks on the ack; a dropped write flashes and resyncs."""
        cells, old = self.move_batch(to)
        if self.pub != PUB_SYNC:
            if not await self.publish_batch_async(cells, old):
                await self.settle_pipeline()
                return False
            self.piece = to  # the optimistic projection; the ack reconciles sequences
            return True
        try:
            await self.publish_batch(cells, True)
        except CASError:
            await self.flash(old)
            await self.resync()
            return False
        except Exception as e:
            log.info("move: %s", e)
            await self.resync()
            return False
        self.piece = to
        return True

    def move_batch(self, to):
        """The batch that moves our piece to `to`: its cells there, and
        (vacated) every active cell of ours it leaves: the piece's old cells,
        and any stray of ours, which is also what flashes if the batch is
        lost. Caller holds mu."""
        old = self.own_active_cells()
        new_cells = to.cells()
        new_set = set(new_cells)
        payload = self.active_payload(to)
        cells = [CellUpd(c, payload) for c in new_cells]
        cells += [CellUpd(c) for c in old if c not in new_set]
        return cells, old

    def active_cells(self):
        return list(self.piece.cells()) if self.piece is not None else []

    def piece_at(self, index):
        """The piece type at the given index of THIS seat's sequence: the
        seven types, or (when the game deals its pieces out between
        teammates) this seat's ration alone, dealt the way the game's bag
        rule says."""
        return piece_at_bag(self.meta_seed, self.ration, self.bag, index)

    async def spawn(self):
        """Publish a fresh piece. Returns (spawn time, whether the piece is
        on the board, whether we topped out). A spawn covered by LOCKED cells
        is the top-out; covered only by another player's falling piece it is
        DEFERRED (placed False, topped False) and the caller retries
        (gameplays §3). Caller holds mu."""
        await self.settle_pipeline()  # barrier: exact expectations, converged board
        if self.piece is not None:
            # We already own a live piece on the board: adopted from a
            # committed transform that moved it, or re-adopted by a resync.
            # Spawning over it would orphan its cells as a frozen ghost
            # nothing ever vacates. Resume it instead: the caller re-plans
            # from its actual position.
            return time.monotonic(), True, False
        pt = self.piece_at(self.piece_idx)
        n = Active(pt, 0, SPAWN_ROW, self.spawn_c)
        cs = n.cells()
        if not self.can_place(cs):
            return 0.0, False, True
        if self.shared_blocked(cs):
            return 0.0, False, False  # a transient piece is crossing our spawn
        payload = self.active_payload(n)
        cells = [CellUpd(c, payload) for c in cs]
        cells += [CellUpd(c) for c in self.stray_own_cells()]  # a cell of ours left behind goes with the spawn, vacated
        try:
            await self.publish_batch(cells, True)
        except CASError:
            await self.flash(cs)
            await self.resync()
            return time.monotonic(), self.piece is not None, False
        except Exception as e:
            log.info("spawn: %s", e)
            await self.resync()
            return time.monotonic(), self.piece is not None, False
        self.piece = n
        spawn_t = time.monotonic()
        self.trace("spawned %s at col %d", PIECE_NAMES[pt], self.spawn_c)
        self.next_gravity, self.gravity_debt = spawn_t + self.gravity_now(), 0
        self.soft_dropped, self.resting_since, self.replan, self.plan_shift = 0, None, REPLAN_NONE, 0
        self.my_claim, self.my_rev, self.deps, self.dep_since, self.ignored_claims = None, 0, [], None, {}
        return spawn_t, True, False

    async def lock_piece(self, dest):
        """Settle the piece at dest and run the lock's consequences: the
        clears, the score, the announcement, the piece index. On a
        competitive board the lock is authoritative, published without a CAS
        (guide §4.3): nobody else writes there. On a shared board it is a CAS
        batch with a bounded merge-retry: the landing cells carry their
        last-seen sequences, so a stale view of ours can never bury a
        crewmate's piece that moved into them within the round trip. The
        batch is rejected instead, the board refetched, and the lock retried
        where the piece stands; unless a landing cell now holds another
        seat's cell (theirs to keep), our piece is gone or moved (a transform
        took it), or the retries run out: then the lock is DROPPED (False),
        the piece active where the stream has it, for the caller to re-plan.
        Caller holds mu."""
        # The hard drop's fall, two points a cell (score_lock): from where
        # the piece stood to where it settles, less the rows the path
        # soft-dropped on the way; nothing when gravity landed it there.
        fell = 0
        before = self.piece
        if before is not None:
            fell = max(dest.row - before.row - self.soft_dropped, 0)
        dest_cells = dest.cells()
        dest_set = set(dest_cells)
        lp = self.locked_payload(dest.pt)
        shared = self.shared()
        attempt = 0
        while True:
            old = self.own_active_cells()  # the piece's cells, and any stray of ours: swept with the lock
            cells = [CellUpd(c, lp) for c in dest_cells]
            cells += [CellUpd(c) for c in old if c not in dest_set]
            try:
                await self.publish_batch(cells, shared)
                break
            except CASError:
                pass
            except Exception as e:
                log.info("lock: %s", e)
                await self.resync()
                return False
            await self.flash(dest_cells)
            self.st.lock_retries += 1
            await self.resync()
            if self.piece is None or self.piece != before:
                self.st.locks_dropped += 1
                return False  # gone, or moved by a transform: re-plan from where it is
            for c in dest_cells:
                if c in self.others_act or c in self.locked:
                    # A crewmate's piece is where we meant to land, or the
                    # stack rose under us: the landing is elsewhere now.
                    self.st.locks_dropped += 1
                    return False
            if attempt + 1 >= LOCK_RETRIES:
                log.info("lock: dropped after %d CAS losses", LOCK_RETRIES)
                self.st.locks_dropped += 1
                return False
            # A short, seat-staggered pause before the retry.
            await asyncio.sleep(min((attempt + 1 + self.idx) * 0.0002, 0.002))
            attempt += 1
        for c in dest_cells:
            self.locked[c] = lp
        self.trace("locked at rot %d col %d row %d after %d attempt(s)", dest.orient, dest.col, dest.row,
                   self.st.lock_retries)
        self.piece = None
        if self.coord is not None:
            # Tell the board: the claim is fulfilled, at these cells (the
            # teammates project them until the lock's echo reaches them).
            self.post_locked_async(self.claim_for(
                dest, Placement(orient=dest.orient, col=dest.col, dest=dest_cells), PHASE_LOCKED, 0.0))
            self.my_claim, self.deps = None, []
            self.end_dep_wait(time.monotonic())
        done = self.completed_rows_shared() if shared else self.completed_rows()
        if done:
            await self.clear_rows(done, fell)
        else:
            await self.score_lock(0, fell, None)  # no line: the drop's points, and the combo ends
        self.piece_idx += 1
        if self.mode == MODE_COMPETITIVE:
            await self.bump_meta_piece_idx()  # shared modes: every seat has its own index; the meta field stays the creator's
        return True

    def completed_rows(self):
        """Every cell settled and at least one of them the stack's: a solid
        garbage row is permanent, a garbage row whose holes the stack filled
        clears like any other (gameplays §4)."""
        out = []
        locked, w = self.locked, self.w
        for r in range(self.height()):
            stack = False
            for c in range(w):
                cc = locked.get((r, c))
                if cc is None:
                    break
                if not cc.g:
                    stack = True
            else:
                if stack:
                    out.append(r)
        return out

    async def clear_rows(self, rows, fell):
        """Collapse completed rows: competitive and teams as a GATED
        transform (teams also shifting teammates' pieces down with the
        stack), coop as a CAS merge-retry batch, unless the crew's floor
        rises (survival), when the crew's board carries the gate and the
        clear takes it like a team's, so it can never race the floor's
        shrink from a stale snapshot; then score the clear, announce it, and
        route the attack (competitive: every surviving opponent; teams: the
        opposing board; coop: nobody). Caller holds mu."""
        cleared_rows = list(rows)
        cleared = 0
        if self.mode == MODE_COOPERATIVE and not self.survival:
            cleared = await self.clear_rows_coop(rows)
        else:
            shared = self.shared()
            for attempt in range(3):
                if attempt > 0:
                    await self.refresh_registers()
                    await self.resync()
                    rows = self.completed_rows_shared() if shared else self.completed_rows()
                    if not rows:
                        return
                txn = txn_payload(self.txn_applied, "clear", self.idx)
                if shared:
                    new_locked, moved = self.clear_projection(rows)
                    diff = self.diff_cells(new_locked)
                    for pi, np in moved.items():
                        op = self.others_piece[pi]
                        old_cells = set(op.cells())
                        new_set = set(np.cells())
                        pl = WireCell(t=np.pt, a=True, r=np.orient, ar=np.row, ac=np.col, pi=pi)
                        # guard: a teammate locking or moving mid-shift must
                        # atomically reject this batch (the recompute sees
                        # their new reality), never strand a ghost at the
                        # shifted spot.
                        diff += [CellUpd(c, pl, True) for c in sorted(new_set)]
                        diff += [CellUpd(c, None, True) for c in sorted(old_cells)
                                 if c not in new_set and c not in new_locked]
                    try:
                        await self.publish_gated_batch(txn, diff)
                    except CASError:
                        continue
                    except Exception as e:
                        log.info("clear: %s", e)
                        return
                    self.locked = new_locked
                    for pi, np in moved.items():
                        self.others_piece[pi] = np
                    self.reindex_others()
                else:
                    removed = set(rows)
                    new_locked = {}
                    shift = 0
                    for r in range(self.height() - 1, -1, -1):
                        if r in removed:
                            shift += 1
                            continue
                        for c in range(self.w):
                            cc = self.locked.get((r, c))
                            if cc is not None:
                                new_locked[(r + shift, c)] = cc
                    diff = self.diff_cells(new_locked)
                    try:
                        await self.publish_gated_batch(txn, diff)
                    except CASError:
                        continue
                    except Exception as e:
                        log.info("clear: %s", e)
                        return
                    self.locked = new_locked
                cleared = len(rows)
                break
        if cleared == 0:
            return
        c = await self.score_lock(cleared, fell, cleared_rows)
        rows_owed = c.attack_rows(self.guideline)
        if rows_owed > 0:
            if self.mode == MODE_COMPETITIVE:
                await self.bump_victim_ledgers(rows_owed)
            elif self.mode == MODE_TEAMS:
                await self.bump_team_ledger(rows_owed)

    def attack_rows(self, lines):
        """The garbage a plain clear of `lines` rows owes: one row per line,
        or under the game's guideline_garbage rule the Modern table."""
        return ClearInfo(lines=lines).attack_rows(self.guideline)

    def score_level(self):
        """The level the next clear is scored at (the level BEFORE the
        clear): the one gravity falls at."""
        return self.level()

    async def score_lock(self, lines, fell, cleared_rows):
        """The scoring's account of the lock that just settled (scoring.py;
        gameplays § Scoring): a clear of `lines` rows (the run of consecutive
        clearing locks is the combo, a quad extends the Back-to-Back chain
        and a plain clear breaks it, a board left empty is a perfect clear)
        or no clear at all, which ends the combo and leaves the chain alone.
        The points are the clear's at the level before it plus the hard
        drop's two a cell (fell); they go to our own totals and to the shared
        or team score. Every lock that scored on a shared board is announced
        with our cumulative totals, a clear or not, so every peer's shared
        score converges on every point; in competitive only a clear is.
        Caller holds mu."""
        level = self.score_level()
        c = ClearInfo()
        if lines > 0:
            self.combo_run += 1
            c = ClearInfo(lines=lines, combo=self.combo_run - 1, perfect=len(self.locked) == 0)
            c.back_to_back = self.b2b_chain and c.difficult()
            self.b2b_chain = c.difficult()
        else:
            self.combo_run = 0
        pts = c.points(level) + drop_points(self.soft_dropped, fell)
        self.soft_dropped = 0
        if self.mode == MODE_COOPERATIVE:
            self.shared_score += pts
            self.total_lines += lines
        elif self.mode == MODE_TEAMS:
            self.team_scores[self.team] += pts
            self.team_lines[self.team] += lines
        self.score += pts
        self.lines += lines
        if lines > 0 or (pts > 0 and self.shared()):
            # Our own tally, counted here as every peer counts it off the event.
            self.stats_for(self.a.name).tally(c, int(c.spin), self.mode != MODE_COOPERATIVE, self.guideline)
            await self.publish_line_clear(c, pts, cleared_rows)
        return c

    async def _bump_ledger(self, subject, lines, what):
        """CAS-add `lines` to one garbage register: the register is a
        cumulative total, so concurrent attackers converge. A lost add race
        is refreshed and re-added, with a bound."""
        for _ in range(20):
            total = seq = 0
            raw = await self._last(subject)
            if raw is not None:
                total, seq = o_int(to_obj(raw.data), "total"), raw.seq
            try:
                _, conflict = await self.a.cas_request(
                    subject, dumps(garbage_reg(total + lines, self.idx)), {jsx.H_EXPECT_LAST: str(seq)})
            except Exception as e:
                log.info("%s: %s", what, e)
                return
            if not conflict:
                return

    async def bump_team_ledger(self, lines):
        """CAS-add `lines` to ONE opposing team's garbage register: between
        two teams the other one, and past that the rotation's next, so an
        attack weighs what it always did rather than hitting every opponent
        at once."""
        await self._bump_ledger(self.team_garbage_subject(self.next_garbage_target()), lines, "team ledger bump")

    def diff_cells(self, new_locked):
        """The cell updates that turn self.locked into new_locked."""
        diff = []
        locked = self.locked
        for r in range(self.height()):
            for c in range(self.w):
                at = (r, c)
                nv, ov = new_locked.get(at), locked.get(at)
                if nv != ov:
                    diff.append(CellUpd(at, nv))
        return diff

    async def bump_victim_ledgers(self, lines):
        """CAS-add `lines` to every surviving opponent's garbage register."""
        for p in list(self.roster):
            pid = p["player_id"]
            if pid == self.a.name or self.eliminated.get(pid):
                continue
            await self._bump_ledger(self.garbage_subject(pid), lines, f"ledger bump {pid}")

    async def bump_meta_piece_idx(self):
        try:
            meta, seq = await self.a.fetch_meta(self.id)
            meta["piece_idx"] = self.piece_idx
            await self.a.meta_publish(self.id, dumps(meta), seq)
        except Exception:
            pass

    async def apply_owed_garbage(self):
        """Apply the outstanding deficit as one gated cascade: shift the
        stack up, fill the bottom with adversarial rows (punched with the
        game's garbage_holes, the same columns on every row of the raise),
        lift the falling piece the minimum needed to clear the risen stack
        (top out if it is pushed off), and top out if locked cells are shoved
        past row 0."""
        async with self.mu:
            await self.settle_pipeline()  # barrier: the gated batch needs exact expectations
            for _ in range(3):
                n = self.garbage_owed - self.txn_applied
                if n <= 0 or self.dead:
                    return
                causer = self.garbage_by
                gp = garbage_payload(causer)
                h = self.height()
                board_full = any(r < n for r, _ in self.locked)
                new_locked = {(r - n, c): v for (r, c), v in self.locked.items() if r - n >= 0}
                # The floor's holes follow the seed and the rows' ordinals
                # (the board's count of rows applied so far) so the well
                # moves every four rows whichever raises the rows came in,
                # and a recompute after a lost gate draws exactly the same;
                # an attack's rows draw at random, once per raise or once per
                # row.
                floor = None
                if self.survival:
                    floor = survival_holes(self.w, self.holes, self.txn_applied, n, self.random_holes, self.meta_seed)
                holes = None
                for r in range(h - n, h):
                    if floor is not None:
                        holes = set(floor[r - (h - n)])
                    elif holes is None or self.random_holes:
                        holes = self.garbage_hole_columns()
                    for c in range(self.w):
                        if c not in holes:
                            new_locked[(r, c)] = gp
                # Lift EVERY falling piece (ours and, on a shared board,
                # every teammate's) the minimum rows that clear the risen
                # stack. Lifts cascade: a lifted piece is an obstacle for
                # pieces above it (gameplays §5). A piece pushed off the top
                # eliminates its owner (txn.topped).
                pieces = []  # [owner, from, to, squeezed]
                if self.piece is not None:
                    pieces.append([self.idx, self.piece, None, True])
                for pi in sorted(self.others_piece):
                    pieces.append([pi, self.others_piece[pi], None, True])
                # Lowest piece first, so a rising column of pieces cascades upward.
                pieces.sort(key=lambda pc: -pc[1].row)
                placed_cells = set()
                topped_owners = []
                for pc in pieces:
                    frm = pc[1]
                    k = 0
                    while True:
                        cand = Active(frm.pt, frm.orient, frm.row - k, frm.col)
                        cs = cand.cells()
                        if min(r for r, _ in cs) < 0:
                            break  # off the top: squeezed out
                        if all(0 <= c < self.w and r < h and (r, c) not in new_locked and (r, c) not in placed_cells
                               for r, c in cs):
                            pc[2], pc[3] = cand, False
                            placed_cells.update(cs)
                            break
                        k += 1
                    if pc[3]:
                        topped_owners.append(pc[0])
                diff = self.diff_cells(new_locked)
                for owner, frm, to, squeezed in pieces:
                    guard = owner != self.idx
                    old_cells = sorted(frm.cells())
                    if squeezed:
                        diff += [CellUpd(c, None, guard) for c in old_cells if c not in new_locked]
                        continue
                    if to == frm:
                        # Untouched teammate piece: guard it with expectation
                        # carriers so a concurrent move rejects this whole
                        # batch instead of being buried under a stale
                        # projection.
                        if guard:
                            pl = WireCell(t=frm.pt, a=True, r=frm.orient, ar=frm.row, ac=frm.col, pi=owner)
                            diff += [CellUpd(c, pl, True) for c in old_cells]
                        continue
                    new_set = set(to.cells())
                    pl = WireCell(t=to.pt, a=True, r=to.orient, ar=to.row, ac=to.col, pi=owner)
                    diff += [CellUpd(c, pl, guard) for c in sorted(new_set)]
                    diff += [CellUpd(c, None, guard) for c in old_cells if c not in new_set and c not in new_locked]
                txn = txn_payload(self.garbage_owed, "shrink", self.idx, topped_owners, board_full)
                try:
                    await self.publish_gated_batch(txn, diff)
                except CASError:
                    await self.refresh_registers()
                    await self.resync()
                    continue
                except Exception as e:
                    log.info("garbage: %s", e)
                    return
                self.locked = new_locked
                my_squeezed = False
                for owner, frm, to, squeezed in pieces:
                    if owner == self.idx:
                        if squeezed:
                            self.piece, my_squeezed = None, True
                        elif to != frm:
                            self.piece = to
                    elif squeezed:
                        self.others_piece.pop(owner, None)
                    elif to != frm:
                        self.others_piece[owner] = to
                self.reindex_others()
                log.info("garbage: +%d row(s) from player %d", n, causer)
                if my_squeezed or board_full:
                    self.dead = True
                return

    # ---- consumers ---------------------------------------------------------

    async def start_consumers(self):
        async def on_meta(m):
            st = o_str(to_obj(m.data), "status")
            if st == "in_progress":
                self.mark_started()
            if st in ("finished", "archived", "cancelled"):
                self.mark_ended()

        async def on_event(m):
            ev = parse_event(m.data)
            if ev["kind"] == "line_clear":
                await self.fold_line_clear(ev)
                await self.check_line_goal(ev)
            elif ev["kind"] == "game_over":
                # Stamped by the server: where the sender's time played ends
                # (the record's per-player stats).
                try:
                    stamp = m.metadata.timestamp
                except Exception:
                    stamp = None
                if stamp is not None:
                    async with self.mu:
                        self.out_at[ev["player_id"]] = stamp
                await self.on_game_over(ev)

        async def on_garbage(m):
            reg = to_obj(m.data)
            async with self.mu:
                if o_int(reg, "total") > self.garbage_owed:
                    self.garbage_owed = o_int(reg, "total")
                self.garbage_by = o_int(reg, "by")
                need = self.garbage_owed > self.txn_applied and not self.dead
            if need:
                await self.apply_owed_garbage()

        await self.consume(meta_subject(self.id), on_meta)
        await self.consume(f"jetris.game.{self.id}.events.>", on_event)
        if self.shared():
            # The shared board's cells (and its garbage/txn registers) arrive
            # on one board consumer.
            await self.consume(self.board_filter(), self.handle_board_msg)
        else:
            await self.consume(self.garbage_subject(self.a.name), on_garbage)
        # A consumer whose stream was deleted goes quiet rather than failing:
        # a probe of the stream ends the game then.
        aio.spawn(self.tasks, self._stream_probe())

    async def consume(self, subject, handler):
        """Attach an ordered consumer filtered to one subject, from the
        stream's start."""

        async def cb(m):
            try:
                await handler(m)
            except asyncio.CancelledError:
                raise
            except Exception as e:  # one bad delivery must not stop the consumer
                log.info("consumer %s: %r", subject, e)

        sub = await self.a.js.subscribe(subject, stream=self.stream, cb=cb, ordered_consumer=True)
        self.subs.append(sub)
        return sub

    async def _stream_probe(self):
        misses = 0
        while not self.is_ended() and not self.a.stopping():
            if not await self.wait(5.0):
                return
            try:
                await self.a.js.stream_info(self.stream)
                misses = 0
            except jsx.NotFoundError:
                misses += 1
                if misses >= 2:
                    self.mark_ended()
                    return
            except Exception:
                pass

    async def stop_consumers(self):
        for sub in self.subs:
            try:
                await sub.unsubscribe()
            except Exception:
                pass
        self.subs = []
        for t in list(self.tasks):
            t.cancel()

    # ---- the game ----------------------------------------------------------

    def opponent_names(self):
        """The other seats' display names, for the log."""
        names = []
        for p in self.roster:
            if p["player_id"] == self.a.name:
                continue
            n = p["name"] or p["player_id"]
            if self.mode == MODE_TEAMS:
                n += " (team " + team_letter(p["team"]) + ")"
            names.append(n)
        return ", ".join(names) if names else "nobody else"

    async def run(self):
        try:
            meta, _ = await self.a.fetch_meta(self.id)
        except Exception as e:
            log.info("fetch meta: %s", e)
            return False
        self.meta_seed = o_int(meta, "seed")
        self.mode = o_int(meta, "mode")
        self.player_count = o_int(meta, "player_count")
        self.individual = (self.mode == MODE_COOPERATIVE and self.player_count > 1
                           and o_str(meta, "scoring") == "individual")
        self.line_goal = max(o_int(meta, "line_goal"), 0)
        if self.line_goal > 0:
            log.info("line goal: the first playfield to clear %d lines wins", self.line_goal)
        if self.mode == MODE_COOPERATIVE:
            self.survival = normalize_survival(o_str(meta, "survival"))
        if self.survival:
            log.info("survival: the floor rises on the %s tier until the crew tops out", self.survival)
        if self.individual:
            log.info("scoring: every seat on its own, the top score wins")
        self.team_count = normalize_team_count(o_int(meta, "team_count"))
        self.next_count = o_int(meta, "next_count")
        self.holes = min(max(o_int(meta, "garbage_holes"), 0), MAX_GARBAGE_HOLES)
        if self.survival:
            self.holes = max(self.holes, MIN_SURVIVAL_HOLES)  # a row the floor raises must be clearable
        self.random_holes = o_bool(meta, "random_garbage_holes") and self.holes > 0
        self.guideline = o_bool(meta, "guideline_garbage")
        self.bag = normalize_bag(o_str(meta, "bag"))
        if self.bag != BAG_SINGLE:
            log.info("bag rule: %s", bag_label(self.bag))
        listing = self.a.listings.get(self.id, {})
        self.roster = players_of(listing)
        # The creator's pause-when-alone rule (guide §5 step 4): only an open
        # game's roster can dwindle to us alone, so it is read off the
        # listing there alone.
        self.pause_alone = o_bool(listing, "agents_pause_alone") and not o_bool(listing, "invite_only")
        if self.pause_alone:
            log.info("agents pause when alone: left as the only player, we wait for company")
        # Board geometry and our spawn section (gameplays §2/§3/§5). Every
        # seat tracks its own piece index on shared boards; competitive keeps
        # the meta counter.
        extra = extra_columns(o_int(meta, "extra_columns"))
        extra_r = extra_rows(o_int(meta, "extra_rows"))
        self.w, self.h, self.spawn_c = WIDTH, STANDARD_HEIGHT, SPAWN_COL
        if self.mode == MODE_COOPERATIVE:
            self.w = shared_width(self.player_count, extra)
            self.h = shared_height(self.player_count, extra_r)
            self.extra = extra
            self.seats_on_pf = self.player_count
            # Our spawn point among the seats present (gameplays §3): an open
            # game's roster is whoever holds a seat right now, refreshed by
            # on_roster as it changes.
            self.spawn_c = self.spawn_column(self.present_slots(self.roster))
            # The piece split (gameplays §5) on the crew's board: the seven
            # types dealt out between the seats off the game's seed, this
            # seat playing only its own ration.
            if o_bool(meta, "split_pieces") and self.player_count > 1:
                self.split = True
                self.ration = piece_set_for(self.meta_seed, self.player_count, self.idx)
                log.info("split pieces: seat %d holds %s", self.idx, ration_names(self.ration))
        elif self.mode == MODE_TEAMS:
            self.team_scores, self.team_lines = [0] * self.teams(), [0] * self.teams()
            for p in self.roster:
                if p["player_id"] == self.a.name:
                    self.team, self.team_slot = p["team"], p["team_slot"]
            team_size = self.team_size()
            self.w = shared_width(team_size, extra)
            self.h = shared_height(team_size, extra_r)
            self.extra = extra
            self.seats_on_pf = team_size
            self.spawn_c = self.spawn_column(self.present_slots(self.roster))
            if o_bool(meta, "split_pieces") and team_size > 1:
                self.split = True
                self.ration = piece_set_for(self.meta_seed, team_size, self.team_slot)
                log.info("split pieces: slot %d holds %s", self.team_slot, ration_names(self.ration))
        else:
            self.piece_idx = o_int(meta, "piece_idx")
        self.pub = resolve_publish(self.a.pub, self.shared())
        if min(self.next_count, self.a.tn.lookahead) >= 2 and getattr(self.a, "plan_pool", None) is not None:
            self.a.plan_pool.warm()  # the lookahead's workers, up before the first piece
        # The blackboard (guide §1.4): a team to coordinate with (the crew of
        # a board scored together, or our team) and a bucket to do it on.
        if self.a.coordinate and self.a.blackboard() is not None:
            b = board_name(self.mode, self.team, self.individual, self.seats_on_pf)
            key = claim_key(self.id, b, self.idx) if b else ""
            if key:
                self.coord = Coordinator(board=b, key=key, watch=f"{self.id}.{b}.*")
                log.info("coordinating with the %s board's agents on the blackboard (%s)", b, key)
        try:
            return await self._run()
        finally:
            await self.stop_consumers()
            if self.coord is not None:
                try:
                    await asyncio.wait_for(self.drop_claim(), 3.0)
                except Exception:
                    pass

    async def _run(self):
        try:
            await self.start_consumers()
        except Exception as e:
            log.info("consumers: %s", e)
            return False
        if self.coord is not None:
            aio.spawn(self.tasks, self.claim_watch())  # our board's claims, for the life of the game

        if await self.a.toggle_ready(self.id):
            log.info("all ready, running the countdown")
            await self.a.run_countdown(self.id)
        # A game that never fills or readies must not wedge the agent
        # forever: after the wait timeout we walk away cleanly (guide §5 step
        # 6) and return to the lobby.
        which = await aio.wait_any([self.started, self.ended, self.a.stop_ev], self.a.wait)
        if which < 0:
            log.info("game %s did not start within %ss, un-joining", self.id, self.a.wait)
            await self.a.unjoin_game(self.id)
            return False
        if which != 0:
            return False
        pl = players_of(self.a.listings.get(self.id, {}))
        if pl:
            self.roster = pl
        log.info("game started with %s", self.opponent_names())
        async with self.mu:
            self.st.started = time.monotonic()
        if self.survival:
            aio.spawn(self.tasks, self.run_survival_clock())  # the rising floor's clock, beside the piece loop
        if self.shared() and self.seats_on_pf > 1:
            aio.spawn(self.tasks, self.sweep_loop())  # the idle-piece sweep every playing peer owes the board

        won = await self.play_pieces()
        async with self.mu:
            log.info("%s", self.stats_line())
        if self.a.stopping() and not self.is_ended():
            # Stopped mid-game: leave the board clean and the seat free
            # (guide §5 step 7) instead of vanishing with a piece on the
            # board.
            await self.leave_running()
            return False
        async with self.mu:
            by_goal = self.goal_decided
        if won or by_goal or await self.finish_on_end():
            # A winner finishes the game (the CAS makes it idempotent: every
            # winner may). A goal-ended game, and the crew's game a peer's
            # top-out ended, are finished by every player alike, so the
            # finish never waits on one client that knows the rule or stays
            # connected.
            await self.transition_finished_and_archive()
        else:
            await self.wait_for_end()
        return won

    async def play_pieces(self):
        deferrals = 0
        rethinking = False  # the piece in play is re-planned from where it stands (request_replan)
        while not self.is_ended():
            async with self.mu:
                dead = self.dead
            if dead:
                return await self.top_out()
            # Between pieces (no piece of ours on the board) is where an
            # agent left alone waits for company, when the game says so.
            if not await self.pause_while_alone():
                break
            async with self.mu:
                spawn_t, placed, over = await self.spawn()
            if over:
                return await self.top_out()
            if not placed:
                # Deferred spawn: a teammate's falling piece is crossing our
                # spawn cells (or our spawn batch lost its CAS). Retry
                # shortly: the blocker falls away within a tick (gameplays
                # §3). A blocker that stays put is a piece nobody is moving:
                # after SPAWN_BLOCKED_VACATE_AFTER we vacate it whole and
                # spawn (note_spawn_blocked, sweep.py); and one that never
                # falls away for another reason is a lock we mis-tracked, so
                # the board is also rebuilt from the stream now and then
                # rather than wedge forever.
                deferrals += 1
                async with self.mu:
                    now = time.monotonic()
                    if self.spawn_deferred_at is None:
                        self.spawn_deferred_at = now
                        self.st.deferred_spawns += 1
                    if self.piece is None:
                        await self.note_spawn_blocked(self.spawn_cells(), now)
                    strays = self.stray_own_cells()
                    if strays:
                        # Cells of ours left behind hold a crewmate's spawn
                        # box as surely as theirs hold ours: swept now, not
                        # at our next write, which waits on this spawn.
                        try:
                            await self.publish_batch([CellUpd(c) for c in strays], True)
                        except CASError:
                            await self.resync_shared()
                        except Exception:
                            pass
                    if deferrals % 30 == 0:
                        await self.settle_pipeline()
                        await self.resync_shared()
                if not await self.wait(0.1):
                    break
                continue
            deferrals = 0
            async with self.mu:
                if self.spawn_deferred_at is not None:
                    self.st.deferred_ms += int((time.monotonic() - self.spawn_deferred_at) * 1000)
                    self.spawn_deferred_at = None
                self.spawn_block = SpawnBlocker()
            # The think pause: the difficulty's for a fresh piece, a short
            # one for a re-plan (the piece is on the board already). The plan
            # is made, and claimed, BEFORE it: the claim is on the blackboard
            # for the whole of the pause, which is when the teammates need
            # it.
            delay = self.think_pause()
            if rethinking:
                delay, rethinking = RETHINK_DELAY, False
            async with self.mu:
                piece = self.piece
            if piece is None:
                continue  # spawn was dropped by CAS; resynced, try again
            plan, pending, ok = await self.plan(piece, delay)
            if not ok:
                return await self.top_out()
            if not await self.wait(delay):
                break
            plan = await self.confirm_plan(piece, plan, pending)
            await self.execute(plan, spawn_t)
            async with self.mu:
                dead = self.dead
                if self.piece is not None and self.replan != REPLAN_NONE:
                    log.info("re-planning: %s", replan_name(self.replan))
                    self.replan, rethinking = REPLAN_NONE, True
            if dead:
                return await self.top_out()
            if await self.win_check():
                return True
        return not self.dead and await self.win_check()

    async def plan(self, p, pause):
        """Choose the piece's placement and, on a coordinated board, claim it
        (guide §1.4): the placement is planned on the projection of the
        teammates' claims (build_projection) and posted to the blackboard.
        What follows the post (waiting for the claim's own echo, and with it
        every claim made before it, then yielding to the earlier ones that
        overlap) is confirm_plan's, run once the caller has thought
        (play_pieces): the echo's round trip overlaps the pause instead of
        preceding it. pause is how long that will be, part of what the
        teammates are told to expect. Returns (placement, pending claim, ok):
        ok is False only with nowhere to place the piece at all, a top-out.
        mu is held for the snapshot alone: the search and the blackboard
        round trip run outside it."""
        began = time.monotonic()
        async with self.mu:
            proj = None
            if self.coord is not None:
                gr, proj = self.build_projection()
            else:
                gr = self.to_grid()
            piece_idx, spawn_c = self.piece_idx, self.spawn_c
        tn = self.a.tn
        upcoming = revealed_pieces(self.meta_seed, self.ration, self.bag, piece_idx, self.next_count, tn.lookahead)
        ranked = await self._search(gr, p, spawn_c, upcoming, proj)
        pl = choose(ranked, tn, self.a.rng)
        if pl is None and proj is not None:
            # Nothing on the projection (a claimed landing walls the piece in
            # where it stands): the board itself decides whether there is
            # nowhere to go (a top-out) or just nowhere clear of the claims.
            async with self.mu:
                gr = self.to_grid()
            ranked = await self._search(gr, p, spawn_c, upcoming, None)
            pl = choose(ranked, tn, self.a.rng)
            proj = None
        took = time.monotonic() - began
        if pl is None:
            self.trace("no placement at all (%d candidates)", len(ranked))
            return Placement(), None, False
        if self.coord is None or proj is None:
            self.trace("planned rot %d col %d row %d (score %.1f, %d candidates, %dms)", pl.orient, pl.col,
                       pl.drop_row, pl.score, len(ranked), int(took * 1000))
            return pl, None, True
        self.trace("planned rot %d col %d row %d (score %.1f, %d candidates, wait on %s, %dms)", pl.orient, pl.col,
                   pl.drop_row, pl.score, len(ranked), pl.wait_on, int(took * 1000))
        eta = pause + tn.move_delay + 0.05
        c = self.claim_for(p, pl, PHASE_PLANNED, eta)
        try:
            rev = await self.post_claim(c)
        except Exception as e:
            self.bb_log_once(e)
            return pl, None, True  # uncoordinated for this piece
        return pl, PendingClaim(c=c, rev=rev, ranked=ranked, proj=proj, eta=eta), True

    async def _search(self, gr, p, spawn_c, upcoming, proj):
        """The planner's search. A shallow one is a millisecond, run here; a
        deep preview's is the beam's lookaheads, run side by side on the
        agent's worker processes (workers.py), or, without them, one after
        the other in a thread: either way off the event loop, so the
        consumers and the heartbeats keep running under it."""
        if len(upcoming) < 2:
            return plan_placements(gr, p.pt, p.row, p.col, spawn_c, upcoming, proj)
        pool_owner = getattr(self.a, "plan_pool", None)
        pool = pool_owner.get() if pool_owner is not None else None
        if pool is not None:
            out, order, jobs = plan_roots(gr, p.pt, p.row, p.col, spawn_c, upcoming, proj)
            loop = asyncio.get_running_loop()
            try:
                totals = await asyncio.gather(*(loop.run_in_executor(pool, lookahead_job, *job) for job in jobs))
                return plan_finish(out, order, totals, proj)
            except asyncio.CancelledError:
                raise
            except Exception as e:
                pool_owner.failed(e)
        return await asyncio.to_thread(plan_placements, gr, p.pt, p.row, p.col, spawn_c, upcoming, proj)

    async def confirm_plan(self, p, pl, pc):
        """The second half of a coordinated plan: once the claim's own echo
        is in (await_rev: every claim made before ours has then been seen)
        the placement is checked against those (classify). Overlapped by an
        earlier claim, it yields: the next placement clear of every earlier
        landing (the ranking has it already, no new plan needed) is claimed
        in its turn, until one stands (or three have been tried, and the wire
        decides, as it always did). Ordered after earlier claims, it is kept,
        with the waits they impose (deps). Returns the placement to execute."""
        if pc is None:
            return pl
        c, rev, ranked = pc.c, pc.rev, pc.ranked
        ok, timed_out = await self.await_rev(rev, ECHO_TIMEOUT)
        if not ok:
            if timed_out:
                async with self.mu:
                    self.st.echo_timeouts += 1
            return pl
        attempt = 0
        tn = self.a.tn
        while True:
            async with self.mu:
                kind, more, taken = self.classify(p, pl, rev)
                if kind != CONFLICT_OVERLAP or attempt >= 2:
                    self.my_claim, self.my_rev = c, rev
                    self.deps = self.merge_deps(pl, pc.proj, more)
                    self.trace("claimed at rev %d: %s, %d dep(s)", rev, kind, len(self.deps))
                    return pl
                self.st.claims_yielded += 1
                self.trace("yielding at rev %d: an earlier claim overlaps", rev)
            ranked = clear_of(ranked, taken)
            nxt = choose(ranked, tn, self.a.rng)
            if nxt is None:
                return pl  # nothing clear of them: the wire decides
            pl = nxt
            c = self.claim_for(p, pl, PHASE_PLANNED, tn.move_delay + 0.05)
            try:
                rev = await self.post_claim(c)
            except Exception:
                return pl
            ok, _ = await self.await_rev(rev, ECHO_TIMEOUT)
            if not ok:
                return pl
            attempt += 1

    def to_grid(self):
        """Snapshot the settled board for the planner (caller holds mu).
        Other players' falling pieces are marked as obstacles (3): solid for
        placement and every feature, but never completing a row: the planner
        routes around the transient obstacle without fantasizing a clear
        through it."""
        gr = Grid(self.height(), self.w)
        for (r, c), v in self.locked.items():
            if 0 <= r < gr.h and 0 <= c < gr.w:
                gr.set(r, c, 2 if v.g else 1)
        for r, c in self.others_act:
            if 0 <= r < gr.h and 0 <= c < gr.w:
                gr.set(r, c, 3)
        return gr

    async def execute(self, plan, spawn_t):
        """Drive the piece to its plan: the path to the planned column and
        orientation (find_path), then the hard drop and the lock, as ONE
        batch when the way is clear; with --soft-drop the walk goes alone and
        the piece then soft-drops a row per turn to its landing. Gravity is
        honored all the while: the rows a deadline owes fall as one batch
        first, and the walk follows in the same turn, so a fast level never
        keeps the piece from walking. On a shared board another player's
        falling piece is a TRANSIENT obstacle: blocked by it we wait at the
        spot the path search picks (out of a teammate's spawn box), never
        lock against it, and never top out on it (gameplays §3). It returns
        when the piece has locked, or when the plan is no longer the thing to
        do (self.replan says why, and play_pieces re-plans from where the
        piece stands), or when the game or the agent stops."""
        async with self.mu:
            if not self.next_gravity:
                # A piece adopted before any spawn of ours set the clock.
                self.next_gravity = spawn_t + self.gravity_now()
        ex = _Exec(plan)
        move_delay = self.a.tn.move_delay
        while True:
            async with self.mu:
                now = time.monotonic()
                verdict = await self._execute_turn(ex, now)
                if verdict == _DONE:
                    self._end_walk_wait(ex, now)
            if verdict != _CONTINUE:
                return
            if not await self.wait(move_delay):
                return

    def _note_walk_wait(self, ex, now):
        # The current wait on a crewmate's piece, for the stats: one episode
        # from the first blocked turn to the next move.
        if ex.waiting_since is None:
            ex.waiting_since = now
            self.st.walk_waits += 1

    def _end_walk_wait(self, ex, now):
        if ex.waiting_since is not None:
            self.st.walk_wait_ms += int((now - ex.waiting_since) * 1000)
            ex.waiting_since = None

    async def _execute_turn(self, ex, now):
        """One turn of execute, mu held: _DONE (the plan is over: locked
        elsewhere, dropped, stale), _LOCKED (the piece locked this turn) or
        _CONTINUE (wait a move step and take another turn)."""
        if self.dead or self.is_ended():
            return _DONE
        if self.plan_shift != 0 and self.piece is not None:
            # A crewmate's clear collapsed rows under the piece and it was
            # re-adopted lower (adopt_own_piece): the plan keeps its column
            # and orientation, and its landing is derived again on the board
            # as it stands (reland_plan). No re-plan needed; the checks
            # below say if that was wrong.
            was = ex.plan.drop_row
            ex.plan = self.reland_plan(self.piece, ex.plan)
            self.trace("collapse: the piece moved %d row(s), the landing %d → %d", self.plan_shift, was,
                       ex.plan.drop_row)
            self.plan_shift = 0
        plan = ex.plan
        if self.pipe_broken:
            # A pipelined batch was lost: drain, repair, re-plan.
            await self.settle_pipeline()
            return _DONE
        if self.piece is None:
            return _DONE  # a shrink squeezed the piece away
        if self.replan != REPLAN_NONE:
            return _DONE  # the board consumer or the blackboard asked for a re-plan
        if ex.waiting_since is not None:
            self.trace("waiting %dms at rot %d col %d row %d", int((now - ex.waiting_since) * 1000),
                       self.piece.orient, self.piece.col, self.piece.row)
        p = self.piece
        shared = self.shared()
        if p != ex.last_pos:
            ex.last_pos, ex.last_progress = p, now
        elif shared and now - ex.last_progress > 3.0:
            # Parked behind an obstacle that never moves: a live teammate
            # piece falls away in well under a second, so this is a stale
            # ghost. Rebuild from the stream and re-plan.
            await self.settle_pipeline()
            await self.resync_shared()
            self.request_replan(REPLAN_STALE)
            return _DONE
        if self.check_plan(plan) == PLAN_STALE:
            # Stale twice running, a turn apart: a collapse batch folded
            # halfway (its active cells before its settled ones) reads as a
            # landing with nothing under it for the space of a message.
            if ex.stale_since is not None:
                self.trace("plan stale: landing %s (placeable %s)", plan.dest, self.can_place(plan.dest))
                self.request_replan(REPLAN_DROP_ROW)
                return _DONE
            ex.stale_since = now
            return _CONTINUE
        ex.stale_since = None
        locked = False
        soft_drop = self.a.soft_drop
        at_target = p.orient == plan.orient and p.col == plan.col
        if self.gravity_debt > 0 or now >= self.next_gravity:
            # The rows gravity owes: the ones a lost batch did not deliver,
            # and one per interval of the level the schedule has passed
            # (several when the loop was busy or the level is faster than the
            # loop). They fall as ONE batch, as far as the board allows; the
            # schedule keeps its phase, the next deadline measured from the
            # last.
            owed = self.gravity_debt
            self.gravity_debt = 0
            if now >= self.next_gravity:
                step = self.gravity_now()
                ticks = min(1 + int((now - self.next_gravity) / step), self.h)
                owed += ticks
                self.next_gravity += ticks * step
            to, fell, transient = self.fall_rows(p, owed)
            self.trace("gravity owes %d: fell %d, transient %s", owed, fell, transient)
            if fell > 0:
                if not await self.publish_piece_move(to):
                    # Lost on the wire (the board re-adopted from the
                    # stream): the rows are owed again, at once.
                    self.gravity_debt += fell
                self.resting_since = None
                self._end_walk_wait(ex, now)
            elif transient:
                # Blocked only by another falling piece: wait, don't lock.
                # The lock delay never starts on one (gameplays §3).
                self._note_walk_wait(ex, now)
            else:
                # Resting on the stack or the floor. Where it meant to be,
                # the piece locks now; elsewhere, on a shared board, it gets
                # the lock delay's grace first: the walk below may still move
                # it once the crewmate in its way falls away.
                if self.resting_since is None:
                    self.resting_since = now
                if at_target or not shared or now - self.resting_since >= LOCK_DELAY:
                    if await self.settle_for_lock(p):
                        return _DONE
                    if not await self.lock_piece(p):
                        self.request_replan(REPLAN_LOCK_DROPPED)
                        return _DONE
                    locked = True
        if not locked and self.piece is not None and self.piece == p:
            if not at_target:
                res = self.find_path(p, plan, self.teammate_boxes())
                pending = self.deps_pending()
                self.trace("path to rot %d col %d: reached %s, %d move(s) to rot %d col %d row %d, transient %s, "
                           "deps pending %s", plan.orient, plan.col, res.reached, len(res.moves), res.end.orient,
                           res.end.col, res.end.row, res.blocked_by_transient, pending)
                if res.reached:
                    dr, clear = self.drop_clear_at(res.end)
                    if clear and not pending and dr == plan.drop_row and soft_drop:
                        # No hard drop: walk to the column, the soft drop
                        # follows from there a row a turn.
                        await self.publish_piece_move(res.end)
                        self.soft_dropped += res.downs
                        self.resting_since = None
                        self._end_walk_wait(ex, now)
                    elif clear and not pending and dr == plan.drop_row:
                        # The walk, the hard drop and the lock as ONE batch:
                        # the piece is on the board one round trip less.
                        if await self.settle_for_lock(p):
                            return _DONE
                        self.soft_dropped += res.downs
                        if not await self.lock_piece(Active(p.pt, plan.orient, dr, plan.col)):
                            self.request_replan(REPLAN_LOCK_DROPPED)
                            return _DONE
                        locked = True
                        self._end_walk_wait(ex, now)
                    elif clear and not pending:
                        # The landing is not the plan's any more (an
                        # overhang, a collapse): the plan is stale.
                        self.request_replan(REPLAN_DROP_ROW)
                        return _DONE
                    elif pending:
                        # A claim we wait on has to land first, through the
                        # columns our landing needs, most likely: wait OUT of
                        # them (dep_aside), and give it a while.
                        aside, moved = self.dep_aside(p)
                        if moved:
                            await self.publish_piece_move(aside)
                            self.resting_since = None
                        if self.await_deps(now):
                            self.request_replan(REPLAN_DEP_TIMEOUT)
                            return _DONE
                        self._note_walk_wait(ex, now)
                    else:
                        # The drop would rest on a falling piece: walk to the
                        # column and wait there for it to fall away.
                        if res.end != p:
                            await self.publish_piece_move(res.end)
                            self.soft_dropped += res.downs
                            self.resting_since = None
                        self._note_walk_wait(ex, now)
                elif res.end != p:
                    # A partial walk, to the waiting spot the search picked.
                    await self.publish_piece_move(res.end)
                    self.soft_dropped += res.downs
                    self.resting_since = None
                    if res.blocked_by_transient:
                        self._note_walk_wait(ex, now)
                elif res.blocked_by_transient:
                    # A crossing piece is in the way, and we are as close as
                    # we get: wait it out.
                    self._note_walk_wait(ex, now)
                else:
                    # Walled off by the stack with nowhere better to stand:
                    # the plan is not the thing to do any more.
                    self.request_replan(REPLAN_UNREACHABLE)
                    return _DONE
            else:
                # At the planned column: settle before deciding the landing,
                # so the drop row comes from converged state.
                if await self.settle_for_lock(p):
                    return _DONE
                dr, clear = self.drop_clear_at(p)
                pending = self.deps_pending()
                self.trace("at target: drop row %d (plan %d), clear %s, deps pending %s", dr, plan.drop_row, clear,
                           pending)
                if not clear:
                    # The drop rests on another FALLING piece: move there (a
                    # row a turn with --soft-drop) but stay active: gravity
                    # resumes once it falls.
                    if dr == p.row:
                        self._note_walk_wait(ex, now)
                    elif soft_drop:
                        if await self.publish_piece_move(Active(p.pt, p.orient, p.row + 1, p.col)):
                            self.soft_dropped += 1
                        self.resting_since = None
                    else:
                        await self.publish_piece_move(Active(p.pt, p.orient, dr, p.col))
                        self.resting_since = None
                        self._note_walk_wait(ex, now)
                elif dr < plan.drop_row:
                    # The board stops the piece short of its landing
                    # (something locked above it since the plan): no claim of
                    # a teammate's can change that, so no waiting.
                    self.request_replan(REPLAN_DROP_ROW)
                    return _DONE
                elif dr > plan.drop_row and pending:
                    # The landing has nothing under it yet: an earlier claim
                    # will put that there, falling through these very
                    # columns, most likely, so the piece waits OUT of them
                    # (dep_aside), and the walk back is one batch once the
                    # claim has landed. Within reason: gravity past the
                    # landing, or too long, is a wait that failed.
                    if p.row > plan.drop_row:
                        self.request_replan(REPLAN_DEP_TIMEOUT)
                        return _DONE
                    aside, moved = self.dep_aside(p)
                    if moved:
                        await self.publish_piece_move(aside)
                        self.resting_since = None
                    if self.await_deps(now):
                        self.request_replan(REPLAN_DEP_TIMEOUT)
                        return _DONE
                    self._note_walk_wait(ex, now)
                elif dr != plan.drop_row:
                    self.request_replan(REPLAN_DROP_ROW)
                    return _DONE
                elif soft_drop and dr > p.row:
                    # No hard drop: one row down a turn, a point a row.
                    if await self.publish_piece_move(Active(p.pt, p.orient, p.row + 1, p.col)):
                        self.soft_dropped += 1
                    self.resting_since = None
                    self._end_walk_wait(ex, now)
                else:
                    if not await self.lock_piece(Active(p.pt, p.orient, dr, p.col)):
                        self.request_replan(REPLAN_LOCK_DROPPED)
                        return _DONE
                    locked = True
                    self._end_walk_wait(ex, now)
        return _LOCKED if locked else _CONTINUE

    def drop_clear_at(self, a):
        """The hard-drop row of a piece standing at a, and whether it would
        LOCK there: not when what stops it is another player's falling piece.
        Then the piece moves there and stays active, and gravity resumes once
        the obstacle falls away. Caller holds mu."""
        dr = self.drop_row_shared(a)
        below = piece_cells(a.pt, a.orient, dr + 1, a.col)
        return dr, not (self.can_place(below) and self.shared_blocked(below))

    def fall_rows(self, p, n):
        """The piece p fallen up to n rows on the local board: as far as
        can_move allows, and (when it stopped short) whether the blocker is
        another player's falling piece (transient: worth waiting for) rather
        than the stack or the floor. Caller holds mu."""
        to, fell = p, 0
        while fell < n:
            down = Active(to.pt, to.orient, to.row + 1, to.col)
            cs = down.cells()
            if not self.can_move(cs):
                return to, fell, self.can_place(cs)
            to = down
            fell += 1
        return to, fell, False

    async def win_check(self):
        async with self.mu:
            if self.goal_decided:
                return self.goal_won
            if self.mode == MODE_COOPERATIVE:
                # The crew's shared run has no winner: the game ends when
                # anyone tops out. A board scored per seat crowns its top
                # scorer(s) once the game has ended: the topper too, if their
                # score is the best.
                return self.individual and self.is_ended() and self.individual_winner_locked()
            if self.mode == MODE_TEAMS:
                return self.others_dead() and not self.dead
            others = 0
            for p in self.roster:
                if p["player_id"] == self.a.name:
                    continue
                others += 1
                if not self.eliminated.get(p["player_id"]):
                    return False
            # Every other board out, or, in an open game, gone: a roster down
            # to us alone after others played is the last board standing
            # (guide §5).
            return (others > 0 or self.had_rivals) and not self.dead

    async def pause_while_alone(self):
        """The creator's agents_pause_alone rule (guide §5 step 4): in an
        open game whose listing sets it, an agent left as the only player on
        the roster stops playing (between pieces, so no piece of ours is left
        hanging on the board; its seat kept and the board followed all the
        while) and plays on the moment anyone, agent or human, takes a seat
        (the lobby watch pushes the roster, on_roster). Without the rule, or
        with company, it returns at once. False when the game or the agent
        stops while waiting."""
        if not self.pause_alone:
            return True
        paused = False
        while True:
            async with self.mu:
                alone = self.alone_locked()
            if not alone:
                if paused:
                    log.info("company arrived, playing on")
                return True
            if not paused:
                paused = True
                log.info("alone in the game, pausing until someone joins")
            if not await self.wait(0.25):
                return False

    def alone_locked(self):
        """Whether nobody but us holds a seat on the roster. mu held."""
        return all(p["player_id"] == self.a.name for p in self.roster)

    def on_roster(self, players):
        """Take the listing's roster as it changes (an open game's seats come
        and go) for the spawn point (the next piece comes in where the
        company of the moment puts it, the piece in play stays), the deal
        (redeal) and the verdict (a team, or a board, nobody holds any more
        is out). Called from the lobby watch: the fold itself waits its turn
        on mu."""
        aio.spawn(self.tasks, self._on_roster(players))

    async def _on_roster(self, players):
        async with self.mu:
            self.roster = players
            self.spawn_c = self.spawn_column(self.present_slots(players))
            if any(p["player_id"] != self.a.name for p in players):
                self.had_rivals = True
            if self.redeal_locked(self.present_slots(players)):
                log.info("re-dealt among the seats present: we hold %s", ration_names(self.ration))

    def score_of_locked(self, pid):
        """A player's score as this agent knows it: our own, a topped-out
        player's game_over figure, or the last line_clear total folded from
        them. mu held."""
        if pid == self.a.name:
            return self.score
        ev = self.results.get(pid)
        if ev is not None:
            return ev["total_score"]
        return self.sender_totals.get(pid, (0, 0))[0]

    def top_scorers_locked(self):
        """The roster's top scorer(s) (everyone tied at the best score) on a
        board scored per seat. mu held."""
        winners = set()
        best = None
        for p in self.roster:
            sc = self.score_of_locked(p["player_id"])
            if best is None or sc > best:
                best, winners = sc, {p["player_id"]}
            elif sc == best:
                winners.add(p["player_id"])
        return winners

    def individual_winner_locked(self):
        """Whether we are among the top scorers of a board scored per seat."""
        return self.a.name in self.top_scorers_locked()

    async def top_out(self):
        if self.mode == MODE_COOPERATIVE:
            # Any top-out ends the game for everyone; the topper finishes and
            # archives it (gameplays §3).
            async with self.mu:
                self.dead = True
                self.eliminated[self.a.name] = True
            await self.publish_game_over()
            if self.individual:
                log.info("topped out, game over (own score %d)", self.score)
            else:
                log.info("topped out, cooperative game over (shared score %d)", self.shared_score)
            await self.transition_finished_and_archive()
            async with self.mu:
                return self.individual and self.individual_winner_locked()
        if self.mode == MODE_TEAMS:
            # A player out is not a team out: vacate our dead piece (gated,
            # so a racing garbage cascade can't resurrect it), announce, and
            # stay for the verdict: our team plays on (gameplays §5).
            async with self.mu:
                self.dead = True
                await self.vacate_own_piece()
                self.eliminated[self.a.name] = True
            await self.publish_game_over()
            log.info("topped out, team %s plays on", team_letter(self.team))
            return await self.wait_for_verdict()
        async with self.mu:
            self.dead = True
            elim = sum(1 for v in self.eliminated.values() if v)
            if not self.eliminated.get(self.a.name):
                elim += 1
            pc = self.player_count
        await self.publish_game_over()
        log.info("topped out (score %d)", self.score)
        if elim >= pc:  # simultaneous draw: we finish it
            await self.transition_finished_and_archive()
        return False

    async def on_game_over(self, ev):
        """Fold one player's game_over (ours echoed, or a peer's): its
        totals, the elimination, and what ends here. Coop: anyone's top-out
        ends the game for everyone, and a PEER's is ours to finish too
        (finish_on_end): the topper finishes it as well, but its client may
        be gone, and the CAS makes the finish idempotent. Teams: the verdict
        is EVENT-driven, not piece-cadence-driven: the enemy team's last
        elimination must end our play loop even if our current piece never
        reaches a lock-in."""
        await self.fold_line_clear(ev)  # its totals: the ending player's last points, like a line_clear's
        async with self.mu:
            pid = ev["player_id"]
            self.eliminated[pid] = True
            peer = pid != self.a.name
            if peer:
                self.results[pid] = ev
            if self.mode == MODE_COOPERATIVE and peer:
                self.peer_ended = True
            enemy_dead = self.mode == MODE_TEAMS and self.others_dead()
        if self.mode == MODE_COOPERATIVE or enemy_dead:
            self.mark_ended()

    async def finish_on_end(self):
        """Whether the game ended on a peer's top-out of the crew's board,
        which every seated player finishes (guide §3)."""
        async with self.mu:
            return self.peer_ended

    async def publish_game_over(self):
        # Our cumulative totals ride along, like a line_clear's: the points
        # of our last locks may never have been announced (a drop's are
        # carried by the next event), and every peer's shared score converges
        # on them.
        ev = {"kind": "game_over", "player_id": self.a.name, "player_idx": self.idx, "score": self.score,
              "level": level_of(self.lines), "piece_count": self.piece_idx, "team": self.team,
              "total_score": self.score, "total_lines": self.lines, "lines_cleared": 0, "t_spin": 0,
              "back_to_back": False, "combo": 0, "perfect": False}
        payload = dumps(ev)
        # The announcement is what every peer's game over (coop) or the
        # elimination count (competitive, teams) runs on: retried past a
        # connection blip rather than dropped.
        for attempt in range(5):
            try:
                await jsx.js_publish(self.a.nc, f"jetris.game.{self.id}.events.game_over.{self.a.name}", payload)
                return
            except Exception as e:
                log.info("game_over: attempt %d failed, retrying: %s", attempt + 1, e)
                await asyncio.sleep((attempt + 1) * 0.25)

    async def transition_finished_and_archive(self):
        if await self.a.transition_meta(self.id, "finished"):
            await self.archive()
        self.mark_ended()

    async def leave_running(self):
        """The way out of a game still in progress when the agent is stopping
        (guide §5 step 7): FIRST vacate our own falling piece (it is ours,
        nothing else moves it, and left behind it would block a crewmate's
        spawn box or walk until a sweep takes it ten seconds later), then
        drop our claim, then free our seat: unjoin_game CAS-removes it from
        an OPEN game's listing while the game runs (an invite game's seat is
        kept for us to rejoin), and the presence goes with the agent's exit."""
        try:
            await asyncio.wait_for(self._leave_running(), 5.0)
        except Exception as e:
            log.info("leave: %r", e)
        log.info("left game %s", self.id)

    async def _leave_running(self):
        async with self.mu:
            await self.settle_pipeline()
            if self.piece is not None:
                if self.mode == MODE_COOPERATIVE and not self.survival:
                    # The crew's board: one atomic batch emptying the piece's
                    # cells at their last-seen sequences, retried from a
                    # fresh snapshot when a crewmate's transform moved them
                    # meanwhile.
                    for attempt in range(3):
                        if self.piece is None:
                            break
                        if attempt > 0:
                            await self.resync()
                            if self.piece is None:
                                break
                        try:
                            await self.publish_batch([CellUpd(c) for c in self.own_active_cells()], True)
                        except CASError:
                            continue
                        except Exception as e:
                            log.info("leave: vacate: %s", e)
                            break
                        self.piece = None
                        break
                else:
                    # A team's board, or the crew's under a rising floor: the
                    # gated vacate, retried.
                    await self.vacate_own_piece()
        await self.drop_claim()
        await self.a.unjoin_game(self.id)

    async def wait_for_end(self):
        await aio.wait_any([self.ended, self.a.stop_ev], 120.0)

    async def archive(self):
        """We triggered the finish, so transition finished→archived (the CAS
        elects one archiver), publish the record, archive the replay, then,
        once the grace has passed, delete the game's resources (guide
        §5.5–5.6)."""
        finished = time.monotonic()
        try:
            meta, seq = await self.a.fetch_meta(self.id)
        except Exception:
            return
        if o_str(meta, "status") != "finished":
            return
        meta["status"] = "archived"
        try:
            _, conflict = await self.a.meta_publish(self.id, dumps(meta), seq)
        except Exception:
            return
        if conflict:
            return  # someone else won the archive CAS
        started_at = o_str(meta, "started_at") or o_str(meta, "created_at")
        finished_at = o_str(meta, "finished_at") or now_rfc()
        record = {
            # The archive record format: levels 1-based, every player's score
            # their own locks' total, every player's stats tally.
            "version": 4,
            "game_id": self.id, "mode": self.mode, "player_count": o_int(meta, "player_count"),
            "players": await self.player_results(parse_rfc(o_str(meta, "started_at")), parse_rfc(finished_at)),
            "started_at": started_at,
            "finished_at": finished_at,
            "winning_team": -1,
            "boards": await self.board_pictures(),
        }
        # The shared board's geometry travels with the record: without it a
        # replay would rebuild the board on the wrong one.
        if o_int(meta, "extra_columns") > 0 and self.mode != MODE_COMPETITIVE:
            record["extra_columns"] = o_int(meta, "extra_columns")
        if o_int(meta, "extra_rows") > 0 and self.mode != MODE_COMPETITIVE:
            record["extra_rows"] = o_int(meta, "extra_rows")
        if o_int(meta, "line_goal") > 0:
            record["line_goal"] = o_int(meta, "line_goal")
        tier = normalize_survival(o_str(meta, "survival"))
        if tier and self.mode == MODE_COOPERATIVE:
            record["survival"] = tier  # the record's rank is the time survived (gameplays §2 Survival)
        async with self.mu:
            if self.mode == MODE_COOPERATIVE:
                if self.individual:
                    record["scoring"] = "individual"
                else:
                    record["total_score"] = self.shared_score
                    record["final_level"] = level_of(self.total_lines)
            elif self.mode == MODE_TEAMS:
                record["team_count"] = self.teams()
                record["team_size"] = self.team_size()
                record["winning_team"] = self.winning_team()
                record["team_scores"] = list(self.team_scores)
                record["team_levels"] = [level_of(n) for n in self.team_lines]
        payload = dumps(record)
        # The record goes out FIRST: it is what every lobby's history shows.
        try:
            await jsx.js_publish(self.a.nc, ARCHIVE_SUBJECT, payload)
        except Exception as e:
            log.info("archive publish: %s", e)
            # An unlisted game could never be ranked or displaced: no replay,
            # and its stream is left for a cleanup pass.
            return
        # Replay archive (guide §5 step 6): copy the game stream to the
        # replay stream (purging the replays this game displaces), after the
        # record, before the game stream is deleted below.
        try:
            await self.a.maybe_archive_replay(payload)
        except Exception as e:
            log.info("replay archive: %r", e)
        # Let every peer see the final events before the stream goes away.
        wait = ARCHIVE_GRACE - (time.monotonic() - finished)
        if wait > 0:
            await asyncio.sleep(wait)
        for step in (self.a.js.delete_stream(self.stream), self.a.kv.delete("games." + self.id),
                     self.a.js.purge_stream(CHAT_STREAM, subject="jetris.chat." + self.id)):
            try:
                await step
            except Exception:
                pass
        log.info("archived game %s", self.id)

    async def player_results(self, started, finished):
        """The record's `players`: every seat's totals, verdict and tally
        (`stats`: the clears by name, the attack sent, and the time played,
        from started, the moment the game went in progress, to the seat's
        game_over as the stream stamped it, or to finished for a seat that
        never announced one; no time at all without a start). Caller does not
        hold mu."""
        async with self.mu:
            winning_team = self.winning_team() if self.mode == MODE_TEAMS else -1
            winners = self.top_scorers_locked() if self.individual else set()
            out = []
            for p in self.roster:
                pid = p["player_id"]
                score = level = lines = pieces = 0
                if pid == self.a.name:
                    score, level, lines, pieces = self.score, level_of(self.lines), self.lines, self.piece_idx
                elif pid in self.results:
                    # Their game_over's total_score: the sender's own locks'
                    # total (its score is the board's: shared on a crew's or
                    # a team's).
                    ev = self.results[pid]
                    score, level, lines, pieces = ev["total_score"], ev["level"], ev["total_lines"], ev["piece_count"]
                elif pid in self.sender_totals:
                    # Never topped out (a coop survivor, an alive teams
                    # winner): their cumulative line-clear totals are the
                    # best record we have, and their last line_clear's piece
                    # count.
                    tot = self.sender_totals[pid]
                    score, level, lines, pieces = tot[0], level_of(tot[1]), tot[1], self.sender_pieces.get(pid, 0)
                r = {"player_id": pid, "score": score, "piece_count": pieces}
                if level:
                    r["level"] = level
                if lines:
                    r["lines"] = lines
                stats = {}
                have = self.sender_stats.get(pid)
                if have is not None:
                    stats = have.to_json()
                stats.pop("played_ms", None)
                if started is not None and finished is not None:
                    end = self.out_at.get(pid) or finished
                    played = int(max((end - started).total_seconds(), 0.0) * 1000)
                    if played:
                        stats["played_ms"] = played
                r["stats"] = stats
                if self.mode == MODE_COOPERATIVE:
                    # The crew's shared run has no winners; a board scored
                    # per seat crowns its top scorer(s).
                    if self.individual and pid in winners:
                        r["winner"] = True
                elif self.mode == MODE_TEAMS:
                    r["team"] = p["team"]
                    if p["team"] == winning_team:
                        r["winner"] = True
                elif not self.eliminated.get(pid):
                    r["winner"] = True
                if p["agent"]:
                    r["agent"] = True
                out.append(r)
            return out

    async def board_pictures(self):
        """The finished boards' visible regions, fetched from the stream
        before it is deleted (row 0 = first visible row, headroom stripped):
        one picture per player in competitive, ONE shared board in coop, one
        per team in teams."""
        h = self.height()

        async def snap(label, idx, prefix):
            subjects = [f"{prefix}.{r}.{c}" for r in range(HEADROOM, h) for c in range(self.w)]
            cells = []
            for start in range(0, len(subjects), FETCH_CHUNK):
                try:
                    msgs = await jsx.get_last_msgs_for(self.a.nc, self.a.js, self.stream,
                                                       subjects[start:start + FETCH_CHUNK])
                except Exception:
                    continue
                for m in msgs:
                    at = parse_cell_subject(m.subject)
                    d = to_obj(m.data)
                    if at is None or not d:
                        continue
                    cells.append({"r": at[0] - HEADROOM, "c": at[1], "d": d})
            cells.sort(key=lambda x: (x["r"], x["c"]))
            return {"label": label, "idx": idx, "w": self.w, "h": h - HEADROOM, "cells": cells or None}

        base = f"jetris.game.{self.id}"
        if self.mode == MODE_COOPERATIVE:
            return [await snap("", -1, f"{base}.playfield.cell")]
        if self.mode == MODE_TEAMS:
            return [await snap("Team " + team_letter(t), t, f"{base}.team.{t}.playfield.cell")
                    for t in range(self.teams())]
        async with self.mu:
            ids = sorted(p["player_id"] for p in self.roster)
        return [await snap(pid, i, f"{base}.player.{pid}.playfield.cell") for i, pid in enumerate(ids)]
