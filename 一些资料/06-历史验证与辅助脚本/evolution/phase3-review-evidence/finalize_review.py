import copy
import datetime
import json
import os
import re
import shutil
from pathlib import Path
from urllib.parse import urlparse

import run_review as R

root, out = R.ROOT, R.OUT
now = datetime.datetime.now(datetime.timezone.utc).isoformat()


def ref(value):
    p = Path(value)
    absolute = p if p.is_absolute() else root / p
    return {'path': str(p), 'sha256': R.sha(absolute), 'bytes': absolute.stat().st_size}


def own(name):
    return ref((out / name).relative_to(root))


def read(name):
    return json.loads((out / name).read_text())


base = json.loads((root/'.local/evolution/phase3-independent-review-checkpoint.json').read_text())
root_review = root/'.local/evolution/tasks-retry-t02-root-review.json'
frozen_root = out/'initial-specialist-reports/tasks-retry-t02-root-review.json'
if not frozen_root.exists():
    shutil.copyfile(root_review, frozen_root)

commands = []
for p in sorted(out.glob('*.metadata.json')):
    data = json.loads(p.read_text())
    if not all(k in data for k in ['label', 'argv', 'source_before', 'source_after']):
        continue
    historical_failure = data['label'] == 't01-retry-independent-reproduction'
    assert data['exit_code'] == (1 if historical_failure else 0), p
    assert data['changed_sources'] == [], p
    if data.get('counts'):
        assert R.counts((root/data['stdout']['path']).read_text()) == data['counts'], p
        assert data['counts']['data_race_occurrences'] == 0, p
        if not historical_failure:
            assert data['counts']['top_level']['fail'] == 0, p
    generation = 'T02' if data['label'].startswith('t02-') else 'T01' if data['label'].startswith('t01-') else 'C05' if data['label'].startswith('c05-') else 'initial phase3'
    commands.append({'label': data['label'], 'source_generation': generation,
                     'metadata': own(p.name), 'argv': data['argv'],
                     'started_at': data['started_at'], 'completed_at': data['completed_at'],
                     'elapsed_seconds': data['elapsed_seconds'], 'exit_code': data['exit_code'],
                     'interpretation': 'Original independently reproduced product failure; corrected same assertion passes under T02' if historical_failure else 'Passed on its recorded source and environment',
                     'source_changed_during_command': data['changed_sources'],
                     'counts': data['counts'], 'stdout': data['stdout'], 'stderr': data['stderr']})

findings = copy.deepcopy(base['findings'][:13])
for finding in findings:
    finding['status'] = 'independently_verified'
    finding['final_combination_evidence'] = [own('t02-platform-full.metadata.json'), own('final-source-reuse-audit.json')]
findings.extend([
    {'id': 'P3-T01', 'independent_id': 'IR-P3-02', 'status': 'independently_verified',
     'conclusion': '任务操作版本保留业务身份、快照、阶段、终态与人工操作代次；后台领取、Snooze及计算进度更新期间允许用户完成确认。当前页面手动刷新后只读取一次详情，17.260秒后正常确认取消200；实际业务或权限变化、旧代次及重复取消继续返回409或403',
     'evidence': [own(n) for n in ['t02-single-read-timing.json', 't02-single-read-confirmation.json',
         't02-single-read-cancelled.json', 't02-browser-independent-audit.json',
         't02-cancel-sqlite-race.metadata.json', 't02-cancel-postgres-race.metadata.json',
         't02-postgres-specialized.metadata.json', 't02-duplicate-stale-http.json']],
     'original_failure': ref('.local/evolution/phase3-independent-review-checkpoint.json')},
    {'id': 'P3-T02', 'independent_id': 'IR-P3-03', 'status': 'independently_verified',
     'conclusion': '正式sf-cloud打开T01失败数据库的副本，原Task及River24完成正常页面重试，旧cancel_attempted_at被删除；实际Snooze计数494、496、498时任务持续可运行，随后同一次详情版本成功取消。代次3到4到5、原始快照、追踪数据和审计保持一致；SQLite与PG实际测试覆盖运行中取消、旧worker更新、并发重试、精确metadata和审计失败回滚',
     'evidence': [own(n) for n in ['t01-retry-independent-reproduction.metadata.json',
         't02-previous-independent-assertion.metadata.json', 't02-binary-independent-audit.json',
         't02-browser-independent-audit.json', 't02-specialist-independent-audit.json',
         't02-postgres-specialized.metadata.json', 't02-cancel-sqlite-race.metadata.json',
         't02-cancel-postgres-race.metadata.json', 'final-cleanup.json']],
     'original_failure': ref('.local/evolution/phase3-independent-review-t01-checkpoint.json')},
])
assert len(findings) == 15 and len({v['id'] for v in findings}) == 15

