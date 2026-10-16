#!/usr/bin/env python3
import datetime,hashlib,json,pathlib,shutil
root=pathlib.Path('/path/to/SmartFactory-2026')
out=root/'.local/evolution/ai-evidence-a02'
base=pathlib.Path('/private/tmp')/('smartfactory-ai-a02-final-'+datetime.datetime.now().strftime('%Y%m%d-%H%M%S'))
workspace=base/'workspace';workspace.mkdir(parents=True)
ignore={'.git','.local','.cache','node_modules','dist','vendor','output','build','bin'}
paths=[]
for directory in ['Platform','DataTransfer','contracts','examples']:
 paths.extend(p for p in (root/directory).rglob('*') if p.is_file() and not any(part in ignore for part in p.relative_to(root/directory).parts))
for name in ['scripts/backup-state.py','scripts/check-contracts.py','Frontends/package.json','Frontends/package-lock.json','Frontends/scripts/generate-api.mjs','Frontends/src/generated-routes.ts']:
 p=root/name
 if p.is_file():paths.append(p)
entries=[]
for p in sorted(set(paths)):
 raw=p.read_bytes();rel=p.relative_to(root);dst=workspace/rel;dst.parent.mkdir(parents=True,exist_ok=True);dst.write_bytes(raw)
 entries.append({'path':str(rel),'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()})
changes=[v['path'] for v in entries if hashlib.sha256((root/v['path']).read_bytes()).hexdigest()!=v['sha256']]
assert not changes,changes
review=json.load(open(root/'.local/evolution/phase4-review-evidence/review-test-changes.json'))
retained=[]
for v in review['files']:
 actual=hashlib.sha256((workspace/v['path']).read_bytes()).hexdigest();assert actual==v['after']['sha256'],v['path']
 retained.append({'path':v['path'],'sha256':actual})
originals=pathlib.Path('/private/tmp/smartfactory-phase4-independent-20261005/overlays')
overlays=[]
for version in ['v2','v3','v3corrected']:
 folder=workspace/'Platform/internal'/('a02review'+version);folder.mkdir()
 (folder/'package.go').write_text('package ai\n')
 helper=workspace/'Platform/internal/ai/investigation_http_test.go'
 shutil.copyfile(helper,folder/helper.name)
 source=originals/('independent-ai-build-'+('v3' if version=='v3corrected' else version))/'independent_ai_review_test.go'
 raw=source.read_bytes();destination=folder/source.name
 if version=='v3corrected':
  text=raw.decode().replace('len(detail.Evidence[0].Arguments) != 0','len(detail.Evidence[0].Arguments) != 0 && string(detail.Evidence[0].Arguments) != "null"')
  assert text!=raw.decode();destination.write_text(text)
 else:destination.write_bytes(raw)
 overlays.append({'version':version,'source':str(source),'source_sha256':hashlib.sha256(raw).hexdigest(),'path':str(destination),'sha256':hashlib.sha256(destination.read_bytes()).hexdigest(),'change':'Treat required arguments JSON null as absent argument content' if version=='v3corrected' else 'Unchanged independent reproduction'})
manifest={'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'workspace':str(workspace),'files':entries,'copy_changes':changes,'files_count':len(entries),'go_files':sum(v['path'].endswith('.go') for v in entries),'directory_metadata_files':[v['path'] for v in entries if pathlib.Path(v['path']).name=='.DS_Store'],'retained_review_test_improvements':retained,'independent_test_overlays':overlays}
for folder in [workspace/'Platform/internal/a02reviewv2',workspace/'Platform/internal/a02reviewv3',workspace/'Platform/internal/a02reviewv3corrected']:
 for p in sorted(folder.iterdir()):
  manifest.setdefault('overlay_files',[]).append({'path':str(p),'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest()})
(out/'final-snapshot.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'workspace':str(workspace),'files':len(entries),'overlays':len(manifest['overlay_files']),'copy_changes':changes,'retained_review_tests':retained},ensure_ascii=False))
