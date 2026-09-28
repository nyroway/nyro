"""Local process acceptance: cargo build -p nyro && python3 tests/mcp_gateway_smoke.py."""
import copy
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CLIENT = "mcp-client-sensitive-marker"
UPSTREAM = "mcp-upstream-sensitive-marker"
RESULT = "mcp-tool-result-sensitive-marker"
ADMIN = "mcp-admin-test-secret"
REVISION = "2026-07-28"


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request_http(port, method, path, value=None, headers=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=8)
    try:
        connection.request(method, path, None if value is None else json.dumps(value), headers or {})
        response = connection.getresponse()
        return response.status, response.getheader("Content-Type", ""), response.read()
    finally:
        connection.close()


def wait(predicate, process=None):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process is not None and process.poll() is not None:
            raise AssertionError(f"nyro exited: {process.returncode}")
        try:
            if predicate():
                return
        except (ConnectionError, OSError):
            pass
        time.sleep(0.03)
    raise AssertionError("timed out waiting for state")


class Upstream(BaseHTTPRequestHandler):
    calls = []
    lock = threading.Lock()

    def log_message(self, *_):
        pass

    def do_POST(self):
        value = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path == "/v1/chat/completions":
            result = {"id": "chat", "object": "chat.completion", "created": 1, "model": "example",
                      "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello"}, "finish_reason": "stop"}],
                      "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
        else:
            assert self.headers["Authorization"] == f"Bearer {UPSTREAM}"
            assert self.headers["MCP-Protocol-Version"] == REVISION
            method = value["method"]
            assert self.headers["Mcp-Method"] == method
            if method == "server/discover":
                result = {"resultType": "complete", "supportedVersions": [REVISION], "capabilities": {"tools": {}}, "ttlMs": 0, "cacheScope": "private"}
            elif method == "tools/list":
                names = ["read", "stream", "drop"] if value["params"].get("cursor") else ["hidden"]
                result = {"tools": [{"name": name, "inputSchema": {"type": "object"}} for name in names]}
                if names == ["hidden"]:
                    result["nextCursor"] = "next-page"
            elif method == "tools/call":
                with self.lock:
                    self.calls.append((self.path, value["params"]["name"]))
                if value["params"]["name"] == "drop":
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                result = {"resultType": "complete", "content": [{"type": "text", "text": RESULT}], "structuredContent": [1, 2]}
            else:
                raise AssertionError(method)
            result = {"jsonrpc": "2.0", "id": value["id"], "result": result}
        body = json.dumps(result).encode()
        stream = value.get("method") == "tools/call" and value["params"]["name"] == "stream"
        if stream:
            token = value["params"]["_meta"]["progressToken"]
            progress = {"jsonrpc": "2.0", "method": "notifications/progress", "params": {"progressToken": token, "progress": 1, "total": 2}}
            body = b"data: " + json.dumps(progress).encode() + b"\n\ndata: " + body + b"\n\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream" if stream else "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        for start in range(0, len(body), 7):
            self.wfile.write(body[start:start + 7])
            self.wfile.flush()


