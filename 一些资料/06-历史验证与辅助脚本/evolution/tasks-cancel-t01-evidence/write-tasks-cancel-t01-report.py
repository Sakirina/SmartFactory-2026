import collections
import datetime
import hashlib
import json
import shutil
import tarfile
from pathlib import Path

root = Path('/path/to/SmartFactory-2026')
out = root / '.local/evolution/tasks-cancel-t01-evidence'
for path in Path('/private/tmp').glob('storage-t01-*'):
    if path.is_file() and path.suffix in ('.log', '.jsonl') and path.name != 'storage-t01-report-summary.log':
        shutil.copy2(path, out / path.name)
if (out / 'storage-t01-report-summary.log').exists():
    (out / 'storage-t01-report-summary.log').unlink()
for name in ['probe-t01-cancel.py', 'build-t01-history-fixture.py', 'write-tasks-cancel-t01-report.py']:
    shutil.copy2(Path('/private/tmp') / name, out / name)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def artifact(name):
    path = root / name
    displayed = str(path.relative_to(root)) if path.is_relative_to(root) else str(path)
    return {'path': displayed, 'sha256': sha(path), 'bytes': path.stat().st_size}


def events(name):
    result = []
    for line in (out / name).read_text().splitlines():
        try:
            result.append(json.loads(line))
        except ValueError:
            pass
    return result


def count(values):
    counter = collections.Counter(values)
    return {key: counter[key] for key in ('pass', 'fail', 'skip')}


def statistics(name):
    data = events(name)
    actions = {'pass', 'fail', 'skip'}
    named = {(row['Package'], row['Test']): row['Action'] for row in data
             if row.get('Test') and row['Action'] in actions}
    top = {key: action for key, action in named.items() if '/' not in key[1]}
    leaf = {key: action for key, action in named.items()
            if not any(other[0] == key[0] and other[1].startswith(key[1] + '/') for other in named)}
    packages = {row['Package']: row['Action'] for row in data if not row.get('Test') and row['Action'] in actions}
    return {'top_level': count(top.values()), 'leaf_cases': count(leaf.values()),
            'all_named_cases': count(named.values()), 'packages': count(packages.values())}


def skipped(name):
    data = events(name)
    result = []
    for row in data:
        if row.get('Test') and row['Action'] == 'skip':
            outputs = ''.join(item.get('Output', '') for item in data
                              if item.get('Test') == row['Test'] and item.get('Package') == row['Package'])
            entry = {'package': row['Package'], 'test': row['Test'],
                     'reason': '; '.join(line.strip() for line in outputs.splitlines() if '_test.go:' in line)}
            if row['Test'].startswith('TestPostgresTaskOperationVersionsAllowHumanConfirmation/'):
                entry.update(disposition='executed_successfully_in_ordinary_PostgreSQL_and_PostgreSQL_race_runs',
                             evidence=['.local/evolution/tasks-cancel-t01-evidence/storage-t01-postgres-first.jsonl',
                                       '.local/evolution/tasks-cancel-t01-evidence/storage-t01-race-final.jsonl'])
            else:
                entry.update(disposition='pre_existing_conditional_test_outside_this_finding_specific_run',
                             note='Retained as a conditional skip in the ordinary affected-package command; no user exclusion is inferred')
            result.append(entry)
    return result


environment = {'GOTOOLCHAIN': 'local', 'GOPATH': '/private/tmp/smartfactory-evolution-gopath',
               'GOMODCACHE': '/private/tmp/smartfactory-evolution-gomodcache',
               'GOCACHE': '/private/tmp/smartfactory-evolution-go-cache', 'GOPROXY': 'off'}
checks = []


def go_check(name, args, scope, postgres=False):
    record = {'name': name, 'cwd': 'Platform', 'argv': ['go', 'test', *args],
              'environment': environment, 'exit_code': 0, 'scope': scope,
              'counts': statistics(name), 'conditional_skips': skipped(name),
              'evidence': artifact(Path('.local/evolution/tasks-cancel-t01-evidence') / name)}
    if postgres:
        record['postgres_environment'] = 'SF_TEST_POSTGRES_DATABASE privately loaded from .local/evolution/pg18-test.json dsn; value omitted'
        record['connection_budget'] = 'Each sequential case uses an isolated schema, at most 3 open / 1 idle application connections and 1 administration connection; package parallelism 1'
    checks.append(record)