requirement_evidence = {
    'E3-01': ['t02-platform-full.metadata.json', 't02-postgres-specialized.metadata.json', 'control-race.metadata.json', 'c05-independent-dual-app-audit.json'],
    'E3-02': ['datatransfer-full.metadata.json', 'datatransfer-command-race.metadata.json', 'c05-independent-dual-app-audit.json', 'c05-specialist-audit.json'],
    'E3-03': ['v2-control-offline-pending.json', 'v2-control-completed.json', 'v2-control-resource-revoked.json', 'v2-control-session-invalid.json', 'c05-independent-dual-app-audit.json'],
    'E3-04': ['t02-platform-full.metadata.json', 't02-postgres-specialized.metadata.json', 'v2-history-replay-result.json', 't02-browser-independent-audit.json'],
    'E3-05': ['v2-history-created-compare.json', 'v2-history-compare-unchanged.json', 'v2-history-compare-node-explanation.json', 'independent-trace-and-shadow-scope.metadata.json'],
    'E3-06': ['t02-platform-full.metadata.json', 't02-postgres-specialized.metadata.json', 'v2-shadow-formal-revision-comparison.json', 'v2-shadow-formal-recompute-node.json', 'v2-shadow-reenabled.json', 'v2-shadow-stale-version.json', 't02-browser-independent-audit.json'],
    'E3-07': ['independent-trace-and-shadow-scope.metadata.json', 'v2-history-compare-node-explanation.json', 'v2-control-trusted-evidence-visible.json', 'v2-control-feedback-visible.json', 't02-single-read-cancelled.json'],
}
requirements = copy.deepcopy(base['requirements'])
for item in requirements:
    item['status'] = 'independently_verified'
    item['scope'] = item['scope'].replace('均已核对；等待任务取消修复', '、同任务重试与正常页面取消均已核对').replace('均已核对；等待父代任务取消修复', '、等待父代任务的重试与正常页面取消均已核对')
    item['evidence'] = [own(n) for n in requirement_evidence[item['id']]]

source = read('final-reviewed-source.json')
compiled = read('final-compiled-inputs.json')
cleanup = read('final-cleanup.json')
assert not source['source_changes_after_checks'] and not compiled['changed_during_manifest']
assert all(p['closed'] for p in cleanup['ports']) and cleanup['browser_closed']
front_log = (out/'c05-frontend-unit.stdout.log').read_text()
front_counts = {k: int(re.search(r'^.* ' + k + r' (\d+)\s*$', front_log, re.M).group(1)) for k in ['tests', 'pass', 'fail', 'skipped']}
assert front_counts == {'tests': 20, 'pass': 20, 'fail': 0, 'skipped': 0}

conditional = copy.deepcopy(base['conditional_checks'])
conditional['ordinary_skips'] = '当前Platform普通回归34项顶层、51项叶子条件跳过，名称与原因逐项保存在t02-platform-full.metadata.json；实际PG七包完成25项顶层、40项叶子且零跳过。两个PG包装测试在无DSN的普通命令中显示顶层通过，其17项子场景已经在专用PG与race命令实际执行'
conditional['resolved_evidence'] = {
    'postgres': own('t02-postgres-specialized.metadata.json'),
    'actual_dual_application': own('c05-actual-dual-application.metadata.json'),
    'browser_fixture': own('browser-process-v2.json'),
    'actual_plugin_process': own('plugin-actual-process.metadata.json'),
    'target_three_replica_control': own('initial-specialist-audit.json'),
    'previous_stage_infrastructure': ref('.local/evolution/phase2-independent-review.json'),
}

