from collections import Counter
import datetime
import hashlib
import json
from pathlib import Path
import shutil

root = Path('/path/to/SmartFactory-2026')
out = root / '.local/evolution/tasks-retry-t02-evidence'
report_path = root / '.local/evolution/tasks-retry-t02-verified.json'

def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def artifact(path):
    path = Path(path)
    if not path.is_absolute():
        path = root / path
    label = str(path.relative_to(root)) if path.is_relative_to(root) else str(path)
    return {'path': label, 'sha256': sha(path), 'bytes': path.stat().st_size}

logs = ['storage-t02-before-repair.jsonl', 'storage-t02-sqlite-first.jsonl',
        'storage-t02-sqlite-repaired.jsonl', 'storage-t02-postgres-final.jsonl',
        'storage-t02-affected-final.jsonl', 'storage-t02-race-final.jsonl',
        'storage-t02-contract-check.log', 'storage-t02-diff-check.log',
        'storage-t02-postgres-cleanup.log', 'storage-t02-build-sf-history-fixture.log',
        'storage-t02-build-sf-cloud.log', 'storage-t02-build-summary.json']
for name in logs:
    shutil.copy2(Path('/private/tmp') / name, out / name)
for source, name in [(Path('/private/tmp/build-t02-binaries.py'), 'build-t02-binaries.py'),
                     (Path('/private/tmp/smartfactory-t02-current/source.tar.gz'), 'current-source.tar.gz'),
                     (Path('/private/tmp/smartfactory-t02-current/sf-history-fixture-help.txt'), 'sf-history-fixture-help.txt'),
                     (Path('/private/tmp/smartfactory-t02-current/sf-cloud-help.txt'), 'sf-cloud-help.txt')]:
    shutil.copy2(source, out / name)
shutil.copy2(Path(__file__), out / 'write-tasks-retry-t02-report.py')

def events(name):
    return [json.loads(line) for line in (out / name).read_text().splitlines() if line.startswith('{')]

def statistics(name):
    rows = events(name)
    terminal = [r for r in rows if r.get('Action') in ('pass', 'fail', 'skip')]
    names = {r['Test'] for r in terminal if r.get('Test')}
    leaves = {name for name in names if not any(other.startswith(name + '/') for other in names)}
    def count(predicate):
        counts = Counter(r['Action'] for r in terminal if predicate(r))
        return {action: counts[action] for action in ('pass', 'fail', 'skip')}
    return {'top_level_tests': count(lambda r: r.get('Test') and '/' not in r['Test']),
            'leaf_tests': count(lambda r: r.get('Test') in leaves),
            'all_named_tests': count(lambda r: bool(r.get('Test'))),
            'packages': count(lambda r: not r.get('Test')),
            'package_elapsed_seconds': sum(r.get('Elapsed', 0) for r in terminal if not r.get('Test')),
            'data_race_reports': (out / name).read_text().count('WARNING: DATA RACE')}

race_events = events('storage-t02-race-final.jsonl')
race_passed = {(r.get('Package'), r.get('Test')) for r in race_events if r.get('Action') == 'pass'}
normal_events = events('storage-t02-affected-final.jsonl')
normal_passed = {r.get('Test') for r in normal_events if r.get('Action') == 'pass'}

