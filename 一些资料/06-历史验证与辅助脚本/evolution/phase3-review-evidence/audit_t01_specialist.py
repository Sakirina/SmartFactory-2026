import json,hashlib,datetime,shutil,sys
from pathlib import Path
import run_review as R
out=R.OUT;root=R.ROOT
original=root/'.local/evolution/tasks-cancel-t01-verified.json';frozen=out/'initial-specialist-reports/tasks-cancel-t01-verified.json'
if not frozen.exists():shutil.copyfile(original,frozen)
report=json.loads(frozen.read_text())
def path(value):
 p=Path(value);return p if p.is_absolute() else root/p
def ref(value):
 p=path(value);return {'path':str(value),'sha256':R.sha(p),'bytes':p.stat().st_size}
refs=[]
def walk(value,where='$'):
 if isinstance(value,dict):
  if isinstance(value.get('path'),str) and isinstance(value.get('sha256'),str):
   p=path(value['path']);refs.append({'at':where,**value,'exists':p.is_file(),'matches':p.is_file() and R.sha(p)==value['sha256']})
  for k,v in value.items():walk(v,where+'.'+k)
 elif isinstance(value,list):
  for i,v in enumerate(value):walk(v,where+'['+str(i)+']')
walk(report)
source=json.loads(path(report['verification']['source_before_commands']['path']).read_text())['source_sha256']
source_rows=[{'path':p,'expected_sha256':v,'matches':path(p).exists() and R.sha(path(p))==v} for p,v in source.items()]
protected=[{'path':v['path'],'matches':R.sha(path(v['path']))==v['before_sha256']==v['after_sha256']} for v in report['provenance']['preserved_migration_compatibility_and_original_reports']]
counts=[]
for c in report['verification']['checks']:
 if not c.get('counts'):continue
 raw=path(c['evidence']['path']).read_text();actual=R.counts(raw)
 comparison={k:actual[k]==v for k,v in c['counts'].items()}
 counts.append({'name':c['name'],'actual_counts':actual,'reported_counts':c['counts'],'comparisons':comparison,'matches':all(comparison.values()),'source':ref(c['evidence']['path'])})
observations=[]
for value in report['verification']['observed_confirmation_and_progress']:
 c=next(c for c in report['verification']['checks'] if c['name']==value['run'])
 events=[]
 for line in path(c['evidence']['path']).read_text().splitlines():
  try:events.append(json.loads(line))
  except ValueError:pass
 text=''.join(e.get('Output','') for e in events if e.get('Test')==value['test'])
 observations.append({**value,'found_in_decoded_raw_log':value['observed'] in text})
result={'recorded_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'specialist_report':ref(str(frozen.relative_to(root))),'typed_refs':refs,'typed_ref_count':len(refs),'reference_mismatches':[r for r in refs if not r['matches']],'source_count':len(source_rows),'source_checks':source_rows,'source_mismatches':[r for r in source_rows if not r['matches']],'protected':protected,'counts':counts,'observations':observations,'current_binary_independent_audit':ref(str((out/'t01-binary-independent-audit.json').relative_to(root))),'scope_qualification':'These verify provenance and actual T01 command results. They do not accept retry behavior: independent browser and stronger SQLite assertion found P3-T02 after this specialist report.','source_test_gap':'waiting_parent_delayed_cancel_and_retry checks business state and revision after retry but does not check the River row remains active after JobSnooze'}
R.json_write(out/'t01-specialist-independent-audit.json',result)
print(json.dumps({'typed_refs':len(refs),'mismatches':len(result['reference_mismatches']),'source':len(source_rows),'source_mismatches':len(result['source_mismatches']),'protected':len(protected),'counted_commands':len(counts),'counts_match':all(c['matches'] for c in counts),'observations_match':all(v['found_in_decoded_raw_log'] for v in observations)},ensure_ascii=False))
