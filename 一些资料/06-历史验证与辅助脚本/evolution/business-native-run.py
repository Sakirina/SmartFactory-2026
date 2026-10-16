import datetime, hashlib, json, os, pathlib, subprocess, time, urllib.parse, urllib.request

root = pathlib.Path('/path/to/SmartFactory-2026')
work = pathlib.Path('/private/tmp/smartfactory-business-native-20261005-retry')
work.mkdir(mode=0o700, exist_ok=False)
report = root / '.local/evolution/business-native-verified.json'
pg = json.loads((root / '.local/evolution/pg18-test.json').read_text())
u = urllib.parse.urlparse(pg['dsn'])
database, name = 'sf_business_native_20261005_retry', 'sf-business-native-20261005-retry'
authfile = pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004/tb-bootstrap.json')
auth = json.loads(authfile.read_text())
image = json.loads((root/'deploy/evolution-targets.lock.json').read_text())['images']['thingsboard']['pinned']
now = lambda: datetime.datetime.now(datetime.timezone.utc).isoformat()
e = {'started_at':now(), 'work_directory':str(work), 'container':name,'database':database,'image':image,'platform':'linux/amd64','docker_host_arch':'aarch64','architecture_emulation':True,'queue':'in-memory','budget':{'container_mib':3072,'postgres_mib':1024,'heap_max_mib':512,'metaspace_max_mib':256,'direct_max_mib':64,'code_cache_max_mib':64,'cpu':3,'jdbc_max_connections':3},'checks':{},'samples':[]}
def save(): report.write_text(json.dumps(e,indent=2)+'\n')
def run(args,env=None): return subprocess.run(args,env=env,capture_output=True,text=True)
def psql(sql):
    env = dict(os.environ, PGPASSWORD=urllib.parse.unquote(u.password))
    r=run(['docker','exec','--env','PGPASSWORD',pg['container'],'psql','-U',u.username,'-d','postgres','-v','ON_ERROR_STOP=1','-c',sql],env)
    if r.returncode: raise RuntimeError('isolated native database operation failed: '+r.stderr)
def login():
    request=urllib.request.Request('http://127.0.0.1:19380/api/auth/login',data=json.dumps({'username':auth['username'],'password':auth['password']}).encode(),headers={'Content-Type':'application/json'})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=6) as response:
        assert json.load(response)['token']
def sample():
    r=run(['docker','stats','--no-stream','--format','{{json .}}',name,pg['container']])
    e['samples'].append({'at':now(),'containers':[json.loads(v) for v in r.stdout.splitlines() if v.startswith('{')]});save()
