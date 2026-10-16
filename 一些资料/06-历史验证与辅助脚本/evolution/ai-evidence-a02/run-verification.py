#!/usr/bin/env python3
import argparse, datetime, hashlib, json, os, pathlib, subprocess, time
parser=argparse.ArgumentParser()
parser.add_argument('label');parser.add_argument('--cwd',default='Platform');parser.add_argument('command',nargs=argparse.REMAINDER)
a=parser.parse_args();root=pathlib.Path(__file__).resolve().parents[3]
out=root/'.local/evolution/ai-evidence-a02';out.mkdir(exist_ok=True)
log=out/(a.label+'.jsonl');errlog=out/(a.label+'.stderr.log')
def sources():
    paths=[]
    for directory in ['Platform','DataTransfer']:
        paths += [p for p in (root/directory).rglob('*.go') if not any(part in p.parts for part in ['.local','.cache','.git','vendor','node_modules'])]
        paths += [root/directory/'go.mod',root/directory/'go.sum']
    paths += list((root/'contracts').rglob('*.json'))+list((root/'contracts').rglob('*.ts'))
    paths += [root/'Platform/pkg/compatibility/manifest.json',root/'Frontends/package.json',root/'Frontends/package-lock.json',root/'Frontends/src/generated-routes.ts',root/'Frontends/scripts/generate-api.mjs']
    return {str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(set(paths)) if p.is_file()}
env=dict(os.environ,GOTOOLCHAIN='local',GOPATH='/private/tmp/smartfactory-evolution-gopath',GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',GOCACHE='/private/tmp/smartfactory-evolution-go-cache',GOPROXY='off',GOMAXPROCS='2')
env["SF_AI_EVIDENCE_ARTIFACT_DIR"]=str(out/"captures"/a.label);(out/"captures"/a.label).mkdir(parents=True,exist_ok=True)
input_path=out/'final-build-inputs.json'
input_manifest=json.loads(input_path.read_text()) if input_path.exists() and str(root/a.cwd).startswith('/private/tmp/smartfactory-ai-a02-final-') else None
def input_changes():
    if input_manifest is None:return None
    return [item['path'] for item in input_manifest['files'] if not pathlib.Path(item['path']).is_file() or hashlib.sha256(pathlib.Path(item['path']).read_bytes()).hexdigest()!=item['sha256']]
inputs_before=input_changes()
before=sources();started=datetime.datetime.now(datetime.timezone.utc).isoformat();start=time.monotonic();command=a.command
if command and command[0]=='--':command=command[1:]
with log.open('w') as stdout,errlog.open('w') as stderr:
    process=subprocess.run(command,cwd=root/a.cwd,env=env,stdout=stdout,stderr=stderr)
after=sources();records=[]
for line in log.read_text().splitlines():
    try:records.append(json.loads(line))
    except json.JSONDecodeError:pass
outcomes=[v for v in records if v.get('Action') in ['pass','fail','skip'] and v.get('Test')]
paths={v['Package']+':'+v['Test'] for v in outcomes}
leaf=[v for v in outcomes if not any(k.startswith(v['Package']+':'+v['Test']+'/') for k in paths)]
counts=lambda vals:{key:sum(v.get('Action')==key for v in vals) for key in ['pass','fail','skip']}
meta=dict(label=a.label,command=command,cwd=str(root/a.cwd),started_at=started,finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),elapsed_seconds=round(time.monotonic()-start,3),exit_code=process.returncode,stdout=str(log),stderr=str(errlog),test_events=counts(outcomes),top_level=counts([v for v in outcomes if '/' not in v['Test']]),leaf=counts(leaf),package_outcomes=[v for v in records if v.get('Action') in ['pass','fail','skip'] and not v.get('Test')],source_before=before,source_after=after,sources_changed=[p for p in sorted(before.keys()|after.keys()) if before.get(p)!=after.get(p)],environment_overrides={k:env[k] for k in ['GOTOOLCHAIN','GOPATH','GOMODCACHE','GOCACHE','GOPROXY','GOMAXPROCS']})
if input_manifest is not None:
    meta.update(compile_inputs_manifest=str(input_path),compile_inputs_manifest_sha256=hashlib.sha256(input_path.read_bytes()).hexdigest(),compile_inputs_changed_before=inputs_before,compile_inputs_changed_after=input_changes())
(out/(a.label+'.metadata.json')).write_text(json.dumps(meta,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({k:v for k,v in meta.items() if k not in ['source_before','source_after','package_outcomes','environment_overrides']},ensure_ascii=False))
for v in outcomes:
    if v.get('Action')=='fail':print(v)
if process.returncode:print(errlog.read_text()[-5000:])
raise SystemExit(process.returncode)