def skipped(name):
    rows = events(name)
    result = []
    for row in rows:
        if row.get('Action') != 'skip' or not row.get('Test'):
            continue
        test = row['Test']
        entry = {'package': row['Package'], 'test': test,
                 'output': ''.join(r.get('Output', '') for r in rows if r.get('Test') == test).strip()}
        if (row['Package'], test) in race_passed:
            entry.update(disposition='executed_on_real_postgres_in_the_dedicated_normal_and_race_runs',
                         coverage=['storage-t02-postgres-final.jsonl', 'storage-t02-race-final.jsonl'])
        elif test == 'TestTaskCrashProcess':
            parents = ['TestRiverRecoversRecomputeCheckpointAfterProcessKill', 'TestRiverRecoversAfterProcessKill']
            assert all(p in normal_passed for p in parents)
            entry.update(disposition='helper_invoked_as_a_real_sigkill_child_by_passing_parent_tests', passing_parents=parents)
        elif test == 'TestHistoryCrashProcess':
            parents = ['TestHistorySIGKILLResumesCommittedCheckpointUsingOfficialRiverRescue', 'TestShadowSIGKILLPreservesResolvedParentSnapshot', 'TestShadowSIGKILLAtProductionDeliveryStages']
            assert all(p in normal_passed for p in parents)
            entry.update(disposition='helper_invoked_as_a_real_sigkill_child_by_passing_parent_tests', passing_parents=parents)
        elif test == 'TestControlledPostgresUpgradeFixture':
            entry.update(disposition='requires_explicit_upgrade_seed_or_verify_phase; not_selected_for_t02',
                         prior_upgrade_evidence=artifact('.local/evolution/pg16-to18-dump-restore.json'))
        else:
            entry.update(disposition='pre_existing_postgres_conditional_test_not_selected_in_the_t02_specific_postgres_command',
                         note='Recorded as a skip in this run; no user exclusion or equivalence claim is inferred')
        result.append(entry)
    return result

environment = {'GOTOOLCHAIN': 'local', 'GOPATH': '/private/tmp/smartfactory-evolution-gopath',
               'GOMODCACHE': '/private/tmp/smartfactory-evolution-gomodcache',
               'GOCACHE': '/private/tmp/smartfactory-evolution-go-cache', 'GOPROXY': 'off'}
checks = []

def go_check(name, arguments, exit_code, scope, postgres=False):
    record = {'name': name, 'cwd': 'Platform', 'argv': ['go', 'test', *arguments],
              'environment': environment, 'exit_code': exit_code, 'scope': scope,
              'counts': statistics(name), 'conditional_skips': skipped(name),
              'evidence': artifact(out / name)}
    if postgres:
        record['postgres_environment'] = 'SF_TEST_POSTGRES_DATABASE loaded privately from .local/evolution/pg18-test.json dsn; value omitted'
        record['connection_budget'] = 'One isolated schema per sequential case; 3 open / 1 idle application connections and at most 1 administration connection; package parallelism 1'
    checks.append(record)

common = ['-p', '1', '-json', './internal/api', '-run']
go_check('storage-t02-before-repair.jsonl', common + ['^TestTaskRetryClearsPriorCancellation$/running_cancel_retry_multiple_snoozes_then_cancel$', '-count=1'], 1,
         'Real running cancellation against pre-fix production source: retry200 followed by a production snooze becomes cancelled while the business run remains waiting_parent')
go_check('storage-t02-sqlite-first.jsonl', common + ['^TestTaskRetryClearsPriorCancellation$', '-count=1'], 1,
         'Initial three-case implementation run: sustained retry and audit rollback passed; the authorization fixture stored a stale embedded User.Version and its later GET returned409')
go_check('storage-t02-sqlite-repaired.jsonl', common + ['^TestTaskRetryClearsPriorCancellation$', '-count=1'], 0,
         'Six SQLite scenarios after correcting the fixture user version and adding concurrency, committed-step recovery and official null-merge comparison')
go_check('storage-t02-postgres-final.jsonl', common + ['^(TestPostgresTaskOperationVersionsAllowHumanConfirmation|TestPostgresTaskRetryClearsPriorCancellation)$', '-count=1'], 0,
         'Actual PostgreSQL 18.6: all 11 T01 and six T02 scenarios; final production source', True)
go_check('storage-t02-affected-final.jsonl', ['-p', '1', '-json', './internal/store', './internal/tasks', './internal/application', './internal/api', './cmd/sf-contracts', '-count=1'], 0,
         'Five complete affected packages with final source; PG environment omitted in this ordinary command; every conditional skip is listed')
