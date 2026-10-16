#!/usr/bin/env python3
import decimal,hashlib,json,pathlib,signal,socket,subprocess,time,urllib.error,urllib.parse,urllib.request
root=pathlib.Path('/path/to/SmartFactory-2026')
out=root/'.local/evolution/ai-evidence-a02'
workspace=pathlib.Path(json.loads((out/'final-snapshot.json').read_text())['workspace'])
base=workspace.parent
binary=base/'bin/sf-ai-evidence-fixture'
directory=base/'final-http-fixture'
records=[]
stdout=(base/'final-http-fixture.stdout.log').open('w')
stderr=(base/'final-http-fixture.stderr.log').open('w')
process=subprocess.Popen([str(binary),'-dir',str(directory),'-address','127.0.0.1:0','-model-api','responses','-model-stream=true','-duration','2m'],stdout=stdout,stderr=stderr)
manifest={};private={};shutdown={}
try:
    deadline=time.monotonic()+15
    while not (directory/'manifest.json').is_file():
        if process.poll() is not None:raise RuntimeError('fixture exited before readiness')
        if time.monotonic()>deadline:raise RuntimeError('fixture readiness timeout')
        time.sleep(.05)
    manifest=json.loads((directory/'manifest.json').read_text())
    private=json.loads((directory/'private-credentials.json').read_text())
    def request(method,path,value=None,token='',record=True):
        body=json.dumps(value,ensure_ascii=False).encode() if value is not None else None
        headers={'Content-Type':'application/json'}
        if token:headers['Authorization']='Bearer '+token
        req=urllib.request.Request(manifest['url']+path,data=body,headers=headers,method=method)
        try:
            with urllib.request.urlopen(req,timeout=12) as response:status=response.status;raw=response.read();content_type=response.headers.get('Content-Type','')
        except urllib.error.HTTPError as error:status=error.code;raw=error.read();content_type=error.headers.get('Content-Type','')
        if record:records.append({'method':method,'path':path,'status':status,'content_type':content_type,'request':value,'response':raw.decode()})
        return status,raw
    def login(name):
        status,raw=request('POST','/api/sf/v1/login',{'login':name,'password':private['password']},record=False)
        assert status==200,status
        return json.loads(raw)['token']
    token=login(private['login']);other=login(private['other_login'])
    def get(path):
        status,raw=request('GET',path,token=token)
        return status,json.loads(raw)
    def investigate(text):
        status,raw=request('POST',manifest['assistant'],{'messages':[{'role':'user','content':text}]},token)
        assert status==200,(status,raw)
        events=[json.loads(line[6:]) for line in raw.decode().splitlines() if line.startswith('data: ')]
        identity=events[0]['investigation_id']
        status,result=get('/api/sf/v1/investigations/'+identity)
        assert status==200,status
        return result,events
    original,events=investigate('调查温度、执行与历史依据')
    assert original['status']=='completed' and original['evidence_status']=='supported' and len(original['evidence'])==5
    assert events[-1]['type']=='done' and any(e['type']=='delta' and e['provisional'] for e in events)
    visible=[json.loads(line) for line in (directory/'model-visible-results.jsonl').read_text().splitlines()]
    proofs=[]
    for evidence in original['evidence']:
        path='/api/sf/v1/investigations/'+original['id']+'/evidence/'+evidence['id']
        status,value=get(path);assert status==200,status
        actual=next(v for v in visible if v['visible_sha256']==value['visible_sha256'])
        assert actual['tool_call_id']==value['tool_call_id'] and actual['visible_content']==value['visible_content']
        assert hashlib.sha256(value['visible_content'].encode()).hexdigest()==value['visible_sha256']
        if value['tool_name']=='query_data':assert '9007199254740993.125' in value['visible_content']
        proofs.append({'tool':value['tool_name'],'call_id':value['tool_call_id'],'visible_sha256':value['visible_sha256'],'bytes':value['visible_bytes']})
    status,_=request('GET','/api/sf/v1/investigations/'+original['id'],token=other);assert status==404,status
    status,_=request('POST',manifest['fixture_state'],{'action':'revoke'},other);assert status==403,status
    cases=[('伪造引用','unsupported','unknown'),('跨调查引用','unsupported','cross_investigation'),('空查询','unsupported','empty'),('查询失败','unsupported','tool_error'),('超预算','unsupported','budget_exceeded'),('无依据回答','missing','')]
    for text,state,reference in cases:
        result,_=investigate(text);assert result['evidence_status']==state,(text,result['evidence_status'])
        if reference:assert result['references'][0]['status']==reference,(text,result['references'])
    for text in ['模拟中断','模拟服务错误','模拟超时']:
        result,failure_events=investigate(text)
        assert result['status']=='failed' and result['evidence_status']=='unsupported'
        assert any(e['type']=='error' for e in failure_events) and all(e['type']!='done' for e in failure_events)
    status,_=request('POST',manifest['fixture_state'],{'action':'revoke'},token);assert status==200,status
    status,revoked=get('/api/sf/v1/investigations/'+original['id'])
    assert status==200 and revoked['evidence_status']=='unsupported' and not revoked.get('answer')
    first=original['evidence'][0]
    detail_path='/api/sf/v1/investigations/'+original['id']+'/evidence/'+first['id']
    status,_=get(detail_path);assert status==403,status
    status,_=request('POST',manifest['fixture_state'],{'action':'restore'},token);assert status==200,status
    status,restored=get(detail_path);assert status==200 and restored['visible_sha256']==first['visible_sha256']
    status,_=request('POST',manifest['fixture_state'],{'action':'expire','investigation_id':original['id']},token);assert status==200,status
    status,_=get(detail_path);assert status==410,status
    escaped,_=investigate('读取身份 '+manifest['definition_id'])
    assert escaped['evidence_status']=='supported'
    resource=next(r for r in escaped['evidence'][0]['resources'] if r['kind']=='definition' and r['id']==manifest['definition_id'])
    status,_=get(resource['url']);assert status==200,(status,resource['url'])
    deletable,_=investigate('读取身份 '+manifest['definition_id'])
    status,raw=request('DELETE','/api/sf/v1/investigations/'+deletable['id'],token=token)
    assert status==200 and json.loads(raw)['deleted']
    status,_=request('POST',manifest['fixture_state'],{'action':'prepare_original_resource'},token);assert status==200,status
    original_asset,_=investigate('读取身份 '+manifest['definition_id'])
    assert original_asset['evidence_status']=='supported' and len(original_asset['evidence'])==1
    asset_path='/api/sf/v1/investigations/'+original_asset['id']+'/evidence/'+original_asset['evidence'][0]['id']
    status,asset_before=get(asset_path);assert status==200 and asset_before['access_status']=='valid'
    captured=next(r for r in asset_before['resources'] if r['kind']=='resource' and r['id']=='private-factory')
    assert captured['version']>0
    actual=next(v for v in [json.loads(line) for line in (directory/'model-visible-results.jsonl').read_text().splitlines()] if v['visible_sha256']==asset_before['visible_sha256'])
    assert actual['visible_content']==asset_before['visible_content']
    status,_=request('POST',manifest['fixture_state'],{'action':'rebind_definition'},token);assert status==200,status
    status,asset_existing=get(asset_path)
    assert status==200 and asset_existing['visible_content']==asset_before['visible_content'] and asset_existing['resources']==asset_before['resources']
    status,_=request('POST',manifest['fixture_state'],{'action':'delete_original_resource'},token);assert status==200,status
    status,asset_deleted=get(asset_path);assert status==404 and 'visible_content' not in asset_deleted
    status,asset_investigation=get('/api/sf/v1/investigations/'+original_asset['id'])
    assert status==200 and asset_investigation['evidence_status']=='unsupported' and not asset_investigation.get('answer') and asset_investigation['messages']==[]
    assert asset_investigation['references'][0]['status']=='deleted'
    hidden=asset_investigation['evidence'][0]
    assert hidden['access_status']=='deleted' and hidden['arguments'] is None and hidden['resources']==[] and not hidden.get('visible_content')
    a02={'investigation_id':original_asset['id'],'original_resource':captured,'original_visible_sha256':asset_before['visible_sha256'],'original_visible_bytes':asset_before['visible_bytes'],'model_visible_content_matches':True,'existing_http':200,'moved_definition_http':200,'deleted_http':404,'investigation_http':200,'reference_status':'deleted','answer_bytes':0,'messages':0,'arguments':None,'resources':[]}
    status,_=request('POST',manifest['fixture_state'],{'action':'delete_resource'},token);assert status==200,status
    status,_=get('/api/sf/v1/investigations/'+escaped['id']+'/evidence/'+escaped['evidence'][0]['id']);assert status==404,status
finally:
    if process.poll() is None:process.send_signal(signal.SIGTERM)
    try:process.wait(timeout=15)
    finally:stdout.close();stderr.close()
    shutdown['exit_code']=process.returncode
    for kind,url in [('application',manifest.get('url','')),('model',private.get('model',{}).get('endpoint',''))]:
        if not url:continue
        parsed=urllib.parse.urlparse(url)
        try:
            connection=socket.create_connection((parsed.hostname,parsed.port),timeout=.5);connection.close();closed=False
        except OSError:closed=True
        shutdown[kind+'_listener_closed']=closed
    partial={'fixture_dir':str(directory),'binary':str(binary),'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'shutdown':shutdown,'requests':records}
    (out/'final-fixture-http.json').write_text(json.dumps(partial,ensure_ascii=False,indent=2)+'\n')
assert process.returncode==0 and all(shutdown[k] for k in ['application_listener_closed','model_listener_closed']),shutdown
result={'fixture_dir':str(directory),'binary':str(binary),'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'http_requests':len(records),'original_asset_deletion':a02,'model_visible_initial_results':proofs,'shutdown':shutdown,'requests':records}
(out/'final-fixture-http.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({k:v for k,v in result.items() if k!='requests'},ensure_ascii=False))
