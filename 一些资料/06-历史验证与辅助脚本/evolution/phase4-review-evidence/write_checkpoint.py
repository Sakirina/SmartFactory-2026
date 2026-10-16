#!/usr/bin/env python3
"""Record the completed backend review without declaring phase acceptance."""
import collections
import datetime
import difflib
import hashlib
import json
import pathlib
import shutil

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
OUT = ROOT / '.local/evolution/phase4-review-evidence'
BASE = pathlib.Path('/private/tmp/smartfactory-phase4-independent-20261005')
WS = BASE / 'workspace'
cache = {}

def sha(path):
    path = pathlib.Path(path)
    if str(path) not in cache:
        digest = hashlib.sha256()
        with path.open('rb') as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(block)
        cache[str(path)] = digest.hexdigest()
    return cache[str(path)]

def ref(path):
    path = pathlib.Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def write(path, value):
    pathlib.Path(path).write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')

def read(path):
    return json.loads(pathlib.Path(path).read_text())

now = datetime.datetime.now(datetime.timezone.utc).isoformat()
runs = [read(path) for path in sorted(OUT.glob('*.metadata.json'))]
by_label = {run['label']: run for run in runs}
groups = {}
for name, prefix in [('ordinary_platform_initial', 'ordinary-Platform-'), ('ordinary_datatransfer', 'ordinary-DataTransfer-'), ('postgres', 'postgres-'), ('limited_race', 'race-')]:
    group = [run for run in runs if run['label'].startswith(prefix)]
    groups[name] = {'runs': [run['label'] for run in group], 'package_runs': len(group), 'top_level': {status: sum(run['counts']['top_level'][status] for run in group) for status in ['pass', 'fail', 'skip']}, 'races': sum(run['counts']['races'] for run in group)}

tests = []
for run in runs:
    outcomes = {}
    for line in pathlib.Path(run['stdout']['path']).read_text().splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        if event.get('Test') and event.get('Action') in ['pass', 'fail', 'skip']:
            outcomes[(event.get('Package', ''), event['Test'])] = event['Action']
    tests += [{'run': run['label'], 'package': package, 'test': test, 'status': status} for (package, test), status in outcomes.items()]
write(OUT / 'test-outcomes.json', {'generated_at': now, 'counting_note': 'Top-level counts are executions, not unique tests; nested skips and outcomes are recorded individually here.', 'outcomes': tests})

reference_checks = []
for run in runs:
    for key in ['stdout', 'stderr', 'binary', 'build_inputs', 'overlay']:
        item = run.get(key)
        if item:
            path = pathlib.Path(item['path'])
            reference_checks.append({'run': run['label'], 'kind': key, 'path': str(path), 'expected_sha256': item['sha256'], 'matched': path.is_file() and sha(path) == item['sha256']})
    for item in run.get('overlay_inputs', []):
        saved = item.get('preserved_path') or run.get('retained_overlay_source') or item['path']
        path = pathlib.Path(saved)
        reference_checks.append({'run': run['label'], 'kind': 'overlay_input', 'original_path': item['path'], 'path': str(path), 'expected_sha256': item['sha256'], 'matched': path.is_file() and sha(path) == item['sha256']})

for manifest_name in ['snapshot.json', 'build-inputs.json', 'auxiliary-runtime-supplement.json']:
    for item in read(OUT / manifest_name)['files']:
        path = pathlib.Path(item.get('preserved_path', item['path']))
        reference_checks.append({'manifest': manifest_name, 'path': str(path), 'expected_sha256': item['sha256'], 'matched': path.is_file() and sha(path) == item['sha256']})

supplements = []
for label, relative, expected in [
    ('independent-ai-build-v2', 'overlays/independent-ai-build-v2/independent_ai_review_test.go', '381b8ba8eb2e7bda27f9b6b1e0f6c9b516db945df872b614d5d3d5f09ba0bed8'),
    ('independent-ai-build-v3', 'overlays/independent-ai-build-v3/independent_ai_review_test.go', 'b52e3c3a36f364aed1712384504072ed039e3daaf6dd5ca591626ab893d06f00'),
    ('idle-regression-build', 'overlays/idle-regression-build/idle_test.go', '952b8d0d41a4800b4a11ae725b287d21179bb115a556fb470c517f0efbc99264'),
]:
    path = BASE / relative
    entry = {'build': label, 'retained_source': ref(path), 'expected_sha256': expected, 'matched': sha(path) == expected}
    supplements.append(entry)
    reference_checks.append({'run': label, 'kind': 'retained_source_supplement', 'path': str(path), 'expected_sha256': expected, 'matched': entry['matched']})
