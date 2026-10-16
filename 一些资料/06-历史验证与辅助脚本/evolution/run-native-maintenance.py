import datetime,json,os,pathlib,secrets,subprocess,time,urllib.parse,urllib.request
ROOT=pathlib.Path('/path/to/SmartFactory-2026')
WORK=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004')
WORK.mkdir(exist_ok=True,mode=0o700)
os.umask(0o077)
pg=json.loads((ROOT/'.local/evolution/pg18-test.json').read_text()); u=urllib.parse.urlparse(pg['dsn'])
images=json.loads((ROOT/'deploy/evolution-targets.lock.json').read_text())['images']
EVIDENCE=ROOT/'.local/evolution/native-maintenance.json'
e={'schema_version':1,'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'work_directory':str(WORK),'processes':{},'resources':{'kafka_memory_bytes':805306368,'ce_memory_bytes':3221225472,'edge_memory_bytes':2147483648},'steps':[]}
def save():EVIDENCE.write_text(json.dumps(e,indent=2)+'\n')
def step(name,result='passed'):
 e['steps'].append({'name':name,'result':result,'at':datetime.datetime.now(datetime.timezone.utc).isoformat()});save();print(name,result,flush=True)
def run(name,cmd,env=None):
 with (WORK/(name+'.log')).open('w') as f:
  p=subprocess.run(cmd,env=env,stdout=f,stderr=subprocess.STDOUT)
 if p.returncode:step(name,'failed');raise RuntimeError(name+' exited '+str(p.returncode))
 step(name)
def docker(name,cmd,environment=None):
 env=dict(os.environ,**(environment or {}))
 command=['docker','run','--detach','--rm','--pull','never','--name',name,'--label','smartfactory.fixture=evolution-native-20261004',*cmd]
 result=subprocess.run(command,env=env,text=True,capture_output=True,check=True)
 e['processes'][name]={'container_id':result.stdout.strip()};save()
 return result.stdout.strip()
def wait(fn,name,seconds=600):
 end=time.monotonic()+seconds
 while time.monotonic()<end:
  try:
   if fn():step(name);return
  except Exception:pass
  time.sleep(2)
 step(name,'timed_out');raise RuntimeError(name)
def request(base,path,value=None,token=None):
 headers={'Content-Type':'application/json'}
 if token:headers['X-Authorization']='Bearer '+token
 req=urllib.request.Request(base+path,data=None if value is None else json.dumps(value).encode(),headers=headers)
 with urllib.request.urlopen(req,timeout=8) as response:return json.load(response)
try:
 network='smartfactory-evolution-native-20261004'
 result=subprocess.run(['docker','network','inspect',network],capture_output=True)
 if result.returncode:run('network',['docker','network','create',network])
 for name in ['sf_evolution_tbce_20261004','sf_evolution_tbedge_20261004']:
  exists=subprocess.check_output(['docker','exec',pg['container'],'psql','-U',u.username,'-d','postgres','-tAc',"SELECT 1 FROM pg_database WHERE datname='"+name+"'"]).strip()
  if not exists:run(name,['docker','exec',pg['container'],'createdb','-U',u.username,name])
 kafka='sf-evolution-kafka-20261004'
 ke={'KAFKA_NODE_ID':'1','KAFKA_PROCESS_ROLES':'broker,controller','KAFKA_LISTENERS':'PLAINTEXT://:9092,CONTROLLER://:9093','KAFKA_ADVERTISED_LISTENERS':'PLAINTEXT://'+kafka+':9092','KAFKA_LISTENER_SECURITY_PROTOCOL_MAP':'CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT','KAFKA_CONTROLLER_QUORUM_VOTERS':'1@'+kafka+':9093','KAFKA_CONTROLLER_LISTENER_NAMES':'CONTROLLER','KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR':'1','KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR':'1','KAFKA_TRANSACTION_STATE_LOG_MIN_ISR':'1','KAFKA_LOG_DIRS':'/tmp/kraft-combined-logs','KAFKA_HEAP_OPTS':'-Xms128m -Xmx384m','KAFKA_NUM_PARTITIONS':'1','KAFKA_LOG_RETENTION_BYTES':'33554432'}
 docker(kafka,['--network',network,'--memory','768m','--memory-swap','768m','--cpus','2','--tmpfs','/tmp/kraft-combined-logs:rw,size=512m,uid=1000,gid=1000',*[item for key in ke for item in ['--env',key]],images['kafka']['pinned']],ke)
 wait(lambda:subprocess.run(['docker','exec',kafka,'bash','-ec','exec 3<>/dev/tcp/127.0.0.1/9092'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0,'kafka-ready',120)
 passwords={'tb_admin_password':secrets.token_urlsafe(24),'tb_sysadmin_password':secrets.token_urlsafe(24),'password':secrets.token_urlsafe(24),'service_token':secrets.token_urlsafe(32)}
 (WORK/'deployment-credentials.json').write_text(json.dumps(passwords));(WORK/'development-credentials.json').write_text(json.dumps({'password':passwords['password'],'service_token':passwords['service_token']}))
 tb={'SPRING_DATASOURCE_URL':f'jdbc:postgresql://host.docker.internal:{u.port}/sf_evolution_tbce_20261004','SPRING_DATASOURCE_USERNAME':u.username,'SPRING_DATASOURCE_PASSWORD':urllib.parse.unquote(u.password),'TB_QUEUE_TYPE':'kafka','TB_KAFKA_SERVERS':kafka+':9092','TB_QUEUE_KAFKA_BOOTSTRAP_SERVERS':kafka+':9092','EDGES_ENABLED':'true','JS_EVALUATOR':'local','MALLOC_ARENA_MAX':'2','LOCAL_JS_THREAD_POOL_SIZE':'2','LOCAL_JS_SANDBOX_MONITOR_THREAD_POOL_SIZE':'1','TBEL_THREAD_POOL_SIZE':'2','ACTORS_RULE_EXTERNAL_CALL_THREAD_POOL_SIZE':'4','JAVA_OPTS':'-Xms256m -Xmx768m -XX:MaxMetaspaceSize=384m -XX:MaxDirectMemorySize=128m -XX:ReservedCodeCacheSize=96m -XX:ActiveProcessorCount=2 -Xss512k','TB_SERVICE_ID':'sf-evolution-cloud-native'}
 install=dict(tb,INSTALL_TB='true',LOAD_DEMO='false')
 env=dict(os.environ,**install)
 run('ce-install',['docker','run','--rm','--pull','never','--name','sf-evolution-ce-install-20261004','--label','smartfactory.fixture=evolution-native-20261004','--network',network,'--memory','3g','--memory-swap','3g','--cpus','3','--add-host','host.docker.internal:host-gateway',*[item for key in install for item in ['--env',key]],images['thingsboard']['pinned']],env)
 ce='sf-evolution-ce-20261004'
 docker(ce,['--network',network,'--memory','3g','--memory-swap','3g','--cpus','3','--add-host','host.docker.internal:host-gateway','--publish','127.0.0.1:19080:8080','--publish','127.0.0.1:19070:7070',*[item for key in tb for item in ['--env',key]],images['thingsboard']['pinned']],tb)
 run('ce-bootstrap',['python3',str(ROOT/'scripts/bootstrap-thingsboard.py'),'--url','http://127.0.0.1:19080','--state-directory',str(WORK),'--edges','edge-a'])
 step('ce-api-ready')
 e['status']='ce_ready';save()
except Exception as ex:
 e['status']='failed';e['error']=str(ex);save();raise
