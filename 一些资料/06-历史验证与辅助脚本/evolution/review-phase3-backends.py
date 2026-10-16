import collections
import datetime
import hashlib
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('prior_audit', ROOT/'.local/evolution/phase2-review-evidence/audit_evidence.py')
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)

def read(path):
    p=Path(path)
    return json.loads((p if p.is_absolute() else ROOT/p).read_text())

def verify_rows(rows):
    checked=[]
    for row in rows:
        p=Path(row.get('path',row.get('binary_path','')))
        if not p.is_absolute(): p=ROOT/p
        expected=row.get('sha256')
        if not expected: continue
        actual=hashlib.sha256(p.read_bytes()).hexdigest() if p.is_file() else None
        checked.append({'path':str(p),'expected':expected,'actual':actual,'matched':actual==expected})
    return {'count':len(checked),'matched':sum(x['matched'] for x in checked),'different':[x for x in checked if not x['matched']]}

def check_command(row):
    p=ROOT/row['log']['path']
    if p.suffix!='.jsonl': return None
    counts={x:0 for x in ('pass','fail','skip')}
    subs=counts.copy(); names={x:[] for x in counts}; packages=[]
    for line in p.read_text().splitlines():
        try: event=json.loads(line)
        except ValueError: continue
        action=event.get('Action'); test=event.get('Test')
        if action not in counts: continue
        if test:
            names[action].append(test)
            target=subs if '/' in test else counts
            target[action]+=1
        else: packages.append({'package':event.get('Package'),'action':action})
    checks={}
    for field,action in [('passed_tests_and_subtests','pass'),('skipped_tests','skip'),('failed_tests','fail')]:
        if field in row:
            expected=list(names[action])
            if field=='failed_tests' and any(value in {p['package'] for p in packages} for value in row[field]):
                expected.extend(p['package'] for p in packages if p['action']=='fail')
            checks[field]=collections.Counter(row[field])==collections.Counter(expected)
    for field,actual in [('top_level',counts),('subtests',subs)]:
        if field in row: checks[field]=row[field]==actual
    return {'path':str(p.relative_to(ROOT)),'top_level':counts,'subtests':subs,'reported_counts_match':checks,'package_results':packages,'failed_tests':names['fail']}

for name,output_name in [('control-execution-verified.json','control-execution-root-review.json'),('history-analysis-verified.json','history-analysis-root-review.json')]:
    d=read('.local/evolution/'+name)
    references=audit.audit_report(name)
    if name.startswith('control'):
        source_rows=read(d['source']['final_test_and_source_manifest']['path'])
        binaries=list(d['source']['binaries'].values())
        commands=d['commands']
        compiled=read(d['source']['compiled_inputs']['path'])
    else:
        source=read(d['source_manifest']['path'])
        source_rows=[]
        for key in ('production_inputs','test_and_fixture_inputs','module_lock_inputs','generated_contract_inputs','documentation_inputs'):
            source_rows.extend(source[key])
        binaries=[d['fixture_binary']]+d['tested_binaries']
        commands=d['checks']; compiled=None
    result={'checked_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'status':'backend_handoff_evidence_checked_phase_verification_pending','reference_audit':references,'current_source':verify_rows(source_rows),'binaries':verify_rows(binaries),'commands':[c for r in commands if (c:=check_command(r)) is not None],'phase_status':'Frontend implementation and independent behavioral review are in progress'}
    if compiled is not None: result['compiled_inputs']=verify_rows(compiled)
    (ROOT/'.local/evolution'/output_name).write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({'report':output_name,'references':{'checked':references['hashed_references'],'different':references['different'],'missing':references['missing_references']},'source':result['current_source'],'binaries':result['binaries'],'compiled_inputs':result.get('compiled_inputs'),'commands':len(result['commands']),'count_mismatches':[x for x in result['commands'] if not all(x['reported_counts_match'].values())]},ensure_ascii=False))
