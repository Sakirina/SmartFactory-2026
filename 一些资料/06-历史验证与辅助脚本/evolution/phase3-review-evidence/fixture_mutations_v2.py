"""Permission changes in the isolated reviewer fixtures only; secrets stay in memory."""
import importlib.util,json,urllib.request,urllib.error
from pathlib import Path
p=Path(__file__).with_name('browser_actions_v2.py');s=importlib.util.spec_from_file_location('a',p);a=importlib.util.module_from_spec(s);s.loader.exec_module(a);b=a.b

def admin_call(mode,path,method='GET',body=None):
 credentials=b.access(mode)
 base='http://127.0.0.1:19061' if mode=='history' else credentials[mode+'_url']
 request=urllib.request.Request(base+'/api/sf/v1/login',method='POST',headers={'Content-Type':'application/json'},data=json.dumps({'login':'admin','password':credentials['password']}).encode())
 with urllib.request.urlopen(request,timeout=15) as response:token=json.loads(response.read())['token']
 request=urllib.request.Request(base+'/api/sf/v1'+path,method=method,headers={'Content-Type':'application/json','Authorization':'REDACTED'+token},data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None)
 with urllib.request.urlopen(request,timeout=15) as response:return response.status,json.loads(response.read())
