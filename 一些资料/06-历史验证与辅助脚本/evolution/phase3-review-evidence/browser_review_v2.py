#!/usr/bin/env python3
"""Isolated current-product browser fixtures and credential-safe CLI evidence."""
import importlib.util,json,os,shutil,signal,socket,subprocess,sys,time,urllib.request
from pathlib import Path
OUT=Path(__file__).resolve().parent
sp=importlib.util.spec_from_file_location('review',OUT/'run_review.py');R=importlib.util.module_from_spec(sp);sp.loader.exec_module(R)
ROOT,TMP=R.ROOT,R.TMP
CLI='/home/developer/.codex/skills/playwright/scripts/playwright_cli.sh'
R.ENV['npm_config_cache']='/private/tmp/smartfactory-evolution-npm-cache'
CONTROL=TMP/'control-browser-v2'
HISTORY=TMP/'history-browser-v2'
STATIC=TMP/'browser-static-v2'

def access(mode):
 if mode=='history':return json.loads((HISTORY/'private-credentials.json').read_text())
 return json.loads((CONTROL/'runtime/private-access.json').read_text())
def scrub(raw):
 for mode in ['cloud','history']:
  try:d=access(mode)
  except FileNotFoundError:continue
  def walk(value):
   nonlocal raw
   if isinstance(value,dict):
    for k,v in value.items():
     if k in ['password','token','cloud_token','edge_token'] and isinstance(v,str):raw=raw.replace(v,'<redacted>')
     else:walk(v)
   elif isinstance(value,list):
    for v in value:walk(v)
  walk(d)
 return raw

def cli(mode,args,quiet=False):
 result=subprocess.run([CLI,'--session','sf-phase3-independent-'+mode,*args],cwd=ROOT,env=R.ENV,capture_output=True,text=True)
 raw=scrub(result.stdout+result.stderr)
 safe=[*args[:2],'<value>'] if args[:1]==['fill'] else args
 with (OUT/'browser-cli-v2.log').open('a') as f:f.write(json.dumps({'session':mode,'arguments':safe,'exit_code':result.returncode},ensure_ascii=False)+'\n'+raw+'\n')
 if not quiet:print(raw)
 if result.returncode:raise SystemExit(result.returncode)
 return raw

def capture(mode,label):
 code="async page => { await page.screenshot({path:"+json.dumps('output/playwright/phase3-independent/'+label+'.png')+",fullPage:true}); return {url:page.url(),text:await page.locator('body').innerText(),capture:await page.evaluate(()=>window.__phase3 ?? {requests:[],errors:[]})}; }"
 raw=cli(mode,['run-code',code],quiet=True)
 result=json.loads(raw.split('### Result\n',1)[1].split('\n###',1)[0])
 result['recorded_at_ms']=int(time.time()*1000)
 R.json_write(OUT/(label+'.json'),result)
 print(json.dumps({'label':label,'characters':len(result['text']),'requests':len(result['capture']['requests']),'page_errors':result['capture']['errors']}))
 return result

def backend(mode,path,method='GET',body=None):
 credentials=access(mode)
 if mode=='history':
  base='http://127.0.0.1:19061'
  if not credentials.get('token'):
   login=urllib.request.Request(base+'/api/sf/v1/login',method='POST',headers={'Content-Type':'application/json'},data=json.dumps({'login':credentials['login'],'password':credentials['password']}).encode())
   with urllib.request.urlopen(login,timeout=15) as response:credentials['token']=json.loads(response.read())['token']
   (HISTORY/'private-credentials.json').write_text(json.dumps(credentials));(HISTORY/'private-credentials.json').chmod(0o600)
  token=credentials['token']
 else:base,token=credentials[mode+'_url'],credentials[mode+'_token']
 request=urllib.request.Request(base+'/api/sf/v1'+path,method=method,headers={'Content-Type':'application/json','Authorization':'REDACTED'+token},data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None)
 with urllib.request.urlopen(request,timeout=15) as response:return response.status,json.loads(response.read())

