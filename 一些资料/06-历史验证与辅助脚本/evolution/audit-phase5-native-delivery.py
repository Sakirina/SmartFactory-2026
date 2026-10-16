from pathlib import Path
import collections
import datetime
import hashlib
import importlib.util
import json
import os
import shutil
import subprocess

root = Path('/path/to/SmartFactory-2026')
source = Path('/private/tmp/smartfactory-delivery-native-restart-verified-20261005')
archive = Path('/private/tmp/smartfactory-phase5-native-coordinator-evidence-20261005')
archive.mkdir(exist_ok=True)
(archive / 'files').mkdir(exist_ok=True)
errors = []


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def ref(path):
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}


def verify(condition, detail):
    if not condition:
        errors.append(detail)


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


report_path = source / 'verification.json'
raw = report_path.read_bytes()
report = json.loads(raw)
saved_report = archive / 'verification.json'
if saved_report.exists():
    verify(saved_report.read_bytes() == raw, 'Previously archived verification report changed')
else:
    saved_report.write_bytes(raw)
verify(report['all_checks_passed'] and len(report['checks']) == 13 and all(report['checks'].values()), 'Native checks incomplete')
for item in report['driver_inputs']:
    verify(sha(Path(item['retained'])) == item['sha256'], 'Retained driver mismatch: ' + item['source'])
for name in ['compile_inputs_before', 'compile_inputs_after']:
    item = report[name]
    verify(sha(Path(item['path'])) == item['sha256'], 'Compile manifest mismatch: ' + name)
before = json.loads(Path(report['compile_inputs_before']['path']).read_text())
after = json.loads(Path(report['compile_inputs_after']['path']).read_text())
verify(len(before) == len(after) == 3226, 'Unexpected build input counts')

environment = dict(os.environ, **report['invocation']['toolchain_environment'], GOOS='darwin', GOARCH='arm64')
builder_path = source / 'driver-inputs/scripts/build-release.py'
spec = importlib.util.spec_from_file_location('retained_builder', builder_path)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)
current_logical, current_absolute = builder.build_inputs(root, {'Platform': ['sf-cloud', 'sf-edge', 'sf-pki']}, environment)
verify(current_logical == before, 'Current complete go-list input selection differs from retained prebuild manifest')
verify(current_absolute == after, 'Current complete absolute input selection differs from post-run observation')
write(archive / 'current-go-list-input-selection.json', {'scope': 'Post-run reconstruction with the retained collector and same target/environment, compared with the recorded prebuild manifest', 'logical': current_logical, 'absolute': current_absolute})
retained = {}
for original, expected in after.items():
    path = Path(original)
    verify(sha(path) == expected, 'Source content mismatch: ' + original)
    target = archive / 'files' / expected
    if not target.exists():
        shutil.copyfile(path, target)
    verify(sha(target) == expected, 'Retained source content mismatch: ' + original)
    retained[original] = {'retained': str(target), 'sha256': expected, 'bytes': target.stat().st_size}
