"""NATS CLI contexts.

Resolve a context the way the `nats` CLI does (the selected one when no name
is given) into the options a connection needs: the servers, the credentials,
TLS, the inbox prefix and the JetStream domain. A context lives in
$XDG_CONFIG_HOME/nats/context/<name>.json (~/.config when the variable is
unset) and the selected one is named in nats/context.txt beside that
directory. A name that is an absolute path is read as the context file
itself.
"""

import json
import os
import ssl
from dataclasses import dataclass, field

DEFAULT_URL = "nats://127.0.0.1:4222"


class ContextError(Exception):
    """A context that cannot be found, read, or connected with."""


@dataclass
class Settings:
    """What a context resolved to."""

    name: str = ""
    url: str = ""
    js_domain: str = ""
    connect_kwargs: dict = field(default_factory=dict)  # for nats.connect(**kwargs), servers included


def parent_dir():
    parent = os.environ.get("XDG_CONFIG_HOME", "")
    if parent:
        return parent
    home = os.path.expanduser("~")
    if not home or home == "~":
        raise ContextError("cannot determine home directory")
    return os.path.join(home, ".config")


def selected_context():
    """The name in nats/context.txt, or "" when none is selected."""
    try:
        with open(os.path.join(parent_dir(), "nats", "context.txt"), encoding="utf-8") as f:
            return f.read().strip()
    except OSError:
        return ""


def _valid_name(name):
    return bool(name) and ".." not in name and os.sep not in name


def _expand_home(path):
    if path.startswith("~"):
        return os.path.expanduser("~") + path[1:]
    return path


def _str(cfg, key):
    v = cfg.get(key)
    return v if isinstance(v, str) else ""


def load(name=""):
    """Read a context's settings as the raw JSON object: the named one, the
    file itself when the name is an absolute path, or the selected one when
    the name is empty ({} when nothing is selected). Returns (name, config)."""
    path = ""
    if name and os.path.isabs(name):
        path = name
        name = os.path.splitext(os.path.basename(name))[0]
    else:
        if not name:
            name = selected_context()
            if not name:
                return "", {}
        if not _valid_name(name):
            raise ContextError(f"invalid context name {name}")
        path = os.path.join(parent_dir(), "nats", "context", name + ".json")
        if not os.path.exists(path):
            raise ContextError(f'unknown context "{name}"')
    try:
        with open(path, encoding="utf-8") as f:
            cfg = json.load(f)
    except (OSError, ValueError) as e:
        raise ContextError(f"context {name}: {e}") from e
    if not isinstance(cfg, dict):
        raise ContextError(f"context {name}: not a JSON object")
    return name, cfg


def resolve(name=""):
    """A context as the options a connection is made with. Anything the
    client library cannot do is refused by name rather than ignored."""
    name, cfg = load(name)
    url = _str(cfg, "url")
    kwargs = {}
    servers = [u.strip() for u in url.split(",") if u.strip()]
    kwargs["servers"] = servers or [DEFAULT_URL]

    for key, what in (("socks_proxy", "a SOCKS proxy"), ("nsc", "an nsc lookup"),
                      ("windows_cert_store", "a Windows certificate store")):
        if cfg.get(key):
            raise ContextError(f"context {name} asks for {what} ({key}), which this agent cannot do: "
                               "pass --server, or use a context without it")

    # Credentials, in the CLI's order of precedence: user/password, then a
    # creds file, then an nkey seed file. The creds path alone gets
    # environment variables expanded, as the CLI does.
    user, creds, nkey = _str(cfg, "user"), os.path.expandvars(_str(cfg, "creds")), _str(cfg, "nkey")
    if user:
        kwargs["user"] = user
        kwargs["password"] = _str(cfg, "password")
    elif creds:
        _need_nkeys(name, "creds")
        kwargs["user_credentials"] = _expand_home(creds)
    elif nkey:
        _need_nkeys(name, "nkey")
        kwargs["nkeys_seed"] = _expand_home(nkey)
    if _str(cfg, "token"):
        kwargs["token"] = _str(cfg, "token")

    cert, key, ca = _str(cfg, "cert"), _str(cfg, "key"), _str(cfg, "ca")
    tls_first = cfg.get("tls_first") is True
    if (cert and key) or ca or tls_first:
        try:
            ctx = ssl.create_default_context(purpose=ssl.Purpose.SERVER_AUTH)
            if ca:
                ctx.load_verify_locations(cafile=_expand_home(ca))
            if cert and key:
                ctx.load_cert_chain(certfile=_expand_home(cert), keyfile=_expand_home(key))
        except (OSError, ssl.SSLError) as e:
            raise ContextError(f"context {name}: TLS files: {e}") from e
        kwargs["tls"] = ctx
        if tls_first:
            kwargs["tls_handshake_first"] = True

    if _str(cfg, "inbox_prefix"):
        kwargs["inbox_prefix"] = _str(cfg, "inbox_prefix")

    return Settings(name=name, url=url, js_domain=_str(cfg, "jetstream_domain"), connect_kwargs=kwargs)


def _need_nkeys(name, what):
    try:
        import nkeys  # noqa: F401
    except ImportError as e:
        raise ContextError(f"context {name} authenticates with {what}, which needs the nkeys package: "
                           "install it (pip install nkeys, or the python-mk2[nkeys] extra)") from e