write(OUT / 'source-retention-supplement.json', {'entries': supplements, 'initial_ai_compile_failure': 'The first overlay used package ai; the existing external fixture uses package ai_test. Compilation stopped before a binary was produced. The corrected v2 and extended v3 sources and binaries are preserved separately.'})

changes = []
diff = []
for relative in ['Platform/internal/api/control_budget_test.go', 'Platform/internal/cloudsync/idle_test.go']:
    old, current = WS / relative, ROOT / relative
    saved = BASE / 'review-test-final' / relative
    saved.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(current, saved)
    changes.append({'path': relative, 'before': ref(old), 'after': ref(saved), 'current_path': str(current)})
    diff += difflib.unified_diff(old.read_text().splitlines(True), current.read_text().splitlines(True), fromfile='snapshot/' + relative, tofile='review-test-final/' + relative)
(OUT / 'review-test-changes.patch').write_text(''.join(diff))
write(OUT / 'review-test-changes.json', {'files': changes, 'patch': ref(OUT / 'review-test-changes.patch'), 'scope': 'Only these two repository test files were changed by the independent reviewer; remaining reviewer files are local evidence and temporary test overlays.', 'control_budget': 'Added task IDs, queue, scheduling, attempt and callback-entry diagnostics; retained the original deadlines and behavior assertions.', 'cloudsync_idle': 'Retained zero writes across 25 idle exchanges. Heartbeat verifies the edge-a source version and timestamp, one sources commit, query watermark and revision, equal projected source payload, four permitted query-table changes and unchanged other domain rows.'})

def outcome_refs(patterns):
    return [row for row in tests if row['status'] == 'pass' and any(pattern in row['test'] for pattern in patterns)]

finding_patterns = {
    'P4-Q01': ['TestQuerySSEHTTPIdleAndRecovery', 'TestQuerySSESlowSocketDoesNotBlockOtherClients'],
    'P4-Q02': ['TestQueryDrainCompetingReader', 'shared-window-deletion-and-restart-resume'],
    'P4-Q03': ['TestQueryApplicationPostgres', 'TestQuerySSEPostgresSnapshotDeltaResumeReset', 'TestQueryLateCommitAndConcurrentRebuildPostgres', 'TestQueryAllKindsStableKeyset'],
    'P4-B01': ['TestBusinessHandoverReviewedAlarmDuringContinuousSampling', 'TestBusinessHTTPContinuousAlarmReviewAndNativeCallback', 'TestBusinessConcurrentAlarm'],
    'P4-B02': ['TestBusinessConnectorSecretsAreFixedByVersion'],
    'P4-B03': ['TestBusinessWorkOrderChecksExecutionEvidenceAndRevokedMember', 'TestBusinessWorkOrderRejectsEvidenceAddedAfterAuthorization'],
    'P4-B04': ['TestTLSJSONLegacyNamesAndCanonicalRoundTrip', 'TestConfigManagerAppliesDeviceUpdatesAndProtectsRevision'],
    'P4-B05': ['TestBusinessNativeWorkerImmediatePeriodicAndFailureRecovery', 'TestBusinessNativePollFairnessRotationAndRecovery', 'TestBusinessNativePollDeadlineResumesAfterSlowMapping'],
    'P4-B06': ['TestBusinessHTTPRequiredInputFieldsAndDefaults'],
    'P4-A01': ['TestInvestigationDirectDevicePermissionUsesQueryAuthorization'],
}
findings = [{'id': identifier, 'status': 'backend_regression_passed_pending_final_combination', 'tests': outcome_refs(patterns)} for identifier, patterns in finding_patterns.items()]
findings += [
    {'id': 'P4-A02', 'status': 'independently_reproduced_waiting_original_specialist_fix', 'reproductions': ['independent-ai-original-asset-deletion', 'independent-ai-original-asset-deletion-v3'], 'retained_sources': supplements[:2], 'observed': 'After definition rebinding and deletion of its original resource, a still-present explicit grant leaves evidence access valid with HTTP 200 and 1859 response bytes. Investigation detail also retains 178 answer bytes, one input message, 29 argument bytes and three resources.'},
    {'id': 'P4-F01', 'status': 'specialist_reports_fixed_independent_browser_retest_pending', 'scope': 'Delayed HTTP snapshot overwrites an already-applied SSE update/new row/removal; independently check initial query, manual refresh and the opaque resume cursor after freeze.'},
    {'id': 'P4-F02', 'status': 'specialist_reports_fixed_independent_browser_retest_pending', 'scope': 'JSON correction, deletion of an invalid parameter row and same-name fields in different metadata sections require independent page verification after freeze.'},
]

