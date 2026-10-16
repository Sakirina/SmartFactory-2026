import json
import pathlib
import sys
import time
import urllib.error
import urllib.request

label, directory_name, address, expected_status = sys.argv[1:]
directory = pathlib.Path(directory_name)
base = address + '/api/sf/v1'
credentials = json.loads((directory / 'private-credentials.json').read_text())


def request(method, path, body=None, token=''):
    payload = None if body is None else json.dumps(body).encode()
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(base + path, data=payload, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


status, login = request('POST', '/login', credentials)
assert status == 200, (status, login)
token = login['token']
identity = 'analysis:shadow:fixture-failure:1:2'
status, task = request('GET', '/tasks/' + identity, token=token)
assert status == 200, (status, task)
status, run = request('GET', '/analysis-runs/' + task['business_id'], token=token)
assert status == 200
started = time.monotonic()
time.sleep(3)
status, observed = request('GET', '/tasks/' + identity, token=token)
waited = time.monotonic() - started
status, response = request('POST', '/tasks/' + identity + '/cancel', {'expected_version': task['version']}, token)
_, run_after = request('GET', '/analysis-runs/' + task['business_id'], token=token)
time.sleep(1.5)
_, task_settled = request('GET', '/tasks/' + identity, token=token)
_, steps_after = request('GET', '/analysis-runs/' + task['business_id'] + '/steps', token=token)
result = {'label': label, 'fixture_directory': str(directory), 'confirmation_wait_seconds': waited,
          'reviewed_task': task, 'observed_after_wait': observed, 'reviewed_run': run,
          'submitted_expected_version': task['version'], 'cancel_http_status': status,
          'cancel_response': response, 'run_after': run_after, 'settled_task': task_settled,
          'steps_after': steps_after, 'expected_status': int(expected_status)}
path = pathlib.Path('.local/evolution/tasks-cancel-t01-evidence') / (label + '-http-cancel.json')
path.write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps({'evidence': str(path), 'wait_seconds': waited, 'status': status,
                  'reviewed_version': task['version'], 'after_version': observed['version'],
                  'run_versions': [run['version'], run_after['version']],
                  'business_state': run_after['status'], 'settled_state': task_settled['state'],
                  'river_id': task['river_id'], 'steps': len(steps_after['items'])}, indent=2))
assert status == int(expected_status), (status, response)
