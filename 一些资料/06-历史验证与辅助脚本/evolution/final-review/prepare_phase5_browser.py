#!/usr/bin/env python3
"""Prepare isolated formal objects and parameterize retained browser checks."""
from pathlib import Path
import copy,datetime,hashlib,json,os,secrets,shutil,subprocess,time,urllib.request,urllib.error
ROOT=Path('/path/to/SmartFactory-2026')
BASE=Path('/private/tmp/smartfactory-final-independent-20261007')
OUT=BASE/'phase5-browser';OUT.mkdir(exist_ok=True);(OUT/'private').mkdir(mode=0o700,exist_ok=True)
FRONT=BASE/'frontend/Frontends'
records=[]
def clean(v):
 if isinstance(v,dict):return {k:('[private]' if k.lower() in ['credential','password','token','authorization','api_key','private_key'] else clean(x)) for k,x in v.items()}
 if isinstance(v,list):return [clean(x) for x in v]
 return v
def save(p,v,private=False):
 p.write_text(json.dumps(v,ensure_ascii=False,indent=2)+'\n')
 if private:p.chmod(0o600)
def req(base,path,body=None,token='',method=None,raw=None,record=True):
 data=raw if raw is not None else json.dumps(body,ensure_ascii=False).encode() if body is not None else None
 call=urllib.request.Request(base+'/api/sf/v1'+path,data=data,method=method or ('POST' if data is not None else 'GET'),headers={'Content-Type':'application/octet-stream' if raw is not None else 'application/json',**({'Authorization':'REDACTED'+token} if token else {})})
 try:
  with urllib.request.urlopen(call,timeout=30) as r:status,received=r.status,r.read()
 except urllib.error.HTTPError as e:status,received=e.code,e.read()
 value=json.loads(received) if received else None
 if record:
  records.append({'base':base,'path':path,'method':call.method,'status':status,'response':clean(value) if path!='/login' else '[private]'});save(OUT/'formal-http-second.json',records)
 if status not in (200,202):raise RuntimeError(path+' returned '+str(status)+'; see retained response')
 return value
node=json.loads((BASE/'node-runtime-v2/frontend.json').read_text());ncred=json.loads(Path(node['credentials_file']).read_text());nurl=node['cloud_url'];nt=req(nurl,'/login',ncred)['token']
if req(nurl,'/releases',token=nt)!=[]:raise RuntimeError('Initial release directory is not empty')
binary=BASE/'release-runtime/bin/sf-edge-v3';build=json.loads(subprocess.check_output([str(binary),'-build-info'],text=True))
assert any(item['sha256'] == build['sha256'] for item in req(nurl,'/release-artifacts',token=nt))
entity={'id':'final-first-device','kind':'device','name':'独立首次发布设备','parent_id':'factory','status':'approved','version':1}
edge_url=node['edge_urls']['edge-a'];edge_token=req(edge_url,'/login',ncred)['token']
req(edge_url,'/entities',{'entity':entity,'expected_version':0},edge_token)
for attempt in range(60):
 if any(item['id']==entity['id'] for item in req(nurl,'/entities',token=nt,record=False)):break
 time.sleep(0.25)
