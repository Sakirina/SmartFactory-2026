#!/usr/bin/env python3
"""Coordinator audit of the fixed third-phase independent review."""
from pathlib import Path
import json,hashlib,socket,datetime,importlib.util
R=Path(__file__).resolve().parents[2]
def mod(n,p):
 s=importlib.util.spec_from_file_location(n,R/p);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
a=mod('audit','.local/evolution/phase2-review-evidence/audit_evidence.py')
h=mod('counts','.local/evolution/review-task-cancel-t01.py')
def path(p):return Path(p) if Path(p).is_absolute() else R/p
def read(p):return json.loads(path(p).read_text())
def sha(p):return hashlib.sha256(path(p).read_bytes()).hexdigest()
r=read('.local/evolution/phase3-independent-review.json');refs=a.audit_report('phase3-independent-review.json')
index=read(r['evidence_index']['path']);indexed=a.compare({v['path']:v['sha256'] for v in index['files']})
commands=[];issues=[]
for c in r['verification_commands']:
 m=read(c['metadata']['path']);rec={'label':c['label'],'exit_code':c['exit_code'],'expected_exit':1 if c['label']=='t01-retry-independent-reproduction' else 0,'metadata_matches':all(m[k]==c[k] for k in ('label','argv','exit_code','counts','stdout','stderr')),'logs_match':all(sha(c[k]['path'])==c[k]['sha256'] for k in ('stdout','stderr')),'source_changed':m['changed_sources']}
 if isinstance(c['counts'],dict) and c['counts'].get('top_level') is not None:
  actual,ev=h.count_log(c['stdout']['path']);rec['raw_counts']=actual;rec['counts_match']=all(actual[k]==c['counts'][k] for k in actual)
  raw=path(c['stdout']['path']).read_text()+path(c['stderr']['path']).read_text();rec['data_race_report']='WARNING: DATA RACE' in raw
 if rec['exit_code']!=rec['expected_exit'] or not rec['metadata_matches'] or not rec['logs_match'] or rec['source_changed'] or rec.get('counts_match') is False or rec.get('data_race_report'):issues.append(c['label'])
 commands.append(rec)
cleanup=read(r['cleanup']['path']);ports=[]
for v in cleanup['ports']:
 with socket.socket() as s:s.settimeout(.3);closed=s.connect_ex(('127.0.0.1',v['port']))!=0
 ports.append({'port':v['port'],'closed':closed})
source_review=read('.local/evolution/phase3-final-source-root-review.json')
source_ref_match=all(sha(source_review[k]['path'])==source_review[k]['sha256'] for k in ('source_manifest','compiled_manifest'))
expected_ids={v['id'] for v in read('.local/evolution/phase3-coordinator-findings.json')['items']}
requirements_ok={v['id'] for v in r['requirements']}=={f'E3-{i:02d}' for i in range(1,8)} and all(v['status']=='independently_verified' for v in r['requirements'])
findings_ok={v['id'] for v in r['findings']}==expected_ids and all(v['status']=='independently_verified' for v in r['findings'])
scan=read(r['credential_scan']['path']);credential_scan_ok=scan['status']=='passed' and not scan['matching_paths'] and scan['known_private_values']>0
problems=bool(refs['different'] or refs['missing_references'] or indexed['different'] or issues or not all(v['closed'] for v in ports) or not cleanup['browser_closed'] or not source_ref_match or source_review['status']!='passed' or not requirements_ok or not findings_ok or r['unresolved_findings'] or r['status']!='verified' or not r['independent_review_complete'] or not credential_scan_ok)
result={'recorded_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'status':'differences_require_review' if problems else 'passed_independent_phase_review','independent_report':{'path':'.local/evolution/phase3-independent-review.json','sha256':sha('.local/evolution/phase3-independent-review.json')},'references':refs,'evidence_index':indexed,'commands':commands,'command_issues':issues,'requirements':{'checked':len(r['requirements']),'identities_and_status_match':requirements_ok},'findings':{'checked':len(r['findings']),'identities_and_status_match':findings_ok,'unresolved':r['unresolved_findings']},'source_provenance':{'review':'.local/evolution/phase3-final-source-root-review.json','sha256':sha('.local/evolution/phase3-final-source-root-review.json'),'fixed_source_manifests_still_match':source_ref_match},'owned_ports':ports,'browser_closed':cleanup['browser_closed'],'credential_scan':{'verified_report':credential_scan_ok,'files_checked':scan['files_checked'],'known_private_values':scan['known_private_values'],'matches':len(scan['matching_paths'])},'scope':'The seven third-phase objectives and all fifteen findings are supported by specialist evidence, independently run current combination checks, browser scenarios, fixed source and binary provenance, and coordinator verification. Historical expected failure remains preserved and is not counted as a current failure.'}
path('.local/evolution/phase3-independent-root-review.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({k:v for k,v in result.items() if k!='commands'},ensure_ascii=False,indent=2))
print(json.dumps({'command_records':len(commands),'raw_Go_counts_checked':sum('raw_counts' in c for c in commands),'expected_historical_failures':sum(c['expected_exit']==1 for c in commands)},ensure_ascii=False))
raise SystemExit(1 if problems else 0)
