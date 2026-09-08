"""Process smoke test: cargo build -p nyro && python3 tests/proxy_smoke.py."""

import http.client
import json
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Upstream(BaseHTTPRequestHandler):
    calls = []

    def log_message(self, *_):
        pass

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.calls.append((self.path, self.headers.get("Authorization"), payload))
        if payload["model"] == "temporary-error":
            self.send_response(503)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if self.path == "/v1/embeddings":
            result = {"object": "list", "model": payload["model"], "data": [
                {"object": "embedding", "index": 0, "embedding": [0.25, 0.5]}],
                "usage": {"prompt_tokens": 1, "total_tokens": 1}}
        else:
            result = {"id": "smoke", "object": "chat.completion", "created": 1,
                      "model": payload["model"], "choices": [{"index": 0,
                      "message": {"role": "assistant", "content": "Hello"}, "finish_reason": "stop"}],
                      "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
        streaming = payload.get("stream", False)
        if streaming:
            result["object"] = "chat.completion.chunk"
            result["choices"][0]["delta"] = result["choices"][0].pop("message")
            body = f"data: {json.dumps(result)}\n\ndata: [DONE]\n\n".encode()
        else:
            body = json.dumps(result).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream" if streaming else "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def main():
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else "target/debug/nyro").resolve()
    assert subprocess.run([binary, "proxy", "--help"], capture_output=True).returncode == 0
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]

    def request(path, payload=None, key="client-secret", credential_header="Authorization"):
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
        try:
            connection.request("POST" if payload is not None else "GET", path,
                               json.dumps(payload) if payload is not None else None,
                               {credential_header: f"Bearer {key}" if credential_header == "Authorization" else key, "Content-Type": "application/json"})
            response = connection.getresponse()
            return response.status, response.read()
        finally:
            connection.close()

    try:
        with tempfile.TemporaryDirectory(prefix="nyro-proxy-smoke-") as directory:
            config = Path(directory) / "config.yaml"
            # JSON is a YAML subset; no PyYAML dependency is needed for this process check.
            data = {"server": {"listen": f"127.0.0.1:{port}"}, "llm": {
                "providers": {"mock": {"kind": "openai", "base_url": f"http://127.0.0.1:{upstream.server_port}/v1",
                                       "api_key": "upstream-secret"}},
                "models": {"public": {"provider": "mock", "upstream_model": "internal",
                                      "workloads": ["chat", "embedding"], "subjects": ["tester"]}}},
                "security": {"api_keys": [{"id": "tester", "secret": "client-secret"}]}}
            data["llm"]["models"]["public-routed"] = {
                "backends": [
                    {"id": "disabled", "provider": "mock", "upstream_model": "must-not-run", "weight": 0},
                    {"id": "enabled", "provider": "mock", "upstream_model": "internal"}],
                "workloads": ["chat"], "subjects": ["tester"]}
            data["llm"]["models"]["public-failover"] = {
                "backends": [
                    {"id": "primary", "provider": "mock", "upstream_model": "temporary-error", "priority": 0},
                    {"id": "backup", "provider": "mock", "upstream_model": "internal", "priority": 1}],
                "max_attempts": 2, "health": {"failure_threshold": 1, "cooldown_ms": 30000},
                "workloads": ["chat"], "subjects": ["tester"]}
            data["llm"]["models"]["public-rate"] = {
                "provider": "mock", "upstream_model": "internal",
                "rate": {"requests": 1, "period_ms": 600000},
                "workloads": ["chat"], "subjects": ["tester"]}
            data["llm"]["models"]["public-quota"] = {
                "provider": "mock", "upstream_model": "internal",
                "quota": {"total_tokens": 7, "reserve_tokens": 4},
                "workloads": ["chat", "embedding"], "subjects": ["tester"]}
            config.write_text(json.dumps({**data, "unknown": "secret-marker"}))
            invalid = subprocess.run([binary, "proxy", "--config", config], capture_output=True, timeout=5)
            assert invalid.returncode != 0
            assert b"secret-marker" not in invalid.stderr
            config.write_text(json.dumps(data))
            process = subprocess.Popen([binary, "proxy", "--config", config], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                deadline = time.monotonic() + 5
                while True:
                    assert process.poll() is None, "proxy exited before readiness"
                    try:
                        if request("/readyz")[0] == 200:
                            break
                    except OSError:
                        pass
                    assert time.monotonic() < deadline, "proxy readiness timed out"
                    time.sleep(0.02)
                assert request("/healthz")[0] == 200
                chat = {"model": "public", "messages": [{"role": "user", "content": "Hello"}]}
                assert request("/v1/chat/completions", chat, "invalid")[0] == 401
                assert len(Upstream.calls) == 0
                for path, payload in [("/v1/chat/completions", chat),
                                      ("/v1/embeddings", {"model": "public", "input": "Hello"})]:
                    status, body = request(path, payload)
                    assert status == 200 and json.loads(body)["model"] == "public"
                status, body = request("/v1/chat/completions", {**chat, "stream": True})
                assert status == 200 and body.endswith(b"data: [DONE]\n\n")
                assert json.loads(body.decode().split("\n\n")[0][6:])["model"] == "public"
                for streaming in (False, True):
                    status, body = request("/v1/messages", {
                        "model": "public", "messages": [{"role": "user", "content": "Hello"}],
                        "max_tokens": 32, "stream": streaming}, credential_header="x-api-key")
                    assert status == 200 and b"Hello" in body and b"public" in body
                    if streaming:
                        assert b"message_stop" in body
                    action = "streamGenerateContent?alt=sse" if streaming else "generateContent"
                    status, body = request(f"/v1beta/models/public:{action}", {
                        "contents": [{"role": "user", "parts": [{"text": "Hello"}]}],
                        "generationConfig": {"maxOutputTokens": 32}}, credential_header="x-goog-api-key")
                    assert status == 200 and b"Hello" in body and b"public" in body
                    if streaming:
                        assert b"finishReason" in body
                    status, body = request("/v1/responses", {
                        "model": "public", "input": "Hello", "stream": streaming,
                        "max_output_tokens": 32, "store": False})
                    assert status == 200 and b"Hello" in body and b"public" in body
                    if streaming:
                        assert b"event: response.completed" in body and b"[DONE]" not in body
                    else:
                        result = json.loads(body)
                        assert result["status"] == "completed"
                        assert result["output"][0]["content"][0]["text"] == "Hello"
                for streaming in (False, True):
                    status, body = request("/v1/chat/completions", {
                        **chat, "model": "public-routed", "stream": streaming})
                    assert status == 200 and b"public-routed" in body
                    if streaming:
                        assert body.endswith(b"data: [DONE]\n\n")
                # First request fails over; subsequent SSE request skips the open primary.
                for streaming in (False, True):
                    status, body = request("/v1/chat/completions", {
                        **chat, "model": "public-failover", "stream": streaming})
                    assert status == 200 and b"public-failover" in body
                    if streaming:
                        assert body.endswith(b"data: [DONE]\n\n")
                assert len(Upstream.calls) == 14
                assert [payload["model"] for _, _, payload in Upstream.calls[-3:]] == [
                    "temporary-error", "internal", "internal"]
                limited = {**chat, "model": "public-rate"}
                assert request("/v1/chat/completions", limited)[0] == 200
                status, body = request("/v1/chat/completions", limited)
                assert status == 429 and json.loads(body)["error"]["code"] == "rate_limit_exceeded"
                assert len(Upstream.calls) == 15
                # Actual usage refunds unused reserve, including SSE usage hidden from the client.
                for streaming in (False, True):
                    status, body = request("/v1/chat/completions", {
                        **chat, "model": "public-quota", "stream": streaming})
                    assert status == 200
                    if streaming:
                        assert body.endswith(b"data: [DONE]\n\n") and b'"usage"' not in body
                        assert Upstream.calls[-1][2]["stream_options"]["include_usage"] is True
                status, body = request("/v1/embeddings", {"model": "public-quota", "input": "Hello"})
                assert status == 429 and json.loads(body)["error"]["code"] == "quota_exceeded"
                assert len(Upstream.calls) == 17
                assert all(key == "Bearer upstream-secret" and payload["model"] in {"internal", "temporary-error"}
                           for _, key, payload in Upstream.calls)
                process.terminate()
                output, errors = process.communicate(timeout=5)
                assert process.returncode == 0, errors.decode()
                assert b"client-secret" not in output + errors
                assert b"upstream-secret" not in output + errors
            finally:
                if process.poll() is None:
                    process.kill()
                process.communicate(timeout=5)
    finally:
        upstream.shutdown()
        upstream.server_close()
    print("proxy smoke passed: config, weighted routing, failover, passive health, rate, quota, probes, auth, OpenAI/Anthropic/Gemini Chat/Responses, Embedding, SSE, SIGTERM")


if __name__ == "__main__":
    main()
