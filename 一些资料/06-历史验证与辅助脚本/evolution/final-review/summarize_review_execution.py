#!/usr/bin/env python3
import collections
import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
BASE = Path('/private/tmp/smartfactory-final-independent-20261007')
OUT = BASE / 'final-summary'
OUT.mkdir(exist_ok=True)
CACHE = {}


def sha(path):
    p = Path(path)
    key = (str(p), p.stat().st_size, p.stat().st_mtime_ns)
    if key not in CACHE:
        with p.open('rb') as f:
            CACHE[key] = hashlib.file_digest(f, 'sha256').hexdigest()
    return CACHE[key]


def ref(path):
    p = Path(path)
    return {'path': str(p), 'sha256': sha(p), 'bytes': p.stat().st_size}


def read(path):
    return json.loads(Path(path).read_text())


def write(name, value):
    p = OUT / name
    p.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    return ref(p)


def tests(suite):
    for spec in suite.get('specs', []):
        for test in spec['tests']:
            yield {'title': spec['title'], 'file': spec.get('file'),
                   'status': test['status'],
                   'attempts': [{'status': r['status'], 'duration_ms': r.get('duration'),
                                 'error': r.get('error', {}).get('message', '')[:1600]}
                                for r in test['results']]}
    for child in suite.get('suites', []):
        yield from tests(child)


reports = sorted((BASE / 'browser-results').glob('browser-*/browser-results.json'))
reports += sorted((BASE / 'phase5-browser/runs').glob('*/results.json'))
browser_runs = []
unique_passed = {}
for report in reports:
    raw = read(report)
    rows = list(tests(raw))
    run = {'report': ref(report), 'stats': raw['stats'], 'cases': rows}
    browser_runs.append(run)
    for row in rows:
        if row['status'] == 'expected' and any(a['status'] == 'passed' for a in row['attempts']):
            unique_passed[row['title']] = {'file': row['file'], 'report': ref(report)}
assert len(unique_passed) == 39, len(unique_passed)
browser_result = {
    'status': 'passed', 'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'unique_successful_test_titles': len(unique_passed),
    'phase4_business_query_precision_ai_cases': 27, 'phase5_cases': 12,
    'runs': browser_runs,
    'successful_cases': [{'title': k, **v} for k, v in sorted(unique_passed.items())],
    'retained_first_failures_and_followups': [
        {'first': 'browser-business-first', 'observed': 'Six Chrome launches ended with SIGABRT inside the default execution sandbox before a page opened', 'followup': 'The same binaries and page inputs passed with the approved browser execution permission; one viewport group stayed intentionally unselected'},
        {'first': 'browser-details-first', 'observed': 'Four tests passed; two could not read evidence from the earlier assumed business directory', 'followup': 'Only two evidence-path literals changed in the retained independent test copy; the two selected cases passed', 'parameterization': ref(BASE / 'frontend/test-overrides/detail-evidence-path.json')},
        {'first': 'phase5-release-first', 'observed': 'Composer passed; detail assertion requested release-b although the selected deployment targets only release-a', 'followup': 'The independent test was parameterized to the selected actual target and the detail-only rerun passed', 'parameterization': ref(BASE / 'frontend/test-overrides/release-detail-target/change.json')},
        {'first': 'browser-ai-first', 'observed': 'All eight cases passed; broad inventory noted edits to seven unselected phase5 test files', 'followup': 'The actual selected AI script, configuration and product inputs remained identical', 'selected_input_audit': ref(BASE / 'browser-results/browser-ai-first-selected-input-audit.json')},
    ],
    'conditional_selection': 'The two old first-publication baseline variants remain skipped, the viewer and editor tests each run in their own selected group, and the ninth-surface layout scenario retains the specialist final-bundle evidence',
    'same_final_nine_surfaces': ref(Path('/private/tmp/smartfactory-release-interface-20261005/browser-surfaces-final-second/all-nine-surfaces.state.json')),
    'old_control_history_task_simulation_scope': ref(BASE / 'readonly/legacy-browser-current-correspondence.json'),
    'frontend_product_and_asset_inventory': ref(BASE / 'frontend/snapshot.json'),
    'fixture_parameterization': ref(BASE / 'phase5-browser/browser-parameterization.json'),
    'original_driver_retention': ref(BASE / 'browser-results/prior-driver-retention.json'),
}
browser_ref = write('browser-summary.json', browser_result)

checks = []
errors = []
historic_script_changes = []


def check(path, expected, role, source):
    p = Path(path)
    ok = p.is_file() and sha(p) == expected
    row = {'path': str(p), 'expected_sha256': expected, 'matched': ok, 'role': role, 'inventory': str(source)}
    checks.append(row)
    if not ok:
        errors.append(row)


native = read(BASE / 'native-build/verification.json')
for target in native['input_sets']:
    index = target['index']['path'] if 'index' in target else target['path']
    for item in read(index):
        check(item['retained_copy'], item['sha256'], 'native-build-retained-input', index)
        check(item['path'], item['sha256'], 'native-build-current-input', index)

