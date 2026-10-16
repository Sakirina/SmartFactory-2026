#!/usr/bin/env python3
"""Publish the independent E4 review using retained, separately versioned evidence."""
import datetime
import hashlib
import json
import pathlib
import shutil
import subprocess

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
EV = ROOT / '.local/evolution'
BASE = pathlib.Path('/private/tmp/smartfactory-phase4-independent-browser-20261005')
BROWSER = EV / 'phase4-review-browser'
TARGET = EV / 'phase4-independent-review.json'

def read(path):
    return json.loads(pathlib.Path(path).read_text())

def ref(path):
    path = pathlib.Path(path)
    return {'path': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size}

def browser_ref(relative):
    return ref(BASE / relative)

assert not TARGET.exists(), 'Preserve an existing final review'
checkpoint_path = EV / 'phase4-independent-review-checkpoint.json'
assert ref(checkpoint_path)['sha256'] == '6e5332b2d6a949a2c228d415e408535a4d3e646aa5c05e02455e21eaa67e6c46'
checkpoint = read(checkpoint_path)
a02 = read(EV / 'phase4-review-a02/summary.json')
browser = read(BROWSER / 'summary.json')
specialist = read(BROWSER / 'specialist-final-audit.json')
cleanup = read(BROWSER / 'final-resource-cleanup.json')
assert browser['status'] == 'independent_browser_review_passed'
assert not specialist['mismatches'] and cleanup['resources_released']
saved_findings = BASE / 'coordinator-findings-at-independent-completion.json'
shutil.copy2(EV / 'phase4-coordinator-findings.json', saved_findings)
finding_ledger = read(saved_findings)['items']
assert len(finding_ledger) == 16

