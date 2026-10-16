#!/usr/bin/env python3
"""Assemble the second-stage review only after required checks have passed."""
import datetime
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess

OUT = Path(__file__).resolve().parent
ROOT = OUT.parents[2]
spec = importlib.util.spec_from_file_location('review_runner', OUT / 'run_review.py')
R = importlib.util.module_from_spec(spec)
spec.loader.exec_module(R)


def read(path):
    return json.loads((ROOT / path).read_text())


def ref(path):
    target = Path(path)
    target = target if target.is_absolute() else ROOT / target
    return {'path': str(target.relative_to(ROOT)) if target.is_relative_to(ROOT) else str(target),
            'sha256': R.sha(target), 'bytes': target.stat().st_size}


def ev(name):
    return '.local/evolution/phase2-review-evidence/' + name


def check(label):
    path = ev(label + '.metadata.json')
    data = read(path)
    return {**ref(path), 'command': data['argv'], 'cwd': data['cwd'],
            'exit_code': data['exit_code'], 'elapsed_seconds': data['elapsed_seconds'],
            'started_at': data['started_at'], 'completed_at': data['completed_at'],
            'source_changes_during_command': data['changed_sources'],
            'counts': data['counts'], 'stdout': data['stdout'], 'stderr': data['stderr']}


def case_list(label):
    results = []
    for line in (OUT / (label + '.stdout.log')).read_text().splitlines():
        try:
            value = json.loads(line)
        except ValueError:
            continue
        if value.get('Action') == 'pass' and value.get('Test') and '/' not in value['Test']:
            results.append({'package': value['Package'], 'test': value['Test']})
    return results


