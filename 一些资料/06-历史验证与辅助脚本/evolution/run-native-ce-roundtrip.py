import json,os,pathlib,subprocess,time,urllib.request
root=pathlib.Path('/path/to/SmartFactory-2026');work=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004')
creds=json.loads((work/'development-credentials.json').read_text());native=json.loads((work/'tb-bootstrap.json').read_text())
env=dict(os.environ,SF_BOOTSTRAP_PASSWORD=creds['password'],SF_SERVICE_TOKEN=creds['service_token'],SF_MASTER_KEY_FILE=str(work/'cloud.key'),SF_DATABASE=str(work/'cloud.db'),SF_STATIC_DIR=str(root/'Frontends/dist'),SF_LISTEN='0.0.0.0:19990',SF_NODE_ID='cloud-native-test',SF_TB_URL='http://127.0.0.1:19080',SF_TB_USERNAME=native['username'],SF_TB_PASSWORD=native['password'],SF_TB_CALLBACK_URL='http://host.docker.internal:19990')
log=(work/'sf-cloud.log').open('w');p=subprocess.Popen([str(work/'sf-cloud'),'--seed'],cwd=root,env=env,stdout=log,stderr=subprocess.STDOUT)
(root/'.local/evolution/native-cloud-process.json').write_text(json.dumps({'pid':p.pid,'address':'http://127.0.0.1:19990','log':str(work/'sf-cloud.log')}))
try:
 for i in range(120):
  if p.poll() is not None:raise RuntimeError('application exited')
  try:
   if json.load(urllib.request.urlopen('http://127.0.0.1:19990/health',timeout=2))['status']=='ok':break
  except Exception:time.sleep(1)
 else:raise RuntimeError('application did not become ready')
 time.sleep(5)
 result=subprocess.run(['python3',str(root/'scripts/check-native.py'),'--state-directory',str(work),'--profiles','cloud','--cloud-url','http://127.0.0.1:19990','--tb-cloud-url','http://127.0.0.1:19080','--output',str(root/'.local/evolution/native-ce-roundtrip.json')])
 if result.returncode:raise RuntimeError('native cloud check failed')
 print('CE roundtrip verified',flush=True)
 finally_keep=True
except Exception:
 p.terminate();raise
