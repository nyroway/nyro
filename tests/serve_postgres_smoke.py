"""Exercise serve against a fresh local PostgreSQL DB, including a lost COMMIT reply.

Run after cargo build -p nyro:
  python3 tests/serve_postgres_smoke.py --postgres-url-file /private/path/test.url
The file must point to an empty, disposable loopback database with sslmode=disable.
The database is initialized and left intact for inspection; no existing DB is erased.
"""
import argparse
import os
from pathlib import Path
import socket
import socketserver
import struct
import subprocess
import tempfile
import threading
from urllib.parse import parse_qs, urlsplit, urlunsplit

import serve_smoke


def startup_rejections(root):
    token = root / 'admin.token'
    token.write_text(serve_smoke.ADMIN)
    source = root / 'invalid.url'
    pgpass = root / 'pgpass'
    pgpass.write_text('private-malformed-pgpass-secret\n')
    pgpass.chmod(0o600)
    env = os.environ.copy()
    env.pop('PGPASSWORD', None)
    env['PGPASSFILE'] = str(pgpass)
    env['RUST_LOG'] = 'sqlx=warn,nyro=info'
    for url in [
        'postgres://user:private-url-secret@localhost/control?sslmode=prefer',
        'postgres://user:private-url-secret@localhost/control?unknown=private-query-secret',
        'postgres://postgres@127.0.0.1:0/control?sslmode=disable',
    ]:
        source.write_text(url)
        result = subprocess.run(
            ['target/debug/nyro', 'serve', '--postgres-url-file', str(source),
             '--admin-token-file', str(token)], env=env, capture_output=True, timeout=10)
        assert result.returncode != 0
        output = result.stdout + result.stderr
        assert all(secret not in output for secret in [
            b'private-url-secret', b'private-query-secret', b'private-malformed-pgpass-secret'])


def read_exact(sock, count):
    data = bytearray()
    while len(data) < count:
        part = sock.recv(count - len(data))
        if not part:
            raise EOFError
        data.extend(part)
    return bytes(data)


class CommitAckDropProxy(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, target):
        self.target = target
        self.armed = threading.Event()
        self.dropped = threading.Event()
        self.errors = []
        super().__init__(('127.0.0.1', 0), Forward)

    def arm(self):
        self.armed.set()


class Forward(socketserver.BaseRequestHandler):
    def handle(self):
        with socket.create_connection(self.server.target, timeout=5) as upstream:
            upstream.settimeout(None)

            def upload():
                try:
                    while data := self.request.recv(65536):
                        upstream.sendall(data)
                except OSError:
                    pass
                finally:
                    try:
                        upstream.shutdown(socket.SHUT_WR)
                    except OSError:
                        pass

            sender = threading.Thread(target=upload, daemon=True)
            sender.start()
            updated = False
            try:
                # sslmode=disable: server messages use PostgreSQL's typed frame format.
                while True:
                    header = read_exact(upstream, 5)
                    length = struct.unpack('!I', header[1:])[0]
                    if not 4 <= length <= 8 * 1024 * 1024:
                        raise ValueError('invalid PostgreSQL test frame')
                    body = read_exact(upstream, length - 4)
                    if self.server.armed.is_set() and header[:1] == b'C':
                        if body.startswith(b'UPDATE '):
                            updated = True
                        elif updated and body == b'COMMIT\0':
                            # The server committed; withhold its acknowledgment from Nyro.
                            self.server.armed.clear()
                            self.server.dropped.set()
                            break
                    self.request.sendall(header + body)
            except (EOFError, OSError):
                pass
            except Exception as error:
                self.server.errors.append(type(error).__name__)
            finally:
                for sock in (upstream, self.request):
                    try:
                        sock.shutdown(socket.SHUT_RDWR)
                    except OSError:
                        pass
                sender.join(timeout=2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--postgres-url-file', required=True, type=Path)
    args = parser.parse_args()
    value = args.postgres_url_file.read_text().rstrip('\r\n')
    url = urlsplit(value)
    assert url.hostname in ('127.0.0.1', 'localhost', '::1'), 'use a disposable loopback database'
    assert parse_qs(url.query).get('sslmode') == ['disable'], 'the fault proxy requires explicit local plaintext'
    credentials = url.netloc.rsplit('@', 1)[0] + '@' if '@' in url.netloc else ''
    with CommitAckDropProxy((url.hostname, url.port or 5432)) as proxy:
        worker = threading.Thread(target=proxy.serve_forever, daemon=True)
        worker.start()
        try:
            with tempfile.TemporaryDirectory(prefix='nyro-pg-smoke-') as directory:
                startup_rejections(Path(directory))
                path = Path(directory) / 'postgres.url'
                path.write_text(urlunsplit(url._replace(netloc=f'{credentials}127.0.0.1:{proxy.server_address[1]}')))
                path.chmod(0o600)
                serve_smoke.main(path, proxy)
            assert proxy.dropped.is_set(), 'lost COMMIT path was not exercised'
            assert not proxy.errors, proxy.errors
        finally:
            proxy.shutdown()
            worker.join(timeout=2)


if __name__ == '__main__':
    main()
