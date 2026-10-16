import collections, datetime, hashlib, json, pathlib, shutil

repo = pathlib.Path('/path/to/SmartFactory-2026')
evidence = repo / '.local/evolution/query-evidence'
def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def relative(path): return str(path.relative_to(repo))
def load(name): return json.loads((evidence / name).read_text())
def record(name):
    path = evidence / name
    return {'path': relative(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

for name in ['sf-query-freeze.py', 'sf-query-final-verify.py', 'sf-query-final-artifacts.py', 'sf-query-final-audit.py', 'sf-query-report.py']:
    shutil.copyfile(pathlib.Path('/private/tmp') / name, evidence / name)
transcript = pathlib.Path('/private/tmp/smartfactory-business-shared-registration-compile-first.log')
if transcript.exists(): shutil.copyfile(transcript, evidence / 'shared-registration-compile-first-transcribed.log')
(evidence / 'first-shared-build-transcribed.log').write_text('Recorded exploratory tool output; transcribed, not a rerun\nTime: 2026-10-04T23:36:21Z\nCommand: go test -p 1 ./internal/store -run ^TestNumberedMigrationsAdoptLegacySQLiteAndPreserveHistory$ -count=1\nWorking directory: Platform\nExit code: 1\n# competition2026/product/platform/pkg/model\npkg/model/business.go:85:28: syntax error: unexpected name enum in struct type; possibly missing semicolon or newline or }\npkg/model/business.go:112:67: string not terminated\nFAIL competition2026/product/platform/internal/store [build failed]\nFAIL\n')

runs = load('final-verification-runs.json')
normal_names = ['final-store', 'final-application', 'final-api', 'final-cloudsync', 'final-v2-control']
race_names = ['final-race-application', 'final-race-api', 'final-race-store', 'final-race-cloudsync', 'final-race-control']
checks = []
for name in normal_names + race_names:
    run = next(item for item in runs['runs'] if item['name'] == name)
    events = [json.loads(line) for line in (repo / run['log']).read_text().splitlines() if line.startswith('{')]
    terminal = {event['Test']: event['Action'] for event in events if event.get('Test') and event['Action'] in ['pass', 'fail', 'skip']}
    leaves = {name: action for name, action in terminal.items() if not any(other.startswith(name + '/') for other in terminal)}
    result = {**run, 'leaf_tests': leaves, 'leaf_counts': dict(collections.Counter(leaves.values())), 'all_named_test_counts': dict(collections.Counter(terminal.values()))}
    if run['exit_code'] or result['leaf_counts'].get('fail') or result['leaf_counts'].get('skip'): raise RuntimeError(name)
    if sha(repo / run['log']) != run['log_sha256']: raise RuntimeError('log hash mismatch ' + name)
    checks.append(result)
normal_count = sum(item['leaf_counts'].get('pass', 0) for item in checks if item['name'] in normal_names)
race_count = sum(item['leaf_counts'].get('pass', 0) for item in checks if item['name'] in race_names)
for path, metadata in runs['binaries'].items():
    if sha(pathlib.Path(path)) != metadata['sha256']: raise RuntimeError('binary changed ' + path)

snapshot_change = load('snapshot-v2-change.json')
audit = load('final-audit.json')
artifacts = load('generated-artifacts.json')
first_failures = [
    {'id': 'exploratory-shared-model-compile', 'kind': 'concurrent_source_edit_compile_failure', 'evidence': record('first-shared-build-transcribed.log'), 'observation': 'The first exploratory store compile encountered incomplete struct tags in business.go while the business specialist was editing that shared model', 'resolution': 'The business specialist completed the model edit; subsequent migration, API, and frozen builds passed', 'provenance': 'Transcribed prior command output; exploratory source and binary were not frozen'},
    {'id': 'query-fixture-archive-signature', 'kind': 'test_compile_failure', 'evidence': record('store-initial.jsonl'), 'observation': 'ArchiveObservations accepts context only; the initial new query test passed an additional policy argument', 'resolution': 'Corrected the test call; final SQLite/PostgreSQL archive and rebuild cases passed'},
    {'id': 'query-fixture-expiry-floor', 'kind': 'test_assertion_failure', 'evidence': record('store-second.jsonl'), 'observation': 'The test expected a snapshot exactly at the inclusive retained floor to expire', 'resolution': 'Advanced a further committed write before compacting so the tested cursor is below the retained floor; final expiry tests passed'},
    {'id': 'query-fixture-user-version', 'kind': 'test_fixture_failure', 'evidence': record('application-second.jsonl'), 'observation': 'A direct test principal had user version zero while the stored document and final control authorization used version one', 'resolution': 'Aligned the fixture with the stored identity and actual definition versions; final execution authorization cases passed'},
    {'id': 'P4-Q03', 'kind': 'production_postgres_failure', 'evidence': record('postgres-first.jsonl'), 'observation': 'The recursive permitted-resource CTE used default collation for its parameter and C collation in its recursive term, causing SQLSTATE 42P21', 'resolution': 'Cast the root parameter to text with the database-specific matching collation; final PostgreSQL filtering, pagination, authorization, and wire tests passed'},
    {'id': 'query-fixture-late-commit-block', 'kind': 'test_fixture_deadlock', 'evidence': record('postgres-first.jsonl'), 'observation': 'Two newly inserted documents contended on the existing collection-membership lock before the intended late-finalization experiment', 'resolution': 'Used updates of two pre-existing independent documents and a bounded context; captured the initial stack before terminating the stalled test and removed its isolated schema; final normal and race variants passed'},
    {'id': 'shared-registration-unused-import', 'kind': 'compile_failure', 'evidence': record('shared-registration-compile-first-transcribed.log'), 'observation': 'The compatibility acknowledge route refactor left an unused fmt import', 'resolution': 'Removed the import; frozen API and application builds passed', 'provenance': 'Recorded tool output transcribed by the business specialist; not a rerun'},
    {'id': 'query-control-legacy-fixture', 'kind': 'regression_test_failure', 'evidence': record('final-control-initial.jsonl'), 'observation': 'The simulated migration-four database retained migration-seven/eight tables after deleting their migration records; opening it failed because sf_query_state already existed', 'resolution': 'Added every post-four query and business table to the historical fixture cleanup, created immutable snapshot v2, rebuilt and reran the entire control package', 'failed_input_manifest_sha256': snapshot_change['v1_manifest_sha256'], 'fixed_input_manifest_sha256': snapshot_change['v2_manifest_sha256'], 'production_source_change': False},
]

report = {
    'schema_version': 1,
    'role': 'phase4_query_subscriptions',
    'completed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'objectives': ['E4-01', 'E4-02'],
    'status': 'specialist_verification_passed_on_recorded_snapshots',
    'acceptance_scope': 'Query/store/application/API verification and selected adjacent control/cloud receipt regressions are complete; the coordinator performs later combined-source, frontend, business-flow, and independent phase acceptance',
    'summary': {'normal_leaf_test_passes': normal_count, 'race_leaf_test_passes': race_count, 'final_failed': 0, 'final_skipped': 0, 'test_count_method': 'Count only terminal test names without child test names; omit package and parent-suite pass events', 'compiled_packages_in_input_manifest': 682, 'source_inputs': 3304, 'local_inputs': 248, 'generated_artifacts': artifacts['generated_count'], 'contracts': audit['workspace_contract_validation']['result']},
    'implementation': {
        'queries': ['entities', 'alarms', 'executions', 'trend'],
        'entrypoints': ['POST /api/sf/v1/queries/{kind}', 'POST /api/sf/v1/queries/{kind}/events', 'GET /events compatibility route delegates to the same query service'],
        'storage': 'Migration 00007 adds historical projection intervals, resource candidates, a committed log and generation/authentication state. Business writes, projection changes and query log records commit in one transaction after the global commit-order lock. Migration 00008 and business API/worker/model registration were integrated for the business specialist',
        'pagination': 'Database-side filters and resource candidates, sort_ms DESC/id ASC with SQLite BINARY and PostgreSQL C collation, snapshot-bound signed keyset tokens, current resource and execution authorization before return',
        'rebuild': 'Current documents, hot/archive observations, rollups, revisions and gaps recreate the projection in one atomic generation switch; version precedence preserves the newest derived observation; cancellation or error preserves the previous generation',
        'subscriptions': 'One per-service poller shares current windows only for matching canonical query and current identity/session/grant scope. Snapshot, coalesced committed-window delta, checkpoint and reset events use signed opaque cursor strings. Deletion/removal uses a newer commit revision. Reconnect reconstructs the prior window before comparing the current window',
        'permissions': 'Database candidates require all row resources within currently authorized ancestry; final entity/observation checks and Control.Detail cover current and historical execution resources. User, session, delegation, grant/bundle and ancestry changes invalidate the bound query scope and clear obsolete cache',
        'write_paths': {'direct_and_http': 'Entity and business application writes flow through Tx.Put, SetEphemeral and Delete hooks', 'native_alarm': 'native_alarm_state updates reproject the linked alarm and expose independent native state', 'cloud_receipts': 'Immutable command evidence inserts mark the execution projection even when its business status/version remains unchanged', 'task_recovery': 'Execution and business task recovery uses the same domain writes; existing entire-control-package recovery regressions passed on v2', 'revisions_and_retention': 'Observation inserts, revisions, rollups, raw retention, archive expiry and rebuild preserve query history and invalidate superseded or deleted current rows'},
        'precision': 'Projection payloads retain exact JSON numbers; tests preserve 9007199254740993 and 18446744073709551615 through query/rebuild paths. Decimal query revision strings and opaque cursor strings avoid JavaScript number conversion',
        'public_registration_files': ['Platform/internal/api/definition_routes.go', 'Platform/internal/api/contracts.go', 'Platform/internal/api/server.go', 'Platform/internal/api/business.go', 'Platform/internal/app/workers.go', 'Platform/internal/store/migrations.go', 'Platform/pkg/compatibility/manifest.json', 'Platform/cmd/sf-contracts/main.go'],
    },
    'finite_operating_budgets': {'default_page_rows': 100, 'ordinary_page_rows_max': 500, 'trend_page_rows_max': 2000, 'resource_filters_max': 256, 'trend_keys_max': 128, 'trend_range_max_days': 1095, 'auxiliary_rows_per_type_max': 2000, 'retained_commits': 4096, 'history_compaction_interval_seconds': 60, 'shared_query_groups_max': 128, 'subscribers_max': 256, 'queued_events_per_subscriber': 32, 'poll_milliseconds': 250, 'heartbeat_seconds': 15, 'write_and_flush_deadline_seconds': 3, 'rebuild_timeout_seconds': 30, 'rebuild_fact_batch_rows': 256},
    'measured_cases': {
        'shared_subscribers': {'subscribers': 12, 'initial_page_reads': 1, 'changed_window_page_reads': 1, 'databases': ['SQLite', 'PostgreSQL'], 'evidence': record('final-application.jsonl')},
        'competing_queue_drain': {'interleavings': 10000, 'result': 'completed without stranded sender/receiver in normal and race runs'},
        'wire_idle_recovery': {'protocols': ['HTTP/1.1', 'HTTP/2.0'], 'normal_observed_idle_seconds': [15.001697458, 15.001820042], 'checks': ['same connection survives idle heartbeat', 'subsequent HTTP mutation yields delta', 'Last-Event-ID recovers a disconnected update', 'current user revocation yields clear reset', 'parallel clients share window reads'], 'evidence': record('final-api.jsonl')},
        'slow_socket': {'snapshot_approximate_mb': 8.4, 'normal_release_seconds': 3.005962167, 'race_release_seconds': 3.002328167, 'other_client_received_delta': True, 'evidence': record('final-race-api.jsonl')},
        'rebuild_cancel_and_writer': {'database': 'PostgreSQL 18', 'seed_rows': 400, 'controlled_writer_barrier_milliseconds': 50, 'observed_cancel_milliseconds': 87.411833, 'observed_writer_commit_after_release_milliseconds': 694.043458, 'old_snapshot_visible_during_rebuild': True, 'cancel_rollback_preserved_old_snapshot': True, 'writer_finalized_in_new_generation': True, 'evidence': record('final-store.jsonl')},
        'late_prepared_commit': {'observed_old_sequence': 5, 'generation_switch_sequence': 6, 'late_commit_sequence': 7, 'actual_postgres_sse_stream_verified': True},
    },
    'source_and_build_provenance': {
        'v1_manifest': record('final-input-manifest-initial.json'), 'v2_manifest': record('final-input-manifest-v2.json'),
        'snapshot_relationship': snapshot_change, 'initial_failed_control_binary_preserved': True,
        'normal_run_binding': 'Store, application, API and cloudsync normal runs used v1; the final full control rerun, application binary builds, contract generator and all race runs used v2. The sole v1/v2 source difference is the historical control migration test fixture',
        'source_selection': 'go list -deps -test -json selected Go/C/assembly/embed compilation inputs, augmented by both module/lock sets and validation/contract inputs; non-source macOS metadata and build caches excluded',
        'all_local_and_external_input_hashes_rechecked': audit['local_inputs'] + [audit['external_inputs']],
        'build_and_test_commands': record('final-verification-runs.json'), 'go_list': record('final-go-list.jsonstream'), 'binaries': runs['binaries'],
        'runtime': {**runs['runtime'], 'go': 'go1.27.1 darwin/arm64', 'database_fixture': 'isolated PostgreSQL 18.6 container; every fixture owns a schema and one management connection', 'credentials': 'read privately into child-process environment; no plaintext credentials in evidence'},
        'helper_scripts': [record(name) for name in ['sf-query-freeze.py', 'sf-query-final-verify.py', 'sf-query-final-artifacts.py', 'sf-query-final-audit.py', 'sf-query-report.py']],
    },
    'tests': checks,
    'first_failures': first_failures,
    'review_findings': [
        {'id': 'P4-Q01', 'observation_source': 'coordinator source review before final verification', 'finding': 'A write deadline left armed after a completed SSE event can expire an idle HTTP/2 stream', 'fix': 'Clear the deadline when write/flush completes, including failure returns', 'verification': 'Actual HTTP/1.1 and HTTP/2 connections cross the 15-second heartbeat and then deliver mutations in normal and race runs', 'pre_fix_runtime_failure_recorded': False},
        {'id': 'P4-Q02', 'observation_source': 'coordinator source review before final verification', 'finding': 'len(channel) followed by a blocking receive can deadlock against a competing consumer while holding the hub mutex', 'fix': 'Use a nonblocking select drain before slow-consumer reset/close', 'verification': '10000 competing reader interleavings and real slow socket runs passed normally and under race', 'pre_fix_runtime_failure_recorded': False},
        {'id': 'P4-Q03', 'observation_source': 'actual PostgreSQL test', 'finding': 'Recursive resource candidate collation mismatch', 'verification': 'Both stores produce the same stable Unicode keyset order and PostgreSQL permission filtering passes'},
    ],
    'contracts': {
        'state': 'provisional_frozen_v2_contracts_pending_later_combined_source_refresh',
        'generated_artifacts': record('generated-artifacts.json'), 'deterministic_generation': True,
        'workspace_write': 'Copied 149 artifacts from frozen v2 after verifying repeat generation byte for byte; workspace validation passed for 146 schemas, 24 examples and 91 API operations',
        'query_frontend_contract': audit['query_contract'],
        'workspace_drift': record('workspace-drift-after-freeze.json'),
        'later_public_type_changes': {'file': 'Platform/pkg/model/templates.go', 'type': 'TemplateInstance', 'new_required_fields': ['device_version', 'configuration_id', 'configuration_version'], 'affected_generated_types': ['TemplateInstance', 'TemplateBatchInput', 'TemplateBatch', 'OpenAPI', 'api-types.ts'], 'owner_next': 'The AI/public-registration successor regenerates from the business-final combined source before frontend integration'},
        'frontend_notes': 'Use POST streaming fetch with Authorization and Last-Event-ID; parse exact JSON for pages and SSE; apply metadata even when a heartbeat status delta reuses the cursor; clear caches/details on reset clear=true; renew fixed trend time ranges as the visible live range moves; legacy GET /events now emits the new query event shape',
    },
    'historical_migrations': {'versions_1_through_6': audit['migration_versions_1_through_6'], 'per_file_evidence': record('migrations-preserved.json'), 'query_migration': 7, 'business_migration': 8, 'next_reserved_ai_migration': 9},
    'final_audit': record('final-audit.json'),
    'resource_cleanup': {'tests_and_build_sessions_finished': True, 'owned_persistent_services_created': 0, 'remaining_query_test_schemas': audit['postgres_cleanup']['remaining_owned_schemas'], 'postgres_race_slot_released_to_coordinator': True, 'workspace_free_bytes': audit['workspace_free_bytes'], 'preserved_test_binaries_location': '/private/tmp'},
    'remaining_phase_work': ['Independent coordinator acceptance against the later combined source', 'Business specialist actual cloud/edge/native callbacks and operation workflows, with query projection observations included where applicable', 'Frontend migration and browser acceptance against the refreshed combined contracts', 'AI role implementation and next public registration/contract generation'],
    'scope_limits': ['Concurrency/cancellation measurements use finite controlled fixtures and are not a production throughput capacity test', 'Explorer runs before source freeze retain their first failure logs; only final recorded v1/v2 verification claims bind to immutable source and binary manifests', 'No tests were rerun to follow ongoing business edits after the agreed frozen capture', 'Full UOS installation/recovery, continuous one/three-node offline 24-hour tests, constrained-link specialist trials and actual 24-hour capacity/runtime were excluded by the user for this overall round'],
}
if normal_count != 131 or race_count != 34: raise RuntimeError((normal_count, race_count))
path = repo / '.local/evolution/query-subscriptions-verified.json'
path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'report': str(path), 'sha256': sha(path), 'normal_leaf_passes': normal_count, 'race_leaf_passes': race_count, 'failed': 0, 'skipped': 0}, ensure_ascii=False))
