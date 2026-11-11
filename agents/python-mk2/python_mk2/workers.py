"""Worker processes for the planner's lookahead.

A plan at a deep preview is six independent searches, one per placement in
the beam (planner.plan_roots): pure functions of a few integers. Run side by
side in worker processes they take a sixth of the time, which is what brings
this planner's plan under a think pause; without workers (one core, a pool
that would not start, --plan-workers 0) the same searches run one after the
other in a thread of the agent's own process.
"""

import os
import signal
from concurrent.futures import ProcessPoolExecutor

from .planner import PLAN_BEAM
from .wire import log


def _worker_init():
    # A Ctrl-C at the terminal reaches every process of the group: the agent
    # handles it and shuts the workers down itself.
    signal.signal(signal.SIGINT, signal.SIG_IGN)


def _ready():
    from . import planner  # noqa: F401  (the import is the warm-up)

    return True


class PlanPool:
    """The agent's lookahead workers, started on first use. workers < 0 picks
    a number: one per placement in the beam, as far as the machine's cores
    allow; 0 is no pool."""

    def __init__(self, workers=-1):
        if workers < 0:
            workers = min(PLAN_BEAM, (os.cpu_count() or 1) - 1)
        self.workers = workers if workers >= 2 else 0
        self._pool = None

    def get(self):
        """The executor to run lookahead jobs on, None for no pool."""
        if self.workers == 0:
            return None
        if self._pool is None:
            try:
                self._pool = ProcessPoolExecutor(max_workers=self.workers, initializer=_worker_init)
            except Exception as e:
                self.failed(e)
        return self._pool

    def warm(self):
        """Start the workers ahead of the first plan that needs them."""
        pool = self.get()
        if pool is None:
            return
        try:
            for _ in range(self.workers):
                pool.submit(_ready)
        except Exception as e:
            self.failed(e)

    def failed(self, err):
        """The pool would not start, or broke: plan in-process from here on."""
        if self.workers:
            log.info("planner workers: %r, planning in-process", err)
        self.workers = 0
        self.close()

    def close(self):
        pool, self._pool = self._pool, None
        if pool is not None:
            try:
                pool.shutdown(wait=False, cancel_futures=True)
            except Exception:
                pass
