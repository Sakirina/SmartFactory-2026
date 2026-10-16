import datetime
import hashlib
import json
import pathlib
import signal
import socket
import subprocess
import time
import urllib.error
import urllib.request

repo = pathlib.Path('/path/to/SmartFactory-2026')
evidence = repo / '.local/evolution/business-evidence'
snapshot = pathlib.Path((evidence / 'final-snapshot-path.txt').read_text().strip())
binary = snapshot / 'binaries/sf-business-fixture'
report_path = evidence / 'final-fixture-verification.json'
report = {'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
          'snapshot': str(snapshot), 'binary': str(binary),
          'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
          'runs': []}


def save():
    report_path.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')


def request(base, method, path, payload=None, token=None, expected=200):
    data = None if payload is None else json.dumps(payload).encode()
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    raw = response.read()
    if response.status != expected:
        raise RuntimeError(f'{method} {path}: expected {expected}, got {response.status}: {raw[:400]!r}')
    return json.loads(raw)


for mode in ['cloud', 'edge']:
    work = pathlib.Path('/private/tmp') / ('smartfactory-business-final-ui-' + mode + '-' + datetime.datetime.now().strftime('%Y%m%d-%H%M%S'))
    with socket.socket() as port:
        port.bind(('127.0.0.1', 0))
        address = f'127.0.0.1:{port.getsockname()[1]}'
    log_path = evidence / ('final-fixture-' + mode + '.log')
    args = [str(binary), '-dir', str(work), '-mode', mode, '-address', address, '-duration', '60s']
    run = {'mode': mode, 'directory': str(work), 'command': args,
           'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
           'log': str(log_path), 'checks': {}, 'artifacts': {}}
    report['runs'].append(run)
    save()
    with log_path.open('w') as log:
        process = subprocess.Popen(args, cwd=snapshot / 'Platform', stdout=log, stderr=subprocess.STDOUT)
        run['pid'] = process.pid
        try:
            deadline = time.monotonic() + 20
            while not (work / 'manifest.json').exists():
                if process.poll() is not None or time.monotonic() >= deadline:
                    raise RuntimeError('fixture failed to become ready: ' + log_path.read_text()[-1000:])
                time.sleep(0.1)
            manifest = json.loads((work / 'manifest.json').read_text())
            sample = json.loads((work / 'samples.json').read_text())
            private = json.loads((work / 'private-credentials.json').read_text())
            base = manifest['url']
            logged_in = request(base, 'POST', '/api/sf/v1/login', {'login': private['login'], 'password': private['password']})
            token = logged_in['token']
            run['checks']['actual_http_login'] = True
            run['checks']['private_credentials_mode_0600'] = ((work / 'private-credentials.json').stat().st_mode & 0o777) == 0o600
            protocols = request(base, 'GET', '/api/sf/v1/device-protocols', token=token)
            templates = request(base, 'GET', '/api/sf/v1/scene-templates', token=token)
            assert len(protocols) == 3 and len(templates) == 5
            run['checks']['protocols'] = len(protocols)
            run['checks']['templates'] = len(templates)
            alarm_path = '/api/sf/v1/alarms/' + manifest['alarm_id']
            reviewed = request(base, 'GET', alarm_path, token=token)
            time.sleep(2.1)
            handled = request(base, 'POST', alarm_path + '/actions', {
                'request_id': 'final-fixture-ack', 'expected_version': reviewed['case']['version'],
                'expected_action_version': reviewed['action_version'], 'action': 'acknowledge',
                'reason': '持续采样期间使用同一次已阅详情确认告警'}, token=token)
            assert handled['alarm']['acknowledged'] and handled['alarm']['count'] > reviewed['alarm']['count']
            assert handled['case']['assignee_id'] == logged_in['user']['id'] and len(handled['case']['operations']) == 3
            run['checks']['continuous_sampling_one_review_confirmed'] = {'count_before': reviewed['alarm']['count'], 'count_after': handled['alarm']['count']}
            page = request(base, 'POST', '/api/sf/v1/queries/alarms', {'limit': 50}, token=token)
            row = next(item for item in page['items'] if item['id'] == manifest['alarm_id'])
            assert row['data']['acknowledged'] and row['data']['handling_case']['assignee_id'] == logged_in['user']['id']
            run['checks']['query_case_projected'] = {'revision': row['revision'], 'operations': len(row['data']['handling_case']['operations'])}
            work_path = '/api/sf/v1/work-orders/' + manifest['work_order_id']
            order = request(base, 'GET', work_path, token=token)
            other = request(base, 'POST', '/api/sf/v1/login', {'login': private['other_login'], 'password': private['password']})
            moved = request(base, 'POST', work_path + '/handovers', {
                'request_id': 'final-fixture-handover', 'expected_version': order['work_order']['version'],
                'from_user_id': logged_in['user']['id'], 'to_user_id': other['user']['id'],
                'pending_items': ['后续班次继续检查温度变化'],
                'evidence': [{'kind': 'alarm', 'id': manifest['alarm_id'], 'version': handled['alarm']['version'], 'action_version': handled['action_version']}],
                'reason': '隔离前端夹具交接验证'}, token=token)
            assert moved['work_order']['assignee_id'] == other['user']['id'] and len(moved['handovers']) == 1
            run['checks']['handover_committed'] = moved['work_order']['version']
            device_id = sample['template_batch_input']['instances'][0]['device_id']
            configuration = request(base, 'GET', '/api/sf/v1/devices/' + device_id + '/configuration', token=token)
            expected = 200 if mode == 'edge' else 403
            saved = request(base, 'POST', '/api/sf/v1/device-configurations', {
                'id': device_id, 'request_id': 'final-fixture-save', 'expected_version': configuration['entity']['version'],
                'parameters': configuration['configuration']['parameters']}, token=token, expected=expected)
            run['checks']['device_save_status'] = expected
            if mode == 'edge':
                assert saved['entity']['version'] == configuration['entity']['version'] + 1
                run['checks']['device_version_advanced'] = saved['entity']['version']
            else:
                batch = request(base, 'GET', '/api/sf/v1/template-batches/' + manifest['template_batch_id'], token=token)
                assert batch['status'] == 'prepared' and len(batch['drafts']) == 25
                run['checks']['prepared_drafts'] = len(batch['drafts'])
            run['passed'] = True
        except Exception as error:
            run['passed'] = False
            run['failure'] = str(error)
            raise
        finally:
            if process.poll() is None:
                process.send_signal(signal.SIGINT)
            try:
                run['exit_code'] = process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                run['forced_kill'] = True
                run['exit_code'] = process.wait(timeout=10)
            run['completed_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
            run['process_stopped'] = process.poll() is not None
            for name in ['manifest.json', 'samples.json']:
                path = work / name
                if path.exists():
                    run['artifacts'][name] = {'path': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size}
            save()
    run['log_sha256'] = hashlib.sha256(log_path.read_bytes()).hexdigest()
    print(mode, 'passed', run.get('passed'), 'stopped', run['process_stopped'], flush=True)
    save()

report['all_checks_passed'] = all(run['passed'] and run['exit_code'] == 0 and run['process_stopped'] for run in report['runs'])
report['completed_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
save()