else:raise RuntimeError('The formally registered device did not synchronize to the cloud')
definition={'id':'frontend-initial-rule','name':'独立首次发布规则','schema_version':'1.0','kind':'analysis','group_id':'factory','selector':{'device_ids':[entity['id']],'keys':['temperature']},'nodes':[{'id':'input','type':'input','params':{'key':'temperature'}}],'outputs':[{'node_id':'input','key':'result','type':'number','unit':'C'}]}
draft=req(nurl,'/drafts',{'draft':{'id':'final-first-draft','definition':definition},'expected_version':0},nt)
validation=req(nurl,'/drafts/final-first-draft/validate',{'expected_version':draft['version']},nt)
if not validation.get('valid'):raise RuntimeError('Initial rule validation failed')
rule=req(nurl,'/drafts/final-first-draft/publish',{'expected_version':draft['version']},nt)
if req(nurl,'/releases',token=nt)!=[]:raise RuntimeError('Preparing prerequisites created an unexpected release')
save(OUT/'first-release-prerequisites.json',{'program':build,'rule':rule,'empty_releases':True,'node_fixture':str(BASE/'node-runtime-v2/frontend.json')})
release=json.loads((BASE/'release-runtime/frontend-fixture.json').read_text());rurl=release['cloud_url'];rp=Path(release['credentials_file']).read_text().strip();rt=req(rurl,'/login',{'login':'admin','password':rp})['token']
release['deployment']={'id':'deploy-template-evolution'};release['navigation_other_deployment']='deploy-rollback';release['final_agent_binary']=str(BASE/'release-runtime/bin/sf-release-agent')
save(OUT/'release-fixture.json',release)
editor_password=REPLACE_WITH_LOCAL_VALUE
editor=req(rurl,'/users',{'user':{'id':'independent-release-editor','login':'independent-release-editor','name':'独立发布编辑验证','active':True,'roles':['engineer'],'resources':['factory']},'password':editor_password,'expected_version':0},rt)
editor_fixture=dict(release,credentials_file=str(editor_file),login='independent-release-editor');save(OUT/'editor-fixture.json',editor_fixture)
releases=json.loads((BASE/'release-runtime/evidence/releases.json').read_text());save(OUT/'normal-final-release.state.json',releases['v3'])
replacements={
 'phase5-release.spec.ts':[('deploy-validation-a01-b','deploy-template-evolution'),('release-validation-a01-b','release-v3'),('phase5-v3-a01','phase5-v3')],
 'phase5-permissions.spec.ts':[('deploy-validation-a01-b','deploy-template-evolution'),('release-validation-a01-b','release-v3')],
 'phase5-first-publish.spec.ts':[('phase5-v3-a01','phase5-v3')],
 'phase5-navigation.spec.ts':[("'/private/tmp/smartfactory-release-restore-recovery-20261005/frontend-fixture.json'","process.env.SF_PHASE5_RELEASE_FIXTURE!"),("'recovery-r02-original-program-failure-fourth'","fixture.navigation_other_deployment")],
 'phase5-pipeline.spec.ts':[("'b02566d928858835502d2d0cd6aac04ef561335e0498f7675fb1d60b0e71f229'","normal.manifest.components.find((component: any) => component.kind === 'program').sha256")],
 'phase5-node.spec.ts':[("'http://127.0.0.1:60904/runtime'",json.dumps(node['runtime_urls']['edge-a']+'/runtime')),("'http://127.0.0.1:60905/runtime'",json.dumps(node['runtime_urls']['edge-b']+'/runtime')),("signalFixture(configurationPID, 'sf-config'","signalFixture(configurationPID, nodeProcessRoot + '/private/center.json'")],
 'phase5-credentials.spec.ts':[("'http://127.0.0.1:60904/runtime'",json.dumps(node['runtime_urls']['edge-a']+'/runtime'))],
}
patches=[];archive=OUT/'original-browser-checks';archive.mkdir()
for name,changes in replacements.items():
 p=FRONT/'tests'/name;before=p.read_text();shutil.copy2(p,archive/name);after=before
 for old,new in changes:
  if old not in after:raise RuntimeError('Missing expected parameterization in '+name+': '+old)
  after=after.replace(old,new)
 p.write_text(after)
 patches.append({'path':str(p),'source_before':str(archive/name),'before_sha256':hashlib.sha256(before.encode()).hexdigest(),'after_sha256':hashlib.sha256(after.encode()).hexdigest(),'changes':[{'before':a,'after':b} for a,b in changes]})
save(OUT/'browser-parameterization.json',{'purpose':'Bind checks to this independent fixture, exact program digest and own process identities','patches':patches})
save(OUT/'ready.json',{'status':'ready','at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'release_fixture':str(OUT/'release-fixture.json'),'editor_fixture':str(OUT/'editor-fixture.json'),'node_fixture':str(BASE/'node-runtime-v2/frontend.json'),'release_cloud':rurl,'release_edge':release['node_urls']['a'],'node_cloud':nurl,'node_edge':node['edge_urls']['edge-a'],'program_sha256':build['sha256']})
print(json.dumps({'status':'ready','formal_requests':len(records),'parameterized_check_files':len(patches),'directory':str(OUT)}))
