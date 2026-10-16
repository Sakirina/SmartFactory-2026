#!/usr/bin/env python3
"""Map the final ordinary conditional cases to unchanged prior compiled test graphs."""
import hashlib,json,collections
from pathlib import Path
BASE=Path('/private/tmp/smartfactory-final-independent-20261007')
OLD=Path('/private/tmp/smartfactory-delivery-regressions-current-20261005')
CUR=BASE/'platform-ordinary'
FIELDS=['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','FFiles','SFiles','SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles']
def objects(path):
 raw=Path(path).read_text();dec=json.JSONDecoder();off=0;out={}
 while off<len(raw):
  while off<len(raw) and raw[off].isspace():off+=1
  if off==len(raw):break
  x,off=dec.raw_decode(raw,off);out[x['ImportPath']]=x
 return out
def ref(p):
 p=Path(p);return {'path':str(p),'sha256':hashlib.sha256(p.read_bytes()).hexdigest()}
def selected(graph,records,name):
 package=graph[name];files=set();mods=set()
 for key in [name,*package.get('Deps',[])]:
  p=graph[key]
  for field in FIELDS:
   for f in p.get(field,[]):files.add(str((Path(p['Dir'])/f).resolve()))
  for m in [p.get('Module',{}),p.get('Module',{}).get('Replace',{})]:
   if m.get('GoMod'):files.add(str(Path(m['GoMod']).resolve()))
 for m in ['Platform','DataTransfer']:
  for f in ['go.mod','go.sum']:files.add('/path/to/SmartFactory-2026/'+m+'/'+f)
 missing=files-records.keys()
 if missing:raise RuntimeError('source index missing: '+repr(missing))
 return {p:records[p]['sha256'] for p in sorted(files)}
a=objects(OLD/'platform-tests-before-go-list.stdout.log');b=objects(CUR/'before-go-list.stdout.log')
ar={r['path']:r for r in json.loads((OLD/'platform-tests-before.inputs.json').read_text())};br={r['path']:r for r in json.loads((CUR/'before.inputs.json').read_text())}
roots=sorted(p for p in b if p.startswith('competition2026/product/platform/') and p.endswith('.test'))
comparisons={}
for p in roots:
 old=selected(a,ar,p) if p in a else {};new=selected(b,br,p)
 changed=[x for x in old.keys()&new.keys() if old[x]!=new[x]];added=sorted(new.keys()-old.keys());removed=sorted(old.keys()-new.keys())
 comparisons[p[:-5]]={'root':p,'before_count':len(old),'current_count':len(new),'unchanged':old==new,'changed':sorted(changed),'added':added,'removed':removed}
prior_events={};outputs=collections.defaultdict(list)
for line in (OLD/'platform-tests.stdout.log').read_text().splitlines():
 e=json.loads(line)
 if e.get('Test') and e['Action'] in ['pass','skip','fail']:prior_events[(e['Package'],e['Test'])]=e['Action']
for line in (CUR/'platform-tests.stdout.log').read_text().splitlines():
 e=json.loads(line)
 if e.get('Test') and e['Action']=='output':outputs[(e['Package'],e['Test'])].append(e['Output'].strip())
v=json.loads((CUR/'verification.json').read_text());skips=[]
for e in v['skipped_tests']:
 key=e['Package'],e['Test'];why=' | '.join(s for s in outputs[key] if not s.startswith(('=== RUN','--- SKIP')))
 graph=comparisons[e['Package']]
 category='postgres' if 'PostgreSQL' in why and '16 -> 18' not in why else 'external_or_fixture'
 skips.append(dict(e,reason=why,category=category,prior_event=prior_events.get(key),compiled_graph_unchanged=graph['unchanged'],disposition='reuse_same_input_pass' if graph['unchanged'] and prior_events.get(key)=='pass' else 'specialized_followup_required'))
result={'ordinary_report':ref(CUR/'verification.json'),'prior_report':ref(OLD/'verification.json'),'method':'Per compiled .test root closure over its Go list Deps; only compilation/embedded fields, generated test mains and module locks included; dependency TestGoFiles are excluded unless selected into the actual test package GoFiles','package_graphs':comparisons,'conditional_cases':skips,'summary':{'current_test_roots':len(roots),'changed_test_roots':[p for p,r in comparisons.items() if not r['unchanged']],'conditional_count':len(skips),'dispositions':dict(collections.Counter(r['disposition'] for r in skips)),'category_counts':dict(collections.Counter(r['category'] for r in skips))}}
p=CUR/'conditional-coverage.json';p.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
print(json.dumps(result['summary'],ensure_ascii=False,indent=2))
print('remaining',[(x['Package'].split('/')[-1],x['Test'],x['prior_event']) for x in skips if x['disposition']!='reuse_same_input_pass'])
