"""Independently verify retained Store evidence without replaying workloads."""
import collections
import datetime
import hashlib
import json
import sqlite3
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
REPORT = ROOT / '.local/evolution/storage-version-r01-verified.json'
BASE = Path('/private/tmp/smartfactory-storage-version-r01-20261005')
report = json.loads(REPORT.read_text())
errors, physical = [], {}


def digest(path):
    p = Path(path)
    if str(p) not in physical:
        data = p.read_bytes()
        physical[str(p)] = (hashlib.sha256(data).hexdigest(), len(data))
    return physical[str(p)]


def check(condition, kind, **details):
    if not condition:
        errors.append({'kind': kind, **details})


def verify(path, expected, size=None):
    if not Path(path).is_file():
        errors.append({'kind': 'missing', 'path': str(path)})
        return
    actual, length = digest(path)
    check(actual == expected and (size is None or length == size), 'file_hash', path=str(path))


def refs(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            yield value
        for item in value.values():
            yield from refs(item)
    elif isinstance(value, list):
        for item in value:
            yield from refs(item)


reference_rows = list(refs(report))
for row in reference_rows:
    verify(row['path'], row['sha256'], row.get('bytes'))
for row in report['fixed_source']:
    verify(row['fixed_copy'], row['sha256'])

manifests, retained = {}, {}
for item in report['compile_input_sets']:
    path = Path(item['manifest']['path'])
    rows = json.loads(path.read_text())
    manifests[path.parent.name] = rows
    check(len(rows) == item['input_count'], 'input_count', label=path.parent.name)
    classes = collections.Counter()
    for row in rows:
        verify(row['retained_copy'], row['sha256'], row.get('bytes'))
        retained[row['retained_copy']] = row['sha256']
        classes.update(set(x['field'] for x in row['uses']))
    check(dict(classes) == item['input_classes'], 'input_classes', label=path.parent.name)
check(len(retained) == report['retained_copy_count'], 'retained_count', actual=len(retained))

# Test variants expose selected tests through GoFiles/CompiledGoFiles/EmbedFiles.
# Dependency TestGoFiles describe tests that are not part of this invocation.
fields = ['GoFiles', 'CgoFiles', 'CompiledGoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles']
decoder, correspondence = json.JSONDecoder(), []
for label in ('before-public-fix', 'after-public-fix', 'race-public-fix', 'original-backup-probe'):
    folder = BASE / 'evidence' / label
    rows = manifests[label]
    by_path = {str(Path(x['path']).resolve()): x for x in rows}
    recorded = {x['path']: x['sha256'] for x in rows}
    commands = json.loads((folder / 'commands.json').read_text())
    list_command = next(x for x in commands if x['name'] == 'go-list')
    check(list_command['exit_code'] == 0, 'go_list_exit', label=label)
    check('-compiled' in list_command['argv'] and '-deps' in list_command['argv'], 'go_list_flags', label=label)
    if label == 'race-public-fix':
        check('-race' in list_command['argv'], 'race_go_list_flags')
        check('-race' in next(x for x in commands if x['name'] == 'build-test')['argv'], 'race_build_flags')
    for path in sorted(folder.glob('inputs-*.json')):
        check(json.loads(path.read_text()) == recorded, 'before_after_inputs', label=label, path=str(path))
    raw, position, packages, selected = (folder / 'go-list.stdout.log').read_text(), 0, 0, {}
    while position < len(raw):
        while position < len(raw) and raw[position].isspace():
            position += 1
        if position == len(raw):
            break
        package, position = decoder.raw_decode(raw, position)
        packages += 1
        for field in fields:
            for name in package.get(field, []):
                path = str((Path(package['Dir']) / name).resolve())
                selected.setdefault(path, set()).add(field)
        module = package.get('Module', {})
        module = module.get('Replace', module)
        if module.get('GoMod'):
            selected.setdefault(str(Path(module['GoMod']).resolve()), set()).add('Module')
    missing = sorted(set(selected) - set(by_path))
    check(not missing, 'go_list_missing_inputs', label=label, paths=missing)
    correspondence.append({'label': label, 'packages': packages, 'selected_paths': len(selected), 'recorded_inputs': len(rows), 'go_list_argv': list_command['argv'], 'missing': missing})


def test_status(folder):
    events = [json.loads(line) for line in (folder / 'test.stdout.log').read_text().splitlines() if line.startswith('{')]
    return {x['Test']: x['Action'] for x in events if x.get('Test') and x['Action'] in ('pass', 'fail', 'skip')}


def counts(status):
    return {
        'all_named': dict(collections.Counter(status.values())),
        'top_level': dict(collections.Counter(v for k, v in status.items() if '/' not in k)),
        'leaf': dict(collections.Counter(v for k, v in status.items() if not any(other.startswith(k + '/') for other in status))),
    }


test_results, statuses = [], {}
for item in report['checks']:
    label, folder = item['label'], Path(item['evidence_directory'])
    verify(item['binary'], item['binary_sha256'])
    if 'precise_counts' not in item:
        continue
    statuses[label] = test_status(folder)
    actual = counts(statuses[label])
    check(actual == item['precise_counts'], 'test_counts', label=label, actual=actual)
    command_exit = item.get('test_exit_code', item.get('exit_code'))
    if label.startswith('full-store'):
        before = json.loads((folder / 'inputs-before.json').read_text())
        after = json.loads((folder / 'inputs-after.json').read_text())
        check(before == after and len(before) == item['input_count'], 'full_store_inputs', label=label)
    test_results.append({'label': label, 'exit_code': command_exit, 'counts': actual})
combined = {**statuses['full-store'], **statuses['full-store-cwd-repair']}
check(combined == report['store_regression']['test_status'], 'full_store_status_union')
check(counts(combined) == report['store_regression']['counts'], 'full_store_count_union')
before_inputs = {x['path']: x['sha256'] for x in manifests['before-public-fix']}
after_inputs = {x['path']: x['sha256'] for x in manifests['after-public-fix']}
changed = sorted(x for x in set(before_inputs) | set(after_inputs) if before_inputs.get(x) != after_inputs.get(x))
check(changed == sorted(report['changed_compiled_inputs']), 'compiled_change_scope', actual=changed)


def database(path):
    connection = sqlite3.connect('file:' + str(path) + '?mode=ro', uri=True)
    tables, rows = {}, {}
    for (name,) in connection.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"):
        quoted = '"' + name.replace('"', '""') + '"'
        values = connection.execute('SELECT * FROM ' + quoted).fetchall()
        encoded = sorted(json.dumps([{'blob_hex': v.hex()} if isinstance(v, bytes) else v for v in row], separators=(',', ':'), ensure_ascii=False) for row in values)
        rows[name] = collections.Counter(encoded)
        tables[name] = {'rows': len(values), 'sha256': hashlib.sha256(('\n'.join(encoded) + '\n').encode()).hexdigest()}
    integrity = [list(x) for x in connection.execute('PRAGMA integrity_check').fetchall()]
    connection.close()
    return {'tables': tables, 'table_count': len(tables), 'integrity': integrity}, rows


original_path = Path(report['original_backup']['all_backup_files_preserved'][0]['path'])
original, original_rows = database(original_path)
with sqlite3.connect('file:' + str(original_path) + '?mode=ro', uri=True) as connection:
    original_fixed = connection.execute("SELECT * FROM documents WHERE kind='parameter_version' AND id='heartbeat.interval_ms:1'").fetchone()
probe_folder = BASE / 'evidence/original-backup-probe'
check(original == json.loads((probe_folder / 'original-before.json').read_text()), 'original_database_summary')
before_db = json.loads(Path(report['original_backup']['table_evidence_before']).read_text())
after_db = json.loads(Path(report['original_backup']['table_evidence_after']).read_text())
databases = []
for case in ('public-put', 'default-put', 'original-control'):
    path = BASE / 'private' / (case + '.db')
    actual, rows = database(path)
    check(before_db[case] == original, 'probe_initial_database', case=case)
    check(actual == after_db[case], 'probe_actual_database', case=case)
    differences = sorted(name for name in original_rows if rows.get(name) != original_rows[name])
    expected = ['document_versions', 'documents'] if case == 'public-put' else []
    check(differences == expected, 'database_change_scope', case=case, actual=differences)
    for name in differences:
        removed = original_rows[name] - rows[name]
        added = rows[name] - original_rows[name]
        check(not removed and sum(added.values()) == 1, 'database_retained_rows', case=case, table=name)
    connection = sqlite3.connect('file:' + str(path) + '?mode=ro', uri=True)
    history = [x[0] for x in connection.execute("SELECT version FROM document_versions WHERE kind='parameter' AND id='heartbeat.interval_ms' ORDER BY version")]
    row = connection.execute("SELECT version,data FROM documents WHERE kind='parameter' AND id='heartbeat.interval_ms'").fetchone()
    fixed = connection.execute("SELECT * FROM documents WHERE kind='parameter_version' AND id='heartbeat.interval_ms:1'").fetchone()
    connection.close()
    observed = next(x for x in report['original_backup']['probe_results'] if x['case'] == case)
    check(history == observed['history_after'], 'probe_history', case=case)
    check(fixed is not None and fixed == original_fixed, 'fixed_parameter_preserved', case=case)
    if case == 'public-put':
        check(row is not None and row[0] == 27 and json.loads(row[1])['version'] == 1, 'local_and_fixed_versions')
    databases.append({'case': case, 'table_count': actual['table_count'], 'integrity': actual['integrity'], 'changed_tables': differences, 'history_versions': history, 'current_local_revision': row[0] if row else None})
check(json.loads((probe_folder / 'probe.stdout.log').read_text()) == report['original_backup']['probe_results'], 'probe_report_correspondence')
cleanup = Path(report['resource_cleanup']['postgres_output']).read_text().splitlines()
check('PostgreSQL 18.6' in cleanup[0] and cleanup[-2:] == ['60', '0'], 'postgres_cleanup_evidence')

output = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'passed': not errors,
    'scope': '公共 Store 本地版本修正及原始数据副本检查；发布恢复、最终 Linux 组合和独立审查继续由对应工程师完成',
    'report': {'path': str(REPORT), 'sha256': digest(REPORT)[0]},
    'reference_count': len(reference_rows), 'physical_files_checked': len(physical),
    'input_manifests': len(manifests), 'unique_retained_inputs': len(retained),
    'changed_compiled_inputs': changed, 'go_list_correspondence': correspondence,
    'test_results': test_results, 'store_regression_counts': counts(combined),
    'original_backup_files': len(report['original_backup']['all_backup_files_preserved']),
    'database_checks': databases,
    'final_release_combination_verified': False, 'errors': errors,
}
target = ROOT / '.local/evolution/phase5-storage-version-root-review.json'
target.write_text(json.dumps(output, ensure_ascii=False, indent=2) + '\n')
printed = {k: output[k] for k in ('passed', 'reference_count', 'physical_files_checked', 'input_manifests', 'unique_retained_inputs', 'test_results', 'store_regression_counts', 'database_checks')}
printed['errors'] = [{k: v for k, v in error.items() if k != 'paths'} for error in errors]
print(json.dumps(printed, ensure_ascii=False))
raise SystemExit(0 if not errors else 1)