page_evidence = {
    'E4-01': ['browser-state-first/query-paging-scope-reset.json', 'browser-state-first/query-paging-error-new-snapshot.json', 'browser-state-first/F01-http-sse-snapshot-race.json'],
    'E4-02': ['browser-state-first/F01-http-sse-snapshot-race.json', 'browser-precision-first/query-heartbeat-resume.json'],
    'E4-03': ['browser-business-final/semantic-impact-and-retained-editor.json', 'browser-details-first/draft-conflict-preserved-edit.json', 'browser-ai-first/ai-user-isolation-and-editor-cleared.json', 'browser-ai-first/ai-expired-session-login.json'],
    'E4-04': ['browser-business-final/protocol-metadata-roundtrip.json', 'browser-details-first/three-protocol-roundtrips.json', 'browser-details-first/node-metadata-exact-save.json', 'browser-state-first/F02-raw-json-repair.json', 'browser-state-first/F02-deleted-invalid-item.json', 'browser-state-first/F02-section-error-identity.json'],
    'E4-05': ['browser-business-final/semantic-impact-and-retained-editor.json', 'browser-details-first/draft-conflict-adoption.json'],
    'E4-06': ['browser-business-final/alarm-persisted.json', 'browser-business-final/alarm-continuous-confirmation.json', 'browser-details-first/alarm-conflict-explicit-adoption.json'],
    'E4-07': ['browser-ai-first/ai-byte-correspondence.json', 'browser-ai-first/ai-escaped-definition-navigation.json', 'browser-ai-first/ai-reference-states.json', 'browser-ai-first/ai-terminal-states.json', 'browser-ai-first/ai-original-resource-deleted-cleared.json', 'browser-extra-diagnostic/independent-ai-poll-deleted-cache.json'],
    'E4-08': ['browser-business-final/handover-persisted.json', 'browser-details-first/work-order-completed.json', 'browser-details-first/work-order-former-owner-permission.json'],
    'E4-09': ['browser-business-final/templates-persisted.json', 'browser-details-first/template-conflict-and-retry.json', 'browser-details-first/template-failed-retry.json'],
    'E4-10': ['r1-runtime/browser-precision/exact-values-browser.json', 'r1-runtime/browser-precision/screen-exact-export.json', 'r1-runtime/browser-empty/independent-empty-surfaces.json', 'r1-runtime/browser-mobile/mobile-export-actions.json'],
}
conclusions = {
    'E4-01': 'SQLite和PostgreSQL实际筛选、稳定分页、投影重建、删除修订及当前授权验证通过；页面范围变化后采用新快照，旧页和旧HTTP结果不会恢复已移出的记录。',
    'E4-02': '初始快照、提交可见游标、迟提交、过期恢复、撤权、多订阅者和并发队列清理通过；实际HTTP/1.1及HTTP/2空闲心跳和慢客户端预算通过，页面15167毫秒后取得心跳及新增量，再以原游标恢复成功。',
    'E4-03': '生成客户端和TanStack Query实际用于服务端读取与写入，缓存身份包含用户及授权信息；页面切换、并发冲突保留编辑，账号切换及会话失效清理缓存和编辑状态，迟到HTTP与SSE次序验证通过。',
    'E4-04': 'Modbus TCP、MQTT、OPC-UA及节点元数据的类型、单位、范围、枚举、必填和精确值往返通过；JSON修正、删除无效重复项和不同分区同名字段的错误状态正确更新，中文设备名称选择保存通过。',
    'E4-05': '节点、连接和参数语义差异、依赖及资源影响的后端验证通过；页面显示身份与版本，冲突时保留本地输入，用户明确采用新版本后继续保存。',
    'E4-06': '持续规则输入后按已阅动作版本完成确认、分配和备注；普通观测更新允许人工处置，人工状态及权限变化仍触发正式校验。原生轮询的404、损坏内容、超过64项轮转、期限和worker周期恢复通过。',
    'E4-07': '实际本地模型五类工具的ToolCallID、查询范围、原文和SHA256对应，中文斜杠冒号标识导航通过；不存在、伪造、无权、到期、工具失败和预算状态正确处理，原资源删除后依据404，调查答案、消息、参数和资源内容清除，页面轮询同步隐藏旧内容。',
    'E4-08': '工单创建、开始处理、责任人变更、完成和交接两项未完成事项通过，关联告警和执行保留审计；事务内完整执行证据资源授权与并发集合变化验证通过，原责任人的操作权限随交接更新。',
    'E4-09': '五类模板通过固定设备和配置版本批量准备25份草稿，引用授权、版本冲突、参数错误及重试通过；失败批次保留失败信息并避免留下部分草稿。',
    'E4-10': 'ECharts6.1.0及锁定依赖完成独立安装、构建、类型和npm审计；云端、现场、大屏的1440×1000、1024×768、390×844实际图表和空数据页面均通过，九种空数据文档宽度等于视口且水平滚动为0，工具提示、表格及导出保持精确整数。',
}
criteria = []
for old in checkpoint['criteria']:
    key = old['id']
    criteria.append({'id': key, 'title': old['title'], 'independent_verification_status': 'passed', 'conclusion': conclusions[key], 'backend_checkpoint_criterion': {'report': ref(checkpoint_path), 'criterion_id': key, 'recorded_test_links': len(old['tests'])}, 'current_backend_reverification': ref(EV / 'phase4-review-a02/summary.json'), 'browser_evidence': [browser_ref(path) for path in page_evidence[key]], 'source_version': 'A02 backend plus formal frozen frontend bb13; style-only R1 verified separately with identical JavaScript, generated client, contracts and locked dependencies'})

