import json,os,pathlib,subprocess,time,urllib.request,urllib.parse
root=pathlib.Path('/path/to/SmartFactory-2026');work=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004')
pg=json.loads((root/'.local/evolution/pg18-test.json').read_text());u=urllib.parse.urlparse(pg['dsn']);images=json.loads((root/'deploy/evolution-targets.lock.json').read_text())['images']
routing=dict(line.split('=',1) for line in (work/'tb-edge.env').read_text().splitlines())
env=dict(os.environ,**routing,SPRING_DATASOURCE_URL=f'jdbc:postgresql://host.docker.internal:{u.port}/sf_evolution_tbedge_20261004',SPRING_DATASOURCE_USERNAME=u.username,SPRING_DATASOURCE_PASSWORD=urllib.parse.unquote(u.password),CLOUD_RPC_HOST='sf-evolution-ce-20261004',CLOUD_RPC_PORT='7070',CLOUD_RPC_SSL_ENABLED='false',MALLOC_ARENA_MAX='2',LOCAL_JS_THREAD_POOL_SIZE='2',LOCAL_JS_SANDBOX_MONITOR_THREAD_POOL_SIZE='1',TBEL_THREAD_POOL_SIZE='2',ACTORS_RULE_EXTERNAL_CALL_THREAD_POOL_SIZE='4',JAVA_OPTS='-Xms128m -Xmx512m -XX:MaxMetaspaceSize=256m -XX:MaxDirectMemorySize=96m -XX:ReservedCodeCacheSize=96m -XX:ActiveProcessorCount=2 -Xss512k')
keys=[key for key in env if key not in os.environ or env[key]!=os.environ.get(key)]
cmd=['docker','run','--detach','--rm','--pull','never','--platform','linux/amd64','--name','sf-evolution-edge-20261004','--label','smartfactory.fixture=evolution-native-20261004','--network','smartfactory-evolution-native-20261004','--memory','2g','--memory-swap','2g','--cpus','2','--add-host','host.docker.internal:host-gateway','--publish','127.0.0.1:19081:8080',*[item for key in keys for item in ['--env',key]],images['thingsboard_edge']['pinned']]
result=subprocess.run(cmd,env=env,text=True,capture_output=True,check=True)
private=json.loads((work/'tb-bootstrap.json').read_text());state={'container':'sf-evolution-edge-20261004','id':result.stdout.strip(),'status':'starting','memory_limit_bytes':2147483648,'postgres':'18.6','edge':'4.3.1.6EDGE'};path=root/'.local/evolution/native-edge-state.json';path.write_text(json.dumps(state,indent=2))
for i in range(300):
 try:
  req=urllib.request.Request('http://127.0.0.1:19081/api/auth/login',data=json.dumps({'username':private['username'],'password':private['password']}).encode(),headers={'Content-Type':'application/json'})
  with urllib.request.urlopen(req,timeout=5) as response:token=json.load(response)['token']
  state['status']='ready';path.write_text(json.dumps(state,indent=2));print('Edge CE RPC synchronization and tenant REST login passed',flush=True);break
 except Exception:time.sleep(2)
else:
 state['status']='timed_out';path.write_text(json.dumps(state,indent=2));raise RuntimeError('Edge did not synchronize in ten minutes')
