"""One game's account of the agent's traffic on its board: the collisions the
blackboard is there to spare it, and what the coordination itself cost,
printed once as the log's `stats:` line at the end of the game, which is what
scripts/bench-mk2.sh reads to compare crews.
"""

import time
from dataclasses import dataclass, field
from datetime import datetime

from .consts import MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from .wire import log


@dataclass
class CoordStats:
    cas_losses: int = 0       # dropped writes, counted where they flash
    walk_waits: int = 0       # episodes spent waiting on a teammate's falling piece
    walk_wait_ms: int = 0     # their total length
    deferred_spawns: int = 0  # spawns deferred by a foreign piece on our spawn box
    deferred_ms: int = 0
    spawn_unblocks: int = 0   # stale spawn blockers vacated
    idle_vacates: int = 0     # idle or seatless foreign pieces vacated
    lock_retries: int = 0     # CAS locks retried on a shared board
    locks_dropped: int = 0    # CAS locks given up on (the piece re-planned)
    claims_yielded: int = 0   # claims re-planned because an earlier claim overlapped them
    dep_waits: int = 0        # drops held for an earlier claim to land
    dep_wait_ms: int = 0      # their total length
    echo_timeouts: int = 0    # claims played uncoordinated because their echo never came
    replans: dict = field(default_factory=dict)  # re-plans by reason
    started: float = 0.0      # when the game started for us (monotonic)

    def note_replan(self, reason):
        """Count a re-plan by its reason. Caller holds mu."""
        self.replans[reason] = self.replans.get(reason, 0) + 1


class StatsMixin:
    def stats_line(self):
        """The game's account in one greppable line:

            stats: game=<id> mode=<coop|competitive|teams> seat=N coordinate=<bool> pieces=N lines=N score=N
            shared_score=N cas_losses=N walk_waits=N/<ms>ms deferred_spawns=N/<ms>ms spawn_unblocks=N
            idle_vacates=N lock_retries=N locks_dropped=N replans=<reason:count,…|none> claims_yielded=N
            dep_waits=N/<ms>ms echo_timeouts=N duration=<s>s

        Caller holds mu."""
        mode = {MODE_COOPERATIVE: "coop", MODE_COMPETITIVE: "competitive", MODE_TEAMS: "teams"}.get(self.mode, "")
        shared = self.score
        if self.mode == MODE_COOPERATIVE:
            shared = self.shared_score
        elif self.mode == MODE_TEAMS and 0 <= self.team < len(self.team_scores):
            shared = self.team_scores[self.team]
        st = self.st
        replans = ",".join(f"{k}:{st.replans[k]}" for k in sorted(st.replans)) or "none"
        duration = time.monotonic() - st.started if st.started else 0.0
        return (f"stats: game={self.id} mode={mode} seat={self.idx} coordinate={str(self.coordinating()).lower()} "
                f"pieces={self.piece_idx} lines={self.lines} score={self.score} shared_score={shared} "
                f"cas_losses={st.cas_losses} walk_waits={st.walk_waits}/{st.walk_wait_ms}ms "
                f"deferred_spawns={st.deferred_spawns}/{st.deferred_ms}ms spawn_unblocks={st.spawn_unblocks} "
                f"idle_vacates={st.idle_vacates} lock_retries={st.lock_retries} locks_dropped={st.locks_dropped} "
                f"replans={replans} claims_yielded={st.claims_yielded} dep_waits={st.dep_waits}/{st.dep_wait_ms}ms "
                f"echo_timeouts={st.echo_timeouts} duration={duration:.1f}s")

    def trace(self, fmt, *args):
        """Log one step of a piece's lifecycle when --trace is on: the tuning
        aid behind every number in the stats line."""
        a = self.a
        if a is None or not a.trace:
            return
        stamp = datetime.now().strftime("%H:%M:%S.%f")
        log.info("%s seat %d piece %d: " + fmt, stamp, self.idx, self.piece_idx, *args)
