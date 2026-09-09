"""Synthetic Gemini process smoke: cargo build -p nyro && python3 tests/proxy_gemini_native_smoke.py.

Local mock responses only; historical provider recordings remain untouched.
Checks exact JSON/SSE data values, not serialization whitespace.
"""

import copy
import http.client
import json
import re
import socket
import subprocess
import sys
import tempfile
import threading
import time
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


REQUEST = {
    "contents": [{"role": "user", "parts": [{"text": "Hello", "thoughtSignature": "opaque-request"}]}],
    "generationConfig": {"candidateCount": 1, "thinkingConfig": {"thinkingBudget": 32}},
    "safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"}],
    "cachedContent": "cachedContents/example",
}
USAGE = {"promptTokenCount": 10, "candidatesTokenCount": 2, "thoughtsTokenCount": 3,
         "totalTokenCount": 15, "cachedContentTokenCount": 6,
         "promptTokensDetails": [{"modality": "TEXT", "tokenCount": 10}], "vendorUsage": {"extra": 7}}
CANDIDATE = {"index": 0, "content": {"role": "model", "parts": [
    {"text": "Hello", "thoughtSignature": "opaque-response"}]}, "finishReason": "STOP",
    "safetyRatings": [{"category": "HARM_CATEGORY_HARASSMENT", "probability": "NEGLIGIBLE"}],
    "groundingMetadata": {"webSearchQueries": ["synthetic smoke"]}}
RESPONSE = {"candidates": [CANDIDATE], "modelVersion": "gemini-private-version-001",
            "usageMetadata": USAGE, "responseId": "synthetic-response"}
BLOCKED = {"promptFeedback": {"blockReason": "SAFETY", "blockReasonMessage": "synthetic block"},
           "modelVersion": "gemini-private-version-001", "usageMetadata": {
               "promptTokenCount": 10, "totalTokenCount": 10, "cachedContentTokenCount": 6}}


class Upstream(BaseHTTPRequestHandler):
    calls = []
    reply = RESPONSE

    def log_message(self, *_):
        pass

    def do_POST(self):
        raw = self.rfile.read(int(self.headers["Content-Length"]))
        self.calls.append((self.path, self.headers.get("x-goog-api-key"), json.loads(raw)))
        streaming = isinstance(self.reply, list)
        body = ("".join(f"data: {json.dumps(value)}\n\n" for value in self.reply)
                if streaming else json.dumps(self.reply)).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream" if streaming else "application/json")
        self.end_headers()
        try:
            if streaming:
                first, separator, body = body.partition(b"\n\n")
                self.wfile.write(first + separator)
                self.wfile.flush()
                time.sleep(0.03)
            self.wfile.write(body)  # HTTP/1.0 EOF terminates Gemini SSE; no [DONE].
        except (BrokenPipeError, ConnectionResetError):
            pass


def request(port, path="/readyz", payload=None, authenticated=True):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        headers = {"Content-Type": "application/json"}
        if authenticated:
            headers["x-goog-api-key"] = "client-secret"
        connection.request("GET" if payload is None else "POST", path,
                           None if payload is None else json.dumps(payload), headers)
        response = connection.getresponse()
        return response.status, response.read(), response.getheader("x-request-id")
    finally:
        connection.close()


@contextmanager
def proxy(binary, upstream_port, native):
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    provider = {"kind": "gemini", "base_url": f"http://127.0.0.1:{upstream_port}/v1beta",
                "api_key": "upstream-secret"}
    if native is not None:
        provider["native_chat"] = native
    config = {"server": {"listen": f"127.0.0.1:{port}"}, "llm": {
        "providers": {"mock": provider}, "models": {"public": {
            "provider": "mock", "upstream_model": "private-model", "workloads": ["chat"],
            "subjects": ["tester"], "quota": {"total_tokens": 1000, "reserve_tokens": 20}}}},
        "security": {"api_keys": [{"id": "tester", "secret": "client-secret"}]}}
    with tempfile.TemporaryDirectory(prefix="nyro-gemini-native-") as directory:
        path = Path(directory) / "config.yaml"
        path.write_text(json.dumps(config))  # JSON is YAML; no PyYAML dependency.
        output, errors = Path(directory) / "stdout.log", Path(directory) / "stderr.log"
        with output.open("wb") as stdout, errors.open("wb") as stderr:
            process = subprocess.Popen([binary, "proxy", "--config", path], stdout=stdout, stderr=stderr)
        try:
            deadline = time.monotonic() + 5
            while True:
                assert process.poll() is None, f"proxy exited before readiness: {errors.read_text()}"
                try:
                    if request(port)[0] == 200:
                        break
                except OSError:
                    pass
                assert time.monotonic() < deadline, "proxy readiness timed out"
                time.sleep(0.02)
            yield port, process, output, errors
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


