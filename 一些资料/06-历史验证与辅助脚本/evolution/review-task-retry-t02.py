#!/usr/bin/env python3
"""Coordinator evidence audit; does not run or alter product code."""
import hashlib, importlib.util, json, tarfile
from pathlib import Path
from datetime import datetime, timezone
ROOT=Path(__file__).resolve().parents[2]
def loadmod(name,path):
 s=importlib.util.spec_from_file_location(name,ROOT/path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
a=loadmod('audit','.local/evolution/phase2-review-evidence/audit_evidence.py')
h=loadmod('counts','.local/evolution/review-task-cancel-t01.py')
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def path(p):return Path(p) if Path(p).is_absolute() else ROOT/p
def read(p):return json.loads(path(p).read_text())
r=read('.local/evolution/tasks-retry-t02-verified.json')
refs=a.audit_report('tasks-retry-t02-verified.json')
checks=[];decoded={};issues=[]
expected_failure={'storage-t02-before-repair.jsonl','storage-t02-sqlite-first.jsonl'}
for c in r['verification']['checks']:
 raw=path(c['evidence']['path']).read_text();rec={'name':c['name'],'exit_code':c['exit_code'],'expected_exit_code':1 if c['name'] in expected_failure else 0,'evidence_sha256_matches':digest(path(c['evidence']['path']))==c['evidence']['sha256']}
 if 'counts' in c:
  cc,ev=h.count_log(c['evidence']['path']);actual={n:cc[o] for n,o in {'top_level_tests':'top_level','leaf_tests':'leaf_cases','all_named_tests':'all_named_cases','packages':'packages'}.items()}
  actual['package_elapsed_seconds']=round(sum(e.get('Elapsed',0) for e in ev if e.get('Action') in ('pass','fail','skip') and not e.get('Test')),3)
  actual['data_race_reports']=raw.count('WARNING: DATA RACE');rec.update(actual_counts=actual,counts_match=actual==c['counts'])
  decoded[c['name']]=''.join(e.get('Output','') for e in ev)
 else:decoded[c['name']]=raw
 if rec['exit_code']!=rec['expected_exit_code'] or not rec['evidence_sha256_matches'] or rec.get('counts_match') is False:issues.append(c['name'])
 checks.append(rec)
obs=r['verification']['observed_measurements']+r['reproduction_and_first_failures']['failures']
unmatched=[{'run':x['run'],'test':x['test']} for x in obs if x['observed'] not in decoded[x['run']]]
sources=a.compare(read(r['verification']['final_source']['path'])['sha256'])
protected=r['provenance']['protected_migrations_compatibility_and_previous_reports']
protected_diff=[x['path'] for x in protected if not(x['before_sha256']==x['after_sha256']==digest(path(x['path'])))]
contracts=a.compare({x['path']:x['sha256'] for x in r['provenance']['contract_files_unchanged']})
front=a.compare({x['path']:x['sha256'] for x in r['provenance']['frontend_source_and_dist_unchanged']})
m=read(r['binary_handoff']['manifest']['path']);binaries={};union={}
for name,b in m['binaries'].items():
 bi=a.compare(b['build_inputs']);union.update(b['build_inputs'])
 binaries[name]={'inputs':bi,'binary_sha256_matches':digest(path(b['binary']['path']))==b['binary']['sha256'],'exit_code':b['exit_code'],'changed_during_build':b['changed_during_build']}
archive=r['binary_handoff']['combined_source_archive'];archive_diffs=[]
with tarfile.open(path(archive['path'])) as t:
 members=[v for v in t.getmembers() if v.isfile()]
 for item in members:
  expected=union.get(item.name,{}).get('sha256');actual=hashlib.sha256(t.extractfile(item).read()).hexdigest()
  if actual!=expected:archive_diffs.append(item.name)
 if {x.name for x in members}!=set(union):archive_diffs.append('archive_member_set')
problems=bool(refs['different'] or refs['missing_references'] or issues or unmatched or sources['different'] or protected_diff or contracts['different'] or front['different'] or archive_diffs or any(v['inputs']['different'] or not v['binary_sha256_matches'] or v['exit_code'] or v['changed_during_build'] for v in binaries.values()))
out={'created_at':datetime.now(timezone.utc).isoformat(),'status':'differences_require_review' if problems else 'passed_specialist_evidence_review','scope':'T02 specialist report, actual source, raw command counts, preserved T01 reports and current binary provenance; final independent phase acceptance is separate','report':{'path':'.local/evolution/tasks-retry-t02-verified.json','sha256':digest(path('.local/evolution/tasks-retry-t02-verified.json'))},'references':refs,'commands':checks,'command_issues':issues,'observations':{'checked':len(obs),'unmatched':unmatched},'final_source':sources,'protected':{'checked':len(protected),'different':protected_diff},'contracts':contracts,'frontend':front,'binaries':binaries,'archive':{'members':len(members),'combined_inputs':len(union),'different':archive_diffs},'cleanup':r['cleanup'],'browser_review':'.local/evolution/tasks-retry-t02-browser-root-review.json'}
path('.local/evolution/tasks-retry-t02-root-review.json').write_text(json.dumps(out,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({k:v for k,v in out.items() if k not in ('cleanup','commands')},ensure_ascii=False,indent=2))
print(json.dumps({'commands':checks},ensure_ascii=False))
raise SystemExit(1 if problems else 0)