criteria = [
    ('E4-01', '查询读模型与分页', 'backend_verified_pending_final_combination', ['TestQueryProjection', 'TestQueryAllKindsStableKeyset', 'TestQueryApplication', 'TestQueryHTTPPagination', 'TestQueryLateCommit', 'TestQueryRebuild'], '数据库与 HTTP 查询通过；最终页面筛选、缓存与授权切换随冻结版本继续检查'),
    ('E4-02', '可恢复增量订阅', 'backend_verified_pending_browser', ['TestQuerySSE', 'TestQueryApplication', 'TestQueryDrainCompetingReader', 'TestQueryTrendExact'], 'HTTP/1.1、HTTP/2 经过心跳后的增量和慢连接测试通过；页面中 HTTP 与 SSE 次序等待独立复验'),
    ('E4-03', '前端业务模块与状态组织', 'waiting_frontend_freeze', [], '页面、生成客户端、TanStack Query、编辑保持和权限身份将在前端冻结后独立验证'),
    ('E4-04', '设备与节点参数表单', 'backend_verified_pending_browser', ['TestBusinessWorkflow', 'TestBusinessHTTPProtocol', 'TestTLSJSON', 'TestConfigManager'], '元数据、TLS 读取及往返通过；P4-F02 修正后表单仍待独立操作'),
    ('E4-05', '语义差异与影响分析', 'backend_verified_pending_browser', ['TestBusinessSemantic', 'TestBusinessTemplateBatchesAndSemanticChanges', 'TestBusinessHTTPProtocol'], '业务语义与资源授权回归通过，页面交互待检'),
    ('E4-06', '告警处置工作台', 'backend_verified_pending_browser_and_native_evidence_review', ['TestBusinessHTTPContinuousAlarm', 'TestBusinessHandoverReviewedAlarm', 'TestBusinessConcurrentAlarm', 'TestBusinessNativeWorker', 'TestNativeAlarm'], '本轮执行受控 HTTP 原生同步与 worker 检查；实际 CE 生命周期本轮条件跳过，专业固定版本证据另列，页面流程待检'),
    ('E4-07', '带依据的AI调查', 'blocked_on_P4_A02', ['TestInvestigation', 'TestResponsesHTTPAssistantPreciseQueryToolOutput'], 'P4-A02 仍可复现，原专业修正后需要复验依据、调查详情与页面导航'),
    ('E4-08', '工单与交接记录', 'backend_verified_pending_browser', ['TestBusinessWorkOrder', 'TestBusinessHandover', 'TestBusinessConcurrentHandover', 'TestBusinessWorkflow'], '当前资源、审计、并发版本及关联执行授权通过，完整页面流转待检'),
    ('E4-09', '场景模板与批量准备', 'backend_verified_pending_browser', ['TestBusinessTemplate', 'TestBusinessHTTPProtocol'], '模板验证、绑定权限、冲突与失败恢复回归通过，五类模板的独立页面操作待检'),
    ('E4-10', '图表与三套页面验证', 'waiting_frontend_freeze', [], 'ECharts 6、三套页面三种尺寸、精确提示与导出仍待本角色浏览器验证'),
]
criterion_rows = [{'id': identifier, 'title': title, 'status': status, 'note': note, 'tests': outcome_refs(patterns)} for identifier, title, status, patterns, note in criteria]