go_check('storage-t01-operation-first.jsonl', ['-json', './internal/store', './internal/api', '-run',
         'TestTaskRoutesCheck|TestRiverTransactional|TestExpiredRiver', '-count=1'],
         'Initial task operation compilation and existing route/transaction/history-expiry regression checks')
go_check('storage-t01-sqlite-first.jsonl', ['-p', '1', '-json', './internal/api', '-run',
         '^TestTaskOperationVersionsAllowHumanConfirmation$', '-count=1'],
         'Initial 10-scenario SQLite development run; the final affected/race runs include the added concurrent-confirmation case and pending-parent normalization')
go_check('storage-t01-postgres-first.jsonl', ['-p', '1', '-json', './internal/api', '-run',
         '^TestPostgresTaskOperationVersionsAllowHumanConfirmation$', '-count=1'],
         'Final production implementation, 11 actual PostgreSQL scenarios', True)
go_check('storage-t01-affected-final.jsonl', ['-p', '1', '-json', './internal/store', './internal/tasks',
         './internal/application', './internal/api', './cmd/sf-contracts', '-count=1'],
         'Five complete affected packages; PostgreSQL environment omitted and all conditional skips listed')
go_check('storage-t01-race-final.jsonl', ['-race', '-p', '1', '-json', './internal/api', '-run',
         '^(TestTaskOperationVersionsAllowHumanConfirmation|TestPostgresTaskOperationVersionsAllowHumanConfirmation)$', '-count=1'],
         'Finite race verification: 11 SQLite and 11 actual PostgreSQL scenarios, including scheduler/parent-resolution/metadata and concurrent HTTP action races', True)
for name, command in [
    ('storage-t01-contract-check.log', ['/private/tmp/smartfactory-evolution-python/bin/python', 'scripts/check-contracts.py']),
    ('storage-t01-diff-check.log', ['git', 'diff', '--check']),
    ('storage-t01-postgres-cleanup.log', ['docker', 'exec', 'smartfactory-evolution-pg18-20261004', 'psql', '-U',
                                       'smartfactory_evolution', '-d', 'smartfactory_evolution', '-At', '-c',
                                       "SELECT COUNT(*) FROM pg_namespace WHERE nspname LIKE 'sf_task_cancel_t01_%'"]),
]:
    checks.append({'name': name, 'cwd': '.', 'argv': command, 'exit_code': 0,
                   'evidence': artifact(Path('.local/evolution/tasks-cancel-t01-evidence') / name)})

before = json.loads((out / 'before-inputs.json').read_text())
verification = json.loads((out / 'verification-source-before.json').read_text())
source_changes = [name for name, digest in verification['source_sha256'].items() if sha(root / name) != digest]
assert not source_changes, source_changes
original_manifest_path = Path('/private/tmp/smartfactory-phase3-before-t01-20261005/manifest.json')
original = json.loads(original_manifest_path.read_text())
changed_since_original = [name for name, value in original['files'].items() if sha(root / name) != value['sha256']]
assert changed_since_original == ['Platform/internal/store/history.go', 'Platform/internal/store/tasks.go'], changed_since_original
protected = []
for name, digest in before['source_sha256'].items():
    if name in changed_since_original:
        continue
    protected.append({'path': name, 'before_sha256': digest, 'after_sha256': sha(root / name),
                      'identical': digest == sha(root / name)})
assert all(item['identical'] for item in protected)
history_report = root / '.local/evolution/history-analysis-verified.json'
preserved_history = root / '.local/evolution/phase3-review-evidence/initial-specialist-reports/history-analysis-verified.json'
assert sha(history_report) == sha(preserved_history)
protected.append({'path': str(history_report.relative_to(root)), 'before_sha256': sha(preserved_history),
                  'after_sha256': sha(history_report), 'identical': True,
                  'preserved_original': artifact(preserved_history)})
contracts = [{'path': name, 'sha256': value['sha256'], 'identical': sha(root / name) == value['sha256']}
             for name, value in original['files'].items() if name.startswith('contracts/')]
