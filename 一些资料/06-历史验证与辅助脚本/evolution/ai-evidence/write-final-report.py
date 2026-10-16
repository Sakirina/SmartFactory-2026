#!/usr/bin/env python3
import base64,collections,datetime,hashlib,json,pathlib
root=pathlib.Path('/path/to/SmartFactory-2026')
out=root/'.local/evolution/ai-evidence'
def read(path):return json.loads(pathlib.Path(path).read_text())
def ref(path):
    path=pathlib.Path(path)
    return {'path':str(path),'sha256':hashlib.sha256(path.read_bytes()).hexdigest(),'bytes':path.stat().st_size}
snapshot=read(out/'final-snapshot.json')
workspace=pathlib.Path(snapshot['workspace'])
inputs=read(out/'final-build-inputs.json')
binaries=read(out/'final-binaries.json')
runs=read(out/'final-test-runs.json')
assert len(runs)==8 and all(r['exit_code']==0 for r in runs)
assert len(binaries)==11
compile_changes=[item['path'] for item in inputs['files'] if hashlib.sha256(pathlib.Path(item['path']).read_bytes()).hexdigest()!=item['sha256']]
preserved_changes=[item['preserved_path'] for item in inputs['files'] if hashlib.sha256(pathlib.Path(item['preserved_path']).read_bytes()).hexdigest()!=item['sha256']]
snapshot_changes=[item['path'] for item in snapshot['files'] if hashlib.sha256((workspace/item['path']).read_bytes()).hexdigest()!=item['sha256']]
original_go_changes=[item['path'] for item in snapshot['files'] if (item['path'].endswith('.go') or item['path'].endswith(('go.mod','go.sum'))) and hashlib.sha256((root/item['path']).read_bytes()).hexdigest()!=item['sha256']]
assert not compile_changes and not preserved_changes and not snapshot_changes and not original_go_changes
for binary in binaries:assert ref(binary['path'])['sha256']==binary['sha256']
ordinary=[r for r in runs if r['label'].startswith('final-ordinary')]
pg=[r for r in runs if r['postgres']]
race=[r for r in runs if r['race']]
def totals(values):
    return {level:{action:sum(r[level][action] for r in values) for action in ['pass','fail','skip']} for level in ['top_level','leaf']}
command_records=[]
for path in sorted(out.glob('*.metadata.json')):
    value=read(path)
    command_records.append({'label':value['label'],'command':value['command'],'cwd':value['cwd'],'started_at':value['started_at'],'finished_at':value['finished_at'],'elapsed_seconds':value['elapsed_seconds'],'exit_code':value['exit_code'],'test_events':value['test_events'],'top_level':value['top_level'],'leaf':value['leaf'],'metadata':ref(path),'stdout':ref(value['stdout']),'stderr':ref(value['stderr']),'source_changes':value['sources_changed'],'compile_inputs_manifest_sha256':value.get('compile_inputs_manifest_sha256'),'compile_inputs_changed_before':value.get('compile_inputs_changed_before'),'compile_inputs_changed_after':value.get('compile_inputs_changed_after')})
proofs=[]
for path in sorted((out/'captures/final-ordinary-repaired-ai').glob('http-visible-*.json')):
    capture=read(path)
    events=[json.loads(line[6:]) for line in capture['sse'].splitlines() if line.startswith('data: ')]
    for evidence in capture['model_visible_evidence']:
        assert hashlib.sha256(evidence['visible_content'].encode()).hexdigest()==evidence['visible_sha256']
        assert evidence['visible_bytes']==len(evidence['visible_content'].encode()) and evidence['visible_bytes']<=524288
    proofs.append({'api':capture['api'],'stream':capture['stream'],'artifact':ref(path),'investigation_id':capture['investigation']['id'],'status':capture['investigation']['status'],'evidence_status':capture['investigation']['evidence_status'],'actual_tool_results':len(capture['model_visible_evidence']),'event_types':[e['type'] for e in events],'model_visible_results':[{'tool':e['tool_name'],'call_id':e['tool_call_id'],'evidence_id':e['id'],'visible_sha256':e['visible_sha256'],'visible_bytes':e['visible_bytes'],'resources':e['resources']} for e in capture['model_visible_evidence']]})
