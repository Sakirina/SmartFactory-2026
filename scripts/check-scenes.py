#!/usr/bin/env python3
"""Verify simulated device changes through real protocols and both business APIs."""
import argparse
import json
from pathlib import Path
import time
from urllib.parse import urlencode
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, default=ROOT / '.local/evidence/five-scenes.json')
    parser.add_argument('--pulses', type=int, default=100)
    parser.add_argument('--state-directory', type=Path, default=ROOT / '.local')
    parser.add_argument('--simulator-port', type=int, default=18083)
    args = parser.parse_args()
    if not 1 <= args.pulses <= 10000:
        parser.error('pulses must be 1..10000')
    private = json.loads((args.state_directory / 'development-credentials.json').read_text())
    def request(port, path, value=None, token=''):
        data = None if value is None else json.dumps(value).encode()
        headers = {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token}
        with urlopen(Request(f'http://127.0.0.1:{port}' + path, data=data, headers=headers), timeout=15) as response:
            return json.load(response)
    tokens = {}
    for name, port in [('cloud', 8090), ('edge', 8091)]:
        tokens[name] = request(port, '/api/sf/v1/login', {'login': 'admin', 'password': private['password']})['token']
    def wait(check, label, timeout=15):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            result = check()
            if result:
                return result
            time.sleep(0.15)
        raise TimeoutError(label)
    def set_values(device, values):
        return request(args.simulator_port, '/state', {'device_id': device, 'values': values})
    def state(device, key):
        return request(args.simulator_port, '/state')['devices'][device][key]
    report = {'check': 'five_scene_protocol_control_and_cloud_edge_observation', 'storage': 'running deployment determines persistence; memory demo is supported', 'started_ms': int(time.time()*1000), 'steps': []}
    def observed(device, key, expected, since):
        evidence = {}
        for name, port in [('cloud', 8090), ('edge', 8091)]:
            query = urlencode({'device_ids': device, 'keys': key, 'from_ms': since, 'to_ms': int(time.time()*1000), 'limit': 200})
            result = request(port, '/api/sf/v1/data?' + query, token=tokens[name])
            point = next((p for p in result['points'] if p['value'] == expected and p['quality'] == 'GOOD'), None)
            if not point:
                return False
            evidence[name] = {'value': point['value'], 'observed_ms': point['observed_ms'], 'quality': point['quality'], 'entity_revision': point['entity_revision'], 'source_id': point['source_id']}
        return evidence
    def transition(label, device, inputs, output, expected, timeout=15):
        since = int(time.time()*1000)
        set_values(device, inputs)
        wait(lambda: state(device, output) == expected, label + ' device response', timeout)
        evidence = wait(lambda: observed(device, output, expected, since), label + ' cloud/edge feedback', timeout)
        report['steps'].append({'label': label, 'device': device, 'input': inputs, 'output': output, 'expected': expected, 'feedback': evidence, 'elapsed_ms': int(time.time()*1000)-since})
        print(label + ': passed', flush=True)
    try:
        for label, inputs, output, expected in [
            ('temperature-high', {'temperature': 34}, 'fan', True),
            ('temperature-recovered', {'temperature': 26}, 'fan', False),
            ('temperature-low', {'temperature': 15}, 'heater', True),
            ('low-temperature-recovered', {'temperature': 22}, 'heater', False),
            ('humidity-high', {'humidity': 80}, 'dehumidifier', True),
            ('high-humidity-recovered', {'humidity': 60}, 'dehumidifier', False),
            ('humidity-low', {'humidity': 20}, 'humidifier', True),
            ('low-humidity-recovered', {'humidity': 45}, 'humidifier', False)]:
            transition(label, 'climate-1', inputs, output, expected)
        transition('presence-lights-on', 'light-1', {'presence': True}, 'light', True)
        transition('absence-delayed-lights-off', 'light-1', {'presence': False}, 'light', False)
        if report['steps'][-1]['elapsed_ms'] < 1800:
            raise AssertionError('absence delay shorter than the configured 2 seconds')
        for gas, value in [('smoke', 10), ('combustible', 40), ('co', 60)]:
            set_values('gas-1', {'smoke': 0, 'combustible': 0, 'co': 0, 'extractor': False})
            time.sleep(0.6)
            transition(gas + '-extraction', 'gas-1', {gas: value}, 'extractor', True)
        transition('agv-obstacle-stop', 'agv-1', {'distance': 25}, 'stopped', True)
        recovery_since = int(time.time()*1000)
        set_values('agv-1', {'distance': 150})
        wait(lambda: observed('agv-1', 'distance', 150, recovery_since), 'fresh AGV recovery distance')
        actors = {name: request(8091, '/api/sf/v1/login', {'login': name, 'password': private['password']})['token'] for name in ['engineer', 'leader']}
        run = request(8091, '/api/sf/v1/executions', {'definition_id': 'agv-resume', 'params': {}, 'downlink_id': 'scene-agv-resume-' + str(time.time_ns())}, actors['engineer'])
        for role in ['engineer', 'leader']:
            request(8091, '/api/sf/v1/executions/' + run['downlink_id'] + '/approve', {'role': role}, actors[role])
        since = int(time.time()*1000)
        dispatched = request(8091, '/api/sf/v1/executions/' + run['downlink_id'] + '/dispatch', {}, actors['engineer'])
        if dispatched['status'] in ['rejected', 'failed']:
            raise AssertionError(dispatched)
        wait(lambda: state('agv-1', 'stopped') is False, 'approved AGV resume')
        evidence = wait(lambda: observed('agv-1', 'stopped', False, since), 'AGV cloud/edge feedback')
        report['steps'].append({'label': 'agv-approved-resume', 'downlink_id': run['downlink_id'], 'feedback': evidence})
        before = state('counter-1', 'total')
        since = int(time.time()*1000)
        request(args.simulator_port, '/pulses', {'count': args.pulses, 'duplicate_every': 3})
        target = before + args.pulses
        evidence = wait(lambda: observed('counter-1', 'goods-count.total', target, since), 'exact goods count', 60)
        report['steps'].append({'label': 'goods-count-with-duplicate-delivery', 'unique_input': args.pulses, 'target': target, 'feedback': evidence})
        report['status'] = 'passed'
    except BaseException as error:
        report['status'], report['error'] = 'failed', str(error)
        raise
    finally:
        report['finished_ms'] = int(time.time()*1000)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')


if __name__ == '__main__':
    main()
