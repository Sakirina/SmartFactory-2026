#!/usr/bin/env python3
"""Create a private empty-release cloud from a consistent, live fixture snapshot."""
import argparse, hashlib, json, os, pathlib, secrets, shutil, sqlite3, subprocess, time, urllib.request
parser=argparse.ArgumentParser()
parser.add_argument('--source',default='/private/tmp/smartfactory-phase5-node-frontend-live-20261005')
parser.add_argument('--directory',required=True)
parser.add_argument('--binary',default='/private/tmp/smartfactory-phase5-release-a01-20261005/bin/sf-cloud')
parser.add_argument('--port',type=int,default=19620)
args=parser.parse_args()
source=pathlib.Path(args.source); target=pathlib.Path(args.directory)
if target.exists(): raise SystemExit('The new fixture directory must not already exist')
target.mkdir(mode=0o700,parents=True); private=target/'private'; private.mkdir(mode=0o700)
descriptor=json.load(open(source/'frontend.json')); credentials=json.load(open(descriptor['credentials_file']))
request=urllib.request.Request(descriptor['cloud_url']+'/api/sf/v1/login',data=json.dumps(credentials).encode(),headers={'Content-Type':'application/json'},method='POST')
with urllib.request.urlopen(request,timeout=10) as response: session=json.load(response)
identity_id='first-publication-cloud-'+secrets.token_hex(6)
identity={'id':identity_id,'node_id':'cloud-1','program':'cloud','purpose':'first-publication-test','enabled':True,'capabilities':['config.v2','config.read','config.report'],'parameter_ids':[],'connector_ids':[],'generation':0,'version':0,'instance_epoch':0,'updated_ms':0}
def post(resource,body):
    request=urllib.request.Request(descriptor['cloud_url']+'/api/sf/v1'+resource,data=json.dumps(body).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+session['token']},method='POST')
    with urllib.request.urlopen(request,timeout=10) as response: return json.load(response)
registered=post('/workload-identities',{'identity':identity,'expected_version':0})
workload_credential=secrets.token_urlsafe(48)
rotated=post('/workload-identities/'+identity_id+'/rotate',{'credential':workload_credential,'expected_version':registered['version']})
credential_path=private/'cloud-workload.token'; credential_path.write_text(workload_credential); credential_path.chmod(0o600)
# Both clouds use this existing authority session. A new login in the copy would
# have no corresponding session in the configuration authority's callback.
session_path=private/'session.json'; session_path.write_text(json.dumps(session)); session_path.chmod(0o600)
src=sqlite3.connect('file:'+str(source/'private/cloud.db')+'?mode=ro',uri=True)
dst=sqlite3.connect(private/'cloud.db'); src.backup(dst); src.close()
removed=dst.execute("SELECT kind,id FROM documents WHERE kind IN ('release','release_deployment','release_report')").fetchall()
for table in ['documents','document_versions']:
    dst.execute("DELETE FROM "+table+" WHERE kind IN ('release','release_deployment','release_report')")
dst.execute('DELETE FROM sf_release_reports'); dst.commit()
assert dst.execute("SELECT count(*) FROM documents WHERE kind='release'").fetchone()[0]==0
dst.close(); shutil.copy2(source/'private/cloud.master',private/'cloud.master'); (private/'cloud.master').chmod(0o600)
pki=source/'private/pki'
env=dict(os.environ,SF_LISTEN=f'127.0.0.1:{args.port}',SF_DATABASE=str(private/'cloud.db'),SF_NODE_ID='cloud-1',SF_MASTER_KEY_FILE=str(private/'cloud.master'),SF_BOOTSTRAP_PASSWORD=credentials['password'],SF_CONFIG_URL=descriptor['configuration_url'],SF_CONFIG_TLS_CA=str(pki/'ca.pem'),SF_CONFIG_TLS_CERT=str(pki/'config-client-cloud-1.pem'),SF_CONFIG_TLS_KEY=str(pki/'config-client-cloud-1.key'),SF_CONFIG_LEGACY_SUBSCRIPTION='false',SF_CONFIG_TOKEN_FILE=str(credential_path),SF_WORKLOAD_BOOTSTRAP_FILE='',SF_AUTHORITY_LISTEN='',SF_SYNC_LISTEN='',SF_RELEASE_PAYLOAD='',SF_RELEASE_ARTIFACT_ROOT=str(source/'private/release-artifacts'))
with open(target/'stdout.log','ab') as out,open(target/'stderr.log','ab') as err:
    child=subprocess.Popen([args.binary],env=env,stdout=out,stderr=err,start_new_session=True)
url=f'http://127.0.0.1:{args.port}'
for _ in range(100):
    if child.poll() is not None: raise SystemExit('The isolated cloud exited before becoming available')
    try:
        with urllib.request.urlopen(urllib.request.Request(url+'/api/sf/v1/releases',headers={'Authorization':'Bearer '+session['token']}),timeout=2) as response:
            assert json.load(response)==[]
        break
    except Exception: time.sleep(.1)
else: raise SystemExit('The isolated cloud did not become available')
metadata={'source_descriptor':str(source/'frontend.json'),'source_database':str(source/'private/cloud.db'),'copy_method':'SQLite online backup','isolated_empty_release_setup':removed,'binary':args.binary,'binary_sha256':hashlib.sha256(pathlib.Path(args.binary).read_bytes()).hexdigest(),'pid':child.pid,'cloud_url':url,'configuration_url':descriptor['configuration_url'],'authority_session_file':str(session_path),'session_authority':descriptor['cloud_url'],'dedicated_workload_identity':identity_id,'dedicated_workload_generation':rotated['generation'],'database':str(private/'cloud.db'),'created_ms':int(time.time()*1000)}
(target/'fixture.json').write_text(json.dumps(metadata,ensure_ascii=False,indent=2)+'\n'); print(json.dumps({k:v for k,v in metadata.items() if k != 'authority_session_file'},ensure_ascii=False))
