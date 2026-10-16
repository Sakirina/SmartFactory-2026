#!/usr/bin/env python3
"""Start one isolated server, or seed exact values over its formal HTTP API."""
import datetime
import hashlib
import json
import os
import pathlib
import secrets
import socket
import sys
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
BASE = pathlib.Path('/private/tmp/smartfactory-phase4-independent-browser-20261005')
BIN = pathlib.Path('/private/tmp/smartfactory-phase4-independent-a02-20261005/bin')
OUT = ROOT / '.local/evolution/phase4-review-browser'
STATIC = BASE / 'frontend/Frontends/dist'
PORTS = {'business-cloud': 19510, 'business-edge': 19511, 'ai': 19512, 'precision-cloud': 19513, 'precision-edge': 19514}
DIRECTORIES = {'business-cloud': 'cloud-final', 'business-edge': 'edge-final', 'ai': 'ai-final', 'precision-cloud': 'cloud-precision', 'precision-edge': 'edge-precision'}
BASE.mkdir(exist_ok=True)
OUT.mkdir(exist_ok=True)

def write(path, value, private=False):
    path = pathlib.Path(path)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    if private:
        path.chmod(0o600)

def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()

def start(kind):
    port = PORTS[kind]
    directory = BASE / DIRECTORIES[kind]
    if directory.exists():
        raise RuntimeError('Select a new isolated state directory instead of replacing existing evidence: ' + str(directory))
    probe = socket.socket()
    probe.bind(('127.0.0.1', port))
    probe.close()
    environment = {key: value for key, value in os.environ.items() if not key.startswith(('SF_', 'DT_', 'OTEL_'))}
    environment['GOMAXPROCS'] = '2'
    if kind.startswith('business-'):
        command = [str(BIN / 'sf-business-fixture'), '-dir', str(directory), '-mode', kind.split('-')[1], '-address', '127.0.0.1:' + str(port), '-static', str(STATIC), '-duration', '2h']
    elif kind == 'ai':
        command = [str(BIN / 'sf-ai-evidence-fixture'), '-dir', str(directory), '-address', '127.0.0.1:' + str(port), '-model-api', 'responses', '-model-stream=true', '-static', str(STATIC), '-duration', '2h']
    else:
        mode = kind.split('-')[1]
        directory.mkdir(mode=0o700)
        password = secrets.token_hex(24)
        write(directory / 'private-credentials.json', {'login': 'admin', 'password': password}, private=True)
        environment['SF_BOOTSTRAP_PASSWORD'] = password
        command = [str(BIN / ('sf-' + mode)), '-listen', '127.0.0.1:' + str(port), '-node-id', 'independent-precision-' + mode, '-database', str(directory / 'app.db'), '-master-key', str(directory / 'master.key'), '-static', str(STATIC), '-seed']
    metadata = {'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'kind': kind, 'pid': os.getpid(), 'port': port, 'command': command, 'binary_sha256': sha(command[0]), 'directory': str(directory), 'credentials': str(directory / 'private-credentials.json'), 'credentials_values_in_report': False, 'static': str(STATIC), 'bootstrap_password': 'private process environment only' if kind.startswith('precision-') else 'generated privately by fixture'}
    write(OUT / (kind + '.server.json'), metadata)
    print(json.dumps({'kind': kind, 'pid': os.getpid(), 'port': port, 'directory': str(directory)}, ensure_ascii=False), flush=True)
    os.execve(command[0], command, environment)

def request(port, resource, body, token=''):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    raw = json.dumps(body, ensure_ascii=False, separators=(',', ':')).encode()
    call = urllib.request.Request('http://127.0.0.1:' + str(port) + '/api/sf/v1' + resource, data=raw, headers=headers, method='POST')
    with urllib.request.urlopen(call, timeout=15) as response:
        return response.status, json.loads(response.read())

def seed():
    reports = []
    cases = [('precise_signed', -9007199254740993), ('precise_unsigned', 18446744073709551615), ('precise_integer', 9007199254740993), ('precise_float', 12.345678901234567), ('precise_string', '9007199254740993'), ('precise_boolean', True), ('precise_aggregate', {'average': 9007199254740993, 'count': 2, 'minimum': 9007199254740992, 'maximum': 9007199254740994}), ('precise_bad', 99)]
    for kind in ['precision-cloud', 'precision-edge']:
        port, directory = PORTS[kind], BASE / DIRECTORIES[kind]
        credentials = json.loads((directory / 'private-credentials.json').read_text())
        status, authentication = request(port, '/login', credentials)
        if status != 200:
            raise RuntimeError('Fixture login failed')
        now = int(time.time() * 1000) - 2000
        message = 'independent-' + kind + '-exact-values'
        points = [{'id': kind + '-' + key, 'message_id': message, 'source_id': 'precision-source', 'source_sequence': now + index, 'device_id': 'agv-1', 'key': key, 'unit': 'count', 'value': value, 'observed_ms': now + index, 'received_ms': now + index, 'revision': 1, 'quality': 'BAD' if key == 'precise_bad' else 'GOOD', 'time_source': 'device'} for index, (key, value) in enumerate(cases)]
        status, result = request(port, '/ingest', {'message_id': message, 'source_id': 'precision-source', 'payload_hash': '', 'critical': False, 'points': points}, authentication['token'])
        if status != 200 or result.get('count') != 8 or not result.get('committed'):
            raise RuntimeError('Exact-value ingest did not commit all eight points')
        item = {'kind': kind.split('-')[1], 'port': port, 'device': 'agv-1', 'points': points, 'status': status, 'result': result}
        write(directory / 'precision-manifest.json', item)
        reports.append(item)
    write(OUT / 'precision-seed-http.json', reports)
    print(json.dumps({'services': len(reports), 'points_per_service': 8, 'http_statuses': [item['status'] for item in reports]}))

if __name__ == '__main__':
    if sys.argv[1] == 'seed':
        seed()
    else:
        start(sys.argv[1])
