import datetime
import hashlib
import json
import pathlib
import re

repo = pathlib.Path('/path/to/SmartFactory-2026')
evidence = repo / '.local/evolution/business-evidence'


def read(path):
    return json.loads(pathlib.Path(path).read_text())


def artifact(path):
    path = pathlib.Path(path)
    return {'path': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size}


v2 = read(evidence / 'final-verification-runs.json')
v3 = read(evidence / 'final-v3-verification-runs.json')
snapshot = pathlib.Path(v3['snapshot'])
copy = read(evidence / 'final-source-copy-v3-manifest.json')
inputs = read(evidence / 'final-v3-actual-inputs.json')
source_mismatches = [name for name, digest in copy['files'].items()
                     if artifact(snapshot / name)['sha256'] != digest]
input_mismatches = [name for name, value in inputs['inputs'].items()
                    if value['local'] and artifact(snapshot / name)['sha256'] != value['sha256']]
tag_only = {}
for name in copy['changes']:
    if name.endswith('.go') and not name.endswith('_test.go'):
        before = (pathlib.Path(copy['previous_snapshot']) / name).read_text()
        after = (snapshot / name).read_text()
        tag_only[name] = re.sub(r'`[^`]*`', '<tag>', before) == re.sub(r'`[^`]*`', '<tag>', after)

test_suites = []
for label, run_report in [('v2_normal_and_race', v2), ('v3_request_contract_and_sqlite', v3)]:
    for run in run_report['runs']:
        assert run['exit_code'] == 0, run['name']
        assert artifact(run['log'])['sha256'] == run['log_sha256'], run['name']
        tests = []
        for line in pathlib.Path(run['log']).read_text().splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get('Test') and event.get('Action') in ['pass', 'fail', 'skip']:
                tests.append({'name': event['Test'], 'status': event['Action'], 'elapsed_seconds': event.get('Elapsed')})
        if tests:
            assert all(test['status'] == 'pass' for test in tests), run['name']
            test_suites.append({'snapshot_group': label, 'run': run['name'], 'log': artifact(run['log']),
                                'top_level_tests': sum('/' not in test['name'] for test in tests), 'tests': tests})
    for name, binary in run_report['binaries'].items():
        assert artifact(name)['sha256'] == binary['sha256'], name

baseline_path = pathlib.Path('/private/tmp/smartfactory-phase3-accepted-final-20261005/manifest.json')
baseline = read(baseline_path)
migrations = []
for name, value in baseline['files'].items():
    if re.search(r'Platform/internal/store/migration0000[2-6]\.go$', name) or re.search(r'Platform/internal/store/migrations/(sqlite|postgres)/00001_baseline\.sql$', name):
        migrations.append({'path': name, 'baseline_sha256': value['sha256'], 'final_sha256': artifact(snapshot / name)['sha256']})
assert len(migrations) == 7 and all(item['baseline_sha256'] == item['final_sha256'] for item in migrations)

openapi = read(snapshot / 'contracts/1.0/openapi.json')
route_source = (snapshot / 'Platform/internal/api/business_routes.go').read_text()
operation_ids = re.findall(r'huma\.Register\(api, (?:get|operation)\("([^"]+)"', route_source)
found = {operation.get('operationId') for path in openapi['paths'].values() for operation in path.values() if isinstance(operation, dict)}
assert len(operation_ids) == 19 and all(name in found for name in operation_ids)
request_names = ['AlarmActionInput', 'CreateWorkOrderInput', 'WorkOrderActionInput', 'HandoverInput',
                 'HandoverEvidence', 'SemanticInput', 'ImpactInput', 'SaveDeviceConfigurationInput',
                 'SaveConnectorConfigurationInput', 'RetryTemplateBatchInput', 'Parameters', 'Request',
                 'TemplateInstance', 'TemplateBatchInput']
required = {}
for name in request_names:
    document = read(snapshot / 'contracts/1.0' / (name + '.schema.json'))
    actual = sorted(openapi['components']['schemas'][name].get('required', []))
    expected = sorted(document['$defs'][name].get('required', []))
    assert actual == expected, name
    required[name] = actual

