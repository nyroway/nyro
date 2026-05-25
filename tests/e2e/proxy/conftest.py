"""Proxy/protocol-conversion E2E tests, backed by ``nyro-tools replay``.

Pipeline orchestrated by these fixtures:

  1. Scan ``tests/e2e/fixtures/<protocol>/<vendor>/*.jsonl`` collecting every
     ``replay_model`` string.
  2. Spawn one ``nyro-tools replay`` subprocess per protocol on ports
     25208-25211 (or ephemeral if those are busy).
  3. Synthesise a ``standalone.yaml`` whose 4 providers point at the replay
     instances and whose models use the ``replay_model`` string as
     ``name`` / ``vmodel`` / ``targets[].model`` (so nyro overrides the
     request body model and replay's HashMap lookup hits).
  4. Boot ``nyro-server`` against that config and yield its base URL.

If the fixtures tree is empty (typical for a fresh checkout) the suite is
skipped so CI stays green until users contribute recorded fixtures.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import threading
from collections import defaultdict
from pathlib import Path
from typing import Iterator

import pytest

from tests.common.helpers import (
    find_free_port,
    is_port_free,
    start_nyro_server,
    stop_nyro_server,
    wait_until_ready,
)

# Public protocol short-names (kebab-case) — must mirror nyro-tools/protocol.rs
PROTOCOLS: tuple[str, ...] = (
    "openai-chat",
    "openai-responses",
    "anthropic-messages",
    "google-content",
)

# Map nyro-tools' kebab-case protocol → canonical ProtocolId string used
# inside nyro server's YAML `endpoints:` map. The server's write-path
# normalization (PR4) round-trips legacy short names through aliases,
# but we feed canonical form so e2e exercises the post-normalization
# path directly.
NYRO_PROTOCOL_KEY: dict[str, str] = {
    "openai-chat": "openai/chat/v1",
    "openai-responses": "openai/responses/v1",
    "anthropic-messages": "anthropic/messages/2023-06-01",
    "google-content": "google/generate/v1beta",
}

# Path suffix appended to the replay base URL inside `endpoints[*].base_url`.
# OpenAI-shape upstreams expect a `/v1` prefix; Anthropic / Gemini use the
# bare host, since their paths are absolute (`/v1/messages`,
# `/v1beta/models/...`).
NYRO_BASE_URL_PATH: dict[str, str] = {
    "openai-chat": "/v1",
    "openai-responses": "/v1",
    "anthropic-messages": "",
    "google-content": "",
}

DEFAULT_REPLAY_PORTS: tuple[int, ...] = (25208, 25209, 25210, 25211)
FIXTURES_ROOT = Path(__file__).resolve().parents[2] / "e2e" / "fixtures"


# ---------------------------------------------------------------------------
# fixture discovery
# ---------------------------------------------------------------------------


def _scan_replay_models() -> dict[str, list[str]]:
    """Return ``{protocol: [replay_model, ...]}`` for every recorded fixture."""
    out: dict[str, list[str]] = defaultdict(list)
    for protocol in PROTOCOLS:
        protocol_dir = FIXTURES_ROOT / protocol
        if not protocol_dir.exists():
            continue
        for jsonl in sorted(protocol_dir.rglob("*.jsonl")):
            try:
                first = jsonl.read_text(encoding="utf-8").splitlines()[0]
                doc = json.loads(first)
            except (OSError, IndexError, json.JSONDecodeError) as exc:
                pytest.fail(f"unreadable fixture {jsonl}: {exc}")
            replay_model = doc.get("replay_model")
            recorded_protocol = doc.get("protocol")
            if not replay_model or recorded_protocol != protocol:
                pytest.fail(
                    f"fixture {jsonl} has invalid replay_model/protocol: "
                    f"replay_model={replay_model!r} protocol={recorded_protocol!r}"
                )
            out[protocol].append(replay_model)
    return dict(out)


def _choose_ports() -> list[int]:
    if all(is_port_free(p) for p in DEFAULT_REPLAY_PORTS):
        return list(DEFAULT_REPLAY_PORTS)
    return [find_free_port() for _ in PROTOCOLS]


# ---------------------------------------------------------------------------
# config rendering
# ---------------------------------------------------------------------------


def _render_standalone(
    proxy_port: int,
    replay_ports: dict[str, int],
    replay_models: dict[str, list[str]],
) -> Path:
    providers = []
    for protocol in PROTOCOLS:
        port = replay_ports[protocol]
        suffix = NYRO_BASE_URL_PATH[protocol]
        nyro_proto = NYRO_PROTOCOL_KEY[protocol]
        providers.append({
            "name": f"replay-{protocol}",
            "default_protocol": nyro_proto,
            "endpoints": {
                nyro_proto: {"base_url": f"http://127.0.0.1:{port}{suffix}"},
            },
            "api_key": "replay",
        })

    models = []
    for protocol in PROTOCOLS:
        for replay_model in replay_models.get(protocol, []):
            models.append({
                "name": replay_model,
                "vmodel": replay_model,
                "backends": [
                    {"provider": f"replay-{protocol}", "model": replay_model},
                ],
            })

    config = {
        "server": {"proxy_host": "127.0.0.1", "proxy_port": proxy_port},
        "providers": providers,
        "models": models,
    }
    tmp = tempfile.NamedTemporaryFile(
        suffix=".yaml", mode="w", delete=False, encoding="utf-8"
    )
    json.dump(config, tmp, indent=2)
    tmp.close()
    return Path(tmp.name)


# ---------------------------------------------------------------------------
# subprocess management
# ---------------------------------------------------------------------------


def _start_replay(
    nyro_tools: Path, protocol: str, port: int
) -> tuple[subprocess.Popen[str], list[str]]:
    logs: list[str] = []
    proc = subprocess.Popen(
        [
            str(nyro_tools),
            "replay",
            "-p",
            protocol,
            "-i",
            str(FIXTURES_ROOT),
            "-P",
            str(port),
            "-H",
            "127.0.0.1",
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        env={**os.environ, "RUST_LOG": "info"},
    )

    def _drain() -> None:
        assert proc.stdout is not None
        for line in proc.stdout:
            logs.append(line.rstrip("\n"))

    threading.Thread(
        target=_drain, name=f"replay-log-{protocol}", daemon=True
    ).start()
    return proc, logs


def _stop_proc(proc: subprocess.Popen[str], logs: list[str], label: str) -> None:
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=8)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=3)
    if proc.returncode not in (0, None, -15):
        tail = "\n".join(logs[-80:])
        print(f"\n--- {label} logs (tail) ---", file=sys.stderr)
        print(tail, file=sys.stderr)


# ---------------------------------------------------------------------------
# fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def nyro_tools_binary(repo_root: Path) -> Path:
    env_bin = os.environ.get("NYRO_TOOLS_BINARY")
    if env_bin:
        candidate = Path(env_bin)
        if not candidate.is_absolute():
            candidate = repo_root / candidate
    else:
        candidate = repo_root / "target" / "debug" / "nyro-tools"
    if not candidate.exists():
        pytest.skip(
            f"nyro-tools binary not found at {candidate}; "
            "run `cargo build -p nyro-tools` or set NYRO_TOOLS_BINARY"
        )
    return candidate


@pytest.fixture(scope="session")
def scenario_metadata(nyro_tools_binary: Path) -> dict[str, dict]:
    """Return ``{scenario_name: metadata}`` from ``nyro-tools print-scenarios``."""
    out = subprocess.run(
        [str(nyro_tools_binary), "print-scenarios"],
        capture_output=True,
        text=True,
        check=True,
    )
    doc = json.loads(out.stdout)
    return {entry["name"]: entry for entry in doc["scenarios"]}


@pytest.fixture(scope="session")
def replay_models() -> dict[str, list[str]]:
    return _scan_replay_models()


@pytest.fixture(scope="module")
def replay_cluster(
    nyro_tools_binary: Path, replay_models: dict[str, list[str]]
) -> Iterator[dict[str, int]]:
    if not any(replay_models.values()):
        pytest.skip(
            "no fixtures recorded under tests/e2e/fixtures/ — "
            "run `nyro-tools record` to populate at least one vendor first"
        )
    ports = _choose_ports()
    port_map = dict(zip(PROTOCOLS, ports))
    procs: list[tuple[str, subprocess.Popen[str], list[str]]] = []
    try:
        for protocol, port in port_map.items():
            proc, logs = _start_replay(nyro_tools_binary, protocol, port)
            procs.append((protocol, proc, logs))
        for port in ports:
            wait_until_ready(f"http://127.0.0.1:{port}/", timeout=15.0)
        yield port_map
    finally:
        for protocol, proc, logs in procs:
            _stop_proc(proc, logs, f"nyro-tools replay {protocol}")


@pytest.fixture(scope="module")
def nyro_proxy_base(
    nyro_binary: Path,
    replay_cluster: dict[str, int],
    replay_models: dict[str, list[str]],
) -> Iterator[str]:
    proxy_port = find_free_port()
    config_path = _render_standalone(proxy_port, replay_cluster, replay_models)
    proc, logs = start_nyro_server(config_path, nyro_binary=nyro_binary)
    base = f"http://127.0.0.1:{proxy_port}"
    try:
        wait_until_ready(f"{base}/v1/chat/completions", timeout=30.0)
        yield base
    finally:
        stop_nyro_server(proc, logs)
        try:
            config_path.unlink(missing_ok=True)
        except OSError:
            pass
