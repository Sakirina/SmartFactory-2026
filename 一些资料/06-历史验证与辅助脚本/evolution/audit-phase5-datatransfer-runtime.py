"""Check DataTransfer follow-up provenance and outcomes without replaying tests."""
import collections
import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
REPORT = ROOT / '.local/evolution/datatransfer-runtime-followup-verified.json'
report = json.loads(REPORT.read_text())
errors, files = [], {}


def verify(path, expected, size=None):
    p = Path(path)
    if not p.is_file():
        errors.append({'kind': 'missing', 'path': str(p)})
        return
    if str(p) not in files:
        raw = p.read_bytes()
        files[str(p)] = (hashlib.sha256(raw).hexdigest(), len(raw))
    actual, count = files[str(p)]
    if actual != expected or (size is not None and count != size):
        errors.append({'kind': 'hash', 'path': str(p), 'expected': expected, 'actual': actual})


def refs(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            yield value
        for child in value.values():
            yield from refs(child)
    elif isinstance(value, list):
        for child in value:
            yield from refs(child)


report_refs = list(refs(report))
for row in report_refs:
    verify(row['path'], row['sha256'], row.get('bytes'))

index = json.loads(Path(report['retained_inputs_verified']['path']).read_text())
retained, manifests = {}, {}
for item in index['manifests']:
    meta = item['manifest']
    verify(meta['path'], meta['sha256'], meta['bytes'])
    rows = json.loads(Path(meta['path']).read_text())
    if len(rows) != item['input_count']:
        errors.append({'kind': 'input_count', 'path': meta['path']})
    manifests[meta['path']] = rows
    for row in rows:
        verify(row['retained_copy'], row['sha256'], row.get('bytes'))
        retained[row['retained_copy']] = row['sha256']
if len(retained) != index['unique_retained_files']:
    errors.append({'kind': 'retained_input_count', 'actual': len(retained)})

fields = ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles', 'TestGoFiles', 'XTestGoFiles', 'TestEmbedFiles', 'XTestEmbedFiles']
decoder = json.JSONDecoder()
selected_commands = [
    report['production_change']['ordinary_tests'],
    report['isolated_consumer_change']['ordinary_tests'],
    report['final_affected_race'],
    report['production_change']['production_linux_build'],
    report['production_change']['linux_same_test_comparison']['baseline_build'],
    report['production_change']['linux_same_test_comparison']['candidate_build'],
]
correspondence = []
for command in selected_commands:
    for stage in ('before', 'after'):
        path = Path(command[stage])
        rows = manifests[str(path)]
        by_path = {row['path']: row for row in rows}
        go_list = path.with_name(path.name.replace('.inputs.json', '.go-list.json'))
        go_list_command = go_list.with_name(go_list.name.replace('.go-list.json', '.go-list-command.json'))
        list_command = json.loads(go_list_command.read_text())
        if list_command['exit_code'] != 0:
            errors.append({'kind': 'go_list_exit', 'path': str(go_list_command)})
        if '-race' in command['argv'] and '-race' not in list_command['argv']:
            errors.append({'kind': 'race_conditions', 'path': str(go_list_command)})
        text, position, packages, selected = go_list.read_text(), 0, 0, {}
        while position < len(text):
            while position < len(text) and text[position].isspace():
                position += 1
            if position == len(text):
                break
            package, position = decoder.raw_decode(text, position)
            packages += 1
            for field in fields:
                for name in package.get(field, []):
                    full = str((Path(package['Dir']) / name).resolve())
                    selected.setdefault(full, set()).add(field)
        missing = []
        for path_name, used_fields in selected.items():
            row = by_path.get(path_name)
            if row is None or not used_fields.issubset(set(row.get('input_kinds', []))):
                missing.append({'path': path_name, 'fields': sorted(used_fields)})
        if missing:
            errors.append({'kind': 'go_list_inputs', 'command': command['name'], 'stage': stage, 'missing': missing})
        correspondence.append({'name': command['name'], 'stage': stage, 'packages': packages, 'selected_paths': len(selected), 'recorded_inputs': len(rows), 'missing': missing,
                               'go_list_sha256': hashlib.sha256(go_list.read_bytes()).hexdigest(), 'go_list_command': list_command})

test_counts = []
for command in selected_commands[:3]:
    events = [json.loads(line) for line in Path(command['stdout']['path']).read_text().splitlines() if line.startswith('{')]
    counts = collections.Counter(x['Action'] for x in events if x.get('Test') and x['Action'] in ('pass', 'fail', 'skip'))
    passed = [x['Test'] for x in events if x.get('Test') and x['Action'] == 'pass']
    top = [x for x in passed if '/' not in x]
    if sorted(passed) != sorted(command['all_passed']) or sorted(top) != sorted(command['top_level_passed']) or counts['fail'] or counts['skip']:
        errors.append({'kind': 'test_counts', 'name': command['name']})
    test_counts.append({'name': command['name'], 'top_level_passed': len(top), 'all_passed': len(passed), 'failed': counts['fail'], 'skipped': counts['skip']})

attempts = []
for command in report['all_build_and_test_attempts']:
    before, after = manifests[command['before']], manifests[command['after']]
    if before != after or len(before) != command['input_count']:
        errors.append({'kind': 'attempt_inputs', 'name': command['name']})
    attempts.append({'name': command['name'], 'exit_code': command['exit_code'], 'inputs': len(before)})

comparison = report['production_change']['linux_same_test_comparison']
source_comparison = json.loads(Path(comparison['source_comparison']['path']).read_text())
base, candidate = Path(source_comparison['baseline']), Path(source_comparison['candidate'])
def source_files(directory):
    return {str(p.relative_to(directory)): hashlib.sha256(p.read_bytes()).hexdigest() for p in directory.rglob('*') if p.is_file()}
base_hashes, candidate_hashes = source_files(base), source_files(candidate)
differences = [x for x in sorted(set(base_hashes) | set(candidate_hashes)) if base_hashes.get(x) != candidate_hashes.get(x)]
if differences != ['internal/bootstrap/app.go']:
    errors.append({'kind': 'linux_source_difference', 'actual': differences})
runs = []
for name, expected in (('baseline_run', 1), ('candidate_run', 0)):
    path = Path(comparison[name]['path'])
    result = json.loads(path.read_text())
    inspect = json.loads((path.parent / 'inspect.stdout.log').read_text())[0]['State']
    if result['actual_exit'] != expected or inspect['ExitCode'] != expected or inspect['OOMKilled'] or result['cleanup_exit_code'] != 0:
        errors.append({'kind': 'linux_actual_exit', 'path': str(path)})
    runs.append({'name': name, 'actual_exit': inspect['ExitCode'], 'oom_killed': inspect['OOMKilled'], 'log_sha256': hashlib.sha256((path.parent/'logs.stdout.log').read_bytes()).hexdigest()})

production = report['production_change']['production_linux_isolated_run']
production_result = json.loads(Path(production['record']['path']).read_text())
production_commands = json.loads(Path(production_result['commands']).read_text())
for command in production_commands:
    verify(command['log'], command['sha256'])
    if command['exit_code'] != 0:
        errors.append({'kind': 'production_command_exit', 'name': command['name']})
for observation in production_result['observations']:
    name = 'round-' + str(observation['round']) + '-inspect'
    command = next(x for x in production_commands if x['name'] == name)
    command_log = Path(command['log']).read_text()
    inspected, parsed_end = decoder.raw_decode(command_log)
    if command_log[parsed_end:].strip() != '--- stderr ---':
        errors.append({'kind': 'production_inspect_stderr', 'round': observation['round']})
    actual_state = inspected[0]['State']
    if actual_state != observation['state'] or actual_state['ExitCode'] != 0 or actual_state['OOMKilled'] or observation['ready'] != {'status': 'ready'}:
        errors.append({'kind': 'production_runtime_state', 'round': observation['round']})
for row in production_result['inputs']:
    if not row['path'].endswith('.db'):
        verify(row['path'], row['sha256'], row['bytes'])
output = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'passed': not errors,
    'scope': 'P5-D01 correction and isolated consumer evidence; final packaged full Linux combination and five-template browser verification remain pending',
    'report': {'path': str(REPORT), 'sha256': hashlib.sha256(REPORT.read_bytes()).hexdigest()},
    'reference_count': len(report_refs), 'physical_files_checked': len(files),
    'input_manifests': len(manifests), 'unique_retained_inputs': len(retained),
    'source_files': report['source_changes'], 'go_list_correspondence': correspondence,
    'tests': test_counts, 'all_attempts': attempts, 'linux_source_differences': differences,
    'linux_actual_runs': runs, 'production_linux_isolated_result': production_result,
    'production_commands_checked': len(production_commands),
    'final_linux_package_entrypoint_passed': False, 'errors': errors,
}
target = ROOT / '.local/evolution/phase5-datatransfer-runtime-root-review.json'
target.write_text(json.dumps(output, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: output[key] for key in ('passed', 'reference_count', 'physical_files_checked', 'input_manifests', 'unique_retained_inputs', 'tests', 'linux_source_differences', 'linux_actual_runs', 'errors')}, ensure_ascii=False))
raise SystemExit(0 if not errors else 1)
