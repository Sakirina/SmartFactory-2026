import json,os,pathlib,subprocess,time
root=pathlib.Path('/path/to/SmartFactory-2026');work=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004');pg=json.loads((root/'.local/evolution/pg18-test.json').read_text());path=root/'.local/evolution/native-resource-repair.json'
e={'schema_version':1,'failure_times_utc':['2026-10-04T14:28:04.688Z','2026-10-04T14:28:18.698Z'],'failures':['PostgreSQL backend signal9; automatic WAL recovery','max_connections=30 exhausted by two default Hikari pools'],'before':{'postgres_memory_bytes':536870912,'postgres_max_connections':30,'tb_default_hikari_max_pool_per_instance':16},'after':{'postgres_memory_bytes':1073741824,'postgres_max_connections':30,'tb_hikari_max_pool_per_instance':4,'tb_hikari_min_idle_per_instance':1},'resource_limits':{'ce':3221225472,'edge':2147483648,'kafka':805306368,'postgres':1073741824},'steps':[]}
def save():path.write_text(json.dumps(e,indent=2)+'\n')
def run(name,command,env=None):
 p=subprocess.run(command,env=env,capture_output=True,text=True);e['steps'].append({'name':name,'exit_code':p.returncode});save()
 if p.returncode:raise RuntimeError(name)
 return p.stdout.strip()
run('pg_live_memory_limit',['docker','update','--memory','1g','--memory-swap','1g',pg['container']]);pg['memory_limit_bytes']=1073741824;(root/'.local/evolution/pg18-test.json').write_text(json.dumps(pg,indent=2));(root/'.local/evolution/pg18-test.json').chmod(0o600)
# Copy the existing Edge adoption marker; its initialized database is retained.
data=work/'edge-data';data.mkdir(exist_ok=True);data.chmod(0o755)
run('edge_marker_copy',['docker','cp','sf-evolution-edge-20261004:/data/.firstlaunch',str(data/'.firstlaunch')]);(data/'.firstlaunch').chmod(0o644)
for kind,name,mem,cpus,ports in [('ce','sf-evolution-ce-20261004','3g','3',['127.0.0.1:19080:8080','127.0.0.1:19070:7070']),('edge','sf-evolution-edge-20261004','2g','2',['127.0.0.1:19081:8080'])]:
 inspected=subprocess.run(['docker','inspect',name],capture_output=True,text=True)
 if inspected.returncode==0:
  old=json.loads(inspected.stdout)[0]
  environment=dict(item.split('=',1) for item in old['Config']['Env'])
 else:
  if kind!='ce':raise RuntimeError('missing Edge fixture')
  import urllib.parse
  u=urllib.parse.urlparse(pg['dsn'])
  image=json.loads((root/'deploy/evolution-targets.lock.json').read_text())['images']['thingsboard']['pinned']
  environment={'SPRING_DATASOURCE_URL':f'jdbc:postgresql://host.docker.internal:{u.port}/sf_evolution_tbce_20261004','SPRING_DATASOURCE_USERNAME':u.username,'SPRING_DATASOURCE_PASSWORD':urllib.parse.unquote(u.password),'TB_QUEUE_TYPE':'kafka','TB_KAFKA_SERVERS':'sf-evolution-kafka-20261004:9092','TB_QUEUE_KAFKA_BOOTSTRAP_SERVERS':'sf-evolution-kafka-20261004:9092','EDGES_ENABLED':'true','JS_EVALUATOR':'local','MALLOC_ARENA_MAX':'2','LOCAL_JS_THREAD_POOL_SIZE':'2','LOCAL_JS_SANDBOX_MONITOR_THREAD_POOL_SIZE':'1','TBEL_THREAD_POOL_SIZE':'2','ACTORS_RULE_EXTERNAL_CALL_THREAD_POOL_SIZE':'4','JAVA_OPTS':'-Xms256m -Xmx640m -XX:MaxMetaspaceSize=320m -XX:MaxDirectMemorySize=96m -XX:ReservedCodeCacheSize=96m -XX:ActiveProcessorCount=2 -Xss512k','TB_SERVICE_ID':'sf-evolution-cloud-native'}
  old={'Config':{'Image':image}}

 environment.update(SPRING_DATASOURCE_HIKARI_MAXIMUM_POOL_SIZE='4',SPRING_DATASOURCE_HIKARI_MINIMUM_IDLE='1',SPRING_DATASOURCE_MAXIMUM_POOL_SIZE='4',SPRING_DATASOURCE_MINIMUM_IDLE='1')
 
 if inspected.returncode==0:run(kind+'_stop',['docker','stop','--time','30',name])
 command=['docker','run','--detach','--rm','--pull','never','--platform','linux/amd64','--name',name,'--label','smartfactory.fixture=evolution-native-20261004','--network','smartfactory-evolution-native-20261004','--memory',mem,'--memory-swap',mem,'--cpus',cpus,'--add-host','host.docker.internal:host-gateway',*[item for port in ports for item in ['--publish',port]],*[item for key in environment for item in ['--env',key]]]
 if kind=='edge':command+=['--mount',f'type=bind,src={data},dst=/data']
 command+=[old['Config']['Image']]
 e[kind+'_container_id']=run(kind+'_restart_with_bounded_pool',command,dict(os.environ,**environment));save()
e['status']='containers_restarted';save();print('PG limit updated without restart; CE/Edge retain databases and use four-connection pools',flush=True)
