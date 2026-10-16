from pathlib import Path
import datetime
import hashlib
import json

root = Path('/path/to/SmartFactory-2026')
evolution = root / '.local/evolution'
ui = Path('/private/tmp/smartfactory-release-interface-20261005')
now = datetime.datetime.now(datetime.timezone.utc).isoformat()


def read(path):
    return json.loads(path.read_text())


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def ref(path):
    content = path.read_bytes()
    return {'path': str(path), 'sha256': hashlib.sha256(content).hexdigest(), 'bytes': len(content)}


transition = Path('/private/tmp/smartfactory-evolution-pg18-final-20261005/pg60-transition/verification.json')
pg = read(transition)
assert pg['status'] == 'passed' and pg['logical_data_unchanged']
assert pg['before_system_identifier'] == pg['after_system_identifier']
assert all(item['normalized_sha256'] == pg['after_databases'][name]['normalized_sha256'] for name, item in pg['before_databases'].items())
incident = read(evolution / 'phase5-pg-capacity-incident/summary.json')
incident['updated_at'] = now
incident['status'] = 'replacement_reinitialized_logical_data_preserved_connection_limit_60_native_restart_verification_in_progress'
incident['replacement']['container'] = 'smartfactory-evolution-pg18-final60-20261005'
incident['replacement']['max_connections'] = 60
incident['replacement']['readiness'] = 'SQL settings, unchanged cluster identifier and full normalized logical dumps of four databases verified before and after restart'
incident['connection_limit_transition'] = ref(transition)
incident['native_databases'] = {'initialization': 'formal installation and bootstrap completed on replacement instance', 'cloud': 'sf_delivery_tbce_20261005', 'edge': 'sf_delivery_tbedge_20261005', 'current_verification': '/private/tmp/smartfactory-delivery-native-restart-final-20261005'}
incident['next_steps'] = ['finish retained-data Edge restart checks using newly initialized native databases', 'final independent PostgreSQL tests use the updated private descriptor and newly isolated schemas']
write(evolution / 'phase5-pg-capacity-incident/summary.json', incident)

report_path = evolution / 'phase5-first-release-flow-root-review.json'
previous = report_path.read_bytes()
previous_path = ui / 'first-release-root-review-before-complete-case.json'
if not previous_path.exists():
    previous_path.write_bytes(previous)
report = json.loads(previous)
case = ui / 'browser-first-isolated'
stats = read(case / 'results.json')['stats']
empty = read(case / 'first-release-empty-directory.json')
saved = read(case / 'first-release-fixed-record.json')
trace = read(case / 'first-release-fixed-saved.json')
assert stats['expected'] == 1 and stats['unexpected'] == stats['skipped'] == stats['flaky'] == 0
assert empty == {'status': 200, 'records': []}
assert len(saved['manifest']['components']) == 4
required = [('POST', '/api/sf/v1/releases/validate'), ('POST', '/api/sf/v1/releases'), ('GET', '/api/sf/v1/releases/' + saved['id'])]
for method, suffix in required:
    assert any(q['method'] == method and q['url'].endswith(suffix) and q['status'] == 200 for q in trace['requests'])
assert not trace['errors']
report['checked_at'] = now
report['status'] = 'corrected_complete_empty_catalog_browser_case_verified_final_frozen_combination_and_independent_review_pending'
report['complete_fixed_browser_stats'] = stats
report['complete_fixed_behavior'] = {'initial_release_count': 0, 'saved_release_id': saved['id'], 'saved_sha256': saved['sha256'], 'component_ids': [c['id'] for c in saved['manifest']['components']], 'formal_validate_save_read_status': [200, 200, 200], 'observed_screenshot': str(case / 'first-release-fixed-saved.png')}
report['remaining_verification'] = 'Final frozen frontend combination and independent review; identify the URL of the captured console 404 before classification'
report['console_observation'] = trace['console']
report['references'].append(ref(previous_path))
for filename in ['results.json', 'first-release-empty-directory.json', 'first-release-fixed-record.json', 'first-release-fixed-saved.json', 'first-release-fixed-saved.png', 'first-release-fixed-validation.json', 'first-release-fixed-validation.png']:
    report['references'].append(ref(case / filename))
