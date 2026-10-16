import json,datetime,socket,os
from pathlib import Path
from urllib.parse import urlparse
import run_review as R
root,out=R.ROOT,R.OUT

def ref(p):
 p=Path(p);a=p if p.is_absolute() else root/p
 return {'path':str(p),'sha256':R.sha(a),'bytes':a.stat().st_size}
def own(name):return ref((out/name).relative_to(root))
def load(name):return json.loads((out/name).read_text())

with socket.socket() as s:closed=s.connect_ex(('127.0.0.1',19062))!=0
assert closed
cleanup={'recorded_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'port':19062,'port_closed':closed,'owned_pid':load('browser-process-t01.json')['history']['pid'],'stop_method':'SIGTERM after exact ps command match, natural River stop','browser_session':'sf-phase3-independent-t01-history','browser_closed':True,'history_log':own('history-browser-t01.log'),'browser_log':own('browser-cli-t01.log'),'retained_directory':str(R.TMP/'history-browser-t01'),'other_services_touched':False}
R.json_write(out/'t01-cleanup.json',cleanup)
secrets=REPLACE_WITH_LOCAL_VALUE
for p in R.TMP.glob('**/private-*.json'):
 private.append(str(p))
 def walk(v):
  if isinstance(v,dict):
   for k,w in v.items():
    if k in ['password','token','cloud_token','edge_token'] and isinstance(w,str):secrets.add(w)
    else:walk(w)
  elif isinstance(v,list):
   for w in v:walk(w)
 walk(json.loads(p.read_text()))
pg=json.loads((root/'.local/evolution/pg18-test.json').read_text())['dsn'];secrets.add(pg)
if urlparse(pg).password:secrets.add(urlparse(pg).password)
files=[p for p in out.rglob('*') if p.is_file() and '__pycache__' not in p.parts]
files += list((root/'output/playwright/phase3-independent').glob('*.png'))
files += list((root/'.playwright-cli').glob('page-*.yml'))
matches=[]
for p in files:
 data=p.read_bytes()
 if any(secret.encode() in data for secret in secrets if secret):matches.append(str(p.relative_to(root)))
assert not matches,matches
R.json_write(out/'t01-credential-scan.json',{'recorded_at':cleanup['recorded_at'],'files_checked':len(files),'private_input_files':private,'known_secret_values':len(secrets),'matching_paths':matches,'credentials_or_tokens_in_report':False})
old=root/'.local/evolution/phase3-independent-review-checkpoint.json'
oldIndex=load('evidence-index-waiting-t01.json');oldmismatches=[x['path'] for x in oldIndex['files'] if R.sha(root/x['path'])!=x['sha256']]
assert not oldmismatches,oldmismatches
cmds=[]
for p in sorted(out.glob('t01-*.metadata.json')):
 m=json.loads(p.read_text());cmds.append({'label':m['label'],'metadata':own(p.name),'exit_code':m['exit_code'],'counts':m['counts'],'source_changed_during_command':m['changed_sources'],'expected_failure':m['label']=='t01-retry-independent-reproduction'})
record={'schema_version':1,'recorded_at':cleanup['recorded_at'],'status':'independent_checkpoint_waiting_for_P3-T02','final_acceptance':False,'previous_checkpoint':ref(old.relative_to(root)),'previous_index':own('evidence-index-waiting-t01.json'),'previous_evidence_files_unchanged':len(oldIndex['files']),'first_cancel_result':'Original task operation version remained identical for 42.33s while actual River availability/running/Snooze state continued. Ordinary page confirm returned200; one cancel audit and retained run/snapshot/zero steps were read afterward. Default one-second page GET refresh was still active in this first cycle.','single_read_attempt':'Set visible page refresh selector to manual, retried same task, then requested exactly one detail GET. The subsequent cancel button was unavailable because P3-T02 had moved the queue to cancelled while business status was waiting_parent. No success is attributed to this unfinished single-read cancel.','new_finding':own('independent-findings-through-t02.json'),'new_test_provenance':own('t01-specialist-independent-audit.json'),'binary':own('t01-binary-independent-audit.json'),'runtime':own('browser-process-t01.json'),'commands':cmds,'frontend_static_bundle':'main-CiVE3bj4.js','frontend_static_sha256':'f02c8f65921cac0ef95ce2e8f7d001188e11b60c8e89c35215ac4c625b0dc9cd','contracts':own('t01-contract-byte-comparison.json'),'task_audit':own('t01-task-action-audit.json'),'final_fixture_state':own('t01-before-cleanup-backend.json'),'cleanup':own('t01-cleanup.json'),'credential_scan':own('t01-credential-scan.json'),'work_volume_free_bytes':os.statvfs(root).f_bavail*os.statvfs(root).f_frsize,'helper_attempts':['Browser launch required authorized sandbox escalation for the dedicated Playwright daemon cache','An initial auxiliary metadata query used previous fixture River26 and cannot describe this new fixture River24; t01-river-before-confirmation.json is retained but excluded from conclusions','Python raw metadata decode failed on SQLite JSONB; subsequent read uses json(metadata) and actual task River24','Header selector click was obstructed by the open drawer; drawer close followed by ordinary header selection enabled manual querying','The second single-read cancellation flow stopped when no cancel button was present; actual queue cancellation after retry is preserved as P3-T02'],'next':'Original task specialist repairs retry cancellation marker. Re-audit new build and run new isolated directory, single detail GET then >=3s normal confirmation; retry survives further production snoozes and can cancel again, while old versions and permissions retain conflicts. Final report must cover seven E3 requirements and all fifteen coordinator findings.'}
record['resume_t02']=own('t02-resume-instructions.json')
paths=[p for p in out.rglob('*') if p.is_file() and '__pycache__' not in p.parts and p.name!='evidence-index-through-t01.json']+list((root/'output/playwright/phase3-independent').glob('*'))
index={'recorded_at':cleanup['recorded_at'],'files':[ref(p.relative_to(root)) for p in sorted(set(paths)) if p.is_file()]}
R.json_write(out/'evidence-index-through-t01.json',index);record['evidence_index']=own('evidence-index-through-t01.json')
target=root/'.local/evolution/phase3-independent-review-t01-checkpoint.json';R.json_write(target,record)
print(json.dumps({'checkpoint':str(target),'sha256':R.sha(target),'commands':len(cmds),'expected_failure_commands':sum(x['expected_failure'] for x in cmds),'indexed_files':len(index['files']),'original_index_unchanged':len(oldIndex['files']),'port_closed':closed,'credential_matches':len(matches)},ensure_ascii=False))
