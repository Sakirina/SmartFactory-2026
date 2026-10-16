from pathlib import Path
import hashlib,json,datetime,urllib.request
ROOT=Path('/path/to/SmartFactory-2026')
E=ROOT/'.local/evolution/control-evidence/c05'
BEFORE=ROOT/'.local/evolution/control-evidence/pre-c05'
OLD=Path('/private/tmp/sf-control-browser-final-v2-20261005')
FIX=Path('/private/tmp/sf-control-c05-final-20261005')
RUN=Path('/private/tmp/sf-control-c05-final-integration-20261005')
def sha(p): return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def artifact(p):
 p=Path(p); p=p if p.is_absolute() else ROOT/p
 try: name=str(p.relative_to(ROOT))
 except ValueError: name=str(p)
 return {'path':name,'sha256':sha(p),'bytes':p.stat().st_size}
def read(p): return json.loads(Path(p).read_text())
def result(name,command,exit_code=0,cwd='Platform',stage='final'):
 p=E/name; rows=[]
 for line in p.read_text().splitlines():
  try: rows.append(json.loads(line))
  except ValueError: pass
 return {'log':artifact(p),'command':command,'cwd':cwd,'exit_code':exit_code,'stage':stage,
  'passed_tests_and_subtests':[r['Test'] for r in rows if r.get('Action')=='pass' and r.get('Test')],
  'failed_tests':[r.get('Test',r.get('Package')) for r in rows if r.get('Action')=='fail'],
  'skipped_tests':[r['Test'] for r in rows if r.get('Action')=='skip' and r.get('Test')],
  'package_results':[{'package':r.get('Package'),'action':r['Action'],'elapsed_seconds':r.get('Elapsed')} for r in rows if r.get('Action') in ('pass','fail','skip') and not r.get('Test')]}
manifest=read(FIX/'manifest.json')
inputs=read(FIX/'build-inputs.json')
old_inputs={r['path']:r for r in read(OLD/'build-inputs.json')}
changed_after_build=[r['path'] for r in inputs if sha(r['path'])!=r['sha256']]
assert not changed_after_build,changed_after_build
changed_compile_inputs=[r for r in inputs if r['path'] not in old_inputs or old_inputs[r['path']]['sha256']!=r['sha256']]
compiled_migrations=[{'path':r['path'],'sha256':r['sha256'],'same_as_pre_c05':old_inputs.get(r['path'],{}).get('sha256')==r['sha256']} for r in inputs if '/Platform/internal/store/migration' in r['path']]
assert all(r['same_as_pre_c05'] for r in compiled_migrations)
old_sources=read(BEFORE/'final-source-manifest.json')
contracts=[r for r in old_sources if r['path'].startswith('contracts/')]
assert all(sha(ROOT/r['path'])==r['sha256'] for r in contracts)
source_names=[
 'Platform/pkg/model/control.go','Platform/internal/store/control.go','Platform/internal/store/control_evidence_test.go',
 'Platform/internal/control/run.go','Platform/internal/control/state.go','Platform/internal/control/evidence.go',
 'Platform/internal/control/intervention_test.go','Platform/internal/control/state_test.go',
 'Platform/internal/cloudsync/exchange.go','Platform/internal/cloudsync/control_receipts.go','Platform/internal/cloudsync/control_receipts_test.go',
 'Platform/internal/api/control_evidence_test.go','Platform/internal/app/control_fixture_test.go','docs/evolution/control-frontend-contract.md']
