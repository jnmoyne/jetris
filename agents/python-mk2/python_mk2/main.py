"""python-mk2: a self-contained Jetris agent in Python.

It plays Jetris (every mode, its teammates coordinated) by speaking the wire
protocol in ../../jetris-agent-guide.md directly against NATS/JetStream,
depending on nothing in the jetris repo: the golang-mk2 agent, ported.

Usage:

    python-mk2 --server nats://localhost:4222                 # resident: waits for invitations
    python-mk2 --context my-context                           # connect via a NATS context
    python-mk2                                                # ...or the currently selected one
    python-mk2 --auto-join                                    # also join open agent-allowed games
    python-mk2 --join <gameID>                                # join one specific game
    python-mk2 --create --players 2 --once                    # host a game, play it, exit
    python-mk2 --create --mode teams --players 2              # host a 2v2 teams game
    python-mk2 --create --mode teams --teams 3 --players 2    # ...or a three-way, 2 per team
    python-mk2 --create --mode cooperative --pause-alone      # host a co-op game and wait, paused, for company
    python-mk2 --create --mode cooperative --survival normal  # host a survival game: the floor rises until the crew tops out
    python-mk2 --create --game-name friday-night              # host a NAMED game: its ID, its lobby row and its stream
    python-mk2 --create --mode cooperative --players 3 --seed 42       # host a co-op game dealt from a fixed seed (benchmarks)
    python-mk2 --create --host-only --mode cooperative --players 2 --once  # host a game for others, seatless, and leave when it is over
    python-mk2 --coordinate=false                             # play shared boards uncoordinated
    python-mk2 --difficulty hard --once                       # play a single game, then exit
    python-mk2 --selftest                                     # offline conformance checks
"""

import argparse
import asyncio
import logging
import re
import signal
import sys

from .agent import Agent, ConnChoice, Hosting, game_name
from .consts import CODENAME, MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from .difficulty import MAX_NEXT_COUNT, valid_difficulty
from .pieces import SPAWN_COL, SPAWN_ROW, WIDTH
from .pipeline import parse_publish_mode
from .rng import BAG_DOUBLE, BAG_NONE, BAG_SINGLE, normalize_bag, piece_at, piece_at_bag, piece_sets
from .rules import DEFAULT_TEAM_COUNT, MIN_EXTRA_COLUMNS, normalize_survival
from .wire import log
from .workers import PlanPool


def _bool_flag(s):
    """A boolean flag's explicit value (--flag=false), as lenient as the
    other agents' flag parsers."""
    if s in ("1", "t", "T", "true", "TRUE", "True"):
        return True
    if s in ("0", "f", "F", "false", "FALSE", "False"):
        return False
    raise argparse.ArgumentTypeError(f"invalid boolean value {s!r}")


_DURATION_UNITS = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "μs": 1e-6, "ms": 1e-3, "s": 1.0, "m": 60.0, "h": 3600.0}
_DURATION_PART = re.compile(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)")


def parse_duration(s):
    """A duration such as 10m, 90s, 1h30m or 500ms, in seconds."""
    text = s.strip()
    sign = 1.0
    if text[:1] in ("+", "-"):
        sign = -1.0 if text[0] == "-" else 1.0
        text = text[1:]
    if text == "0":
        return 0.0
    total, pos = 0.0, 0
    while pos < len(text):
        m = _DURATION_PART.match(text, pos)
        if not m:
            raise argparse.ArgumentTypeError(f"invalid duration {s!r} (want e.g. 10m, 90s, 1h30m)")
        total += float(m.group(1)) * _DURATION_UNITS[m.group(2)]
        pos = m.end()
    if pos == 0:
        raise argparse.ArgumentTypeError(f"invalid duration {s!r} (want e.g. 10m, 90s, 1h30m)")
    return sign * total


def _uint64(s):
    try:
        v = int(s, 0)
    except ValueError:
        raise argparse.ArgumentTypeError(f"invalid seed {s!r}") from None
    if not 0 <= v < 1 << 64:
        raise argparse.ArgumentTypeError(f"seed {s!r} is out of range (0 to 2^64-1)")
    return v


