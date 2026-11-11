"""The replay retention policy."""

import json
from datetime import datetime, timedelta, timezone

from python_mk2.consts import MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.replay import REPLAY_RECENT_N, REPLAY_TOP_N, ReplayRecord, replay_keep_set


def test_replay_keep_set_pins():
    """A pinned game stays in the set out of both the top N and the recent N,
    and the top set is unchanged by it."""
    t0 = datetime(2026, 8, 1, 12, 0, 0, tzinfo=timezone.utc)
    total = REPLAY_TOP_N + REPLAY_RECENT_N + 1
    recs = [ReplayRecord(game_id=f"g-{i:02d}", mode=MODE_COOPERATIVE, total_score=i * 100,
                         started_at=t0 + timedelta(minutes=i), finished_at=t0 + timedelta(minutes=i, seconds=30))
            for i in range(1, total + 1)]
    keep, top = replay_keep_set(recs, None)
    assert "g-01" not in keep and "g-01" not in top, "the oldest, lowest game is out of both sets"
    keep, top = replay_keep_set(recs, {"g-01"})
    assert "g-01" in keep, "a pinned game must be kept"
    assert "g-01" not in top, "a pin must not rank a game"
    assert len(keep) in (REPLAY_TOP_N + REPLAY_RECENT_N + 1, REPLAY_RECENT_N + 1)


def test_replay_record_from_json():
    rec = ReplayRecord.from_json(json.dumps({
        "game_id": "g", "mode": MODE_TEAMS, "team_scores": [300, 900], "total_score": 5,
        "started_at": "2026-08-01T12:00:00.123456789Z", "finished_at": "2026-08-01T12:01:00Z",
        "players": [{"score": 100, "agent": True}, {"score": 700}]}).encode())
    assert rec.game_id == "g" and rec.headline_score() == 900 and rec.has_agents()
    assert rec.duration() == timedelta(seconds=59, microseconds=876544)
    solo = ReplayRecord.from_json(b'{"game_id":"s","mode":%d,"players":[{"score":40},{"score":70}]}' % MODE_COMPETITIVE)
    assert solo.headline_score() == 70 and not solo.has_agents() and solo.duration() == timedelta(0)
    assert ReplayRecord.from_json(b'{"game_id":"x","started_at":"yesterday"}') is None
    assert ReplayRecord.from_json(b"not json") is None


def test_rank_order():
    t0 = datetime(2026, 8, 1, tzinfo=timezone.utc)

    def rec(gid, score, minutes, finished):
        return ReplayRecord(game_id=gid, mode=MODE_COOPERATIVE, total_score=score,
                            started_at=t0 + timedelta(minutes=finished - minutes), finished_at=t0 + timedelta(minutes=finished))

    high, quick, slow, newer = rec("a", 500, 5, 10), rec("b", 300, 2, 10), rec("c", 300, 4, 10), rec("d", 300, 2, 20)
    assert high.rank_before(quick) and not quick.rank_before(high)
    assert quick.rank_before(slow), "the shorter game ranks first on a tie"
    assert newer.rank_before(quick), "the newer finish ranks first on a tie"
    assert newer.recent_before(high) and high.recent_before(quick), "newer first, then the game ID"
