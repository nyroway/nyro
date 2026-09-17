"""Run after cargo build -p nyro; local SQLite by default; serve_postgres_smoke.py reuses this suite for PostgreSQL."""
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
        assert self.headers['Authorization'] == f'Bearer {self.server.expected_key}'
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


def main(postgres_url_file=None, publication_fault=None):
    if os.name != 'posix':
        print('serve smoke skipped: process shutdown test requires Unix')
        return
    upstream = ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
    upstream.expected_key = 'upstream-test-secret'
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
            database_args = ['--postgres-url-file', str(postgres_url_file)] if postgres_url_file else ['--database', str(root / 'control.db')]
            command = [str(Path('target/debug/nyro').resolve()), 'serve', *database_args, '--admin-listen', f'127.0.0.1:{admin_port}', '--admin-token-file', str(token)]
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
            def chat(want, stream=False, key=CLIENT, expected=200):
                connection = http.client.HTTPConnection('127.0.0.1', data_port, timeout=15)
                connection.request('POST', '/v1/chat/completions', json.dumps({'model': 'public', 'messages': [{'role': 'user', 'content': 'hello'}], 'stream': stream}), {'Content-Type': 'application/json', 'Authorization': f'Bearer {key}'})
                response = connection.getresponse()
                assert response.status == expected, response.read()
                if expected != 200:
                    response.read()
                    connection.close()
                    return
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
                competing = subprocess.run(command, capture_output=True, timeout=10)
                assert competing.returncode != 0 and b'configuration storage failed' in competing.stderr
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
                # Entity edits share the same draft revision and explicit publication path.
                def edit(method, path, value=None, entity_id=None):
                    revision = admin()['draft']['revision']
                    body = {'expected_revision': revision}
                    if value is not None:
                        body['value'] = value
                    if entity_id is not None:
                        body['id'] = entity_id
                    result = admin(method, path, body, expected=201 if method == 'POST' else 200)
                    assert result['draft_revision'] == revision + 1
                edit('POST', '/admin/providers', {'kind': 'openai', 'base_url': config['llm']['providers']['p']['base_url']}, 'secondary')
                edit('POST', '/admin/models', {'provider': 'secondary', 'upstream_model': 'temporary', 'workloads': ['chat'], 'allow_anonymous': True}, 'temporary')
                revision = admin()['draft']['revision']
                admin('DELETE', '/admin/providers/secondary', {'expected_revision': revision}, expected=409)
                edit('DELETE', '/admin/models/temporary')
                edit('DELETE', '/admin/providers/secondary')
                edit('POST', '/admin/api-keys', {'secret': {'action': 'set', 'value': 'extra-client-secret'}}, 'extra')
                edit('DELETE', '/admin/api-keys/extra')
                admin(path='/admin/api-keys/extra', expected=404)
                STREAM_STARTED.clear()
                STREAM_RELEASE.clear()
                held, response = chat(None, stream=True)
                assert STREAM_STARTED.wait(3)
                first = b''
                while not first.endswith(b'\n\n'):
                    line = response.readline()
                    assert line
                    first += line
                replacement = {'kind': 'openai', 'base_url': config['llm']['providers']['p']['base_url'], 'api_key': {'action': 'set', 'value': 'rotated-upstream-secret'}}
                edit('PUT', '/admin/providers/p', replacement)
                edit('PUT', '/admin/api-keys/client', {'secret': {'action': 'set', 'value': 'rotated-client-secret'}})
                chat('after-stream')  # Draft rotation has not changed active credentials.
                for path in ['/admin/config', '/admin/providers', '/admin/providers/p', '/admin/api-keys', '/admin/api-keys/client']:
                    view = json.dumps(admin(path=path))
                    for secret in [CLIENT, 'upstream-test-secret', 'rotated-upstream-secret', 'rotated-client-secret']:
                        assert secret not in view
                exported = admin(path='/admin/config/export')
                assert exported['draft']['config']['llm']['providers']['p']['api_key'] == 'rotated-upstream-secret'
                revision = admin()['draft']['revision']
                admin('DELETE', '/admin/api-keys/client', {'expected_revision': revision}, expected=409)
                upstream.expected_key = 'rotated-upstream-secret'
                publish(revision)
                chat('after-stream', key='rotated-client-secret')
                chat(None, expected=401)
                admin(key='rotated-client-secret', expected=401)
                STREAM_RELEASE.set()
                assert response.read().endswith(b'data: [DONE]\n\n')
                held.close()
                stop()
                start()
                chat('after-stream', key='rotated-client-secret')
                chat(None, expected=401)
                if publication_fault:
                    exported = admin(path='/admin/config/export')
                    changed = exported['draft']['config']
                    changed['llm']['models']['public']['backends'][0]['upstream_model'] = 'recovered-after-lost-commit'
                    revision = save(changed, exported['draft']['revision'])['draft_revision']
                    publication_fault.arm()
                    outcome = publish(revision, expected=503)
                    assert publication_fault.dropped.wait(3), 'the database COMMIT reply was not intercepted'
                    assert outcome['error']['code'] == 'storage_outcome_unknown'
                    # A committed target is not activated without a confirmed write outcome.
                    chat('after-stream', key='rotated-client-secret')
                    assert admin(expected=503)['error']['code'] == 'storage_failed'
                    stop()
                    start()
                    state = admin()
                    assert state['published_revision'] == state['active_revision'] == revision
                    chat('recovered-after-lost-commit', key='rotated-client-secret')
                stop()
                for secret in [ADMIN, CLIENT, 'upstream-test-secret', 'rotated-client-secret', 'rotated-upstream-secret', 'extra-client-secret']:
                    assert secret not in (root / 'process.log').read_text()
            finally:
                STREAM_RELEASE.set()
                if process:
                    process.terminate()
                    process.wait(timeout=15)
    finally:
        upstream.shutdown()
        upstream.server_close()
    print(f'serve smoke passed ({"PostgreSQL" if postgres_url_file else "SQLite"}): auth, ownership, drafts/publication, conflicts/rejection, restart, SSE retention, entity CRUD/credential rotation, redaction' + (', lost COMMIT recovery' if publication_fault else ''))


if __name__ == '__main__':
    main()
