#!/usr/bin/env python3
"""Independent phase-four verification with immutable inputs and retained attempts."""
import datetime, hashlib, json, os, pathlib, shutil, subprocess, sys, time
ROOT=pathlib.Path('/path/to/SmartFactory-2026')
OUT=ROOT/'.local/evolution/phase4-review-evidence'
BASE=pathlib.Path('/private/tmp/smartfactory-phase4-independent-20261005')
WS=BASE/'workspace'
ENV=dict(os.environ,GOTOOLCHAIN='local',GOPATH='/private/tmp/smartfactory-evolution-gopath',GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',GOCACHE='/private/tmp/smartfactory-evolution-go-cache',GOPROXY='off',GOMAXPROCS='2')
ENV['PATH']=str(ROOT/'.local/toolchains/node-v24.21.0-darwin-arm64/bin')+os.pathsep+ENV['PATH']
ENV.pop('SF_TEST_POSTGRES_DATABASE',None)
OUT.mkdir(parents=True,exist_ok=True);BASE.mkdir(parents=True,exist_ok=True)
def sha(p):return hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()
def write(p,v):pathlib.Path(p).write_text(json.dumps(v,ensure_ascii=False,indent=2)+'\n')
def ref(p):return dict(path=str(p),sha256=sha(p),bytes=pathlib.Path(p).stat().st_size)
def objects(raw):
 d=json.JSONDecoder();pos=0;result=[]
 while pos<len(raw):
  while pos<len(raw) and raw[pos].isspace():pos+=1
  if pos==len(raw):break
  item,pos=d.raw_decode(raw,pos);result.append(item)
 return result
def counts(raw):
 results={};outputs={};packages={}
 for line in raw.splitlines():
  try:e=json.loads(line)
  except ValueError:continue
  if not isinstance(e,dict):continue
  p,t,a=e.get('Package',''),e.get('Test'),e.get('Action')
  if t:
   if a=='output':outputs.setdefault((p,t),[]).append(e.get('Output',''))
   if a in ['pass','fail','skip']:results[(p,t)]=a
  elif a in ['pass','fail','skip']:packages[p]=a
 def total(v):
  v=list(v);return {a:v.count(a) for a in ['pass','fail','skip']}
 return dict(top_level=total(v for (p,t),v in results.items() if '/' not in t),leaf=total(v for (p,t),v in results.items() if not any(p==p2 and t2.startswith(t+'/') for p2,t2 in results)),all_named=total(results.values()),packages=total(packages.values()),skips=[dict(package=p,test=t,reason=''.join(outputs.get((p,t),[]))) for (p,t),v in results.items() if v=='skip'],failures=[dict(package=p,test=t,output=''.join(outputs.get((p,t),[]))) for (p,t),v in results.items() if v=='fail'],races=raw.count('WARNING: DATA RACE'))
def run(label,cmd,cwd,pg=False,timeout=900,extra=None):
 env=ENV.copy();env.update(extra or {});secrets=[]
 if pg:
  env['SF_TEST_POSTGRES_DATABASE']=json.loads((ROOT/'.local/evolution/pg18-test.json').read_text())['dsn'];secrets.append(env['SF_TEST_POSTGRES_DATABASE'])
  from urllib.parse import urlparse
  if urlparse(secrets[0]).password:secrets.append(urlparse(secrets[0]).password)
 env['SF_AI_EVIDENCE_ARTIFACT_DIR']=str(OUT/'captures'/label);pathlib.Path(env['SF_AI_EVIDENCE_ARTIFACT_DIR']).mkdir(parents=True,exist_ok=True)
 start=datetime.datetime.now(datetime.timezone.utc).isoformat();then=time.monotonic()
 try:
  p=subprocess.run(cmd,cwd=cwd,env=env,capture_output=True,text=True,errors='replace',timeout=timeout);code,stdout,stderr=p.returncode,p.stdout,p.stderr
 except subprocess.TimeoutExpired as e:
  code,stdout,stderr=124,e.stdout or '',e.stderr or ''
  if isinstance(stdout,bytes):stdout=stdout.decode(errors='replace')
  if isinstance(stderr,bytes):stderr=stderr.decode(errors='replace')
 for secret in secrets:stdout=stdout.replace(secret,'<redacted>');stderr=stderr.replace(secret,'<redacted>')
 out=OUT/(label+'.stdout.log');err=OUT/(label+'.stderr.log');out.write_text(stdout);err.write_text(stderr)
 m=dict(label=label,command=cmd,cwd=str(cwd),started_at=start,completed_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),elapsed_seconds=round(time.monotonic()-then,3),exit_code=code,postgres=pg,stdout=ref(out),stderr=ref(err),counts=counts(stdout))
 if (OUT/'build-inputs.json').exists():m['build_inputs']=ref(OUT/'build-inputs.json')
 if '-overlay' in cmd:
  overlay=pathlib.Path(cmd[cmd.index('-overlay')+1]);m['overlay']=ref(overlay);m['overlay_inputs']=[dict(virtual_path=k,**ref(v)) for k,v in json.loads(overlay.read_text())['Replace'].items() if v]
  for item in m['overlay_inputs']:
   target=BASE/'overlays'/label/pathlib.Path(item['path']).name;target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(item['path'],target);item['preserved_path']=str(target)
 for part in cmd:
  if part.startswith(str(BASE/'bin')) and pathlib.Path(part).is_file():m['binary']=ref(part)
 write(OUT/(label+'.metadata.json'),m)
 print(json.dumps({k:m[k] for k in ['label','exit_code','elapsed_seconds']}|{'top_level':m['counts']['top_level']},ensure_ascii=False),flush=True)
 return m
