import datetime,hashlib,json,os,pathlib,subprocess,sys,time
repo=pathlib.Path('/path/to/SmartFactory-2026');evidence=repo/'.local/evolution/business-evidence';frozen=pathlib.Path((evidence/'final-snapshot-path.txt').read_text().strip());bins=frozen/'binaries';bins.mkdir(exist_ok=True)
manifest=evidence/'final-actual-inputs.json';config=json.loads((repo/'.local/evolution/pg18-test.json').read_text());env=dict(os.environ,GOTOOLCHAIN='local',GOPATH='/private/tmp/smartfactory-evolution-gopath',GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',GOCACHE='/private/tmp/smartfactory-evolution-go-cache',GOPROXY='off',GOMAXPROCS='2',SF_TEST_POSTGRES_DATABASE=config['dsn'])
report_path=evidence/'final-verification-runs.json';report=json.loads(report_path.read_text()) if report_path.exists() else {'snapshot':str(frozen),'input_manifest':str(manifest),'input_manifest_sha256':hashlib.sha256(manifest.read_bytes()).hexdigest(),'runtime':{'GOMAXPROCS':2,'GOPROXY':'off','package_parallelism':1,'application_connections':3,'idle_connections':1,'management_connections':1},'runs':[],'binaries':{}}
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def save():report_path.write_text(json.dumps(report,indent=2)+'\n')
def run(name,cmd,module='Platform'):
 started=now();clock=time.monotonic();log=evidence/(name+'.log')
 with log.open('w') as out:r=subprocess.run(cmd,cwd=frozen/module,env=env,stdout=out,stderr=subprocess.STDOUT)
 entry={'name':name,'command':cmd,'cwd':str(frozen/module),'started_at':started,'completed_at':now(),'elapsed_seconds':round(time.monotonic()-clock,3),'exit_code':r.returncode,'log':str(log),'log_sha256':hashlib.sha256(log.read_bytes()).hexdigest()}
 for arg in cmd:
  p=pathlib.Path(arg)
  if p.is_file() and p.is_relative_to(bins):entry['executed_binary_sha256']=hashlib.sha256(p.read_bytes()).hexdigest()
 report['runs'].append(entry);save();print(name,'exit',r.returncode,'elapsed',entry['elapsed_seconds'],flush=True)
 if r.returncode:print(log.read_text()[-8000:],flush=True);raise SystemExit(r.returncode)
def bind(p,role):report['binaries'][str(p)]={'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'bytes':p.stat().st_size,'role':role,'recorded_at':now()};save()
normal=[('application','^TestBusiness'),('api','^TestBusinessHTTP'),('cloudsync','^TestBusiness'),('deviceconfig','^TestConfigurationMetadata'),('thingsboard','^TestBusinessNativePoll'),('app','^TestBusinessNativeWorker'),('store','^TestMigration')]
if len(sys.argv)==1 or sys.argv[1]=='normal':
 run('final-build-platform-tests',['go','test','-c','-trimpath','-p','1','-o',str(bins),*[f'./internal/{p}' for p,_ in normal]])
 for pkg,pattern in normal:
  binary=bins/(pkg+'.test');bind(binary,'normal business test binary');run('final-'+pkg,['go','tool','test2json','-t','-p','competition2026/product/platform/internal/'+pkg,str(binary),'-test.v=test2json','-test.timeout=180s','-test.run='+pattern,'-test.count=1'])
 dbins=bins/'datatransfer';dbins.mkdir(exist_ok=True)
 run('final-build-datatransfer-tests',['go','test','-c','-trimpath','-p','1','-o',str(dbins),'./internal/config','./internal/configmanager'],'DataTransfer')
 for pkg,pattern in [('config','^TestTLSJSON'),('configmanager','^Test(BusinessConnector|ConfigurationAndRevisionRestore)')]:
  binary=dbins/(pkg+'.test');bind(binary,'configuration wire and effective manager test binary');run('final-datatransfer-'+pkg,['go','tool','test2json','-t','-p','competition2026/product/datatransfer/internal/'+pkg,str(binary),'-test.v=test2json','-test.timeout=120s','-test.run='+pattern,'-test.count=1'],'DataTransfer')
 for target in ['sf-cloud','sf-edge','sf-contracts','sf-business-fixture']:
  binary=bins/target;run('final-build-'+target,['go','build','-trimpath','-p','1','-o',str(binary),'./cmd/'+target]);bind(binary,'application binary')
 run('final-contract-generation',[str(bins/'sf-contracts'),'-out',str(frozen/'contracts/1.0'),'-examples',str(frozen/'examples/factory/definitions.json')])
 run('final-contract-validation',['/private/tmp/smartfactory-evolution-python/bin/python',str(frozen/'scripts/check-contracts.py')])
 print('NORMAL_VERIFICATION_COMPLETE',flush=True)
else:
 race=bins/'race';race.mkdir(exist_ok=True)
 checks=[('application','^TestBusiness(Concurrent|HandoverReviewed|WorkOrderRejects|ConnectorSecrets)'),('api','^TestBusinessHTTPContinuous'),('cloudsync','^TestBusiness'),('thingsboard','^TestBusinessNativePoll'),('app','^TestBusinessNativeWorker')]
 run('final-build-race-tests',['go','test','-c','-race','-trimpath','-p','1','-o',str(race),*[f'./internal/{p}' for p,_ in checks]])
 for pkg,pattern in checks:
  binary=race/(pkg+'.test');bind(binary,'race business test binary');run('final-race-'+pkg,['go','tool','test2json','-t','-p','competition2026/product/platform/internal/'+pkg,str(binary),'-test.v=test2json','-test.timeout=240s','-test.run='+pattern,'-test.count=1'])
 drace=race/'datatransfer';drace.mkdir(exist_ok=True);binary=drace/'configmanager.test'
 run('final-build-race-datatransfer',['go','test','-c','-race','-trimpath','-p','1','-o',str(binary),'./internal/configmanager'],'DataTransfer');bind(binary,'race effective configuration manager test binary')
 run('final-race-datatransfer',['go','tool','test2json','-t','-p','competition2026/product/datatransfer/internal/configmanager',str(binary),'-test.v=test2json','-test.timeout=180s','-test.run=^Test(BusinessConnector|ConcurrentPushes)','-test.count=1'],'DataTransfer')
 print('RACE_VERIFICATION_COMPLETE',flush=True)