old_findings = {item['id']: item for item in checkpoint['coordinator_findings']}
finding_results = {
    'P4-Q01': '实际HTTP/1.1、HTTP/2空闲15秒心跳及后续提交通过，慢客户端写入预算和其他订阅继续运行；独立页面心跳及恢复另行通过。',
    'P4-Q02': '队列清理与客户端继续读取并发测试及race通过，共享订阅锁释放，其他订阅和取消能够结束。',
    'P4-Q03': 'PostgreSQL受限权限查询、四类分页、订阅和两个独立对象迟提交测试通过，SQLite回归通过。',
    'P4-B01': '普通测试、PG并发、race及持续采样页面操作通过，人工确认使用动作版本，后续人工状态冲突需要明确采用新的操作依据。',
    'P4-B02': '每版本秘密引用、延迟旧任务、新版并发保存和新版先应用的普通与race验证通过。',
    'P4-B03': '命令证据设备授权、撤销成员和检查后新增证据在正式事务中拒绝，普通与race测试通过。',
    'P4-B04': '规范JSON字段和旧Go字段名兼容往返、实际ConfigManager配置应用及旧版本拒绝通过。',
    'P4-B05': '404、损坏映射及慢映射后的有效告警继续同步，超过64项轮转和恢复通过；正式worker立即首轮及周期调用通过。',
    'P4-B06': 'OpenAPI、独立Schema与实际HTTP必须字段、合法零值及省略默认参数一致，最终契约重新生成比较160项原文一致，17项发布部署检查通过。',
    'P4-A01': '只授权设备且未授权来源节点的趋势依据可读取，撤销设备权限后依据拒绝，普通及race验证通过。',
    'P4-A02': '保留原v2/v3失败；独立核对最小JSON null断言修正后，以当前组合复验依据404、答案和消息清空、参数null及资源清空，普通119项、PG17项与race20项通过，页面删除失效及自动轮询隐藏通过。',
    'P4-F01': '真实Ingest和100行窗口先取得9999、7777并移出81，延迟旧HTTP返回后页面保留更新结果和当前恢复游标；首次查询与手动刷新验证通过。',
    'P4-F02': '完整JSON修正、删除含无效字段数据点及不同分区同名字段三个原始场景全部通过。',
    'P4-F03': '使用完整中文设备名称搜索、选择并保存，草稿中的设备身份正确。',
    'P4-F04': '原资源删除404、到期410、撤权403及会话401隐藏已打开的旧内容；补充场景在删除后没有再次请求依据，调查轮询仍在2376毫秒内清除原文、参数、链接和答案。',
    'P4-F05': '保留原390px视口产生393px文档和3px水平滚动的九组合证据；R1只调整style.css，九种空数据及九种非空页面通过，手机导出按钮右缘160px且实际导出保留精确整数。',
}
finding_browser = {'P4-B01': ['browser-business-final/alarm-persisted.json'], 'P4-A02': ['browser-ai-first/ai-original-resource-deleted-cleared.json', 'browser-extra-diagnostic/independent-ai-poll-deleted-cache.json'], 'P4-F01': ['browser-state-first/F01-http-sse-snapshot-race.json'], 'P4-F02': ['browser-state-first/F02-raw-json-repair.json', 'browser-state-first/F02-deleted-invalid-item.json', 'browser-state-first/F02-section-error-identity.json'], 'P4-F03': ['browser-details-first/node-metadata-exact-save.json'], 'P4-F04': ['browser-ai-first/ai-open-evidence-expired-cleared.json', 'browser-ai-first/ai-revoked-cache-cleared.json', 'browser-ai-first/ai-expired-session-login.json', 'browser-extra-diagnostic/independent-ai-poll-deleted-cache.json'], 'P4-F05': ['browser-empty-all-before-fix/independent-empty-surfaces.json', 'r1-runtime/browser-empty/independent-empty-surfaces.json', 'r1-runtime/browser-mobile/mobile-export-actions.json']}
findings = []
for item in finding_ledger:
    key = item['id']
    findings.append({'id': key, 'owner': item['owner'], 'requirements': item['requirements'], 'independent_status': 'resolved_and_verified', 'original_problem': item['finding'], 'conclusion': finding_results[key], 'original_record': {'ledger': ref(saved_findings), 'finding_id': key}, 'backend_test_links': old_findings.get(key, {}).get('tests', []), 'browser_evidence': [browser_ref(path) for path in finding_browser.get(key, [])], 'related_summary': ref(EV / ('phase4-review-a02/summary.json' if key == 'P4-A02' else 'phase4-review-browser/summary.json' if key.startswith('P4-F') else 'phase4-independent-review-checkpoint.json'))})

