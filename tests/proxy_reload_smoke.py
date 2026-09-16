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
            if not self.release_stream.wait(40):
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
            headers = {"Content-Type": "application/json"}
            if key is not None:
                headers["Authorization"] = f"Bearer {key}"
            connection.request("POST" if payload is not None else "GET", path,
                               json.dumps(payload) if payload is not None else None, headers)
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

    def models(expected, key="client-secret-old"):
        calls_before = len(Upstream.calls)
        status, body = request("/v1/models", key=key)
        assert status == 200, body
        assert json.loads(body) == {"object": "list", "data": [
            {"id": name, "object": "model", "created": 0, "owned_by": "Nyro"}
            for name in expected]}
        assert len(Upstream.calls) == calls_before, "model discovery dispatched upstream"

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
                "public-retired": copy.deepcopy(model),
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
                original_models = ["public", "public-failover", "public-quota", "public-rate", "public-retired"]
                models(original_models)
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
                invalid["llm"]["models"]["never-published"] = copy.deepcopy(model)
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
                    models(original_models)
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
                updated["llm"]["models"]["public-added"] = updated["llm"]["models"].pop("public-retired")
                new_generation = reload_config(updated, "applied")
                assert new_generation > generation
                assert chat()[0] == 401
                assert request("/v1/models")[0] == 401
                updated_models = ["public", "public-added", "public-failover", "public-quota", "public-rate"]
                models(updated_models, "client-secret-new")
                models([], None)
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

                # Route/access edits preserve health while updating discovery grants and anonymous access.
                assert chat("public-failover", "client-secret-new")[0] == 200
                failures_before = sum(payload["model"] == "temporary-error" for _, payload in Upstream.calls)
                updated["llm"]["models"]["public"]["upstream_model"] = "internal-newer"
                updated["llm"]["models"]["public"]["allow_anonymous"] = True
                updated["security"]["api_keys"].append({"id": "limited", "secret": "limited-secret"})
                updated["llm"]["models"]["public-added"]["subjects"] = ["limited"]
                assert reload_config(updated, "applied") > new_generation
                models(["public", "public-failover", "public-quota", "public-rate"], "client-secret-new")
                models(["public"], None)
                models(["public", "public-added"], "limited-secret")
                assert chat("public-failover", "client-secret-new")[0] == 200
                assert sum(payload["model"] == "temporary-error" for _, payload in Upstream.calls) == failures_before
                # Lifecycle edits retain subject bindings and take effect for new
                # admissions; an admitted SSE body survives expiry and disabling.
                inactive = copy.deepcopy(updated)
                inactive["security"]["api_keys"][1]["enabled"] = False
                disabled_generation = reload_config(inactive, "applied")
                calls_before = len(Upstream.calls)
                assert request("/v1/models", key="limited-secret")[0] == 401
                assert chat("public-added", "limited-secret")[0] == 401
                assert chat("public", "limited-secret")[0] == 401  # No anonymous fallback.
                assert len(Upstream.calls) == calls_before
                assert reload_config(inactive, "unchanged") == disabled_generation
                invalid_lifecycle = copy.deepcopy(inactive)
                invalid_lifecycle["security"]["api_keys"][1]["enabled"] = "true"
                reload_config(invalid_lifecycle, "rejected", "invalid_config")
                assert chat("public-added", "limited-secret")[0] == 401

                expiring = copy.deepcopy(updated)
                expires_at = int(time.time()) + 10
                expiring["security"]["api_keys"][1]["expires_at"] = expires_at
                assert reload_config(expiring, "applied") > disabled_generation
                models(["public", "public-added"], "limited-secret")
                Upstream.stream_started.clear()
                Upstream.release_stream.clear()
                stream_connection, stream_response = connect("/v1/chat/completions", {
                    "model": "public-added", "messages": [{"role": "user", "content": "Hello"}],
                    "stream": True}, "limited-secret")
                assert stream_response.status == 200 and Upstream.stream_started.wait(2)
                first_event = b""
                while not first_event.endswith(b"\n\n"):
                    line = stream_response.readline()
                    assert line, "stream closed before expiry test"
                    first_event += line
                assert b"old-start" in first_event
                # Expiration is a wall-clock admission check, not a reload timer.
                expiry_deadline = time.monotonic() + 15
                while time.time() < expires_at:
                    assert time.monotonic() < expiry_deadline, "wall clock did not reach key expiry"
                    time.sleep(0.02)
                calls_before = len(Upstream.calls)
                assert request("/v1/models", key="limited-secret")[0] == 401
                assert chat("public-added", "limited-secret")[0] == 401
                assert len(Upstream.calls) == calls_before
                assert reload_config(expiring, "unchanged") is not None
                assert reload_config(updated, "applied") is not None  # Remove expiry; same binding.
                models(["public", "public-added"], "limited-secret")
                assert chat("public-added", "limited-secret")[0] == 200
                reload_config(inactive, "applied")
                assert chat("public-added", "limited-secret")[0] == 401
                Upstream.release_stream.set()
                remaining = stream_response.read()
                assert b"old-finish" in remaining and remaining.endswith(b"data: [DONE]\n\n")
                stream_connection.close()
                stream_connection = None
                reload_config(updated, "applied")

                # Subject windows cross model aliases and survive credential/lifecycle edits.
                # A distinct identity keeps this budget independent from earlier smoke requests.
                limited = copy.deepcopy(updated)
                limited["security"]["api_keys"].append({"id": "windowed", "secret": "window-secret-old"})
                limited["llm"]["subject_limits"] = {"windowed": {"rpm": 2, "rpd": 2}}
                limited["llm"]["models"]["public-added"]["subjects"].append("windowed")
                reload_config(limited, "applied")
                assert chat("public", "window-secret-old")[0] == 200
                rotated = copy.deepcopy(limited)
                rotated["security"]["api_keys"][-1]["secret"] = "window-secret-new"
                reload_config(rotated, "applied")
                assert chat("public", "window-secret-old")[0] == 401
                assert chat("public-added", "window-secret-new")[0] == 200

                def window_denied():
                    calls_before = len(Upstream.calls)
                    for name in ["public", "public-added"]:
                        connection, response = connect("/v1/chat/completions", {
                            "model": name, "messages": [{"role": "user", "content": "Hello"}]},
                            "window-secret-new")
                        try:
                            assert response.status == 429
                            assert 86340 < int(response.getheader("Retry-After")) <= 86400
                            assert json.loads(response.read())["error"]["code"] == "rate_limit_exceeded"
                        finally:
                            connection.close()
                    assert len(Upstream.calls) == calls_before

                window_denied()
                assert chat("public", "client-secret-new")[0] == 200
                assert chat("public", None)[0] == 200
                window_generation = reload_config(rotated, "unchanged")
                for field, value, reason in [("rpm", 3, "candidate_rejected"),
                                             ("rpd", 0, "invalid_config")]:
                    changed_window = copy.deepcopy(rotated)
                    changed_window["llm"]["subject_limits"]["windowed"][field] = value
                    reload_config(changed_window, "rejected", reason)
                    window_denied()
                assert reload_config(rotated, "unchanged") == window_generation
                disabled_window = copy.deepcopy(rotated)
                disabled_window["security"]["api_keys"][-1]["enabled"] = False
                reload_config(disabled_window, "applied")
                assert chat("public", "window-secret-new")[0] == 401
                reload_config(rotated, "applied")
                window_denied()
                # Remove subject, grants and rule, then add them back: history stays retained.
                reload_config(updated, "applied")
                assert chat("public", "window-secret-new")[0] == 401
                reload_config(rotated, "applied")
                window_denied()

                # A pending token reservation survives rotation and generation retirement;
                # a completed stream replaces it with actual usage before later admissions.
                token_config = copy.deepcopy(rotated)
                token_config["security"]["api_keys"].append({"id": "tokened", "secret": "token-secret-old"})
                token_config["llm"]["subject_limits"]["tokened"] = {
                    "tpm": 10, "tpd": 10, "reserve_tokens": 6}
                reload_config(token_config, "applied")
                Upstream.stream_started.clear()
                Upstream.release_stream.clear()
                stream_connection, stream_response = connect("/v1/chat/completions", {
                    "model": "public", "messages": [{"role": "user", "content": "Hello"}],
                    "stream": True}, "token-secret-old")
                assert stream_response.status == 200 and Upstream.stream_started.wait(2)
                first_event = b""
                while not first_event.endswith(b"\n\n"):
                    line = stream_response.readline()
                    assert line, "token window stream closed before first event"
                    first_event += line
                token_rotated = copy.deepcopy(token_config)
                token_rotated["security"]["api_keys"][-1]["secret"] = "token-secret-new"
                token_generation = reload_config(token_rotated, "applied")
                assert chat("public", "token-secret-old")[0] == 401

                def token_denied(pending):
                    calls_before = len(Upstream.calls)
                    connection, response = connect("/v1/chat/completions", {
                        "model": "public", "messages": [{"role": "user", "content": "Hello"}]},
                        "token-secret-new")
                    try:
                        assert response.status == 429
                        if pending:
                            assert response.getheader("Retry-After") is None
                        else:
                            assert 86340 < int(response.getheader("Retry-After")) <= 86400
                        assert json.loads(response.read())["error"]["code"] == "rate_limit_exceeded"
                    finally:
                        connection.close()
                    assert len(Upstream.calls) == calls_before

                token_denied(True)
                for field, value in [("tpm", 20), ("tpd", 20), ("reserve_tokens", 7)]:
                    changed_token = copy.deepcopy(token_rotated)
                    changed_token["llm"]["subject_limits"]["tokened"][field] = value
                    reload_config(changed_token, "rejected", "candidate_rejected")
                    token_denied(True)
                assert reload_config(token_rotated, "unchanged") == token_generation
                disabled_token = copy.deepcopy(token_rotated)
                disabled_token["security"]["api_keys"][-1]["enabled"] = False
                reload_config(disabled_token, "applied")
                assert chat("public", "token-secret-new")[0] == 401
                reload_config(token_rotated, "applied")
                token_denied(True)
                reload_config(rotated, "applied")  # Remove subject while its stream is in flight.
                reload_config(token_rotated, "applied")
                token_denied(True)
                Upstream.release_stream.set()
                remaining = stream_response.read()
                assert b"old-finish" in remaining and remaining.endswith(b"data: [DONE]\n\n")
                stream_connection.close()
                stream_connection = None
                # Actual usage is 2, so refunding the 6-unit reservation admits two more calls.
                assert chat("public", "token-secret-new")[0] == 200
                assert chat("public", "token-secret-new")[0] == 200
                token_denied(False)
                reload_config(rotated, "applied")
                reload_config(token_rotated, "applied")
                token_denied(False)

                # Routing history is shared by root generations, including an old live SSE.
                routed = copy.deepcopy(token_rotated)
                routed["llm"]["models"]["public-routing"] = {
                    "backends": [
                        {"id": "a", "provider": "mock", "upstream_model": "routing-a"},
                        {"id": "b", "provider": "mock", "upstream_model": "routing-b", "priority": 1}],
                    "workloads": ["chat"], "subjects": ["tester"]}
                reload_config(routed, "applied")
                Upstream.stream_started.clear()
                Upstream.release_stream.clear()
                stream_connection, stream_response = connect("/v1/chat/completions", {
                    "model": "public-routing", "messages": [{"role": "user", "content": "Hello"}],
                    "stream": True}, "client-secret-new")
                assert stream_response.status == 200 and Upstream.stream_started.wait(2)
                assert Upstream.calls[-1][1]["model"] == "routing-a"
                first_event = b""
                while not first_event.endswith(b"\n\n"):
                    line = stream_response.readline()
                    assert line, "routing stream closed before reload"
                    first_event += line

                def routed_chat(expected):
                    status, body = chat("public-routing", "client-secret-new")
                    assert status == 200, body
                    assert json.loads(body)["choices"][0]["message"]["content"] == expected, body

                routed["llm"]["models"]["public-routing"]["strategy"] = "least_recent"
                routed["llm"]["models"]["public-routing"]["backends"][1]["priority"] = 0
                routing_generation = reload_config(routed, "applied")
                routed_chat("routing-b")
                routed["llm"]["models"]["public-routing"]["backends"].reverse()
                assert reload_config(routed, "unchanged") == routing_generation
                invalid_strategy = copy.deepcopy(routed)
                invalid_strategy["llm"]["models"]["public-routing"]["strategy"] = "cooldown"
                reload_config(invalid_strategy, "rejected", "invalid_config")
                routed_chat("routing-a")
                routed["llm"]["models"]["public-routing"]["backends"][0]["upstream_model"] = "routing-c"
                reload_config(routed, "applied")
                routed_chat("routing-c")  # Changed binding starts without recent history.
                routed["llm"]["models"]["public-routing"]["strategy"] = "latency"
                reload_config(routed, "applied")
                assert chat("public-routing", "client-secret-new")[0] == 200
                Upstream.release_stream.set()
                remaining = stream_response.read()
                assert b"old-finish" in remaining and remaining.endswith(b"data: [DONE]\n\n")
                stream_connection.close()
                stream_connection = None

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
                discovery_logs = [line for line in logs().splitlines()
                                  if 'protocol="openai_models"' in line]
                assert len(discovery_logs) >= 10
                for line in discovery_logs:
                    assert 'workload="none"' in line and "attempts=0" in line, line
                    assert 'usage_state="not_attempted"' in line and "quota_charged_tokens=0" in line, line
                reload_logs = "\n".join(reload_events())
                for secret in ["client-secret-old", "client-secret-new", "upstream-secret-old",
                               "upstream-secret-new", "window-secret-old", "window-secret-new", "token-secret-old", "token-secret-new", "private-invalid-provider-marker",
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
          "auth/routes, model discovery, key disable/expiry/renewal, SSE retention, "
          "subject RPM/RPD and TPM/TPD retention, rate/quota/health reuse, routing strategies/history, redacted logs, FIFO rejection, SIGTERM")


if __name__ == "__main__":
    main()
