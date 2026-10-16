#!/usr/bin/env python3
"""Preserve completed independent review and the outstanding task cancellation defect."""
import datetime
import json
import os
from pathlib import Path
import run_review as R

ROOT, OUT = R.ROOT, R.OUT


def ref(path):
    value = Path(path)
    absolute = value if value.is_absolute() else ROOT / value
    return {'path': str(value), 'sha256': R.sha(absolute), 'bytes': absolute.stat().st_size}


def own(name):
    return ref(str((OUT / name).relative_to(ROOT)))


def read(name):
    return json.loads((OUT / name).read_text())


commands = []
for path in sorted(OUT.glob('*.metadata.json')):
    data = json.loads(path.read_text())
    if not all(k in data for k in ['label', 'argv', 'source_before', 'source_after']):
        continue
    assert data['exit_code'] == 0, path
    assert data['changed_sources'] == [], path
    if data['counts']:
        assert data['counts']['data_race_occurrences'] == 0
        assert data['counts']['top_level']['fail'] == 0
    commands.append({
        'label': data['label'], 'metadata': own(path.name),
        'argv': data['argv'], 'started_at': data['started_at'],
        'completed_at': data['completed_at'], 'elapsed_seconds': data['elapsed_seconds'],
        'exit_code': data['exit_code'], 'source_changed_during_command': data['changed_sources'],
        'counts': data['counts'], 'stdout': data['stdout'], 'stderr': data['stderr'],
    })

finding_proofs = {
    'P3-C01': ['c05-platform-full.metadata.json', 'control-race.metadata.json'],
    'P3-C02': ['c05-platform-full.metadata.json', 'control-race.metadata.json', 'control-actual-dual-app.metadata.json'],
    'P3-C03': ['c05-platform-full.metadata.json', 'v2-control-resource-revoked.json', 'v2-control-session-invalid.json'],
    'P3-C04': ['c05-platform-full.metadata.json', 'initial-specialist-audit.json', 'c05-actual-dual-application.metadata.json'],
    'P3-C05': ['c05-specialist-audit.json', 'c05-independent-dual-app-audit.json', 'c05-postgres-evidence.metadata.json',
               'c05-control-race.metadata.json', 'v2-control-recovered-backend.json', 'v2-control-cancelled-backend.json',
               'v2-control-trusted-evidence-visible.json', 'v2-control-feedback-visible.json'],
    'P3-H01': ['independent-trace-and-shadow-scope.metadata.json', 'history_trace_overlay_test.go'],
    'P3-H02': ['c05-platform-full.metadata.json', 'postgres-specialized.metadata.json',
               'v2-shadow-formal-revision-comparison.json', 'v2-shadow-formal-recompute-node.json'],
    'P3-H03': ['c05-platform-full.metadata.json', 'postgres-specialized.metadata.json', 'initial-specialist-audit.json'],
    'P3-H04': ['independent-trace-and-shadow-scope.metadata.json', 'shadow_scope_overlay_test.go', 'history-api-race.metadata.json'],
    'P3-H05': ['independent-trace-and-shadow-scope.metadata.json', 'history_trace_overlay_test.go', 'v2-history-compare-node-explanation.json'],
    'P3-F01': ['c05-frontend-unit.metadata.json', 'v2-control-offline-pending.json', 'v2-control-completed.json'],
    'P3-F02': ['c05-frontend-unit.metadata.json', 'v2-history-replay-result.json',
               'v2-history-created-compare.json', 'v2-history-compare-unchanged.json', 'v2-shadow-formal-revision-comparison.json'],
    'P3-F03': ['v2-shadow-reenable-modal.json', 'v2-shadow-reenabled.json', 'v2-shadow-stale-version.json', 'final-frontend-specialist-audit.json'],
}
finding_conclusions = {
    'P3-C01': '中文及冒号执行身份按字符长度读取原动作，既有 command_id 原样核对，恢复只发送剩余步骤',
    'P3-C02': '开始时间在查询前持久保存，跨期限的重复领取、数据库重开和旧开始回执沿用首次开始语义，边缘与云端结果一致',
    'P3-C03': '历史定义、执行快照、反馈设备及当前资产归属共同决定详情与操作结果的读取权限；浏览器撤权后清除旧详情',
    'P3-C04': 'pending 操作与 edge_control_operation 在同一事务保存，同身份重试及后台领取可继续处理，执行权竞争保持可重试',
    'P3-C05': '可信原命令证据、反馈来源及追踪身份随执行回执共同保存，云边内容一致；旧来源版本补充证据且保持新状态，同证据身份改写被原子拒绝',
    'P3-H01': '独立 HTTP/OTLP 用例确认缺失定义、权限拒绝和输入预算三类创建失败均导出 ERROR，执行失败也导出 ERROR',
    'P3-H02': '生产迟到观测经补算与 revision2 outbox 后，影子步骤读取对应正式补算结果；页面呈现正式值 60、候选值 61 与来源',
    'P3-H03': '普通连续输入使用上一已创建代际，首次执行固定实际初始状态；20 个窗口0单设备单字段输入产生20个影子步骤，SQLite/PG保存40份快照，共128531个UTF-8 payload字节；SIGKILL后保持已解析快照',
    'P3-H04': '独立竞争用例分别缩小显式设备和字段范围，新 epoch 输入数量为0，旧 epoch 已完成运行、输入及快照仍保存',
    'P3-H05': '独立导出 OTLP 中两侧 plan_ids、definition_versions、analysis_sides 与持久步骤逐项一致，并使用同一实际执行快照',
    'P3-F01': '实际离线202显示信息状态，完成后依据执行结果显示；重复请求保留同一 operation_id 和请求内容',
    'P3-F02': '回放显示本次结果，比较显示差异或一致，影子显示对应正式结果比较；大整数在输入、节点输出和导出文件中保留',
    'P3-F03': '重新启用对话框位于详情抽屉上层，普通点击完成停止v2到active v3/epoch2；陈旧v3提交在并发停止v4后返回409，旧成功提示消失',
}
findings = [{'id': name, 'status': 'independently_verified_in_reviewed_combination',
             'conclusion': finding_conclusions[name], 'evidence': [own(path) for path in paths]}
            for name, paths in finding_proofs.items()]