def build_parser():
    p = argparse.ArgumentParser(prog=CODENAME, allow_abbrev=False,
                                description="A Jetris agent: plays every mode over NATS, its teammates coordinated.")

    def flag(name, **kw):
        # Both spellings, --flag and -flag, as the Go agents take them.
        p.add_argument("--" + name, "-" + name, dest=name.replace("-", "_"), **kw)

    def boolean(name, default, help):
        flag(name, nargs="?", const=True, default=default, type=_bool_flag, metavar="BOOL", help=help)

    flag("server", default="", help="NATS server URL (overrides --context)")
    flag("context", default="", help="NATS context to connect with (default: the selected context)")
    flag("user", default="", help="NATS username (used with --server)")
    flag("password", default="", help="NATS password (used with --server)")
    flag("name", default=CODENAME, help="agent VERSION stem of the player name (default: the codename)")
    flag("difficulty", default="hard", help="play strength: easy, medium, or hard")
    flag("join", default="", help="join this specific game id instead of scanning the lobby")
    boolean("create", False, "create a game and wait for opponents")
    flag("game-name", default="", help="name the game when creating one: the name becomes the game's ID, so its stream is JETRIS_GAME_<name> and lobbies list it by name (empty = a generated ID)")
    flag("mode", default="competitive", help="game mode when creating: cooperative, competitive or teams (with --create)")
    flag("players", type=int, default=2, help="player count when creating a game (with --create; cooperative: 1 or more, a solo game plays for the high score; teams: players per team)")
    flag("teams", type=int, default=DEFAULT_TEAM_COUNT, help="teams mode: how many teams play each other when creating a game (2-6; total seats = teams x --players)")
    flag("max-agents", type=int, default=0, help="agent seats when creating a game, including this agent: on each team in teams mode (0 = all seats)")
    boolean("pause-alone", False, "when creating a game: an agent left as the only player in it stops playing until someone joins, instead of playing on by itself")
    flag("extra-cols", type=int, default=MIN_EXTRA_COLUMNS, help="shared-board width when creating a cooperative or teams game: columns every seat beyond the first adds to the standard 10 (4-10)")
    flag("next", type=int, default=MAX_NEXT_COUNT, help="upcoming pieces the game reveals when creating a game (0-6, 0 = none)")
    flag("holes", type=int, default=0, help="holes per garbage row when creating a competitive or teams game (0-4, 0 = solid rows that never clear)")
    boolean("random-holes", False, "every garbage row draws its own hole columns when creating a game (default: the rows of one attack share a draw)")
    boolean("guideline-garbage", False, "the modern attack table when creating a game: a single sends no garbage, a double 1 row, a triple 2, a quad 4 (default: one row per line)")
    boolean("hold", False, "the hold queue when creating a game (the agent itself never holds; the humans in the game may)")
    boolean("split-pieces", False, "when creating a game whose playfields have two or more seats: deal the seven piece types out between the seats of a playfield, each playing only its own ration")
    flag("extra-rows", type=int, default=0, help="shared-board height when creating a cooperative or teams game: rows every seat beyond the first adds below the standard 20 (0-10)")
    flag("line-goal", type=int, default=0, help="the game's length in lines when creating a game: the first playfield to clear this many wins (0 = until top out)")
    boolean("individual", False, "when creating a cooperative game of two or more: score every seat on its own, the top score wins")
    flag("survival", default="", help="when creating a cooperative game: the rising floor and its tier (too_easy, super_easy, very_easy, easy, normal or hard): garbage rows rise on a clock that quickens with the level until the crew tops out, the time survived the result (no line goal; at least one hole per row)")
    flag("bag", default="", help="piece randomizer when creating a game: the 7-bag (empty, the default), double (two of each type per bag of fourteen) or none (every piece an independent draw)")
    boolean("guideline", False, "create the game with the Modern preset (next 6, hold, the 7-bag, 1 hole per garbage row, the modern attack table) overriding --next, --holes, --random-holes, --guideline-garbage, --hold and --bag")
    flag("publish", default="auto", help="how move batches are committed (guide §4.3): auto (sync on a shared board, async on a private one), sync (await every commit ack), async (pipelined, no expectation on in-flight cells), or optimistic (pipelined with predicted sequences)")
    boolean("coordinate", True, "shared boards: coordinate with teammate agents through the JETRIS_BLACKBOARD KV bucket: claims, projections, right of way (false: plan every board alone)")
    flag("seed", type=_uint64, default=None, help="with --create: the game's piece seed (unset = the clock), so benchmark runs deal every crew the same pieces")
    boolean("host-only", False, "with --create: create the game and stay in the lobby without taking a seat until it is over (a benchmark's neutral host)")
    boolean("trace", False, "log every piece's lifecycle (spawn, plan, claim, waits, moves, lock) with microsecond stamps (a tuning aid)")
    boolean("soft-drop", False, "never hard-drop: once at its planned column and orientation, the piece is soft-dropped a row at a time (a row per move step) until it rests, then locks: slower, a point a row instead of two")
    flag("plan-workers", type=int, default=-1, metavar="N", help="worker processes for the planner's lookahead at a deep preview (default: one per placement in the beam, as the cores allow; 0 = plan in-process)")
    boolean("auto-join", False, "also join open agent-allowed games (default: invited games only)")
    flag("wait", type=parse_duration, default=600.0, metavar="DURATION", help="max wait for a joined game to fill and start before un-joining it (default 10m)")
    boolean("once", False, "play one game, then exit")
    boolean("selftest", False, "run offline conformance checks and exit")
    return p