def main():
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else "target/debug/nyro").resolve()
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    try:
        baseline = []
        for native in (None, False):
            with proxy(binary, upstream.server_port, native) as (port, *_):
                before = len(Upstream.calls)
                status, body, _ = request(port, "/v1beta/models/public:generateContent", REQUEST)
                assert status == 400 and len(Upstream.calls) == before, (native, status, body)
                baseline.append(json.loads(body)["error"])
        assert baseline[0] == baseline[1], baseline
        print("strict omitted/false: HTTP 400, no upstream calls", flush=True)
        # Intentionally RED for a binary predating Gemini native_chat support.
        with proxy(binary, upstream.server_port, True) as (port, process, output, errors):
            observations = []
            for streaming in (False, True):
                action = "streamGenerateContent?alt=sse" if streaming else "generateContent"
                path = f"/v1beta/models/public:{action}"
                before = len(Upstream.calls)
                query_path = path + ("&" if streaming else "?") + "key=client-secret"
                assert request(port, query_path, REQUEST, authenticated=False)[0] == 401
                assert len(Upstream.calls) == before
                for blocked in (False, True):
                    expected = copy.deepcopy(BLOCKED if blocked else RESPONSE)
                    if streaming:
                        if blocked:
                            expected = [expected]
                        else:
                            usage = expected.pop("usageMetadata")
                            expected = [expected, {"usageMetadata": usage}]
                    Upstream.reply = expected
                    before = len(Upstream.calls)
                    status, body, request_id = request(port, path, REQUEST)
                    assert status == 200, (streaming, blocked, status, body)
                    if streaming:
                        assert b"[DONE]" not in body
                        actual = [json.loads(frame[6:]) for frame in body.decode().replace("\r\n", "\n").split("\n\n")
                                  if frame.startswith("data: ")]
                    else:
                        actual = json.loads(body)
                    assert actual == expected, (streaming, blocked, actual, expected)
                    assert Upstream.calls[before:] == [(
                        f"/v1beta/models/private-model:{action}", "upstream-secret", REQUEST)]
                    observations.append((request_id, 10 if blocked else 15, 0 if blocked else 5))
            for field in ("promptTokenCount", "totalTokenCount"):
                Upstream.reply = copy.deepcopy(RESPONSE)
                del Upstream.reply["usageMetadata"][field]
                status, body, _ = request(port, "/v1beta/models/public:generateContent", REQUEST)
                assert status == 502, (field, status, body)
            for candidates in ([{**CANDIDATE, "index": 1}], [CANDIDATE, CANDIDATE]):
                Upstream.reply = {**RESPONSE, "candidates": candidates}
                status, body, _ = request(port, "/v1beta/models/public:generateContent", REQUEST)
                assert status == 502, (candidates, status, body)
            process.terminate()
            process.wait(timeout=5)
            logs = re.sub(r"\x1b\[[0-9;]*m", "", output.read_text() + errors.read_text())
            assert process.returncode == 0, logs
            for request_id, total, completion in observations:
                lines = [line for line in logs.splitlines() if request_id in line and "LLM request finished" in line]
                assert len(lines) == 1, (request_id, lines)
                for field, value in {"input_tokens": 10, "output_tokens": completion,
                                     "total_tokens": total, "quota_charged_tokens": total}.items():
                    assert re.search(rf"\b{field}={value}\b", lines[0]), lines[0]
                assert 'usage_state="complete"' in lines[0], lines[0]
            assert "client-secret" not in logs and "upstream-secret" not in logs
    finally:
        upstream.shutdown()
        upstream.server_close()
    print("Gemini native smoke passed: exact JSON/SSE, alias URL, metadata, blocked prompts, "
          "usage/quota, malformed responses, strict defaults, header-only auth, SIGTERM")


if __name__ == "__main__":
    main()
