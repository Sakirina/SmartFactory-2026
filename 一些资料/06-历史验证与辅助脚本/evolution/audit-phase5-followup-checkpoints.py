#!/usr/bin/env python3
"""Audit saved diagnostic and interface evidence without running services."""
import datetime
import hashlib
import json
from pathlib import Path
import sqlite3

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / '.local/evolution'
NOW = datetime.datetime.now(datetime.timezone.utc).isoformat()


def read(path):
    return json.loads(Path(path).read_text())


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def ref(path):
    path = Path(path)
    return {'path': str(path), 'sha256': digest(path), 'bytes': path.stat().st_size}


def check_files(rows, key='path', copies=False):
    errors = []
    checked = set()
    for row in rows:
        names = [row[key]]
        if copies:
            names.append(row['retained_copy'])
        for name in names:
            expected = row['sha256']
            if (name, expected) in checked:
                continue
            checked.add((name, expected))
            if not Path(name).is_file() or digest(name) != expected:
                errors.append(name)
    return {'physical_files_checked': len(checked), 'mismatches': errors}


def save(name, data):
    data = {'checked_at': NOW, **data}
    (OUT / name).write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({'report': name, **{k: v for k, v in data.items() if k in ('passed', 'errors', 'summary')}}, ensure_ascii=False))


