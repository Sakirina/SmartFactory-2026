#!/usr/bin/env python3
"""Measure a prepared persistent UOS deployment for offline recovery or storage.

Full runs last at least 24 actual hours. Development smoke runs require an
explicit flag and are reported separately. The script operates only containers
belonging to the selected generated Compose project and restores its own pauses
and traffic shaping before returning.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import time
from urllib.request import Request, urlopen


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory',type=Path,required=True)
    parser.add_argument('--mode',choices=['offline','storage'],required=True)
    parser.add_argument('--duration-seconds',type=int,default=86400)
    parser.add_argument('--recovery-seconds',type=int,default=21600)
    parser.add_argument('--sample-seconds',type=int,default=60)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--backup-output',type=Path)
    parser.add_argument('--uos-iso-version',default='')
    parser.add_argument('--uos-iso-sha256',default='')
    parser.add_argument('--development-smoke',action='store_true')
    parser.add_argument('--plan-only',action='store_true')
    args=parser.parse_args()
    if args.duration_seconds<30 or args.recovery_seconds<30 or not 5<=args.sample_seconds<=300:parser.error('duration/recovery >= 30 and sample interval 5..300 required')
    work=args.directory.resolve();profile=json.loads((work/'profile.json').read_text());compose_path=work/'compose.json'
    configuration=json.loads(compose_path.read_text());nodes=profile['nodes'];bundle=Path(profile['bundle'])
    if profile['simulation']!='fleet':raise SystemExit('Use a capacity/site fleet deployment for long-duration acceptance')
    full=args.duration_seconds>=86400 and not args.development_smoke
    if not full and not args.development_smoke:raise SystemExit('Short runs require --development-smoke and never pass full A08/A09/A12')
    if args.plan_only:
        print(json.dumps({'profile':profile['profile'],'mode':args.mode,'duration_seconds':args.duration_seconds,'full_duration_run':full,
                          'paused_during_offline':['cloud','config','thingsboard'] if args.mode=='offline' else [],
                          'recovery_link':'100 Mbps aggregate, configured 20 ms round-trip delay; peer-filtered netem in cloud and node network namespaces',
                          'sample_interval_seconds':args.sample_seconds,'backup_output':str(args.backup_output) if args.backup_output else None},ensure_ascii=False,indent=2));return
    if not (work/'installed.json').is_file():raise SystemExit('Complete bootstrap-runtime.py install first')
    os_release=Path('/etc/os-release').read_text() if Path('/etc/os-release').exists() else ''
    if full:
        if platform.system()!='Linux' or not any(name in os_release.lower() for name in ['uniontech','uos']):raise SystemExit('Full acceptance requires the UOS target system')
        if not args.uos_iso_version or len(args.uos_iso_sha256)!=64 or any(c not in '0123456789abcdefABCDEF' for c in args.uos_iso_sha256):raise SystemExit('Record --uos-iso-version and the 64-character --uos-iso-sha256')
        if any(service.get('tmpfs') for name,service in configuration['services'].items() if name.endswith('-db')):raise SystemExit('Full storage/offline measurement requires persistent PostgreSQL volumes')
        if args.mode=='storage' and not args.backup_output:raise SystemExit('Full A12 requires --backup-output to measure backup size')
    shaping=full and args.mode=='offline'
    if shaping and (os.geteuid()!=0 or not shutil.which('nsenter') or not shutil.which('tc')):raise SystemExit('Recovery shaping requires root, nsenter and tc on the UOS Docker host')
    os.umask(0o077)
    credentials=json.loads((work/'development-credentials.json').read_text())
    compose=['docker','compose','-f',str(compose_path),'--profile','native-edge']
    def command(parts,timeout=30):return subprocess.check_output(parts,text=True,stderr=subprocess.PIPE,timeout=timeout).strip()
    containers={}
    for name in configuration['services']:
        if name in ['tb-install','kafka-storage-init']:continue
        container=command([*compose,'ps','--quiet',name])
        if not container:raise RuntimeError('Required service is not running: '+name)
        detail=json.loads(command(['docker','inspect',container]))[0]
        if detail['Config']['Labels'].get('com.docker.compose.project')!=profile['project']:raise RuntimeError('Unexpected container ownership')
        containers[name]=detail
    def request(base,route,value=None,token=''):
        with urlopen(Request(base+route,data=None if value is None else json.dumps(value).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+token}),timeout=20) as response:return json.load(response)
    def login(base):return request(base,'/api/sf/v1/login',{'login':'admin','password':credentials['password']})['token']
    addresses={node:'http://127.0.0.1:'+str(8091 if i==0 else 8092+i) for i,node in enumerate(nodes)}
    simulator={node:'http://127.0.0.1:'+str(19083+i) for i,node in enumerate(nodes)}
    cloud=profile['business_cloud_url']
    def snapshot(include_cloud=True):
        result={'at_ms':int(time.time()*1000),'nodes':{},'databases':{},'storage':{}}
        for node,base in addresses.items():
            token=login(base);state=request(simulator[node],'/state')
            if len(state['devices'])!=(500 if profile['profile']=='capacity' else 50):raise RuntimeError('Unexpected simulator fleet: '+node)
            recent=request(base,'/api/sf/v1/data?device_ids='+node+'-device-000&keys=smoke&limit=1',token=token)
            if not recent['points'] or int(time.time()*1000)-recent['points'][-1]['observed_ms']>15000:raise RuntimeError('Collection stopped or stale at '+node)
            with urlopen(base+'/edge',timeout=20) as page:
                if page.status!=200:raise RuntimeError('Local page unavailable: '+node)
            audit=request(base,'/api/sf/v1/audit/verify',{},token)
            if not audit['valid']:raise RuntimeError('Audit integrity failed at '+node)
            result['nodes'][node]={'mqtt_messages':state['mqtt_messages'],'pending_device_events':state['pending_events'],
                                   'latest_ms':recent['points'][-1]['observed_ms'],'runtime':request(base,'/api/sf/v1/runtime',token=token),'audit_valid':True}
        databases={'cloud-db':['smartfactory','thingsboard','sf_config'],**{node+'-db':['smartfactory','tb_edge'] for node in nodes}}
        for service,names in databases.items():
            container=containers[service]['Id']
            result['storage'][service]=command(['docker','exec',container,'du','-sk','/var/lib/postgresql/data'])
            result['storage'][service+'-free']=command(['docker','exec',container,'df','-Pk','/var/lib/postgresql/data'])
            free=int(result['storage'][service+'-free'].splitlines()[-1].split()[3])*1024
            if full and free<5*1024**3:raise RuntimeError('Test stopped with less than 5 GiB available at '+service)
            for database in names:
                sql="SELECT json_build_object('database_bytes',pg_database_size(current_database()),'tables',coalesce((SELECT json_agg(json_build_object('name',relname,'bytes',pg_total_relation_size(relid),'estimated_rows',n_live_tup)) FROM pg_stat_user_tables),'[]'::json))"
                result['databases'][service+'/'+database]=json.loads(command(['docker','exec',container,'psql','-U','postgres','-d',database,'-tAc',sql]))
        if include_cloud:
            token=login(cloud);result['cloud_runtime']=request(cloud,'/api/sf/v1/runtime',token=token);result['cloud_jobs']=request(cloud,'/api/sf/v1/jobs',token=token)
        return result
    def probe_action():
        before={node:{c['command_id'] for c in request(simulator[node],'/commands')} for node in nodes}
        for node in nodes:request(simulator[node],'/state',{'device_id':node+'-device-000','values':{'smoke':0,'extractor':False}})
        time.sleep(6)
        request(simulator[nodes[0]],'/state',{'device_id':nodes[0]+'-device-000','values':{'smoke':10}})
        until=time.monotonic()+30
        while time.monotonic()<until:
            if all(request(simulator[node],'/state')['devices'][node+'-device-000']['extractor'] is True for node in nodes):break
            time.sleep(.5)
        else:raise TimeoutError('Published offline strategy did not change all target devices')
        actions={node:[c for c in request(simulator[node],'/commands') if c['command_id'] not in before[node]] for node in nodes}
        if not all(len(commands)==1 and commands[0]['result']['status']=='SUCCESS' for commands in actions.values()):raise RuntimeError('Unexpected command count or result in offline action probe')
        request(simulator[nodes[0]],'/state',{'device_id':nodes[0]+'-device-000','values':{'smoke':0}})
        return {'at_ms':int(time.time()*1000),'commands':actions}
    shaped=[];paused=[]
    def configure_link():
        network=next(iter(containers['cloud']['NetworkSettings']['Networks']))
        cloud_ip=containers['cloud']['NetworkSettings']['Networks'][network]['IPAddress']
        peers=[containers[node+'-network']['NetworkSettings']['Networks'][network]['IPAddress'] for node in nodes]
        for name,targets in [('cloud',peers),*[(node+'-network',[cloud_ip]) for node in nodes]]:
            pid=containers[name]['State']['Pid'];prefix=['nsenter','--target',str(pid),'--net','tc']
            original=command([*prefix,'qdisc','show','dev','eth0'])
            if original and 'noqueue' not in original:raise RuntimeError('Existing traffic shaping must be reviewed before this test: '+name)
            command([*prefix,'qdisc','add','dev','eth0','root','handle','1:','prio','bands','3','priomap',*(['0']*16)])
            shaped.append((name,prefix))
            command([*prefix,'qdisc','add','dev','eth0','parent','1:3','handle','30:','netem','delay','10ms','rate','100mbit'])
            for target in targets:command([*prefix,'filter','add','dev','eth0','protocol','ip','parent','1:','prio','1','u32','match','ip','dst',target+'/32','flowid','1:3'])
    def clear_link():
        while shaped:
            _,prefix=shaped.pop();subprocess.run([*prefix,'qdisc','del','dev','eth0','root'],check=False,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    report={'format':'smartfactory-long-runtime-v1','started_ms':int(time.time()*1000),'mode':args.mode,'profile':profile['profile'],
            'environment':platform.platform(),'os_release':os_release,'iso_version':args.uos_iso_version,'iso_sha256':args.uos_iso_sha256,
            'release_manifest_sha256':hashlib.sha256((bundle/'release-manifest.json').read_bytes()).hexdigest(),
            'actual_duration_seconds':0,'samples':[],'recovery_samples':[],'action_probes':[],'full_duration':full,'passed':False,'full_A08':False,'full_A09':False,'full_A12':False,
            'link_shaping_applied':False,'site_fault_tests':'Use check-site.py evidence alongside this sustained runtime report; it is a separate fixture'}
    def save():args.output.parent.mkdir(parents=True,exist_ok=True);args.output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    try:
        report['baseline']=snapshot()
        if args.mode=='offline':
            for name in ['cloud','config','thingsboard']:
                command(['docker','pause',containers[name]['Id']]);paused.append(name)
        start=time.monotonic()
        next_probe=0
        while True:
            if time.monotonic()-start>=next_probe:
                report['action_probes'].append(probe_action());next_probe=time.monotonic()-start+3600
            report['samples'].append(snapshot(include_cloud=args.mode!='offline'))
            report['actual_duration_seconds']=time.monotonic()-start;save()
            print(f"{args.mode}: {report['actual_duration_seconds']:.1f}s, all local pages, collection, permissions and audit checks passed",flush=True)
            remaining=args.duration_seconds-report['actual_duration_seconds']
            if remaining<=0:break
            time.sleep(min(args.sample_seconds,remaining))
        # Fleet telemetry contains three configured points per MQTT message.
        # Action responses also use MQTT, so subtract the verified device commands.
        last=report['samples'][-1];baseline=report['baseline']
        elapsed=(last['at_ms']-baseline['at_ms'])/1000
        command_count=sum(len(commands) for probe in report['action_probes'] for commands in probe['commands'].values())
        messages=sum(last['nodes'][node]['mqtt_messages']-baseline['nodes'][node]['mqtt_messages'] for node in nodes)
        report['fleet_point_rate_estimate']=(messages-command_count)*3/elapsed
        report['point_rate_basis']='successful simulator MQTT publications minus verified command replies, times the three configured telemetry fields'
        if full and profile['profile']=='capacity' and report['fleet_point_rate_estimate']<495:raise RuntimeError('Source load fell below 99 percent of the configured 500 points/s')
        if args.mode=='offline':
            if shaping:configure_link();report['link_shaping_applied']=True
            report['recovery_started_ms']=int(time.time()*1000)
            for name in list(paused):command(['docker','unpause',containers[name]['Id']]);paused.remove(name)
            deadline=time.monotonic()+args.recovery_seconds
            while time.monotonic()<deadline:
                try:sample=snapshot()
                except (OSError,TimeoutError):time.sleep(min(args.sample_seconds,10));continue
                report['recovery_samples'].append(sample);save()
                old=report['recovery_started_ms']
                backlog=any(q['kind']=='cloud_observation' and q['oldest_ms']<old for node in sample['nodes'].values() for q in node['runtime']['queues'])
                jobs=any(j['status']!='completed' and j.get('from_ms',0)<old for j in sample['cloud_jobs'])
                if not backlog and not jobs:
                    report['recovery_seconds']=(int(time.time()*1000)-old)/1000;break
                time.sleep(args.sample_seconds)
            else:raise TimeoutError('Offline backlog or historical recomputation exceeded the recovery duration')
            clear_link()
        if args.backup_output:
            non_databases=[name for name in containers if not name.endswith('-db')]
            command([*compose,'stop',*non_databases],timeout=180)
            try:
                backup=['python3',str(bundle/'scripts/backup-state.py'),'create','--output',str(args.backup_output),'--quiesced']
                for service,names in {'cloud-db':['smartfactory','thingsboard','sf_config'],**{n+'-db':['smartfactory','tb_edge'] for n in nodes}}.items():
                    for database in names:backup+=['--postgres',service+'-'+database+'='+containers[service]['Id']+','+database+',postgres']
                for node in nodes:
                    for name in ['datatransfer-state.db','simulator.db']:
                        if (work/node/name).is_file():backup+=['--sqlite',node+'-'+name+'='+str(work/node/name)]
                for index,file in enumerate(sorted(p for p in work.rglob('*') if p.is_file() and (p.parent==work or p.parent==work/'pki'))):backup+=['--file','private-'+str(index)+'='+str(file)]
                command(backup,timeout=14400)
                report['backup_bytes']=sum(p.stat().st_size for p in args.backup_output.rglob('*') if p.is_file())
            finally:command([*compose,'up','-d','--pull','never'],timeout=240)
        report['passed']=True
        report['full_A08']=full and args.mode=='offline' and profile['profile']=='capacity' and shaping
        report['full_A12']=full and args.mode=='storage' and bool(args.backup_output)
        # A09 additionally needs the declared fault scenarios on the target system.
        report['full_A09']=False
    except BaseException as error:
        report['error']=str(error);raise
    finally:
        for name in paused:subprocess.run(['docker','unpause',containers[name]['Id']],check=False,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        clear_link();report['finished_ms']=int(time.time()*1000);save()


if __name__=='__main__':main()
