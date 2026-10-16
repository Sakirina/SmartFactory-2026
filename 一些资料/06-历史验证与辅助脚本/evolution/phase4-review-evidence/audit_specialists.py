#!/usr/bin/env python3
import datetime,hashlib,json,pathlib
ROOT=pathlib.Path('/path/to/SmartFactory-2026')
OUT=ROOT/'.local/evolution/phase4-review-evidence'
def sha(p):return hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest()
def references(v):
 if isinstance(v,dict):
  if isinstance(v.get('path'),str) and isinstance(v.get('sha256'),str):yield v['path'],v['sha256']
  for key,value in v.items():
   if isinstance(value,str) and key.endswith('_sha256'):
    base=key[:-7]
    path=v.get(base) or v.get(base+'_path')
    if isinstance(path,str) and ('/' in path or path.endswith(('.json','.log','.jsonl'))):yield path,value
  for x in v.values():yield from references(x)
 elif isinstance(v,list):
  for x in v:yield from references(x)
root_review=json.loads((ROOT/'.local/evolution/ai-evidence-root-review.json').read_text())
preserved={p['original_path']:p['preserved_path'] for p in root_review['preserved_report_source']}
fallbacks=[pathlib.Path('/private/tmp/smartfactory-ai-final-20261005-0952/workspace'),pathlib.Path('/private/tmp/smartfactory-business-final-20261005-085650-v3'),pathlib.Path('/private/tmp/smartfactory-query-final-20261005-082007-v2'),pathlib.Path('/private/tmp/smartfactory-query-final-20261005-082007')]
results=[]
for name in ['query-subscriptions-verified.json','business-workflows-verified.json','ai-evidence-verified.json']:
 path=ROOT/'.local/evolution'/name;report=json.loads(path.read_text());rows=[]
 for original,expected in sorted(set(references(report))):
  p=pathlib.Path(original)
  if not p.is_absolute():p=ROOT/p
  candidates=[]
  if name.startswith('ai-') and str(p) in preserved:candidates.append(pathlib.Path(preserved[str(p)]))
  candidates.append(p)
  if p.is_relative_to(ROOT):candidates += [f/p.relative_to(ROOT) for f in fallbacks]
  match=next((c for c in candidates if c.is_file() and sha(c)==expected),None)
  rows.append(dict(original_path=original,expected_sha256=expected,resolved_path=str(match) if match else None,matched=match is not None))
 results.append(dict(report=str(path),report_sha256=sha(path),references=rows,reference_count=len(rows),unmatched=[r for r in rows if not r['matched']]))
snapshot=json.loads((OUT/'snapshot.json').read_text())
comparison=[]
fixed=fallbacks[0]
for row in snapshot['files']:
 relative=pathlib.Path(row['path'])
 if relative.parts[0] not in ['Platform','DataTransfer'] or not (relative.suffix=='.go' or relative.name in ['go.mod','go.sum']):continue
 old=fixed/relative
 comparison.append(dict(path=str(relative),independent_sha256=row['sha256'],ai_frozen_sha256=sha(old) if old.exists() else None,identical=old.exists() and sha(old)==row['sha256']))
out=dict(created_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),reports=results,independent_backend_vs_ai_fixed=comparison,differences=[x for x in comparison if not x['identical']])
(OUT/'specialist-evidence-audit.json').write_text(json.dumps(out,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'reports':[{'report':r['report'],'sha256':r['report_sha256'],'references':r['reference_count'],'unmatched':r['unmatched']} for r in results],'backend_files':len(comparison),'backend_differences':out['differences']},ensure_ascii=False))
