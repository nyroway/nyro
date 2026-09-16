"""Local transport regression: cargo build -p nyro && python3 tests/proxy_network_smoke.py.

Uses only Python's standard library and the public test certificate/key in
fixtures/transport. No external network, vendor credentials or system CA edits.
"""
import base64
import copy
import http.client
import json
import os
from pathlib import Path
import re
import select
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

PROVIDER_KEY = "provider-network-secret"
CLIENT_KEY = "client-network-secret"
PROXY_AUTH = "Basic " + base64.b64encode(b"proxy-user:p@ss-network-secret").decode()
STREAM_STARTED = threading.Event()
STREAM_RELEASE = threading.Event()


class Server(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, handler, tls=None):
        super().__init__(("127.0.0.1", 0), handler)
        self.calls = []
        self.alpn = []
        self.tls_errors = []
        self.tls = tls
        self.allowed = set()
        self.fail = False
        self.thread = threading.Thread(target=self.serve_forever, daemon=True)
        self.thread.start()

    def get_request(self):
        connection, address = super().get_request()
        if self.tls:
            connection.settimeout(5)
            try:
                connection = self.tls.wrap_socket(connection, server_side=True)
                protocol = connection.selected_alpn_protocol()
                self.alpn.append(protocol)
                if protocol == "h2":
                    # The probe records ALPN; this fixture intentionally serves HTTP/1 only.
                    raise OSError("HTTP/2 observed")
            except (OSError, ssl.SSLError) as error:
                self.tls_errors.append(str(error))
                connection.close()
                raise
        return connection, address

    def close(self):
        self.shutdown()
        self.server_close()
        self.thread.join(timeout=5)

    def handle_error(self, *_):
        # Cancellation and intentionally rejected handshakes can close local sockets.
        pass


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def record(self, payload=None):
        self.server.calls.append((self.command, self.path, dict(self.headers), payload))

    def reply(self, status, body=b"", content_type="application/json"):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class Upstream(Handler):
    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.record(payload)
        if payload["model"] == "redirect":
            self.send_response(302)
            self.send_header("Location", "/redirect-target")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if payload["model"] == "stall":
            time.sleep(2)
        answer = {"id": "transport-test", "object": "chat.completion", "created": 1,
                  "model": payload["model"], "choices": [{"index": 0,
                  "message": {"role": "assistant", "content": "transport-ok"}, "finish_reason": "stop"}],
                  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
        if payload.get("stream"):
            answer["object"] = "chat.completion.chunk"
            answer["choices"][0] = {"index": 0, "delta": {"content": "transport-start"}, "finish_reason": None}
            first = f"data: {json.dumps(answer)}\n\n".encode()
            answer["choices"][0] = {"index": 0, "delta": {"content": "transport-finish"}, "finish_reason": "stop"}
            last = f"data: {json.dumps(answer)}\n\ndata: [DONE]\n\n".encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(first) + len(last)))
            self.end_headers()
            self.wfile.write(first)
            self.wfile.flush()
            STREAM_STARTED.set()
            if STREAM_RELEASE.wait(15):
                self.wfile.write(last)
        else:
            self.reply(200, json.dumps(answer).encode())

    def do_GET(self):
        self.record()
        self.reply(500)


