"""Synthetic Responses smoke: cargo build -p nyro && python3 tests/proxy_responses_native_smoke.py.

Stdlib only; compares JSON values and SSE event/data fidelity, not whitespace.
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
    "model": "public",
    "input": [
        {"type": "message", "role": "user", "content": [
            {"type": "input_text", "text": "Hello"},
            {"type": "input_image", "image_url": "data:image/png;base64,c3ludGhldGlj"}]},
        {"type": "reasoning", "id": "rs_synthetic", "summary": [],
         "encrypted_content": "opaque-request"},
        {"type": "function_call", "call_id": "call_synthetic", "name": "weather",
         "arguments": "{}"},
        {"type": "function_call_output", "call_id": "call_synthetic", "output": "sunny"},
        {"type": "message", "role": "assistant", "phase": "commentary",
         "content": [{"type": "output_text", "text": "Checking", "annotations": []}]},
    ],
    "tools": [{"type": "function", "name": "weather", "parameters": {"type": "object"}}],
    "include": ["reasoning.encrypted_content"],
    "reasoning": {"effort": "low", "summary": "auto"},
    "metadata": {"synthetic": "native-smoke"},
}
USAGE = {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
         "input_tokens_details": {"cached_tokens": 6},
         "output_tokens_details": {"reasoning_tokens": 3}}
RESPONSE = {
    "id": "resp_synthetic", "object": "response", "created_at": 1,
    "model": "private-model", "status": "completed", "store": False,
    "output": [{"type": "reasoning", "id": "rs_output", "summary": [],
                "encrypted_content": "opaque-response"},
               {"type": "message", "id": "msg_synthetic", "role": "assistant",
                "phase": "final_answer", "status": "completed", "content": [
                    {"type": "output_text", "text": "Hello", "annotations": []}]}],
    "usage": USAGE, "metadata": {"synthetic": "preserved"},
}


def events(terminal):
    created = {key: terminal[key] for key in ("id", "object", "created_at", "model")}
    created.update(status="in_progress", output=[], usage=None)
    values = [
        {"type": "response.created", "response": created},
        {"type": "response.in_progress", "response": created},
        {"type": "response.reasoning_summary_text.delta", "item_id": "rs_output",
         "output_index": 0, "summary_index": 0, "delta": "Synthetic thought"},
        {"type": "response.synthetic_extension", "vendor": {"opaque": [1, "unchanged"]}},
        {"type": f"response.{terminal['status']}", "response": terminal},
    ]
    return [(None if index == 3 else value["type"], {**value, "sequence_number": index})
            for index, value in enumerate(values)]


class Upstream(BaseHTTPRequestHandler):
    calls = []
    reply = RESPONSE

    def log_message(self, *_):
        pass

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.calls.append((self.path, self.headers.get("Authorization"), payload))
        streaming = isinstance(self.reply, list)
        body = ("".join((f"event: {event}\n" if event else "") +
                        f"data: {json.dumps(value)}\n\n" for event, value in self.reply)
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
            self.wfile.write(body)  # Clean HTTP/1.0 EOF; Responses has no [DONE].
        except (BrokenPipeError, ConnectionResetError):
            pass


def request(port, path="/readyz", payload=None, authenticated=True):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    body = bytearray()
    try:
        headers = {"Content-Type": "application/json"}
        if authenticated:
            headers["Authorization"] = "Bearer client-secret"
        connection.request("GET" if payload is None else "POST", path,
                           None if payload is None else json.dumps(payload), headers)
        response = connection.getresponse()
        failure = None
        try:
            while chunk := response.read1(65536):
                body.extend(chunk)
        except (http.client.HTTPException, OSError) as error:
            if isinstance(error, http.client.IncompleteRead):
                body.extend(error.partial)
            failure = type(error).__name__
        return response.status, bytes(body), response.getheader("x-request-id"), failure
    finally:
        connection.close()


@contextmanager
def proxy(binary, upstream_port, native):
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    provider = {"kind": "openai", "api": "responses",
                "base_url": f"http://127.0.0.1:{upstream_port}/v1", "api_key": "upstream-secret"}
    if native is not None:
        provider["native_chat"] = native
    config = {"server": {"listen": f"127.0.0.1:{port}"}, "llm": {
        "providers": {"mock": provider}, "models": {"public": {
            "provider": "mock", "upstream_model": "private-model", "workloads": ["chat"],
            "subjects": ["tester"], "quota": {"total_tokens": 1000, "reserve_tokens": 20}}}},
        "security": {"api_keys": [{"id": "tester", "secret": "client-secret"}]}}
    with tempfile.TemporaryDirectory(prefix="nyro-responses-native-") as directory:
        path = Path(directory) / "config.yaml"
        path.write_text(json.dumps(config))
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


def sse(body):
    frames = []
    for frame in body.decode().replace("\r\n", "\n").split("\n\n"):
        lines = frame.splitlines()
        data = "\n".join(line[5:].lstrip(" ") for line in lines if line.startswith("data:"))
        if data:
            event = next((line[6:].lstrip(" ") for line in lines if line.startswith("event:")), None)
            frames.append((event, json.loads(data)))
    return frames


def main():
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else "target/debug/nyro").resolve()
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    try:
        baseline = []
        for native in (None, False):
            with proxy(binary, upstream.server_port, native) as (port, *_):
                before = len(Upstream.calls)
                status, body, _, _ = request(port, "/v1/responses", REQUEST)
                assert status == 400 and len(Upstream.calls) == before, (native, status, body)
                baseline.append(json.loads(body)["error"])
        assert baseline[0] == baseline[1], baseline
        print("strict omitted/false: HTTP 400, no upstream calls", flush=True)
        # Intentionally RED for a binary predating Responses native_chat support.
        with proxy(binary, upstream.server_port, True) as (port, process, output, errors):
            observations = []
            before = len(Upstream.calls)
            assert request(port, "/v1/responses?api_key=client-secret", REQUEST, False)[0] == 401
            for patch in ({"store": True}, {"background": True}, {"previous_response_id": "resp_old"},
                          {"conversation": "conv_old"}, {"tools": [{"type": "web_search"}]}):
                status, body, _, _ = request(port, "/v1/responses", {**REQUEST, **patch})
                assert status == 400, (patch, status, body)
            assert len(Upstream.calls) == before
            for streaming in (False, True):
                for incomplete in (False, True):
                    terminal = copy.deepcopy(RESPONSE)
                    if incomplete:
                        terminal.update(status="incomplete", incomplete_details={"reason": "max_output_tokens"})
                    Upstream.reply = events(terminal) if streaming else terminal
                    payload = {**REQUEST, "stream": streaming}
                    if incomplete:
                        payload.update(store=None, background=False, previous_response_id=None, conversation=None)
                    before = len(Upstream.calls)
                    status, body, request_id, failure = request(port, "/v1/responses", payload)
                    assert status == 200 and failure is None, (streaming, incomplete, status, body, failure)
                    expected = copy.deepcopy(Upstream.reply)
                    if streaming:
                        assert b"[DONE]" not in body
                        for _, value in expected:
                            if "response" in value:
                                value["response"]["model"] = "public"
                        actual = sse(body)
                    else:
                        expected["model"] = "public"
                        actual = json.loads(body)
                    assert actual == expected, (actual, expected)
                    assert Upstream.calls[before:] == [(
                        "/v1/responses", "Bearer upstream-secret", {**payload, "model": "private-model", "store": False})]
                    observations.append(request_id)
            Upstream.reply = RESPONSE
            status, body, request_id, failure = request(port, "/v1/responses", {
                "model": "public", "input": "Hello", "store": False})
            assert status == 200 and failure is None, (status, body, failure)
            observations.append(request_id)
            for field in ("input_tokens", "output_tokens", "total_tokens"):
                Upstream.reply = copy.deepcopy(RESPONSE)
                del Upstream.reply["usage"][field]
                status, body, _, _ = request(port, "/v1/responses", REQUEST)
                assert status == 502, (field, status, body)
            for details, field, value in (("input_tokens_details", "cached_tokens", 11),
                                           ("output_tokens_details", "reasoning_tokens", 6)):
                Upstream.reply = copy.deepcopy(RESPONSE)
                Upstream.reply["usage"][details][field] = value
                status, body, _, _ = request(port, "/v1/responses", REQUEST)
                assert status == 502, (details, status, body)
            for case in ("sequence", "missing_sequence", "first_sequence", "identity", "event",
                         "missing_created", "missing_terminal", "trailing_event", "failure", "error"):
                Upstream.reply = copy.deepcopy(events(RESPONSE))
                if case == "sequence":
                    Upstream.reply[-1][1]["sequence_number"] += 1
                elif case == "missing_sequence":
                    del Upstream.reply[-1][1]["sequence_number"]
                elif case == "first_sequence":
                    Upstream.reply[0][1]["sequence_number"] = 1
                elif case == "identity":
                    Upstream.reply[-1][1]["response"]["id"] = "resp_other"
                elif case == "event":
                    Upstream.reply[-1] = ("response.incomplete", Upstream.reply[-1][1])
                elif case == "missing_created":
                    Upstream.reply.pop(0)
                elif case == "missing_terminal":
                    Upstream.reply.pop()
                elif case == "trailing_event":
                    Upstream.reply.append(("response.synthetic_extension", {
                        "type": "response.synthetic_extension", "sequence_number": 5}))
                elif case == "error":
                    Upstream.reply[-1] = ("error", {"type": "error", "sequence_number": 4,
                                                  "message": "upstream-secret-sensitive-error"})
                else:
                    Upstream.reply[-1] = ("response.failed", {
                        "type": "response.failed", "sequence_number": 4,
                        "response": {**RESPONSE, "status": "failed",
                                     "error": {"message": "upstream-secret-sensitive-error"}}})
                status, body, _, failure = request(port, "/v1/responses", {**REQUEST, "stream": True})
                assert status == 502 or (status == 200 and failure is not None), (case, status, body, failure)
                assert b"upstream-secret-sensitive-error" not in body, (case, body)
            process.terminate()
            process.wait(timeout=5)
            logs = re.sub(r"\x1b\[[0-9;]*m", "", output.read_text() + errors.read_text())
            assert process.returncode == 0, logs
            for request_id in observations:
                lines = [line for line in logs.splitlines() if request_id in line and "LLM request finished" in line]
                assert len(lines) == 1, (request_id, lines)
                for field, value in {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
                                     "quota_charged_tokens": 15}.items():
                    assert re.search(rf"\b{field}={value}\b", lines[0]), lines[0]
                assert 'usage_state="complete"' in lines[0], lines[0]
            assert "client-secret" not in logs and "upstream-secret" not in logs
    finally:
        upstream.shutdown()
        upstream.server_close()
    print("Responses native smoke passed: exact JSON/SSE, stateless requests, aliases, extensions, "
          "usage/quota, invalid responses/streams, strict defaults, header auth, SIGTERM")


if __name__ == "__main__":
    main()
