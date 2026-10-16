import json,os,pathlib,sys
root=pathlib.Path('/path/to/SmartFactory-2026');work=pathlib.Path('/private/tmp/smartfactory-evolution-native-20261004');mode=sys.argv[1]
if mode not in ['cloud','edge']:raise SystemExit('invalid mode')
creds=json.loads((work/'development-credentials.json').read_text());native=json.loads((work/'tb-bootstrap.json').read_text());port=19990 if mode=='cloud' else 19991;tbport=19080 if mode=='cloud' else 19081
node='cloud-native-test' if mode=='cloud' else 'edge-a'
env=dict(os.environ,SF_BOOTSTRAP_PASSWORD=creds['password'],SF_SERVICE_TOKEN=creds['service_token'],SF_MASTER_KEY_FILE=str(work/(mode+'.key')),SF_DATABASE=str(work/(mode+'.db')),SF_STATIC_DIR=str(root/'Frontends/dist'),SF_LISTEN='0.0.0.0:'+str(port),SF_NODE_ID=node,SF_TB_URL='http://127.0.0.1:'+str(tbport),SF_TB_USERNAME=native['username'],SF_TB_PASSWORD=native['password'],SF_TB_CALLBACK_URL='http://host.docker.internal:'+str(port))
(root/('.local/evolution/native-'+mode+'-process.json')).write_text(json.dumps({'pid':os.getpid(),'address':'http://127.0.0.1:'+str(port),'log':str(work/('sf-'+mode+'.log'))}))
os.chdir(root);binary=str(work/('sf-'+mode));os.execve(binary,[binary,'--seed'],env)
