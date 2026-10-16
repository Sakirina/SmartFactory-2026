"""Verify the delivery checkpoint while retaining every incomplete outcome."""
import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CHECKPOINT = ROOT / '.local/evolution/phase5-delivery-linux-preflight-checkpoint.json'
report = json.loads(CHECKPOINT.read_text())
manifest_path = Path(report['fixed_inputs']['path'])
manifest = json.loads(manifest_path.read_text())
errors = []
digests = {}

def verify(path, expected, size=None):
    path = Path(path)
    if not path.is_file():
        errors.append({'kind': 'missing', 'path': str(path)})
        return
    if str(path) not in digests:
        raw = path.read_bytes()
        digests[str(path)] = (hashlib.sha256(raw).hexdigest(), len(raw))
    actual, count = digests[str(path)]
    if actual != expected or (size is not None and size != count):
        errors.append({'kind': 'content', 'path': str(path), 'expected': expected, 'actual': actual})

verify(manifest_path, report['fixed_inputs']['sha256'], report['fixed_inputs']['bytes'])
inputs = {row['path']: row for row in manifest['inputs']}
if len(inputs) != len(manifest['inputs']):
    errors.append({'kind': 'duplicate_input_path'})
for path, row in inputs.items():
    verify(path, row['sha256'], row['bytes'])
    verify(row['retained_copy'], row['sha256'], row['bytes'])

decoder = json.JSONDecoder()
listed = {}
packages = 0
for command in manifest['commands']:
    path = Path(command['go_list'])
    verify(path, command['go_list_sha256'])
    raw = path.read_text()
    position = 0
    while position < len(raw):
        while position < len(raw) and raw[position].isspace():
            position += 1
        if position == len(raw):
            break
        package, position = decoder.raw_decode(raw, position)
        packages += 1
        for field in manifest['field_names']:
            for filename in package.get(field, []):
                selected = str((Path(package['Dir']) / filename).resolve())
                listed.setdefault(selected, set()).add(field)
    if command['exit_code'] != 0:
        errors.append({'kind': 'go_list_exit', 'path': str(path)})
for path, fields in listed.items():
    row = inputs.get(path)
    if row is None or not fields.issubset(set(row.get('input_kinds', []))):
        errors.append({'kind': 'go_list_coverage', 'path': path, 'fields': sorted(fields)})

builds = []
for comparison in manifest['build_comparisons']:
    path = Path(comparison['build_report'])
    verify(path, comparison['sha256'])
    build = json.loads(path.read_text())
    if build['inputs_before'] != build['inputs_after']:
        errors.append({'kind': 'build_input_changed', 'path': str(path)})
    for identifier, original in build['inputs_before'].items():
        retained = manifest['public_identifiers'].get(identifier)
        if retained is None or retained['sha256'] != original['sha256']:
            errors.append({'kind': 'original_build_correspondence', 'path': str(path), 'identifier': identifier})
        else:
            verify(retained['retained_copy'], original['sha256'], original['bytes'])
    for name, binary in build['binaries'].items():
        verify(path.parent / 'bundle/bin' / name, binary['sha256'], binary['bytes'])
    for command in build['commands']:
        verify(command['log'], command['sha256'])
        if command['exit_code'] != 0:
            errors.append({'kind': 'build_command_exit', 'path': str(path)})
    builds.append({'path': str(path), 'inputs_before': len(build['inputs_before']), 'binaries': len(build['binaries']), 'commands': len(build['commands'])})

rounds = []
for summary in report['rounds']:
    path = Path(summary['report']['path'])
    verify(path, summary['report']['sha256'], summary['report']['bytes'])
    actual = json.loads(path.read_text())
    if actual['status'] != summary['status'] or actual.get('error') != summary.get('error'):
        errors.append({'kind': 'round_summary', 'path': str(path)})
    verify(summary['driver']['path'], summary['driver']['sha256'])
    commands = {c['name']: c for c in actual['commands']}
    for expected in summary['commands']:
        current = commands.get(expected['name'])
        if current is None or current['argv'] != expected['argv'] or current['exit_code'] != expected['exit_code']:
            errors.append({'kind': 'command_correspondence', 'path': str(path), 'name': expected['name']})
    rounds.append({'path': str(path), 'status': actual['status'], 'error': actual.get('error'), 'passed_checks': sum(c['passed'] for c in actual['checks']), 'failed_checks': [c['name'] for c in actual['checks'] if not c['passed']], 'commands': len(actual['commands'])})

result = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'passed' if not errors else 'needs_review',
    'scope': 'Checkpoint file integrity, retained actual inputs and raw-result correspondence; all eight Linux rounds remain incomplete or failed',
    'checkpoint': {'path': str(CHECKPOINT), 'sha256': hashlib.sha256(CHECKPOINT.read_bytes()).hexdigest()},
    'input_index': {'path': str(manifest_path), 'sha256': hashlib.sha256(manifest_path.read_bytes()).hexdigest()},
    'unique_input_paths': len(inputs), 'go_list_packages': packages,
    'go_list_selected_paths': len(listed), 'physical_files_verified': len(digests),
    'builds': builds, 'rounds': rounds, 'errors': errors,
    'final_linux_entrypoint_passed': False,
    'remaining': report['observed_entrypoint_progress']['not_completed'],
}
output = ROOT / '.local/evolution/phase5-linux-checkpoint-root-review.json'
output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: result[key] for key in ('status', 'unique_input_paths', 'go_list_packages', 'go_list_selected_paths', 'physical_files_verified', 'final_linux_entrypoint_passed', 'errors')}))
raise SystemExit(0 if not errors else 1)
