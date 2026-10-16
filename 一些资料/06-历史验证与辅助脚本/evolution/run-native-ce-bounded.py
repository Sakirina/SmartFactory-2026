import datetime, json, os, pathlib, re, subprocess, threading, time, urllib.parse, urllib.request
root=pathlib.Path('/path/to/SmartFactory-2026');work=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004');evidence=root/'.local/evolution/native-controlled-resources.json';name='sf-evolution-ce-20261004'
pg=json.loads((root/'.local/evolution/pg18-test.json').read_text());native=json.loads((work/'tb-bootstrap.json').read_text());credentials=json.loads((work/'development-credentials.json').read_text());u=urllib.parse.urlparse(pg['dsn']);started=time.monotonic();lock=threading.Lock();finished=threading.Event()
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat()
e={'schema_version':1,'started_at':now(),'budget':{'ce_container_bytes':3*1024**3,'edge_container_bytes':1792*1024**2,'kafka_container_bytes':1024**3,'postgres_container_bytes':1024**3,'combined_limit_bytes':int(6.75*1024**3),'docker_memory_bytes':8319504384},'jvm_budget':{'ce':{'heap_max_mib':512,'metaspace_max_mib':256,'direct_max_mib':64,'code_cache_max_mib':64,'thread_stack_kib':512,'active_processors':2,'nmt':'summary'},'edge':{'heap_max_mib':512,'metaspace_max_mib':256,'direct_max_mib':96,'code_cache_max_mib':96,'thread_stack_kib':512,'active_processors':2},'kafka':{'heap_max_mib':256}},'architecture':{'docker_host':'aarch64','ce_image':'linux/amd64','edge_image':'linux/amd64','architecture_emulation':True,'measurement_note':'JVM NMT and cgroup/container memory are separate observations; unmeasured overhead is not attributed'},'samples':[],'checks':[],'startup_login_attempts':0,'controlled_read_iterations':0}
def save():
 with lock:evidence.write_text(json.dumps(e,indent=2)+'\n')
def run(command,check=True):return subprocess.run(command,capture_output=True,text=True,check=check)
def private_log(file,content):
 p=work/file;p.write_text(content);p.chmod(0o600);return str(p)
def req(base,path,value=None,token=None,tb=False):
 headers={'Content-Type':'application/json'}
 if token:headers['X-Authorization' if tb else 'Authorization']='Bearer '+token
 request=urllib.request.Request(base+path,data=None if value is None else json.dumps(value).encode(),headers=headers)
 with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=30) as response:return json.load(response)
def sample():
 command=['docker','stats','--no-stream','--format','{{json .}}',name,'sf-evolution-edge-20261004','sf-evolution-kafka-20261004',pg['container']]
 while not finished.is_set():
  out=run(command,check=False);values=[]
  for line in out.stdout.splitlines():
   try:
    v=json.loads(line);values.append({k:v.get(k) for k in ['Name','MemUsage','CPUPerc','PIDs']})
   except json.JSONDecodeError:pass
  e['samples'].append({'at':now(),'elapsed_seconds':round(time.monotonic()-started,3),'containers':values});save();finished.wait(2)
