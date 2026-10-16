from pathlib import Path
import hashlib,json,os,subprocess,datetime,urllib.request
ROOT=Path('/path/to/SmartFactory-2026')
E=ROOT/'.local/evolution/control-evidence'
FIX=Path('/private/tmp/sf-control-browser-final-v2-20261005')
INTEGRATION=Path('/private/tmp/sf-control-integration-final-20261005')
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def artifact(p):
 p=Path(p); p=p if p.is_absolute() else ROOT/p
 try: label=str(p.relative_to(ROOT))
 except ValueError: label=str(p)
 return {'path':label,'sha256':sha(p),'bytes':p.stat().st_size}
def result(name,cmd,exit_code=0,cwd='Platform',stage='final'):
 p=E/name; rows=[]
 for line in p.read_text().splitlines():
  try: rows.append(json.loads(line))
  except ValueError: pass
 return {'log':artifact(p),'command':cmd,'cwd':cwd,'exit_code':exit_code,'stage':stage,'passed_tests_and_subtests':[r['Test'] for r in rows if r.get('Action')=='pass' and r.get('Test')], 'package_results':[{'package':r.get('Package'), 'action':r['Action'],'elapsed_seconds':r.get('Elapsed')} for r in rows if r.get('Action') in ('pass','fail','skip') and not r.get('Test')], 'skipped_tests':[r.get('Test') for r in rows if r.get('Action')=='skip' and r.get('Test')], 'failed_tests':[r.get('Test',r.get('Package')) for r in rows if r.get('Action')=='fail']}
commands=[
 result('platform-final-ordinary.jsonl','go test -json -p 1 ./... -count=1',1),
 result('control-final-race.jsonl','go test -json -race -p 1 ./internal/control ./internal/cloudsync ./internal/coordination ./internal/datatransfer -count=1'),
 result('datatransfer-final-ordinary.jsonl','go test -json -p 1 ./... -count=1',cwd='DataTransfer'),
 result('contracts-final.jsonl','go test -json -p 1 ./pkg/compatibility ./pkg/contracts ./cmd/sf-contracts -count=1'),
 result('postgres-control-final.jsonl','go test -json -p 1 ./internal/control -run ^TestPostgresControlAtomicTransitionsAndConcurrentIntervention$ -count=1'),
 result('nats-final.jsonl','go test -json -p 1 ./internal/coordination -run TestRealThreeReplicaReadOnlyReconcileAndManualResume|TestRealConcurrentOperationDeliveryKeepsLeaseContentionRetryable|TestPersistentThreeReplicaUpgrade -count=1'),
 result('dual-app-final-binary.jsonl',json.loads((E/'dual-app-final-command.json').read_text())['command']),
 result('operation-deadline-concurrent-first.jsonl','go test -json -p 1 ./internal/control -run TestOperation|TestStartedOperation -count=1',stage='earlier_supported_by_final_race'),
 result('operation-started-receipt-first.jsonl','go test -json -p 1 ./internal/control -run TestOperation|TestStartedOperation|TestLocalOperation|TestCloudPending -count=1',stage='earlier_supported_by_final_race'),
 result('state-legacy-first.jsonl','go test -json -p 1 ./internal/control -run TestStateEvents|TestProcessRecovery|TestMigrationFour -count=1',stage='earlier_supported_by_final_race'),
 result('authorization-first.jsonl','go test -json -p 1 ./internal/control -run TestCurrentAuthorization|TestInterventionRequires -count=1',stage='earlier_supported_by_final_race'),
]
source_files=set()
for project in ['Platform','DataTransfer']:
 for path in (ROOT/project).rglob('*'):
  if any(part in ('.git','.cache','.tools','node_modules','vendor') for part in path.parts): continue
  if path.is_file() and (path.suffix in ('.go','.proto') or path.name in ('go.mod','go.sum') or path==ROOT/'Platform/pkg/compatibility/manifest.json'):
   source_files.add(path)
for path in (ROOT/'contracts').rglob('*'):
 if path.is_file(): source_files.add(path)
