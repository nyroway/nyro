"""Unix process test: cargo build -p nyro && python3 tests/proxy_reload_smoke.py."""

import copy
import http.client
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Upstream(BaseHTTPRequestHandler):
    calls = []
    stream_started = threading.Event()
    release_stream = threading.Event()

    def log_message(self, *_):
        pass

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.calls.append((self.headers.get("Authorization"), payload))
        if payload["model"] == "temporary-error":
            self.send_response(503)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        result = {"id": "reload-smoke", "object": "chat.completion", "created": 1,
                  "model": payload["model"], "choices": [{"index": 0,
                  "message": {"role": "assistant", "content": payload["model"]},
                  "finish_reason": "stop"}],
                  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
        streaming = payload.get("stream", False)
        if streaming:
            result["object"] = "chat.completion.chunk"
            result["choices"][0]["delta"] = {"content": "old-start"}
            result["choices"][0].pop("message")
            result["choices"][0]["finish_reason"] = None
            first = f"data: {json.dumps(result)}\n\n".encode()
            result["choices"][0]["delta"] = {"content": "old-finish"}
            result["choices"][0]["finish_reason"] = "stop"
            last = f"data: {json.dumps(result)}\n\ndata: [DONE]\n\n".encode()
            body = first + last
        else:
            body = json.dumps(result).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream" if streaming else "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if streaming:
            self.wfile.write(first)
            self.wfile.flush()
            self.stream_started.set()
            if not self.release_stream.wait(20):
                return
            self.wfile.write(last)
        else:
            self.wfile.write(body)


def main():
    if os.name != "posix":
        print("proxy reload smoke skipped: SIGHUP requires Unix")
        return
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else "target/debug/nyro").resolve()
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]

    def connect(path, payload=None, key="client-secret-old"):
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        try:
            connection.request("POST" if payload is not None else "GET", path,
                               json.dumps(payload) if payload is not None else None,
                               {"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
            return connection, connection.getresponse()
        except Exception:
            connection.close()
            raise

    def request(path="/readyz", payload=None, key="client-secret-old"):
        connection, response = connect(path, payload, key)
        try:
            return response.status, response.read()
        finally:
            connection.close()

    def chat(model="public", key="client-secret-old"):
        return request("/v1/chat/completions", {
            "model": model, "messages": [{"role": "user", "content": "Hello"}]}, key)

    try:
        with tempfile.TemporaryDirectory(prefix="nyro-reload-private-path-") as directory:
            config = Path(directory) / "config.yaml"
            output_path, error_path = Path(directory) / "stdout.log", Path(directory) / "stderr.log"
            data = {"server": {"listen": f"127.0.0.1:{port}"}, "limit": {"concurrency": 8},
                    "llm": {"providers": {"mock": {"kind": "openai",
                        "base_url": f"http://127.0.0.1:{upstream.server_port}/v1",
                        "api_key": "upstream-secret-old"}}, "models": {}},
                    "security": {"api_keys": [{"id": "tester", "secret": "client-secret-old"}]}}
            model = {"provider": "mock", "upstream_model": "internal-old",
                     "workloads": ["chat"], "subjects": ["tester"]}
            data["llm"]["models"] = {
                "public": copy.deepcopy(model),
                "public-rate": {**model, "rate": {"requests": 1, "period_ms": 600000}},
                "public-quota": {**model, "quota": {"total_tokens": 3, "reserve_tokens": 2}},
                "public-failover": {"backends": [
                    {"id": "primary", "provider": "mock", "upstream_model": "temporary-error"},
                    {"id": "backup", "provider": "mock", "upstream_model": "internal-old", "priority": 1}],
                    "max_attempts": 2, "health": {"failure_threshold": 1, "cooldown_ms": 600000},
                    "workloads": ["chat"], "subjects": ["tester"]}}

            def replace(value):
                # JSON is a YAML subset. Replace the entire file as an editor would.
                temporary = config.with_suffix(".tmp")
                temporary.write_text(json.dumps(value) if isinstance(value, dict) else value)
                temporary.replace(config)

            def logs():
                return re.sub(r"\x1b\[[0-9;]*m", "", output_path.read_text() + error_path.read_text())

            def reload_events():
                return [line for line in logs().splitlines() if "nyro::reload" in line and (
                    "Configuration reload finished" in line or "Configuration reload rejected" in line)]

            def reload_config(value, outcome, reason=None):
                count = len(reload_events())
                if value is None:
                    config.unlink()
                else:
                    replace(value)
                process.send_signal(signal.SIGHUP)
                deadline = time.monotonic() + 5
                while len(reload_events()) == count:
                    assert process.poll() is None, f"proxy exited after SIGHUP: {process.returncode}\n{logs()}"
                    assert time.monotonic() < deadline, f"reload event timed out\n{logs()}"
                    time.sleep(0.02)
                event = reload_events()[count]
                assert re.search(rf'outcome="?{outcome}"?(?:\s|$)', event), event
                if reason is not None:
                    assert re.search(rf'reason="?{reason}"?(?:\s|$)', event), event
                assert request()[0] == 200
                if outcome != "rejected":
                    generation = re.search(r"generation=(\d+)", event)
                    assert generation, event
                    return int(generation[1])

            replace(data)
            with output_path.open("wb") as stdout, error_path.open("wb") as stderr:
                process = subprocess.Popen([binary, "proxy", "--config", config], stdout=stdout,
                                           stderr=stderr, env={**os.environ, "RUST_LOG": "nyro=info"})
            stream_connection = None
            try:
                deadline = time.monotonic() + 5
                while True:
                    assert process.poll() is None, f"proxy exited before readiness\n{logs()}"
                    try:
                        if request()[0] == 200:
                            break
                    except OSError:
                        pass
                    assert time.monotonic() < deadline, f"readiness timed out\n{logs()}"
                    time.sleep(0.02)

                generation = reload_config(data, "unchanged")
                # Semantic equality must skip activation despite whitespace and key ordering changes.
                assert reload_config(json.dumps(data, indent=2, sort_keys=True), "unchanged") == generation
                assert chat()[0] == 200
                for name, code in [("public-rate", "rate_limit_exceeded"), ("public-quota", "quota_exceeded")]:
                    assert chat(name)[0] == 200
                    status, body = chat(name)
                    assert status == 429 and json.loads(body)["error"]["code"] == code
                assert chat("public-failover")[0] == 200
                failed_calls = sum(payload["model"] == "temporary-error" for _, payload in Upstream.calls)
                assert failed_calls == 1

                # Rejections leave the listener, auth, routes, and depleted counters intact.
                invalid = copy.deepcopy(data)
                invalid["llm"]["models"]["public"]["provider"] = "private-invalid-provider-marker"
                moved = copy.deepcopy(data)
                moved["server"]["listen"] = "127.0.0.1:0"
                resized = copy.deepcopy(data)
                resized["limit"]["concurrency"] = 9
                rate_changed = copy.deepcopy(data)
                rate_changed["llm"]["models"]["public-rate"]["rate"]["requests"] = 2
                quota_changed = copy.deepcopy(data)
                quota_changed["llm"]["models"]["public-quota"]["quota"]["total_tokens"] = 30
                for value, reason in [
                    ("[private-malformed-marker", "invalid_config"), (None, "read_failed"),
                    (invalid, "invalid_config"), (moved, "restart_required"),
                    (resized, "restart_required"), (rate_changed, "candidate_rejected"),
                    (quota_changed, "candidate_rejected")]:
                    reload_config(value, "rejected", reason)
                    assert chat()[0] == 200
                    assert Upstream.calls[-1][0] == "Bearer upstream-secret-old"
                    assert Upstream.calls[-1][1]["model"] == "internal-old"
                    assert chat("public-rate")[0] == 429 and chat("public-quota")[0] == 429
                assert reload_config(data, "unchanged") == generation

                # Hold an actual upstream SSE body across activation and credential rotation.
                stream_connection, stream_response = connect("/v1/chat/completions", {
                    "model": "public", "messages": [{"role": "user", "content": "Hello"}], "stream": True})
                assert stream_response.status == 200 and Upstream.stream_started.wait(2)
                first_event = b""
                while not first_event.endswith(b"\n\n"):
                    line = stream_response.readline()
                    assert line, "stream closed before its first event"
                    first_event += line
                assert b"old-start" in first_event
                held_call = Upstream.calls[-1]

                updated = copy.deepcopy(data)
                updated["llm"]["models"]["public"]["upstream_model"] = "internal-new"
                updated["llm"]["providers"]["mock"]["api_key"] = "upstream-secret-new"
                updated["security"]["api_keys"][0]["secret"] = "client-secret-new"
                new_generation = reload_config(updated, "applied")
                assert new_generation > generation
                assert chat()[0] == 401
                status, body = chat(key="client-secret-new")
                assert status == 200 and b"internal-new" in body
                assert Upstream.calls[-1][0] == "Bearer upstream-secret-new"
                assert Upstream.calls[-1][1]["model"] == "internal-new"
                for name, code in [("public-rate", "rate_limit_exceeded"), ("public-quota", "quota_exceeded")]:
                    status, body = chat(name, "client-secret-new")
                    assert status == 429 and json.loads(body)["error"]["code"] == code
                assert reload_config(updated, "unchanged") == new_generation
                Upstream.release_stream.set()
                remaining = stream_response.read()
                assert b"old-finish" in remaining and remaining.endswith(b"data: [DONE]\n\n")
                assert held_call[0] == "Bearer upstream-secret-old" and held_call[1]["model"] == "internal-old"
                stream_connection.close()
                stream_connection = None

                # A further route-only edit preserves passive health for unchanged backends.
                assert chat("public-failover", "client-secret-new")[0] == 200
                failures_before = sum(payload["model"] == "temporary-error" for _, payload in Upstream.calls)
                updated["llm"]["models"]["public"]["upstream_model"] = "internal-newer"
                assert reload_config(updated, "applied") > new_generation
                assert chat("public-failover", "client-secret-new")[0] == 200
                assert sum(payload["model"] == "temporary-error" for _, payload in Upstream.calls) == failures_before
                # A special file must not leave a blocking open alive during shutdown.
                config.unlink()
                os.mkfifo(config)
                count = len(reload_events())
                process.send_signal(signal.SIGHUP)
                deadline = time.monotonic() + 5
                while len(reload_events()) == count:
                    assert process.poll() is None, f"proxy exited on FIFO reload\n{logs()}"
                    assert time.monotonic() < deadline, f"FIFO reload did not reject promptly\n{logs()}"
                    time.sleep(0.02)
                event = reload_events()[count]
                assert re.search(r'outcome="?rejected"?(?:\s|$)', event), event
                assert re.search(r'reason="?read_failed"?(?:\s|$)', event), event
                assert request()[0] == 200
                process.terminate()
                process.communicate(timeout=5)
                assert process.returncode == 0, logs()
                reload_logs = "\n".join(reload_events())
                for secret in ["client-secret-old", "client-secret-new", "upstream-secret-old",
                               "upstream-secret-new", "private-invalid-provider-marker",
                               "private-malformed-marker", str(config)]:
                    assert secret not in reload_logs, f"reload log leaked {secret}"
                assert "fingerprint" not in reload_logs
            finally:
                Upstream.release_stream.set()
                if stream_connection is not None:
                    stream_connection.close()
                if process.poll() is None:
                    process.kill()
                process.communicate(timeout=5)
    finally:
        upstream.shutdown()
        upstream.server_close()
    print("proxy reload smoke passed: SIGHUP, atomic replacement, deduplication, rejection, "
          "auth/routes, SSE retention, rate/quota/health reuse, redacted logs, FIFO rejection, SIGTERM")


if __name__ == "__main__":
    main()