def _fail(msg, code=2):
    print(msg, file=sys.stderr)
    sys.exit(code)


def hosting_from_args(args):
    """The --create configuration, validated; exits on a flag that names
    nothing the game knows."""
    mode = {"cooperative": MODE_COOPERATIVE, "coop": MODE_COOPERATIVE,
            "competitive": MODE_COMPETITIVE, "teams": MODE_TEAMS}.get(args.mode)
    if mode is None:
        _fail(f'unknown mode "{args.mode}" (want cooperative, competitive or teams)')
    if normalize_bag(args.bag) != args.bag:
        _fail(f'--bag "{args.bag}" is not a bag kind: use double, none, or leave it unset for the 7-bag')
    if normalize_survival(args.survival) != args.survival:
        _fail(f'--survival "{args.survival}" is not a tier: use too_easy, super_easy, very_easy, easy, normal or hard, or leave it unset')
    if args.survival and mode != MODE_COOPERATIVE:
        _fail("--survival is a single playfield's game: use it with --mode cooperative")
    # A name is the game's ID, so it has to be one: something a stream name,
    # a subject and a KV key all take (game_name), and not "lobby", which the
    # chat stream and the voice rooms keep for the lobby's own channels.
    gn = game_name(args.game_name)
    if args.game_name and not gn:
        _fail(f'--game-name "{args.game_name}" has nothing a game ID can be made of: use letters, digits, - or _')
    if gn.lower() == "lobby":
        _fail("--game-name lobby is reserved: it is what the lobby's own chat and voice channels go by")
    host = Hosting(
        game_name=gn, mode=mode, players=args.players, teams=args.teams, extra_cols=args.extra_cols,
        max_agents=args.max_agents, next=args.next, holes=args.holes, random=args.random_holes,
        guideline=args.guideline_garbage, hold=args.hold, split=args.split_pieces, bag=args.bag,
        extra_rows=args.extra_rows, line_goal=args.line_goal, single=args.individual, survival=args.survival,
        pause_alone=args.pause_alone, seed=args.seed or 0, seed_set=args.seed is not None, host_only=args.host_only)
    if args.guideline:
        # The same rules the game's "Modern" preset picks.
        host.next, host.holes, host.random, host.guideline, host.hold, host.bag = MAX_NEXT_COUNT, 1, False, True, True, BAG_SINGLE
    return host


def setup_logging():
    """Log lines as the Go agents print them: the time, then the message, on
    stderr."""
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(logging.Formatter("%(asctime)s %(message)s", datefmt="%H:%M:%S"))
    log.handlers[:] = [handler]
    log.setLevel(logging.INFO)
    log.propagate = False


async def _run(agent):
    loop = asyncio.get_running_loop()

    def on_signal():
        log.info("shutting down…")
        agent.stop()

    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, on_signal)
        except (NotImplementedError, RuntimeError):
            signal.signal(sig, lambda *_: loop.call_soon_threadsafe(on_signal))
    await agent.run()


def main(argv=None):
    setup_logging()
    args = build_parser().parse_args(argv)

    if args.selftest:
        run_selftest()
        return

    try:
        diff = valid_difficulty(args.difficulty)
        pub = parse_publish_mode(args.publish)
    except ValueError as e:
        _fail(str(e))
    if args.join and args.create:
        _fail("--join and --create are mutually exclusive")
    host = hosting_from_args(args) if args.create else None

    async def amain():
        # The agent holds an asyncio.Event, so it is built inside the loop.
        try:
            agent = Agent(ConnChoice(server=args.server, context=args.context, user=args.user, password=args.password),
                          args.name, diff, args.join, args.once, args.auto_join, host, args.wait, pub)
        except ValueError as e:
            _fail(str(e), 1)
        agent.coordinate = args.coordinate
        agent.trace = args.trace
        agent.soft_drop = args.soft_drop
        agent.plan_pool = PlanPool(args.plan_workers)
        await _run(agent)

    try:
        asyncio.run(amain())
    except KeyboardInterrupt:
        sys.exit(130)
    except SystemExit:
        raise
    except Exception as e:
        log.info("fatal: %s", e or type(e).__name__)
        sys.exit(1)


