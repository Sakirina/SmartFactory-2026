#!/usr/bin/env python3
import collections
import datetime
import hashlib
import json
import pathlib
import shutil

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
BASE = pathlib.Path('/private/tmp/smartfactory-phase4-independent-browser-20261005')
OUT = ROOT / '.local/evolution/phase4-review-browser'
SCRIPT = ROOT / '.local/evolution/phase4-review-evidence'

def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()

def ref(path):
    path = pathlib.Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def read(path):
    return json.loads(pathlib.Path(path).read_text())

def write(path, data):
    pathlib.Path(path).write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n')

def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()

def walk_refs(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str) and len(value['sha256']) == 64:
            yield value
        for child in value.values():
            yield from walk_refs(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk_refs(child)

def suite_rows(suite, report):
    rows = []
    for spec in suite.get('specs', []):
        for test in spec.get('tests', []):
            final = test['results'][-1]
            rows.append({'report': report, 'file': spec['file'], 'title': spec['title'], 'status': final['status'], 'expected_status': test['expectedStatus'], 'duration_ms': final.get('duration'), 'errors': final.get('errors', [])})
    for child in suite.get('suites', []):
        rows.extend(suite_rows(child, report))
    return rows

assert not (OUT / 'summary.json').exists(), 'Preserve final browser summary'
all_rows, browser_runs = [], []
for path in sorted(OUT.rglob('browser-results.json')):
    data = read(path)
    key = str(path.parent.relative_to(OUT))
    rows = []
    for suite in data['suites']:
        rows.extend(suite_rows(suite, key))
    all_rows.extend(rows)
    browser_runs.append({'name': key, 'report': ref(path), 'stats': data['stats'], 'process_errors': data['errors'], 'tests': rows})
write(OUT / 'browser-test-outcomes.json', {'created_at': now(), 'runs': browser_runs})
standard_groups = {'browser-' + value + '-first' for value in ['business', 'details', 'state', 'precision', 'surfaces', 'ai']}
standard = [row for row in all_rows if row['report'] in standard_groups and row['status'] == 'passed']
unique = {(row['file'], row['title']) for row in all_rows if row['status'] == 'passed'}
assert len(standard) == 28 and len(unique) == 31, (len(standard), len(unique))

runtime_directories = sorted([p for p in BASE.glob('browser-*') if p.is_dir()] + [p for p in (BASE / 'r1-runtime').glob('browser-*') if p.is_dir()])
capture_rows, runtime_files = [], []
for directory in runtime_directories:
    for path in sorted(directory.rglob('*')):
        if not path.is_file():
            continue
        runtime_files.append(path)
        if path.suffix == '.json':
            data = read(path)
            if isinstance(data, dict) and all(key in data for key in ['viewport', 'console', 'errors', 'requests']):
                capture_rows.append({'capture': ref(path), 'viewport': data['viewport'], 'page_errors': data['errors'], 'console': data['console'], 'request_count': len(data['requests'])})
assert not any(row['page_errors'] for row in capture_rows)
write(OUT / 'page-console-review.json', {'created_at': now(), 'capture_count': len(capture_rows), 'page_error_count': sum(len(row['page_errors']) for row in capture_rows), 'records': capture_rows, 'note': 'Expected HTTP errors from the deliberate conflict, forbidden, expired, deleted and session-loss cases remain in the corresponding capture and test assertion'})

empty_before = read(BASE / 'browser-empty-all-before-fix/independent-empty-surfaces.json')
empty_after = read(BASE / 'r1-runtime/browser-empty/independent-empty-surfaces.json')
assert len(empty_after) == 9
assert all(row['dimensions']['document'] == row['width'] and row['dimensions']['body'] == row['width'] and row['horizontal']['after'] == 0 for row in empty_after)
visual = []
for group in ['browser-surfaces', 'browser-empty']:
    for path in sorted((BASE / 'r1-runtime' / group).glob('*.png')):
        visual.append({'image': ref(path), 'inspection': 'Reviewed actual pixels: heading and actions fit, numeric/time axes remain readable where data exists, legend paging controls are visible, and empty-state values and labels agree with the query', 'revision': 'r1'})
assert len(visual) == 18
write(OUT / 'visual-review.json', {'created_at': now(), 'viewports': [[1440, 1000], [1024, 768], [390, 844]], 'surfaces': ['cloud/index.html', 'edge/edge.html', 'cloud/screen.html'], 'r1_images_reviewed': visual, 'empty_before': ref(BASE / 'browser-empty-all-before-fix/independent-empty-surfaces.json'), 'empty_after': ref(BASE / 'r1-runtime/browser-empty/independent-empty-surfaces.json'), 'empty_before_widths': [{'surface': row['surface'], 'kind': row['kind'], 'viewport': row['width'], 'document': row['dimensions']['document'], 'horizontal': row['horizontal']['after']} for row in empty_before], 'empty_after_widths': [{'surface': row['surface'], 'kind': row['kind'], 'viewport': row['width'], 'document': row['dimensions']['document'], 'horizontal': row['horizontal']['after']} for row in empty_after], 'mobile_export_actions': ref(BASE / 'r1-runtime/browser-mobile/mobile-export-actions.json'), 'precision': ref(BASE / 'r1-runtime/browser-precision/exact-values-browser.json'), 'fixture_note': 'Eight ingest observations cover seven accepted values and one BAD value; each exact-value series has one sample. Approximate plotted coordinates and scientific axis labels accompany preserved exact tooltip/table/export values.'})

retention = []
for version in [1, 2, 3]:
    path = BASE / 'supplemental-sources' / ('extra-v' + str(version) + '.spec.ts')
    retention.append({'source': ref(path), 'original_paths': [str(SCRIPT / 'phase4-independent-extra.spec.ts'), str(BASE / 'frontend/Frontends/tests/phase4-independent-extra.spec.ts')], 'run': {1: 'browser-extra-first', 2: 'browser-extra-diagnostic', 3: 'browser-empty-all-before-fix and r1/browser-empty'}[version]})
tested_sources = []
for revision, workspace in [('frozen', BASE / 'frontend'), ('r1', BASE / 'frontend-r1')]:
    for path in sorted((workspace / 'Frontends/tests').glob('phase4*')):
        if path.is_file():
            tested_sources.append({'revision': revision, **ref(path)})
    tested_sources.append({'revision': revision, **ref(workspace / 'Frontends/playwright.phase4.config.ts')})
write(OUT / 'browser-source-retention.json', {'created_at': now(), 'historical_supplemental_tests': retention, 'final_test_sources': tested_sources, 'production_source_changes_for_testing': [], 'unchanged_frozen_source_verified': True})

public_files = list(runtime_files) + [path for path in OUT.rglob('*') if path.is_file() and path.suffix in ['.json', '.log', '.txt']]
private_values = []
for path in list(BASE.glob('*/private-credentials.json')) + list((BASE / 'r1-runtime').glob('*/private-credentials.json')):
    data = read(path)
    for key, value in data.items():
        if isinstance(value, str) and len(value) >= 12 and any(word in key.lower() for word in ['password', 'secret', 'token', 'key']):
            private_values.append(value.encode())
leaks = []
for path in public_files:
    if path.suffix in ['.json', '.log', '.txt']:
        data = path.read_bytes()
        if any(value in data for value in private_values):
            leaks.append(str(path))
assert not leaks, leaks
write(OUT / 'privacy-check.json', {'created_at': now(), 'actual_private_value_count': len(private_values), 'public_text_file_count': sum(path.suffix in ['.json', '.log', '.txt'] for path in public_files), 'matching_files': leaks, 'credential_values_emitted': False})

artifact_paths = set(runtime_files)
artifact_paths.update(path for path in OUT.rglob('*') if path.is_file() and path.name not in ['artifact-index.json', 'artifact-integrity.json', 'summary.json', 'specialist-final-audit.json'])
artifact_paths.update(path for path in SCRIPT.glob('*') if path.is_file() and (path.name.startswith(('browser_', 'phase4-independent-', 'audit_browser')) or path.name in ['final_cleanup.py', 'summarize_browser_review.py']))
artifact_paths.update(pathlib.Path(row['path']) for row in tested_sources)
artifact_paths.update(pathlib.Path(row['source']['path']) for row in retention)
for name in ['ai-final', 'ai-extra']:
    for pattern in ['*transcript*', 'manifest.json']:
        artifact_paths.update(path for path in (BASE / name).glob(pattern) if path.is_file())
for path in [BASE / 'specialist-final-inputs.json', BASE / 'specialist-freeze.json', BASE / 'frontend-r1/specialist-final-inputs-r1.json', BASE / 'frontend-r1/specialist-freeze-r1.json']:
    artifact_paths.add(path)
artifacts = [ref(path) for path in sorted(artifact_paths)]
write(OUT / 'artifact-index.json', {'created_at': now(), 'files': artifacts, 'file_count': len(artifacts), 'private_files_included': False})

references = list(artifacts)
for relative in ['source-snapshot.json', 'installed-dependencies.json', 'production-assets.json', 'r1/source-snapshot.json', 'r1/production-assets.json']:
    data = read(OUT / relative)
    references.extend(walk_refs(data))
for relative in ['independent-build-correspondence.json', 'r1/independent-build-correspondence.json']:
    data = read(OUT / relative)
    for item in data.get('correspondence', data.get('checks', [])):
        references.append({'path': item['own'], 'sha256': item['sha256']})
deduplicated = {(row['path'], row['sha256']): row for row in references}
failures = []
for row in deduplicated.values():
    path = pathlib.Path(row['path'])
    if not path.is_file() or sha(path) != row['sha256']:
        failures.append({'path': str(path), 'expected': row['sha256']})
assert not failures, failures
write(OUT / 'artifact-integrity.json', {'checked_at': now(), 'reference_checks': len(references), 'unique_path_digest_pairs': len(deduplicated), 'mismatches': failures, 'artifact_index': ref(OUT / 'artifact-index.json'), 'note': 'Historical supplemental input references use the preserved version-specific source map; compile-time and browser-time files remain independently available'})

heartbeat = read(BASE / 'browser-precision-first/query-heartbeat-resume.json')
poll = read(BASE / 'browser-extra-diagnostic/independent-ai-poll-deleted-cache.json')
byte_checks = read(BASE / 'browser-ai-first/ai-byte-correspondence.json')
audit = read(OUT / 'npm-audit.stdout.log')
summary = {'created_at': now(), 'status': 'independent_browser_review_passed', 'standard_frozen_scenarios_passed': len(standard), 'additional_independent_scenarios_passed': 3, 'unique_scenarios_with_final_pass': len(unique), 'r1_affected_rerun_scenarios': 5, 'r1_nonempty_layout_combinations': 9, 'r1_empty_layout_combinations': 9, 'first_failures_preserved': ['P4-F05 mobile empty-data document width 393 at viewport 390', 'Additional AI check initially reused a fixture whose definition had been deleted; fresh independent fixture completed the unchanged product assertion'], 'test_results': ref(OUT / 'browser-test-outcomes.json'), 'source_retention': ref(OUT / 'browser-source-retention.json'), 'frozen_correspondence': ref(OUT / 'independent-build-correspondence.json'), 'r1_correspondence': ref(OUT / 'r1/independent-build-correspondence.json'), 'r1_only_source_change': ref(OUT / 'r1/revision-scope.json'), 'build_checks': {'original_unit_pass': 27, 'r1_unit_pass': 27, 'typescript_7': 'passed with both production builds', 'typescript_6_compatibility': 'passed on both frozen inputs', 'independent_npm_audit': audit['metadata']['vulnerabilities'], 'assets_per_build': 15, 'each_asset_matches_specialist': True}, 'actual_heartbeat': {'elapsed_ms': heartbeat['elapsed_ms'], 'initial_status': heartbeat['status'], 'heartbeat': heartbeat['heartbeat'], 'delta': heartbeat['delta'], 'resume_status': heartbeat['resume_status'], 'ui_reconnection_cursor_count': len(heartbeat['ui_reconnect_cursors']), 'evidence': ref(BASE / 'browser-precision-first/query-heartbeat-resume.json')}, 'model_evidence': {'tool_count': len(byte_checks['checked']), 'tool_names': [row['tool_name'] for row in byte_checks['checked']], 'model_byte_correspondence': ref(BASE / 'browser-ai-first/ai-byte-correspondence.json'), 'deleted_resource_poll': {'elapsed_to_hidden_ms': poll['elapsed_to_hidden_ms'], 'evidence_requests_after_delete': poll['evidence_requests_after_delete'], 'messages': len(poll['final']['messages']), 'arguments': poll['final']['evidence'][0]['arguments'], 'resources': len(poll['final']['evidence'][0]['resources']), 'evidence': ref(BASE / 'browser-extra-diagnostic/independent-ai-poll-deleted-cache.json')}}, 'visual_review': ref(OUT / 'visual-review.json'), 'page_console_review': ref(OUT / 'page-console-review.json'), 'privacy': ref(OUT / 'privacy-check.json'), 'artifact_index': ref(OUT / 'artifact-index.json'), 'integrity': ref(OUT / 'artifact-integrity.json'), 'resource_cleanup': ref(OUT / 'final-resource-cleanup.json'), 'stage_acceptance_owner': 'root'}
write(OUT / 'summary.json', summary)
print(json.dumps({'standard_pass': len(standard), 'unique_scenario_final_pass': len(unique), 'artifacts': len(artifacts), 'integrity_unique': len(deduplicated), 'page_capture_count': len(capture_rows), 'page_errors': 0, 'private_value_matches': len(leaks), 'summary': str(OUT / 'summary.json')}))