source_files.update([ROOT/'docs/evolution/control-frontend-contract.md', ROOT/'scripts/control-intervention-fixture.py'])
source_manifest=[artifact(p) for p in sorted(source_files)]
(E/'final-source-manifest.json').write_text(json.dumps(source_manifest,indent=2)+'\n')
manifest=json.loads((FIX/'manifest.json').read_text()); runtime=manifest['runtime']['startup']
health={}
for key in ['cloud_url','edge_url']:
 with urllib.request.urlopen(runtime[key]+'/health', timeout=3) as response: health[key]=response.status
with urllib.request.urlopen(runtime['driver_url']+'/state',timeout=3) as response: driver_state=json.load(response)
old_processes={}
for path in ['/private/tmp/sf-control-browser-20261005','/private/tmp/sf-control-browser-final-20261005']:
 pid=json.loads((Path(path)/'process.json').read_text())['pid']
 log=(Path(path)/'applications.log').read_text()
 stopped=log.rstrip().endswith('PASS')
 old_processes[path]={'pid':pid,'running':not stopped,'shutdown_evidence':'fixture log completed with PASS after application and DataTransfer cleanup','retained_logs':str(Path(path)/'applications.log')}
verification=json.loads((INTEGRATION/'verified.json').read_text())
failures=[
 {'log':'first-integrated.jsonl','problem':'Automatic-worker budget fixture omitted the published-policy service identity, and was rejected before budget/send.','repair':'Fixture now uses the same automatic mode and service identity as the production worker; final all-module API package passed.'},
 {'log':'nats-manual-first.jsonl','problem':'New NATS test had an unused import.','repair':'Removed unused import; final actual NATS three-node tests passed.'},
 {'log':'dual-app-first.jsonl','problem':'New dual-application fixture had two unused imports.','repair':'Removed unused imports; final built fixture passed actual integration.'},
 {'log':'nats-legacy.jsonl','problem':'Restored cluster had no metadata leader before the first connection deadline.','repair':'Retried after the restored cluster elected a metadata leader; preserved signed checkpoint digest verified again in final NATS run.'},
 {'log':'platform-final-ordinary.jsonl','problem':'Two old tests hard-coded migration four after current migration minimum/maximum moved to six.','repair':'Updated migration-count and compatibility-range assertions against the declared migration manifest; full store package and targeted SQLite/PostgreSQL migration tests passed in history evidence, compatibility/contracts passed in this report.'},
]
for item in failures: item['log']=artifact(E/item['log'])
criteria={
 'E3-01':{'status':'verified_backend','behavior':'Single transition path is used by manual requests, automatic/scheduled workers, recovered journals, remote steps and cloud receipts. Immutable request identity and local CAS are checked, and execution/document revision, audit, transition, optional receipt and operation outcome commit atomically.','tests':['TestStateEventsEnforcePersistenceAndImmutableRequest','TestProcessRecoveryFaultPreservesReservedCommand','TestCrashWindowsRecoverWithoutRepeatingReservedCommands','TestPostgresControlAtomicTransitionsAndConcurrentIntervention','TestRealThreeReplicaReadOnlyReconcileAndManualResume'],'fault_points':['before_reserve','after_reserve','after_send','before_result_save','after_result_save','process_recovery','after_operation_persist','after_operation_started']},
 'E3-02':{'status':'verified_backend','behavior':'Current principals, sessions, grants, team membership, original and current referenced resources, normal/degraded devices, published definitions and organization eligibility are validated; their revisions are checked again in the mutation transaction. Automatic/scheduled control requires published-policy identity. Historical read APIs check original resources.','tests':['TestCurrentAuthorizationAndResourceVersionsBeforeExecution','TestInterventionRequiresHistoricalConditionResources','TestOrganizationMoveInvalidatesLeaderApproval','TestExpiredAndRevokedApprovalsAndInterlock','TestInterlockIsRecheckedBetweenDeviceSteps','TestControlHTTPReadsHistoricalResourcesAndRechecksPrincipal','TestHistoricalExecutionAndOperationReadChecksOriginalResources']},
 'E3-03':{'status':'verified_backend','behavior':'Reconciliation queries original command journal over actual DataTransfer gRPC or signed NATS owner request, validates provenance and immutable binding, and persists trusted/rejected evidence. Manual cancellation/reconciliation/resume use expected versions; unknown actions never resend automatically. StartedMS is durably claimed before lookup and preserved across duplicate delivery/restart. Cloud acceptance is HTTP202 and keeps execution state until edge receipts.','tests':['TestChineseColonIdentityReconcileAndResumeRemainingSteps','TestUntrustedFeedbackCannotAuthorizeResume','TestCancelDuringCommandRetainsLateResultAndSkipsRemaining','TestConcurrentManualOperationsHaveSingleVersionWinner','TestOperationStartSurvivesDuplicateAfterDeadline','TestStartedOperationResumesAfterDatabaseReopenBeyondDeadline','TestOperationDeadlineUsesStartTimeAndCloudAcceptsLateCompletion','TestLocalOperationSurvivesCommitBeforeProcessingAndSameIDRetry','TestRealConcurrentOperationDeliveryKeepsLeaseContentionRetryable','TestControlledCloudEdgeControlIntervention']},
 'E3-07-control':{'status':'verified_backend_frontend_handoff_ready','behavior':'Execution detail exposes allowed actions, original-source timeline, approvals, atomic transitions, actions, feedback provenance, operation request/start/result times and trace IDs. Actual OTLP span export joins persisted transitions and command evidence.','tests':['TestMigrationFourExecutionAndActionJournalRemainRecoverable','TestControlledCloudEdgeControlIntervention','TestControlHTTPCloudPendingAndVersionContract'],'frontend_owner':'phase3 frontend; browser implementation and independent acceptance are outside this role'}
}
report={
 'schema_version':1,'phase':3,'role':'control_execution','status':'verified_backend_frontend_handoff_ready','generated_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
 'acceptance':criteria,
 'implementation':{'migration':5,'current_migration':6,'storage_interface':'internal/control.Repository used by production Service','typed_contracts':['ExecutionAction','ExecutionDetail','ExecutionTransition','CommandEvidence','ControlOperation'],'protocol_addition':'datatransfer.v1.DataTransferService/GetCommandResult read-only journal lookup; original message field numbers and RPC signatures preserved','frontend_contract':artifact('docs/evolution/control-frontend-contract.md')},
 'execution_environment':{'go':'go1.27.1','ordinary_and_race':'GOMAXPROCS=2; go test -p 1; GOTOOLCHAIN=local; GOPROXY=off','postgres':'PostgreSQL18 isolated schema, each pool max3 idle1; management connection1, returned to coordinator after test','nats':{'version':'2.14.7','replicas':3,'image':'library/nats@sha256:b2301863d84fda2cf8260d308203708f5231b2db34d821cc518d5c4c3c79780a','memory_limit_per_container':268435456,'transport':'actual mTLS NATS','state':'containers and fixture network stopped','legacy_checkpoint_source_versions':['2.11.9','2.12.15','2.14.7'],'legacy_checkpoint_digest':'60855a0350da99521058590e3ea7fc93a716325d8b8ec62ee7518a5144b6bab9'}},
 'commands':commands,
 'generation_evidence':[artifact(E/'contracts-generation.txt'),artifact(E/'types-generation.txt'),artifact(E/'protocol-migrations-final.json')],
 'full_module_result':{'Platform':'363 tests/subtests passed; the first all-module command exited1 for two obsolete migration-count assertions. Both repaired packages were reverified; initial failure remains in evidence.','DataTransfer':'101 tests/subtests passed, no failures. Environment-gated cases are enumerated per command; actual control-device fixture was separately run in final dual-application test.','repairs_verified_by':[artifact('.local/evolution/history-evidence/migration-compatibility-repair.jsonl'),artifact('.local/evolution/history-evidence/store-regression-repair.jsonl'),artifact(E/'contracts-final.jsonl')]},
 'actual_dual_application':{'command':artifact(E/'dual-app-final-command.json'),'results':artifact(INTEGRATION/'verified.json'),'traces':artifact(INTEGRATION/'control-traces.json'),'uses_same_binaries_as_browser_fixture':True,'http_pending':verification['cloud_http_pending_status'],'same_operation_conflict_http':verification['same_operation_content_conflict_http'],'receipt_order_actually_reversed':verification['actual_out_of_order_receipts'],'receipt_response_lost_and_retried':verification['receipt_response_lost_and_retried'],'physical_actions':verification['physical_actions'],'rejected_operations':{k:{'status':v['status'],'error':v.get('error'),'started_ms':v.get('started_ms',0)} for k,v in verification['rejected_operations'].items()},'otlp_control_spans':verification['otlp_control_span_count'],'every_persisted_recovery_transition_and_feedback_has_exported_trace':True},
 'legacy_compatibility':{'migration4_fixture':'Reconstructed actual SQLite schema with old execution fields, original approval/action records and audit; dropped new transition/evidence tables then reopened using current migrations. Missing action times stayed0, no historic transitions were fabricated; explicit reconciliation/resume sent only remaining step.','signed_NATS_fixture':artifact(E/'legacy-nats-source.json'),'signed_NATS_result':artifact(E/'legacy-nats-verified.json'),'old_migration_and_protobuf_checks':artifact(E/'protocol-migrations-final.json')},
 'source':{'final_test_and_source_manifest':artifact(E/'final-source-manifest.json'),'actual_binary_build_manifest':artifact(FIX/'manifest.json'),'compiled_inputs':artifact(FIX/'build-inputs.json'),'compiled_input_count':manifest['build_inputs']['count'],'compiled_input_scope':'Go list -deps -test selected package variants; source, assembly/C, embedded files, module go.mod and root module go.sum; test main is generated by recorded Go toolchain','binaries':manifest['binaries'],'coordinator_independent_rehash':artifact('.local/evolution/control-final-fixture-root-review.json')},
 'first_failures_and_repairs':failures,
 'historical_log_policy':'Earlier logs retain their original builds. Final ordinary/race, PG/NATS, final binary integration and fixture manifest identify the final production source; earlier focused evidence is retained for first failures and fault details.',
 'not_run':[
 {'scope':'24-hour offline recovery and long-duration soak','reason':'Outside this phase bounded verification agreement'},
 {'scope':'UOS host validation and constrained-link long-duration acceptance','reason':'Retained previously agreed dedicated acceptance scope'},
 {'scope':'Frontend implementation/browser acceptance','reason':'Assigned to frontend role; actual services and immutable binary manifest provided here'}],
 'fixture':{'directory':str(FIX),'manifest':artifact(FIX/'manifest.json'),'cloud_url':runtime['cloud_url'],'edge_url':runtime['edge_url'],'driver_url':runtime['driver_url'],'private_access_file':runtime['access_file'],'credentials_in_report':False,'initial_cases':{'recover-browser':'result_unknown; reconcile then resume remaining step','cancel-browser':'approved; dispatch then cancel while first feedback waits'},'health_http':health,'initial_driver_state':driver_state,'pid':manifest['runtime']['pid'],'running_for_frontend':True,'maximum_test_lifetime':'2h from startup; restart with the fixture script when needed','stop_command':'python3 scripts/control-intervention-fixture.py stop --output '+str(FIX),'original_fixtures':old_processes},
 'working_tree_validation':{'git_diff_check_exit_code':subprocess.run(['git','diff','--check'],cwd=ROOT,capture_output=True).returncode},
}
assert report['working_tree_validation']['git_diff_check_exit_code']==0
assert not any(v['running'] for v in old_processes.values())
(ROOT/'.local/evolution/control-execution-verified.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'report':'.local/evolution/control-execution-verified.json','sha256':sha(ROOT/'.local/evolution/control-execution-verified.json'),'source_files':len(source_manifest),'compiled_inputs':manifest['build_inputs']['count'],'fixture_health':health,'actual_control_spans':verification['otlp_control_span_count'],'status':report['status']},ensure_ascii=False))
