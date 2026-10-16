import datetime,hashlib,json,os,pathlib,subprocess,sys
root=pathlib.Path(sys.argv[1]).resolve();out=pathlib.Path(sys.argv[2]);native=len(sys.argv)>3 and sys.argv[3]=='native'
env=dict(os.environ,GOTOOLCHAIN='local',GOPATH='/private/tmp/smartfactory-evolution-gopath',GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',GOCACHE='/private/tmp/smartfactory-evolution-go-cache',GOPROXY='off',GOMAXPROCS='2')
groups={'Platform':['./internal/api']} if native else {'Platform':['./internal/store','./internal/application','./internal/api','./internal/cloudsync','./internal/deviceconfig','./internal/thingsboard','./internal/app','./internal/scenetemplates','./cmd/sf-cloud','./cmd/sf-edge','./cmd/sf-contracts','./cmd/sf-business-fixture'],'DataTransfer':['./internal/config','./internal/configmanager']}
started=datetime.datetime.now(datetime.timezone.utc).isoformat();goroot=pathlib.Path(subprocess.check_output(['go','env','GOROOT'],env=env,text=True).strip()).resolve();inputs={};packages=[];commands=[]
for module,targets in groups.items():
 cmd=['go','list','-deps','-test','-json',*targets];commands.append({'module':module,'argv':cmd});r=subprocess.run(cmd,cwd=root/module,env=env,capture_output=True,text=True)
 if r.returncode:print(r.stderr[-3000:]);raise SystemExit(r.returncode)
 decoder=json.JSONDecoder();text=r.stdout
 while text.strip():
  v,end=decoder.raw_decode(text.lstrip());packages.append(v);text=text.lstrip()[end:]
for pkg in packages:
 d=pathlib.Path(pkg.get('Dir','.'));mod=pkg.get('Module') or {}
 for field in ['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','FFiles','SFiles','SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles']:
  for name in pkg.get(field,[]):
   p=(d/name).resolve()
   if not p.is_file():continue
   local=p.is_relative_to(root)
   if local:label=str(p.relative_to(root))
   elif p.is_relative_to(goroot):label='toolchain/'+str(p.relative_to(goroot))
   elif mod.get('Dir') and p.is_relative_to(pathlib.Path(mod['Dir'])):label='module/'+mod['Path']+'@'+mod.get('Version','local')+'/'+str(p.relative_to(pathlib.Path(mod['Dir'])))
   else:label='generated/'+hashlib.sha256(str(p).encode()).hexdigest()+'/'+p.name
   raw=p.read_bytes();v=inputs.setdefault(label,{'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw),'local':local,'roles':[],'packages':[]})
   if field not in v['roles']:v['roles'].append(field)
   if pkg['ImportPath'] not in v['packages']:v['packages'].append(pkg['ImportPath'])
for label in ['Platform/go.mod','Platform/go.sum','DataTransfer/go.mod','DataTransfer/go.sum','scripts/check-contracts.py','docs/evolution/business-frontend-contract.md']:
 p=root/label
 if p.exists():raw=p.read_bytes();inputs[label]={'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw),'local':True,'roles':['module-lock' if p.name in ['go.mod','go.sum'] else 'verification'],'packages':[]}
manifest={'captured_start':started,'captured_end':datetime.datetime.now(datetime.timezone.utc).isoformat(),'root':str(root),'commands':commands,'go_version':subprocess.check_output(['go','version'],env=env,text=True).strip(),'input_count':len(inputs),'local_input_count':sum(v['local'] for v in inputs.values()),'package_count':len(packages),'inputs':inputs}
out.parent.mkdir(parents=True,exist_ok=True);out.write_text(json.dumps(manifest,indent=2,sort_keys=True)+'\n');print(json.dumps({k:manifest[k] for k in ['root','input_count','local_input_count','package_count']}))