for item in read(BASE / 'node-runtime-v2/evidence/local-source-inputs.json'):
    check(item['archived_path'], item['sha256'], 'node-runtime-retained-input', 'node-runtime-v2/evidence/local-source-inputs.json')
    check(item['path'], item['sha256'], 'node-runtime-current-input', 'node-runtime-v2/evidence/local-source-inputs.json')

release_local = read(BASE / 'release-runtime/evidence/compiled-inputs.json')
for relative, expected in release_local['files'].items():
    check(BASE / 'release-runtime/source' / relative, expected, 'release-runtime-retained-local-input', 'release-runtime/evidence/compiled-inputs.json')
    check(ROOT / relative, expected, 'release-runtime-current-local-input', 'release-runtime/evidence/compiled-inputs.json')
for item in read(BASE / 'release-runtime/evidence/external-compiled-inputs.json'):
    check(item['retained_copy'], item['sha256'], 'release-runtime-retained-external-input', 'release-runtime/evidence/external-compiled-inputs.json')
    check(item['path'], item['sha256'], 'release-runtime-current-external-input', 'release-runtime/evidence/external-compiled-inputs.json')

binary_indexes = [BASE / 'release-runtime/evidence/binaries.json', BASE / 'node-runtime-v2/evidence/binaries.json']
for index in binary_indexes:
    for item in read(index):
        check(item['path'], item['sha256'], 'executed-binary', index)
for item in native['binaries']:
    artifact = item['artifact']
    check(artifact['path'], artifact['sha256'], 'native-executed-binary', 'native-build/verification.json')

browser_input_indexes = sorted((BASE / 'browser-results').glob('*.inputs.json'))
browser_input_indexes += sorted((BASE / 'phase5-browser/runs').glob('*.inputs.json'))
for index in browser_input_indexes:
    for item in read(index):
        check(item['retained_copy'], item['sha256'], 'browser-retained-input', index)
        p = Path(item['path'])
        if p.is_file() and sha(p) == item['sha256']:
            check(p, item['sha256'], 'browser-current-input', index)
        elif '/tests/' in str(p):
            historic_script_changes.append({'path': str(p), 'original_sha256': item['sha256'], 'current_sha256': sha(p) if p.is_file() else None, 'retained_copy': item['retained_copy'], 'inventory': str(index)})
        else:
            check(p, item['sha256'], 'browser-current-product-input', index)
snapshot = read(BASE / 'frontend/snapshot.json')
for item in snapshot['source_files']:
    check(item['original'], item['sha256'], 'frontend-current-source-at-final-build', 'frontend/snapshot.json')
for item in snapshot['static_assets']:
    check(item['path'], item['sha256'], 'browser-served-production-asset', 'frontend/snapshot.json')

input_result = {'status': 'passed' if not errors else 'failed',
                'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                'checks': checks, 'check_count': len(checks),
                'unique_checked_paths': len({c['path'] for c in checks}),
                'errors': errors,
                'retained_prior_test_scripts': historic_script_changes,
                'prior_test_script_reason': 'Independent fixtures use their own IDs, ports, evidence paths and selected targets; original and each selected test version are retained. The AI broad-list condition has a separate selected-input audit.',
                'inventory_scope': 'Native Go inventories include all declared test and embed fields of selected packages; production builds used their recorded go build commands. This is a conservative input inventory, not a claim that test files were compiled into production executables.',
                'browser_input_indexes': [ref(p) for p in browser_input_indexes],
                'driver': ref(Path(__file__))}
input_ref = write('executed-input-and-binary-audit.json', input_result)
assert not errors, errors[:2]

artifact_paths = set()
for folder in list((BASE / 'browser-runtime').glob('browser-*')) + list((BASE / 'phase5-browser').glob('evidence-*')):
    artifact_paths.update(p for p in folder.rglob('*') if p.is_file())
for report in reports:
    artifact_paths.add(report)
for folder in ['browser-results', 'phase5-browser/runs']:
    artifact_paths.update(p for p in (BASE / folder).glob('*') if p.is_file())
artifact_result = {'status': 'indexed', 'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   'file_count': len(artifact_paths), 'files': [ref(p) for p in sorted(artifact_paths)],
                   'browser_summary': browser_ref, 'input_verification': input_ref,
                   'scope': 'Own browser screenshots, state and network evidence, exports, raw result files, wrapper metadata and logs. Private fixture databases and credentials remain in their existing private directories.'}
artifact_ref = write('browser-artifact-index.json', artifact_result)
print(json.dumps({'browser': browser_ref, 'input_audit': input_ref, 'artifact_index': artifact_ref,
                  'passed_browser_cases': len(unique_passed), 'input_checks': len(checks),
                  'distinct_checked_paths': input_result['unique_checked_paths'],
                  'retained_prior_test_versions': len(historic_script_changes),
                  'browser_artifacts': len(artifact_paths)}, indent=2))