env={'SPRING_DATASOURCE_URL':f'jdbc:postgresql://host.docker.internal:{u.port}/sf_evolution_tbce_20261004','SPRING_DATASOURCE_USERNAME':u.username,'SPRING_DATASOURCE_PASSWORD':urllib.parse.unquote(u.password),'SPRING_DATASOURCE_MAXIMUM_POOL_SIZE':'4','SPRING_DATASOURCE_MINIMUM_IDLE':'1','SPRING_DATASOURCE_HIKARI_MAXIMUM_POOL_SIZE':'4','SPRING_DATASOURCE_HIKARI_MINIMUM_IDLE':'1','TB_QUEUE_TYPE':'kafka','TB_KAFKA_SERVERS':'sf-evolution-kafka-20261004:9092','TB_QUEUE_KAFKA_BOOTSTRAP_SERVERS':'sf-evolution-kafka-20261004:9092','EDGES_ENABLED':'true','JS_EVALUATOR':'local','MALLOC_ARENA_MAX':'2','LOCAL_JS_THREAD_POOL_SIZE':'2','LOCAL_JS_SANDBOX_MONITOR_THREAD_POOL_SIZE':'1','TBEL_THREAD_POOL_SIZE':'2','ACTORS_RULE_EXTERNAL_CALL_THREAD_POOL_SIZE':'4','JAVA_OPTS':'-Xms128m -Xmx512m -XX:MaxMetaspaceSize=256m -XX:MaxDirectMemorySize=64m -XX:ReservedCodeCacheSize=64m -XX:ActiveProcessorCount=2 -Xss512k -XX:NativeMemoryTracking=summary -XX:+UnlockDiagnosticVMOptions -XX:+PrintNMTStatistics','TB_SERVICE_ID':'sf-evolution-cloud-native'}
image=json.loads((root/'deploy/evolution-targets.lock.json').read_text())['images']['thingsboard']['pinned']
command=['docker','run','--detach','--pull','never','--platform','linux/amd64','--name',name,'--label','smartfactory.fixture=evolution-native-20261004','--network','smartfactory-evolution-native-20261004','--memory','3g','--memory-swap','3g','--cpus','3','--add-host','host.docker.internal:host-gateway','--publish','127.0.0.1:19080:8080','--publish','127.0.0.1:19070:7070',*[item for key in env for item in ['--env',key]],image]
container=run(command,check=False) if False else subprocess.run(command,env=dict(os.environ,**env),capture_output=True,text=True,check=True)
e['container_id']=container.stdout.strip();save();sampler=threading.Thread(target=sample,daemon=True);sampler.start();print('CE controlled budget container started; collecting container memory',flush=True)
try:
 token=None
 for attempt in range(180):
  e['startup_login_attempts']=attempt+1
  try:token=req('http://127.0.0.1:19080','/api/auth/login',{'username':native['username'],'password':native['password']})['token'];break
  except Exception:
   state=json.loads(run(['docker','inspect',name]).stdout)[0]['State']
   if not state['Running']:raise RuntimeError('CE exited before readiness')
   time.sleep(2)
 else:raise RuntimeError('CE login readiness timed out')
 e['ready_at']=now();e['startup_seconds']=round(time.monotonic()-started,3);save();print('CE login ready; validating preserved messages and new roundtrips',flush=True)
 commandline=run(['docker','exec',name,'sh','-c','tr "\\000" " " < /proc/1/cmdline']).stdout.strip();e['actual_ce_jvm_command']=commandline
 e['actual_edge_jvm_command']=run(['docker','exec','sf-evolution-edge-20261004','sh','-c','ps -eo args | sed -n "3p"']).stdout.strip()
 assert all(v in commandline for v in ['-Xmx512m','MaxMetaspaceSize=256m','MaxDirectMemorySize=64m','ReservedCodeCacheSize=64m','NativeMemoryTracking=summary'])
 e['checks'].append({'name':'actual_ce_jvm_budget','passed':True});save()
 for mode,port,tbport in [('ce',19990,19080),('edge',19991,19081)]:
  sf='http://127.0.0.1:'+str(port);tb='http://127.0.0.1:'+str(tbport)
  tbtoken=req(tb,'/api/auth/login',{'username':native['username'],'password':native['password']})['token'];sftoken=req(sf,'/api/sf/v1/login',{'login':'admin','password':credentials['password']})['token']
  old=json.loads((root/('.local/evolution/native-'+mode+'-roundtrip.json')).read_text())['profiles'][0];mid=old['message_id'];timestamp=int(mid.rsplit('-',1)[1]);batch={'message_id':mid,'source_id':'edge-a','critical':True,'event':{'kind':'native_verification'},'points':[{'device_id':'climate-1','key':'temperature','value':22.5,'observed_ms':timestamp,'quality':'GOOD','time_source':'simulation'}]}
  repeat=req(sf,'/api/sf/v1/ingest',batch,credentials['service_token']);assert repeat['duplicate']
  data=req(sf,f'/api/sf/v1/data?device_ids=climate-1&from_ms={timestamp}&to_ms={timestamp}&limit=100',token=sftoken);assert {'temperature','climate-average.temperature'}<={p['key'] for p in data['points']}
  audit=req(sf,'/api/sf/v1/audit/verify',{},sftoken);assert audit['valid'] and not audit['issues']
  entities=req(sf,'/api/sf/v1/entities',token=sftoken);entity=next(v for v in entities if v['id']=='climate-1')
  points=req(tb,f"/api/plugins/telemetry/DEVICE/{entity['tb_id']}/values/timeseries?keys=temperature,climate-average.temperature&startTs={timestamp-1}&endTs={timestamp+1}&agg=NONE&limit=100",token=tbtoken,tb=True);assert points.get('temperature') and points.get('climate-average.temperature')
  e['checks'].append({'name':mode+'_preserved_original_message_and_audit','passed':True,'message_id':mid,'duplicate':True,'audit_valid':True,'tb_keys':sorted(points)});save()
 check=run(['python3',str(root/'scripts/check-native.py'),'--state-directory',str(work),'--cloud-url','http://127.0.0.1:19990','--edge-url','http://127.0.0.1:19991','--tb-cloud-url','http://127.0.0.1:19080','--tb-edge-url','http://127.0.0.1:19081','--output',str(root/'.local/evolution/native-controlled-roundtrip.json')],check=False)
 e['roundtrip_exit_code']=check.returncode;e['roundtrip_log']=private_log('ce-controlled-roundtrip.log',check.stdout+check.stderr);save();assert check.returncode==0
 for i in range(12):
  req('http://127.0.0.1:19080','/api/auth/user',token=token,tb=True);e['controlled_read_iterations']=i+1;save();time.sleep(5)
 for phase in ['after_load']:
  result=run(['docker','exec',name,'sh','-c','if command -v jcmd >/dev/null; then jcmd 1 VM.native_memory summary; else echo jcmd_unavailable; fi'],check=False);e['nmt_output_path']=private_log('ce-'+phase+'-nmt.log',result.stdout+result.stderr);e['nmt_command_exit_code']=result.returncode;e['nmt_summary']=result.stdout
  result=run(['docker','exec',name,'sh','-c','cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak /sys/fs/cgroup/memory.stat'],check=False);e['cgroup_memory_output']=result.stdout
 e['controlled_load']={'new_ingress_messages':2,'fresh_duplicate_checks':2,'prior_duplicate_checks':2,'post_readiness_authenticated_ce_reads':12,'polling_readiness_attempts':e['startup_login_attempts'],'load_concurrency':1}
 e['completed_at']=now();e['elapsed_seconds']=round(time.monotonic()-started,3);e['all_checks_passed']=True;save();print('CE bounded API/prior/new/duplicate/audit verification passed',flush=True)