write(report_path, report)

findings_path = evolution / 'phase5-coordinator-findings.json'
findings = read(findings_path)
findings['updated_at'] = now
f01 = next(x for x in findings['items'] if x['id'] == 'P5-F01')
f01['status'] = 'corrected_complete_empty_catalog_browser_case_coordinator_verified_final_combination_and_independent_review_pending'
f01['verification_result'] = '独立空目录实际复验一项通过，初始GET返回空数组，正式校验、登记和按ID读取均为200；页面与保存记录显示程序、规则、参数和连接器四个组件。先前保存后抽屉断言失败记录保留，最终固定页面组合与独立复验继续进行；一条控制台404等待定位实际URL。'
f01['evidence'].append(str(case / 'results.json'))

if not any(x['id'] == 'P5-F02' for x in findings['items']):
    findings['items'].append({'id': 'P5-F02', 'owner': 'phase4_frontend', 'status': 'actual_browser_failure_and_old_state_update_reviewed_correction_verification_in_progress', 'requirements': ['E5-04', 'E5-05'], 'finding': '手动刷新操作依据的请求尚未结束时填写处理原因，响应回调使用旧状态对象更新版本，覆盖后来填写的原因，暂停按钮保持禁用', 'evidence': [str(ui / 'before-basis-fix/manifest.json'), str(ui / 'browser-pipeline-third/results.json'), str(ui / 'browser-pipeline-third/frontend-offline-1791187671910-heartbeat-basis.json')], 'runtime_reproduction': [ref(ui / 'browser-pipeline-third/results.json'), ref(ui / 'before-basis-fix/manifest.json')], 'observed_effect': '第一批已完成、第二节点等待恢复，前后业务版本均为5且暂停允许；实际页面原因变为空，暂停按钮等待超过180秒。旧回调setBasis({...basis, version})及三份受影响源文件、十九份静态产物已经保存。', 'correction': '由前端专业对当前对象匹配的状态采用函数式版本更新，检查发布、身份、模板和配置的相同手动刷新流程', 'verification': '延迟真实HTTP响应后核对用户新输入、当前对象及业务版本，完成后续正式操作并检查请求和审计', 'verification_result': '修正及实际复验进行中'})
if not any(x['id'] == 'P5-F03' for x in findings['items']):
    findings['items'].append({'id': 'P5-F03', 'owner': 'phase4_frontend', 'status': 'specialist_actual_browser_reproduction_coordinator_http_sequence_review_pending', 'requirements': ['E5-01', 'E5-05'], 'finding': '发布只读身份进入页面后，无条件请求仅配置管理人员获准读取的目录，403触发页面缓存清理，已有读取权限的发布详情消失', 'evidence': [str(ui / 'browser-permissions-before/results.json')], 'runtime_reproduction': [ref(ui / 'browser-permissions-before/results.json')], 'observed_effect': '实际只读浏览器用例找不到已请求的发布详情，专业继续保存发布读取200、编辑目录403与详情消失的完整时序', 'correction': '前端按正式权限决定编辑目录查询，使获准读取的发布状态在部分目录拒绝时继续可用', 'verification': '核对只读、部分编辑权限及完整管理权限三类实际身份，保留前后请求与页面结果', 'verification_result': '原始HTTP交叉核对、修正和复验进行中'})
write(findings_path, findings)

