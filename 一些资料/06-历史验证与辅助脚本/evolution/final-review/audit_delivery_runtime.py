#!/usr/bin/env python3
import collections,datetime,hashlib,json
from pathlib import Path
BASE=Path('/private/tmp/smartfactory-final-independent-20261007')
DELIVERY=Path('/private/tmp/smartfactory-delivery-final-20261007')
RUNTIME=Path('/private/tmp/smartfactory-delivery-final-runtime-20261007/verification.json')
OUT=BASE/'package-audit'
cache={}
def sha(p):
 p=Path(p)
 if str(p) not in cache:
  h=hashlib.sha256()
  with p.open('rb') as s:
   for part in iter(lambda:s.read(1048576),b''):h.update(part)
  cache[str(p)]=h.hexdigest()
 return cache[str(p)]
def ref(p):
 p=Path(p);return {'path':str(p),'sha256':sha(p),'bytes':p.stat().st_size}
def read(p):return json.loads(Path(p).read_text())
r=read(RUNTIME);s=read(DELIVERY/'static-verification-license-complete/verification.json');i=read(DELIVERY/'retained-final-build-inputs.json');manifest=read(DELIVERY/'bundle/release-manifest.json')
assert r['status']=='passed' and r['all_checks_passed'] and len(r['checks'])==55 and all(x['passed'] for x in r['checks'])
assert s['passed']
assert r['bundle_manifest_sha256']==sha(DELIVERY/'bundle/release-manifest.json')
assert i['source_sha256']==manifest['source_sha256'] and i['before_after_equal']
inputs=[]
for row in i['files']+i['additional_toolchain_files']:
 fixed=sha(row['fixed_path']);current=sha(row['original_path'])
 inputs.append({'original_path':row['original_path'],'fixed_path':row['fixed_path'],'expected_sha256':row['sha256'],'fixed_matched':fixed==row['sha256'],'current_matched':current==row['sha256']})
assert all(x['fixed_matched'] and x['current_matched'] for x in inputs)
refs=[];seen=set()
def walk(value,source):
 if isinstance(value,list):
  for x in value:walk(x,source)
 elif isinstance(value,dict):
  for key in ['path','log','fixed_path']:
   if isinstance(value.get(key),str) and isinstance(value.get('sha256'),str) and len(value['sha256'])==64:
    p=Path(value[key]);pair=(str(p),value['sha256'])
    if p.is_file() and pair not in seen:
     seen.add(pair);refs.append({'source':source,'path':str(p),'expected_sha256':value['sha256'],'matched':sha(p)==value['sha256']})
  for x in value.values():walk(x,source)
walk(r,str(RUNTIME));walk(s,str(DELIVERY/'static-verification-license-complete/verification.json'))
assert all(x['matched'] for x in refs)
commands=[{'name':c['name'],'exit_code':c['exit_code'],'timed_out':c.get('timed_out',False),'log':ref(c['log'])} for c in r['commands']]
nonzero=[x for x in commands if x['exit_code']!=0]
assert all(not x['timed_out'] for x in commands)
assert len(nonzero)==3 and all(x['name'].startswith(('gateway-loopback-readiness','managed-runtime-loopback')) for x in nonzero)
assert all('Connection refused' in Path(x['log']['path']).read_text() for x in nonzero)
assert all(not x['Running'] and x['ExitCode']==0 and not x['OOMKilled'] for x in r['final_states'].values())
pg=next(x for x in commands if x['name']=='final-own-database-connections');assert Path(pg['log']['path']).read_text().strip()=='--- stderr ---'
checks={x['name']:x for x in r['checks']}
v1=checks['actual-bundle-program-and-fixed-consumer-version-one']['detail'];v2=checks['actual-consumer-and-parameter-version-two']['detail']
assert v1['runtime']['program_sha256']==manifest['files']['bin/sf-edge']['sha256']==v2['runtime']['program_sha256']
assert v1['configurations'][0]['reference']['version']==1 and v2['configurations'][0]['reference']['version']==2
assert v1['configurations'][0]['consumer_digest']!=v2['configurations'][0]['consumer_digest']
report={'status':'passed','created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'runtime':ref(RUNTIME),'static':ref(DELIVERY/'static-verification-license-complete/verification.json'),'retained_build_inputs':ref(DELIVERY/'retained-final-build-inputs.json'),'input_checks':inputs,'retained_input_count':len(i['files']),'additional_toolchain_files':len(i['additional_toolchain_files']),'structured_file_hash_checks':refs,'structured_hash_count':len(refs),'runtime_check_count':55,'runtime_checks':[{'name':x['name'],'passed':x['passed']} for x in r['checks']],'commands':commands,'command_count':len(commands),'nonzero_readiness_probes':nonzero,'readiness_probe_explanation':'Three bounded wget probes returned connection refused during startup; later probes on the same gateway/runtime observation route exited 0 before the corresponding actual-consumer checks. No command timed out.','http_status_counts':dict(collections.Counter(str(x.get('status',x.get('error_type','unknown'))) for x in r['http'])),'http_scope':'Initial login/health RemoteDisconnected observations precede readiness; the two 409 responses preserve actual business-version conflicts; source-unavailable credentials returned 503, and the external HTTP runtime route returned the expected 403 with a service token before authenticated container-loopback runtime observation. The final functional checks retain their own results.','final_service_states':r['final_states'],'database_connections_zero_log':pg['log'],'finite_workload':r['finite_workload'],'fixed_consumers':{'v1':v1,'v2':v2},'source_sha256':manifest['source_sha256'],'scope':'Final Linux amd64 package, independent PostgreSQL cloud/config/edge databases, one managed Edge plus actual DataTransfer and protocol simulator; independent two-node batch/browser behavior is covered by the review own SQLite composition','driver':ref(__file__)}
(OUT/'runtime-evidence-audit.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'status':report['status'],'input_checks':len(inputs),'file_hash_checks':len(refs),'commands':len(commands),'ready_probe_nonzero':len(nonzero),'checks':55,'services':len(r['final_states'])}))