created=False
try:
    psql('CREATE DATABASE '+database+' TEMPLATE sf_evolution_tbce_20261004');created=True
    values={'SPRING_DATASOURCE_URL':f'jdbc:postgresql://host.docker.internal:{u.port}/{database}','SPRING_DATASOURCE_USERNAME':u.username,'SPRING_DATASOURCE_PASSWORD':urllib.parse.unquote(u.password),'SPRING_DATASOURCE_MAXIMUM_POOL_SIZE':'3','SPRING_DATASOURCE_MINIMUM_IDLE':'1','SPRING_DATASOURCE_HIKARI_MAXIMUM_POOL_SIZE':'3','SPRING_DATASOURCE_HIKARI_MINIMUM_IDLE':'1','TB_QUEUE_TYPE':'in-memory','JS_EVALUATOR':'local','MALLOC_ARENA_MAX':'2','LOCAL_JS_THREAD_POOL_SIZE':'2','LOCAL_JS_SANDBOX_MONITOR_THREAD_POOL_SIZE':'1','TBEL_THREAD_POOL_SIZE':'2','ACTORS_RULE_EXTERNAL_CALL_THREAD_POOL_SIZE':'4','JAVA_OPTS':'-Xms128m -Xmx512m -XX:MaxMetaspaceSize=256m -XX:MaxDirectMemorySize=64m -XX:ReservedCodeCacheSize=64m -XX:ActiveProcessorCount=2 -Xss512k -XX:NativeMemoryTracking=summary','TB_SERVICE_ID':name}
    args=['docker','run','--detach','--pull','never','--platform','linux/amd64','--name',name,'--label','smartfactory.fixture=business-native-20261005','--memory','3g','--memory-swap','3g','--cpus','3','--add-host','host.docker.internal:host-gateway','--publish','127.0.0.1:19380:8080']
    for key in values: args+=['--env',key]
    r=run(args+[image],dict(os.environ,**values))
    if r.returncode: raise RuntimeError('native container start failed: '+r.stderr)
    e['container_id']=r.stdout.strip();save();print('Isolated CE started with a cloned fixture database and bounded JVM.',flush=True)
    for attempt in range(180):
        try: login();e['ready_at']=now();e['login_attempts']=attempt+1;break
        except Exception:
            state=json.loads(run(['docker','inspect',name]).stdout)[0]['State']
            if not state['Running']:raise RuntimeError('native CE exited before readiness')
            if attempt%15==0:sample()
            time.sleep(2)
    else:raise RuntimeError('native CE readiness timeout')
    sample();save();print('Isolated CE is authenticated and ready.',flush=True)
    # Build and execute a retained binary; exact source hashes are captured before compilation.
    files={}
    for module in ['Platform','DataTransfer']:
        for p in (root/module).rglob('*'):
            if p.is_file() and (p.suffix in ['.go','.sql'] or p.name in ['go.mod','go.sum']):files[str(p.relative_to(root))]=hashlib.sha256(p.read_bytes()).hexdigest()
    e['source_before_build']=files;e['build_started_at']=now();save()
    env=dict(os.environ,GOTOOLCHAIN='local',GOPATH='/private/tmp/smartfactory-evolution-gopath',GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',GOCACHE='/private/tmp/smartfactory-evolution-go-cache',GOPROXY='off',GOMAXPROCS='2',SF_NATIVE_URL='http://127.0.0.1:19380',SF_NATIVE_CREDENTIALS=str(authfile))
    binary=work/'api-native.test'
    with (work/'build.log').open('w') as out:r=subprocess.run(['go','test','-c','-o',str(binary),'./internal/api'],cwd=root/'Platform',env=env,stdout=out,stderr=subprocess.STDOUT)
    e['build_exit_code']=r.returncode;e['build_finished_at']=now();save()
    if r.returncode:raise RuntimeError('native test binary build failed')
    e['binary_sha256']=hashlib.sha256(binary.read_bytes()).hexdigest()
    with (work/'native-test.log').open('w') as out:r=subprocess.run([str(binary),'-test.v','-test.run','^TestBusinessNativeCEAlarmLifecycle$','-test.timeout','3m'],cwd=root/'Platform',env=env,stdout=out,stderr=subprocess.STDOUT)
    e['test_exit_code']=r.returncode;e['checks']['actual_native_lifecycle']=r.returncode==0;sample();save()
    print((work/'native-test.log').read_text(),flush=True)
    if r.returncode:raise RuntimeError('native lifecycle integration test failed')
    e['all_checks_passed']=True
except Exception as err:
    e['all_checks_passed']=False;e['error']=str(err);print(str(err),flush=True)
finally:
    state=run(['docker','inspect',name])
    if state.returncode==0:
        e['cgroup']=run(['docker','exec',name,'sh','-c','cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak']).stdout
        e['stop_exit_code']=run(['docker','stop','--time','30',name]).returncode
        r=run(['docker','logs',name]);(work/'container.log').write_text(r.stdout+r.stderr)
        final=json.loads(run(['docker','inspect',name]).stdout)[0]['State'];e['final_container_state']={k:final.get(k) for k in ['Status','Running','OOMKilled','ExitCode']}
        if not final['Running']:e['remove_exit_code']=run(['docker','rm',name]).returncode
    if created:
        psql('DROP DATABASE '+database);e['database_removed']=True
    e['ended_at']=now();save();print('Native fixture stopped and isolated database removed.',flush=True)
if not e.get('all_checks_passed'):raise SystemExit(1)