status_path = root / 'docs/evolution/status.md'
status = status_path.read_text()
status = status.replace('当前由发布协调与节点配置两位专业共同推进。', '节点配置及发布后端已交付，当前由构建交付和发布界面两位专业继续完成组合验证。', 1)
status = status.replace('| 发布协调工程师 | 6 Astra，Max | 发布清单、节点协调、批次与恢复 | 第五阶段实施中 |', '| 发布协调工程师 | 6 Astra，Max | 发布清单、节点协调、批次与恢复 | 第五阶段后端交付完成 |')
status = status.replace('| 配置与节点身份工程师 | 6.1 Sol，Max | 身份授权、配置分发、应用及运行报告 | 第五阶段实施中 |', '| 配置与节点身份工程师 | 6.1 Sol，Max | 身份授权、配置分发、应用及运行报告 | 第五阶段后端交付完成 |\n| 构建交付工程师 | 6.1 Sol，Max | 正式打包、原生平台停止与启动、最终回归及文档 | 第五阶段进行中 |\n| 发布界面工程师（沿用第四阶段成员） | 6.1 Sol，Max | 首次发布、分批操作、节点身份、模板演进及浏览器验证 | 第五阶段进行中 |')
status = status.replace('第五阶段已新增发布协调与节点配置两位专业，当前同时运行两位。', '第五阶段已新增发布协调、节点配置及构建交付三位专业，当前构建交付与沿用的前端成员同时运行。')
status += '\n2026-10-05 16:20（Asia/Shanghai）：独立空目录首次发布完成实际浏览器复验，四组件清单的校验、登记和读取均为200；P5-F01等待最终固定组合与独立复验。实际页面进一步复现刷新覆盖新输入的P5-F02和只读发布被编辑目录403打断的P5-F03，前端专业正在修正。原生CE/Edge已完成本轮新库初始化及双侧遥测写入，保留数据再次启动检查继续进行；主协调在窗口交接期间完整备份四个数据库，将持久PG上限30改为60，前后实例标识和完整逻辑数据一致，原容器正常退出0。累计38项验收通过，第五阶段七项继续验证。\n'
status_path.write_text(status)

brief_path = root / 'docs/evolution/brief-final-independent-review.md'
brief = brief_path.read_text()
brief += '\n持久PG测试实例已调整为 `smartfactory-evolution-pg18-final60-20261005`，连接上限60，内存1GiB与2CPU保持原设置。调整之前及之后完整导出四个非模板数据库，cluster SystemIdentifier相同，去除PG18随机restrict/unrestrict控制行后的全部逻辑导出摘要相同；证明见 `/private/tmp/smartfactory-evolution-pg18-final-20261005/pg60-transition/verification.json`。最终PG检查读取当前私有描述，使用新隔离schema。原生保留数据复验最新目录为 `/private/tmp/smartfactory-delivery-native-restart-final-20261005`。\n\nP5-F01独立空目录完整用例已经由主协调核对：一项通过、四个组件、正式校验/登记/读取均为200，历史失败保留。P5-F02为刷新回调覆盖后来填写的操作原因，P5-F03为发布只读页面请求配置管理目录返回403后详情消失；前端专业保存首次失败、旧源码和实际产物，最终独立审查按 `.local/evolution/phase5-coordinator-findings.json` 核对修正后的相同条件、部分权限和当前对象切换。\n'
brief_path.write_text(brief)

acceptance_path = root / 'docs/evolution/acceptance.json'
acceptance = read(acceptance_path)
acceptance['updated_at'] = now
for item in acceptance['requirements']:
    if item['id'] == 'E5-05':
        item['implementation'] = ['Frontends/src/releases.tsx', 'Frontends/src/workload-identities.tsx', 'Frontends/src/template-evolution.tsx']
        item['evidence'] = ['.local/evolution/phase5-first-release-flow-root-review.json', '.local/evolution/phase5-coordinator-findings.json']
        item['notes'] = ['首次空目录四组件清单实际通过；分批发布、节点身份和模板演进的完整页面验证继续进行，刷新输入保留与只读权限问题等待修正后复验和独立结论。']
write(acceptance_path, acceptance)

usage = read(evolution / 'usage-monitor.json')
usage['last_checked_at'] = '2026-10-05T08:19:23+00:00'
usage['latest'].update(used_percent=43, remaining_percent=57, available_reset_cards=0, ordinary_usage_allowed=True)
write(evolution / 'usage-monitor.json', usage)
print(json.dumps({'findings': len(findings['items']), 'first_release_browser': stats, 'pg_transition': pg['status'], 'accepted': sum(x['verification_status'] == 'passed' for x in acceptance['requirements'])}, ensure_ascii=False))