source_manifest=[artifact(p) for p in source_names]
(E/'source-manifest.json').write_text(json.dumps(source_manifest,indent=2)+'\n')
old_bin=read(OLD/'manifest.json')['binaries']
binary_comparison={name:{'before':old_bin[name],'after':value,'changed':old_bin[name]['sha256']!=value['sha256']} for name,value in manifest['binaries'].items()}
verification=read(RUN/'verified.json')
commands=[
 result('affected-final.jsonl','go test -json -p 1 ./internal/api ./internal/control ./internal/cloudsync ./internal/store ./internal/coordination -count=1'),
 result('contract-state-final.jsonl','go test -json -p 1 ./internal/control ./pkg/contracts ./pkg/compatibility ./cmd/sf-contracts -count=1'),
 result('postgres-final.jsonl','GOMAXPROCS=2 SF_TEST_POSTGRES_DATABASE=<private DSN from pg18-test.json> go test -json -p 1 ./internal/store ./internal/cloudsync -run ^TestPostgresControl(EvidenceAtomicReceiptAndImmutableIdentity|ReceiptEvidenceOrderingAndLegacyCompatibility)$ -count=1'),
 result('race-final.jsonl','GOMAXPROCS=2 go test -json -race -p 1 ./internal/control ./internal/cloudsync ./internal/coordination ./internal/store -run TestControl|TestReceipt|TestCancel|TestCloudPending|TestStartedOperation|TestLocalOperation|TestStateEvents|TestCheckpoint|TestExecutionReceipt|TestReadOnly -count=1'),
 result('race-coordination-final.jsonl','GOMAXPROCS=2 go test -json -race -p 1 ./internal/coordination -run ^TestFenceCommitContentionDoesNotAuthorizeDegradedActions$ -count=1'),
 result('dual-app-final-binary.jsonl','SF_CONTROL_DEVICE_BINARY='+str(FIX/'device-control.test')+' SF_CONTROL_INTEGRATION_OUTPUT='+str(RUN)+' go tool test2json -t -p competition2026/product/platform/internal/app '+str(FIX/'platform-control.test')+' -test.run=^TestControlledCloudEdgeControlIntervention$ -test.v -test.timeout=90s'),
]
assert all(not row['failed_tests'] for row in commands)
initial=result('dual-app-before-fix.jsonl','GOMAXPROCS=2 SF_CONTROL_DEVICE_BINARY='+str(OLD/'device-control.test')+' SF_CONTROL_INTEGRATION_OUTPUT=/private/tmp/sf-control-c05-reproduce-20261005 go test -json -p 1 ./internal/app -run ^TestControlledCloudEdgeControlIntervention$ -count=1',exit_code=1,stage='before_production_fix')
assert initial['failed_tests']
health={}
for key in ['cloud_url','edge_url']:
 with urllib.request.urlopen(manifest['runtime']['startup'][key]+'/health',timeout=3) as response: health[key]=response.status