def run():
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="nyro-mcp-") as directory:
            root = Path(directory)
            data, admin_port = port(), port()
            config = {"server": {"listen": f"127.0.0.1:{data}"},
                      "security": {"api_keys": [{"id": "client", "secret": CLIENT}]},
                      "llm": {"providers": {"p": {"kind": "openai", "base_url": f"http://127.0.0.1:{upstream.server_port}/v1"}},
                              "models": {"public": {"provider": "p", "upstream_model": "example", "workloads": ["chat"], "subjects": ["client"]}}},
                      "mcp": {"request_timeout_ms": 1000, "servers": {name: {"transport": "http", "url": f"http://127.0.0.1:{upstream.server_port}/{name}",
                              "bearer_token": UPSTREAM, "subjects": ["client"], "allowed_tools": tools}
                              for name, tools in [("one", ["read", "stream", "drop"]), ("two", ["read"])]}}}
            path = root / "nyro.yaml"
            path.write_text(json.dumps(config))
            token = root / "admin-token"
            token.write_text(ADMIN)
            binary = str(Path("target/debug/nyro").resolve())
            process = None
            log_path = root / "nyro.log"
            log = log_path.open("wb")

            def start(args):
                nonlocal process
                process = subprocess.Popen([binary, *args], stdout=log, stderr=log,
                                           env={**os.environ, "RUST_LOG": "nyro=debug,rmcp=trace,rmcp::service=trace"})
                wait(lambda: request_http(data, "GET", "/readyz")[0] == 200, process)

            def stop():
                nonlocal process
                if process is not None:
                    process.terminate()
                    try:
                        assert process.wait(timeout=10) == 0
                    finally:
                        if process.poll() is None:
                            process.kill()
                            process.wait()
                        process = None

            def mcp(method="tools/call", name="read", server="one", key=CLIENT, cursor=None):
                meta = {"io.modelcontextprotocol/protocolVersion": REVISION,
                        "io.modelcontextprotocol/clientInfo": {"name": "nyro-smoke", "version": "1"},
                        "io.modelcontextprotocol/clientCapabilities": {}, "progressToken": "smoke-progress"}
                params = {"_meta": meta}
                headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json",
                           "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": REVISION, "Mcp-Method": method}
                if method == "tools/call":
                    params.update(name=name, arguments={"query": "smoke"})
                    headers["Mcp-Name"] = name
                if cursor:
                    params["cursor"] = cursor
                return request_http(data, "POST", f"/mcp/{server}", {"jsonrpc": "2.0", "id": 7, "method": method, "params": params}, headers)

            def admin(method="GET", path="/admin/config", value=None):
                status, _, body = request_http(admin_port, method, path, value, {"Authorization": f"Bearer {ADMIN}", "Content-Type": "application/json"})
                assert status == 200, (status, body)
                return json.loads(body)

            try:
                start(["proxy", "--config", str(path)])
                for server in ["one", "two"]:
                    status, _, body = mcp(server=server)
                    assert status == 200 and json.loads(body)["result"]["structuredContent"] == [1, 2], body
                assert mcp(name="hidden")[0] == 403
                assert mcp(name="stream", server="two")[0] == 403
                assert mcp(key="wrong")[0] == 401
                status, _, body = mcp(method="tools/list")
                first = json.loads(body)["result"]
                assert status == 200 and first["tools"] == [] and first["nextCursor"] == "next-page"
                _, _, body = mcp(method="tools/list", cursor=first["nextCursor"])
                assert len(json.loads(body)["result"]["tools"]) == 3
                status, mime, body = mcp(name="stream")
                assert status == 200 and "text/event-stream" in mime, (mime, body)
                events = [json.loads(line[6:]) for line in body.splitlines() if line.startswith(b"data: ")]
                assert any(e.get("method") == "notifications/progress" for e in events), events
                assert events[-1]["id"] == 7 and events[-1]["result"]["structuredContent"] == [1, 2]
                status, _, body = mcp(name="drop")
                assert status >= 400 or "error" in json.loads(body)
                assert Upstream.calls.count(("/one", "drop")) == 1
                status, _, body = request_http(data, "POST", "/v1/chat/completions", {"model": "public", "messages": [{"role": "user", "content": "hello"}]}, {"Authorization": f"Bearer {CLIENT}", "Content-Type": "application/json"})
                assert status == 200 and json.loads(body)["choices"][0]["message"]["content"] == "hello"
                changed = copy.deepcopy(config)
                changed["security"]["api_keys"][0]["secret"] = "replacement-client"
                path.write_text(json.dumps(changed))
                process.send_signal(signal.SIGHUP)
                wait(lambda: mcp(method="server/discover", key="replacement-client")[0] == 200, process)
                assert mcp()[0] == 401
                changed["mcp"]["servers"]["one"]["subjects"] = ["missing"]
                path.write_text(json.dumps(changed))
                process.send_signal(signal.SIGHUP)
                wait(lambda: "Configuration reload rejected" in log_path.read_text() or "reload failed" in log_path.read_text().lower(), process)
                assert mcp(method="server/discover", key="replacement-client")[0] == 200
                stop()
                path.write_text(json.dumps(config))
                command = ["serve", "--database", str(root / "control.db"), "--admin-listen", f"127.0.0.1:{admin_port}", "--admin-token-file", str(token)]
                start([*command, "--config", str(path)])
                view = admin()
                assert UPSTREAM not in json.dumps(view) and CLIENT not in json.dumps(view)
                exported = admin(path="/admin/config/export")["draft"]["config"]
                exported["mcp"]["servers"]["one"]["allowed_tools"] = ["stream"]
                saved = admin("PUT", value={"expected_revision": 1, "config": exported})
                assert mcp()[0] == 200
                admin("POST", "/admin/config/publish", {"revision": 2})
                assert mcp()[0] == 403
                stop()
                start(command)
                assert mcp()[0] == 403 and mcp(server="two")[0] == 200
                stop()
                log.flush()
                logs = log_path.read_text()
                for secret in [CLIENT, UPSTREAM, RESULT]:
                    assert secret not in logs, "sensitive data found in logs"
            except Exception:
                log.flush()
                print(log_path.read_text()[-6000:])
                raise
            finally:
                stop()
                log.close()
    finally:
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=3)
    print("MCP proxy/serve process smoke passed")


if __name__ == "__main__":
    run()