assert all(item['identical'] for item in contracts)
binary_manifest_path = Path('.local/evolution/tasks-cancel-t01-evidence/current-binary-manifest.json')
binary_manifest = json.loads((root / binary_manifest_path).read_text())
assert all(sha(root / name) == value['sha256'] for name, value in binary_manifest['build_inputs'].items())
for key in ('binary', 'source_archive'):
    value = binary_manifest[key]
    assert sha(Path(value['path'])) == value['sha256']

production_files = ['Platform/internal/store/tasks.go', 'Platform/internal/store/history.go']
owned_files = production_files + ['Platform/internal/api/task_cancel_semantics_test.go', 'docs/evolution/tasks-frontend-contract.md']
for name in owned_files:
    destination = out / 'final-source' / name
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(root / name, destination)

race_events = events('storage-t01-race-final.jsonl')
measurements = []
for name in ['storage-t01-sqlite-first.jsonl', 'storage-t01-postgres-first.jsonl', 'storage-t01-affected-final.jsonl', 'storage-t01-race-final.jsonl']:
    for row in events(name):
        output = row.get('Output', '')
        if 'confirmation >=3s;' in output or '3s confirmation while actual committed progress advanced' in output:
            measurements.append({'run': name, 'test': row.get('Test'), 'observed': output.strip()})
browser_discovery = [artifact(Path('.local/evolution/phase3-review-evidence') / name) for name in [
    'v2-history-cancel-timing.json', 'v2-history-cancel-keyboard-timing.json',
    'v2-history-waiting-parent-cancel-reviewed-second.json']]
reproduction = json.loads((out / 'before-http-cancel.json').read_text())
assert reproduction['cancel_http_status'] == 409
assert reproduction['reviewed_run']['version'] == reproduction['run_after']['version'] == 2
assert reproduction['confirmation_wait_seconds'] >= 3
assert (out / 'storage-t01-postgres-cleanup.log').read_text().strip() == '0'

setup_attempts = [
    {'action': 'First HTTP probe used POST /api/sf/v1/auth/login', 'exit_code': 1,
     'observed_http_status': 405, 'observed_body': {'error': 'method not allowed'},
     'source': 'Observed initial exec_command result; this entry records the setup error separately from the product defect',
     'repair': 'Read the registered login route and used POST /api/sf/v1/login; the repaired probe then reproduced the task cancellation 409'},
    {'action': 'Inspect the owned fixture process using ps before shutdown', 'exit_code': 1,
     'observed_error': "PermissionError: [Errno 1] Operation not permitted: 'ps'",
     'repair': 'The approved exact-path pkill -TERM stopped only /private/tmp/smartfactory-t01-before/sf-history-fixture with its isolated runtime directory; command and process completed with exit 0 and port 19071 is closed'},
]
(out / 'setup-attempts.json').write_text(json.dumps(setup_attempts, indent=2) + '\n')

