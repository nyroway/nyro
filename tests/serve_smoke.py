"""Run after cargo build -p nyro; only local SQLite and loopback HTTP are used."""
import copy
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ADMIN = 'local-admin-test-secret'
CLIENT = 'local-client-test-secret'
STREAM_STARTED = threading.Event()
STREAM_RELEASE = threading.Event()


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        assert self.headers['Authorization'] == 'Bearer upstream-test-secret'
        answer = {'id': 'local-test', 'object': 'chat.completion', 'created': 1,
                  'model': body['model'], 'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': body['model']}, 'finish_reason': 'stop'}],
                  'usage': {'prompt_tokens': 1, 'completion_tokens': 1, 'total_tokens': 2}}
        if body.get('stream'):
            answer['object'] = 'chat.completion.chunk'
            answer['choices'][0] = {'index': 0, 'delta': {'content': body['model']}, 'finish_reason': None}
            first = f'data: {json.dumps(answer)}\n\n'.encode()
            answer['choices'][0] = {'index': 0, 'delta': {}, 'finish_reason': 'stop'}
            last = f'data: {json.dumps(answer)}\n\ndata: [DONE]\n\n'.encode()
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Content-Length', str(len(first) + len(last)))
            self.end_headers()
            self.wfile.write(first)
            self.wfile.flush()
            STREAM_STARTED.set()
            if STREAM_RELEASE.wait(15):
                self.wfile.write(last)
        else:
            encoded = json.dumps(answer).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)


