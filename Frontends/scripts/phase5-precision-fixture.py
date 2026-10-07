#!/usr/bin/env python3
"""Use the handed-off production nodes to create exact numeric observations."""
import argparse
import json
import pathlib
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--fixture', default='/private/tmp/smartfactory-release-restore-recovery-20261005/frontend-fixture.json')
parser.add_argument('--device', default='frontend-device-goods-counting')
parser.add_argument('--directory', required=True)
args = parser.parse_args()
fixture = json.loads(pathlib.Path(args.fixture).read_text())
credential = json.loads(pathlib.Path(fixture['credentials_file']).read_text()) if 'credentials_file' in fixture else {'login': fixture.get('login', 'admin'), 'password': pathlib.Path(fixture['password_file']).read_text().strip()}
out = pathlib.Path(args.directory)
out.mkdir(parents=True, exist_ok=True)
for kind, url in [('cloud', fixture['cloud_url']), ('edge', fixture.get('node_urls', {}).get('a') or fixture['edge_urls']['edge-a'])]:
    def call(resource, value=None, token=''):
        body = None if value is None else json.dumps(value, ensure_ascii=False).encode()
        request = urllib.request.Request(url + '/api/sf/v1' + resource, data=body, headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, json.load(response)
    _, auth = call('/login', credential)
    _, entities = call('/entities', token=auth['token'])
    assert any(entity['id'] == args.device for entity in entities), 'The selected formal resource does not exist'
    at = int(time.time() * 1000) - 1000
    identity = 'phase5-precision-' + kind + '-' + str(at)
    values = [('precise_signed', -9007199254740993), ('precise_unsigned', 18446744073709551615), ('precise_integer', 9007199254740993), ('precise_float', 12.345678901234567), ('precise_string', '9007199254740993'), ('precise_boolean', True), ('precise_aggregate', {'average': 9007199254740993, 'count': 2, 'minimum': 9007199254740992, 'maximum': 9007199254740994}), ('precise_bad', 99)]
    points = [{'id': identity + '-' + key, 'message_id': identity, 'source_id': 'precision-source', 'source_sequence': at + index, 'device_id': args.device, 'key': key, 'unit': 'count', 'value': value, 'observed_ms': at + index, 'received_ms': at + index, 'revision': 1, 'quality': 'BAD' if key == 'precise_bad' else 'GOOD', 'time_source': 'device'} for index, (key, value) in enumerate(values)]
    payload = {'message_id': identity, 'source_id': 'precision-source', 'payload_hash': '', 'critical': False, 'points': points}
    status, result = call('/ingest', payload, auth['token'])
    target = out / (kind + '.state.json')
    target.write_text(json.dumps({'kind': kind, 'url': url, 'device': args.device, 'fixture': args.fixture, 'points': points, 'status': status, 'result': result}, ensure_ascii=False, indent=2))
    print(json.dumps({'kind': kind, 'status': status, 'point_count': len(points), 'evidence': str(target)}))
