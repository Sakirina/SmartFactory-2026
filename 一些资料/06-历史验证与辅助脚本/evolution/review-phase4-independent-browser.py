import pathlib,json,hashlib,datetime,collections,socket
R=pathlib.Path(__file__).resolve().parents[2];E=R/'.local/evolution';B=E/'phase4-review-browser';p=B/'summary.json';d=json.loads(p.read_text());errors=[];cache={};refs=[]
def ck(path,expected,label):
 q=pathlib.Path(path);q=q if q.is_absolute() else R/q;s=str(q)
 if s not in cache:
  try:cache[s]=hashlib.sha256(q.read_bytes()).hexdigest()
  except Exception as e:cache[s]=str(e)
 refs.append((s,expected,label))
 if cache[s]!=expected:errors.append({'label':label,'path':s,'expected':expected,'actual':cache[s]})
def walk(v,label):
 if isinstance(v,dict):
  if isinstance(v.get('path'),str) and isinstance(v.get('sha256'),str):ck(v.get('preserved_path',v['path']),v['sha256'],label)
  for k,x in v.items():walk(x,label+'/'+k)
 elif isinstance(v,list):
  for i,x in enumerate(v):walk(x,label+'/'+str(i))
walk(d,'summary');idx=json.loads((B/'artifact-index.json').read_text());walk(idx,'index');source=json.loads((B/'browser-source-retention.json').read_text());walk(source,'retention')
for name in ['frozen_correspondence','r1_correspondence']:
 x=json.loads(pathlib.Path(d[name]['path']).read_text());walk(x,name)
 for item in x.get('checks',x.get('correspondence',[])):
  ck(item['own'],item['sha256'],name+'/independent-input')
 if x['mismatches']:errors.append({'correspondence':name,'mismatches':x['mismatches']})
specialist=json.loads((B/'specialist-final-audit.json').read_text());walk(specialist,'specialist-audit')
runs=json.loads((B/'browser-test-outcomes.json').read_text())['runs'];raw_runs=[];passed=set()
for run in runs:
 raw=json.loads(pathlib.Path(run['report']['path']).read_text());stats=raw['stats']
 if stats!=run['stats']:errors.append({'raw_stats_mismatch':run['name']})
 raw_runs.append({'name':run['name'],'passed':stats['expected'],'failed':stats['unexpected'],'skipped':stats['skipped'],'flaky':stats['flaky']})
 for test in run['tests']:
  if test['status']=='passed':passed.add((test['file'],test['title']))
if len(passed)!=d['unique_scenarios_with_final_pass']:errors.append({'unique_scenario_count':len(passed),'reported':d['unique_scenarios_with_final_pass']})
cleanup=json.loads(pathlib.Path(d['resource_cleanup']['path']).read_text());walk(cleanup,'cleanup')
output={'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'status':'passed' if not errors else 'failed','summary':{'path':str(p),'sha256':hashlib.sha256(p.read_bytes()).hexdigest()},'scope':'Independent final browser summary, evidence paths, original and R1 independent build inputs, specialist cross-checks and raw browser result counts','initial_audit_diagnostic':'First attempt stopped before verification because original correspondence uses correspondence while R1 uses checks; parser accepts both explicit schemas','hash_checks':len(refs),'unique_files':len(cache),'artifact_index_count':len(idx['files']),'browser_run_count':len(runs),'browser_runs':raw_runs,'unique_scenarios_with_final_pass':len(passed),'standard_frozen_scenarios_passed':d['standard_frozen_scenarios_passed'],'r1_affected_rerun_scenarios':d['r1_affected_rerun_scenarios'],'r1_empty_layouts':d['r1_empty_layout_combinations'],'r1_nonempty_layouts':d['r1_nonempty_layout_combinations'],'resource_cleanup':cleanup,'errors':errors}
(E/'phase4-independent-browser-root-review.json').write_text(json.dumps(output,ensure_ascii=False,indent=2)+'\n');print(json.dumps({k:v for k,v in output.items() if k!='resource_cleanup'},ensure_ascii=False,indent=2))