assert len(proofs)==4
otlp_path=out/'captures/final-ordinary-repaired-ai/otlp-investigation-0.json'
otlp=read(otlp_path)
spans=[span for resource in otlp['resourceSpans'] for scope in resource.get('scopeSpans',[]) for span in scope.get('spans',[])]
names=collections.Counter(s['name'] for s in spans)
evidence_spans=[s for s in spans if s['name']=='ai.investigation.evidence']
assert len(evidence_spans)==5 and names['http.request']>=1 and names['store.transaction']>=1
trace_ids={base64.b64decode(s['traceId']).hex() for s in evidence_spans}
assert trace_ids=={'11111111111111111111111111111111'}
schema=read(root/'contracts/1.0/openapi.json')
contracts=[ref(root/c['path']) for c in read(out/'final-generated-contract-comparison.json')['files']]
contracts += [ref(root/'contracts/1.0/api-types.ts'),ref(root/'Frontends/src/generated-routes.ts')]
schemas=list((root/'contracts/1.0').glob('*.schema.json'))
fixture=read(out/'final-fixture-http.json')
assert fixture['http_requests']==42 and fixture['shutdown']=={'exit_code':0,'application_listener_closed':True,'model_listener_closed':True}
failures={
 'first-functional':('本地模型和既有Responses草稿测试仍解码旧列表结构','分页工具采用 items/scope 响应；本地模型及既有草稿回归按同一响应读取','typed-resource-compiled'),
 'page-mock-compatibility':('显式资源范围撤权后正式查询过滤成空页，旧MCP回归要求范围拒绝','AI query_data 在调用正式用例前检查用户显式请求的资源范围','typed-resource-compiled'),
 'fixture-build':('夹具删除函数错误传递了额外CAS参数','按既有 Tx.Delete(kind,id) 调用','fixture-build-repaired'),
 'investigation-first':('夹具观测时间早于规则发布版本，历史回放找不到对应版本','夹具在规则正式发布后记录并输入观测','typed-resource-compiled'),
 'investigation-regressions':('专用应用测试创建用户时缺少必需姓名','补充专用夹具用户姓名','typed-resource-compiled'),
 'investigation-finite-tests':('直接 Server.Handler 夹具缺少生产 HTTPContext，OTLP缺少HTTP请求跨度','HTTP夹具和隔离命令共用生产 HTTPContext','typed-resource-compiled'),
 'direct-device-source-reproduction':('P4-A01：趋势查询仅按设备授权，通用遍历把 SourceID 节点加入引用授权对象','按各工具类型化结构提取实际授权对象，保留原 SourceID 内容；补直接设备授权和撤权复验','final-ordinary-repaired-ai'),
 'typed-resource-regression':('新增草稿返回装饰时重复声明已有 raw 变量导致编译失败','改为现有变量赋值','typed-resource-compiled'),
 'final-ordinary-ai':('直接运行固定测试二进制时使用模块目录，既有AI测试找不到包内 testdata','在对应包目录运行相同 SHA 的测试二进制','final-ordinary-repaired-ai'),
 'final-ordinary-app':('固定副本缺少既有恢复测试调用的 scripts/backup-state.py','按摘要补充运行脚本，单列 final-runtime-inputs.json；复用同一 app 二进制','final-ordinary-repaired-app')}
first_failures=[]
for label,(cause,repair,retest) in failures.items():
    first=read(out/(label+'.metadata.json'));after=read(out/(retest+'.metadata.json'))
    assert first['exit_code']!=0
    first_failures.append({'label':label,'cause':cause,'repair':repair,'first_metadata':ref(out/(label+'.metadata.json')),'first_stdout':ref(first['stdout']),'first_stderr':ref(first['stderr']),'retest_label':retest,'retest_metadata':ref(out/(retest+'.metadata.json')),'retest_exit_code':after['exit_code']})
