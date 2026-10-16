#!/usr/bin/env python3
import collections,datetime,hashlib,json,pathlib
root=pathlib.Path('/path/to/SmartFactory-2026');out=root/'.local/evolution/ai-evidence-a02'
snapshot=json.load(open(out/'final-snapshot.json'));workspace=pathlib.Path(snapshot['workspace']);base=workspace.parent
references={}
def artifact(p):
 p=pathlib.Path(p)
 if not p.is_absolute():p=root/p
 raw=p.read_bytes();item={'path':str(p),'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)}
 references[str(p)]=item
 return item
def load(name):return json.load(open(out/name))
inputs=load('final-build-inputs.json');binaries=load('final-binaries.json');runs=load('final-test-runs.json');http=load('final-fixture-http.json');contracts=load('contract-reproduction.json');changes=load('source-changes.json')
assert len(runs)==8,len(runs)
for run in runs:
 assert run['exit_code']==run['expected_exit_code'],run['label']
 artifact(run['binary']);artifact(run['metadata'])
for item in inputs['files']:
 assert pathlib.Path(item['path']).read_bytes()==pathlib.Path(item['preserved_path']).read_bytes(),item['path']
 assert hashlib.sha256(pathlib.Path(item['preserved_path']).read_bytes()).hexdigest()==item['sha256'],item['path']
for item in snapshot['files']:
 assert hashlib.sha256((workspace/item['path']).read_bytes()).hexdigest()==item['sha256'],item['path']
for item in snapshot['overlay_files']:
 assert hashlib.sha256(pathlib.Path(item['path']).read_bytes()).hexdigest()==item['sha256'],item['path']
commands=[]
for p in sorted(out.glob('*.metadata.json')):
 value=json.load(open(p));entry={k:value.get(k) for k in ['label','command','cwd','started_at','finished_at','elapsed_seconds','exit_code','test_events','top_level','leaf','environment_overrides','sources_changed','compile_inputs_manifest_sha256','compile_inputs_changed_before','compile_inputs_changed_after']}
 entry['metadata']=artifact(p);entry['stdout']=artifact(value['stdout']);entry['stderr']=artifact(value['stderr'])
 if 'compile_inputs_manifest_sha256' in value:
  assert value['compile_inputs_manifest_sha256']==hashlib.sha256((out/'final-build-inputs.json').read_bytes()).hexdigest(),p
  assert value['compile_inputs_changed_before']==[] and value['compile_inputs_changed_after']==[],p
 commands.append(entry)
def sum_runs(selected):
 return {field:{action:sum(v[field][action] for v in selected) for action in ['pass','fail','skip']} for field in ['top_level','leaf']}
ordinary=sum_runs([v for v in runs if v['label'].startswith('a02-fixed-ordinary-')])
postgres=sum_runs([v for v in runs if v['postgres']])
race=sum_runs([v for v in runs if v['race']])
independent=sum_runs([v for v in runs if v['label'] in ['a02-fixed-independent-v2','a02-fixed-independent-v3-null-corrected']])
original=pathlib.Path('/private/tmp/smartfactory-phase4-independent-20261005')
original_failures=[]
for version,stem in [('v2','independent-ai-original-asset-deletion'),('v3','independent-ai-original-asset-deletion-v3')]:
 item={'version':version,'source':artifact(original/'overlays'/('independent-ai-build-'+version)/'independent_ai_review_test.go'),'binary':artifact(original/'bin'/('independent-ai-'+version+'.test')),'run':artifact(root/'.local/evolution/phase4-review-evidence'/(stem+'.metadata.json')),'stdout':artifact(root/'.local/evolution/phase4-review-evidence'/(stem+'.stdout.log')),'stderr':artifact(root/'.local/evolution/phase4-review-evidence'/(stem+'.stderr.log'))}
 original_failures.append(item)
assert original_failures[0]['stdout']['sha256']=='caf33a3d363f8490c7b943131ebc83015528384e439b1b02e6d93ac360e09c0f'
assert original_failures[1]['stdout']['sha256']=='283856d1e0f4d551c4d0c033b111943c9bafe0a2d0964f83df91e1ee0381cac6'
expected_null=next(v for v in runs if v['expected_exit_code'])
null_outputs=[json.loads(line).get('Output','') for line in (out/(expected_null['label']+'.jsonl')).read_text().splitlines()]
assert any('after_deletion_status=404 content_bytes=0' in v for v in null_outputs)
assert any('answer_bytes=0 messages=0 evidence_arguments_bytes=4 evidence_resources=0 reference_status=deleted' in v for v in null_outputs)
assert not any('deleted original asset evidence remains accessible' in v for v in null_outputs)
assert http['shutdown']=={'exit_code':0,'application_listener_closed':True,'model_listener_closed':True}
assert http['http_requests']==51 and http['original_asset_deletion']['deleted_http']==404
assert len(contracts['files'])==160 and contracts['byte_equal_to_snapshot_and_current']
for item in changes:
 artifact(item['after']['path'])
 if item['before']:artifact(item['before']['path'])
for item in binaries:artifact(item['path']);artifact(item['metadata'])
for item in snapshot['independent_test_overlays']:artifact(item['source']);artifact(item['path'])
for p in sorted(out.rglob('*')):
 if p.is_file():artifact(p)
for p in [root/'.local/evolution/phase4-ai-a02-followup.txt',root/'.local/evolution/phase4-independent-review-checkpoint.json',root/'.local/evolution/phase4-ai-a02-build-provenance-root-review.json',root/'.local/evolution/ai-evidence-a02-frontend.txt']:
 artifact(p)
for p in base.glob('*.json'):artifact(p)
for p in base.glob('*.log'):artifact(p)
for p in [pathlib.Path(http['fixture_dir'])/'manifest.json',pathlib.Path(http['fixture_dir'])/'model-visible-results.jsonl']:
 artifact(p)
old_report=artifact(root/'.local/evolution/ai-evidence-verified.json')
assert old_report['sha256']=='bd3034224715e6c368e56ad873944058fd4576114553648fc9c46436f62dbb44'
old_snapshot=artifact(root/'.local/evolution/ai-evidence/final-snapshot.json')
old_inputs=artifact(root/'.local/evolution/ai-evidence/final-build-inputs.json')
report={
 'schema_version':'1.0','role':'phase4_ai_evidence','criterion':'E4-07','finding':'P4-A02','status':'verified','verified_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
 'result':'具体 entity/resource 的原始实体删除后，依据详情为 HTTP 404；调查详情保持 HTTP 200，引用和依据状态为 deleted，答案、消息、参数、原文和资源引用按现有契约隐藏。',
 'implementation':{'production_changes':'Investigations.checkResource 先检查具体 entity/resource 的实体存在性，再执行当前授权；resource="*" 按目录权限范围处理。隔离 fixture 增加原始定义组准备、定义改组和删除原资产三个场景。','production_files':2,'regression_test_files':2,'source_changes':artifact(out/'source-changes.json'),'source_diff':artifact(out/'source-changes.patch'),'contract_behavior':'arguments 清空继续输出 JSON null，现有类型、路由、迁移、模块和生成契约均沿用既有实现。','public_contract_reproduction':artifact(out/'contract-reproduction.json')},
 'original_reproductions':{'failures_preserved':original_failures,'original_report_preserved':old_report,'original_snapshot_preserved':old_snapshot,'original_compile_inputs_preserved':old_inputs,'independent_checkpoint':artifact(root/'.local/evolution/phase4-independent-review-checkpoint.json'),'fixed_unmodified_v2':'通过，原资产删除后 HTTP 404，原文为零字节。','fixed_unmodified_v3':'产品访问和隐藏状态符合要求；原版断言将 arguments=null 解码成四字节 RawMessage 后要求长度为零，该单一断言继续失败并保留运行日志。','fixed_v3_null_corrected':'仅改为接受 nil 或 JSON null 后，依据详情和调查详情断言全部通过。'},
 'test_assertion_correction':{'purpose':'按现有 arguments=null 接口含义判断无参数内容，解决测试解码后的误判；产品修正解决原资源已删除但授权条目仍存在时继续返回原文的问题。','minimal_patch':artifact(out/'independent-v3-null-assertion.patch'),'first_new_assertion_failure':artifact(out/'a02-initial-targeted.metadata.json'),'first_test_source':artifact(out/'first-test-null-assertion/investigation_resource_deletion_test.go'),'corrected_targeted_run':artifact(out/'a02-targeted-null-semantics.metadata.json'),'expected_original_v3_failure_run':expected_null,'overlay_provenance':snapshot['independent_test_overlays']},
 'resource_mapping':[{'kind':k,'storage_kind':('entity' if k in ['entity','resource'] else k),'current_access':a} for k,a in [('definition','ReadDefinition 当前正式定义授权'),('draft','Definitions.LoadDraft read'),('execution','Control.Detail'),('analysis_run','History.Get'),('alarm','读取正式 alarm 后校验 EntityID'),('catalogue','读取正式 catalogue 后校验文档关联定义或组'),('entity','读取正式 entity 后校验该身份权限'),('resource','具体身份读取正式 entity 后校验权限；"*" 校验目录权限范围')]],
 'verification':{'ordinary':ordinary,'postgres':postgres,'limited_race':race,'independent_accepted_reproductions':independent,'independent_original_v3_null_assertion_failure':{'top_level':1,'leaf':1,'product_content_hidden':True},'test_runs':runs,'actual_http':{'artifact':artifact(out/'final-fixture-http.json'),'requests':http['http_requests'],'original_asset_deletion':http['original_asset_deletion'],'initial_five_tools':http['model_visible_initial_results'],'application_command':[http['binary'],'-dir',http['fixture_dir'],'-address','127.0.0.1:0','-model-api','responses','-model-stream=true','-duration','2m'],'command_source':artifact(out/'validate-final-fixture.py'),'stdout':artifact(base/'final-http-fixture.stdout.log'),'stderr':artifact(base/'final-http-fixture.stderr.log'),'startup_manifest':artifact(pathlib.Path(http['fixture_dir'])/'manifest.json')},'scenarios':'八类原始资源分别验证存在、撤权、恢复、删除；正式模型工具循环验证原定义改组后的原始资源保留、原始依赖从最新定义移除后删除、原有调查生命周期、仅设备授权的 SourceID、扩展参数同名键、四种模型协议/流式组合。资源种类矩阵直接调用正式调查应用保存独立资源依据；定义改组和依赖删除使用实际模型/工具 HTTP 循环。','contract_files_byte_equal':160},
 'provenance':{'snapshot':artifact(out/'final-snapshot.json'),'workspace':str(workspace),'copied_files':len(snapshot['files']),'copied_production_go_files':sum(v['path'].endswith('.go') and not v['path'].endswith('_test.go') for v in snapshot['files']),'copied_test_go_files':sum(v['path'].endswith('_test.go') for v in snapshot['files']),'directory_metadata_files':snapshot['directory_metadata_files'],'independent_overlay_files':len(snapshot['overlay_files']),'copy_changes':snapshot['copy_changes'],'retained_independent_test_improvements':snapshot['retained_review_test_improvements'],'actual_compile_inputs':artifact(out/'final-build-inputs.json'),'input_count':len(inputs['files']),'active_project_input_files':sum(v['source_class']=='frozen_workspace' for v in inputs['files']),'input_classification':load('input-classification.json'),'external_inputs_preserved':True,'all_inputs_currently_match':True,'go_version':inputs['go_version'],'go_binary':inputs['go_binary'],'go_binary_sha256':inputs['go_binary_sha256'],'binaries_manifest':artifact(out/'final-binaries.json'),'binaries':binaries,'build_review':artifact(root/'.local/evolution/phase4-ai-a02-build-provenance-root-review.json'),'commands':commands},
 'resources_and_cleanup':{'Go_builds':'GOMAXPROCS=2，-p 1；测试 -parallel 1，逐包运行；有限 race 范围为 ^TestInvestigation。','PostgreSQL':'PG18 私有 DSN，testdb 各自创建/删除隔离 schema；管理连接 1，Store.Open 完成后应用 MaxOpen=3/MaxIdle=1；继承 Store.Open 内部初始化默认值，未改公共存储配置。','SQLite':'各测试与独立 HTTP 使用新隔离数据库，MaxOpen=1/MaxIdle=1。','fixture_shutdown':http['shutdown'],'test_cleanup':'适用 PG 与 race 均通过，测试注册的 schema/连接和 httptest 服务清理未报错。','private_runtime_credentials':'只在隔离 fixture 的 private-credentials.json 和 master.key 保存，报告不引用凭据文件内容。'},
 'frontend_handoff':{'instructions':artifact(root/'.local/evolution/ai-evidence-a02-frontend.txt'),'binary':artifact(base/'bin/sf-ai-evidence-fixture'),'fixed_source':str(workspace),'contract_changes':False,'handoff_sent_to':'phase4_frontend and root'},
 'scope_notes':'601 项复制文件含 3 个 .DS_Store；9 项独立测试覆盖文件另行登记。实际编译输入从对应普通、程序和 race 的 go list 获取，只有 222 项项目输入参与本轮目标编译。既有报告和 0952 副本保持原摘要，首次失败与测试 null 断言修正分别列明。',
 'artifact_references':list(references.values())
}
report_path=root/'.local/evolution/ai-evidence-a02-verified.json';report_path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
for v in report['artifact_references']:
 assert pathlib.Path(v['path']).is_file() and hashlib.sha256(pathlib.Path(v['path']).read_bytes()).hexdigest()==v['sha256'],v['path']
print(json.dumps({'report':str(report_path),'sha256':hashlib.sha256(report_path.read_bytes()).hexdigest(),'bytes':report_path.stat().st_size,'references':len(references),'ordinary':ordinary,'postgres':postgres,'race':race,'independent':independent},ensure_ascii=False))
