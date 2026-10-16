#!/usr/bin/env python3
import hashlib,json,pathlib,subprocess
root=pathlib.Path('/path/to/SmartFactory-2026');out=root/'.local/evolution/ai-evidence-a02'
workspace=pathlib.Path(json.load(open(out/'final-snapshot.json'))['workspace']);base=workspace.parent
export=base/'regenerated-contracts';binary=base/'bin/sf-contracts';runner=out/'run-verification.py'
command=[str(binary),'-out',str(export/'contracts/1.0'),'-api-source',str(workspace/'Platform/internal/api'),'-examples',str(export/'examples/factory/definitions.json')]
subprocess.run(['python3',str(runner),'--cwd',str(workspace/'Platform'),'a02-contract-export','--',*command],check=True)
checks=[]
for p in sorted(export.rglob('*.json')):
 rel=p.relative_to(export);digest=hashlib.sha256(p.read_bytes()).hexdigest()
 for origin in [workspace,root]:
  other=origin/rel;actual=hashlib.sha256(other.read_bytes()).hexdigest();assert actual==digest,(str(other),actual,digest)
 checks.append({'path':str(rel),'export_path':str(p),'snapshot_path':str(workspace/rel),'current_path':str(root/rel),'sha256':digest,'bytes':p.stat().st_size})
assert len(checks)==160,len(checks)
subprocess.run(['python3',str(runner),'--cwd',str(workspace),'a02-contract-validation','--','/private/tmp/smartfactory-evolution-python/bin/python',str(workspace/'scripts/check-contracts.py')],check=True)
result={'binary':str(binary),'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'files':checks,'count':len(checks),'byte_equal_to_snapshot_and_current':True,'schema_arguments_null':'Existing required arguments permits JSON null; no contract changes'}
(out/'contract-reproduction.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({k:v for k,v in result.items() if k!='files'},ensure_ascii=False))