skipped = [row for row in tests if row['status'] == 'skip']
passed_names = {row['test'] for row in tests if row['status'] == 'pass'}
for row in skipped:
    row['has_pass_in_another_independent_run'] = row['test'] in passed_names
write(OUT / 'skipped-test-audit.json', {'tests': skipped, 'scope_note': 'PostgreSQL-specific cases were rerun with the private DSN; process helper entrypoints are exercised through their parent tests. External native CE, OPC UA, MQTT, controlled release/upgrade and long-run fixtures remain explicitly identified in the original logs. Prior specialist/phase acceptance is historical evidence, not counted as an independent rerun.', 'outside_requested_scope': ['完整 UOS 安装恢复', '1 节点和 3 节点连续断云 24 小时', '受限链路专项', '实际 24 小时容量与持续运行']})

write(OUT / 'artifact-integrity.json', {'generated_at': now, 'checks': reference_checks, 'checked_count': len(reference_checks), 'mismatches': [row for row in reference_checks if not row['matched']]})
write(OUT / 'run-index.json', {'generated_at': now, 'metadata': [ref(OUT / (run['label'] + '.metadata.json')) for run in runs], 'run_count': len(runs), 'groups': groups, 'test_outcomes': ref(OUT / 'test-outcomes.json'), 'source_retention_supplement': ref(OUT / 'source-retention-supplement.json')})