findings.append({'id': 'P3-T01', 'independent_id': 'IR-P3-02', 'status': 'waiting_for_specialist_fix_and_independent_retest',
    'conclusion': '同一 waiting_parent 业务状态下，500ms River 调度持续改变操作版本，四次正常确认取消均409',
    'evidence': [own(name) for name in ['v2-history-waiting-parent-cancel-first.json',
        'v2-history-waiting-parent-cancel-reviewed.json', 'v2-history-waiting-parent-cancel-reviewed-second.json',
        'v2-history-waiting-parent-cancel-confirmed.json', 'v2-history-cancel-timing.json',
        'v2-history-cancel-keyboard-timing.json', 'v2-history-cancelled-identities.json']],
    'filename_note': 'cancel-confirmed 与 cancelled-identities 为操作意图名称；文件实际状态明确记录409和available/waiting_parent，未发生取消成功',
    'task_id': 'analysis:shadow:fixture-failure:1:2', 'river_id': 26,
    'business_run': 'shadow:fixture-failure:1:2',
    'snapshot_id': 'snapshot:561bd6aeefcd7f148577d44fb35ca0c93891940694fa0ba6cb4569feed522bbe',
    'mouse_last_elapsed_ms': 883, 'keyboard_detail_to_post_ms': 848.278,
    'automatic_retry': False, 'page_bypassed': False,
    'expected_fix_check': '后台真实持续调度，读取一次版本后等待2至3秒再正常确认取消；保留业务变化、终态、重复取消和撤权的拒绝'})

source = read('reviewed-source-through-c05.json')
compiled = read('reviewed-compiled-inputs-through-c05.json')
cleanup = read('cleanup-waiting-t01.json')
assert not source['changed_since_checks']
assert not compiled['current_mismatches']
assert all(item['closed'] for item in cleanup['ports'])
assert all(item['closed'] for item in cleanup['browser_sessions'])

