"""NATS CLI contexts, resolved from a private config directory."""

import json

import pytest

from python_mk2 import natsctx


@pytest.fixture
def ctxdir(tmp_path, monkeypatch):
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))
    d = tmp_path / "nats" / "context"
    d.mkdir(parents=True)
    return d


def write(ctxdir, name, cfg):
    (ctxdir / f"{name}.json").write_text(json.dumps(cfg))


def test_named_context(ctxdir):
    write(ctxdir, "lab", {"url": "nats://a:4222, nats://b:4222", "user": "u", "password": "p",
                          "jetstream_domain": "hub", "inbox_prefix": "_IN", "description": "x"})
    s = natsctx.resolve("lab")
    assert s.name == "lab" and s.js_domain == "hub"
    assert s.connect_kwargs == {"servers": ["nats://a:4222", "nats://b:4222"], "user": "u", "password": "p",
                                "inbox_prefix": "_IN"}


def test_selected_context(ctxdir):
    write(ctxdir, "one", {"url": "nats://one:4222", "token": "s3cret"})
    (ctxdir.parent / "context.txt").write_text("one\n")
    s = natsctx.resolve("")
    assert s.name == "one" and s.connect_kwargs == {"servers": ["nats://one:4222"], "token": "s3cret"}


def test_nothing_selected_is_the_default_server(ctxdir):
    s = natsctx.resolve("")
    assert s.name == "" and s.connect_kwargs == {"servers": [natsctx.DEFAULT_URL]} and s.js_domain == ""


def test_absolute_path(ctxdir, tmp_path):
    f = tmp_path / "elsewhere.json"
    f.write_text(json.dumps({"url": "nats://e:4222"}))
    s = natsctx.resolve(str(f))
    assert s.name == "elsewhere" and s.connect_kwargs["servers"] == ["nats://e:4222"]


def test_unknown_and_invalid(ctxdir):
    with pytest.raises(natsctx.ContextError, match="unknown context"):
        natsctx.resolve("nope")
    with pytest.raises(natsctx.ContextError, match="invalid context name"):
        natsctx.resolve("../nope")
    (ctxdir / "broken.json").write_text("{")
    with pytest.raises(natsctx.ContextError):
        natsctx.resolve("broken")


def test_unsupported_is_refused(ctxdir):
    write(ctxdir, "proxied", {"url": "nats://p:4222", "socks_proxy": "socks5://x:1080"})
    with pytest.raises(natsctx.ContextError, match="socks_proxy"):
        natsctx.resolve("proxied")


def test_tls_first(ctxdir):
    write(ctxdir, "tls", {"url": "tls://t:4222", "tls_first": True})
    s = natsctx.resolve("tls")
    assert s.connect_kwargs["tls_handshake_first"] is True and s.connect_kwargs["tls"] is not None


def test_creds_need_nkeys(ctxdir, monkeypatch):
    write(ctxdir, "creds", {"url": "nats://c:4222", "creds": "~/x.creds"})
    try:
        import nkeys  # noqa: F401
    except ImportError:
        with pytest.raises(natsctx.ContextError, match="nkeys"):
            natsctx.resolve("creds")
    else:
        s = natsctx.resolve("creds")
        assert not s.connect_kwargs["user_credentials"].startswith("~")
