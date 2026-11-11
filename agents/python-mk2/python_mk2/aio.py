"""Small asyncio helpers."""

import asyncio


async def wait_any(events, timeout=None):
    """Wait until any of the events is set, or the timeout runs out. Returns
    the index of the first event found set, -1 on a timeout. Always yields to
    the loop at least once."""
    for i, e in enumerate(events):
        if e.is_set():
            await asyncio.sleep(0)
            return i
    if timeout is not None and timeout <= 0:
        await asyncio.sleep(0)
    else:
        tasks = [asyncio.ensure_future(e.wait()) for e in events]
        try:
            await asyncio.wait(tasks, timeout=timeout, return_when=asyncio.FIRST_COMPLETED)
        finally:
            for t in tasks:
                t.cancel()
    for i, e in enumerate(events):
        if e.is_set():
            return i
    return -1


def spawn(tasks, coro):
    """Start a background task and keep it in `tasks` until it is done."""
    t = asyncio.ensure_future(coro)
    tasks.add(t)
    t.add_done_callback(tasks.discard)
    return t