native_path = repo / '.local/evolution/business-native-verified.json'
native = read(native_path)
native_work = pathlib.Path(native['work_directory'])
native_rebuild = read(evidence / 'native-rebuild-comparison.json')
assert native['all_checks_passed'] and native_rebuild['original_path_bytes_reproduced']
assert artifact(native_work / 'api-native.test')['sha256'] == native['binary_sha256']
native_integrity = read(native_work / 'compiled-source/retained-source-integrity.json')
assert not native_integrity['mismatches']
fixture = read(evidence / 'final-v3-fixture-verification.json')
cleanup = read(evidence / 'final-cleanup.json')
assert fixture['all_checks_passed'] and cleanup['all_checks_passed']
assert not source_mismatches and not input_mismatches and all(tag_only.values())

failures = [
    ('initial malformed business struct tags', repo / '.local/evolution/query-evidence/first-shared-build-transcribed.log', 'Corrected the struct tags before compiling business implementations'),
    ('shared registration retained an unused fmt import', pathlib.Path('/private/tmp/smartfactory-business-shared-registration-compile-first.log'), 'Shared owner removed the unused import; the retained file is explicitly transcribed tool output'),
    ('test supplied pointers for scalar draft expected versions', pathlib.Path('/private/tmp/smartfactory-business-sqlite-first.log'), 'Corrected test inputs to match the formal application input'),
    ('signed-bundle user source version differed from the local document revision', pathlib.Path('/private/tmp/smartfactory-business-sync-first.log'), 'Authorization now pins the actual local user document revision and canonical current identity'),
    ('template recovery attempted to reuse a historically occupied draft identity', pathlib.Path('/private/tmp/smartfactory-business-additional-first.log'), 'Template identity checks include historical versions; recovery uses the formal rename path and historical identity remains a recorded failure'),
    ('native readiness login submitted the bootstrap wrapper', repo / '.local/evolution/business-native-first-failure.json', 'Passed the actual username and password fields, then completed the actual CE lifecycle test'),
    ('first final source copy omitted five DataTransfer observability inputs', evidence / 'first-snapshot-failure.json', 'Preserved the first copy and failure, added the five inputs in v2, then captured actual compiler input lists'),
    ('P4-B06 required input fields were optional in Huma OpenAPI', evidence / 'required-input-contract-before.json', 'Added explicit required tags and accurate optional serialization; all 14 schemas agree and 53 missing-field requests fail HTTP validation'),
    ('sandbox process inventory was unavailable', evidence / 'cleanup-first-failure.json', 'Used retained subprocess wait results, completed tool sessions, closed loopback ports and Docker/PostgreSQL inventory'),
]