write(archive / 'retained-inputs.json', {'recorded_before': report['compile_inputs_before'], 'recorded_after': report['compile_inputs_after'], 'retained_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'count': len(retained), 'inputs': retained})

for item in report['commands']:
    verify(item['exit_code'] == 0, 'Nonzero command: ' + item['name'])
    verify(sha(Path(item['log'])) == item['sha256'], 'Command log mismatch: ' + item['name'])
for name, item in report['binaries'].items():
    verify(sha(Path(item['path'])) == item['sha256'], 'Binary mismatch: ' + name)
    result = subprocess.run(['go', 'version', '-m', item['path']], capture_output=True, text=True, env=environment)
    verify(result.returncode == 0 and 'go1.27.1' in result.stdout, 'Binary toolchain metadata mismatch: ' + name)
    (archive / (name + '-build-info.txt')).write_text(result.stdout + result.stderr)
for item in report['processes']:
    verify(item['exit_code'] == 0 and not item.get('forced_kill'), 'Business process did not stop normally: ' + item['name'])
    verify(sha(Path(item['log'])) == item['log_sha256'], 'Business process log mismatch: ' + item['name'])
verify(report['pid1']['edge-a-native'].startswith('java '), 'Edge Java was not PID 1')
verify('-Xmx512m' in report['pid1']['kafka'], 'Formal Kafka heap missing')
verify(report['edge_stop_seconds'] < 60 and report['edge_stopped_state']['ExitCode'] in [0, 143] and not report['edge_stopped_state']['OOMKilled'], 'Edge stop limits failed')
verify((source / 'edge-database-transactions-ended.log').read_text().split('--- stderr ---')[0].strip() == '0', 'Edge database sessions remain')
verify('ShutdownHook' in (source / 'edge-shutdown-log.log').read_text(), 'Explicit Edge shutdown hook missing')
for name, state in report['final_container_states'].items():
    verify(state['ExitCode'] in [0, 143] and not state['OOMKilled'] and not state['Running'], 'Final native stop failed: ' + name)

roundtrip = json.loads((source / 'roundtrip.json').read_text())
verify({item['name'] for item in roundtrip['profiles']} == {'cloud', 'edge'}, 'Cloud and Edge profiles missing')
for item in roundtrip['profiles']:
    timestamp = int(item['message_id'].rsplit('-', 1)[1])
    verify(item['passed'] and item['ingress']['committed'] and not item['ingress']['duplicate'] and item['repeat']['committed'] and item['repeat']['duplicate'] and item['repeat']['count'] == 0, 'Native ingress/deduplication failed: ' + item['name'])
    for key in ['temperature', 'climate-average.temperature']:
        verify(any(point['ts'] == timestamp and float(point['value']) == 22.5 for point in item['tb_timeseries'][key]), 'Native telemetry does not match message timestamp/value: ' + item['name'] + '/' + key)
edge = next(item for item in roundtrip['profiles'] if item['name'] == 'edge')
edge_timestamp = int(edge['message_id'].rsplit('-', 1)[1])
for key in ['temperature', 'climate-average.temperature']:
    verify(any(point['ts'] == edge_timestamp and float(point['value']) == 22.5 for point in report['retained_timeseries'][key]), 'Retained data changed after Edge restart: ' + key)
longest = max(report['http_attempts'], key=lambda item: item['duration_seconds'])
input_kinds = collections.Counter(kind for item in before.values() for kind in item['input_kinds'])
review = {'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'passed' if not errors else 'failed', 'scope': 'Coordinator inspection of actual native execution, recorded prebuild/post-run input correspondence and retained source bytes', 'verification': ref(report_path), 'retained_verification': ref(saved_report), 'recorded_input_count': len(before), 'input_kinds': dict(input_kinds), 'current_go_list_selection_matches_recorded_before': current_logical == before, 'current_absolute_selection_matches_recorded_after': current_absolute == after, 'retained_inputs': ref(archive / 'retained-inputs.json'), 'driver_count': len(report['driver_inputs']), 'command_count': len(report['commands']), 'binary_count': len(report['binaries']), 'behavior_check_count': len(report['checks']), 'http_attempt_count': len(report['http_attempts']), 'edge_stop_seconds': report['edge_stop_seconds'], 'edge_exit_code': report['edge_stopped_state']['ExitCode'], 'final_container_exit_codes': {name: item['ExitCode'] for name, item in report['final_container_states'].items()}, 'all_final_containers_without_oom': all(not item['OOMKilled'] for item in report['final_container_states'].values()), 'native_restart_timestamp': edge_timestamp, 'retained_original_and_derived_value': 22.5, 'slowest_recorded_request': longest, 'native_fixture_settings': report['prepared_entrypoint']['fixture_adjustments'], 'native_state_origin': report['native_state_origin'], 'finite_workload': report['finite_workload'], 'original_failures_preserved': ['/private/tmp/smartfactory-delivery-native-retained-20261005/verification.json', '/private/tmp/smartfactory-delivery-native-restart-final-20261005/verification.json'], 'pending_requirement_scope': 'Final Linux release package prepared service entry and independent overall review', 'errors': errors}
write(root / '.local/evolution/phase5-native-delivery-root-review.json', review)
print(json.dumps({key: review[key] for key in ['status', 'recorded_input_count', 'driver_count', 'command_count', 'binary_count', 'behavior_check_count', 'edge_stop_seconds', 'errors']}, ensure_ascii=False))
raise SystemExit(bool(errors))
