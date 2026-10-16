import datetime,json,os,pathlib,subprocess,time
root=pathlib.Path('/path/to/SmartFactory-2026');image=json.loads((root/'deploy/evolution-targets.lock.json').read_text())['images']['kafka']['pinned'];name='sf-evolution-kafka-20261004'
now=datetime.datetime.now(datetime.timezone.utc).isoformat()
result=subprocess.run(['docker','events','--since','2026-10-04T14:20:00Z','--until',now,'--filter','event=oom','--format','{{json .}}'],capture_output=True,text=True)
(root/'.local/evolution/native-oom-events.jsonl').write_text(result.stdout)
env={'KAFKA_NODE_ID':'1','KAFKA_PROCESS_ROLES':'broker,controller','KAFKA_LISTENERS':'PLAINTEXT://:9092,CONTROLLER://:9093','KAFKA_ADVERTISED_LISTENERS':'PLAINTEXT://'+name+':9092','KAFKA_LISTENER_SECURITY_PROTOCOL_MAP':'CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT','KAFKA_CONTROLLER_QUORUM_VOTERS':'1@'+name+':9093','KAFKA_CONTROLLER_LISTENER_NAMES':'CONTROLLER','KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR':'1','KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR':'1','KAFKA_TRANSACTION_STATE_LOG_MIN_ISR':'1','KAFKA_LOG_DIRS':'/tmp/kraft-combined-logs','KAFKA_HEAP_OPTS':'-Xms64m -Xmx256m','KAFKA_NUM_PARTITIONS':'1','KAFKA_LOG_RETENTION_BYTES':'33554432','KAFKA_LOG_SEGMENT_BYTES':'16777216'}
subprocess.run(['docker','update','--memory','2560m','--memory-swap','2560m','sf-evolution-ce-20261004'],check=True,capture_output=True)
subprocess.run(['docker','update','--memory','1792m','--memory-swap','1792m','sf-evolution-edge-20261004'],check=True,capture_output=True)
args=['docker','run','--detach','--rm','--pull','never','--platform','linux/amd64','--name',name,'--label','smartfactory.fixture=evolution-native-20261004','--network','smartfactory-evolution-native-20261004','--memory','1g','--memory-swap','1g','--cpus','2','--tmpfs','/tmp/kraft-combined-logs:rw,size=512m,uid=1000,gid=1000',*[item for key in env for item in ['--env',key]],image]
id=subprocess.check_output(args,env=dict(os.environ,**env),text=True).strip()
p=root/'.local/evolution/native-resource-repair.json';d=json.loads(p.read_text());d['resource_limits'].update(ce=2684354560,edge=1879048192,kafka=1073741824);d['kafka_recovery']={'container_id':id,'heap_max_mib':256,'log_segment_bytes':16777216,'status':'starting','at':now,'previous_temporary_queue':'previous broker container absent; prior manifest/message stores will be checked independently'}
d['failure_times_utc']=['2026-10-04T14:23:07.913Z','2026-10-04T14:23:07.987Z','2026-10-04T14:26:08.155Z','2026-10-04T14:28:04.688Z','2026-10-04T14:28:18.698Z'];p.write_text(json.dumps(d,indent=2)+'\n')
for i in range(60):
 if subprocess.run(['docker','exec',name,'bash','-ec','exec 3<>/dev/tcp/127.0.0.1/9092'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:
  d['kafka_recovery']['status']='ready';p.write_text(json.dumps(d,indent=2)+'\n');print('Kafka restored with 1GiB container and 256MiB heap; total configured native budget 6.25GiB',flush=True);break
 time.sleep(1)
else:raise RuntimeError('Kafka readiness failed')