report = {
    'schema_version': 1, 'phase': 3, 'finding': 'P3-T01', 'role': 'storage_and_background_tasks',
    'generated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'specialist_fix_and_verification_complete_pending_coordinator_and_independent_review',
    'scope': 'Make task operation versions stable during scheduler/progress updates while preserving input, generation, phase, terminal-state, authorization and transactional action conflicts',
    'original_finding': {'evidence': browser_discovery,
                         'observed_confirmation_ms': [883, 848],
                         'condition': 'Actual history worker returned JobSnooze(500ms) while the persisted child run remained waiting_parent; scheduler fields invalidated the full-row hash'},
    'before_reproduction': {
        'source_snapshot': artifact(original_manifest_path),
        'source_archive': artifact(Path('/private/tmp/smartfactory-phase3-before-t01-20261005/source.tar.gz')),
        'binary': artifact(Path('/private/tmp/smartfactory-t01-before/sf-history-fixture')),
        'build': {'cwd': 'Platform', 'argv': ['go', 'build', '-o', '/private/tmp/smartfactory-t01-before/sf-history-fixture', './cmd/sf-history-fixture'],
                  'environment': environment, 'exit_code': 0, 'log': artifact(out / 'storage-t01-before-build.log')},
        'runtime': {'argv': ['/private/tmp/smartfactory-t01-before/sf-history-fixture', '-dir', '/private/tmp/smartfactory-t01-before/runtime',
                             '-address', '127.0.0.1:19071', '-static', str(root / 'Frontends/dist')],
                    'final_exit_code': 0, 'log': artifact(out / 'storage-t01-before-runtime.log')},
        'probe': {'argv': ['python3', '/private/tmp/probe-t01-cancel.py', 'before', '/private/tmp/smartfactory-t01-before/runtime', 'http://127.0.0.1:19071', '409'],
                  'exit_code': 0, 'script': artifact(out / 'probe-t01-cancel.py'),
                  'log': artifact(out / 'storage-t01-before-http.log'), 'response': artifact(out / 'before-http-cancel.json')},
        'confirmation_wait_seconds': reproduction['confirmation_wait_seconds'],
        'cancel_http_status': reproduction['cancel_http_status'],
        'business_versions': [reproduction['reviewed_run']['version'], reproduction['run_after']['version']],
        'business_state': reproduction['run_after']['status'], 'committed_steps': len(reproduction['steps_after']['items']),
        'original_confirmation_version_submitted': reproduction['submitted_expected_version'] == reproduction['reviewed_task']['version'],
    },
    'operation_version_semantics': {
        'wire_contract': 'Existing Task.version and TaskAction.expected_version strings; all protected contract files unchanged',
        'identity': ['task id', 'River id', 'kind', 'business id and creation version', 'outbox identity', 'queue',
                     'all task resources', 'encoded River work identity', 'max attempts', 'cancel marker', 'internal operation revision'],
        'queue_state': 'available/scheduled/pending/running share the active operation class; retryable/discarded/cancelled/completed remain distinct; recorded retryable/discarded error history is included',
        'analysis': 'Preserve run/task/candidate generation, selected definition versions, actual/captured snapshot ids and hashes, input hash, parent relation, initial-state resolution, resources, errors and meaningful business stage. Cursor/progress/updated timestamp/document version/accumulated final state are excluded.',
        'recompute': 'Preserve task/replay identity, fixed requested/replay interval, device/definition, phase/status/reason/error. Progress/cursor/document version are excluded.',
        'pending_and_running': 'Pending and running in the same captured operation share the active business stage. Pending unresolved children with a parent share the waiting_parent stage, excluding the generated waiting message.',
        'parent_completion': 'Parent-only updates leave the child confirmation valid while the child remains durably waiting. Saving the resolved child snapshot and beginning execution changes the operation version.',
        'internal_revision': {'field': 'river_job.metadata.sf_operation_revision', 'legacy_default': 0,
                              'update': 'Increment once per successful cancel or retry in the same business/Task/River/audit transaction',
                              'official_driver_api': 'TaskClient.Driver().UnwrapExecutor(tx).JobUpdate with MetadataDoMerge=true',
                              'metadata_preserved': ['River snoozes/output/cancellation metadata', 'sf_trace', 'unrelated metadata keys'],
                              'new_migration': False, 'new_public_field': False},
        'locking': 'Existing authorization collection/object shared guards precede linked business object lock, task advisory lock and PostgreSQL River row FOR UPDATE. Latest committed business cursor and results are read within this transaction and preserved on cancellation.',
        'conflicts_retained': ['terminal state', 'duplicate cancellation', 'prior manual operation or retry generation',
                               'resolved/replaced input snapshot', 'changed task resources', 'authorization/grant revision changes'],
        'forbidden_retained': ['role revoked before request', 'current resource ancestry removes access'],
    },
    'verification': {
        'checks': checks,
        'finite_race': {'sqlite_leaf_passed': 11, 'postgres_leaf_passed': 11,
                        'data_race_reports': (out / 'storage-t01-race-final.jsonl').read_text().count('WARNING: DATA RACE'),
                        'skipped': 0, 'production_snooze_interval_changed': False, 'production_timeouts_changed': False},
        'case_names': [row['Test'].split('/', 1)[1] for row in race_events
                       if row.get('Test', '').startswith('TestTaskOperationVersionsAllowHumanConfirmation/') and row['Action'] == 'pass'],
        'observed_confirmation_and_progress': measurements,
        'atomicity': 'A failing audit trigger leaves business status/version, sf_tasks.cancelled_ms and internal operation revision unchanged; dropping the trigger allows the original confirmation to succeed with one signed audit entry',
        'parent_and_metadata_competition': 'One barrier permits actual parent snapshot resolution before stale cancellation (409); another holds the worker after its waiting read while the parent completes and cancellation commits, then lets the old worker merge snooze metadata. No child step commits and the internal revision/trace remain present.',
        'concurrent_confirmations': 'Two actual HTTP cancellations with the same confirmation produce one 200 and one 409, one cancel audit and revision 1 on both stores',
        'running_progress_fixture': 'The actual evaluator and commit path run between a test-only 100ms observation delay after each committed step, keeping a finite 80-input run active during the 3s confirmation. Production worker snooze and timeout settings are unchanged.',
        'counting': 'Top-level and leaf counts are separate. The ordinary run has 82 top-level passes/23 skips and 133 leaf passes/34 skips; the PostgreSQL wrapper has 11 individually skipped children when no DSN is provided. Those 11 execute and pass in both dedicated PostgreSQL runs.',
        'source_before_commands': artifact(out / 'verification-source-before.json'),
        'source_changes_after_verification_snapshot': source_changes,
        'setup_failures_and_repairs': artifact(out / 'setup-attempts.json'),
    },
    'provenance': {
        'production_files': [artifact(Path(name)) for name in production_files],
        'owned_files': [artifact(Path(name)) for name in owned_files],
        'changes_among_coordinator_snapshotted_files': changed_since_original,
        'patch': artifact(out / 'implementation.patch'),
        'preserved_migration_compatibility_and_original_reports': protected,
        'contract_files_unchanged': len(contracts), 'contract_integrity': contracts,
        'schema_min_max': {key: [value['migration_minimum'], value['migration_maximum']]
                           for key, value in json.loads((root / 'Platform/pkg/compatibility/manifest.json').read_text())['databases'].items()},
        'frontend_changes': [], 'control_changes': [], 'global_phase_record_changes_by_specialist': [],
    },
    'binary_handoff': {
        'manifest': artifact(binary_manifest_path), 'binary': binary_manifest['binary'],
        'source_archive': binary_manifest['source_archive'], 'build_inputs': len(binary_manifest['build_inputs']),
        'local_packages': len(binary_manifest['local_packages']), 'changed_during_build': [],
        'all_build_inputs_still_match_current_source': True,
        'build_log': artifact(out / 'storage-t01-current-build.log'),
        'build_manifest_log': artifact(out / 'storage-t01-current-build-manifest.log'),
        'build_script': artifact(out / 'build-t01-history-fixture.py'),
        'coordinator_review': artifact(Path('.local/evolution/tasks-cancel-t01-binary-root-review.json')),
        'example_argv': ['/private/tmp/smartfactory-t01-current/sf-history-fixture', '-dir', '<new-isolated-data-directory>',
                         '-address', '127.0.0.1:<review-port>', '-static', str(root / 'Frontends/dist')],
        'credentials': 'Generated only in the selected data directory private-credentials.json; no credential values are copied to this report',
        'independent_browser_review': 'Owned and recorded separately by phase2_independent_review',
    },
    'cleanup': {'owned_service': artifact(out / 'owned-service-cleanup.json'),
                'postgres_test_schema_count_remaining': 0, 'all_test_servers_closed_by_successful_test_cleanup': True,
                'postgres_and_race_window_returned_to_coordinator': True,
                'runtime_data_and_original_evidence_retained': True,
                'QA_fixture_and_evidence_modified_by_specialist': False},
}
report['artifacts'] = [artifact(path) for path in sorted(out.rglob('*')) if path.is_file()]


def verify(value):
    if isinstance(value, dict):
        if {'path', 'sha256', 'bytes'} <= value.keys():
            actual = artifact(Path(value['path']))
            assert all(actual[key] == value[key] for key in actual), (actual, value)
        for item in value.values():
            verify(item)
    elif isinstance(value, list):
        for item in value:
            verify(item)


verify(report)
path = root / '.local/evolution/tasks-cancel-t01-verified.json'
path.write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps({'report': str(path.relative_to(root)), 'sha256': sha(path), 'checks': len(checks),
                  'artifacts': len(report['artifacts']), 'race': statistics('storage-t01-race-final.jsonl'),
                  'affected': statistics('storage-t01-affected-final.jsonl'), 'protected_sources_and_reports': len(protected),
                  'contract_files_unchanged': len(contracts), 'binary': binary_manifest['binary']}, indent=2))
