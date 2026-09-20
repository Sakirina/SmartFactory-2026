#!/usr/bin/env python3
"""Exercise three application processes, 150 real MQTT devices and a NATS quorum.

All high-write databases reside in tmpfs or process memory. The PostgreSQL
instance contains three independent databases; UOS production uses three hosts.
Only containers and child processes created by this invocation are interrupted.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import signal
import socket
import subprocess
import time
from urllib.error import URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary-dir', type=Path, default=Path('/private/tmp/smartfactory-build/bin'))
    parser.add_argument('--faults', type=int, default=100)
    parser.add_argument('--postgres-data-mib', type=int, default=1536, help='bounded tmpfs capacity for all three databases')
    parser.add_argument('--output', type=Path, default=ROOT / '.local/evidence/site-150-faults.json')
    args = parser.parse_args()
    if not 4 <= args.faults <= 1000:
        parser.error('faults must be 4..1000')
    if not 512 <= args.postgres_data_mib <= 4096:
        parser.error('postgres-data-mib must be 512..4096')
    os.umask(0o077)
    work = ROOT / '.local/site-memory'
    compose = work / 'compose.json'
    if not compose.exists():
        raise SystemExit('Run scripts/prepare-site.py --memory first')
    private = json.loads((work / 'credentials.json').read_text())
    binaries = args.binary_dir.resolve()
    names = ['sf-site-fixture', 'datatransfer', 'dt-simulator']
    report = {'started_ms': int(time.time()*1000), 'platform': platform.platform(),
              'source_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
              'binary_sha256': {n: hashlib.sha256((binaries/n).read_bytes()).hexdigest() for n in names},
              'nodes': 3, 'devices_per_node': 50, 'protocol': 'MQTT 5.0', 'sampling_ms': 5000,
              'storage': 'three PostgreSQL databases in one bounded tmpfs fixture; three in-memory DataTransfer journals and simulator stores; three independent NATS tmpfs stores',
              'lease_ms': 5000, 'production_default_lease_ms': 15000, 'offline_ms': 15000,
              'cloud_connected': False, 'tests': [], 'passed': False,
              'full_A09_24h': False, 'postgres_tmpfs_mib': args.postgres_data_mib, 'storage_samples': []}
    pg_name = 'smartfactory-site-memory-postgres'
    if subprocess.run(['docker', 'inspect', pg_name], capture_output=True).returncode == 0:
        raise SystemExit('This fixture already exists; finish its current owner before retrying')
    for port in [54323, 18101, 18102, 18103, 18111, 18112, 18113, 18121, 18122, 18123, 50101, 50102, 50103, 18901, 18902, 18903]:
        with socket.socket() as probe:
            probe.settimeout(0.1)
            if probe.connect_ex(('127.0.0.1', port)) == 0:
                raise SystemExit('Fixture port already in use: '+str(port))
    processes, edges, paused = [], {}, []
    pg_started, nats_started = False, False
    def stop_signal(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGINT, stop_signal)
    signal.signal(signal.SIGTERM, stop_signal)
    def docker(*parts):
        out=subprocess.run(['docker', *parts], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        if out.returncode:
            raise RuntimeError('Docker fixture command failed: '+out.stderr.strip())
    def nats(*parts):
        docker('compose', '-f', str(compose), *parts)
    def start(name, command, environment=None):
        with (work/(name+'.log')).open('ab') as log:
            child = subprocess.Popen([str(v) for v in command], cwd=ROOT, env=environment, stdout=log, stderr=subprocess.STDOUT)
        processes.append(child)
        return child
    def request(port, path, value=None, token=''):
        with urlopen(Request(f'http://127.0.0.1:{port}'+path, data=None if value is None else json.dumps(value).encode(),
                             headers={'Content-Type':'application/json','Authorization':'Bearer '+token}), timeout=5) as response:
            return json.load(response)
    def wait(check, label, seconds=35):
        until = time.monotonic()+seconds
        while time.monotonic() < until:
            try:
                value=check()
                if value:
                    return value
            except (URLError, TimeoutError, ConnectionError):
                pass
            time.sleep(0.05)
        raise TimeoutError(label)
    def edge_start(index):
        node=f'edge-{chr(97+index)}'
        edges[index]=start(node,[binaries/'sf-site-fixture','--config',work/'fixture.json','--node',node])
        wait(lambda: request(18101+index,'/health').get('status')=='ok',node+' startup')
    tokens={}
    def login(index):
        tokens[index]=request(18101+index,'/api/sf/v1/login',{'login':'admin','password':password})['token']
    def commands(index, execution):
        return [c for c in request(18111+index,'/commands') if c['command_id'].startswith(execution+':')]
    def result(execution, expected, owner=None):
        for index in range(3):
            if edges[index].poll() is not None:
                continue
            try:
                rows=request(18101+index,'/api/sf/v1/executions',token=tokens[index])
            except (URLError, TimeoutError, ConnectionError):
                continue
            for row in rows:
                if row['downlink_id']==execution and row['status'] in expected:
                    if owner is None or row.get('coordinator_id')!=owner:
                        return row
        return None
    try:
        password=secrets.token_urlsafe(24)
        image=json.loads((ROOT/'deploy/compose.native.json').read_text())['services']['cloud-db']['image']
        environment=dict(os.environ, POSTGRES_PASSWORD=password)
        subprocess.run(['docker','run','--detach','--rm','--pull','never','--platform','linux/amd64','--name',pg_name,
                        '--label','smartfactory.fixture=site-memory','--memory',str(args.postgres_data_mib+256)+'m','--memory-swap',str(args.postgres_data_mib+256)+'m',
                        '--tmpfs','/var/lib/postgresql/data:rw,size='+str(args.postgres_data_mib)+'m','--shm-size','64m',
                        '--publish','127.0.0.1:54323:5432','--env','POSTGRES_PASSWORD','--env','POSTGRES_USER=smartfactory',
                        '--log-driver','none',image,'postgres','-c','shared_buffers=48MB','-c','max_wal_size=96MB','-c','min_wal_size=32MB'],
                       env=environment,check=True,stdout=subprocess.DEVNULL)
        pg_started=True
        wait(lambda: subprocess.run(['docker','exec',pg_name,'pg_isready','-h','127.0.0.1','-U','smartfactory'],capture_output=True).returncode==0,'memory PostgreSQL')
        nodes=[]
        for index in range(3):
            node=f'edge-{chr(97+index)}'
            db='site_'+chr(97+index)
            docker('exec',pg_name,'createdb','-U','smartfactory',db)
            nodes.append({'id':node,'dsn':f'postgres://smartfactory:{quote(password)}@127.0.0.1:54323/{db}?sslmode=disable',
                          'listen':f'127.0.0.1:{18101+index}','gateway':f'127.0.0.1:{50101+index}'})
        config={'directory':str(work),'password':password,'token':private['token'],
                'nats_url':','.join(f'tls://127.0.0.1:{15222+i}' for i in range(3)),
                'prefix':'site_'+str(int(time.time())), 'nodes':nodes}
        (work/'fixture.json').write_text(json.dumps(config,indent=2)+'\n')
        nats('up','-d','--pull','never')
        nats_started=True
        seed=subprocess.check_output([str(binaries/'sf-site-fixture'),'--mode','seed','--config',str(work/'fixture.json')],cwd=ROOT,text=True)
        report['fixture']=json.loads(seed)
        for index,node in enumerate(nodes):
            folder=work/node['id'];folder.mkdir(exist_ok=True)
            start(node['id']+'-sim',[binaries/'dt-simulator','--fleet-count','50','--fleet-prefix',node['id'],
                  '--state',':memory:','--config-out',folder/'datatransfer.yaml','--http',f'127.0.0.1:{18111+index}',
                  '--mqtt',f'127.0.0.1:{18901+index}','--gateway-grpc',node['gateway'],'--gateway-http',f'127.0.0.1:{18121+index}',
                  '--interval','5s','--command-reply-delay','400ms'])
            wait(lambda: len(request(18111+index,'/state')['devices'])==50,'fleet startup')
            start(node['id']+'-dt',[binaries/'datatransfer','--config',folder/'datatransfer.yaml'])
            edge_start(index);login(index)
        time.sleep(8)
        def execute(execution):
            subprocess.run([str(binaries/'sf-site-fixture'),'--mode','enqueue','--config',str(work/'fixture.json'),
                            '--node','edge-a','--execution-id',execution],check=True,cwd=ROOT,stdout=subprocess.DEVNULL)
        def ready():
            return subprocess.run([str(binaries/'sf-site-fixture'),'--mode','ready','--config',str(work/'fixture.json'),
                                   '--node','edge-a'],cwd=ROOT,capture_output=True,timeout=15).returncode==0
        def assert_actions(execution, indexes, degraded=False):
            counts=[len(commands(i,execution)) for i in range(3)]
            if counts != [1 if i in indexes else 0 for i in range(3)]:
                raise AssertionError(f'{execution}: unexpected physical actions {counts}')
            for i in indexes:
                c=commands(i,execution)[0]
                suffix=('local-extract-' if degraded else 'extract-')+nodes[i]['id']
                if c['command_id']!=execution+':'+suffix or c['result']['status']!='SUCCESS':
                    raise AssertionError(f'{execution}: incorrect device result')
            return counts
        wait(ready,'initial policy agreement')
        execute('site-baseline')
        baseline=wait(lambda:result('site-baseline',{'completed'}),'baseline all-node execution')
        report['baseline']={'execution':baseline,'physical_actions':assert_actions('site-baseline',{0,1,2})}
        for trial in range(args.faults):
            kind=['quorum_partition','single_replica_loss','coordinator_process_loss','participant_process_loss'][trial%4]
            execution=f'site-fault-{trial:03d}'
            started=time.monotonic()
            if kind=='quorum_partition':
                nats('pause','nats-2','nats-3')
                try:
                    execute(execution)
                    value=wait(lambda:result(execution,{'degraded_completed'}),'local degraded execution',45)
                    counts=assert_actions(execution,{0},True)
                finally:
                    nats('unpause','nats-2','nats-3')
                wait(ready,'majority restored')
                time.sleep(3)
            elif kind=='single_replica_loss':
                nats('pause','nats-3')
                try:
                    time.sleep(4)
                    wait(ready,'two-replica majority available')
                    execute(execution)
                    value=wait(lambda:result(execution,{'completed'}),'two-replica majority execution',45)
                    counts=assert_actions(execution,{0,1,2})
                finally:
                    nats('unpause','nats-3')
                time.sleep(2)
            elif kind=='coordinator_process_loss':
                execute(execution)
                wait(lambda: len(commands(1,execution))==1,'second physical step started')
                edges[0].kill();edges[0].wait(timeout=5)
                value=wait(lambda:result(execution,{'completed'},'edge-a'),'coordinator takeover after process loss',14)
                counts=assert_actions(execution,{0,1,2})
                edge_start(0);login(0)
                time.sleep(3)
            else:
                edges[1].send_signal(signal.SIGSTOP);paused.append(edges[1])
                try:
                    time.sleep(16)
                    execute(execution)
                    value=wait(lambda:result(execution,{'degraded_completed'}),'stale participant degraded execution',30)
                    counts=assert_actions(execution,{0},True)
                finally:
                    edges[1].send_signal(signal.SIGCONT);paused.remove(edges[1])
                time.sleep(3)
            report['tests'].append({'number':trial+1,'fault':kind,'execution_id':execution,'status':value['status'],
                                    'coordinator_id':value.get('coordinator_id'),'fence':value.get('fence'),
                                    'physical_actions':counts,'elapsed_ms':round((time.monotonic()-started)*1000)})
            if (trial+1)%4==0:
                raw=subprocess.check_output(['docker','exec',pg_name,'df','-Pk','/var/lib/postgresql/data'],text=True)
                fields=raw.splitlines()[-1].split()
                report['storage_samples'].append({'trial':trial+1,'total_kib':int(fields[1]),'used_kib':int(fields[2]),'available_kib':int(fields[3])})
            print(f"{trial+1}/{args.faults} {kind}: {value['status']} actions={counts}",flush=True)
            args.output.parent.mkdir(parents=True,exist_ok=True)
            args.output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
        execute('site-final-recovery')
        wait(lambda:result('site-final-recovery',{'completed'}),'final healthy recovery')
        report['final_physical_actions']=assert_actions('site-final-recovery',{0,1,2})
        report['passed']=True
        report['duplicate_physical_actions']=0
    except BaseException as err:
        report['error']=str(err)
        report['failure_state']={}
        report['failure_commands']={}
        for index in range(3):
            if index in tokens and index in edges and edges[index].poll() is None and edges[index] not in paused:
                try:
                    report['failure_state'][str(index)]=request(18101+index,'/api/sf/v1/executions',token=tokens[index])
                except Exception:
                    pass
            try:
                report['failure_commands'][str(index)]=request(18111+index,'/commands')
            except Exception:
                pass
        raise
    finally:
        for child in paused:
            child.send_signal(signal.SIGCONT)
        if nats_started:
            subprocess.run(['docker','compose','-f',str(compose),'unpause'],capture_output=True)
        for child in reversed(processes):
            if child.poll() is None: child.terminate()
        for child in reversed(processes):
            try: child.wait(timeout=8)
            except subprocess.TimeoutExpired: child.kill();child.wait()
        if nats_started:
            subprocess.run(['docker','compose','-f',str(compose),'down'],capture_output=True)
        if pg_started:
            docker('stop','--timeout','10',pg_name)
        report['finished_ms']=int(time.time()*1000)
        args.output.parent.mkdir(parents=True,exist_ok=True)
        args.output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')


if __name__=='__main__':
    main()
