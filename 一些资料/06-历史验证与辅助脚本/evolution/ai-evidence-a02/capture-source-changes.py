#!/usr/bin/env python3
import difflib,hashlib,json,pathlib
root=pathlib.Path('/path/to/SmartFactory-2026');out=root/'.local/evolution/ai-evidence-a02'
workspace=pathlib.Path(json.load(open(out/'final-snapshot.json'))['workspace'])
old=pathlib.Path('/private/tmp/smartfactory-ai-final-20261005-0952/workspace')
changes=[];diff=[]
for name in ['Platform/internal/application/investigations.go','Platform/internal/application/investigations_test.go','Platform/internal/ai/investigation_resource_deletion_test.go','Platform/cmd/sf-ai-evidence-fixture/main.go']:
 before=old/name;after=workspace/name
 a=before.read_text() if before.exists() else '';b=after.read_text();assert a!=b,name
 assert (root/name).read_bytes()==after.read_bytes(),name
 item={'path':name,'scope':'production' if not name.endswith('_test.go') else 'regression_test','before':{'path':str(before),'sha256':hashlib.sha256(before.read_bytes()).hexdigest(),'bytes':before.stat().st_size} if before.exists() else None,'after':{'path':str(after),'sha256':hashlib.sha256(after.read_bytes()).hexdigest(),'bytes':after.stat().st_size}}
 changes.append(item);diff.extend(difflib.unified_diff(a.splitlines(True),b.splitlines(True),fromfile='original-0952/'+name if before.exists() else '/dev/null',tofile='a02-fixed/'+name))
(out/'source-changes.json').write_text(json.dumps(changes,ensure_ascii=False,indent=2)+'\n');(out/'source-changes.patch').write_text(''.join(diff))
print(json.dumps({'files':len(changes),'production':sum(v['scope']=='production' for v in changes),'tests':sum(v['scope']=='regression_test' for v in changes)}))
