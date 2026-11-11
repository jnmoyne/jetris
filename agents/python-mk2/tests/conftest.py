import os

import pytest

NATS_URL = os.environ.get("JETRIS_NATS_URL", "")

needs_server = pytest.mark.skipif(not NATS_URL, reason="JETRIS_NATS_URL not set")