report = {
    'schema_version': 1, 'phase': 3, 'recorded_at': now, 'status': 'verified',
    'reviewer': 'phase2_independent_review', 'coordinator': 'root',
    'independent_review_complete': True, 'global_acceptance_modified': False,
    'base_commit': base['base_commit'],
    'summary': '第三阶段七项目标与十五项发现均完成独立审查；当前组合普通回归、实际PostgreSQL、有限race和正常页面取消重试均通过，原始失败与各轮源码及产物对应关系保留',
    'requirements': requirements, 'findings': findings, 'unresolved_findings': [],
    'verification_commands': commands,
    'counts_scope': base['counts_scope'],
    'current_combination_summary': {
        'platform': read('t02-platform-full.metadata.json')['counts'],
        'postgres': read('t02-postgres-specialized.metadata.json')['counts'],
        'sqlite_task_race': read('t02-cancel-sqlite-race.metadata.json')['counts'],
        'postgres_task_race': read('t02-cancel-postgres-race.metadata.json')['counts'],
        'datatransfer_unchanged': read('datatransfer-full.metadata.json')['counts'],
        'frontend_unchanged': front_counts,
    },
    'reviewed_source': {
        'source_snapshot': own('final-reviewed-source.json'), 'files': len(source['files']),
        'compiled_input_provenance': own('final-compiled-inputs.json'), 'compiled_inputs': len(compiled['inputs']),
        'compiled_input_method': compiled['method'], 'source_and_reuse_audit': own('final-source-reuse-audit.json'),
        'backend_full_regression': own('t02-platform-full.metadata.json'),
        'datatransfer_regression': own('datatransfer-full.metadata.json'),
        'frontend_production_build': own('c05-frontend-production-build.metadata.json'),
        'frontend_typescript': own('c05-frontend-typescript6.metadata.json'),
        'frontend_unit_tests': own('c05-frontend-unit.metadata.json'),
        'frontend_static_file': ref('Frontends/dist/assets/main-CiVE3bj4.js'),
        'contract_regeneration': own('t02-contract-byte-comparison.json'),
        'contract_validation': own('t02-contract-schema-validation.metadata.json'),
        'frontend_specialist_audit': own('final-frontend-specialist-audit.json'),
        'original_specialist_audit': own('initial-specialist-audit.json'),
        'control_c05_specialist_audit': own('c05-specialist-audit.json'),
        'tasks_t01_specialist_audit': own('t01-specialist-independent-audit.json'),
        'tasks_t02_specialist_audit': own('t02-specialist-independent-audit.json'),
        'tasks_t02_coordinator_review': ref(frozen_root.relative_to(root)),
        'source_changes_after_current_checks': [],
    },
    'actual_services_and_binary_binding': {
        'c05_control_browser': own('browser-process-v2.json'),
        'initial_browser': own('browser-process.json'),
        'c05_actual_dual_application': own('c05-independent-dual-app-audit.json'),
        'history_before_task_fixes': base['actual_services_and_binary_binding']['history_binary'],
        't01_current_at_its_review': own('t01-binary-independent-audit.json'),
        't02_current': own('t02-binary-independent-audit.json'),
        't02_running_production_entrypoint': own('browser-process-t02.json'),
        'source_difference_and_evidence_reuse': own('final-source-reuse-audit.json'),
        'nats_target': '2.14.7', 'nats_actual_nodes': 3,
        'nats_evidence': base['actual_services_and_binary_binding']['nats_evidence'],
        'postgres': '18.6；当前七包实际25项顶层、40项叶子全部通过；每个用例独立schema，3open/1idle加1管理连接、包并行1；PG及race串行',
    },
    'browser_scope': {
        **base['browser_scope'],
        'history': base['browser_scope']['history'].split('；River26等待父代取消四次409')[0],
        'original_task_failures': '初始River26四次取消409形成T01；T01夹具River24取消成功后两次正常重试重新进入cancelled/waiting_parent形成T02，原始文件均保留',
        'task_final_verification': own('t02-browser-independent-audit.json'),
        'single_read_timing': own('t02-single-read-timing.json'),
        'rejected_duplicate_and_old_operations': own('t02-duplicate-stale-http.json'),
        'current_page_errors': [],
    },
    'first_failures_and_corrections': base['first_failures_and_corrections'][:-1] + [
        'P3-T01原四次正常取消409保留；T02同组合页面仅读取一次详情，17.260秒后确认200，保留原expected_version且没有自动重试',
        'P3-T02原两次正常页面重试200后仍cancelled/waiting_parent及独立增强断言exit1保留；原失败数据副本在当前正式sf-cloud恢复，同增强断言当前exit0',
        'T02专业首轮权限测试夹具的嵌入User.Version过时，原GET409保留；修正夹具并重新认证后，实际角色、资源和并发授权检查通过',
        '最终编译输入清单初稿额外枚举了依赖包自身未编译的TestGoFiles；最终清单按go list生成的实际测试包GoFiles枚举，并加入cgo、嵌入文件及模块元数据',
    ],
    'conditional_checks': conditional,
    'preserved_checkpoints': [ref('.local/evolution/phase3-independent-review-checkpoint.json'),
                              ref('.local/evolution/phase3-independent-review-t01-checkpoint.json')],
    'previous_evidence_integrity': own('final-previous-evidence-audit.json'),
    'cleanup': own('final-cleanup.json'),
    'work_volume_free_bytes': os.statvfs(root).f_bavail * os.statvfs(root).f_frsize,
    'retained_data': base['retained_data'] + [str(R.TMP/name) for name in ['history-browser-t01', 'history-browser-t02-from-t01', 'browser-static-t01', 'browser-static-t02']],
    'handoff': '主协调据此核对七项目标与十五项发现并更新全局验收；本角色的服务和浏览器均已关闭，专业构建、原失败数据、验证副本和原始日志保留',
}
target = root/'.local/evolution/phase3-independent-review.json'
R.json_write(target, report)