def prepare():
 if WS.exists():raise RuntimeError('Refusing to replace existing snapshot')
 WS.mkdir()
 files=[]
 for directory in ['Platform','DataTransfer','contracts','scripts','examples','tests','deploy']:
  for p in (ROOT/directory).rglob('*'):
   if not p.is_file() or any(x in p.parts for x in ['.git','.local','.cache','.tools','node_modules','__pycache__','output'] ) or p.name=='.DS_Store':continue
   rel=p.relative_to(ROOT);dest=WS/rel;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(p,dest)
   files.append(dict(path=str(rel),original_path=str(p),preserved_path=str(dest),sha256=sha(dest),bytes=dest.stat().st_size))
 write(OUT/'snapshot.json',dict(created_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),workspace=str(WS),files=files))
 print(json.dumps({'snapshot_files':len(files),'bytes':sum(x['bytes'] for x in files)}))
def capture():
 inputs={};specs=[];packages=[];generated=[]
 for module in ['Platform','DataTransfer']:
  for race in [False,True]:
   target=['./...'] if not race else (['./internal/application','./internal/api','./internal/store','./internal/ai','./internal/cloudsync','./internal/app','./internal/thingsboard'] if module=='Platform' else ['./internal/config','./internal/configmanager'])
   label='list-'+module+('-race' if race else '')
   cmd=['go','list','-p','1']+(['-race'] if race else [])+['-deps','-test','-json']+target
   m=run(label,cmd,WS/module);assert m['exit_code']==0
   pkgs=objects(pathlib.Path(m['stdout']['path']).read_text());specs.append(dict(label=label,metadata=ref(OUT/(label+'.metadata.json')),packages=len(pkgs)))
   for p in pkgs:
    assert not p.get('Error') and not p.get('DepsErrors'),p
    if not race and not p.get('DepOnly') and not p.get('ForTest') and (p.get('TestGoFiles') or p.get('XTestGoFiles')) and '[' not in p['ImportPath']:
     packages.append(dict(module=module,import_path=p['ImportPath'],directory=p['Dir'],relative='./'+str(pathlib.Path(p['Dir']).relative_to(WS/module))))
    for field in ['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','SFiles','SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles','TestEmbedFiles','XTestEmbedFiles']:
     for name in p.get(field,[]):
      path=pathlib.Path(name)
      if not path.is_absolute():path=pathlib.Path(p.get('Dir',WS/module))/path
      if not path.is_file():generated.append(str(path));continue
      inputs[str(path)]=path
    for mod in [p.get('Module') or {},(p.get('Module') or {}).get('Replace') or {}]:
     if mod.get('GoMod') and pathlib.Path(mod['GoMod']).exists():inputs[mod['GoMod']]=pathlib.Path(mod['GoMod'])
 for module in ['Platform','DataTransfer']:
  for n in ['go.mod','go.sum']:inputs[str(WS/module/n)]=WS/module/n
 rows=[]
 for key,p in sorted(inputs.items()):
  item=ref(p);item['local']=p.is_relative_to(WS)
  dest=p if item['local'] else BASE/'external-inputs'/str(p).lstrip('/')
  if not item['local']:dest.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(p,dest)
  item['preserved_path']=str(dest);rows.append(item)
 write(OUT/'build-inputs.json',dict(specifications=specs,files=rows,generated_inputs=sorted(set(generated)),go_binary=ref(pathlib.Path(shutil.which('go')).resolve()),go_version=subprocess.check_output(['go','version'],env=ENV,text=True).strip()))
 write(OUT/'test-packages.json',packages);print(json.dumps({'inputs':len(rows),'test_packages':len(packages)}))
