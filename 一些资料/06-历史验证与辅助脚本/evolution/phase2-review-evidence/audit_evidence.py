#!/usr/bin/env python3
import hashlib
import json
from pathlib import Path
import re
import sys
from datetime import datetime, timezone

ROOT=Path(__file__).resolve().parents[3]
OUT=Path(__file__).resolve().parent
SHA=re.compile(r'^[0-9a-f]{64}$')
SNAPSHOTS = None

def digest(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def resolve(path): return Path(path) if Path(path).is_absolute() else ROOT/path
def read(path): return json.loads(resolve(path).read_text())
def read_preserved(path):
    entry = (SNAPSHOTS or {}).get(path)
    return read(entry['path'] if entry else path)
def compare(files):
    errors=[]
    for path,expected in files.items():
        if isinstance(expected,dict): expected=expected.get('sha256')
        if not isinstance(expected,str) or not SHA.fullmatch(expected): continue
        p=resolve(path)
        actual=digest(p) if p.is_file() else None
        if actual!=expected: errors.append(dict(path=path,expected=expected,actual=actual))
    return dict(files=len(files),different=errors)

def audit_report(name):
    origin='.local/evolution/'+name
    path=resolve((SNAPSHOTS or {}).get(origin, {}).get('path', origin))
    d=json.loads(path.read_text())
    refs={}; string_refs=set()
    def walk(v,at=''):
        if isinstance(v,dict):
            if isinstance(v.get('path'),str) and isinstance(v.get('sha256'),str) and SHA.fullmatch(v['sha256']):
                refs[at]=(v['path'],v['sha256'])
            for k,x in v.items():
                if ('/' in k) and isinstance(x,str) and SHA.fullmatch(x):
                    ref='Frontends/dist/'+k if at=='/artifacts/production_digests' else k
                    refs[at+'/'+k]=(ref,x)
                if ('/' in k) and isinstance(x,dict) and isinstance(x.get('sha256'),str) and SHA.fullmatch(x['sha256']):
                    refs[at+'/'+k]=(k,x['sha256'])
                walk(x,at+'/'+k)
        elif isinstance(v,list):
            for i,x in enumerate(v): walk(x,at+'/'+str(i))
        elif isinstance(v,str) and v.startswith(('.local/evolution/','output/playwright/','/private/tmp/')) and '\n' not in v and len(v)<250:
            p=resolve(v)
            if not any(c in v for c in [';','*']) and p.suffix in ['.json','.log','.jsonl','.txt','.png']:
                string_refs.add(v)
    walk(d)
    results=[]
    for at,(ref,expected) in refs.items():
        p=resolve(ref)
        actual=digest(p) if p.is_file() else None
        results.append(dict(location=at,path=ref,expected=expected,actual=actual,identical=actual==expected))
    return dict(path=str(path.relative_to(ROOT)),original_path=origin,sha256=digest(path),hashed_references=len(results),
        matched=sum(r['identical'] for r in results),different=[r for r in results if not r['identical']],
        referenced_files_checked=len(string_refs),missing_references=sorted(p for p in string_refs if not resolve(p).is_file()))

if __name__=='__main__':
    if len(sys.argv)>2:
        SNAPSHOTS=read(sys.argv[2])['files']
    reports=['rules-verified.json','storage-tasks-verified.json','ai-protocols-verified.json','frontend-phase2-verified.json']
    if (ROOT/'.local/evolution/message-infrastructure-verified.json').exists(): reports.append('message-infrastructure-verified.json')
    result={'created_at':datetime.now(timezone.utc).isoformat(),'reports':[audit_report(p) for p in reports]}
    front=read_preserved('.local/evolution/frontend-phase2-verified.json')
    result['frontend_evidence_index']=compare(front['evidence_index'])
    if SNAPSHOTS:
        result['frontend_preserved_evidence']=compare({SNAPSHOTS[path]['path']:expected for path,expected in front['evidence_index'].items()})
    result['frontend_source']=compare(read_preserved(front['artifacts']['source_manifest'])['files'])
    result['frontend_backend_source']=compare(read_preserved(front['backend']['current_build_snapshot'])['files'])
    result['frontend_dist']=compare({'Frontends/dist/'+p:h for p,h in front['artifacts']['production_digests'].items()})
    result['frontend_current_binary']=compare({front['backend']['backend_path_current_sha256']['path']:front['backend']['current_binary_sha256']})
    result['frontend_initial_binary_sha256']=front['backend']['initial_binary_sha256']
    storage=read_preserved('.local/evolution/storage-tasks-verified.json')
    result['storage_final_source']=compare(read(storage['source_manifest']['path'])['final_sources'])
    result['ai_owned_source']=compare(read_preserved('.local/evolution/ai-protocols-verified.json')['source_summary']['owned_source_sha256'])
    result['rule_source_snapshot']=compare(read_preserved('.local/evolution/rules-verified.json')['source_sha256'])
    name=sys.argv[1] if len(sys.argv)>1 else 'evidence-audit.json'
    (OUT/name).write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({'report':name,**{k:{'checked':v.get('files'),'different':len(v.get('different',[]))} for k,v in result.items() if isinstance(v,dict)},'reports':[{'path':r['path'],'checked':r['hashed_references'],'different':len(r['different']),'missing':r['missing_references']} for r in result['reports']]},ensure_ascii=False))