report = {
    'schema_version': 1,
    'kind': 'recoverable_checkpoint',
    'phase': 4,
    'reviewer': 'phase4_independent_review',
    'created_at': now,
    'status': 'awaiting_AI_fix_and_frontend_freeze',
    'final_acceptance': False,
    'repository': str(ROOT),
    'snapshot': ref(OUT / 'snapshot.json'),
    'build_inputs': ref(OUT / 'build-inputs.json'),
    'auxiliary_runtime_inputs': ref(OUT / 'auxiliary-runtime-supplement.json'),
    'run_index': ref(OUT / 'run-index.json'),
    'artifact_integrity': ref(OUT / 'artifact-integrity.json'),
    'source_retention_supplement': ref(OUT / 'source-retention-supplement.json'),
    'review_test_changes': ref(OUT / 'review-test-changes.json'),
    'specialist_evidence_audit': ref(OUT / 'specialist-evidence-audit.json'),
    'specialist_evidence_note': 'Query 31, business 204 and AI 360 unique path/hash references matched, using the preserved mutable-source mappings where provided. Independent initial backend Go/module inputs match 412 files in the final AI backend snapshot. AI coordinator count 361 additionally includes its supplemental runtime script.',
    'summary': groups,
    'supplemental_results': {
        'commands_built': len([run for run in runs if run['label'].startswith('command-build-')]),
        'commands_failed': [run['label'] for run in runs if run['label'].startswith('command-build-') and run['exit_code']],
        'contract_regeneration': {'compared_files': 160, 'mismatches': 0, 'comparison': ref(OUT / 'contract-comparison.json')},
        'contract_validation': {'schemas': 157, 'example_definitions': 24, 'api_operations': 96, 'run': 'contract-validation'},
        'actual_hr_plugin': 'actual-hr-plugin',
        'api_original_binary_repeat': 'api-budget-reproduction',
        'api_diagnostic_repeats': ['api-budget-diagnostic-' + str(i) for i in range(1, 6)],
        'api_full_diagnostic': {'run': 'api-full-diagnostic', 'top_level': by_label['api-full-diagnostic']['counts']['top_level']},
        'cloudsync_full_with_updated_assertions': {'run': 'cloudsync-full-idle-fixed', 'top_level': by_label['cloudsync-full-idle-fixed']['counts']['top_level']},
        'release_deployment': {'initial': {'run': 'release-deployment-tests', 'pass': 10, 'errors': 7}, 'runtime_inputs_supplemented': {'run': 'release-deployment-tests-supplemented', 'pass': 16, 'fail': 1}, 'remaining_failure': 'Snapshot centralized toolchain record still describes the prior frontend dependency versions. Frontend has since reported synchronized records; independently rerun against its final frozen inputs.'},
    },
    'first_failures_and_resolutions': [
        {'run': 'ordinary-Platform-internal-api', 'test': 'TestHTTPAndBackgroundControlShareBudgetDuringTaskBacklog', 'failure': 'Five-second wait did not observe the second backlog worker.', 'status': 'isolated_original_repeat_and_diagnostic_full_package_passed_cause_unproven', 'notes': 'Original same binary passed once; five diagnostic repeats and the full API package passed with unchanged deadlines. River scheduler uses a five-second interval and startup staggering, but the first-run timing cause was not established.'},
        {'run': 'ordinary-Platform-internal-cloudsync', 'test': 'TestIdleExchangesDoNotWriteUntilHeartbeat', 'failure': 'The prior test expected one total changed row; query projection and commit updates now produce six.', 'status': 'test_assertions_updated_and_full_package_passed', 'notes': 'Idle zero-write behavior remains asserted; source and query changes are checked separately with other domain rows unchanged.'},
        {'run': 'idle-regression-fixed', 'failure': 'First revised assertion incorrectly expected an unchanged source version.', 'status': 'test_corrected_to_one_version_increment_and_verified', 'retained_source': supplements[2]},
        {'run': 'independent-ai-build', 'failure': 'Overlay test package name ai conflicted with the existing external ai_test fixture.', 'status': 'compile_setup_corrected_in_v2'},
        {'run': 'independent-ai-original-asset-deletion', 'failure': 'Original resource deletion keeps evidence and investigation detail accessible.', 'status': 'P4-A02_awaiting_original_specialist'},
        {'run': 'release-deployment-tests', 'failure': 'Frontend package declarations and CI workflow were not included in the first backend snapshot.', 'status': 'three_runtime_files_added_and_preserved'},
        {'run': 'release-deployment-tests-supplemented', 'failure': 'Frontend dependency declarations differ from the central lock record in the tested snapshot.', 'status': 'frontend_reports_fixed_pending_final_freeze_retest'},
        {'evidence': ref(OUT / 'first-driver-failure.json'), 'failure': 'The run statistics parser attempted dictionary access on non-dictionary JSON from go list.', 'status': 'driver_parser_corrected_to_ignore_non_event_json'},
        {'evidence': ref(OUT / 'resource-inspection-first-environment-failure.json'), 'failure': 'Sandbox process inspection returned Operation not permitted.', 'status': 'approved_read_only_host_inspection_completed'},
    ],
    'criteria': criterion_rows,
    'coordinator_findings': findings,
    'skipped_test_audit': ref(OUT / 'skipped-test-audit.json'),
    'resource_cleanup': ref(OUT / 'resource-cleanup.json'),
    'resource_window': {'PG_and_race': 'released', 'own_persistent_services': [], 'preserved_prior_stage_schema': 'sf_pilot_20261004', 'work_volume_minimum_free_bytes': 1073741824},
    'next_actions': [
        '主协调恢复原 AI 专业修正 P4-A02，并由前端完成冻结；本角色结束本轮运行以释放并发名额',
        '恢复独立审查后读取两专业最终报告，保存新组合输入并核对与本检查点的源码关系',
        '使用保留的 v2/v3 复现测试核对依据、调查详情及其失效内容；运行相关后端与 PG 回归',
        '在自己的隔离端口完成前端生产构建与实际页面全部业务流程，独立复验 P4-F01/F02、三套页面和精确导出',
        '核对最终前端依赖、中央工具链记录和发布部署回归，处理仍适用的条件验证，再生成 phase4-independent-review.json 供主协调验收',
    ],
}
write(ROOT / '.local/evolution/phase4-independent-review-checkpoint.json', report)
print(json.dumps({'checkpoint': str(ROOT / '.local/evolution/phase4-independent-review-checkpoint.json'), 'run_count': len(runs), 'reference_checks': len(reference_checks), 'mismatches': len([row for row in reference_checks if not row['matched']]), 'groups': groups, 'findings_without_test_matches': [row['id'] for row in findings[:10] if not row['tests']]}, ensure_ascii=False))