def binary_for(p,race=False):return BASE/'bin'/(p['module']+'-'+p['relative'].replace('./','').replace('/','-')+('.race' if race else '')+'.test')
def build(p,race=False):
 b=binary_for(p,race);b.parent.mkdir(exist_ok=True);label='build-'+b.stem
 m=run(label,['go','test','-p','1']+(['-race'] if race else [])+['-c','-o',str(b),p['relative']],WS/p['module']);return m,b
def test(p,label,pattern='.',pg=False,race=False,extra=None,timeout='240s'):
 b=binary_for(p,race)
 if not b.exists():
  m,b=build(p,race)
  if m['exit_code']:return m
 return run(label,['go','tool','test2json','-t','-p',p['import_path'],str(b),'-test.v=test2json','-test.count=1','-test.parallel=1','-test.timeout='+timeout,'-test.run='+pattern],pathlib.Path(p['directory']),pg=pg,extra=extra)
def execute(mode):
 pkgs=json.loads((OUT/'test-packages.json').read_text())
 for p in pkgs:
  rel=p['relative'];module=p['module']
  if mode=='ordinary':test(p,'ordinary-'+module+'-'+rel.replace('./','').replace('/','-'))
  elif mode=='postgres' and module=='Platform' and rel in ['./internal/store','./internal/tasks','./internal/api','./internal/application','./internal/compiledplan','./internal/control','./internal/cloudsync','./internal/ai']:
   test(p,'postgres-'+rel.rsplit('/',1)[-1],r'^Test(.*Postgres.*|QueryAllKindsStableKeyset)$',pg=True)
  elif mode=='race' and module=='Platform' and rel in ['./internal/application','./internal/api','./internal/store','./internal/ai','./internal/cloudsync','./internal/app','./internal/thingsboard']:
   test(p,'race-'+rel.rsplit('/',1)[-1],r'^Test(Query|BusinessConcurrent|BusinessConnectorSecrets|BusinessWorkOrderRejects|BusinessHandoverReviewed|BusinessNative|Investigation|ControlReceiptEvidence|PostgresControlEvidence)',pg=True,race=True,timeout='300s')
  elif mode=='race' and module=='DataTransfer' and rel in ['./internal/config','./internal/configmanager']:
   test(p,'race-dt-'+rel.rsplit('/',1)[-1],r'^Test(BusinessConnector|ConcurrentPushes|TLSJSON|ConfigurationAndRevision)',race=True)
if __name__=='__main__':
 mode=sys.argv[1]
 if mode=='prepare':prepare();capture()
 elif mode=='capture':capture()
 else:execute(mode)
