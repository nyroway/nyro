"""Recorded process replay: cargo build -p nyro && python3 tests/proxy_native_replay.py.

Uses only local HTTP and the stdlib. Compare JSON/SSE data, not wire whitespace
or arbitrary response headers. Strict-mode failures are classified, not hidden.
"""

import base64
import copy
import http.client
import json
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time


FIXTURES = Path(__file__).resolve().parent / "e2e" / "fixtures"


def sse_data(body):
    events = []
    for frame in body.decode().replace("\r\n", "\n").split("\n\n"):
        lines = [line[5:].removeprefix(" ") for line in frame.splitlines()
                 if line.startswith("data:")]
        if lines:
            data = "\n".join(lines)
            events.append(data if data == "[DONE]" else json.loads(data))
    return events


class Upstream(BaseHTTPRequestHandler):
    records = {}
    calls = []

    def log_message(self, *_):
        pass

    def do_POST(self):
        alias = self.path.split("/")[1]
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.calls.append((alias, self.path, payload))
        response = self.records[alias]["response"]
        self.send_response(response["status"])
        self.send_header("Content-Type", response["headers"]["content-type"])
        self.end_headers()
        # HTTP/1.0 closes the body; recorded hop-by-hop/length headers are invalid here.
        try:
            body = base64.b64decode(response["body_base64"])
            if "text/event-stream" in response["headers"]["content-type"]:
                # Deliver the first event separately so later codec failures are
                # observable after headers, rather than hidden by HTTP buffering.
                first, separator, rest = body.partition(b"\n\n")
                self.wfile.write(first + separator)
                self.wfile.flush()
                time.sleep(0.05)
                body = rest
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass  # Strict codecs can reject a response before consuming its body.


def request(port, path="/readyz", payload=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    body = bytearray()
    try:
        connection.request("GET" if payload is None else "POST", path,
                           None if payload is None else json.dumps(payload),
                           {"Content-Type": "application/json"})
        try:
            response = connection.getresponse()
        except (http.client.HTTPException, OSError) as error:
            return None, b"", type(error).__name__
        failure = None
        try:
            while chunk := response.read1(65536):
                body.extend(chunk)
        except (http.client.HTTPException, OSError) as error:
            if isinstance(error, http.client.IncompleteRead):
                body.extend(error.partial)
            failure = type(error).__name__
        return response.status, bytes(body), failure
    finally:
        connection.close()


@contextmanager
def proxy(binary, records, upstream_port, native):
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    providers, models = {}, {}
    for alias, record in records.items():
        provider = {"kind": "openai" if record["protocol"] == "openai-chat" else "anthropic",
                    "base_url": f"http://127.0.0.1:{upstream_port}/{alias}/v1"}
        if native is not None:
            provider["native_chat"] = native
        providers[alias] = provider
        models[alias] = {"provider": alias,
                         "upstream_model": record["request"]["body_json"]["model"],
                         "workloads": ["chat"], "allow_anonymous": True}
    with tempfile.TemporaryDirectory(prefix="nyro-native-replay-") as directory:
        config = Path(directory) / "config.yaml"
        config.write_text(json.dumps({"server": {"listen": f"127.0.0.1:{port}"},
                                      "llm": {"providers": providers, "models": models}}))
        output, errors = Path(directory) / "stdout.log", Path(directory) / "stderr.log"
        with output.open("wb") as stdout, errors.open("wb") as stderr:
            process = subprocess.Popen([binary, "proxy", "--config", config],
                                       stdout=stdout, stderr=stderr)
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
            yield port
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


def replay(port, alias, record):
    payload = copy.deepcopy(record["request"]["body_json"])
    payload["model"] = alias
    openai = record["protocol"] == "openai-chat"
    if openai and payload.get("stream"):
        payload.setdefault("stream_options", {})["include_usage"] = True
    path = "/v1/chat/completions" if openai else "/v1/messages"
    before = len(Upstream.calls)
    status, body, transport = request(port, path, payload)
    calls = Upstream.calls[before:]
    parsed = sse_data(body) if status == 200 and payload.get("stream") else (json.loads(body) if body else {})
    if isinstance(parsed, list):
        complete = bool(parsed) and (parsed[-1] == "[DONE]" if openai
                                    else parsed[-1].get("type") == "message_stop")
        error = next((event.get("error", {}) for event in parsed
                      if isinstance(event, dict) and "error" in event), {})
    else:
        error = parsed.get("error", {})
        complete = status == 200 and not error
    summary = (status, complete, transport, error.get("code") or error.get("type"),
               len(parsed) if isinstance(parsed, list) else int(bool(body)), len(calls))
    return summary, payload, parsed, calls


def main():
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else "target/debug/nyro").resolve()
    records = {}
    for path in sorted(FIXTURES.rglob("*.jsonl")):
        for line in path.read_text().splitlines():
            record = json.loads(line)
            alias = record["replay_model"]
            assert alias not in records, f"duplicate fixture: {alias}"
            records[alias] = record
    assert len(records) == 16, f"expected 16 recorded fixtures, found {len(records)}"
    native_records = {alias: r for alias, r in records.items() if r["protocol"] == "openai-chat"}
    assert len(native_records) == 8
    assert {r["protocol"] for r in records.values()} == {"openai-chat", "anthropic-messages"}
    Upstream.records = records
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    baseline = {}
    try:
        with proxy(binary, records, upstream.server_port, None) as port:
            for alias, record in records.items():
                summary, _, _, _ = replay(port, alias, record)
                baseline[alias] = summary
                status, complete, transport, code, events, calls = summary
                print(f"strict {alias}: HTTP {status}; complete={complete}; "
                      f"data={events}; upstream={calls}; error={code}; transport={transport}", flush=True)
        assert all(not result[1] for result in baseline.values()), baseline
        assert all(baseline[alias][0] == 502 for alias in native_records), baseline
        # This launch is intentionally RED on binaries predating native_chat.
        with proxy(binary, native_records, upstream.server_port, True) as port:
            for alias, record in native_records.items():
                summary, payload, actual, calls = replay(port, alias, record)
                assert summary[:3] == (200, True, None), (alias, summary, actual)
                expected_request = copy.deepcopy(payload)
                expected_request["model"] = record["request"]["body_json"]["model"]
                assert calls == [(alias, f"/{alias}/v1/chat/completions", expected_request)], alias
                body = base64.b64decode(record["response"]["body_base64"])
                expected = sse_data(body) if payload.get("stream") else json.loads(body)
                for value in expected if isinstance(expected, list) else [expected]:
                    if isinstance(value, dict) and "model" in value:
                        value["model"] = alias
                assert actual == expected, f"native JSON/SSE data mismatch: {alias}"
                print(f"native {alias}: PASS ({summary[4]} data values)", flush=True)
        with proxy(binary, records, upstream.server_port, False) as port:
            for alias, record in records.items():
                summary, _, _, _ = replay(port, alias, record)
                assert summary == baseline[alias], (alias, baseline[alias], summary)
    finally:
        upstream.shutdown()
        upstream.server_close()
    print("native replay passed: 16 strict classifications; 8 exact native replays; "
          "native_chat=false matches omitted")


if __name__ == "__main__":
    main()