class Proxy(Handler):
    def authorized(self):
        if self.headers.get("Proxy-Authorization") != PROXY_AUTH:
            self.reply(407)
            return False
        return True

    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        self.record(json.loads(body))
        if not self.authorized():
            return
        if self.server.fail:
            self.reply(503, b"private-proxy-error")
            return
        target = urlsplit(self.path)
        assert target.scheme == "http" and (target.hostname, target.port) in self.server.allowed
        connection = http.client.HTTPConnection(target.hostname, target.port, timeout=10)
        try:
            # A forwarding proxy owns removing its own authentication header.
            headers = {name: self.headers[name] for name in ["Authorization", "Content-Type"] if name in self.headers}
            connection.request("POST", target.path, body, headers)
            response = connection.getresponse()
            self.send_response(response.status)
            for name in ["Content-Type", "Content-Length", "Location"]:
                if response.getheader(name) is not None:
                    self.send_header(name, response.getheader(name))
            self.end_headers()
            while chunk := response.read1(4096):
                self.wfile.write(chunk)
                self.wfile.flush()
        finally:
            connection.close()

    def do_CONNECT(self):
        self.record()
        if not self.authorized():
            return
        host, port = self.path.rsplit(":", 1)
        assert (host, int(port)) in self.server.allowed
        with socket.create_connection((host, int(port)), timeout=5) as remote:
            self.wfile.write(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            self.wfile.flush()
            sockets = [self.connection, remote]
            while True:
                ready, _, _ = select.select(sockets, [], [], 10)
                if not ready:
                    return
                for source in ready:
                    data = source.recv(65536)
                    if not data:
                        return
                    (remote if source is self.connection else self.connection).sendall(data)


def main():
    if os.name != "posix":
        print("proxy network smoke skipped: SIGHUP requires Unix")
        return
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else "target/debug/nyro").resolve()
    fixtures = Path(__file__).resolve().parent / "fixtures" / "transport"
    def tls(protocols):
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(fixtures / "cert.pem", fixtures / "key.pem")
        context.set_alpn_protocols(protocols)
        return context
    plain = Server(Upstream)
    secure = Server(Upstream, tls(["h2", "http/1.1"]))
    proxy = Server(Proxy)
    second = Server(Proxy, tls(["http/1.1"]))
    trap = Server(Proxy)
    servers = [plain, secure, proxy, second, trap]
    for server in [proxy, second]:
        server.allowed = {("127.0.0.1", plain.server_port), ("127.0.0.1", secure.server_port)}
    def proxy_url(server, scheme="http"):
        return f"{scheme}://proxy-user:p%40ss-network-secret@127.0.0.1:{server.server_port}"
    try:
        with tempfile.TemporaryDirectory(prefix="nyro-transport-") as temporary:
            directory = Path(temporary)
            config_path = directory / "config.yaml"
            with socket.socket() as reservation:
                reservation.bind(("127.0.0.1", 0))
                port = reservation.getsockname()[1]
            data = {"server": {"listen": f"127.0.0.1:{port}", "request_timeout_ms": 5000},
                    "llm": {"providers": {"p": {"kind": "openai", "base_url": f"http://127.0.0.1:{plain.server_port}/v1", "api_key": PROVIDER_KEY}},
                            "models": {"public": {"provider": "p", "upstream_model": "normal", "workloads": ["chat"], "subjects": ["client"]}}},
                    "security": {"api_keys": [{"id": "client", "secret": CLIENT_KEY}]}}
            def replace(value):
                staged = config_path.with_suffix(".tmp")
                staged.write_text(json.dumps(value))
                staged.replace(config_path)
            def request(model="public", stream=False):
                connection = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
                connection.request("POST", "/v1/chat/completions", json.dumps({"model": model, "messages": [{"role": "user", "content": "Hello"}], "stream": stream}),
                                   {"Content-Type": "application/json", "Authorization": f"Bearer {CLIENT_KEY}", "Proxy-Authorization": "Basic caller-must-not-leak"})
                return connection, connection.getresponse()
            def call(status=200, model="public"):
                connection, response = request(model)
                try:
                    body = response.read()
                    assert response.status == status, (response.status, body, "TLS diagnostics", secure.alpn, secure.tls_errors, second.alpn, second.tls_errors)
                    if status == 200:
                        assert json.loads(body)["choices"][0]["message"]["content"] == "transport-ok"
                    return body
                finally:
                    connection.close()
            for bypass in ["", "*"]:
                replace(data)
                env = os.environ.copy()
                env.update({name: f"http://127.0.0.1:{trap.server_port}" for name in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"]})
                env.update({"NO_PROXY": bypass, "no_proxy": bypass, "SSL_CERT_FILE": str(fixtures / "cert.pem"), "RUST_LOG": "info"})
                logfile = directory / ("direct.log" if not bypass else "explicit.log")
                with logfile.open("w") as output:
                    process = subprocess.Popen([binary, "proxy", "--config", str(config_path)], stdout=output, stderr=output, env=env)
                    held = None
                    try:
                        deadline = time.monotonic() + 15
                        while True:
                            assert process.poll() is None, logfile.read_text()
                            try:
                                connection = http.client.HTTPConnection("127.0.0.1", port, timeout=1)
                                connection.request("GET", "/readyz")
                                response = connection.getresponse()
                                ready = response.status == 200
                                response.read()
                                connection.close()
                                if ready:
                                    break
                            except OSError:
                                pass
                            assert time.monotonic() < deadline, "readiness timeout"
                            time.sleep(.02)
                        def logs():
                            return re.sub(r"\x1b\[[0-9;]*m", "", logfile.read_text())
                        def reload(value, outcome="applied"):
                            count = logs().count("Configuration reload")
                            replace(value)
                            process.send_signal(signal.SIGHUP)
                            deadline = time.monotonic() + 10
                            while logs().count("Configuration reload") == count:
                                assert process.poll() is None, logs()
                                assert time.monotonic() < deadline, "reload timeout"
                                time.sleep(.02)
                            event = [line for line in logs().splitlines() if "Configuration reload" in line][-1]
                            assert f'outcome="{outcome}"' in event or f"outcome={outcome}" in event, event
                        call()
                        assert not trap.calls, "environment proxy was used"
                        if not bypass:
                            # CORS and legacy probe aliases remain deliberately disabled.
                            for path, status in [("/healthz", 200), ("/readyz", 200), ("/health", 404), ("/", 404)]:
                                connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
                                connection.request("GET", path, headers={"Origin": "https://browser.example"})
                                response = connection.getresponse()
                                assert response.status == status
                                assert response.getheader("Access-Control-Allow-Origin") is None
                                response.read()
                                connection.close()
                            continue
                        provider = data["llm"]["providers"]["p"]
                        provider["transport"] = {"proxy_url": proxy_url(proxy)}
                        reload(data)
                        call()
                        assert proxy.calls[-1][1] == f"http://127.0.0.1:{plain.server_port}/v1/chat/completions"
                        headers = {k.lower(): v for k, v in proxy.calls[-1][2].items()}
                        assert headers["proxy-authorization"] == PROXY_AUTH
                        assert headers["authorization"] == f"Bearer {PROVIDER_KEY}"
                        assert CLIENT_KEY not in str(proxy.calls)
                        bad = copy.deepcopy(data)
                        bad["llm"]["providers"]["p"]["transport"]["proxy_url"] = "http://proxy-user:private-invalid-secret@localhost/path"
                        reload(bad, "rejected")
                        before = len(proxy.calls)
                        call()
                        assert len(proxy.calls) == before + 1
                        # A failing explicit proxy must not silently send directly to this backend.
                        proxy.fail = True
                        before = len(plain.calls)
                        assert b"private-proxy-error" not in call(503)
                        assert len(plain.calls) == before
                        proxy.fail = False
                        # A TLS proxy for an HTTP origin exercises a different connection from CONNECT.
                        provider["transport"]["proxy_url"] = proxy_url(second, "https")
                        reload(data)
                        call()
                        assert second.calls[-1][0] == "POST"
                        assert second.alpn[-1] in [None, "http/1.1"]
                        # CONNECT uses proxy credentials; the inner TLS request uses only Provider credentials.
                        provider["base_url"] = f"https://127.0.0.1:{secure.server_port}/v1"
                        provider["transport"] = {"proxy_url": proxy_url(proxy), "http1_only": True}
                        reload(data)
                        call()
                        assert proxy.calls[-1][0:2] == ("CONNECT", f"127.0.0.1:{secure.server_port}")
                        headers = {k.lower(): v for k, v in proxy.calls[-1][2].items()}
                        assert headers["proxy-authorization"] == PROXY_AUTH and "authorization" not in headers
                        headers = {k.lower(): v for k, v in secure.calls[-1][2].items()}
                        assert headers["authorization"] == f"Bearer {PROVIDER_KEY}" and "proxy-authorization" not in headers
                        assert secure.alpn[-1] == "http/1.1"
                        # HTTP1 works without a proxy, while default TLS negotiation still offers HTTP2.
                        provider["transport"] = {"http1_only": True}
                        reload(data)
                        call()
                        assert secure.alpn[-1] == "http/1.1"
                        provider["transport"] = {}
                        reload(data)
                        call(502)
                        assert secure.alpn[-1] == "h2"
                        provider["base_url"] = f"http://127.0.0.1:{plain.server_port}/v1"
                        provider["transport"] = {"proxy_url": proxy_url(proxy)}
                        data["llm"]["models"]["public"]["upstream_model"] = "redirect"
                        reload(data)
                        before = len(plain.calls)
                        call(502)
                        assert len(plain.calls) == before + 1 and plain.calls[-1][0] == "POST"
                        data["llm"]["models"]["public"]["upstream_model"] = "stall"
                        data["server"]["request_timeout_ms"] = 200
                        reload(data)
                        call(504)
                        data["server"]["request_timeout_ms"] = 5000
                        data["llm"]["models"]["public"]["upstream_model"] = "normal"
                        data["llm"]["models"]["budget"] = {**data["llm"]["models"]["public"], "quota": {"total_tokens": 2, "reserve_tokens": 2}}
                        reload(data)
                        call(model="budget")
                        STREAM_STARTED.clear()
                        STREAM_RELEASE.clear()
                        held, response = request(stream=True)
                        assert response.status == 200 and STREAM_STARTED.wait(2)
                        first = b""
                        while not first.endswith(b"\n\n"):
                            line = response.readline()
                            assert line, "stream ended before reload"
                            first += line
                        before = len(second.calls)
                        provider["transport"] = {"proxy_url": proxy_url(second, "https"), "http1_only": True}
                        reload(data)
                        call()
                        assert len(second.calls) == before + 1
                        call(429, model="budget")  # Network binding changes do not reset quota.
                        STREAM_RELEASE.set()
                        rest = response.read()
                        assert b"transport-finish" in rest and rest.endswith(b"data: [DONE]\n\n")
                        held.close()
                        held = None
                        assert not trap.calls, "explicit proxy unexpectedly used the environment proxy"
                        for secret in [CLIENT_KEY, PROVIDER_KEY, "proxy-user", "p@ss-network-secret", "p%40ss-network-secret", PROXY_AUTH, "private-invalid-secret"]:
                            assert secret not in logs(), "transport credential leaked into process logs"
                    finally:
                        STREAM_RELEASE.set()
                        if held:
                            held.close()
                        process.terminate()
                        process.wait(timeout=10)
                        assert process.returncode == 0, logfile.read_text()
        print("proxy network smoke passed: direct/env isolation, HTTP/HTTPS proxies, CONNECT/auth, ALPN HTTP1, no redirects, deadline, reload/SSE/quota, CORS/probes, redaction")
    finally:
        STREAM_RELEASE.set()
        for server in servers:
            server.close()


if __name__ == "__main__":
    main()