checkpoint = {
    'schema_version': 1,
    'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'independent_review_checkpoint_waiting_for_P3-T01',
    'final_acceptance': False,
    'coordinator': 'root', 'reviewer': 'phase2_independent_review',
    'base_commit': 'dde3315bd91f62be5fa3593200f9d267bc264811',
    'summary': 'E3-01至E3-07的现有组合审查已经完成，十三项修正已独立复核；任务取消P3-T01为新增待修复项，修复后需要当前组合复验和最终报告',
    'requirements': [
        {'id': 'E3-01', 'title': '持久控制状态机', 'status': 'verified_in_reviewed_combination',
         'scope': '统一状态转换、原子审计与回执、来源版本、终止Journal、生产入口、历史资源读取授权'},
        {'id': 'E3-02', 'title': '动作日志与结果核对', 'status': 'verified_in_reviewed_combination',
         'scope': '发送前持久预留、命令身份与内容去重、未知结果只读核对、可信来源绑定、证据云边同步、实际设备发送次数'},
        {'id': 'E3-03', 'title': '人工干预流程', 'status': 'verified_in_reviewed_combination',
         'scope': '取消、核对、恢复、当前身份和授权、审批与条件、执行权、云端离线pending、重复/乱序/跨期、实际页面409/403/401'},
        {'id': 'E3-04', 'title': '历史序列回放', 'status': 'waiting_for_P3-T01',
         'scope': '跨版本资产、精确数值、质量、时钟、历史窗口、独立状态、快照、SIGKILL恢复均已核对；等待任务取消修复'},
        {'id': 'E3-05', 'title': '规则版本效果比较', 'status': 'verified_in_reviewed_combination',
         'scope': '两侧共同输入/时钟/历史/初态，独立状态，差异/一致/缺失分别呈现，创建与导出保留精确整数'},
        {'id': 'E3-06', 'title': '影子计算', 'status': 'waiting_for_P3-T01',
         'scope': '生产观测入口、重复/迟到/修订、正式补算来源、候选替换/停止、前序状态解析、有限增长量均已核对；等待父代任务取消修复'},
        {'id': 'E3-07', 'title': '计算及执行过程解释', 'status': 'verified_in_reviewed_combination',
         'scope': '实际节点输入/输出/历史及资产版本、两侧计划OTLP、失败状态、控制证据及反馈时间线、当前授权'},
    ],
    'findings': findings,
    'verification_commands': commands,
    'counts_scope': '顶层、叶子、全部命名测试和条件跳过分别记录；前后修正的重复回归不相加为唯一用例数量',
    'reviewed_source': {
        'source_snapshot': own('reviewed-source-through-c05.json'), 'files': len(source['files']),
        'compiled_input_provenance': own('reviewed-compiled-inputs-through-c05.json'), 'compiled_inputs': len(compiled['inputs']),
        'compiled_input_method': compiled['method'],
        'backend_full_regression': own('c05-platform-full.metadata.json'),
        'datatransfer_regression': own('datatransfer-full.metadata.json'),
        'frontend_production_build': own('c05-frontend-production-build.metadata.json'),
        'frontend_static_sha256': 'f02c8f65921cac0ef95ce2e8f7d001188e11b60c8e89c35215ac4c625b0dc9cd',
        'frontend_static_file': 'Frontends/dist/assets/main-CiVE3bj4.js',
        'contract_regeneration': own('c05-contract-byte-comparison.json'),
        'frontend_specialist_audit': own('final-frontend-specialist-audit.json'),
        'original_specialist_audit': own('initial-specialist-audit.json'),
        'control_c05_specialist_audit': own('c05-specialist-audit.json'),
        'source_changes_after_reviewed_checks_at_checkpoint': [],
    },
    'actual_services_and_binary_binding': {
        'current_control_browser': own('browser-process-v2.json'),
        'initial_browser': own('browser-process.json'),
        'actual_dual_application': own('c05-independent-dual-app-audit.json'),
        'history_binary': {'path': '/private/tmp/sf-history-fixture-20261005',
                           'sha256': '171df7a4a6cbf818f7d26c0331fd1d506d5f06574ca9e06379eb6a8f4b8a3727',
                           'build_completed_at': '2026-10-04T20:23:13.041787+00:00',
                           'scope': '原生产历史代码、SQLite、River workers；后续C05控制回执修正由新的控制夹具独立覆盖；P3-T01尚未修复'},
        'nats_target': '2.14.7', 'nats_actual_nodes': 3,
        'nats_evidence': '已逐项核对控制专业实际三节点mTLS原始记录及构建输入，独立读取协调执行源码，并运行当前协调/控制普通与race检查；本角色未重新创建NATS集群',
        'postgres': '18.6；21项原专项与2项C05新专项分别完成，独立schema、3open/1idle、1管理连接、包并行1；race与PG串行',
    },
    'browser_scope': {
        'control': '实际双应用离线提交及重复202，同operation_id；恢复后核对原命令与剩余动作各1次；旧版本取消409，正常取消后未知结果，迟到成功保留，后续动作0；403清除旧证据和允许操作，401清除真实sessionStorage',
        'history': '实际创建比较保留9007199254740993输入与9007199254740996节点结果，导出整数；跨资产版本回放的UNCERTAIN/null；影子正式补算v2来源、候选停止/重新启用与409；失败任务沿同River25重试8到9仍如实失败；River26等待父代取消四次409',
        'page_errors': [],
        'intentional_http_errors': '保存了400/409/403/401及对应操作上下文，普通favicon404不属于业务失败',
        'visual_followup': '原截图在抽屉顶部，仅DOM文字包含证据；补充v2-control-trusted-evidence-visible和v2-control-feedback-visible已滚动到相应内容并检查图像',
    },
    'first_failures_and_corrections': [
        '控制专业首次全模块中两项迁移计数断言过时；当前独立两轮全模块已通过，原失败报告保留',
        '独立发现C05云端缺少原命令证据，首次原始API与页面证据保留；修正后的当前双应用、PG、race及页面复验通过',
        'F01/F02/F03原始页面问题保留于前端报告；第三轮前端与独立页面验证通过',
        '浏览器辅助脚本曾遇到旧元素引用、带loading前缀的ARIA名称及短于链路退避的等待，使用新快照和实际状态继续，原日志保留',
        '任务状态补充读取首次使用两条错误路径产生404，随后按正式generated-routes的/analysis-runs读取；409原请求独立保存',
        '最终前端证据核对脚本首次假设first-unit.log和TAP标签；按命令记录实际unit.log与Node文本计数修正，50份类型引用、154份索引、41份源码及全部保留产物匹配',
        'P3-T01四次409为实际产品问题，已交任务队列工程师修复，尚未记为通过',
    ],
    'conditional_checks': {
        'ordinary_skips': '每条测试名称与原因在命令metadata内保存；PG、实际设备、浏览器、插件和目标三节点在各自独立或专业实运行记录中核对',
        'subprocess_helpers': 'TestHistoryCrashProcess和TestTaskCrashProcess只在父测试启动的子进程条件下运行，父级SIGKILL/恢复测试在当前全模块通过',
        'earlier_stage_infrastructure': 'PostgreSQL16到18迁移、原生ThingsBoard/Kafka及原三节点基础验证沿用第二阶段独立验收，当前未改动部分按源码摘要判断适用范围',
        'user_excluded': ['完整UOS安装恢复', '1或3节点连续断云24小时', '受限链路专项', '实际24小时容量及持续运行'],
    },
    'cleanup': own('cleanup-waiting-t01.json'),
    'credential_scan': own('credential-scan-waiting-t01.json'),
    'work_volume_free_bytes': os.statvfs(ROOT).f_bavail * os.statvfs(ROOT).f_frsize,
    'retained_data': [str(R.TMP / name) for name in ['control-browser', 'control-browser-v2',
        'history-browser', 'history-browser-v2', 'control-integration', 'control-integration-v2', 'browser-static', 'browser-static-v2']],
    'resume': {
        'trigger': '主协调交付tasks-cancel-t01-verified.json及当前编译历史夹具后再次启动独立复验',
        'required': [
            '核对T01专业报告、实际源码和编译输入摘要，保留本检查点的原组合与首次失败',
            '启动新名称的隔离历史夹具和浏览器，持续生产River调度，读取一次任务版本，等待至少3秒并通过正常页面确认取消',
            '核对同一task/River/run/快照、取消状态、无继续计算及原子审计；实际业务变化、重复取消、撤权仍按当前权限与版本拒绝',
            '按T01影响范围补充SQLite/PG/race和必要当前组合回归，检查HTTP契约及最终前端产物摘要',
            '逐项更新独立报告，全部必要验证完成后提交.local/evolution/phase3-independent-review.json，由主协调写全局验收',
        ],
        'helpers': [own('run_review.py'), own('browser_review_v2.py'), own('browser_actions_v2.py'), own('fixture_mutations_v2.py')],
        'fixture_note': '旧数据和截图保留，新夹具使用新目录；已关闭的历史服务为PID51755/61900，控制夹具通过browser-stop正常退出，八个端口均关闭',
    },
}

index_paths = [p for p in OUT.rglob('*') if p.is_file() and '__pycache__' not in p.parts
               and p.name != 'evidence-index-waiting-t01.json']
index_paths += [p for p in (ROOT / 'output/playwright/phase3-independent').rglob('*') if p.is_file()]
index = {'recorded_at': checkpoint['recorded_at'], 'files': [ref(str(p.relative_to(ROOT))) for p in sorted(set(index_paths))]}
R.json_write(OUT / 'evidence-index-waiting-t01.json', index)
checkpoint['evidence_index'] = own('evidence-index-waiting-t01.json')
target = ROOT / '.local/evolution/phase3-independent-review-checkpoint.json'
R.json_write(target, checkpoint)
print(json.dumps({'path': str(target), 'sha256': R.sha(target), 'status': checkpoint['status'],
                  'verified_findings': 13, 'waiting_findings': ['P3-T01'],
                  'commands': len(commands), 'indexed_files': len(index['files'])}, ensure_ascii=False))