go_check('storage-t02-race-final.jsonl', ['-race', '-p', '1', '-json', './internal/api', '-run',
         '^(TestTaskOperationVersionsAllowHumanConfirmation|TestPostgresTaskOperationVersionsAllowHumanConfirmation|TestTaskRetryClearsPriorCancellation|TestPostgresTaskRetryClearsPriorCancellation)$', '-count=1'], 0,
         'Final source finite race: 17 SQLite plus 17 actual PostgreSQL cases; old-worker snooze/commit overlap, concurrent HTTP operations, waiting/progress versions, authorization and audit rollback', True)
for name, argv in [('storage-t02-contract-check.log', ['/private/tmp/smartfactory-evolution-python/bin/python', 'scripts/check-contracts.py']),
                   ('storage-t02-diff-check.log', ['git', 'diff', '--check'])]:
    checks.append({'name': name, 'cwd': '.', 'argv': argv, 'exit_code': 0, 'evidence': artifact(out / name)})
cleanup = json.loads((out / 'storage-t02-postgres-cleanup.log').read_text())
assert cleanup['remaining_own_schemas'] == 0
checks.append({'name': 'storage-t02-postgres-cleanup.log', 'exit_code': 0,
               'command': "docker exec smartfactory-evolution-pg18-20261004 psql -U smartfactory_evolution -d smartfactory_evolution -At -c <read-only PostgreSQL version, data_directory, max_connections and own-schema count query>",
               'result': cleanup, 'evidence': artifact(out / 'storage-t02-postgres-cleanup.log')})

before = json.loads((out / 'before-inputs.json').read_text())
source = json.loads((out / 'final-verification-source-before.json').read_text())
changes_during_final_verification = [name for name, value in source['sha256'].items() if sha(root / name) != value]
assert not changes_during_final_verification, changes_during_final_verification
changes_since_before = [name for name, value in before['sha256'].items() if sha(root / name) != value]
assert set(changes_since_before) == {'Platform/internal/store/tasks.go', 'Platform/internal/api/task_cancel_semantics_test.go', 'docs/evolution/tasks-frontend-contract.md'}, changes_since_before
old_report = json.loads((root / '.local/evolution/tasks-cancel-t01-verified.json').read_text())
protected = []
for item in old_report['provenance']['preserved_migration_compatibility_and_original_reports']:
    name = item['path']; expected = item['after_sha256']; actual = sha(root / name)
    protected.append({'path': name, 'before_sha256': expected, 'after_sha256': actual, 'identical': expected == actual})
for name, value in before['sha256'].items():
    if name.startswith('.local/evolution/tasks-cancel-t01'):
        protected.append({'path': name, 'before_sha256': value, 'after_sha256': sha(root / name), 'identical': value == sha(root / name)})
assert all(p['identical'] for p in protected)
contracts = [{'path': name, 'sha256': value, 'identical': sha(root / name) == value}
             for name, value in before['sha256'].items() if name.startswith('contracts/')]
assert all(p['identical'] for p in contracts)
frontend = json.loads((root / '.local/evolution/frontend-phase3/source-manifest.json').read_text())
frontend_protected = [{'path': name, 'sha256': value, 'identical': sha(root / name) == value} for name, value in frontend['source_files'].items()]
frontend_protected += [{'path': 'Frontends/dist/' + name, 'sha256': value, 'identical': sha(root / 'Frontends/dist' / name) == value} for name, value in frontend['artifacts'].items()]
assert all(p['identical'] for p in frontend_protected)

binary_manifest = json.loads((out / 'current-binaries-manifest.json').read_text())
for name, entry in binary_manifest['binaries'].items():
    assert all(sha(root / path) == value['sha256'] for path, value in entry['build_inputs'].items()), name
    assert sha(Path(entry['binary']['path'])) == entry['binary']['sha256']
    assert sha(Path(entry['source_archive']['path'])) == entry['source_archive']['sha256']
old_manifest = json.loads((root / '.local/evolution/tasks-cancel-t01-evidence/current-binary-manifest.json').read_text())
for field in ('binary', 'source_archive'):
    assert sha(Path(old_manifest[field]['path'])) == old_manifest[field]['sha256']

