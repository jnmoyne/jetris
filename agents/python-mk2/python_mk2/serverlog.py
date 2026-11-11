"""The server log (the JETRIS_LOG stream).

The journal the game's clients append to as they connect and disconnect and
as games are created and started, read back into the lobby's SERVER LOG tab.
Agents are not journaled (they come and go by the dozen) with one exception:
a game's start is the game's event, and the agent that ran its countdown (it
readied up last) is the one peer placed to write it, in the format every
other client writes. The stream is created the same way every client creates
it, so whichever reaches a server first leaves it as the other expects.
"""

from nats.js import api

from . import jsx
from .wire import dumps, log, now_rfc

LOG_STREAM = "JETRIS_LOG"
LOG_SUBJECT_PREFIX = "jetris.log."
LOG_MAX_AGE = 100 * 24 * 3600.0  # seconds
LOG_DUPLICATE_WINDOW = 120.0     # seconds

LOG_GAME_STARTED = "game.started"


async def create_or_update_stream(js, cfg):
    """Converge a stream to the config: update it, or create it when it is
    not there yet."""
    try:
        return await js.update_stream(cfg)
    except jsx.NotFoundError:
        return await js.add_stream(cfg)


async def ensure_log_stream(a):
    await create_or_update_stream(a.js, api.StreamConfig(
        name=LOG_STREAM,
        subjects=[LOG_SUBJECT_PREFIX + ">"],
        storage=api.StorageType.FILE,
        num_replicas=1,
        max_age=LOG_MAX_AGE,
        duplicate_window=LOG_DUPLICATE_WINDOW,
    ))


async def journal(a, kind, game_id, mode, players):
    """Append one game entry to the server log: the game, its mode and its
    seat count. Fire-and-forget: a lost entry is a missing line in a tab,
    nothing more."""
    e = {"kind": kind, "player_id": a.name, "name": a.name, "agent": True, "time": now_rfc(),
         "game_id": game_id, "mode": mode, "player_count": players}
    try:
        await jsx.js_publish(a.nc, LOG_SUBJECT_PREFIX + kind, dumps(e))
    except Exception as err:
        log.info("server log %s: %s", kind, err)
