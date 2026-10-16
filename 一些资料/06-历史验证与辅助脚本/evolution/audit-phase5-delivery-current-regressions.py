from pathlib import Path
import collections
import datetime
import hashlib
import json

root = Path('/path/to/SmartFactory-2026')
directory = Path('/private/tmp/smartfactory-delivery-regressions-current-20261005')
report_path = directory / 'verification.json'
report = json.loads(report_path.read_text())
errors, verified, input_sets, stats = [], {}, {}, {}


def digest(path):
    path = path.resolve()
    if str(path) not in verified:
        content = path.read_bytes()
        verified[str(path)] = (hashlib.sha256(content).hexdigest(), len(content))
    return verified[str(path)]


def check(condition, description):
    if not condition:
        errors.append(description)


def check_file(path, expected, size=None):
    value, actual_size = digest(Path(path))
    check(value == expected, 'Hash mismatch: ' + str(path))
    if size is not None:
        check(actual_size == size, 'Size mismatch: ' + str(path))


check(report['status'] == 'passed' and report['all_checks_passed'], 'Specialist regression result incomplete')
check_file(directory / 'driver.py', report['driver_sha256'])
commands = {item['name']: item for item in report['commands']}
for item in report['commands']:
    check(item['exit_code'] == 0, 'Nonzero command: ' + item['name'])
    for channel in ['stdout', 'stderr']:
        check_file(item[channel], item[channel + '_sha256'])
for item in report['toolchain_executables'].values():
    check_file(item['path'], item['sha256'])
binary = report['consumer_binary']
check_file(binary['path'], binary['sha256'])
check(report['consumer_process']['exit_code'] == 0, 'Configuration consumer exit was nonzero')
for item in report['consumer_logs'].values():
    if isinstance(item, dict) and 'path' in item:
        check_file(item['path'], item['sha256'])

decoder = json.JSONDecoder()
all_originals = set()
retained_paths = set()
fields = report['input_fields']
coverage = {}
for manifest in report['input_sets']:
    check_file(manifest['path'], manifest['sha256'])
    rows = json.loads(Path(manifest['path']).read_text())
    check(len(rows) == manifest['input_count'], 'Input count mismatch: ' + manifest['name'])
    by_path = {}
    for item in rows:
        original = str(Path(item['path']).resolve())
        by_path[original] = item['sha256']
        all_originals.add(original)
        retained_paths.add(item['retained_copy'])
        check_file(item['retained_copy'], item['sha256'], item['bytes'])
    input_sets[manifest['name']] = by_path
    command = commands[manifest['name'] + '-go-list']
    raw = Path(command['stdout']).read_text()
    package_count, selected, absent = 0, set(), []
    while raw.strip():
        package, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        package_count += 1
        parent = Path(package.get('Dir', '/nonexistent'))
        for field in fields:
            for filename in package.get(field, []):
                path = (parent / filename).resolve()
                if not path.is_file():
                    continue
                selected.add(str(path))
                if str(path) not in by_path:
                    absent.append({'field': field, 'path': str(path)})
    check(package_count == manifest['package_count'], 'Package count mismatch: ' + manifest['name'])
    check(not absent, 'Missing selected compiler inputs: ' + manifest['name'] + ' / ' + str(absent[:3]))
    coverage[manifest['name']] = {'recorded_inputs': len(rows), 'package_count': package_count, 'selected_existing_go_list_inputs': len(selected), 'missing': absent}
for prefix in ['consumer-build', 'platform-tests', 'datatransfer-tests']:
    check(input_sets[prefix + '-before'] == input_sets[prefix + '-after'], 'Before/after inputs differ: ' + prefix)

for module, name in [('Platform', 'platform-tests'), ('DataTransfer', 'datatransfer-tests')]:
    counts, top, failures, skips = collections.Counter(), collections.Counter(), [], []
    for line in Path(commands[name]['stdout']).read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get('Test') and event.get('Action') in ['pass', 'skip', 'fail']:
            counts[event['Action']] += 1
            if '/' not in event['Test']:
                top[event['Action']] += 1
            if event['Action'] == 'skip':
                skips.append({'package': event['Package'], 'test': event['Test']})
            if event['Action'] == 'fail':
                failures.append(event)
    check(dict(counts) == report['test_results'][module]['counts_including_subtests'], 'Raw named-test counts differ: ' + module)
    check(not failures, 'Raw failed test: ' + module)
    expected_skips = {(item['package'], item['test']) for item in report['test_results'][module]['skips']}
    check(expected_skips == {(item['package'], item['test']) for item in skips}, 'Skipped case list differs: ' + module)
    stats[module] = {'top_level': dict(top), 'including_subtests': dict(counts), 'skips': skips}

check(report['final_database_connections'] == '0', 'Database connections remain')
check(not report['remaining_schemas'].strip(), 'Test schemas remain')
check(report['database_removed'], 'Owned regression database was not removed')
summary = {'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'passed' if not errors else 'failed', 'scope': 'Serial regression of candidate-final-second before P5-R02; verify retained input contents rather than the subsequently mutable workspace, with final affected-package and runtime checks pending', 'report': {'path': str(report_path), 'sha256': digest(report_path)[0], 'bytes': digest(report_path)[1]}, 'commands': len(report['commands']), 'input_sets': coverage, 'unique_original_input_paths': len(all_originals), 'unique_retained_content_paths': len(retained_paths), 'verified_file_paths': len(verified), 'results': stats, 'consumer_exit_code': report['consumer_process']['exit_code'], 'final_database_connections': report['final_database_connections'], 'remaining_schemas': report['remaining_schemas'], 'own_database_removed': report['database_removed'], 'external_fixture_correspondence': 'Final delivery report and final independent review pending', 'p5_r02_included': False, 'errors': errors}
output = root / '.local/evolution/phase5-delivery-current-regressions-root-review.json'
output.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: summary[key] for key in ['status', 'commands', 'unique_original_input_paths', 'unique_retained_content_paths', 'verified_file_paths', 'errors']}, ensure_ascii=False))
raise SystemExit(bool(errors))