report = {
    'phase': 4, 'role': 'business_workflows', 'status': 'verified',
    'generated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'scope': ['E4-04', 'E4-05', 'E4-06', 'E4-08', 'E4-09'],
    'all_checks_passed': True, 'remaining_role_work': [],
    'implementation': {
        'E4-04': {'result': 'Three protocol catalogs drive validation, precision-preserving configuration round trips and extension preservation; formal versioned connector configuration and public application receipts synchronize through mTLS; encrypted credentials use per-version references',
                  'evidence_tests': ['TestConfigurationMetadataRoundTripAndValidation', 'TestTLSJSONLegacyNamesAndCanonicalRoundTrip', 'TestBusinessConnectorVersionPayloadAndDelayedOldTask', 'TestConfigurationAndRevisionRestoreAcrossRestart', 'TestBusinessConnectorSecretsAreFixedByVersion', 'TestBusinessConnectorPublicConfigurationMTLSSynchronization']},
        'E4-05': {'result': 'Draft and published revisions produce identity-bearing semantic changes with exact numeric values, explicit null/missing values and duplicate connection multiplicity; impact traversals detect cycles, missing/restricted references and exhausted budgets while retaining current resource authorization',
                  'evidence_tests': ['TestBusinessTemplateBatchesAndSemanticChanges', 'TestBusinessSemanticPermissionsAndDuplicateConnections', 'TestBusinessHTTPProtocolTemplatesSemanticAndHandover']},
        'E4-06': {'result': 'Manual alarm actions use semantic action versions, atomic audit/idempotency records and immutable causal source operations; continuous samples preserve a reviewed action; cloud/edge conflicts converge; actual native acknowledgement and clearance retain manual handling and the local rule condition',
                  'evidence_tests': ['TestBusinessWorkflowSQLite', 'TestBusinessWorkflowPostgres', 'TestBusinessConcurrentAlarmSQLite', 'TestBusinessConcurrentAlarmPostgres', 'TestBusinessHTTPContinuousAlarmReviewAndNativeCallback', 'TestBusinessAlarmMTLSConcurrentAssignmentConverges', 'TestBusinessAlarmSyncRejectsIdentityAndRevokedActor', 'TestBusinessNativeCEAlarmLifecycle', 'TestBusinessNativePollFairnessRotationAndRecovery', 'TestBusinessNativePollDeadlineResumesAfterSlowMapping', 'TestBusinessNativeWorkerImmediatePeriodicAndFailureRecovery']},
        'E4-08': {'result': 'Work orders and handovers validate the current member, department, associated alarms and all execution evidence devices; responsibility changes, reviewed evidence, audit and request identity commit atomically; concurrent handovers produce one success and one conflict',
                  'evidence_tests': ['TestBusinessWorkOrderChecksExecutionEvidenceAndRevokedMember', 'TestBusinessWorkOrderRejectsEvidenceAddedAfterAuthorization', 'TestBusinessConcurrentHandoverSQLite', 'TestBusinessConcurrentHandoverPostgres', 'TestBusinessHandoverReviewedAlarmDuringContinuousSampling', 'TestBusinessHTTPProtocolTemplatesSemanticAndHandover']},
        'E4-09': {'result': 'Five versioned templates prepare 25 independently validated drafts, bind reviewed device/configuration versions and actual connector action mappings, preserve failed batch attempts, reject stale or private inputs and historical identity collisions, and support explicit idempotent retries',
                  'templates': {'climate-ventilation': 14, 'infrared-lighting': 2, 'hazardous-gas': 6, 'agv-obstacle': 2, 'goods-counting': 1},
                  'evidence_tests': ['TestBusinessTemplateBatchesAndSemanticChanges', 'TestBusinessTemplateValidationRecoveryAndPinnedInputs', 'TestBusinessHTTPRequiredInputFieldsAndDefaults']},
    },
    'source_provenance': {
        'final_snapshot': str(snapshot), 'source_file_count': len(copy['files']),
        'actual_compiler_input_count': inputs['input_count'], 'local_compiler_input_count': inputs['local_input_count'],
        'source_manifest': artifact(evidence / 'final-source-copy-v3-manifest.json'),
        'actual_inputs': artifact(evidence / 'final-v3-actual-inputs.json'),
        'source_mismatches_after_verification': source_mismatches, 'local_input_mismatches_after_verification': input_mismatches,
        'v2_snapshot': v2['snapshot'], 'v2_source_manifest': artifact(evidence / 'final-source-copy-manifest.json'),
        'v2_actual_inputs': artifact(evidence / 'final-actual-inputs.json'),
        'v2_to_v3_diff': artifact(evidence / 'v2-to-v3-business-input-contract.diff'),
        'v2_to_v3_production_changes_are_struct_tags_only': tag_only,
        'go_version': inputs['go_version'], 'module_locks': {name: artifact(snapshot / name) for name in ['Platform/go.mod', 'Platform/go.sum', 'DataTransfer/go.mod', 'DataTransfer/go.sum']},
        'phase3_baseline': artifact(baseline_path), 'migrations_1_through_6_unchanged': migrations,
        'registered_business_migration': 8,
        'scope_exclusions': copy['exclusions'],
    },
    'verification': {
        'v2_normal_postgres_race_builds': artifact(evidence / 'final-verification-runs.json'),
        'v3_application_http_contract_builds': artifact(evidence / 'final-v3-verification-runs.json'),
        'v2_command_count': len(v2['runs']), 'v3_command_count': len(v3['runs']),
        'test_suites': test_suites,
        'v2_binaries': v2['binaries'], 'v3_binaries': v3['binaries'],
        'postgres': {'version': '18.6', 'application_max_open': 3, 'application_max_idle': 1, 'management_max_open': 1, 'isolated_schema_prefix': 'sf_business_', 'package_parallelism': 1},
        'request_contracts': {'schemas': required, 'missing_required_http_422_cases': 53, 'legal_zero_and_optional_defaults_verified': True},
    },
    'contracts': {'root': str(snapshot / 'contracts/1.0'), 'business_operation_count': len(operation_ids),
                  'business_operation_ids': operation_ids, 'schema_validation_passed': True,
                  'files': {str(path.relative_to(snapshot)): artifact(path) for path in sorted((snapshot / 'contracts/1.0').glob('*.json'))}},
    'native_validation': {
        'report': artifact(native_path), 'binary': artifact(native_work / 'api-native.test'),
        'build_started_at': native['build_started_at'], 'build_completed_at': native['build_finished_at'],
        'build_log': artifact(native_work / 'build.log'), 'test_log': artifact(native_work / 'native-test.log'),
        'test': 'TestBusinessNativeCEAlarmLifecycle', 'image': native['image'], 'platform': native['platform'],
        'architecture_emulation': native['architecture_emulation'], 'budget': native['budget'],
        'retained_source': artifact(native_work / 'compiled-source/retained-source-integrity.json'),
        'actual_inputs': artifact(evidence / 'native-actual-inputs.json'),
        'rebuild_comparison': artifact(evidence / 'native-rebuild-comparison.json'),
        'original_binary_reproduced_byte_for_byte': native_rebuild['original_path_bytes_reproduced'],
        'source_scope': 'Actual CE lifecycle uses the preserved original native build. B05 fair rotation and deadline changes use v2 normal/race HTTP and formal worker tests. B06 changes only request serialization/schema tags and uses v3 HTTP/application/contracts verification.',
        'cleanup': {key: native[key] for key in ['final_container_state', 'remove_exit_code', 'database_removed']},
    },
    'frontend_delivery': {
        'contract_document': artifact(repo / 'docs/evolution/business-frontend-contract.md'),
        'fixture_source': artifact(snapshot / 'Platform/cmd/sf-business-fixture/main.go'),
        'fixture_binary': artifact(snapshot / 'binaries/sf-business-fixture'),
        'v3_http_verification': artifact(evidence / 'final-v3-fixture-verification.json'),
        'v2_http_verification': artifact(evidence / 'final-fixture-verification.json'),
        'v3_runs': fixture['runs'],
        'credentials': 'Each new fixture directory owns a random private-credentials.json with mode 0600; no credential values are included in this report',
    },
    'first_failures_and_repairs': [{'failure': title, 'evidence': artifact(path), 'repair': repair} for title, path, repair in failures],
    'cleanup': artifact(evidence / 'final-cleanup.json'),
    'runtime_scope': {'actual_completed': ['SQLite and PostgreSQL application transactions', 'race detector on concurrent business and synchronization paths', 'mTLS cloud/edge replication', 'actual CE alarm lifecycle', 'formal polling worker immediate and periodic recovery', 'effective DataTransfer configuration persistence', 'isolated cloud and edge HTTP fixtures'],
                      'excluded_by_user_for_full_program': ['complete UOS install/recovery', 'one-node and three-node continuous 24-hour disconnected operation', 'restricted-link campaign', 'actual 24-hour capacity and continuous runtime'],
                      'frontend_browser_review_owner': 'frontend specialist',
                      'combined_working_tree_contract_generation_owner': 'phase4_ai_evidence; final business DTOs and required tags have been handed off'},
}
(repo / '.local/evolution/business-workflows-verified.json').write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
print(json.dumps({'report': str(repo / '.local/evolution/business-workflows-verified.json'),
                  'status': report['status'], 'source_files': len(copy['files']),
                  'test_suites': len(test_suites), 'top_level_test_executions': sum(suite['top_level_tests'] for suite in test_suites),
                  'native_binary_bytes_reproduced': native_rebuild['original_path_bytes_reproduced']}))