measurements = []
for name in ['storage-t02-sqlite-repaired.jsonl', 'storage-t02-postgres-final.jsonl', 'storage-t02-affected-final.jsonl', 'storage-t02-race-final.jsonl']:
    for row in events(name):
        output = row.get('Output', '')
        if any(text in output for text in ('real running cancellation:', 'additional production snoozes', 'old worker completion rejected', 'official JobUpdate merge null:')):
            measurements.append({'run': name, 'test': row.get('Test'), 'observed': output.strip()})
failures = []
for name in ['storage-t02-before-repair.jsonl', 'storage-t02-sqlite-first.jsonl']:
    for row in events(name):
        if row.get('OutputType') == 'error':
            failures.append({'run': name, 'test': row.get('Test'), 'observed': row.get('Output', '').strip()})

report = {
    'schema_version': 1, 'phase': 3, 'finding': 'P3-T02', 'role': 'storage_and_background_tasks',
    'generated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'specialist_fix_and_verification_complete_pending_coordinator_and_independent_browser_review',
    'scope': 'Restore explicit task retry after a real running cancellation by removing the prior River cancellation intent inside the existing authorized action transaction',
    'original_finding': {'task': 'analysis:shadow:fixture-failure:1:2', 'river_id': 24,
        'observed': 'retry200 increments sf_operation_revision but preserves cancel_attempted_at; the next actual JobSnooze cancels River again while analysis_run remains waiting_parent',
        'evidence': [artifact('.local/evolution/phase3-review-evidence/' + name) for name in
            ['t01-single-read-task-and-river.json', 't01-retry-second-cancelled-with-waiting-parent.json', 't01-retry-second-backend.json', 't01-retry-second-river.json', 't01-retry-independent-reproduction.metadata.json', 't01-retry-independent-reproduction.stdout.log', 'independent-findings-through-t02.json']]},
    'reproduction_and_first_failures': {'failures': failures,
        'pre_repair_production_source': artifact(out / 'before-tasks.go'),
        'pre_repair_source_manifest': artifact(out / 'before-inputs.json'),
        'test_source_provenance': 'The initial new regression test was expanded after reproducing the failure; the exact pre-repair production source and raw failing run are preserved. Final test source is saved separately.',
        'initial_test_fixture_repair': 'The authorization scenario now increments the embedded model.User.Version with each fixture write and reauthenticates its principal before testing a concurrent grant mutation. No production authorization code changed.',
        'evidence_collector_attempt': artifact(out / 'evidence-preparation.json')},
    'implementation': {
        'production_files': ['Platform/internal/store/tasks.go'],
        'transaction_order': 'existing authorization revisions -> business object lock -> task identity lock -> River row lock -> current semantic version/state check -> business retry + official JobRetryTx -> sf_tasks cancellation-intent reset -> metadata cancellation-key deletion and operation revision increment -> audit and commit',
        'metadata': 'Read the latest locked River row into map[string]json.RawMessage, remove only cancel_attempted_at, advance sf_operation_revision, then use official JobUpdateFull with only MetadataDoUpdate enabled. Cancellation retains its existing official merge operation.',
        'preserved': ['all other River metadata including river:unique_nonce and snoozes', 'sf_trace', 'unknown custom values including integer9007199254740993, nested JSON null and a top-level JSON null field', 'same task ID, River ID, business run, captured snapshot and completed steps'],
        'driver_semantics': 'Both JobRetry implementations retain metadata. SQLite JobUpdate uses JSON merge patch and removes a null-valued supplied key; PostgreSQL JobUpdate concatenates JSONB and retains a null-valued key, while cancellation tests key presence. Locked full metadata replacement expresses deletion consistently.',
        'official_driver_sources': artifact(out / 'river-driver-source-hashes.json'),
        'official_driver_excerpts': artifact(out / 'river-driver-source-excerpts.txt'),
        'concurrency': 'Retry stays unavailable while the cancelled worker still owns a running attempt. Tests overlap cancellation with the original worker snooze or second step commit, require premature retry409, then retry the finalized cancellation. Concurrent HTTP retries produce one200 and one409 with one retry audit and one operation revision increment.',
        'rollback': 'An injected task.retry audit failure restores the prior cancellation timestamp, metadata, sf_tasks cancellation intent, business document and semantic version; retry can then use the same confirmed version after removing the injected failure.'},
    'verification': {'checks': checks, 'observed_measurements': measurements,
        'final_source': artifact(out / 'final-verification-source-before.json'),
        'postgres_normal_source': artifact(out / 'verification-source-before.json'),
        'source_files_checked': len(source['sha256']), 'source_changes_during_final_verification': changes_during_final_verification,
        'source_note': 'After the normal PG command, the metadata test added an explicit key-presence assertion for retained JSON null. The final ordinary and SQLite/PG race commands include that assertion; production source was unchanged.',
        'normal_top_level_pass_interpretation': 'The two PostgreSQL scenario wrappers return pass when every child conditionally skips without a DSN. Their 17 leaf scenarios were executed on the actual PG18 fixture in both the dedicated normal and final race commands.',
        'race_result': statistics('storage-t02-race-final.jsonl'),
        'ordinary_result': statistics('storage-t02-affected-final.jsonl')},
    'provenance': {'existing_files_changed': changes_since_before,
        'added_test': 'Platform/internal/api/task_retry_semantics_test.go',
        'patch': artifact(out / 't02.patch'),
        'protected_migrations_compatibility_and_previous_reports': protected,
        'contract_files_unchanged': contracts, 'frontend_source_and_dist_unchanged': frontend_protected,
        'schema_min_max': old_report['provenance']['schema_min_max'],
        't01_report_preserved': artifact('.local/evolution/tasks-cancel-t01-verified.json'),
        't01_binary_preserved': artifact(old_manifest['binary']['path']),
        't01_archive_preserved': artifact(old_manifest['source_archive']['path']),
        't01_contract_document_preserved': artifact('.local/evolution/tasks-cancel-t01-document-preservation-root-review.json'),
        'global_phase_record_changes_by_specialist': []},
    'binary_handoff': {'manifest': artifact(out / 'current-binaries-manifest.json'),
        'external_manifest': artifact('/private/tmp/smartfactory-t02-current/build-inputs.json'),
        'coordinator_review': artifact('.local/evolution/tasks-retry-t02-binary-root-review.json'),
        'binaries': {name: entry['binary'] for name, entry in binary_manifest['binaries'].items()},
        'combined_source_archive': artifact(out / 'current-source.tar.gz'),
        'launch_for_existing_failed_fixture_copy': binary_manifest['restore_launch'],
        'launch_validation': 'Production flags checked in app.Main and both actual binaries returned help with exit0; independent QA owns starting a preserved failure-data copy and its browser workflow'},
    'cleanup': {'self_started_long_running_services': [], 'httptest_servers_and_workers': 'Closed by each test cleanup',
        'postgres': cleanup, 'resource_window': 'PostgreSQL, race and large-Go verification window returned to coordinator after final tests',
        'independent_browser_service': 'Owned by the independent review role and outside this specialist cleanup'},
    'artifacts': [artifact(path) for path in sorted(out.rglob('*')) if path.is_file()],
}
assert report['verification']['race_result']['leaf_tests'] == {'pass': 34, 'fail': 0, 'skip': 0}
assert report['verification']['race_result']['data_race_reports'] == 0
assert report['verification']['ordinary_result']['packages'] == {'pass': 5, 'fail': 0, 'skip': 0}
report_path.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
references = []
def verify(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            path = Path(value['path']); path = path if path.is_absolute() else root / path
            assert path.is_file(), value
            assert sha(path) == value['sha256'], value
            references.append(value['path'])
        for item in value.values():
            verify(item)
    elif isinstance(value, list):
        for item in value:
            verify(item)
verify(report)
print(json.dumps({'report': artifact(report_path), 'checks': len(checks), 'artifacts': len(report['artifacts']),
                  'verified_hash_references': len(references), 'source_files': len(source['sha256']),
                  'protected_files': len(protected), 'contract_files': len(contracts),
                  'frontend_files': len(frontend_protected), 'race': report['verification']['race_result'],
                  'ordinary': report['verification']['ordinary_result']}, indent=2))
