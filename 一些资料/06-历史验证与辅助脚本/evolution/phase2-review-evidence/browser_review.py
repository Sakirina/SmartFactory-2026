#!/usr/bin/env python3
"""Own isolated sf-cloud and CLI browser session for current-source review."""
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.request

OUT=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('review',OUT/'run_review.py')
R=importlib.util.module_from_spec(spec); spec.loader.exec_module(R)
ROOT=R.ROOT
TMP=R.TMP
PRIVATE=TMP/'private'
PRIVATE.mkdir(mode=0o700,exist_ok=True)
SESSION='sf-phase2-independent'
CLI='/home/developer/.codex/skills/playwright/scripts/playwright_cli.sh'
BASE='http://127.0.0.1:19040'
R.ENV['npm_config_cache']='/private/tmp/smartfactory-evolution-npm-cache'

def secret_file(): return PRIVATE/'access.json'
def access(): return json.loads(secret_file().read_text())
def api(method,path,body=None):
    data=access()
    headers={'Content-Type':'application/json'}
    if data.get('token'): headers['Authorization']='Bearer '+data['token']
    request=urllib.request.Request(BASE+'/api/sf/v1'+path,method=method,headers=headers,
        data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None)
    with urllib.request.urlopen(request,timeout=15) as response:
        return response.status,json.loads(response.read())

def cli(args,quiet=False):
    p=subprocess.run([CLI,'--session',SESSION,*args],cwd=ROOT,env=R.ENV,capture_output=True,text=True)
    raw=p.stdout+p.stderr
    private=access()
    for key in ['password','token']:
        if private.get(key): raw=raw.replace(private[key],'<redacted>')
    with (OUT/'browser-cli.log').open('a') as log: log.write(json.dumps({'arguments':args if args[:1]!=['fill'] else [*args[:2],'<value>'],'exit_code':p.returncode},ensure_ascii=False)+'\n'+raw+'\n')
    if not quiet: print(raw)
    if p.returncode: raise SystemExit(p.returncode)
    return raw

def snapshot_database(label):
    db=sqlite3.connect(f'file:{TMP}/browser.db?mode=ro',uri=True)
    statements={
      'formal_documents':"SELECT kind,id,version,data FROM documents WHERE kind IN ('definition','engine_state','alarm','active_alarm','revision','entity','data_catalog','compiled_plan') ORDER BY kind,id",
      'observations':'SELECT * FROM observations ORDER BY id,observed_ms',
      'latest':'SELECT * FROM latest ORDER BY device_id,key',
      'rollups':'SELECT * FROM rollups ORDER BY device_id,key,granularity,bucket_ms',
    }
    result={}
    import hashlib
    for key,query in statements.items():
        rows=db.execute(query).fetchall()
        raw=json.dumps(rows,sort_keys=True,separators=(',',':'),default=str).encode()
        result[key]={'query':query,'rows':len(rows),'sha256':hashlib.sha256(raw).hexdigest()}
    db.close(); R.json_write(OUT/(label+'.json'),result); print(json.dumps(result))

if __name__=='__main__':
    mode=sys.argv[1]
    if mode=='start':
        with socket.socket() as sock: sock.bind(('127.0.0.1',19040))
        shutil.copytree(ROOT/'Frontends/dist',TMP/'browser-static',dirs_exist_ok=True)
        password=secrets.token_urlsafe(24)
        R.json_write(secret_file(),{'password':password})
        secret_file().chmod(0o600)
        env={k:v for k,v in R.ENV.items() if not k.startswith('SF_')}
        env['SF_BOOTSTRAP_PASSWORD']=password
        argv=[str(TMP/'sf-cloud'),'-listen','127.0.0.1:19040','-node-id','phase2-independent-cloud',
              '-database',str(TMP/'browser.db'),'-master-key',str(PRIVATE/'master.key'),
              '-static',str(TMP/'browser-static'),'-seed']
        with (OUT/'browser-backend.log').open('w') as log:
            process=subprocess.Popen(argv,cwd=TMP,env=env,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
        R.json_write(OUT/'browser-process.json',{'pid':process.pid,'argv':argv,'binary_sha256':R.sha(TMP/'sf-cloud'),
            'binary_build_metadata':'.local/evolution/phase2-review-evidence/browser-backend-build.metadata.json',
            'static_files':{str(p.relative_to(TMP/'browser-static')):R.sha(p) for p in (TMP/'browser-static').rglob('*') if p.is_file()}})
        for _ in range(100):
            if process.poll() is not None: raise RuntimeError('sf-cloud terminated during startup')
            try:
                with urllib.request.urlopen(BASE,timeout=1) as response:
                    if response.status==200: break
            except OSError: time.sleep(.1)
        else: raise RuntimeError('sf-cloud did not listen')
        status,result=api('POST','/login',{'login':'admin','password':password})
        R.json_write(secret_file(),{'password':password,'token':result['token']});secret_file().chmod(0o600)
        definitions=json.loads((ROOT/'examples/factory/definitions.json').read_text())['definitions']
        definition=next(d for d in definitions if d['id']=='goods-count')
        definition.update(id='independent-counter',name='独立审查连续计数')
        definition['outputs'][0]['unit']='piece'
        status,draft=api('POST','/drafts',{'draft':{'id':'independent/simulation:counter','definition':definition,'base_version':0,'version':0},'expected_version':0})
        R.json_write(OUT/'browser-draft.json',draft)
        print(json.dumps({'pid':process.pid,'url':BASE,'draft_status':status,'draft_id':draft['id'],'revision':draft['version']}))
    elif mode=='login':
        cli(['fill',sys.argv[2],'admin'],quiet=True)
        cli(['fill',sys.argv[3],access()['password']],quiet=True)
        cli(['click',sys.argv[4]])
    elif mode=='snapshot-db': snapshot_database(sys.argv[2])
    elif mode=='cli': cli(sys.argv[2:])
    elif mode=='stop':
        cli(['close'])
        info=json.loads((OUT/'browser-process.json').read_text()); os.kill(info['pid'],signal.SIGTERM)
        for _ in range(100):
            try:
                with socket.create_connection(('127.0.0.1',19040),timeout=.1): pass
            except OSError: break
            time.sleep(.1)
        else: raise RuntimeError('own port remains open')
        secret_file().unlink(missing_ok=True)
        (PRIVATE/'master.key').unlink(missing_ok=True)
        R.json_write(OUT/'browser-cleanup.json',{'pid':info['pid'],'port':19040,'port_accepting':False,'browser_session':SESSION,'browser_closed':True,'plaintext_credentials_removed':True})
