"""Protocol constants (jetris-agent-guide.md §4, gameplays §2/§7)."""

CODENAME = "python-mk2"

LOBBY_BUCKET = "JETRIS_LOBBY"
BLACKBOARD_BUCKET = "JETRIS_BLACKBOARD"
ARCHIVE_STREAM = "JETRIS_ARCHIVE"
ARCHIVE_SUBJECT = "jetris.archive"
CHAT_STREAM = "JETRIS_CHAT"

MODE_COOPERATIVE = 0
MODE_COMPETITIVE = 1
MODE_TEAMS = 2

HEADROOM = 4               # hidden spawn rows 0..3
PRESENCE_EVERY = 5.0       # seconds between presence heartbeats
PRESENCE_TTL = 300.0       # per-message TTL on presence writes: a crashed peer's entry self-deletes
INVITE_TTL = 120.0         # an invitation is fresh for two minutes

# The publish disciplines (--publish). PUB_AUTO is the default: the
# discipline is picked per game, sync on a shared board, async on the agent's
# own competitive board (pipeline.resolve_publish).
PUB_SYNC, PUB_ASYNC, PUB_OPTIMISTIC, PUB_AUTO = range(4)


def game_stream_name(game_id):
    """The per-game stream all its subjects live under."""
    return "JETRIS_GAME_" + game_id


def meta_subject(game_id):
    return "jetris.game." + game_id + ".meta"
