import datetime,json,os,pathlib,signal,subprocess,time
root=pathlib.Path('/path/to/SmartFactory-2026');work=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004');record=root/'.local/evolution/service-cleanup.json';e={'schema_version':1,'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'scope':'Only fixtures created for this evolution verification. PostgreSQL18 remains running for other engineers.','steps':[],'retained':['smartfactory-evolution-pg18-20261004',str(work),'/private/tmp/smartfactory-evolution-build'],'original_persistent_containers':'Nine existing stopped containers preserved'}
def save():record.write_text(json.dumps(e,indent=2)+'\n')
for mode in ['cloud','edge']:
 j=json.loads((root/('.local/evolution/native-'+mode+'-process.json')).read_text());pid=j['pid'];p=subprocess.run(['ps','-p',str(pid),'-o','command='],capture_output=True,text=True);command=p.stdout.strip();expected=str(work/('sf-'+mode))
 if p.returncode==0 and command.split()[0]==expected:
  os.kill(pid,signal.SIGTERM)
  for i in range(50):
   if subprocess.run(['kill','-0',str(pid)],capture_output=True).returncode:break
   time.sleep(.1)
  ended=subprocess.run(['kill','-0',str(pid)],capture_output=True).returncode!=0;e['steps'].append({'name':'native-'+mode,'pid':pid,'signal':'SIGTERM','ended':ended})
 else:e['steps'].append({'name':'native-'+mode,'pid':pid,'status':'already_ended' if p.returncode else 'ownership_mismatch_not_stopped'})
 save()
for name in ['sf-evolution-edge-20261004','sf-evolution-kafka-20261004']:
 p=subprocess.run(['docker','inspect',name],capture_output=True,text=True)
 if p.returncode:e['steps'].append({'name':name,'status':'already_absent'});save();continue
 j=json.loads(p.stdout)[0]
 if j['Config']['Labels'].get('smartfactory.fixture')!='evolution-native-20261004':raise RuntimeError('fixture label mismatch')
 logs=subprocess.run(['docker','logs',name],capture_output=True,text=True);log=work/(name+'-final.log');log.write_text(logs.stdout+logs.stderr);log.chmod(0o600)
 p=subprocess.run(['docker','stop','--time','30',name],capture_output=True,text=True);e['steps'].append({'name':name,'container_id':j['Id'],'stop_exit_code':p.returncode,'log':str(log),'auto_remove':j['HostConfig']['AutoRemove']});save()
network='smartfactory-evolution-native-20261004';p=subprocess.run(['docker','network','inspect',network],capture_output=True,text=True)
if p.returncode==0:
 j=json.loads(p.stdout)[0]
 if j.get('Containers'):e['steps'].append({'name':network,'status':'retained_nonempty','container_ids':list(j['Containers'])})
 else:
  p=subprocess.run(['docker','network','rm',network],capture_output=True,text=True);e['steps'].append({'name':network,'remove_exit_code':p.returncode})
pg=json.loads((root/'.local/evolution/pg18-test.json').read_text());p=subprocess.run(['docker','inspect',pg['container']],capture_output=True,text=True);j=json.loads(p.stdout)[0];e['postgres_retained']={'container':pg['container'],'running':j['State']['Running'],'container_id':j['Id'],'memory_limit_bytes':j['HostConfig']['Memory'],'credentials_file':'.local/evolution/pg18-test.json','credential_mode':oct((root/'.local/evolution/pg18-test.json').stat().st_mode&0o777)}
e['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();save();print('Owned native fixtures ended; PostgreSQL18 retained',flush=True)