go_files=[f for f in snapshot['files'] if f['path'].endswith('.go')]
test_go=[f for f in go_files if f['path'].endswith('_test.go')]
local_inputs=[f for f in inputs['files'] if f['source_class']=='frozen_workspace']
important=['Platform/pkg/model/investigation.go','Platform/pkg/model/assistant.go','Platform/internal/application/investigations.go','Platform/internal/application/investigation_resources.go','Platform/internal/store/investigations.go','Platform/internal/store/migration00009.go','Platform/internal/store/migrations.go','Platform/internal/api/investigation_routes.go','Platform/internal/api/assistant_contract.go','Platform/internal/api/definition_routes.go','Platform/internal/api/contracts.go','Platform/internal/ai/chat.go','Platform/internal/ai/tools.go','Platform/internal/ai/evidence.go','Platform/internal/app/app.go','Platform/cmd/sf-contracts/main.go','Platform/cmd/sf-ai-evidence-fixture/main.go','Platform/internal/aievidencefixture/model.go','Platform/internal/aievidencefixture/seed.go','Platform/internal/ai/investigation_http_test.go','Platform/internal/ai/investigation_observability_test.go','Platform/internal/application/investigations_test.go','Platform/pkg/compatibility/manifest.json']
report={
 'phase':4,'role':'ai_evidence','acceptance_ids':['E4-07'],'status':'passed','implementation_status':'complete','generated_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'all_role_checks_passed':True,'remaining_role_work':[],
 'scope':{'investigation_backend':'工具依据持久化、当前授权复核、引用状态、预算与版本、正式SSE和查询/规则/执行/历史用例复用','public_assembly':'继任API总注册、应用/worker公共装配、Go模块、迁移9和当前查询/业务/调查组合契约汇合维护者','frontend_browser_owner':'/root/phase4_frontend','frontend_browser_status':'由前端角色继续实际页面和当前组合验证'},
 'implementation':{'files':[ref(root/p) for p in important],'endpoints':{'assistant':{'method':'POST','path':'/api/sf/v1/assistant','operation_id':'post_assistant','request':'AssistantRequest','events':'AssistantEvent','content_type':'text/event-stream'},'investigations':[{'method':'GET','path':'/api/sf/v1/investigations','operation_id':'get_investigations'},{'method':'GET','path':'/api/sf/v1/investigations/{id}','operation_id':'get_investigations_id'},{'method':'GET','path':'/api/sf/v1/investigations/{id}/evidence/{evidence_id}','operation_id':'get_investigations_id_evidence_evidence_id'},{'method':'DELETE','path':'/api/sf/v1/investigations/{id}','operation_id':'delete_investigations_id'}]},'evidence_lifecycle':{'default_access_days':7,'expiry':'读取时判断；过期依据详情410，响应清空敏感内容；持久数据由显式删除清空','delete':'已结束调查清空持久输入、答案、错误、原文、参数与资源，保留身份、时间、哈希及审计；运行中409','delivery':'结果先prepared，仅在下一轮模型完整完成后标记delivered','single_model_result_budget_bytes':524288,'bounded_document_reads':{'page_limit':200,'candidate_scan_budget':1000,'cursor_minutes':15,'permissions_filtered_empty_page_can_continue':True,'database_candidate_counts_are_not_visible_totals':True}},'resource_authorization':'查询摘要依据采用原工具实际授权对象；完整业务详情仍由正式业务用例检查。按类型化结构记录原始资源和版本，SourceID及扩展参数文本保留为数据内容。'},
 'source_provenance':{'base_commit':'dde3315bd91f62be5fa3593200f9d267bc264811','frozen_workspace':str(workspace),'snapshot_manifest':ref(out/'final-snapshot.json'),'copied_file_count':len(snapshot['files']),'copied_go_files':len(go_files),'copied_production_go_files':len(go_files)-len(test_go),'copied_test_go_files':len(test_go),'directory_metadata_excluded_from_production_scope':[f['path'] for f in snapshot['files'] if pathlib.Path(f['path']).name=='.DS_Store'],'actual_compile_inputs':ref(out/'final-build-inputs.json'),'actual_compile_input_count':len(inputs['files']),'actual_workspace_input_count':len(local_inputs),'external_inputs_include_active_toolchain_and_module_files':True,'actual_input_definition':inputs['scope'],'runtime_inputs_supplement':ref(out/'final-runtime-inputs.json'),'copy_source_changes':snapshot['copy_source_changes'],'copy_digest_differences':snapshot['copy_digest_differences'],'compiled_inputs_changed_after_all_verification':compile_changes,'preserved_compiler_inputs_changed':preserved_changes,'snapshot_files_changed':snapshot_changes,'current_worktree_go_module_changes_from_frozen_snapshot':original_go_changes,'module_locks':[ref(workspace/p) for p in ['Platform/go.mod','Platform/go.sum','DataTransfer/go.mod','DataTransfer/go.sum']],'migrations_1_through_8':ref(out/'final-migrations-1-through-8.json'),'registered_ai_migration':9,'go_version':inputs['go_version'],'go_binary':ref(inputs['go_binary']),'binaries_manifest':ref(out/'final-binaries.json'),'binaries':binaries},
 'verification':{'counts_are_actual_executions_including_repeated_protocol_subtests':True,'ordinary_five_packages':totals(ordinary),'postgres_two_isolated_suites':totals(pg),'limited_race':totals(race),'race_scope':'AI及应用用例 TestInvestigation 专项；PG在独立普通二进制专用运行验证','conditional_skips':'普通全包中的通用PG、原生CE、受控设备子进程等测试按环境条件跳过；本角色两项PG在随后专用运行通过；race中的两项PG跳过，独立PG运行已完成','test_runs_manifest':ref(out/'final-test-runs.json'),'test_runs':runs,'protocol_and_model_visible_matrix':proofs,'four_matrix_tools_per_run':['get_definition','query_data','get_execution','get_analysis_run','get_analysis_steps'],'exact_numeric_value':'9007199254740993.125','citation_states':['valid','unknown','cross_investigation','expired','deleted','forbidden','tool_error','budget_exceeded','empty','not_delivered'],'additional_checks':['原定义移除先前私有设备与依赖、依赖版本改变后，旧依据按原资源集合检查当前权限','直接设备授权、SourceID为无权节点时，正式趋势查询和依据均可用；设备撤权后403','扩展参数和观测值里的device_id/group_id/dependencies等名称按数据内容处理','1000扫描预算、权限过滤跨页和空页、加密续页绑定用户/类别/集合与期限','中文/斜杠/冒号草稿保存、读取、校验、差异、模拟和来源导航','敏感字段、模型密钥、Bearer授权文本在模型消息与持久依据中处理','审计失败时依据事务回滚、CAS冲突及重复ToolCallID拒绝','两个API及流式/非流式完整工具循环；Responses工具调用身份和结束条件','客户端HTTP断开后调查保存cancelled；模型异常保存failed且无done','SQLite与PostgreSQL保存后关闭并重新打开存储，5条实际工具依据保持原文与哈希一致'],'otlp':{'artifact':ref(otlp_path),'spans':len(spans),'span_names':dict(names),'tool_evidence_spans':len(evidence_spans),'trace_ids':sorted(trace_ids),'identity_checks':'实际HTTP、调查、五个工具调用与存储事务沿用同一W3C trace；属性含request_id、actor_id、investigation_id、evidence_id及tool_call_id'},'standalone_fixture':{'artifact':ref(out/'final-fixture-http.json'),'http_requests':fixture['http_requests'],'binary':ref(fixture['binary']),'model_visible_results':fixture['model_visible_initial_results'],'model_transcript':ref(pathlib.Path(fixture['fixture_dir'])/'model-visible-results.jsonl'),'stdout':ref(workspace.parent/'final-http-fixture.stdout.log'),'stderr':ref(workspace.parent/'final-http-fixture.stderr.log'),'shutdown':fixture['shutdown']},'command_records':command_records},
 'contracts':{'independent_json_schemas':len(schemas),'openapi_components':len(schema['components']['schemas']),'operations':sum(len(v) for v in schema['paths'].values()),'openapi':ref(root/'contracts/1.0/openapi.json'),'generated_artifact_count':len(contracts),'generated_artifacts':contracts,'frozen_export_comparison':ref(out/'final-generated-contract-comparison.json'),'frozen_export_compared_json_files':160,'frozen_export_differences':[],'generator':'openapi-typescript 7.13.0 / TypeScript 6.0.2 compiler API / Node24.21.0','generator_sources':[ref(root/'Frontends/scripts/generate-api.mjs'),ref(root/'Frontends/package.json'),ref(root/'Frontends/package-lock.json')],'model_merge':['TemplateInstance device_version/configuration_id/configuration_version 必需，parameters可省略','HandoverEvidence.action_version 可选','NativeAlarmSyncStatus batch_limit/timeout_ms/failed_mappings，详情清空cursor','业务P4-B06 required标签与默认值','AssistantRequest / AssistantEvent及 post_assistant 操作'],'historical_query_report':ref(root/'.local/evolution/query-subscriptions-verified.json'),'historical_business_report':ref(root/'.local/evolution/business-workflows-verified.json'),'historical_reports_preserved':True},
 'frontend_delivery':{'contract':ref(root/'docs/evolution/ai-evidence-frontend-contract.md'),'fixture_binary':ref(workspace.parent/'bin/sf-ai-evidence-fixture'),'start_arguments':['-dir','NEW_PRIVATE_DIRECTORY','-address','127.0.0.1:0','-model-api','responses','-model-stream=true'],'optional_arguments':['-static BUILT_FRONTEND_DIRECTORY','-duration 30m'],'private_credentials':'新目录/private-credentials.json，0600，账号operator-a/operator-b和随机密码、本地模型配置','model_capture':'新目录/model-visible-results.jsonl','state_entry':'POST /__fixture/state；仅夹具operator-a，可revoke/restore/expire/delete_resource','received_by':'/root/phase4_frontend'},
 'first_failures_and_repairs':first_failures,'verification_drivers':[ref(out/p) for p in ['run-verification.py','capture-build-inputs.py','build-final-binaries.py','run-final-tests.py','validate-final-fixture.py']],
 'cleanup':{'own_fixture_application_closed':fixture['shutdown']['application_listener_closed'],'own_fixture_model_closed':fixture['shutdown']['model_listener_closed'],'exit_code':fixture['shutdown']['exit_code'],'postgres_test_schema_cleanup':'每项 testdb.Postgres 注册 DROP SCHEMA CASCADE；测试通过含清理回调检查','pg_race_window_released_to_coordinator':True,'historical_snapshots_and_evidence_preserved':True},
 'protocol_sources':[{'title':'OpenAI function calling','url':'https://developers.openai.com/api/docs/guides/function-calling'},{'title':'OpenAI streaming Responses','url':'https://developers.openai.com/api/docs/guides/streaming-responses'}],
 'runtime_scope':{'models':'本地模拟HTTP端点覆盖两种API及流式/非流式；未调用付费模型','databases':['SQLite','PostgreSQL18.6'],'resource_settings':'Go GOMAXPROCS=2，构建p1，测试parallel1；PG独立schema、应用池在打开后设MaxOpen3/MaxIdle1、管理连接1；大产物在/private/tmp','user_exclusions_preserved':['完整UOS安装恢复','1/3节点连续断云24小时','受限链路专项','实际24小时容量/持续运行'],'global_acceptance_owner':'主协调及后续独立审查'}
}
target=root/'.local/evolution/ai-evidence-verified.json'
target.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'report':str(target),'sha256':ref(target)['sha256'],'bytes':target.stat().st_size,'commands':len(command_records),'ordinary':totals(ordinary),'postgres':totals(pg),'race':totals(race),'protocol_matrix':len(proofs),'http_requests':fixture['http_requests'],'generated_artifacts':len(contracts),'compile_inputs':len(inputs['files']),'binaries':len(binaries),'first_failures':len(first_failures)},ensure_ascii=False))