def run_selftest():
    """Exercise the pieces of the agent that need no server: RNG parity with
    the game's sequence, and a sanity check that the planner clears a line it
    is handed."""
    def die(msg):
        log.info("%s", msg)
        sys.exit(1)

    fixtures = {
        42: [2, 3, 4, 1, 0, 5, 6, 6, 0, 5, 4, 2, 3, 1, 2, 1, 4, 3, 6, 5, 0],
        12345: [6, 3, 2, 1, 0, 4, 5, 0, 1, 5, 3, 2, 6, 4, 3, 6, 4, 2, 5, 1, 0],
    }
    for seed, seq in fixtures.items():
        for i, want in enumerate(seq):
            got = piece_at(seed, i)
            if got != want:
                die(f"RNG mismatch seed {seed} index {i}: {got} != {want}")
    # The teams-mode piece split (meta split_pieces): the fixtures every
    # engine must deal, plus the rule the deal must always keep: all seven
    # types dealt out, nobody empty-handed.
    split_fixtures = {
        42: {2: [[0, 1, 4], [2, 3, 5, 6]], 3: [[0, 4, 5], [2, 3], [1, 6]]},
        12345: {2: [[0, 1, 2, 5], [3, 4, 6]], 3: [[1, 2], [0, 5, 6], [3, 4]]},
    }
    for seed, by_seats in split_fixtures.items():
        for seats, want in by_seats.items():
            got = piece_sets(seed, seats)
            for slot in range(len(want)):
                if list(got[slot]) != want[slot]:
                    die(f"split mismatch seed {seed} seats {seats} slot {slot}: {got[slot]} != {want[slot]}")
    # The bag rule (meta bag): the double bag and no bag, the same seeds
    # dealt two more ways.
    bag_fixtures = {
        BAG_DOUBLE: {
            42: [6, 5, 2, 3, 0, 3, 2, 4, 1, 4, 0, 1, 6, 5, 4, 5, 4, 1, 3, 1, 3, 6, 0, 2, 5, 6, 0, 2],
            12345: [3, 5, 4, 0, 3, 0, 6, 1, 6, 1, 5, 2, 2, 4, 4, 4, 1, 1, 0, 2, 0, 3, 5, 6, 6, 5, 3, 2],
        },
        BAG_NONE: {
            42: [6, 1, 0, 4, 1, 2, 2, 5, 4, 6, 6, 5, 0, 0, 5, 0, 3, 0, 0, 0, 0],
            12345: [5, 4, 0, 5, 5, 0, 2, 1, 4, 4, 1, 6, 2, 1, 5, 2, 1, 6, 5, 5, 5],
        },
    }
    for kind, by_seed in bag_fixtures.items():
        for seed, seq in by_seed.items():
            for i, want in enumerate(seq):
                got = piece_at_bag(seed, None, kind, i)
                if got != want:
                    die(f'bag "{kind}" mismatch seed {seed} index {i}: {got} != {want}')
    for seats in range(1, 10):
        seen = [False] * 7
        for slot, pset in enumerate(piece_sets(12345, seats)):
            if not pset:
                die(f"split deal of {seats} seats leaves slot {slot} empty-handed")
            for pt in pset:
                seen[pt] = True
        for pt, ok in enumerate(seen):
            if not ok:
                die(f"split deal of {seats} seats leaves piece {pt} undealt")
    # A board one cell short of a full bottom row: dropping an I flat into
    # the gap must clear it, which the planner should prefer.
    from .engine import Grid
    from .planner import plan_placements

    g = Grid(24, WIDTH)
    for c in range(WIDTH):
        if c < 3 or c > 6:
            g.set(23, c, 1)
    ranked = plan_placements(g, 0, SPAWN_ROW, SPAWN_COL, SPAWN_COL, [], None)
    if not ranked or ranked[0].lines == 0:
        die("planner did not find the line clear")
    print("selftest OK")


if __name__ == "__main__":
    main()
