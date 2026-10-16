import datetime,hashlib,json,os,pathlib,shutil,subprocess
repo=pathlib.Path('/path/to/SmartFactory-2026');evidence=repo/'.local/evolution/query-evidence'
env=os.environ.copy();env.update(GOTOOLCHAIN='local',GOPATH='/private/tmp/smartfactory-evolution-gopath',GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',GOCACHE='/private/tmp/smartfactory-evolution-go-cache',GOPROXY='off',GOMAXPROCS='2')
packages=['./internal/store','./internal/application','./internal/api','./internal/cloudsync','./internal/control','./internal/app','./cmd/sf-cloud','./cmd/sf-edge','./cmd/sf-contracts']
started=datetime.datetime.now(datetime.timezone.utc).isoformat();cmd=['go','list','-deps','-test','-json',*packages];r=subprocess.run(cmd,cwd=repo/'Platform',env=env,capture_output=True,text=True)
if r.returncode: print(r.stderr[-3000:]);raise SystemExit(r.returncode)
(evidence/'final-go-list.jsonstream').write_text(r.stdout)
decoder=json.JSONDecoder();text=r.stdout;pkgs=[]
while text.strip(): value,end=decoder.raw_decode(text.lstrip());pkgs.append(value);text=text.lstrip()[end:]
version=subprocess.check_output(['go','version'],cwd=repo/'Platform',env=env,text=True).strip();goroot=pathlib.Path(subprocess.check_output(['go','env','GOROOT'],env=env,text=True).strip());inputs={};actual={}
for pkg in pkgs:
 d=pathlib.Path(pkg.get('Dir','.'));module=pkg.get('Module') or {}
 for field in ['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','FFiles','SFiles','SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles']:
  for name in pkg.get(field,[]):
   p=d/name
   if not p.is_file():continue
   p=p.resolve();local=p.is_relative_to(repo)
   if local:label=str(p.relative_to(repo))
   elif p.is_relative_to(goroot):label='toolchain/'+str(p.relative_to(goroot))
   elif module.get('Dir') and p.is_relative_to(pathlib.Path(module['Dir'])):label='module/'+module['Path']+'@'+module.get('Version','local')+'/'+str(p.relative_to(pathlib.Path(module['Dir'])))
   else:label='generated/'+hashlib.sha256(str(p).encode()).hexdigest()+'/'+p.name
   data=p.read_bytes();entry=inputs.setdefault(label,{'sha256':hashlib.sha256(data).hexdigest(),'bytes':len(data),'local':local,'roles':[],'packages':[]})
   if field not in entry['roles']:entry['roles'].append(field)
   if pkg['ImportPath'] not in entry['packages']:entry['packages'].append(pkg['ImportPath'])
   actual[label]=p
for name in ['Platform/go.mod','Platform/go.sum','DataTransfer/go.mod','DataTransfer/go.sum','scripts/check-contracts.py','docs/evolution/query-frontend-contract.md']:
 p=repo/name
 if p.exists():data=p.read_bytes();inputs[name]={'sha256':hashlib.sha256(data).hexdigest(),'bytes':len(data),'local':True,'roles':['module' if p.suffix in ('.mod','.sum') else 'verification-contract'],'packages':[]};actual[name]=p
stamp=datetime.datetime.now().strftime('%Y%m%d-%H%M%S');frozen=pathlib.Path('/private/tmp')/('smartfactory-query-final-'+stamp);frozen.mkdir()
for label,p in actual.items():
 if inputs[label]['local']:
  target=frozen/label;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(p,target)
changed=[];copy_mismatch=[]
for label,p in actual.items():
 if hashlib.sha256(p.read_bytes()).hexdigest()!=inputs[label]['sha256']:changed.append(label)
 if inputs[label]['local'] and hashlib.sha256((frozen/label).read_bytes()).hexdigest()!=inputs[label]['sha256']:copy_mismatch.append(label)
manifest={'captured_start':started,'captured_end':datetime.datetime.now(datetime.timezone.utc).isoformat(),'snapshot':str(frozen),'toolchain':version,'command':cmd,'package_count':len(pkgs),'input_count':len(inputs),'local_input_count':sum(x['local'] for x in inputs.values()),'changed_during_capture':changed,'copy_mismatches':copy_mismatch,'inputs':inputs}
raw=json.dumps(manifest,indent=2,sort_keys=True)+'\n';(frozen/'input-manifest.json').write_text(raw);(evidence/'final-input-manifest.json').write_text(raw)
(evidence/'final-snapshot-path.txt').write_text(str(frozen)+'\n')
print(json.dumps({k:manifest[k] for k in ['snapshot','package_count','input_count','local_input_count','changed_during_capture','copy_mismatches']},ensure_ascii=False))
if changed or copy_mismatch:raise SystemExit(1)
