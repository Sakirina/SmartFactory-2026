#!/usr/bin/env python3
"""Run independent cloud/config and production subscriber processes without exposing credentials."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import ssl
import subprocess
import time
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[1]


def run():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--keep-running', action='store_true', help='leave frontend fixtures running after verification')
    parser.add_argument('--frontend',action='store_true',help='leave the initial two-node fixture available for frontend operations')
    args = parser.parse_args()
    directory = args.directory.resolve()
    directory.mkdir(parents=True, exist_ok=False)
    os.chmod(directory, 0o700)
    private = directory / 'private'
    private.mkdir()
    evidence = directory / 'evidence'
    evidence.mkdir()
    binaries = directory / 'bin'
    binaries.mkdir()
    driver_archive=evidence/'verification-driver.py'
    driver_archive.write_bytes(Path(__file__).read_bytes())
    driver_archive.chmod(0o600)
    commands, http_records, assertions, processes = [], [], [], {}

    def source_capture(name, argv, cwd, env):
        targets=[item for item in argv if item.startswith('./cmd/')]
        listed=subprocess.run(['go','list','-deps','-json',*targets],cwd=cwd,env=env,capture_output=True,text=True,check=True)
        (evidence/(name+'.go-list.json')).write_text(listed.stdout)
        decoder=json.JSONDecoder(); packages=[];position=0
        while position<len(listed.stdout):
            while position<len(listed.stdout) and listed.stdout[position].isspace():position+=1
            if position==len(listed.stdout):break
            package,position=decoder.raw_decode(listed.stdout,position);packages.append(package)
        inputs=set()
        for package in packages:
            package_dir=Path(package['Dir'])
            for field in ['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','FFiles','HFiles','SFiles','SysoFiles','EmbedFiles']:
                for filename in package.get(field,[]):inputs.add(package_dir/filename)
            module=package.get('Module',{}); module=module.get('Replace',module)
            if module.get('GoMod'):inputs.add(Path(module['GoMod']))
        for module in ['Platform','DataTransfer']:
            for filename in ['go.mod','go.sum']:inputs.add(ROOT/module/filename)
        rows=[];archive=evidence/'fixed-inputs';archive.mkdir(exist_ok=True)
        for path in sorted(inputs):
            data=path.read_bytes();digest=hashlib.sha256(data).hexdigest();stored=archive/digest
            if not stored.exists():stored.write_bytes(data)
            rows.append({'path':str(path),'sha256':digest,'archived_path':str(stored),'bytes':len(data)})
        path=evidence/(name+'.source-inputs.json');save(path,rows);return str(path)

    def save(path, value):
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
        path.chmod(0o600)

    def command(name, argv, cwd=ROOT, env=None):
        before=source_capture(name+'-before',argv,cwd,env) if name.startswith('build-') else None
        start = time.time()
        completed = subprocess.run(argv, cwd=cwd, env=env, capture_output=True, text=True)
        stdout, stderr = evidence / (name + '.stdout.log'), evidence / (name + '.stderr.log')
        stdout.write_text(completed.stdout)
        stderr.write_text(completed.stderr)
        after=source_capture(name+'-after',argv,cwd,env) if name.startswith('build-') else None
        unchanged=json.loads(Path(before).read_text())==json.loads(Path(after).read_text()) if before else None
        commands.append({'name': name, 'argv': argv, 'cwd': str(cwd), 'exit_code': completed.returncode, 'started_at': start, 'elapsed_seconds': time.time() - start, 'stdout': str(stdout), 'stderr': str(stderr),'source_before':before,'source_after':after,'input_unchanged':unchanged})
        save(evidence / 'commands.json', commands)
        if completed.returncode or unchanged is False:
            save(directory/'result.json',{'status':'failed','error':name+' failed','commands':str(evidence/'commands.json')})
            raise RuntimeError(name + ' failed')
        return completed.stdout

    build_env = dict(os.environ, GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath', GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache', GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOPROXY='off',GOMAXPROCS='2')
    command('go-version', ['go', 'version'], ROOT / 'Platform', build_env)
    for name in ['sf-cloud', 'sf-edge', 'sf-pki', 'sf-node-configuration-fixture', 'sf-contracts']:
        command('build-' + name, ['go', 'build', '-o', str(binaries / name), './cmd/' + name], ROOT / 'Platform', build_env)
    command('build-dt-node-configuration-fixture', ['go', 'build', '-o', str(binaries / 'dt-node-configuration-fixture'), './cmd/dt-node-configuration-fixture'], ROOT / 'DataTransfer', build_env)
    binary_rows = [{'path': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size} for path in binaries.iterdir()]
    save(evidence / 'binaries.json', binary_rows)
    source_rows={}
    for entry in commands:
        if entry['source_before']:
            for row in json.loads(Path(entry['source_before']).read_text()):source_rows[row['path']]=row
    save(evidence/'local-source-inputs.json',[source_rows[path] for path in sorted(source_rows)])
    contract_directory=evidence/'contracts'/'1.0'
    command('export-contracts',[str(binaries/'sf-contracts'),'--out',str(contract_directory),'--examples',str(evidence/'editable-definitions.json'),'--api-source','internal/api'],ROOT/'Platform',build_env)
    openapi=json.loads((contract_directory/'openapi.json').read_text())
    required_fields={
        'WorkloadIdentity':{'id','node_id','program','purpose','enabled','capabilities','parameter_ids','connector_ids','generation','version','instance_epoch','updated_ms'},
        'ConfigurationReference':{'kind','id','version','digest'},
        'ConfigurationVersion':{'version','digest'},
        'ConfigurationReport':{'identity_id','node_id','program','purpose','instance_id','instance_epoch','generation','sequence','kind','id','desired','prepared','applied','running','state','at_ms'},
        'SaveWorkloadIdentityInput':{'identity','expected_version'},
        'RotateWorkloadCredentialInput':{'credential','expected_version'},
    }
    schema_checks=[]
    for name,required in required_fields.items():
        standalone=json.loads((contract_directory/(name+'.schema.json')).read_text())['$defs'][name]
        generated=openapi['components']['schemas'][name]
        for origin,schema in [('independent',standalone),('openapi',generated)]:
            assert set(schema.get('required',[]))==required,(origin,name,'required',schema.get('required'))
            if name in ['ConfigurationReference','ConfigurationReport']:
                assert schema['properties']['kind']['enum']==['parameter','connector']
            if name=='ConfigurationReport':
                assert schema['properties']['state']['enum']==['prepared','running','failed','restart_required']
            if name=='ConfigurationVersion':assert schema['properties']['version']['minimum']==0
            if name=='ConfigurationReference':assert schema['properties']['version']['minimum']==1
            if name=='SaveWorkloadIdentityInput':assert schema['properties']['expected_version']['minimum']==0
            if name=='WorkloadIdentity':
                for field in ['generation','version','instance_id','instance_epoch','updated_ms']:assert schema['properties'][field]['readOnly']
            schema_checks.append({'origin':origin,'name':name,'required':sorted(required),'status':'passed'})
    save(evidence/'schema-checks.json',schema_checks)

    def port():
        with socket.socket() as stream:
            stream.bind(('127.0.0.1', 0))
            return stream.getsockname()[1]

    cloud_port, authority_port, config_port, a_port, b_port, sync_port, edge_a_port, edge_b_port, source_a_port, source_b_port, dt_a_port, dt_b_port, dt_a_http, dt_b_http = [port() for _ in range(14)]
    cloud_url, config_url = f'http://127.0.0.1:{cloud_port}', f'https://127.0.0.1:{config_port}'
    authority_url = f'https://127.0.0.1:{authority_port}'
    pki = private / 'pki'
    command('pki-init', [str(binaries / 'sf-pki'), '--mode', 'init', '--dir', str(pki)])
    for name, client in [('cloud-1', False), ('config-service', True), ('config-client-cloud-1', True), ('edge-a', True), ('edge-b', True)]:
        argv = [str(binaries / 'sf-pki'), '--dir', str(pki), '--node', name, '--hosts', 'localhost,127.0.0.1']
        if client:
            argv += ['--client']
        command('pki-' + name, argv)
    tls = ssl.create_default_context(cafile=str(pki / 'ca.pem'))
    tls.load_cert_chain(str(pki / 'config-client-cloud-1.pem'), str(pki / 'config-client-cloud-1.key'))
    password, token_a, token_b, token_cloud = [secrets.token_urlsafe(48) for _ in range(4)]
    for node in ['cloud', 'edge-a', 'edge-b']:
        (private / (node + '.master')).write_text(base64.b64encode(os.urandom(32)).decode())
        (private / (node + '.master')).chmod(0o600)
    public_keys = {}
    for node in ['cloud-1', 'edge-a', 'edge-b']:
        master = 'cloud' if node == 'cloud-1' else node
        public_keys[node] = json.loads(command('public-keys-' + node, [str(binaries / 'sf-pki'), '--mode', 'public-keys', '--dir', str(pki), '--node', node, '--master-key', str(private / (master + '.master'))]))
    for name, token in [('a.token', token_a), ('b.token', token_b),('cloud.token',token_cloud)]:
        (private / name).write_text(token + '\n')
        (private / name).chmod(0o600)
    capabilities = ['config.v2', 'config.read', 'config.report', 'credential.resolve', 'release', 'connector:mqtt_device', 'goos:darwin', 'goarch:arm64']
    identities = [
        {'id': 'workload-a', 'node_id': 'edge-a', 'program': 'edge', 'purpose': 'runtime', 'enabled': True, 'capabilities': capabilities, 'parameter_ids': ['control.start_ttl_ms', 'heartbeat.interval_ms', 'secret.a'], 'connector_ids': ['edge-a/mqtt-a']},
        {'id': 'workload-b', 'node_id': 'edge-b', 'program': 'gateway', 'purpose': 'gateway-runtime', 'enabled': True, 'capabilities': capabilities, 'parameter_ids': ['queue.capacity', 'secret.b'], 'connector_ids': ['edge-b/mqtt-b']},
    ]
    bootstrap=[{'identity': identity, 'credential': token} for identity, token in zip(identities, [token_a, token_b])]
    bootstrap.append({'identity':{'id':'workload-cloud','node_id':'cloud-1','program':'cloud','purpose':'runtime','enabled':True,'capabilities':['config.v2','config.read','config.report','credential.resolve'],'parameter_ids':[],'connector_ids':[]},'credential':token_cloud})
    save(private / 'workloads.json',bootstrap)
    cloud_env = dict(os.environ, SF_LISTEN=f'127.0.0.1:{cloud_port}', SF_DATABASE=str(private / 'cloud.db'), SF_NODE_ID='cloud-1', SF_MASTER_KEY_FILE=str(private / 'cloud.master'), SF_BOOTSTRAP_PASSWORD=password, SF_WORKLOAD_BOOTSTRAP_FILE=str(private / 'workloads.json'), SF_AUTHORITY_LISTEN=f'127.0.0.1:{authority_port}', SF_AUTHORITY_TLS_CA=str(pki / 'ca.pem'), SF_AUTHORITY_TLS_CERT=str(pki / 'cloud-1.pem'), SF_AUTHORITY_TLS_KEY=str(pki / 'cloud-1.key'), SF_CONFIG_URL=config_url, SF_CONFIG_TLS_CA=str(pki / 'ca.pem'), SF_CONFIG_TLS_CERT=str(pki / 'config-client-cloud-1.pem'), SF_CONFIG_TLS_KEY=str(pki / 'config-client-cloud-1.key'), SF_CONFIG_LEGACY_SUBSCRIPTION='false')
    # The cloud fixture is a control plane, so it has no regular config subscriber.
    cloud_env['SF_RELEASE_PAYLOAD'] = ''
    cloud_env['SF_CONFIG_TOKEN_FILE']=str(private/'cloud.token')
    cloud_env.update(SF_SYNC_LISTEN=f'127.0.0.1:{sync_port}', SF_TLS_CA=str(pki/'ca.pem'), SF_TLS_CERT=str(pki/'cloud-1.pem'), SF_TLS_KEY=str(pki/'cloud-1.key'))

    def start(name, argv, env=None):
        out, err = open(evidence / (name + '.stdout.log'), 'a'), open(evidence / (name + '.stderr.log'), 'a')
        process = subprocess.Popen(argv, cwd=ROOT, env=env, stdout=out, stderr=err, start_new_session=True)
        processes[name] = (process, out, err)
        return process

    def stop(name):
        if name not in processes:
            return
        process, out, err = processes.pop(name)
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        out.close()
        err.close()
        assertions.append({'case': 'process-exit-' + name, 'exit_code': process.returncode})

    def sanitize(value):
        if isinstance(value, dict):
            return {key: ('[private]' if key in ['password', 'credential', 'payload', 'token', 'api_key'] else sanitize(item)) for key, item in value.items()}
        if isinstance(value, list):
            return [sanitize(item) for item in value]
        return value

    def request(base, path, method='GET', value=None, token=None, instance=None, expected=200, context=None):
        body = None if value is None else json.dumps(value).encode()
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if instance:
            headers['X-SF-Instance-ID'] = instance
        req = urllib.request.Request(base + path, data=body, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, context=context or (tls if base.startswith('https:') else None), timeout=10) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        try:
            result = json.loads(raw)
        except (ValueError, UnicodeDecodeError):
            result = raw.decode(errors='replace')
        # Login session strings and resolved secret payloads are kept private.
        visible = '[private]' if path.endswith('/login') or path.endswith('/credentials/resolve') else sanitize(result)
        http_records.append({'method': method, 'base': base, 'path': path, 'status': status, 'response': visible})
        save(evidence / 'http.json', http_records)
        if status != expected:
            raise AssertionError(f'{method} {path}: expected {expected}, got {status}')
        return result

    def await_check(case, check, timeout=30):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                value = check()
                if value:
                    assertions.append({'case': case, 'status': 'passed', 'result': '[private]' if case.endswith('-permissions') else sanitize(value)})
                    save(evidence / 'assertions.json', assertions)
                    return value
            except (OSError, ValueError, AssertionError):
                pass
            time.sleep(0.2)
        raise AssertionError(case + ' did not converge')

    def node_state(name):
        return request(f'http://127.0.0.1:{a_port if name == "a" else b_port}', '/runtime')

    def local_policy(name, field, expected):
        state = node_state(name)
        policy_fields = {'start_ttl_ms': 'StartTTLMS', 'heartbeat_ms': 'HeartbeatMS', 'queue_capacity': 'QueueCapacity'}
        return state if state['policy'][policy_fields[field]] == expected else None

    login = lambda: request(cloud_url, '/api/sf/v1/login', 'POST', {'login': 'admin', 'password': password})['token']
    try:
        start('cloud', [str(binaries / 'sf-cloud')], cloud_env)
        await_check('cloud-ready', lambda: request(cloud_url, '/health'))
        user_token = login()
        params = [
            {'id': 'control.start_ttl_ms', 'program': 'edge', 'category': 'verification', 'schema': {'type': 'integer', 'minimum': 1000}, 'value': 10000, 'dynamic': True, 'target_node_ids': ['edge-a']},
            {'id': 'heartbeat.interval_ms', 'program': 'edge', 'category': 'verification', 'schema': {'type': 'integer', 'minimum': 1000}, 'value': 5000, 'dynamic': False, 'target_node_ids': ['edge-a']},
            {'id': 'secret.a', 'program': 'edge', 'schema': {'type': 'string'}, 'value': secrets.token_urlsafe(32), 'dynamic': True, 'secret': True, 'target_node_ids': ['edge-a']},
            {'id': 'secret.b', 'program': 'gateway', 'schema': {'type': 'string'}, 'value': secrets.token_urlsafe(32), 'dynamic': True, 'secret': True, 'target_node_ids': ['edge-b']},
        ]
        connectors = [{'request_id': 'seed-' + name, 'expected_version': 0, 'edge_id': node, 'group_id': 'factory', 'protocol': 'mqtt_device', 'parameters': {'kind': 'connector', 'connector_id': 'mqtt-' + name, 'connection': {'url': 'tcp://127.0.0.1:1883', 'username': 'fixture', 'password': secrets.token_urlsafe(32)}, 'polling': {'interval_millis': 1000}}} for name, node in [('a', 'edge-a'), ('b', 'edge-b')]]
        request(cloud_url, '/api/sf/v1/entities', 'POST', {'entity': {'id': 'factory', 'kind': 'asset', 'name': '配置验证工厂', 'status': 'active', 'version': 1}, 'expected_version': 0}, token=user_token)
        owner_envs={}
        for node, edge_http, source_port in [('edge-a', edge_a_port, source_a_port), ('edge-b', edge_b_port, source_b_port)]:
            registration = dict(public_keys[node], configuration_url=f'https://127.0.0.1:{source_port}')
            request(cloud_url, '/api/sf/v1/entities', 'POST', {'entity': {'id': node, 'kind': 'edge', 'parent_id': 'factory', 'name': node, 'status': 'active', 'version': 1, 'config': registration}, 'expected_version': 0}, token=user_token)
            edge_env = dict(os.environ, SF_LISTEN=f'127.0.0.1:{edge_http}', SF_DATABASE=str(private/(node+'.db')), SF_NODE_ID=node, SF_MASTER_KEY_FILE=str(private/(node+'.master')), SF_BOOTSTRAP_PASSWORD=password, SF_SYNC_URL=f'https://127.0.0.1:{sync_port}', SF_CLOUD_SIGNING_KEY=public_keys['cloud-1']['audit_public_key'], SF_TLS_CA=str(pki/'ca.pem'), SF_TLS_CERT=str(pki/(node+'.pem')), SF_TLS_KEY=str(pki/(node+'.key')), SF_CONFIG_AUTHORITY_URL=authority_url, SF_CONFIG_TLS_CA=str(pki/'ca.pem'), SF_CONFIG_TLS_CERT=str(pki/(node+'.pem')), SF_CONFIG_TLS_KEY=str(pki/(node+'.key')), SF_AUTHORITY_LISTEN=f'127.0.0.1:{source_port}', SF_AUTHORITY_TLS_CA=str(pki/'ca.pem'), SF_AUTHORITY_TLS_CERT=str(pki/(node+'.pem')), SF_AUTHORITY_TLS_KEY=str(pki/(node+'.key')), SF_CONFIG_URL='')
            owner_envs[node]=edge_env
            start('owner-'+node, [str(binaries/'sf-edge')], edge_env)
            base=f'http://127.0.0.1:{edge_http}'
            await_check('owning-'+node+'-ready', lambda base=base: request(base, '/health'))
            edge_token=await_check('owning-'+node+'-permissions', lambda base=base: request(base, '/api/sf/v1/login', 'POST', {'login':'admin','password':password})['token'])
            # Wait for the formally admitted cloud-edge synchronization to carry resources.
            await_check('owning-'+node+'-registered-resource', lambda base=base,et=edge_token,node=node: next((e for e in request(base,'/api/sf/v1/entities',token=et) if e['id']==node),None))
            connector=next(c for c in connectors if c['edge_id']==node)
            request(base,'/api/sf/v1/connector-configurations','POST',connector,token=edge_token)
        await_check('cloud-synchronized-public-fixed-connectors', lambda: request(cloud_url,'/api/sf/v1/connector-configurations',token=user_token) if len(request(cloud_url,'/api/sf/v1/connector-configurations',token=user_token))==2 else None)
        center = {'role': 'center', 'directory': str(private / 'center'), 'node_id': 'config', 'address': f'127.0.0.1:{config_port}', 'authority_url': authority_url, 'tls_ca': str(pki / 'ca.pem'), 'tls_certificate': str(pki / 'config-service.pem'), 'tls_key': str(pki / 'config-service.key'), 'password': password, 'parameters': params, 'connectors': []}
        save(private / 'center.json', center)
        fixture = binaries / 'sf-node-configuration-fixture'
        start('config', [str(fixture), '--settings', str(private / 'center.json')])
        await_check('config-mtls-ready', lambda: request(config_url, '/health'))
        for name, dt_port, dt_http in [('a',dt_a_port,dt_a_http),('b',dt_b_port,dt_b_http)]:
            start('datatransfer-'+name,[str(binaries/'dt-node-configuration-fixture'),'--directory',str(private/('dt-'+name)),'--grpc-listen',f'127.0.0.1:{dt_port}','--http-listen',f'127.0.0.1:{dt_http}'])
            await_check('datatransfer-'+name+'-actual-manager',lambda dt_http=dt_http:request(f'http://127.0.0.1:{dt_http}','/runtime'))
        for name, node, address in [('a', 'edge-a', a_port), ('b', 'edge-b', b_port)]:
            settings = {'role': 'subscriber', 'directory': str(private / ('node-' + name)), 'node_id': node, 'address': f'127.0.0.1:{address}', 'config_url': config_url, 'tls_ca': str(pki / 'ca.pem'), 'tls_certificate': str(pki / (node + '.pem')), 'tls_key': str(pki / (node + '.key')), 'token_file': str(private / (name + '.token')), 'reject_file': str(private / ('reject-' + name))}
            settings['datatransfer_address']=f'127.0.0.1:{dt_a_port if name=="a" else dt_b_port}'
            save(private / (name + '.json'), settings)
            start('node-' + name, [str(fixture), '--settings', str(private / (name + '.json'))])
        await_check('a-initial-actual-policy', lambda: local_policy('a', 'start_ttl_ms', 10000))
        await_check('b-initial-actual-policy', lambda: local_policy('b', 'queue_capacity', 200000))

        def reports():
            return request(config_url, '/api/sf/v1/configuration-reports', token=user_token)

        await_check('two-program-reports-and-connectors', lambda: reports() if len(reports()) == 7 else None)
        state_a, state_b = node_state('a'), node_state('b')
        assert {entry['reference']['id'] for entry in state_a['configurations']} == {'control.start_ttl_ms', 'heartbeat.interval_ms', 'secret.a', 'edge-a/mqtt-a'}
        assert {entry['reference']['id'] for entry in state_b['configurations']} == {'queue.capacity', 'secret.b', 'edge-b/mqtt-b'}
        actual_a=request(f'http://127.0.0.1:{dt_a_http}','/runtime')
        actual_b=request(f'http://127.0.0.1:{dt_b_http}','/runtime')
        assert actual_a['connectors'][0]['credential_consumed'] and actual_b['connectors'][0]['credential_consumed']
        assertions.append({'case': 'independent-node-program-scope-and-fixed-connector-credentials', 'status': 'passed', 'process_ids': [state_a['pid'], state_b['pid']]})
        cloud_reports=request(cloud_url,'/api/sf/v1/configuration-reports',token=user_token)
        assert len(cloud_reports)==7
        if args.frontend:
            save(private/'frontend-credentials.json',{'login':'admin','password':password})
            save(directory/'frontend.json',{'status':'ready','cloud_url':cloud_url,'configuration_url':config_url,'edge_urls':{'edge-a':f'http://127.0.0.1:{edge_a_port}','edge-b':f'http://127.0.0.1:{edge_b_port}'},'runtime_urls':{'edge-a':f'http://127.0.0.1:{a_port}','edge-b':f'http://127.0.0.1:{b_port}'},'credentials_file':str(private/'frontend-credentials.json'),'tls_client_ca':str(pki/'ca.pem'),'start_command':['python3',str(ROOT/'scripts/verify-node-configuration.py'),'--directory','<new private temporary directory>','--frontend'],'processes_file':str(private/'running-processes.json')})
            save(directory/'result.json',{'status':'passed','scope':'frontend initial authenticated two-node runtime fixture','frontend':str(directory/'frontend.json'),'source_inputs':str(evidence/'local-source-inputs.json'),'binaries':str(evidence/'binaries.json'),'http':str(evidence/'http.json'),'assertions':str(evidence/'assertions.json')})
            print(json.dumps({'status':'ready','frontend':str(directory/'frontend.json')}));return

        connector_ref=next(row['reference'] for row in state_a['configurations'] if row['reference']['kind']=='connector')
        connector_metadata=request(config_url,'/internal/config/v2/metadata','POST',{'references':[connector_ref]})[0]
        source_resolution={'reference':connector_ref,'credential_ref':connector_metadata['credential_ref'],'purpose':'runtime'}
        current_a=next(row for row in request(cloud_url,'/api/sf/v1/workload-identities',token=user_token) if row['id']=='workload-a')
        stop('owner-edge-a')
        request(config_url,'/internal/config/v2/credentials/resolve','POST',source_resolution,token=token_a,instance=current_a['instance_id'],expected=503)
        assert local_policy('a','start_ttl_ms',10000) is not None
        assert len(request(f'http://127.0.0.1:{dt_a_http}','/runtime')['connectors'])==1
        assertions.append({'case':'owning-connector-source-unavailable-503-preserves-cached-consumer','status':'passed','reference':connector_ref})
        start('owner-edge-a-restored',[str(binaries/'sf-edge')],owner_envs['edge-a'])
        await_check('owning-connector-source-restored',lambda:request(f'http://127.0.0.1:{edge_a_port}','/health'))
        request(config_url,'/internal/config/v2/credentials/resolve','POST',source_resolution,token=token_a,instance=current_a['instance_id'])

        def put_parameter(parameter_id, value):
            entries = request(config_url, '/api/sf/v1/config', token=user_token)
            entry = next(row for row in entries if row['id'] == parameter_id)
            entry['value'] = value
            return request(config_url, '/api/sf/v1/config', 'POST', {'parameter': entry, 'expected_version': entry['version']}, token=user_token)

        put_parameter('control.start_ttl_ms', 2400)
        await_check('dynamic-a-actual-success', lambda: local_policy('a', 'start_ttl_ms', 2400))
        (private / 'reject-b').write_text('consumer fails after applying policy\n')
        put_parameter('queue.capacity', 3100)
        await_check('dynamic-b-failure-preserves-running-version', lambda: next((r for r in reports() if r['id'] == 'queue.capacity' and r['state'] == 'failed' and r['running']['version'] < r['desired']['version']), None))
        assert node_state('b')['policy']['QueueCapacity'] == 200000
        (private / 'reject-b').unlink()
        await_check('dynamic-b-recovery-actual-success', lambda: local_policy('b', 'queue_capacity', 3100))
        put_parameter('heartbeat.interval_ms', 7000)
        await_check('static-prepared-running-old', lambda: next((r for r in reports() if r['id'] == 'heartbeat.interval_ms' and r['state'] == 'restart_required' and r['prepared']['version'] > r['running']['version']), None))
        assert node_state('a')['policy']['HeartbeatMS'] == 5000
        stop('node-a')
        start('node-a-restarted', [str(fixture), '--settings', str(private / 'a.json')])
        await_check('static-restart-actual-consumer-7000', lambda: local_policy('a', 'heartbeat_ms', 7000))
        await_check('static-restart-new-epoch-running-report', lambda: next((r for r in reports() if r['id'] == 'heartbeat.interval_ms' and r['state'] == 'running' and r['running'] == r['desired']), None))

        # Credential resolution and reports use each live process's current instance.
        live = {row['id']: row for row in request(cloud_url, '/api/sf/v1/workload-identities', token=user_token)}
        metadata = request(config_url, '/internal/config/v2/metadata', 'POST', {'references': [{'kind': 'parameter', 'id': 'secret.a', 'version': 1, 'digest': ''}]})[0]
        resolution = {'reference': metadata['reference'], 'credential_ref': metadata['credential_ref'], 'purpose': 'runtime'}
        request(config_url, '/internal/config/v2/credentials/resolve', 'POST', resolution, token=token_b, instance=live['workload-b']['instance_id'], expected=403)
        resolution['purpose'] = 'wrong-purpose'
        request(config_url, '/internal/config/v2/credentials/resolve', 'POST', resolution, token=token_a, instance=live['workload-a']['instance_id'], expected=403)
        resolution['purpose'] = 'runtime'
        request(config_url, '/internal/config/v2/credentials/resolve', 'POST', resolution, token=token_a, instance=live['workload-a']['instance_id'])
        report = next(row for row in reports() if row['id'] == 'control.start_ttl_ms')
        for field in ['kind','id','version','digest']:
            incomplete=dict(resolution['reference']);incomplete.pop(field)
            request(config_url,'/internal/config/v2/versions','POST',{'references':[incomplete]},token=token_a,instance=live['workload-a']['instance_id'],expected=400)
        for field in ['desired','kind','instance_epoch']:
            incomplete=dict(report);incomplete.pop(field)
            request(config_url,'/internal/config/v2/reports','POST',incomplete,token=token_a,instance=live['workload-a']['instance_id'],expected=400)
        incomplete=json.loads(json.dumps(report));incomplete['desired'].pop('digest')
        request(config_url,'/internal/config/v2/reports','POST',incomplete,token=token_a,instance=live['workload-a']['instance_id'],expected=400)
        assertions.append({'case':'required-reference-array-and-report-nested-http-fields','status':'passed','requests':8})
        request(config_url, '/internal/config/v2/reports', 'POST', report, token=token_a, instance=live['workload-a']['instance_id'])
        forged = dict(report, node_id='edge-b', sequence=report['sequence'] + 1)
        request(config_url, '/internal/config/v2/reports', 'POST', forged, token=token_a, instance=live['workload-a']['instance_id'], expected=403)
        stale = dict(report, sequence=max(0, report['sequence'] - 1))
        request(config_url, '/internal/config/v2/reports', 'POST', stale, token=token_a, instance=live['workload-a']['instance_id'], expected=409 if stale['sequence'] else 400)
        mismatch = json.loads(json.dumps(report))
        mismatch['desired']['digest'] = '0' * 64
        request(config_url, '/internal/config/v2/reports', 'POST', mismatch, token=token_a, instance=live['workload-a']['instance_id'], expected=409)

        # Scope changes affect the already running stream and remove managed values.
        identity_a = live['workload-a']
        identity_a['parameter_ids'] = ['control.start_ttl_ms', 'heartbeat.interval_ms']
        request(cloud_url, '/api/sf/v1/workload-identities', 'POST', {'identity': identity_a, 'expected_version': identity_a['version']}, token=user_token)
        await_check('established-stream-scope-change', lambda: node_state('a') if 'secret.a' not in {entry['reference']['id'] for entry in node_state('a')['configurations']} else None)
        request(config_url, '/internal/config/v2/credentials/resolve', 'POST', resolution, token=token_a, instance=identity_a['instance_id'], expected=403)
        live = {row['id']: row for row in request(cloud_url, '/api/sf/v1/workload-identities', token=user_token)}
        next_token = secrets.token_urlsafe(48)
        updated = request(cloud_url, '/api/sf/v1/workload-identities/workload-a/rotate', 'POST', {'credential': next_token, 'expected_version': live['workload-a']['version']}, token=user_token)
        await_check('established-stream-rotation-stops-old-managed-values', lambda: node_state('a') if not node_state('a')['configurations'] else None)
        await_check('rotation-removes-actual-connector-consumer',lambda:request(f'http://127.0.0.1:{dt_a_http}','/runtime') if not request(f'http://127.0.0.1:{dt_a_http}','/runtime')['connectors'] else None)
        request(config_url, '/internal/config/v2/versions', 'POST', {'references': []}, token=token_a, instance=identity_a['instance_id'], expected=401)
        token_a = next_token
        (private / 'a.token').write_text(token_a + '\n')
        stop('node-a-restarted')
        start('node-a-rotated', [str(fixture), '--settings', str(private / 'a.json')])
        await_check('rotated-program-actual-policy-restored', lambda: local_policy('a', 'start_ttl_ms', 2400))

        # Offline authority preserves local values, including startup from validated cache.
        stop('cloud')
        assert node_state('a')['policy']['StartTTLMS'] == 2400
        stop('node-a-rotated')
        start('node-a-offline', [str(fixture), '--settings', str(private / 'a.json')])
        authority_offline_state=await_check('authority-offline-cached-process-starts', lambda: local_policy('a', 'start_ttl_ms', 2400))
        authority_recovery_ms=int(time.time()*1000)
        start('cloud-restored', [str(binaries / 'sf-cloud')], cloud_env)
        await_check('authority-restored', lambda: request(cloud_url, '/health'))
        user_token = login()
        current_a=await_check('authority-restored-activated-new-process',lambda:next((w for w in request(cloud_url,'/api/sf/v1/workload-identities',token=user_token) if w['id']=='workload-a' and w.get('instance_id')==authority_offline_state['instance_id']),None))
        await_check('authority-restored-current-epoch', lambda: next((r for r in reports() if r['identity_id']=='workload-a' and r['id']=='control.start_ttl_ms' and r['state']=='running' and r['instance_id']==authority_offline_state['instance_id'] and r['instance_epoch']==current_a['instance_epoch'] and r['at_ms']>=authority_recovery_ms and r['desired']==r['running'] and r.get('effective_value')==2400), None))
        stop('config')
        stop('node-a-offline')
        start('node-a-config-offline', [str(fixture), '--settings', str(private / 'a.json')])
        config_offline_state=await_check('config-offline-cached-process-starts', lambda: local_policy('a', 'heartbeat_ms', 7000))
        config_recovery_ms=int(time.time()*1000)
        start('config-restored', [str(fixture), '--settings', str(private / 'center.json')])
        await_check('config-restored-ready', lambda: request(config_url, '/health'))
        current_a=await_check('config-restored-activated-new-process',lambda:next((w for w in request(cloud_url,'/api/sf/v1/workload-identities',token=user_token) if w['id']=='workload-a' and w.get('instance_id')==config_offline_state['instance_id']),None))
        await_check('config-restored-current-report', lambda: next((r for r in reports() if r['identity_id']=='workload-a' and r['id']=='heartbeat.interval_ms' and r['state']=='running' and r['instance_id']==config_offline_state['instance_id'] and r['instance_epoch']==current_a['instance_epoch'] and r['at_ms']>=config_recovery_ms and r['desired']==r['running'] and r.get('effective_value')==7000), None))
        live = {row['id']: row for row in request(cloud_url, '/api/sf/v1/workload-identities', token=user_token)}
        identity_b = live['workload-b']
        identity_b['enabled'] = False
        disabled=request(cloud_url, '/api/sf/v1/workload-identities', 'POST', {'identity': identity_b, 'expected_version': identity_b['version']}, token=user_token)
        await_check('established-stream-revocation', lambda: node_state('b') if not node_state('b')['configurations'] else None)
        await_check('revocation-removes-actual-connector-consumer',lambda:request(f'http://127.0.0.1:{dt_b_http}','/runtime') if not request(f'http://127.0.0.1:{dt_b_http}','/runtime')['connectors'] else None)
        assert local_policy('b','queue_capacity',200000) is not None
        previous_b=next(row for row in reports() if row['identity_id']=='workload-b' and row['id']=='queue.capacity')
        assert previous_b['at_ms']<=disabled['updated_ms']
        request(config_url,'/internal/config/v2/reports','POST',previous_b,token=token_b,instance=identity_b['instance_id'],expected=401)
        assertions.append({'case':'revoked-live-process-default-policy-and-last-authorized-report','status':'passed','process_id':node_state('b')['pid'],'report_at_ms':previous_b['at_ms'],'revoked_at_ms':disabled['updated_ms'],'running_version':previous_b['running']})
        request(config_url, '/internal/config/v2/versions', 'POST', {'references': []}, token=token_b, instance=identity_b['instance_id'], expected=401)
        # Schema-required checks use actual typed management HTTP.
        for body in [{}, {'identity': live['workload-a']}, {'expected_version': 0}]:
            request(cloud_url, '/api/sf/v1/workload-identities', 'POST', body, token=user_token, expected=422)
        for body in [{}, {'expected_version': 1}, {'credential': secrets.token_urlsafe(48)}]:
            request(cloud_url, '/api/sf/v1/workload-identities/workload-a/rotate', 'POST', body, token=user_token, expected=422)
        save(evidence / 'assertions.json', assertions)
        save(directory / 'result.json', {'status': 'passed', 'source_inputs': str(evidence / 'local-source-inputs.json'), 'binaries': str(evidence / 'binaries.json'), 'commands': str(evidence / 'commands.json'), 'http': str(evidence / 'http.json'), 'assertions': str(evidence / 'assertions.json'), 'independent_databases': [str(private / 'cloud.db'), str(private / 'center/configuration.db')], 'scope': 'independent cloud authority, mTLS config center, two real subscriber processes and live policy consumers'})
        print(json.dumps({'status': 'passed', 'directory': str(directory), 'http_calls': len(http_records), 'assertions': len(assertions)}))
    except Exception as error:
        save(directory / 'result.json', {'status': 'failed', 'error': str(error), 'commands': str(evidence / 'commands.json'), 'http': str(evidence / 'http.json'), 'assertions': str(evidence / 'assertions.json')})
        raise
    finally:
        if not args.keep_running and not args.frontend:
            for name in list(processes):
                stop(name)
            save(evidence / 'assertions.json', assertions)
        else:
            save(private / 'running-processes.json', [{'name': name, 'pid': process.pid} for name, (process, _, _) in processes.items()])
        result_path=directory/'result.json'
        if result_path.exists():
            result=json.loads(result_path.read_text())
            result['assertion_counts']={'passed':sum(row.get('status')=='passed' for row in assertions),'process_exit':sum(row.get('case','').startswith('process-exit-') for row in assertions),'total':len(assertions)}
            result['schema_checks']=str(evidence/'schema-checks.json')
            result['execution_driver']={'path':str(driver_archive),'sha256':hashlib.sha256(driver_archive.read_bytes()).hexdigest()}
            save(result_path,result)


if __name__ == '__main__':
    run()
