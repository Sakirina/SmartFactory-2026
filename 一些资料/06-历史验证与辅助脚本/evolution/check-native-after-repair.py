import json,pathlib,time,urllib.request,urllib.error,subprocess
root=pathlib.Path('/path/to/SmartFactory-2026');work=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004');creds=json.loads((work/'development-credentials.json').read_text());native=json.loads((work/'tb-bootstrap.json').read_text());e={'schema_version':1,'profiles':[]}
def req(base,path,value=None,token=None,tb=False):
 headers={'Content-Type':'application/json'}
 if token:headers['X-Authorization' if tb else 'Authorization']='Bearer '+token
 request=urllib.request.Request(base+path,data=None if value is None else json.dumps(value).encode(),headers=headers)
 with urllib.request.urlopen(request,timeout=20) as response:return json.load(response)
for kind,port,tbport in [('ce',19990,19080),('edge',19991,19081)]:
 sf='http://127.0.0.1:'+str(port);tb='http://127.0.0.1:'+str(tbport)
 for attempt in range(120):
  try:
   native_token=req(tb,'/api/auth/login',{'username':native['username'],'password':native['password']})['token'];break
  except (urllib.error.URLError,TimeoutError):time.sleep(1)
 else:raise RuntimeError(kind+' native not ready after repair')
 token=req(sf,'/api/sf/v1/login',{'login':'admin','password':creds['password']})['token']
 old=json.loads((root/('.local/evolution/native-'+kind+'-roundtrip.json')).read_text())['profiles'][0];mid=old['message_id'];timestamp=int(mid.rsplit('-',1)[1])
 batch={'message_id':mid,'source_id':'edge-a','critical':True,'event':{'kind':'native_verification'},'points':[{'device_id':'climate-1','key':'temperature','value':22.5,'observed_ms':timestamp,'quality':'GOOD','time_source':'simulation'}]}
 repeated=req(sf,'/api/sf/v1/ingest',batch,creds['service_token']);assert repeated['duplicate']
 data=req(sf,f'/api/sf/v1/data?device_ids=climate-1&from_ms={timestamp}&to_ms={timestamp}&limit=100',token=token)
 keys={p['key'] for p in data['points']};assert {'temperature','climate-average.temperature'}<=keys
 audit=req(sf,'/api/sf/v1/audit/verify',{},token=token);assert audit['valid'] and not audit['issues']
 entities=req(sf,'/api/sf/v1/entities',token=token);entity=next(v for v in entities if v['id']=='climate-1')
 timeseries=req(tb,f"/api/plugins/telemetry/DEVICE/{entity['tb_id']}/values/timeseries?keys=temperature,climate-average.temperature&startTs={timestamp-1}&endTs={timestamp+1}&agg=NONE&limit=100",token=native_token,tb=True)
 assert timeseries.get('temperature') and timeseries.get('climate-average.temperature')
 e['profiles'].append({'name':kind,'prior_message_id':mid,'duplicate_preserved':True,'platform_prior_observations':data['points'],'tb_prior_telemetry':timeseries,'audit':audit,'passed':True})
 print(kind+' prior message, duplicate, native timeseries and audit retained after resource repair',flush=True)
(root/'.local/evolution/native-post-resource-verification.json').write_text(json.dumps(e,indent=2)+'\n')
subprocess.run(['python3',str(root/'scripts/check-native.py'),'--state-directory',str(work),'--cloud-url','http://127.0.0.1:19990','--edge-url','http://127.0.0.1:19991','--tb-cloud-url','http://127.0.0.1:19080','--tb-edge-url','http://127.0.0.1:19081','--output',str(root/'.local/evolution/native-full-roundtrip-after-repair.json')],check=True)