def main():
    if os.name != 'posix':
        print('serve smoke skipped: process shutdown test requires Unix')
        return
    upstream = ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix='nyro-serve-') as directory:
            root = Path(directory)
            data_port, admin_port = port(), port()
            while admin_port == data_port:
                admin_port = port()
            config = {'server': {'listen': f'127.0.0.1:{data_port}'},
                      'llm': {'providers': {'p': {'kind': 'openai', 'base_url': f'http://127.0.0.1:{upstream.server_port}/v1', 'api_key': 'upstream-test-secret'}},
                              'models': {'public': {'provider': 'p', 'upstream_model': 'original', 'workloads': ['chat'], 'subjects': ['client'], 'quota': {'total_tokens': 100, 'reserve_tokens': 2}}}},
                      'security': {'api_keys': [{'id': 'client', 'secret': CLIENT}]}}
            seed = root / 'seed.yaml'
            seed.write_text(json.dumps(config))
            token = root / 'admin-token'
            token.write_text(ADMIN + '\n')
            command = [str(Path('target/debug/nyro').resolve()), 'serve', '--database', str(root / 'control.db'), '--admin-listen', f'127.0.0.1:{admin_port}', '--admin-token-file', str(token)]
            process = None
            def admin(method='GET', path='/admin/config', body=None, key=ADMIN, expected=200):
                connection = http.client.HTTPConnection('127.0.0.1', admin_port, timeout=15)
                headers = {'Content-Type': 'application/json'}
                if key:
                    headers['Authorization'] = f'Bearer {key}'
                connection.request(method, path, json.dumps(body) if body is not None else None, headers)
                response = connection.getresponse()
                raw = response.read()
                assert response.status == expected, (response.status, raw)
                assert response.getheader('Cache-Control') == 'no-store'
                connection.close()
                return json.loads(raw)
            def chat(want, stream=False):
                connection = http.client.HTTPConnection('127.0.0.1', data_port, timeout=15)
                connection.request('POST', '/v1/chat/completions', json.dumps({'model': 'public', 'messages': [{'role': 'user', 'content': 'hello'}], 'stream': stream}), {'Content-Type': 'application/json', 'Authorization': f'Bearer {CLIENT}'})
                response = connection.getresponse()
                assert response.status == 200, response.read()
                if stream:
                    return connection, response
                assert json.loads(response.read())['choices'][0]['message']['content'] == want
                connection.close()
            def save(value, revision, expected=200):
                return admin('PUT', body={'expected_revision': revision, 'config': value}, expected=expected)
            def publish(revision, expected=200):
                return admin('POST', '/admin/config/publish', {'revision': revision}, expected=expected)
            def start(initial=False):
                nonlocal process
                output = (root / 'process.log').open('a')
                env = os.environ.copy()
                env['RUST_LOG'] = 'nyro=info'
                process = subprocess.Popen(command + (['--config', str(seed)] if initial else []), stdout=output, stderr=output, env=env)
                output.close()
                deadline = time.monotonic() + 15
                while True:
                    assert process.poll() is None, (root / 'process.log').read_text()
                    try:
                        connection = http.client.HTTPConnection('127.0.0.1', data_port, timeout=1)
                        connection.request('GET', '/readyz')
                        response = connection.getresponse()
                        status = response.status
                        response.read()
                        connection.close()
                        if status == 200:
                            break
                    except OSError:
                        pass
                    assert time.monotonic() < deadline
                    time.sleep(.02)
            def stop():
                nonlocal process
                if process:
                    process.terminate()
                    process.wait(timeout=15)
                    assert process.returncode == 0, (root / 'process.log').read_text()
                    process = None
            try:
                start(True)
                assert admin()['active_revision'] == 1
                admin(key=None, expected=401)
                admin(key=CLIENT, expected=401)
                chat('original')
                edited = copy.deepcopy(config)
                edited['llm']['models']['public']['upstream_model'] = 'published'
                save(edited, 1)
                state = admin()
                assert state['draft']['revision'] == 2 and state['published_revision'] == state['active_revision'] == 1
                chat('original')
                save(edited, 1, 409)
                publish(2)
                chat('published')
                assert admin()['active_revision'] == 2
                publish(2)
                changed = copy.deepcopy(edited)
                changed['llm']['models']['public']['quota']['total_tokens'] = 200
                save(changed, 2)
                publish(3, 422)
                state = admin()
                assert state['published_revision'] == state['active_revision'] == 2
                chat('published')
                invalid = copy.deepcopy(edited)
                invalid['llm']['models']['public']['provider'] = 'missing'
                save(invalid, 3, 422)
                assert admin()['draft']['revision'] == 3
                stop()
                # Existing databases never silently reimport the seed.
                rejected = subprocess.run(command + ['--config', str(seed)], capture_output=True, timeout=5)
                assert rejected.returncode != 0 and b'already initialized' in rejected.stderr
                if os.name == 'posix':
                    fifo = root / 'startup.fifo'
                    os.mkfifo(fifo)
                    token_command = command.copy()
                    token_command[-1] = str(fifo)
                    for rejected_command in [token_command, command + ['--config', str(fifo)]]:
                        rejected = subprocess.run(rejected_command, capture_output=True, timeout=5)
                        assert rejected.returncode != 0 and b'regular file' in rejected.stderr
                seed.unlink()  # Recovery must come from the database, not the seed file.
                start()
                state = admin()
                assert state['draft']['revision'] == 3 and state['published_revision'] == state['active_revision'] == 2
                chat('published')
                held, response = chat(None, stream=True)
                assert STREAM_STARTED.wait(3)
                first = b''
                while not first.endswith(b'\n\n'):
                    line = response.readline()
                    assert line
                    first += line
                edited['llm']['models']['public']['upstream_model'] = 'after-stream'
                save(edited, 3)
                publish(4)
                chat('after-stream')
                STREAM_RELEASE.set()
                assert response.read().endswith(b'data: [DONE]\n\n')
                held.close()
                assert b'published' in first
                stop()
                for secret in [ADMIN, CLIENT, 'upstream-test-secret']:
                    assert secret not in (root / 'process.log').read_text()
            finally:
                STREAM_RELEASE.set()
                if process:
                    process.terminate()
                    process.wait(timeout=15)
    finally:
        upstream.shutdown()
        upstream.server_close()
    print('serve smoke passed: auth, SQLite drafts/publication, conflicts/rejection, restart, SSE retention, redaction')


if __name__ == '__main__':
    main()