def main():
    specialist_snapshots = read(ev('specialist-snapshot-index.json'))
    for entry in specialist_snapshots['files'].values():
        assert R.sha(ROOT / entry['path']) == entry['sha256'], entry['path']
    frontend_report_path = specialist_snapshots['files']['.local/evolution/frontend-phase2-verified.json']['path']
    required = ['plugin-build', 'plugin-actual-process', 'datatransfer-full', 'platform-full',
                'postgres-specialized', 'bounded-race', 'cross-mode-arithmetic',
                'go-contract-generation', 'contract-schema-validation', 'browser-backend-build',
                'toolchain-installed', 'deployment-unit-repaired-default', 'frontend-unit',
                'frontend-typescript6', 'frontend-build', 'final-diff-check']
    commands = {name: check(name) for name in required}
    assert all(value['exit_code'] == 0 for value in commands.values())
    assert all(not value['source_changes_during_command'] for value in commands.values())
    assert not read(ev('contract-byte-comparison.json'))['mismatches']
    browser = read(ev('browser-current-simulation-validation.json'))
    assert browser['export_reproduced'] and browser['formal_database_unchanged']
    cleanup = read(ev('browser-cleanup.json'))
    assert cleanup['browser_closed'] and not cleanup['port_accepting']
    current_backend = R.snapshot(['Platform', 'DataTransfer'])
    tested_backend = read(ev('platform-full.metadata.json'))['source_after']
    ignored = ['.local', '.cache', '.tools', '.pytest_cache', 'node_modules', '.git', 'dist', '__pycache__']
    tested_backend = {path: digest for path, digest in tested_backend.items()
                      if not any(part in Path(path).parts for part in ignored) and Path(path).name != '.DS_Store'}
    backend = {'captured_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
               'files': len(current_backend),
               'matched': sum(tested_backend.get(path) == digest for path, digest in current_backend.items()),
               'changes': [path for path, digest in current_backend.items() if tested_backend.get(path) != digest],
               'removed': sorted(set(tested_backend) - set(current_backend)),
               'normalization': 'Exclude dependency caches and temporary build wrappers; original command metadata retains the larger captured scope.'}
    R.json_write(OUT / 'backend-current-scope-comparison.json', backend)
    assert not backend['changes'] and not backend['removed']
    current_frontend = R.snapshot(['Frontends', 'contracts', 'deploy/toolchains.json', 'deploy/node-version'])
    assert current_frontend == read(ev('frontend-build.metadata.json'))['source_after']
    audit = read(ev('final-evidence-audit.json'))
    for field in ['frontend_preserved_evidence', 'frontend_current_binary', 'ai_owned_source']:
        assert not audit[field]['different'], field
    assert all(not item['missing_references'] for item in audit['reports'])
    frontend = read(frontend_report_path)
    assert frontend['backend']['initial_binary_sha256'] is None
    assert 'taskNoticeDescription(notice, task)' in (ROOT / 'Frontends/src/tasks.tsx').read_text()
    frontend_fix = read(ev('frontend-final-notice-review.json'))
    assert frontend_fix['passed']
    for path, expected in frontend_fix['evidence_sha256'].items():
        assert specialist_snapshots['files'][path]['sha256'] == expected, path
    assert {item['path'] for item in audit['frontend_source']['different']} == {
        'Frontends/src/task-status.ts', 'Frontends/src/tasks.tsx',
        'Frontends/tests/generated-client.test.mjs', 'docs/evolution/frontend-phase2.md'}
    assert read(ev('frontend-original-behavior-audit.json'))['passed']
    source_scope = ['Platform', 'DataTransfer', 'Frontends', 'contracts', 'deploy', 'scripts',
                    'tests', '.github/workflows/build-release.yml', 'examples/factory/definitions.json']
    final_sources = R.snapshot(source_scope)
    R.json_write(OUT / 'final-source-manifest.json', {
        'captured_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
        'scope': source_scope, 'excluded': ['node_modules', '.git', 'dist', '__pycache__', '.local', '.cache', '.tools', '.pytest_cache', '.DS_Store'],
        'files': final_sources})
    requirements = read('docs/evolution/acceptance.json')['requirements']
    old = {item['id']: item for item in requirements}
    items = []

    def requirement(identifier, conclusion, evidence):
        items.append({'id': identifier, 'title': old[identifier]['title'],
                      'verification_criteria': old[identifier]['verification_criteria'],
                      'independent_review': 'passed', 'conclusion': conclusion,
                      'evidence': evidence})

    requirement('E2-01', '复核 PostgreSQL 16.15 至 18.6 导出恢复的九类记录摘要、128 条完整字段观测、旧任务承接和目标读写归档；当前源码的 16 项 PostgreSQL 专项一次运行全部通过。专业报告保留 CE/Edge 与 PostgreSQL18 的实际组合验证。',
                ['.local/evolution/pg16-to18-dump-restore.json', '.local/evolution/postgres-migration-root-review.json',
                 '.local/evolution/toolchain-observability-verified.json', ev('postgres-specialized.metadata.json'), ev('deployment-unit-repaired-default.metadata.json')])
    requirement('E2-02', '当前应用与 worker 使用领域存储接口；PostgreSQL 采用对象身份锁及提交阶段日志锁，独立对象并行、相同对象竞争、迟提交同步游标、审计链、inbox/outbox/River 共同回滚均通过当前专项。事务回调遇到冲突直接返回可识别错误。',
                [ev('postgres-specialized.metadata.json'), ev('platform-full.metadata.json'), ev('bounded-race.metadata.json'), '.local/evolution/storage-tasks-verified.json'])
    requirement('E2-03', '归档编码和完整解码校验在写入事务之外执行，提交时核对原记录集合与内容。当前全量与 PostgreSQL 专项通过精确字段往返、晚到数据、分区竞争及失败回滚；专业受控计时保留单次样本和运行条件。',
                [ev('platform-full.metadata.json'), ev('postgres-specialized.metadata.json'), '.local/evolution/storage-tasks-verified.json'])
    requirement('E2-04', '当前源码通过 River 同事务入队、回滚、实际进程终止恢复、补算父子传播检查点与汇总阶段恢复。新旧 HTTP 重试入口在 PostgreSQL 并发请求中保持任务、River 与 replay 身份；旧入口保留 202 与 Job 响应。',
                [ev('platform-full.metadata.json'), ev('postgres-specialized.metadata.json'), ev('bounded-race.metadata.json'), frontend_report_path])
    requirement('E2-05', '当前全量通过采集/控制预算及后台队列并发、积压、取消和前台处理测试；七个状态共享包的有限竞态检查通过。两项大样本继续采用普通执行与 PostgreSQL 专项结果，正式运行预算保持现值。',
                [ev('platform-full.metadata.json'), ev('postgres-specialized.metadata.json'), ev('bounded-race.metadata.json'), '.local/evolution/storage-tasks-verified.json'])
    requirement('E2-06', '发布、启动、同步导入和运行入口均使用编译计划；计划身份、格式、摘要拒绝、旧消息准备及运行复用通过当前测试。PostgreSQL 专项重新验证并发发布与损坏计划回滚。',
                [ev('platform-full.metadata.json'), ev('postgres-specialized.metadata.json'), '.local/evolution/rules-verified.json'])
    requirement('E2-07', '独立测试对相同输入序列、事件时钟和资产版本1分别调用生产实时计算、连续模拟和历史补算；计数样本从10开始，1500毫秒窗口求和使用空初始状态。测试逐点核对预期数值、质量、单位、时间和最终状态，两种算子全部一致。当前全量与竞态检查同时覆盖取消、运行隔离和资源预算。',
                [ev('cross_mode_test.go.txt'), ev('cross-mode-arithmetic.metadata.json'), ev('platform-full.metadata.json'), ev('bounded-race.metadata.json')])
    requirement('E2-08', '统一12种节点元数据驱动后端参数校验、节点 API 和条件 Schema；当前 Go 生成产物74份逐字节一致，71份 Schema、56项 API 操作与24个定义样本校验通过。',
                [ev('contract-byte-comparison.json'), ev('contract-schema-validation.metadata.json'), ev('platform-full.metadata.json')])
    requirement('E2-09', '独立浏览器使用当前生产 sf-cloud 构建完成两种连续输入排序、精确整数、初始状态、历史省略/空数组、导出复现、过期修订409与恢复；正式数据摘要保持一致。任务专业故障与撤权证据已核对，完成后的操作提示由当前详情生成，修复后的实际页面和回归测试通过。',
                [ev('browser-current-simulation-validation.json'), ev('browser-current-final-capture.json'), ev('browser-export-reproduction.json'),
                 ev('browser-export-replayed.json'), ev('browser-before-simulation.json'), ev('browser-after-conflict-recovery.json'),
                 ev('frontend-final-notice-review.json'), ev('frontend-original-behavior-audit.json'), frontend_report_path, ev('frontend-unit.metadata.json')])
    requirement('E2-10', '当前全量与竞态检查再次执行官方 Go SDK 客户端、既有 JSON-RPC 客户端、五个受支持协议版本、草稿精度、传输与会话规则、动态撤权和错误处理。实现复用业务应用用例，实际 HTTP 验证由当前测试进程执行。',
                [ev('platform-full.metadata.json'), ev('bounded-race.metadata.json'), '.local/evolution/ai-protocols-verified.json'])
    requirement('E2-11', '当前全量与竞态检查通过 Chat Completions 和 Responses 的本地 HTTP 模型端点、流式/完整消息、完整工具循环、精确参数、原生 reasoning/call_id 续传及异常结束。旧 SQLite 模型配置 Schema 升级与原回执保留也通过当前回归。',
                [ev('platform-full.metadata.json'), ev('bounded-race.metadata.json'), '.local/evolution/ai-protocols-verified.json'])
    requirement('E2-12', '独立检查实际 Node24.21.0、npm11.19.0 及 Go1.27.1，执行正式 openapi-typescript 生成一致性检查、TypeScript7 原生检查、TypeScript6 编译器 API 兼容检查、前端单元测试和 Vite8.1.5 生产构建；专业的独立 npm ci 产物比较与三个页面入口证据均已核对。',
                [ev('toolchain-current.json'), ev('frontend-unit.metadata.json'), ev('frontend-typescript6.metadata.json'), ev('frontend-build.metadata.json'),
                 ev('deployment-unit-repaired-default.metadata.json'), frontend_report_path, ev('final-evidence-audit.json')])

    pg_tests = {value['test'] for value in case_list('postgres-specialized')}
    skipped = []
    for label in ['datatransfer-full', 'platform-full', 'bounded-race']:
        for item in commands[label]['counts']['conditional_skips']:
            value = dict(item, originating_command=label)
            name = item['test']
            if name in pg_tests:
                value.update(disposition='passed_in_current_postgres_specialized_run', evidence=ev('postgres-specialized.metadata.json'))
            elif name == 'TestActualOrganizationProcessPullAndCrashIsolation':
                value.update(disposition='passed_with_current_Darwin_arm64_plugin', evidence=ev('plugin-actual-process.metadata.json'))
            elif name in ['TestPersistentThreeReplicaUpgrade', 'TestRealThreeReplicaCoordination', 'TestNativeCalculatedFieldAlarmAndRetention']:
                value.update(disposition='passed_in_specialist_actual_external_fixture', evidence='.local/evolution/message-infrastructure-verified.json')
            elif name == 'TestControlledPostgresUpgradeFixture':
                value.update(disposition='controlled_source_and_target_fixture_compared_in_retained_migration_run', evidence='.local/evolution/pg16-to18-dump-restore.json')
            elif name == 'TestTaskCrashProcess':
                value.update(disposition='helper_entrypoint_invoked_by_passing_actual_process_crash_tests', evidence=ev('platform-full.metadata.json'))
            else:
                value.update(disposition='not_run_in_this_independent_stage2_environment',
                             explanation='DataTransfer external protocol/process or A06 100000-message fixture; retained as an explicit conditional skip for the final applicable legacy regression')
            skipped.append(value)

    report = {
        'schema_version': 1, 'role': 'phase2_independent_review',
        'completed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
        'status': 'independent_review_complete', 'scope': 'E2-01 through E2-12 and applicable stage1 regressions',
        'acceptance_recommendation': '第二阶段各项必要验证通过，建议由主协调者更新第二阶段验收状态；整个五阶段仍按总计划继续推进',
        'acceptance_owner': 'root coordinator', 'all_required_checks_passed': True,
        'repository': str(ROOT), 'git_head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
        'environment': read(ev('toolchain-current.json')),
        'frontend_observed_versions_and_counts': read(ev('frontend-independent-summary.json')),
        'requirements': items, 'commands': commands,
        'go_test_counting': 'Top-level tests exclude slash-named subtests. Leaf cases exclude parent cases. Package skip events include packages with no tests. Conditional named skips are retained separately.',
        'independent_cross_mode': {
            'fixture': ref(ev('cross_mode_test.go.txt')), 'overlay': '/private/tmp/smartfactory-phase2-independent/cross-mode-overlay.json',
            'production_paths': ['Service.ProcessCalculations', 'Service.SimulateSequence', 'Service.Recompute'],
            'input_values_decimal': ['9007199254740993', '2', '-1'], 'initial_counter': 10,
            'sample_interval_ms': 1000, 'window_ms': 1500, 'asset_version': 1,
            'counter_expected_decimal': ['9007199254741003', '9007199254741005', '9007199254741004'],
            'window_sum_expected_decimal': ['9007199254740993', '9007199254740995', '1'],
            'verified_fields': ['numeric values', 'quality GOOD', 'unit piece', 'logical event time', 'definition version', 'asset version', 'final node state', 'replay completion'],
            'production_files_modified_for_this_check': False},
        'browser': {'binary': read(ev('browser-process.json')), 'validation': browser,
                    'cleanup': cleanup, 'production_store': 'isolated SQLite',
                    'task_postgres_evidence': frontend_report_path,
                    'scope_note': 'Independent browser run uses the prior frontend production build whose simulation sources remain unchanged; the final build and task notice repair have their own later validation.'},
        'frontend_notice_repair': frontend_fix,
        'frontend_retained_behavior_audit': read(ev('frontend-original-behavior-audit.json')),
        'known_existing_business_tool_limit': read(ev('ai-identifier-scope-review.json')),
        'conditional_skips': skipped,
        'race_scope': {'packages': 7, 'top_level_passed': 115, 'top_level_conditional_skips': 17,
                       'data_race_occurrences': 0,
                       'explicitly_excluded': ['TestArchivePreservesFullObservationsRevisionsAndRollups', 'TestSQLiteBulkIngestTasksBudget'],
                       'first_instrumented_failures': '.local/evolution/storage-tasks-verified.json#/race_summary',
                       'ordinary_large_sample_tests_passed': True, 'postgres_specialized_passed': True,
                       'production_budgets_modified': False},
        'reviewer_fixture_repair': {
            'path': 'tests/test_message_deploy.py', 'change': "root = Path(temporary).resolve() / 'source'",
            'cause': 'macOS /var/folders temporary path resolves to /private/var/folders; the fixture patched an unresolved ROOT into a production source filter that compares resolved paths',
            'authorization': 'REDACTED',
            'initial_failed_combination': ref(ev('deployment-unit.metadata.json')),
            'named_reproduction': ref(ev('deployment-release-named.metadata.json')),
            'canonical_temp_diagnosis': ref(ev('deployment-unit-canonical-tmp.metadata.json')),
            'default_environment_revalidation': ref(ev('deployment-unit-repaired-default.metadata.json')),
            'default_environment_tests_passed': 13, 'production_code_changed': False},
        'source_review': {
            'current_backend_comparison': ref(ev('backend-current-scope-comparison.json')),
            'final_source_manifest': ref(ev('final-source-manifest.json')),
            'evidence_audit': ref(ev('final-evidence-audit.json')),
            'historical_specialist_snapshots': 'Older specialist source digests are retained at their execution dates. Later integration edits are named in the audit; current Go, PostgreSQL, contracts, frontend and bounded-race checks cover the final applicable production source.',
            'initial_frontend_binary': {'sha256': None, 'interpretation': 'The first frontend specialist server binary was not hashed. Its early browser evidence remains historical; the independent current production build supplements continuous simulation evidence.'},
            'current_frontend_backend': frontend['backend'],
            'specialist_report_snapshot_index': ref(ev('specialist-snapshot-index.json')),
            'frontend_historical_manifest_differences': {
                'source_paths': [item['path'] for item in audit['frontend_source']['different']],
                'explanation': 'The immutable 18:01 UTC report precedes the task notice repair and followup documentation. Current source, 14 tests, TS6 compatibility, production build and three actual task notice states were separately verified by this reviewer.',
                'old_dist_paths': [item['path'] for item in audit['frontend_dist']['different']],
                'current_dist_verified_by': ev('frontend-final-notice-review.json')},
            'message_report_fixture_difference': {
                'path': 'tests/test_message_deploy.py',
                'explanation': 'The message specialist snapshot predates the coordinator-authorized macOS temporary-root fixture correction. All 13 relevant deployment tests then passed in the default environment.',
                'evidence': ev('deployment-unit-repaired-default.metadata.json')},
            'preexisting_work': '.local/evolution/preexisting-work-current-review.json',
            'reviewer_authored_scope': ['.local/evolution/phase2-review-evidence', '.local/evolution/phase2-independent-review.json', 'output/playwright/phase2-independent', 'tests/test_message_deploy.py temporary fixture root normalization']},
        'professional_reports': [{**ref(specialist_snapshots['files']['.local/evolution/' + name]['path']),
                                  'original_path': '.local/evolution/' + name}
                                 for name in ['rules-verified.json', 'storage-tasks-verified.json', 'ai-protocols-verified.json', 'frontend-phase2-verified.json', 'message-infrastructure-verified.json']],
        'coordinator_handoffs': [{
            'owner': 'root coordinator', 'status': 'documentary_followup',
            'item': 'Verify the frontend specialist final summary update and final private fixture cleanup after the independent report is delivered.',
            'technical_review_complete': True,
            'basis': 'The coordinator explicitly requested final delivery using the immutable 18:01 UTC professional report and the independent verified source, browser and build evidence.',
            'observed_service_state': ref(ev('handoff-state.json'))}],
        'followups_in_later_phases': [
            {'owner_requirement': 'E4-07', 'item': 'Existing AI business tools restrict identifiers to A-Za-z0-9_.:- while normal application drafts accept escaped slash-containing IDs. tools.go is byte-identical to the base commit and both old/new MCP transports call this layer. Unify validation, escaping and save/read/reference round trips before final overall delivery.'},
            {'owner_requirement': 'E4-10', 'item': 'ECharts5.6.0 moderate GHSA-fgmj-fm8m-jvvx; professional audit contains one advisory, upgrade to the official fixed ECharts version and audit again'},
            {'owner_requirement': 'E4-10', 'item': 'The production main chunk exceeds the existing 500kB build warning threshold; final frontend changes include the planned page and chart migration'},
            {'owner_requirement': 'E5-06', 'item': 'Complete offline image archives, formal release/profile deployment and applicable legacy external fixtures'},
            {'owner_requirement': 'E5', 'item': 'NATS full source cold-copy restore after the target upgrade; this stage verified old cold backup and persistent source restart'},
            {'owner_requirement': 'E5', 'item': 'Formal Kafka 512MiB heap and 1GiB container measurement under final combined workload'},
            {'owner_requirement': 'E5-04', 'item': 'ThingsBoard Edge PID1 stop signal forwarding handoff from stage1'},
            {'owner_requirement': 'E5', 'item': 'Kafka restore cutoff is 42 retained records versus 45 at finalization; three records after the copy require replay'}],
        'explicit_exclusions': read('docs/evolution/acceptance.json')['excluded_from_this_goal'],
        'resource_cleanup': {'browser': cleanup, 'reviewer_containers_created': 0,
                              'retained_validation_container': 'smartfactory-evolution-pg18-20261004',
                              'work_volume_free_bytes': shutil.disk_usage(ROOT).free,
                              'required_work_volume_free_bytes': 1073741824},
    }
    assert report['resource_cleanup']['work_volume_free_bytes'] >= 1073741824
    index = {}
    for directory in [OUT, ROOT / 'output/playwright/phase2-independent']:
        for path in sorted(directory.rglob('*')):
            if path.is_file() and '__pycache__' not in path.parts:
                index[str(path.relative_to(ROOT))] = {'sha256': R.sha(path), 'bytes': path.stat().st_size}
    report['evidence_index'] = index
    R.json_write(ROOT / '.local/evolution/phase2-independent-review.json', report)
    print(json.dumps({'status': report['status'], 'requirements': len(items), 'commands': len(commands),
                      'evidence_files': len(index), 'report_sha256': R.sha(ROOT / '.local/evolution/phase2-independent-review.json')}, ensure_ascii=False))


if __name__ == '__main__':
    main()