private_sources = list(R.TMP.rglob('private-access.json')) + list(R.TMP.rglob('private-credentials.json')) + [root/'.local/evolution/pg18-test.json']
secrets = set()


def secret_values(value):
    if isinstance(value, dict):
        for k, v in value.items():
            if isinstance(v, str) and (k in ['password', 'token', 'dsn'] or k.endswith('_token')):
                if len(v) >= 8:
                    secrets.add(v.encode())
                if k == 'dsn' and urlparse(v).password:
                    secrets.add(urlparse(v).password.encode())
            else:
                secret_values(v)
    elif isinstance(value, list):
        for v in value:
            secret_values(v)


for p in private_sources:
    secret_values(json.loads(p.read_text()))
scan_files = [p for p in out.rglob('*') if p.is_file() and '__pycache__' not in p.parts]
scan_files += [p for p in (root/'output/playwright/phase3-independent').rglob('*') if p.is_file()]
scan_files += list((root/'.playwright-cli').glob('page-*.yml')) + [target]
leaks = []
for p in scan_files:
    raw = p.read_bytes()
    if any(v in raw for v in secrets):
        leaks.append(str(p.relative_to(root)))
assert not leaks, leaks
scan = {'recorded_at': now, 'status': 'passed', 'files_checked': len(set(scan_files)),
        'known_private_values': len(secrets), 'matching_paths': leaks,
        'private_input_files': [str(p) for p in private_sources],
        'scope': 'Literal known credentials/tokens/PG DSN and password scan of reviewer evidence, screenshots, generated page snapshots and final report. No login-page screenshot was captured; private files remain outside report/index.'}
R.json_write(out/'final-credential-scan.json', scan)
report['credential_scan'] = own('final-credential-scan.json')

index_files = [p for p in out.rglob('*') if p.is_file() and '__pycache__' not in p.parts and p.name not in ['evidence-index-final.json', 'final-delivery-validation.json']]
index_files += [p for p in (root/'output/playwright/phase3-independent').rglob('*') if p.is_file()]
index = {'recorded_at': now, 'files': [ref(p.relative_to(root)) for p in sorted(set(index_files))]}
R.json_write(out/'evidence-index-final.json', index)
report['evidence_index'] = own('evidence-index-final.json')
R.json_write(target, report)

typed_refs = []


def walk(value, at='$'):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            p = Path(value['path'])
            p = p if p.is_absolute() else root/p
            typed_refs.append({'at': at, 'path': value['path'], 'matches': p.is_file() and R.sha(p) == value['sha256']})
        for k, v in value.items():
            walk(v, at+'.'+k)
    elif isinstance(value, list):
        for i, v in enumerate(value):
            walk(v, at+'['+str(i)+']')


walk(report)
index_mismatches = [v['path'] for v in index['files'] if R.sha(root/v['path']) != v['sha256']]
assert all(r['matches'] for r in typed_refs), [r for r in typed_refs if not r['matches']]
assert not index_mismatches
assert not any(v in target.read_bytes() for v in secrets)
validation = {'recorded_at': now, 'report': ref(target.relative_to(root)),
              'typed_reference_count': len(typed_refs), 'typed_reference_mismatches': [],
              'indexed_files': len(index['files']), 'indexed_file_mismatches': [],
              'requirements_verified': len(requirements), 'findings_verified': len(findings),
              'command_records': len(commands), 'passed_command_records': sum(c['exit_code'] == 0 for c in commands),
              'preserved_expected_failure_records': sum(c['exit_code'] == 1 for c in commands),
              'credentials_match_count': 0, 'owned_ports_closed': len(cleanup['ports']),
              'work_volume_above_one_gib': report['work_volume_free_bytes'] >= 1024**3}
R.json_write(out/'final-delivery-validation.json', validation)
print(json.dumps(validation, ensure_ascii=False))
