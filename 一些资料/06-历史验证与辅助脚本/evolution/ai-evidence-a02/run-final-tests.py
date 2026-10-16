#!/usr/bin/env python3
import hashlib,json,os,pathlib,subprocess
root=pathlib.Path('/path/to/SmartFactory-2026')
out=root/'.local/evolution/ai-evidence-a02'
workspace=pathlib.Path(json.loads((out/'final-snapshot.json').read_text())['workspace'])
binary_dir=workspace.parent/'bin';runner=out/'run-verification.py';results=[]
def verify(label,name,race=False,postgres=False,pattern=None,expected=0):
 binary=binary_dir/(name+('.race.test' if race else '.test'))
 command=['go','tool','test2json','-t','-p','competition2026/product/platform/internal/'+name,str(binary),'-test.v','-test.count=1','-test.parallel=1','-test.timeout=8m']
 if pattern:command.append('-test.run='+pattern)
 env=dict(os.environ);env.pop('SF_TEST_POSTGRES_DATABASE',None)
 if postgres:env['SF_TEST_POSTGRES_DATABASE']=json.loads((root/'.local/evolution/pg18-test.json').read_text())['dsn']
 completed=subprocess.run(['python3',str(runner),'--cwd',str(workspace/'Platform/internal'/name),label,'--',*command],env=env)
 metadata=json.loads((out/(label+'.metadata.json')).read_text())
 results.append({'label':label,'race':race,'postgres':postgres,'binary':str(binary),'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'metadata':str(out/(label+'.metadata.json')),'exit_code':completed.returncode,'expected_exit_code':expected,'expected_failure_reason':'Original v3 counts JSON null as four retained parameter bytes' if expected else None,'top_level':metadata['top_level'],'leaf':metadata['leaf']})
 (out/'final-test-runs.json').write_text(json.dumps(results,ensure_ascii=False,indent=2)+'\n')
 if completed.returncode!=expected:raise SystemExit('Unexpected test exit: '+label)
verify('a02-fixed-independent-v2','a02reviewv2',pattern='^TestIndependentInvestigationDeletedOriginalAsset$')
verify('a02-fixed-independent-v3-original-null-assertion','a02reviewv3',pattern='^TestIndependentInvestigationDeletedOriginalAsset$',expected=1)
verify('a02-fixed-independent-v3-null-corrected','a02reviewv3corrected',pattern='^TestIndependentInvestigationDeletedOriginalAsset$')
for name in ['ai','application']:verify('a02-fixed-ordinary-'+name,name)
verify('a02-fixed-postgres-ai','ai',postgres=True,pattern='^TestInvestigationPostgres')
for name in ['ai','application']:verify('a02-fixed-race-'+name,name,race=True,pattern='^TestInvestigation')
print(json.dumps({'test_runs':len(results),'manifest':str(out/'final-test-runs.json'),'sha256':hashlib.sha256((out/'final-test-runs.json').read_bytes()).hexdigest()}))