old_report=ROOT/'.local/evolution/control-execution-verified.json'
assert sha(old_report)==sha(BEFORE/'control-execution-verified.json')
report={
 'schema_version':1,'phase':3,'role':'control_execution','finding':'P3-C05',
 'status':'verified_backend_fix_browser_fixture_available_for_independent_review',
 'generated_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
 'scope':'Synchronize durable original-command evidence from edge execution receipts into cloud detail, including late original results after cancellation',
 'baseline':{'original_report':artifact(old_report),'preserved_report':artifact(BEFORE/'control-execution-verified.json'),'original_source_manifest':artifact(BEFORE/'final-source-manifest.json'),'original_binary_manifest':artifact(OLD/'manifest.json'),'independent_before_fix_evidence':[artifact('.local/evolution/phase3-review-evidence/control-resume-backend.json'),artifact('.local/evolution/phase3-review-evidence/control-trusted-evidence.json')],'first_reproduced_failure':initial},
 'behavior':{
  'wire_contract':'Execution root JSON fields are retained. Optional receipt_source has schema_version=1, authenticated node_id, source execution version and recorded_ms. command_evidence carries all immutable evidence for that execution on each newly committed receipt.',
  'validation':'Sender remains admitted by mTLS and current edge ownership. Receipt source node/version must match authenticated delivery and execution identity. Evidence must bind original published definition version, execution, command, step, device, bound payload hash, collection time and trusted source/result provenance. Referenced trusted evidence must match the step result.',
  'atomicity':'Evidence, execution local version, transition, durable outbox/receipt and signed audit commit or roll back together. Cloud receipt audit preserves original evidence and authenticated source node/version/time; original evidence TraceID is retained.',
  'replay_ordering':'Same delivery identity with altered content conflicts. Same evidence ID with altered content conflicts across deliveries and source versions. Older source versions and fences may add immutable feedback without regressing state or source cursor. Legacy plain Execution receipts remain accepted. Confirmed evidence identities cannot be erased or replaced by receipt/checkpoint progression.',
  'late_cancel_result':'Late confirmed original command responses are captured once as trusted control.action_journal:<node> evidence with request/result hashes, platform journal time, original command identity and available TraceID; cancellation still prevents remaining commands.',
  'read_authorization':'Current principal, original published resources, current resource ancestry and every evidence device remain required for execution detail and operation results. Cloud HTTP evidence and feedback timeline use existing public types.',
  'public_api':'No public HTTP field or generated client schema changed; all '+str(len(contracts))+' pre-C05 contract files remain byte-for-byte identical.'},
 'verification':{
  'commands':commands,
  'database':{'engine':'existing PostgreSQL 18 service','isolation':'two sequential unique schemas, cleaned by test helper','application_pool_max_open':3,'application_pool_max_idle':1,'management_pool_max_open':1,'migration_inputs_unchanged':compiled_migrations},
  'actual_dual_application':{'verification':artifact(RUN/'verified.json'),'http_details':artifact(RUN/'cloud-edge-evidence-details.json'),'traces':artifact(RUN/'control-traces.json'),'physical_actions':verification['physical_actions'],'cloud_edge_http_feedback_evidence_equal':verification['cloud_edge_http_feedback_evidence_equal'],'actual_out_of_order_receipts':verification['actual_out_of_order_receipts'],'receipt_response_lost_and_retried':verification['receipt_response_lost_and_retried'],'original_command_evidence':verification['command_evidence'],'late_cancel_evidence':verification['cancel_late_command_evidence'],'otlp_control_span_count':verification['otlp_control_span_count'],'data_directory':str(RUN),'execution':'actual cloud and edge Application.Run with separate SQLite files, actual mTLS exchange, separate DataTransfer process, generated gRPC and durable command journal, counted Modbus connector'},
  'implementation_attempts':[result('dual-app-first-fix.jsonl','SF_CONTROL_DATATRANSFER_BINARY=<old device binary; wrong variable name> go test -json -p 1 ./internal/app -run ^TestControlledCloudEdgeControlIntervention$ -count=1',stage='setup_skipped_not_counted_as_verification'),result('dual-app-second-fix.jsonl','SF_CONTROL_DEVICE_BINARY='+str(OLD/'device-control.test')+' SF_CONTROL_INTEGRATION_OUTPUT=/private/tmp/sf-control-c05-first-20261005 go test -json -p 1 ./internal/app -run ^TestControlledCloudEdgeControlIntervention$ -count=1',stage='intermediate_production_fix'),result('dual-app-details-first.jsonl','SF_CONTROL_DEVICE_BINARY='+str(OLD/'device-control.test')+' SF_CONTROL_INTEGRATION_OUTPUT=/private/tmp/sf-control-c05-details-20261005 go test -json -p 1 ./internal/app -run ^TestControlledCloudEdgeControlIntervention$ -count=1',stage='intermediate_http_details_verification')],
  'not_repeated':[{'scope':'actual three-replica NATS cluster and persistent upgrade rehearsal','reason':'NATS transport/journal implementation and command transport payload are unchanged in C05; affected checkpoint evidence progression was tested directly, and fence-contention case passed ordinary and race tests. Prior actual NATS results remain in preserved original report.'},{'scope':'full DataTransfer suite','reason':'DataTransfer and protocol inputs are unchanged in C05; final actual application test exercises the same newly built DataTransfer gRPC command/result journal path. Original full suite remains preserved.'},{'scope':'24-hour limited-network and UOS environments','reason':'original phase exclusions continue; no new environment result claimed'},{'scope':'frontend implementation and independent browser acceptance','reason':'owned by frontend and independent review roles; fresh fixture provided'}]},
 'artifacts':{'changed_source_manifest':artifact(E/'source-manifest.json'),'compiled_input_manifest':artifact(FIX/'build-inputs.json'),'compiled_input_count':len(inputs),'compiled_inputs_checked_after_final_binary_test':True,'changed_compile_inputs_from_original_fixture':changed_compile_inputs,'binary_manifest':artifact(FIX/'manifest.json'),'binary_comparison':binary_comparison,'frontend_contract':artifact('docs/evolution/control-frontend-contract.md')},
 'browser_handoff':{'directory':str(FIX),'startup':manifest['runtime']['startup'],'health':health,'serves_current_frontend':str(ROOT/'Frontends/dist'),'status':'running and reserved for frontend/independent review; stop coordinated by root','startup_command':'env GOTOOLCHAIN=local GOPATH=/private/tmp/smartfactory-evolution-gopath GOMODCACHE=/private/tmp/smartfactory-evolution-gomodcache GOCACHE=/private/tmp/smartfactory-evolution-go-cache GOPROXY=off python3 scripts/control-intervention-fixture.py start --output <new-empty-directory>','existing_services_preserved':['http://127.0.0.1:59107','http://127.0.0.1:59108','http://127.0.0.1:59114','http://127.0.0.1:19060']},
 'resource_status':{'postgres_connections_closed':True,'owned_test_nats_containers_started_for_c05':False,'large_artifacts_directory':'/private/tmp','working_volume_free_gib_at_last_check':1.7},
}
p=ROOT/'.local/evolution/control-execution-c05-verified.json'
p.write_text(json.dumps(report,indent=2,ensure_ascii=False)+'\n')
print(json.dumps(artifact(p),indent=2))
