#!/usr/bin/env python3
"""Create template prerequisites through the owning Edge's formal HTTP APIs."""
import argparse
import json
import pathlib
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--fixture', default='/private/tmp/smartfactory-phase5-release-a01-20261005/frontend-fixture.json')
parser.add_argument('--output', required=True)
args = parser.parse_args()
fixture = json.loads(pathlib.Path(args.fixture).read_text())
password = pathlib.Path(fixture['credentials_file']).read_text().strip()
cloud = fixture.get('cloud_url', 'http://127.0.0.1:60047')
edge = fixture.get('node_a_url', 'http://127.0.0.1:60050')
records = []

def call(base, route, body=None, token='', record=True):
    payload = json.dumps(body, ensure_ascii=False).encode() if body is not None else None
    request = urllib.request.Request(base + '/api/sf/v1' + route, data=payload, headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    result = json.loads(raw)
    if record:
        records.append({'url': base + '/api/sf/v1' + route, 'method': 'GET' if body is None else 'POST', 'status': status, 'request': body, 'response': result, 'at_ms': int(time.time() * 1000)})
    if status != 200:
        raise RuntimeError(f'{route} returned HTTP {status}: {result}')
    return result

cloud_token = call(cloud, '/login', {'login': fixture.get('login', 'admin'), 'password': password}, record=False)['token']
edge_token = call(edge, '/login', {'login': fixture.get('login', 'admin'), 'password': password}, record=False)['token']
catalog = call(cloud, '/scene-templates', token=cloud_token)
existing_connectors = {value['configuration']['id']: value for value in call(edge, '/connector-configurations', token=edge_token)}
existing_entities = {value['id']: value for value in call(edge, '/entities', token=edge_token)}
connectors, devices = {}, []
for protocol in sorted({template['protocol'] for template in catalog}):
    connector_id = 'frontend-five-' + protocol
    identity = 'edge-a/' + connector_id
    actions = sorted({action for template in catalog if template['protocol'] == protocol for action in template['required_actions']})
    connection = {'host': '127.0.0.1', 'port': 1502} if protocol == 'modbus_tcp' else {'url': 'tcp://127.0.0.1:1883'} if protocol == 'mqtt_device' else {'url': 'opc.tcp://127.0.0.1:4840', 'security_mode': 'None', 'security_policy': 'None'}
    mappings = {}
    for index, action in enumerate(actions):
        mapping = {'param': 'value'}
        if protocol == 'modbus_tcp':
            mapping.update(type='write_single_coil', address=100 + index)
        elif protocol == 'mqtt_device':
            mapping.update(topic='frontend/command/' + action, template='{{.value}}')
        else:
            mapping.update(type='write', node_id='ns=2;s=' + action, data_type='bool')
        mappings[action] = mapping
    parameters = {'kind': 'connector', 'connector_id': connector_id, 'connection': connection, 'polling': {'interval_millis': 1000}, 'converter': {'action_mappings': mappings}}
    connectors[protocol] = existing_connectors.get(identity) or call(edge, '/connector-configurations', {'request_id': 'frontend-five-connector-' + protocol, 'expected_version': 0, 'group_id': 'factory', 'edge_id': 'edge-a', 'protocol': protocol, 'parameters': parameters}, edge_token)
for template in catalog:
    device_id = 'frontend-device-' + template['id']
    protocol = template['protocol']
    points = []
    for index, key in enumerate(template['required_keys']):
        point = {'key': key}
        if protocol == 'modbus_tcp':
            point.update(register_type='coil' if key == 'interlock' else 'holding_register', address=index * 2, data_type='bool' if key == 'interlock' else 'uint16')
        elif protocol == 'mqtt_device':
            point['source'] = key
        else:
            point['node_id'] = 'ns=2;s=' + key
        points.append(point)
    parameters = {'kind': 'device', 'device_id': device_id, 'device_name': '发布验证·' + template['name'], 'connector_id': 'frontend-five-' + protocol, 'datapoints': points}
    validated = call(edge, '/device-configurations/validate', {'protocol': protocol, 'device_id': device_id, 'parameters': parameters}, edge_token)
    if not validated['valid']:
        raise RuntimeError(f'device configuration validation failed: {validated["issues"]}')
    entity = existing_entities.get(device_id) or call(edge, '/entities', {'entity': {'id': device_id, 'name': '发布验证·' + template['name'], 'kind': 'device', 'parent_id': 'factory', 'edge_id': 'edge-a', 'status': 'approved', 'protocol': protocol, 'config': validated['config'], 'version': 1}, 'expected_version': 0}, edge_token)
    devices.append({'template': template, 'entity': entity, 'configuration': connectors[protocol]['configuration']})
deadline = time.monotonic() + 45
while True:
    cloud_entities = call(cloud, '/entities', token=cloud_token, record=False)
    cloud_connectors = call(cloud, '/connector-configurations', token=cloud_token, record=False)
    if all(any(value['id'] == item['entity']['id'] and value['version'] == item['entity']['version'] for value in cloud_entities) for item in devices) and all(any(value['configuration']['id'] == item['configuration']['id'] and value['configuration']['version'] == item['configuration']['version'] for value in cloud_connectors) for item in devices):
        break
    if time.monotonic() >= deadline:
        raise RuntimeError('formal owning-Edge metadata did not synchronize to the cloud within 45 seconds')
    time.sleep(0.5)
output = pathlib.Path(args.output)
output.write_text(json.dumps({'created_ms': int(time.time() * 1000), 'fixture': args.fixture, 'cloud_url': cloud, 'edge_url': edge, 'records': records, 'devices': devices, 'cloud_entities': cloud_entities, 'cloud_connectors': cloud_connectors}, ensure_ascii=False, indent=2))
print(json.dumps({'output': str(output), 'devices': len(devices), 'connectors': len(connectors), 'formal_requests': len(records), 'cloud_sync_verified': True}))
