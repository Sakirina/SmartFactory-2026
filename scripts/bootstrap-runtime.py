#!/usr/bin/env python3
"""Install or operate one explicitly prepared SmartFactory deployment."""
import argparse
import json
from pathlib import Path
import subprocess
import time
from urllib.error import URLError
from urllib.request import Request,urlopen


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action',choices=['install','start','stop','status'])
    parser.add_argument('--directory',type=Path,required=True)
    args=parser.parse_args()
    work=args.directory.resolve();profile=json.loads((work/'profile.json').read_text());bundle=Path(profile['bundle'])
    command=['docker','compose','-f',str(work/'compose.json')]
    def compose(*parts):subprocess.run([*command,*parts],check=True)
    if args.action=='status':compose('--profile','native-edge','ps');return
    if args.action=='stop':compose('--profile','native-edge','stop');return
    marker=work/'installed.json'
    if args.action=='start':
        if not marker.exists():raise SystemExit('Run install for the prepared deployment first')
        compose('--profile','native-edge','up','-d','--pull','never');return
    if marker.exists():raise SystemExit('Installation already recorded; use start')
    credentials=json.loads((work/'deployment-credentials.json').read_text())
    def request(base,path,value=None,token=''):
        with urlopen(Request(base+path,data=None if value is None else json.dumps(value).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+token}),timeout=20) as response:return json.load(response)
    def wait(check,label,seconds=120):
        until=time.monotonic()+seconds
        while time.monotonic()<until:
            try:
                value=check()
                if value:return value
            except (URLError,TimeoutError,ConnectionError):pass
            time.sleep(1)
        raise TimeoutError(label)
    initialized=work/'database-initialized.json'
    if not initialized.exists() and not (work/'tb-bootstrap.json').exists():
        compose('--profile','install','run','--rm','tb-install')
        initialized.write_text(json.dumps({'initialized_ms':int(time.time()*1000)})+'\n')
    core=['thingsboard','config','cloud',*profile['nodes'],'web']
    if profile['profile']=='site':core += ['nats-1','nats-2','nats-3']
    compose('up','-d','--pull','never',*core)
    if not (work/'tb-bootstrap.json').exists():
        subprocess.run(['python3',str(bundle/'scripts/bootstrap-thingsboard.py'),'--url',profile['native_cloud_url'],'--state-directory',str(work),'--edges',','.join(profile['nodes'])],check=True)
    cloud=profile['business_cloud_url']
    wait(lambda:request(cloud,'/health')['status']=='ok','cloud startup')
    for node in profile['nodes']:
        subprocess.run(['python3',str(bundle/'scripts/enroll-local-edge.py'),'--state-directory',str(work),'--cloud-url',cloud,'--node',node],check=True)
    compose('--profile','native-edge','up','-d','--pull','never',*[node+'-native' for node in profile['nodes']])
    ct=request(cloud,'/api/sf/v1/login',{'login':'admin','password':credentials['password']})['token']
    for index,node in enumerate(profile['nodes']):
        port=8091 if index==0 else 8092+index;base='http://127.0.0.1:'+str(port)
        et=wait(lambda:request(base,'/api/sf/v1/login',{'login':'admin','password':credentials['password']}).get('token'),node+' offline permission bundle')
        native='http://127.0.0.1:'+str(18081+index)
        wait(lambda:request(native,'/api/auth/login',{'username':'platform@smartfactory.local','password':credentials['tb_admin_password']}).get('token'),node+' native platform synchronization',600)
        def definitions_ready():
            queues=request(base,'/api/sf/v1/runtime',token=et)['queues']
            return all(row['count']==0 for row in queues if row['kind'] in ['tb_entity','tb_definition'])
        wait(definitions_ready,node+' native definition installation',300)
        compose('up','-d','--pull','never',node+'-simulator',node+'-gateway')
        if profile['simulation'] in ['fleet','scenes']:
            expected=(500 if profile['profile']=='capacity' else 50) if profile['simulation']=='fleet' else 5
            scene_devices={'climate-1','light-1','gas-1','agv-1','counter-1'}
            def selected(entity):
                return entity['id'].startswith(node+'-device-') if profile['simulation']=='fleet' else entity['id'] in scene_devices
            def discovered():
                rows=request(base,'/api/sf/v1/entities',token=et)
                targets=[e for e in rows if e['kind']=='device' and e.get('edge_id')==node and selected(e)]
                return targets if len(targets)==expected else None
            for entity in wait(discovered,node+' device discovery'):
                if entity['status']!='approved':
                    entity['status']='approved'
                    request(base,'/api/sf/v1/entities',{'entity':entity,'expected_version':entity['version']},et)
            def replicated():
                rows=request(cloud,'/api/sf/v1/entities',token=ct)
                targets=[e for e in rows if selected(e) and e['status']=='approved']
                return targets if len(targets)==expected else None
            for entity in wait(replicated,node+' approved metadata synchronization'):
                if profile['simulation']=='fleet' and entity.get('parent_id')!='factory':
                    entity['parent_id']='factory'
                    request(cloud,'/api/sf/v1/entities',{'entity':entity,'expected_version':entity['version']},ct)
        def native_ready():
            queues=request(base,'/api/sf/v1/runtime',token=et)['queues']
            return all(row['count']==0 for row in queues if row['kind'] in ['tb_entity','tb_definition']) and sum(row['count'] for row in queues if row['kind']=='tb_telemetry')<=1000
        wait(native_ready,node+' native definitions and startup telemetry backlog',900)
    if profile['simulation']=='fleet':
        nodes=profile['nodes'];devices=[n+'-device-000' for n in nodes]
        definition={'schema_version':'1.0','id':'site-gas-response','name':'现场烟雾联动排风','kind':'strategy','group_id':'factory','status':'draft','version':0,
            'selector':{'device_ids':[devices[0]],'keys':['smoke']},
            'nodes':[{'id':'input','type':'input'},{'id':'threshold','type':'threshold','params':{'operator':'>','value':1}},{'id':'action','type':'action'}],
            'connections':[{'from':'input','to':'threshold'},{'from':'threshold','to':'action'}],
            'policy':{'edge_ids':nodes,'risk_category':'business','risk_level':1,
                      'conditions':[{'device_id':d,'key':'interlock','operator':'==','value':False,'interlock':True,'max_age_ms':15000} for d in devices],
                      'steps':[{'id':'extract-'+n,'edge_id':n,'device_id':d,'action':'set_extractor','params':{'value':'true'},'idempotent':True,'timeout_ms':5000} for n,d in zip(nodes,devices)],
                      'degraded':[{'id':'local-extract-'+n,'edge_id':n,'device_id':d,'action':'set_extractor','params':{'value':'true'},'idempotent':True,'timeout_ms':5000} for n,d in zip(nodes,devices)]}}
        request(cloud,'/api/sf/v1/drafts',{'draft':{'id':definition['id'],'definition':definition,'base_version':0},'expected_version':0},ct)
        request(cloud,'/api/sf/v1/drafts/'+definition['id']+'/publish',{},ct)
        current=next(d for d in request(cloud,'/api/sf/v1/dashboards',token=ct) if d['id']=='factory')
        current.update(device_ids=devices,keys=['smoke','extractor'],metrics=[{'device_id':d,'key':'smoke','label':n+' 烟雾'} for n,d in zip(nodes,devices)])
        request(cloud,'/api/sf/v1/dashboards',{'dashboard':current,'expected_version':current['version']},ct)
    marker.write_text(json.dumps({'installed_ms':int(time.time()*1000),'profile':profile},ensure_ascii=False,indent=2)+'\n')
    print('Initialized business permissions, native platform and simulation devices. Browser URLs: '+', '.join(profile['urls']))


if __name__=='__main__':main()