git_check = subprocess.run(['git', 'diff', '--check'], cwd=ROOT, capture_output=True, text=True)
assert git_check.returncode == 0, git_check.stdout + git_check.stderr
run_counts = {'initial_backend': read(EV / 'phase4-review-evidence/run-index.json')['run_count'], 'a02_backend': len(a02['run_metadata']), 'frontend_and_browser': len(list(BROWSER.rglob('*.run.json')))}
review = {'schema': 'smartfactory.phase4-independent-review/1', 'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'reviewer': 'phase4_independent_review', 'phase': 4, 'status': 'independent_review_passed', 'all_applicable_checks_passed': True, 'open_product_findings': [], 'acceptance_recommendation': 'eligible_for_coordinator_acceptance', 'final_acceptance_owner': 'root', 'global_acceptance_files_modified': False,
    'summary': 'E4-01至E4-10及16项阶段发现完成独立验证。后端、实际PostgreSQL、有限race、正式HTTP和独立Chrome页面均有固定输入及运行证据；原28项页面流程和3项独立补充场景最终通过，R1样式变化另行完成受影响回归。8个自建服务及端口已关闭，PG测试会话和schema清理完成。',
    'criteria': criteria, 'findings': findings,
    'fixed_evidence': {'initial_backend_checkpoint': ref(checkpoint_path), 'a02_backend_summary': ref(EV / 'phase4-review-a02/summary.json'), 'frontend_browser_summary': ref(BROWSER / 'summary.json'), 'frontend_specialist_audit': ref(BROWSER / 'specialist-final-audit.json'), 'frontend_specialist_report': ref(BASE / 'specialist-final-report.json'), 'current_backend_snapshot': ref(EV / 'phase4-review-a02/snapshot.json'), 'current_backend_actual_build_inputs': ref(EV / 'phase4-review-a02/build-inputs.json'), 'frontend_formal_manifest': ref(BASE / 'specialist-final-inputs.json'), 'frontend_r1_manifest': ref(BASE / 'frontend-r1/specialist-final-inputs-r1.json'), 'r1_only_source_difference': ref(BROWSER / 'r1/revision-scope.json'), 'browser_artifact_index': ref(BROWSER / 'artifact-index.json'), 'browser_integrity': ref(BROWSER / 'artifact-integrity.json')},
    'validation_counts': {'counting': 'Top-level Go executions and separately versioned browser scenarios; repeated runs are retained separately and are not presented as unique tests', 'initial_backend': checkpoint['summary'], 'supplemental_api_full': checkpoint['supplemental_results']['api_full_diagnostic'], 'supplemental_cloudsync_full': checkpoint['supplemental_results']['cloudsync_full_with_updated_assertions'], 'a02_backend': a02['test_groups'], 'release_deployment': a02['release_deployment'], 'contract_regeneration': checkpoint['supplemental_results']['contract_regeneration'], 'contract_validation': checkpoint['supplemental_results']['contract_validation'], 'frontend': {'standard_frozen_scenarios_passed': 28, 'additional_independent_scenarios_passed': 3, 'unique_scenarios_with_final_pass': 31, 'r1_affected_scenario_executions_passed': 5, 'r1_nonempty_layout_combinations': 9, 'r1_empty_layout_combinations': 9, 'unit_tests_each_fixed_build': 27, 'production_builds': 2, 'typecheck_compatibility_runs': 2, 'static_files_each_build': 15, 'npm_audit_vulnerabilities': 0, 'captured_pages_without_javascript_errors': 97}, 'recorded_runs': run_counts},
    'original_failures_and_resolutions': {'initial_backend': ref(checkpoint_path), 'a02_original_v2_v3_and_null_assertion': ref(EV / 'phase4-review-a02/independent-null-correction.json'), 'frontend_specialist_originals': ref(BASE / 'specialist-final-report.json'), 'independent_f05_and_extra_fixture_setup': ref(BROWSER / 'browser-test-outcomes.json'), 'supplemental_sources': ref(BROWSER / 'browser-source-retention.json'), 'transition_manifest_clarification': ref(BROWSER / 'transition-manifest-retention.json'), 'final_audit_driver': {'initial_error': "The audit helper used row['relative'] for the backend snapshot, whose actual key is 'path'; no validation result was written by that failed attempt", 'retained_original': ref(BASE / 'audit-frontend-first.py'), 'corrected_driver': ref(EV / 'phase4-review-evidence/audit_frontend_final.py'), 'retest': ref(BROWSER / 'specialist-final-audit.json')}},
    'conditional_tests_and_prior_stage_regressions': {'initial_skip_index': ref(EV / 'phase4-review-evidence/skipped-test-audit.json'), 'postgres': 'Ordinary-mode PostgreSQL skips are linked to actual isolated PostgreSQL executions; initial36 and finalA0217 top-level tests passed', 'native_ce': {'status': 'specialist_actual_native_evidence_checked_with_current_affected_code_retested', 'evidence': ref(EV / 'phase4-review-a02/native-history-audit.json'), 'scope': 'Actual CE lifecycle keeps its original compiled source and binary. Current B05 adapter/worker changes were independently tested with controlled HTTP failures, rotation, deadlines, recovery and race; this review does not relabel that historical CE run as a current-binary CE execution'}, 'external_fixtures': 'External OPC-UA, MQTT broker, sidecar-process, durable-process and controlled PostgreSQL-upgrade fixtures keep their documented condition skips and prior-stage evidence. Current affected TLS/configuration, collection, control, synchronization, authorization, task, exact-value and AI paths completed their applicable local regression.', 'browser_group_skips': 'Shared business/surfaces test files use phase selectors; every selected logical scenario passed in its proper group', 'previous_stage_source_retention': str(ROOT / '.local/evolution/phase3-preserved-paths.json')},
    'nonblocking_observations': [{'topic': 'control budget timing', 'detail': 'The first five-second wait did not observe the second backlog worker. The original binary repeated successfully, five diagnostic repeats passed, and both complete API reruns passed with unchanged deadlines; the original timing cause remains unproven', 'evidence': ref(checkpoint_path)}, {'topic': 'frontend bundle notice', 'detail': 'Vite reports a main chunk over 500 kB; production builds and the required page behavior pass, and the original build logs preserve its actual size', 'evidence': ref(BROWSER / 'r1/build.run.json')}],
    'excluded_by_user': ['完整UOS安装恢复', '1/3节点连续断云24小时', '受限链路专项', '实际24小时容量与持续运行'],
    'resource_cleanup': ref(BROWSER / 'final-resource-cleanup.json'), 'resources_released': True, 'privacy': ref(BROWSER / 'privacy-check.json'),
    'review_changes': {'product_code_changes_by_reviewer': 0, 'test_only_repository_changes': ref(EV / 'phase4-review-evidence/review-test-changes.json'), 'independent_test_material': ref(BROWSER / 'browser-source-retention.json'), 'git_diff_check_exit_code': git_check.returncode, 'global_status_updates_owner': 'root'},
    'handoff': 'Independent checks complete; root may apply phase acceptance and start phase five using the preserved final source combination.'}
TARGET.write_text(json.dumps(review, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'report': ref(TARGET), 'criteria_passed': len(criteria), 'findings_resolved': len(findings), 'run_counts': run_counts, 'resources_released': True}, ensure_ascii=False))
