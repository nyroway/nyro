#!/usr/bin/env python3
"""Root nyro deployments against local HTTP fixtures. Run after cargo build -p nyro."""
import contextlib
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BINARY = Path(__file__).resolve().parents[1] / "target/debug/nyro"
ADMIN = "admin-test-token-0123456789"
SYNC = "sync-test-token-0123456789"


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(base, path, method="GET", body=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(base + path, None if body is None else json.dumps(body).encode(), headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=3)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = response.read()
        return response.status, json.loads(data) if data else None


def wait_for(predicate, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if predicate():
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.05)
    raise AssertionError("condition did not become true before deadline")


@contextlib.contextmanager
def process(directory, name, *args):
    with open(directory / f"{name}.log", "wb") as log:
        child = subprocess.Popen([str(BINARY), *args], stdout=log, stderr=log)
        try:
            yield child
            assert child.poll() is None, f"{name} exited early; inspect {log.name}"
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
                    raise AssertionError(f"{name} failed to drain")
            if child.returncode not in (0, None):
                raise AssertionError(f"{name} exited {child.returncode}: {Path(log.name).read_text()}")


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        assert self.path == "/v1/chat/completions"
        assert self.headers.get("Authorization") == "Bearer upstream-test-secret"
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        response = {"id": "smoke", "object": "chat.completion", "created": 1, "model": body["model"],
                    "choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello"}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3}}
        data = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def chat(base, model="chat"):
    return request(base, "/v1/chat/completions", "POST", {"model": model, "messages": [{"role": "user", "content": "Hello"}]}, "caller-test-secret")


def populate(base, resources):
    for kind in ("upstreams", "models", "consumers"):
        for value in resources[kind]:
            status, response = request(base, f"/v1/resources/{kind}", "POST", value, ADMIN)
            assert status == 200, (kind, status, response)
    return response["version"]


def run(directory, upstream):
    (directory / "admin.token").write_text(ADMIN)
    (directory / "sync.token").write_text(SYNC)
    resources = {"version": 1, "upstreams": [{"id": "pool", "kind": "llm", "targets": [{"id": "a", "protocol": "openai/chat-completions", "base_url": upstream, "model": "backend",
                "auth": {"type": "key-auth", "in": "header", "name": "Authorization", "prefix": "Bearer", "secret": "upstream-test-secret"}}]}],
                "models": [{"id": "chat", "capability": "chat", "upstream": "pool", "access": {"mode": "restricted"}}], "mcps": [],
                "consumers": [{"id": "app", "credentials": [{"id": "key", "type": "key-auth", "secret": "caller-test-secret"}], "grants": {"models": ["chat"]}}]}
    file_port = port()
    config = directory / "resources.json"
    config.write_text(json.dumps(resources))
    base = f"http://127.0.0.1:{file_port}"
    with process(directory, "file", "proxy", "--config", str(config), "--listen", f"127.0.0.1:{file_port}"):
        wait_for(lambda: request(base, "/readyz")[0] == 200)
        assert chat(base)[0] == 200
        config.write_text('{"version":1}')
        assert chat(base)[0] == 200  # File contents are not reread.
    admin_port, sync_port, remote_port, unused_proxy = (port() for _ in range(4))
    admin_base, remote_base = f"http://127.0.0.1:{admin_port}", f"http://127.0.0.1:{remote_port}"
    cp_args = ("serve", "--database", str(directory / "control.db"), "--admin-token-file", str(directory / "admin.token"),
               "--admin-listen", f"127.0.0.1:{admin_port}", "--listen", f"127.0.0.1:{unused_proxy}",
               "--sync-listen", f"127.0.0.1:{sync_port}", "--sync-token-file", str(directory / "sync.token"))
    with process(directory, "remote", "proxy", "--server", f"http://127.0.0.1:{sync_port}", "--sync-token-file", str(directory / "sync.token"), "--node-id", "smoke-edge", "--listen", f"127.0.0.1:{remote_port}"):
        wait_for(lambda: request(remote_base, "/healthz")[0] == 200)
        assert request(remote_base, "/readyz")[0] == 503
        with process(directory, "control", *cp_args):
            wait_for(lambda: request(admin_base, "/v1/resources", token=ADMIN)[0] == 200)
            with socket.socket() as sock:
                assert sock.connect_ex(("127.0.0.1", unused_proxy)) != 0
            assert request(admin_base, "/v1/resources", token=SYNC)[0] == 401
            assert request(f"http://127.0.0.1:{sync_port}", "/v1/config/sync", "POST", {"node_id": "x"}, ADMIN)[0] == 401
            wait_for(lambda: request(remote_base, "/readyz")[0] == 200)
            version = populate(admin_base, resources)
            wait_for(lambda: chat(remote_base)[0] == 200)
            wait_for(lambda: request(admin_base, "/v1/nodes/smoke-edge", token=ADMIN)[1]["applied"] == version)
            view = request(admin_base, "/v1/resources/upstreams/pool", token=ADMIN)[1]
            assert "secret" not in view["targets"][0]["auth"]
            assert request(admin_base, "/v1/resources/upstreams/pool", "PUT", view, ADMIN)[0] == 200
            renamed = dict(resources["models"][0], id="renamed")
            assert request(admin_base, "/v1/resources/models/chat", "PUT", renamed, ADMIN)[0] == 200
            wait_for(lambda: chat(remote_base, "renamed")[0] == 200)
            assert request(admin_base, "/v1/resources/consumers/app", token=ADMIN)[1]["grants"]["models"] == ["renamed"]
            assert request(admin_base, "/v1/resources/models/renamed", "PUT", dict(renamed, upstream="missing"), ADMIN)[0] == 400
        assert chat(remote_base, "renamed")[0] == 200  # Last active generation survives disconnect.
        with process(directory, "control-restarted", *cp_args):
            wait_for(lambda: request(admin_base, "/v1/resources", token=ADMIN)[0] == 200)
            wait_for(lambda: request(admin_base, "/v1/nodes/smoke-edge", token=ADMIN)[0] == 200, timeout=35)
            assert chat(remote_base, "renamed")[0] == 200
    combined_admin, combined_data = port(), port()
    admin_base, data_base = f"http://127.0.0.1:{combined_admin}", f"http://127.0.0.1:{combined_data}"
    with process(directory, "combined", "serve", "--database", str(directory / "combined.db"), "--admin-token-file", str(directory / "admin.token"),
                 "--admin-listen", f"127.0.0.1:{combined_admin}", "--enable-proxy", "--listen", f"127.0.0.1:{combined_data}"):
        wait_for(lambda: request(data_base, "/readyz")[0] == 200)
        version = populate(admin_base, resources)
        wait_for(lambda: chat(data_base)[0] == 200)
        wait_for(lambda: request(admin_base, "/v1/nodes/embedded", token=ADMIN)[1]["applied"] == version)


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="nyro-resource-smoke-") as temporary:
        directory = Path(temporary)
        server = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            run(directory, f"http://127.0.0.1:{server.server_port}/v1")
        finally:
            server.shutdown()
            thread.join()
            server.server_close()
    print("file, remote control/data, memory, restart, auth isolation and rename: passed")