def start():
 if CONTROL.exists() or HISTORY.exists():raise RuntimeError('Use clean isolated fixture directories')
 with socket.socket() as sock:sock.bind(('127.0.0.1',19061))
 previous=Path('/private/tmp/sf-control-c05-final-20261005')
 manifest=json.loads((previous/'manifest.json').read_text())
 inputs=json.loads(Path(manifest['build_inputs']['path']).read_text())
 mismatches=[v['path'] for v in inputs if R.sha(v['path'])!=v['sha256']]
 for v in manifest['binaries'].values():
  if R.sha(v['path'])!=v['sha256']:mismatches.append(v['path'])
 if mismatches:raise RuntimeError('Control fixture source/binary changed: '+str(mismatches))
 historyReport=json.loads((ROOT/'.local/evolution/history-analysis-verified.json').read_text())
 historyBinary=historyReport['fixture_binary']
 if R.sha(historyBinary['path'])!=historyBinary['sha256']:raise RuntimeError('History binary changed')
 shutil.copytree(ROOT/'Frontends/dist',STATIC)
 CONTROL.mkdir(mode=0o700);(CONTROL/'runtime').mkdir(mode=0o700)
 env=R.ENV.copy();env.update(SF_CONTROL_DEVICE_BINARY=manifest['binaries']['device']['path'],SF_CONTROL_BROWSER_FIXTURE=str(CONTROL/'runtime'),SF_CONTROL_STATIC_DIR=str(STATIC))
 controlArgv=[manifest['binaries']['platform']['path'],'-test.run=^TestControlBrowserFixture$','-test.timeout=2h','-test.v']
 with (OUT/'control-browser-v2.log').open('w') as log:control=subprocess.Popen(controlArgv,cwd=ROOT/'Platform',env=env,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
 historyArgv=[historyBinary['path'],'-dir',str(HISTORY),'-address','127.0.0.1:19061','-static',str(STATIC)]
 with (OUT/'history-browser-v2.log').open('w') as log:history=subprocess.Popen(historyArgv,cwd=ROOT,env=R.ENV,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
 record={'started_at_ms':int(time.time()*1000),'control':{'pid':control.pid,'argv':controlArgv,'existing_binary_manifest':str(previous/'manifest.json'),'compiled_inputs_current_matches':len(inputs)},'history':{'pid':history.pid,'argv':historyArgv,'binary':historyBinary},'static_files':{str(p.relative_to(STATIC)):R.sha(p) for p in STATIC.rglob('*') if p.is_file()},'frontend_source':R.snapshot(['Frontends'])}
 R.json_write(OUT/'browser-process-v2.json',record)
 for _ in range(300):
  if control.poll() is not None or history.poll() is not None:raise RuntimeError('Browser fixture terminated during startup; inspect logs')
  if (CONTROL/'runtime/browser-ready.json').exists() and (HISTORY/'manifest.json').exists():break
  time.sleep(.1)
 else:raise RuntimeError('Browser fixture startup timeout')
 ready=json.loads((CONTROL/'runtime/browser-ready.json').read_text());ready={k:v for k,v in ready.items() if not k.endswith('file')}
 record['control']['ready']=ready;record['history']['ready']={'base_url':'http://127.0.0.1:19061'}
 R.json_write(OUT/'browser-process-v2.json',record)
 print(json.dumps({'control':ready,'history':record['history']['ready'],'pids':[control.pid,history.pid]},ensure_ascii=False))

if __name__=='__main__':
 if sys.argv[1]=='start':start()
 elif sys.argv[1]=='login':
  mode=sys.argv[2];data=access(mode)
  cli(mode,['fill',sys.argv[3],data.get('login',data.get('username'))],quiet=True)
  cli(mode,['fill',sys.argv[4],data['password']],quiet=True)
  cli(mode,['click',sys.argv[5]])
  for path in (ROOT/'.playwright-cli').glob('page-*.yml'):
   content=path.read_text()
   if data['password'] in content:path.write_text(content.replace(data['password'],'<redacted>'))
 elif sys.argv[1]=='cli':cli(sys.argv[2],sys.argv[3:])
 elif sys.argv[1]=='access-shape':
  def shape(v):return {k:shape(x) for k,x in v.items()} if isinstance(v,dict) else [shape(x) for x in v] if isinstance(v,list) else type(v).__name__
  print(json.dumps(shape(access(sys.argv[2]))))