except Exception as error:
 e['all_checks_passed']=False;e['error_type']=type(error).__name__;e['error']=str(error);save();raise
finally:
 finished.set();sampler.join(timeout=30)
 result=run(['docker','exec',name,'sh','-c','if command -v jcmd >/dev/null; then jcmd 1 VM.native_memory summary; else echo jcmd_unavailable; fi'],check=False);e['final_nmt_live_output']=result.stdout+result.stderr
 result=run(['docker','exec',name,'sh','-c','cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak /sys/fs/cgroup/memory.stat'],check=False);e['final_cgroup_live_output']=result.stdout
 stopped=run(['docker','stop','--time','30',name],check=False);e['stop_command_exit_code']=stopped.returncode
 log=run(['docker','logs',name],check=False);e['container_log']=private_log('ce-controlled-container.log',log.stdout+log.stderr)
 idx=log.stdout.rfind('Native Memory Tracking:')
 if idx<0:idx=log.stderr.rfind('Native Memory Tracking:');e['shutdown_nmt_summary']=log.stderr[idx:] if idx>=0 else None
 else:e['shutdown_nmt_summary']=log.stdout[idx:]
 inspected=run(['docker','inspect',name],check=False)
 if inspected.returncode==0:
  final=json.loads(inspected.stdout)[0]['State'];e['final_container_state']={k:final.get(k) for k in ['Status','Running','OOMKilled','ExitCode','StartedAt','FinishedAt','Error']}
  if not final['Running']:e['remove_command_exit_code']=run(['docker','rm',name],check=False).returncode
 e['ended_at']=now();save();print('CE controlled container exit and diagnostics recorded',flush=True)