report_path = OUT / 'release-restore-followup-verified.json'
report = read(report_path)
diagnostic_refs = report['evidence'] + [
    {'path': v['fixed_source_path'], 'sha256': v['sha256']}
    for v in report['source_locations'].values()
]
diagnostic_checks = check_files(diagnostic_refs)
inputs_path = report['compiled_probe_inputs']['manifest']
inputs = read(inputs_path)
input_checks = check_files(inputs, copies=True)
table_proof_path = Path('/private/tmp/smartfactory-release-restore-followup-20261005/database-table-content-proof.json')
proof = read(table_proof_path)
actual_table_states = []
for state in proof['states']:
    db = Path(state['database'])
    with sqlite3.connect(db.as_uri() + '?mode=ro', uri=True) as conn:
        conn.execute('PRAGMA query_only=ON')
        tables = [x[0] for x in conn.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")]
        current = {}
        for table in tables:
            quoted = '"' + table.replace('"', '""') + '"'
            rows = [repr(tuple(row)) for row in conn.execute('SELECT * FROM ' + quoted)]
            current[table] = {'rows': len(rows), 'sha256': hashlib.sha256('\n'.join(sorted(rows)).encode()).hexdigest()}
        actual_table_states.append({'database': str(db), 'tables': current})
table_equal = all(x['tables'] == actual_table_states[0]['tables'] for x in actual_table_states)
errors = diagnostic_checks['mismatches'] + input_checks['mismatches']
if not table_equal:
    errors.append('database tables differ')
if len(report['minimum_reproductions']) != 3 or any('UNIQUE constraint failed' not in x['failure'] for x in report['minimum_reproductions']):
    errors.append('minimum reproduction outcomes differ')
save('phase5-release-restore-diagnosis-root-review.json', {
    'passed': not errors,
    'scope': 'Original failure diagnosis and retained data; correction and release recovery remain pending',
    'report': ref(report_path), 'reference_checks': diagnostic_checks,
    'input_manifest': ref(inputs_path), 'input_checks': input_checks,
    'minimum_reproduction_count': len(report['minimum_reproductions']),
    'actual_table_states': actual_table_states, 'all_database_tables_equal': table_equal,
    'summary': {'compiled_inputs': len(inputs), 'tables': len(actual_table_states[0]['tables']), 'database_copies': len(actual_table_states)},
    'errors': errors,
})

dt = Path('/private/tmp/smartfactory-datatransfer-runtime-followup-20261005/evidence')
dt_rows, dt_cases = [], []
for name in ('shutdown-stream-baseline-v1', 'shutdown-stream-diagnostic-v2'):
    command = read(dt / (name + '.command.json'))
    before, after = read(command['before']), read(command['after'])
    entries = [json.loads(line) for line in (dt / (name + '.stdout.log')).read_text().splitlines() if line.startswith('{')]
    failures = [x for x in entries if x.get('Action') == 'fail']
    dt_rows += before + after
    dt_cases.append({'command': ref(dt / (name + '.command.json')), 'stdout': ref(dt / (name + '.stdout.log')),
                     'input_count': len(before), 'before_after_equal': before == after,
                     'exit_code': command['exit_code'], 'failure_events': failures})
dt_checks = check_files(dt_rows, key='retained_copy')
errors = dt_checks['mismatches']
if any(not x['before_after_equal'] or x['exit_code'] != 1 or not x['failure_events'] for x in dt_cases):
    errors.append('baseline diagnostic outcome differs')
save('phase5-datatransfer-shutdown-baseline-root-review.json', {
    'passed': not errors,
    'scope': 'Controlled blocked-stream failure and goroutine diagnostic; corrected code and final Linux combination remain pending',
    'cases': dt_cases, 'input_checks': dt_checks,
    'selected_stacks': ref(dt / 'shutdown-stream-diagnostic-v2.selected-stacks.log'),
    'summary': {'controlled_failure_runs': len(dt_cases), 'inputs_per_run': dt_cases[0]['input_count']},
    'historical_linux_failure_has_no_same_condition_goroutine_proof': True,
    'errors': errors,
})

ui_base = Path('/private/tmp/smartfactory-release-interface-20261005')
ui_path = OUT / 'release-interface-checkpoint.json'
ui = read(ui_path)
backup = read(ui['backup'])
backup_checks = check_files(backup['files'], key='copy')
frontend = read(ui['source_and_static'])
retained = Path('/private/tmp/smartfactory-phase5-ui-checkpoint-coordinator-evidence-20261005')
retained.mkdir(mode=0o700, exist_ok=True)
sources = []
errors = list(backup_checks['mismatches'])
for field, base in (('source_files', ROOT), ('static_artifacts', ui_base / 'dist-dev')):
    for row in frontend[field]:
        path = base / row['path']
        if not path.exists() or digest(path) != row['sha256']:
            errors.append(str(path))
            continue
        copy = retained / row['sha256']
        if not copy.exists():
            copy.write_bytes(path.read_bytes())
            copy.chmod(0o600)
        if digest(copy) != row['sha256']:
            errors.append(str(copy))
        sources.append({**row, 'path': str(path), 'retained_copy': str(copy), 'kind': field})
rounds = []
for row in ui['all_browser_rounds']:
    actual = read(row['report'])['stats']
    consistent = (actual['expected'] == row['passed'] and actual['unexpected'] == row['failed'] and actual['skipped'] == row['skipped'])
    if not consistent:
        errors.append(row['report'])
    rounds.append({**row, 'sha256': digest(row['report']), 'stats_consistent': consistent})
cleanup = read(ui['self_service_cleanup'])
if not all(x['exited'] for x in cleanup['exit_confirmation']):
    errors.append('self service cleanup incomplete')
cb = ui_base / 'browser-credentials-fourth'
disabled = read(cb / 'identity-disabled-cache-cleared.state.json')
enabled = read(cb / 'identity-enabled-new-process.state.json')
rotated = read(cb / 'credential-rotated-old-instance-cleared.state.json')
activated = read(cb / 'rotated-credential-real-new-generation.state.json')
credential_checks = {
    'disabled_false': disabled['disabled']['enabled'] is False,
    'disabled_credential_denied': disabled['denied']['status'] == 401,
    'disabled_policy_cleared': disabled['before_policy']['HeartbeatMS'] == 7000 and disabled['revoked_policy']['HeartbeatMS'] == 5000,
    'new_process_restores_policy': enabled['restarted']['previous_pid'] != enabled['restarted']['pid'] and enabled['actual']['HeartbeatMS'] == 7000,
    'rotation_increments_generation': rotated['rotated']['generation'] == rotated['recovered']['generation'] + 1,
    'old_rotated_credential_denied': rotated['old_credential_denied']['status'] == 401,
    'old_process_policy_cleared': rotated['actual']['HeartbeatMS'] == 5000,
    'new_generation_restores_policy': activated['current']['generation'] == rotated['rotated']['generation'] and activated['actual']['HeartbeatMS'] == 7000,
    'four_reports_match_new_instance': len(activated['reports']) == 4 and all(x['instance_id'] == activated['current']['instance_id'] for x in activated['reports']),
}
if not all(credential_checks.values()):
    errors.append('credential state assertion differs')
page_observations = []
for path in sorted(cb.glob('*.page.json')):
    page = read(path)
    page_observations.append({'file': ref(path), 'errors': page['errors'], 'console': page['console']})
    if page['errors']:
        errors.append(str(path))
if digest(ui['backup']) != ui['backup_manifest_sha256']:
    errors.append('backup manifest hash differs')
save('phase5-release-interface-checkpoint-root-review.json', {
    'passed': not errors,
    'scope': 'Interface checkpoint, failure preservation and partial credential flow; final source/static combination is pending',
    'report': ref(ui_path), 'backup': ref(ui['backup']), 'backup_checks': backup_checks,
    'retained_source_inputs': sources, 'browser_rounds': rounds,
    'cleanup': ref(ui['self_service_cleanup']), 'credential_checks': credential_checks,
    'credential_evidence': [ref(x) for x in sorted(cb.iterdir()) if x.is_file()],
    'page_observations': page_observations,
    'visually_reviewed': [str(cb / 'rotated-credential-real-new-generation.png')],
    'source_static_equivalent_final_build': False,
    'source_static_note': frontend['note'],
    'summary': {'backup_files': len(backup['files']), 'source_files': len(frontend['source_files']), 'static_files': len(frontend['static_artifacts']), 'browser_rounds': len(rounds), 'self_services_exited': len(cleanup['exit_confirmation'])},
    'errors': errors,
})
