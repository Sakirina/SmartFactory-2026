import datetime,hashlib,json,os,pathlib,subprocess,sys,time
repo=pathlib.Path('/path/to/SmartFactory-2026');evidence=repo/'.local/evolution/query-evidence';frozen=pathlib.Path((evidence/'final-snapshot-path.txt').read_text().strip());platform=frozen/'Platform';bins=frozen/'binaries';bins.mkdir(exist_ok=True)
config=json.loads((repo/'.local/evolution/pg18-test.json').read_text());env=os.environ.copy();env.update(GOTOOLCHAIN='local',GOPATH='/private/tmp/smartfactory-evolution-gopath',GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',GOCACHE='/private/tmp/smartfactory-evolution-go-cache',GOPROXY='off',GOMAXPROCS='2',SF_TEST_POSTGRES_DATABASE=config['dsn'])
report_path=evidence/'final-verification-runs.json';report=json.loads(report_path.read_text()) if report_path.exists() else {'snapshot':str(frozen),'runtime':{'GOMAXPROCS':2,'GOPROXY':'off','package_parallelism':1,'postgres_database':'private isolated PostgreSQL DSN supplied through environment','max_application_connections':3,'max_idle_connections':1},'runs':[],'binaries':{}}
def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def save():report_path.write_text(json.dumps(report,indent=2)+'\n')
def run(name,cmd):
 started=stamp();clock=time.monotonic();path=evidence/(name+('.jsonl' if 'test2json' in cmd else '.log'))
 with path.open('w') as output:r=subprocess.run(cmd,cwd=platform,env=env,stdout=output,stderr=subprocess.STDOUT)
 item={'input_manifest_sha256':hashlib.sha256((frozen/'input-manifest.json').read_bytes()).hexdigest(),'snapshot':str(frozen),'name':name,'command':cmd,'cwd':str(platform),'started_at':started,'completed_at':stamp(),'elapsed_seconds':round(time.monotonic()-clock,3),'exit_code':r.returncode,'log':str(path.relative_to(repo)),'log_sha256':hashlib.sha256(path.read_bytes()).hexdigest()};
 for arg in cmd:
  path=pathlib.Path(arg)
  if path.is_file() and (path.suffix=='.test' or path.parent==bins):item['executed_binary_sha256']=hashlib.sha256(path.read_bytes()).hexdigest()
 report['runs'].append(item);save();print(name,'exit',r.returncode,'elapsed',item['elapsed_seconds'],flush=True)
 if r.returncode:raise SystemExit(r.returncode)
def bind(path,role):
 report['binaries'][str(path)]={'sha256':hashlib.sha256(path.read_bytes()).hexdigest(),'bytes':path.stat().st_size,'role':role,'recorded_at':stamp()};save()
normal=[('store','^Test(Query|ControlEvidenceAtomicReceiptAndImmutableIdentity|PostgresControlEvidenceAtomicReceiptAndImmutableIdentity)'),('application','^TestQuery'),('api','^TestQuery'),('cloudsync','^Test(PostgresControlReceipt|ControlReceipt)'),('control','^Test')]
if len(sys.argv)==1 or sys.argv[1]=='normal':
 run('final-build-tests',['go','test','-c','-trimpath','-p','1','-o',str(bins),*[f'./internal/{p}' for p,_ in normal]])
 for pkg,pattern in normal:
  binary=bins/(pkg+'.test');bind(binary,'normal test binary');run('final-'+pkg,['go','tool','test2json','-t','-p','competition2026/product/platform/internal/'+pkg,str(binary),'-test.v=test2json','-test.timeout=120s','-test.run='+pattern,'-test.count=1'])
 for target in ['sf-cloud','sf-edge','sf-contracts']:
  binary=bins/target;run('final-build-'+target,['go','build','-trimpath','-p','1','-o',str(binary),'./cmd/'+target]);bind(binary,'application binary')
 contract=frozen/'contracts/1.0';example=frozen/'examples/factory/definitions.json';run('final-contract-generation',[str(bins/'sf-contracts'),'-out',str(contract),'-examples',str(example)])
 run('final-contract-validation',['/private/tmp/smartfactory-evolution-python/bin/python',str(frozen/'scripts/check-contracts.py')])
 print('NORMAL_VERIFICATION_COMPLETE',flush=True)
elif sys.argv[1]=='normal-tail':
 binary=bins/'control.test';run('final-v2-build-control',['go','test','-c','-trimpath','-p','1','-o',str(binary),'./internal/control']);bind(binary,'normal control regression binary with complete migration-four fixture')
 run('final-v2-control',['go','tool','test2json','-t','-p','competition2026/product/platform/internal/control',str(binary),'-test.v=test2json','-test.timeout=120s','-test.run=^Test','-test.count=1'])
 for target in ['sf-cloud','sf-edge','sf-contracts']:
  binary=bins/target;run('final-build-'+target,['go','build','-trimpath','-p','1','-o',str(binary),'./cmd/'+target]);bind(binary,'application binary')
 run('final-contract-generation',[str(bins/'sf-contracts'),'-out',str(frozen/'contracts/1.0'),'-examples',str(frozen/'examples/factory/definitions.json')])
 run('final-contract-validation',['/private/tmp/smartfactory-evolution-python/bin/python',str(frozen/'scripts/check-contracts.py')])
 print('NORMAL_VERIFICATION_COMPLETE',flush=True)
else:
 race=bins/'race';race.mkdir(exist_ok=True)
 checks=[('application','^TestQuery'),('api','^TestQuerySSE'),('store','^TestQuery(LateCommit|RebuildPostgres)'),('cloudsync','^Test(ControlReceiptEvidenceMTLSResponseLossAndCurrentResources|PostgresControlReceiptEvidenceOrderingAndLegacyCompatibility)$'),('control','^Test(PostgresControlAtomicTransitionsAndConcurrentIntervention|CancelDuringCommandRetainsLateResultAndSkipsRemaining|ConcurrentManualOperationsHaveSingleVersionWinner)$')]
 run('final-race-build',['go','test','-c','-race','-trimpath','-p','1','-o',str(race),*[f'./internal/{p}' for p,_ in checks]])
 for pkg,pattern in checks:
  binary=race/(pkg+'.test');bind(binary,'race test binary');run('final-race-'+pkg,['go','tool','test2json','-t','-p','competition2026/product/platform/internal/'+pkg,str(binary),'-test.v=test2json','-test.timeout=180s','-test.run='+pattern,'-test.count=1'])
 print('RACE_VERIFICATION_COMPLETE',flush=True)
