#!/usr/bin/env python3
import json,pathlib
from run_review import *
for module in ['Platform','DataTransfer']:
 for source in sorted((WS/module/'cmd').glob('*/main.go')):
  name=source.parent.name;binary=BASE/'bin'/name
  run('command-build-'+name,['go','build','-p','1','-o',str(binary),'./cmd/'+name],WS/module)
target=BASE/'generated/1.0';examples=BASE/'generated/examples.json'
m=run('contract-export',[str(BASE/'bin/sf-contracts'),'-out',str(target),'-examples',str(examples)],WS/'Platform')
if m['exit_code']==0:
 pairs=[(p,ROOT/'contracts/1.0'/p.name) for p in sorted(target.iterdir()) if p.is_file()]
 pairs += [(target.parent/'compatibility.json',ROOT/'contracts/compatibility.json'),(examples,ROOT/'examples/factory/definitions.json')]
 rows=[dict(generated=ref(a),current=ref(b) if b.exists() else None,identical=b.exists() and sha(a)==sha(b)) for a,b in pairs]
 write(OUT/'contract-comparison.json',dict(rows=rows,mismatches=[r for r in rows if not r['identical']]))
 print(json.dumps({'contracts_compared':len(rows),'mismatches':len([r for r in rows if not r['identical']])}),flush=True)
run('contract-validation',['/private/tmp/smartfactory-evolution-python/bin/python','scripts/check-contracts.py'],WS)
run('release-deployment-tests',['/private/tmp/smartfactory-evolution-python/bin/python','-m','unittest','discover','-s','tests','-v'],WS)
p=next(p for p in json.loads((OUT/'test-packages.json').read_text()) if p['module']=='Platform' and p['relative']=='./internal/plugins')
test(p,'actual-hr-plugin','^TestActualOrganizationProcessPullAndCrashIsolation$',extra={'SF_HR_PLUGIN_BINARY':str(BASE/'bin/sf-hr-plugin')})
