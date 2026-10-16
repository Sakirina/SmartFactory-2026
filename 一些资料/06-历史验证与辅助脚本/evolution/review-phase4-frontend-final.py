import pathlib,json,hashlib,datetime,re,collections,socket
ROOT=pathlib.Path(__file__).resolve().parents[2]; E=ROOT/'.local/evolution';p=E/'frontend-phase4-verified.json';d=json.loads(p.read_text());errs=[];refs=[];unique={}
def check(path,expected,label):
 q=pathlib.Path(path);q=q if q.is_absolute() else ROOT/q
 try:
  if str(q) not in unique:unique[str(q)]=hashlib.sha256(q.read_bytes()).hexdigest()
  actual=unique[str(q)]
 except Exception as e:actual=str(e)
 refs.append({'path':str(q),'sha256':expected,'actual':actual,'label':label})
 if actual!=expected:errs.append({'label':label,'path':str(q),'expected':expected,'actual':actual})
def walk(x,label):
 if isinstance(x,dict):
  if isinstance(x.get('path'),str) and isinstance(x.get('sha256'),str):check(x.get('preserved_path',x['path']),x['sha256'],label)
  for k,v in x.items():walk(v,label+'/'+k)
 elif isinstance(x,list):
  for i,v in enumerate(x):walk(v,label+'/'+str(i))
walk(d,'report');idx=json.loads(pathlib.Path(d['retained_evidence_index']['path']).read_text());walk(idx,'evidence_index')
b=json.loads(pathlib.Path(d['backend_combination']['business_actual_build_inputs']['path']).read_text());walk(b,'business_build');check(b['binary'],b['binary_sha256'],'business_binary');check(b['build_log'],b['build_log_sha256'],'business_build_log')
raw_unit=pathlib.Path(d['checks']['unit']['log']['path']).read_text();unit={k:int(re.search(r'ℹ '+k+r' (\d+)',raw_unit).group(1)) for k in ['tests','pass','fail','cancelled','skipped']}
if unit!={'tests':27,'pass':27,'fail':0,'cancelled':0,'skipped':0}:errs.append({'unit':unit})
groups=[]
for g in d['browser']['unified']['group_statistics']:
 raw=json.loads(pathlib.Path(g['result']).read_text());s=raw['stats'];groups.append({'group':g['group'],'passed':s['expected'],'failed':s['unexpected'],'flaky':s['flaky'],'routing_skips':s['skipped']})
 if s!=g['statistics']:errs.append({'browser_stats_mismatch':g['group']})
 if g['exit_code']!=0 or s['unexpected'] or s['flaky']:errs.append({'browser_failure':g['group']})
if sum(g['passed'] for g in groups)!=28:errs.append({'browser_pass_total':groups})
pr=json.loads(pathlib.Path(d['persistence']['verification']['path']).read_text());persist_pass=sum(x['passed'] for x in pr['checks'])
if persist_pass!=18 or pr['failed']:errs.append({'persistence':persist_pass})
audits={}
for name in ['frozen','r1']:
 a=json.loads(pathlib.Path(d['audit'][name]['path']).read_text());audits[name]={'records':sum(x['records'] for x in a),'checkpoints':sum(x['checkpoints'] for x in a),'databases':len(a),'all_valid':all(x['valid'] and not x['issues'] for x in a)}
 if not audits[name]['all_valid']:errs.append({'audit':name})
if audits['frozen']['records']!=d['audit']['records'] or audits['r1']['records']!=d['audit']['r1_records']:errs.append({'audit_count_mismatch':audits})
layouts={}
for name in ['cloud_edge','screen']:
 a=json.loads(pathlib.Path(d['browser']['r1_empty'][name]['path']).read_text());layouts[name]=a
ports=[]
for port in range(19410,19416):
 with socket.socket() as s:s.settimeout(.2);closed=s.connect_ex(('127.0.0.1',port))!=0
 ports.append({'port':port,'closed':closed})
 if not closed:errs.append({'owned_port_open':port})
if d['status']!='complete' or d['remaining_frontend_implementation'] or d['remaining_frontend_verification']:errs.append({'incomplete_specialist':True})
out={'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'status':'passed' if not errs else 'failed','report':{'path':str(p),'sha256':hashlib.sha256(p.read_bytes()).hexdigest()},'scope':'Final frontend report references, full retained evidence index, business fixture actual build input, raw unit/browser/persistence/audit counts, and service shutdown','report_hash_reference_count':sum(x['label'].startswith('report') for x in refs),'evidence_index_count':len(idx['files']),'business_input_count':len(b['files']),'business_packages':b['packages'],'hash_checks':len(refs),'unique_files':len(unique),'build_provenance_reviews':['frontend-phase4-build-provenance-root-review.json','frontend-phase4-r1-build-provenance-root-review.json'],'unit':unit,'browser_groups':groups,'browser_passed':sum(x['passed'] for x in groups),'persistence_passed':persist_pass,'audit':audits,'r1_empty_layouts':layouts,'ports':ports,'errors':errs}
(E/'frontend-phase4-root-review.json').write_text(json.dumps(out,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({k:v for k,v in out.items() if k not in ['r1_empty_layouts','ports']},ensure_ascii=False,indent=2))
