#!/usr/bin/env python3
"""Exercise a Linux bundle's prepared identities and release service conversion."""
import argparse
import copy
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import ssl
import subprocess
import threading
import time
import traceback
from urllib.error import HTTPError
from urllib.parse import urlparse, unquote
from urllib.request import HTTPSHandler, ProxyHandler, Request, build_opener

ROOT = Path(__file__).resolve().parents[1]


def utc():
    return datetime.now(timezone.utc).isoformat()


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':')).replace('<', '\\u003c').replace('>', '\\u003e').replace('&', '\\u0026').encode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', type=Path, required=True)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--postgres-description', type=Path, default=ROOT / '.local/evolution/pg18-test.json')
    args = parser.parse_args()
    bundle, work = args.bundle.resolve(), args.directory.resolve()
    os.umask(0o077)
    work.mkdir(parents=True, exist_ok=False)
    private, evidence = work / 'private', work / 'evidence'
    private.mkdir()
    evidence.mkdir()
    prepared = private / 'prepared'
    postgres = json.loads(args.postgres_description.read_text())
    pg = urlparse(postgres['dsn'])
    pg_user, pg_password = unquote(pg.username), unquote(pg.password)
    secrets_to_hide = [pg_password]
    report = {'schema_version': 1, 'started_at': utc(), 'status': 'running', 'commands': [], 'http': [], 'checks': [], 'samples': [],
              'scope': 'actual Linux bundle; formal preparation, TLS and distinct PostgreSQL identities, real DataTransfer, artifact registration, prepare-only, changed assignment, conversion and cached restart',
              'native_platform_scope': 'CE/Edge/Kafka installation, callbacks and PID1 retained-data restart use the separately recorded native verification; these services are not started by this finite fixture',
              'bundle': str(bundle), 'bundle_manifest_sha256': sha(bundle / 'release-manifest.json') if (bundle / 'release-manifest.json').is_file() else None,
              'invocation': {'argv': [str(Path(__file__).resolve()), '--bundle', str(bundle), '--directory', str(work), '--postgres-description', str(args.postgres_description.resolve())], 'cwd': str(ROOT)}}
    started = False
    stop_samples = threading.Event()

    def private_json(path, value):
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
        path.chmod(0o600)

    def save():
        private_json(work / 'verification.json', report)

    def redact(value):
        text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
        for secret in secrets_to_hide:
            if secret:
                text = text.replace(secret, '<private>')
        return text

    def visible(value):
        return json.loads(redact(json.dumps(value, ensure_ascii=False)))

    def command(name, argv, *, input=None, timeout=180, required=True):
        start = time.monotonic()
        started_at = utc()
        timed_out = False
        try:
            result = subprocess.run(argv, input=input, capture_output=True, text=True, cwd=ROOT, timeout=timeout)
            stdout, stderr, exit_code = result.stdout, result.stderr, result.returncode
        except subprocess.TimeoutExpired as error:
            timed_out = True
            stdout, stderr, exit_code = error.stdout or '', error.stderr or '', None
            if isinstance(stdout, bytes):
                stdout = stdout.decode(errors='replace')
            if isinstance(stderr, bytes):
                stderr = stderr.decode(errors='replace')
        if argv[:2] == ['docker', 'inspect'] and exit_code == 0:
            values = json.loads(stdout)
            for value in values:
                value.get('Config', {})['Env'] = [entry.split('=', 1)[0] + '=<private>' for entry in value.get('Config', {}).get('Env', [])]
            stdout = json.dumps(values, indent=2)
        path = evidence / (name + '.log')
        path.write_text(redact(stdout + '\n--- stderr ---\n' + stderr))
        report['commands'].append({'name': name, 'argv': visible(argv), 'cwd': str(ROOT), 'exit_code': exit_code,
                                   'started_at': started_at, 'completed_at': utc(),
                                   'duration_seconds': time.monotonic() - start, 'timeout_seconds': timeout, 'timed_out': timed_out,
                                   'log': str(path), 'sha256': sha(path)})
        save()
        if required and (timed_out or exit_code):
            raise RuntimeError(name + ' failed; see ' + str(path))
        return stdout

    def check(name, condition, detail=None):
        report['checks'].append({'name': name, 'passed': bool(condition), 'detail': visible(detail) if detail is not None else None})
        save()
        if not condition:
            raise AssertionError(name)

    def sql(name, query, database='postgres'):
        return command(name, ['docker', 'exec', '-i', postgres['container'], 'psql', '-U', pg_user, '-d', database, '-v', 'ON_ERROR_STOP=1', '-At'], input=query)

    def wait(name, call, accept=lambda value: bool(value), timeout=180):
        until, count, errors = time.monotonic() + timeout, 0, []
        while time.monotonic() < until:
            count += 1
            try:
                value = call()
                if accept(value):
                    report.setdefault('readiness', []).append({'name': name, 'at': utc(), 'attempts': count, 'last_errors': errors[-5:]})
                    save()
                    return value
            except (OSError, KeyError, AssertionError, ValueError) as error:
                errors.append(type(error).__name__ + ': ' + redact(str(error))[:500])
            time.sleep(0.5)
        raise TimeoutError(name + '; last errors=' + repr(errors[-5:]))

    def request(base, path, method='GET', value=None, token='', instance='', context=None, expected=200):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if instance:
            headers['X-SF-Instance-ID'] = instance
        opener = build_opener(ProxyHandler({}), *([HTTPSHandler(context=context)] if context else []))
        start = time.monotonic()
        started_at = utc()
        try:
            with opener.open(Request(base + path, data=None if value is None else json.dumps(value).encode(), method=method, headers=headers), timeout=20) as response:
                status, body = response.status, response.read()
        except HTTPError as error:
            status, body = error.code, error.read()
        except OSError as error:
            report['http'].append({'started_at': started_at, 'at': utc(), 'base': base, 'path': path, 'method': method, 'duration_seconds': time.monotonic() - start, 'error_type': type(error).__name__, 'error': redact(str(error))})
            save()
            raise
        try:
            result = json.loads(body)
        except (ValueError, UnicodeDecodeError):
            result = body.decode(errors='replace')
        response = '<private>' if path.endswith('/login') or path.endswith('/credentials/resolve') else visible(result)
        report['http'].append({'started_at': started_at, 'at': utc(), 'base': base, 'path': path, 'method': method, 'status': status,
                               'duration_seconds': time.monotonic() - start, 'response': response,
                               'request_sha256': hashlib.sha256(json.dumps(value).encode()).hexdigest() if value is not None else None,
                               'request': '<private>' if path.endswith('/login') else visible(value)})
        save()
        if status != expected:
            raise AssertionError(path + ': HTTP ' + str(status) + ', expected ' + str(expected) + ': ' + redact(result)[:500])
        return result

    def container_state(service):
        identifier = command('id-' + service + '-' + str(len(report['commands'])), [*compose, '--profile', 'release', '--profile', 'direct-runtime', 'ps', '-a', '-q', service]).strip()
        return json.loads(command('inspect-' + service + '-' + str(len(report['commands'])), ['docker', 'inspect', identifier]))[0]

    try:
        driver_copy = evidence / 'verify-delivery-runtime.py'
        shutil.copy2(Path(__file__).resolve(), driver_copy)
        report['driver'] = {'path': str(driver_copy), 'sha256': sha(driver_copy)}
        images = json.loads((bundle / 'deploy/images.lock.json').read_text())['images']
        runtime_image = images['runtime']['pinned']
        mount = ['--mount', 'type=bind,src=' + str(bundle) + ',dst=/bundle,readonly']
        linux_run = ['docker', 'run', '--rm', '--pull', 'never', '--platform', 'linux/amd64', '--memory', '128m', '--cpus', '1', '--network', 'none', *mount]
        identity = json.loads(command('actual-linux-edge-build-info', [*linux_run, '--entrypoint', '/bundle/bin/sf-edge', runtime_image, '-build-info']))
        build = identity['build']
        check('actual-linux-executable-identity', identity['sha256'] == sha(bundle / 'bin/sf-edge') and build['goos'] == 'linux' and build['goarch'] == 'amd64', identity)
        if (bundle / 'release-manifest.json').is_file():
            manifest = json.loads((bundle / 'release-manifest.json').read_text())
            check('version-and-source-match-bundle', build['version'] == manifest['application_version'] and build['source_sha256'] == manifest['source_sha256'], build)
        build_file = evidence / 'edge-build-info.json'
        private_json(build_file, identity)
        wrapper = private / 'linux-pki'
        pki_command = [*linux_run, '--user', str(os.getuid()) + ':' + str(os.getgid()), '--mount', 'type=bind,src=' + str(work) + ',dst=' + str(work), '--entrypoint', '/bundle/bin/sf-pki', runtime_image]
        wrapper.write_text('#!/usr/bin/env python3\nimport subprocess,sys\nraise SystemExit(subprocess.call(' + repr(pki_command) + '+sys.argv[1:]))\n')
        wrapper.chmod(0o700)
        command('formal-linux-bundle-preparation', ['python3', str(bundle / 'scripts/prepare-runtime.py'), '--bundle', str(bundle), '--directory', str(prepared), '--profile', 'capacity', '--hosting', 'hosted', '--simulation', 'scenes', '--pki-binary', str(wrapper)])
        formal = json.loads((prepared / 'compose.json').read_text())
        shutil.copy2(prepared / 'compose.json', private / 'compose.formal.json')
        credentials = json.loads((prepared / 'deployment-credentials.json').read_text())
        secrets_to_hide.extend(credentials.values())
        secrets_to_hide.extend(path.read_text().strip() for path in prepared.glob('*.token'))
        formal_agent = formal['services']['edge-a-release-agent']
        settings = json.loads((prepared / 'release-runtime-edge-a.json').read_text())
        supplied = {'SF_NODE_ID', 'SF_LISTEN', 'SF_DATABASE', 'SF_MASTER_KEY_FILE', 'SF_RELEASE_PAYLOAD'}
        original_environment = formal['services']['edge-a']['environment']
        check('all-formal-runtime-service-settings-retained', settings['environment'] == {key: value for key, value in original_environment.items() if key not in supplied and key.startswith(('SF_', 'OTEL_'))})
        check('original-database-master-and-static-settings', settings['database'] == original_environment['SF_DATABASE'] and settings['master_key_file'] == original_environment['SF_MASTER_KEY_FILE'] and settings['static_directory'] == original_environment['SF_STATIC_DIR'])
        check('distinct-generated-runtime-and-release-identities', (prepared / 'release-edge-a.token').read_bytes() != (prepared / 'workload-edge-a.token').read_bytes() and original_environment['SF_CONFIG_LEGACY_SUBSCRIPTION'] == 'false')
        report['formal_preparation'] = {'compose': str(private / 'compose.formal.json'), 'sha256': sha(private / 'compose.formal.json'), 'agent_command': formal_agent['command'],
                                        'pki_utility': 'exact Linux bundle executable through the pinned runtime image', 'profiles': ['capacity', 'hosted', 'scenes']}
        generated = formal['services']
        for name in ['config', 'cloud', 'edge-a']:
            values = generated[name]['environment']
            check('generated-' + name + '-legacy-disabled', values['SF_CONFIG_LEGACY_SUBSCRIPTION'] == 'false')
            check('generated-' + name + '-tls-material-configured', all(values.get('SF_CONFIG_TLS_' + suffix) for suffix in ['CA', 'CERT', 'KEY']))
            if name != 'config':
                check('generated-' + name + '-https-center-url', values['SF_CONFIG_URL'] == 'https://config:8092')
            if name != 'cloud':
                check('generated-' + name + '-separate-cloud-authority', values['SF_CONFIG_AUTHORITY_URL'] == 'https://cloud:18445')
        for certificate, host in [('config-service', 'config'), ('cloud-1', 'cloud'), ('edge-a', 'edge-a')]:
            command('generated-' + certificate + '-server-certificate-valid', ['openssl', 'verify', '-CAfile', str(prepared / 'pki/ca.pem'), '-purpose', 'sslserver', '-verify_hostname', host, str(prepared / ('pki/' + certificate + '.pem'))])
            check('generated-' + certificate + '-server-certificate-valid', True)
        for certificate in ['config-service', 'config-client-cloud-1', 'edge-a']:
            command('generated-' + certificate + '-client-certificate-valid', ['openssl', 'verify', '-CAfile', str(prepared / 'pki/ca.pem'), '-purpose', 'sslclient', str(prepared / ('pki/' + certificate + '.pem'))])
            check('generated-' + certificate + '-client-certificate-valid', True)
        check('generated-independent-config-database', generated['config']['environment']['SF_DATABASE'] != generated['cloud']['environment']['SF_DATABASE'])
        workloads = json.loads((prepared / 'workloads.json').read_text())
        check('generated-distinct-runtime-and-release-identities', len({item['identity']['id'] for item in workloads}) == 3 and len({item['identity']['purpose'] for item in workloads}) == 2)
        check('generated-distinct-private-workload-credentials', len({item['credential'] for item in workloads}) == 3)
        check('generated-release-runtime-purpose', next(item['identity']['purpose'] for item in workloads if item['identity']['id'] == 'release-edge-a') == 'release-runtime')
        for name in ['workloads.json', 'workload-cloud-1.token', 'workload-edge-a.token', 'release-edge-a.token']:
            check('generated-' + name + '-owner-only', (prepared / name).stat().st_mode & 0o777 == 0o600)
        suffix = hashlib.sha256(str(work).encode()).hexdigest()[:10]
        database_names, roles, dsns = {}, {}, {}
        statements = []
        for service in ['cloud', 'config', 'edge-a']:
            name = 'sf_delivery_' + service.replace('-', '_') + '_' + suffix
            password = secrets.token_hex(24)
            secrets_to_hide.append(password)
            roles[service] = name
            database_names[service] = name
            dsns[service] = f'postgres://{name}:{password}@host.docker.internal:{pg.port}/{name}?sslmode=disable'
            statements.extend([f"CREATE ROLE {name} LOGIN PASSWORD '{password}';", f'CREATE DATABASE {name} OWNER {name};'])
        sql('create-distinct-private-databases-and-roles', '\n'.join(statements))
        initial_sessions = sql('initial-own-database-connections', 'SELECT datname,count(*) FROM pg_stat_activity WHERE datname IN (' + ','.join("'" + name + "'" for name in database_names.values()) + ') GROUP BY datname;').strip()
        check('initial-own-database-connections-zero', not initial_sessions, initial_sessions)
        services = {name: copy.deepcopy(formal['services'][name]) for name in ['config', 'cloud', 'edge-a-network', 'edge-a', 'edge-a-release-agent', 'edge-a-simulator', 'edge-a-gateway', 'web']}
        for name, service in services.items():
            service['restart'] = 'no'
            service['labels'] = {'sf.evolution.fixture': 'delivery-linux-' + suffix}
            if not service.get('network_mode', '').startswith('service:'):
                service['extra_hosts'] = ['host.docker.internal:host-gateway']
            service['depends_on'] = {key: value for key, value in service.get('depends_on', {}).items() if key in services}
            service['mem_limit'] = {'cloud': '512m', 'config': '256m', 'edge-a': '256m', 'edge-a-release-agent': '256m', 'edge-a-gateway': '256m', 'edge-a-network': '32m', 'web': '128m'}.get(name, '128m')
            service['memswap_limit'] = service['mem_limit']
            if name in dsns:
                service['environment']['SF_DATABASE'] = dsns[name]
        services['cloud']['ports'] = ['127.0.0.1:20190:8090', '127.0.0.1:20445:18445']
        services['config']['ports'] = ['127.0.0.1:20292:8092']
        services['edge-a-network']['ports'] = ['127.0.0.1:20191:8091', '127.0.0.1:20182:18082', '127.0.0.1:20183:18083', '127.0.0.1:20446:18445']
        services['web']['ports'] = ['127.0.0.1:20443:8443', '127.0.0.1:20444:8444']
        settings['database'] = dsns['edge-a']
        private_json(prepared / 'release-runtime-edge-a.json', settings)
        config = {'name': 'sf-delivery-linux-' + suffix, 'services': services}
        private_json(prepared / 'compose.json', config)
        profile = json.loads((prepared / 'profile.json').read_text())
        profile.update(project=config['name'], business_cloud_url='http://127.0.0.1:20190', urls=['https://localhost:20443', 'https://localhost:20444'])
        private_json(prepared / 'profile.json', profile)
        report['fixture'] = {'compose': str(prepared / 'compose.json'), 'sha256_before_conversion': sha(prepared / 'compose.json'), 'databases': database_names, 'roles': roles,
                             'configuration_environment_differences': {name: ['SF_DATABASE'] for name in dsns},
                             'other_adjustments': 'isolated host ports, external validation PostgreSQL with a distinct role and database per application, finite memory limits, native services omitted',
                             'steady_container_limit_bytes_including_postgres': sum(int(service['mem_limit'][:-1]) * 1024**2 for name, service in services.items() if name != 'edge-a') + 1024**3,
                             'prepare_only_peak_container_limit_bytes_including_postgres': sum(int(service['mem_limit'][:-1]) * 1024**2 for service in services.values()) + 1024**3}
        compose = ['docker', 'compose', '-f', str(prepared / 'compose.json')]
        command('validate-finite-composition', [*compose, '--profile', 'release', 'config', '--quiet'])
        started = True
        service_names = list(services)
        def sample():
            while not stop_samples.is_set():
                values = {}
                for name in service_names:
                    result = subprocess.run([*compose, '--profile', 'release', 'ps', '-q', name], capture_output=True, text=True)
                    identifier = result.stdout.strip()
                    if not identifier:
                        continue
                    measured = subprocess.run(['docker', 'exec', identifier, 'sh', '-c', 'cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak'], capture_output=True, text=True)
                    if measured.returncode == 0:
                        values[name] = [int(value) for value in measured.stdout.split()]
                report['samples'].append({'at': utc(), 'memory_current_and_peak': values})
                stop_samples.wait(3)
        sampler = threading.Thread(target=sample, daemon=True)
        sampler.start()
        command('start-original-formal-business-services', [*compose, 'up', '-d', '--pull', 'never', 'config', 'cloud', 'edge-a', 'web'])
        cloud, edge, configuration = 'http://127.0.0.1:20190', 'http://127.0.0.1:20191', 'https://127.0.0.1:20292'
        tls = ssl.create_default_context(cafile=str(prepared / 'pki/ca.pem'))
        client_tls = ssl.create_default_context(cafile=str(prepared / 'pki/ca.pem'))
        client_tls.load_cert_chain(str(prepared / 'pki/config-client-cloud-1.pem'), str(prepared / 'pki/config-client-cloud-1.key'))
        node_tls = ssl.create_default_context(cafile=str(prepared / 'pki/ca.pem'))
        node_tls.load_cert_chain(str(prepared / 'pki/edge-a.pem'), str(prepared / 'pki/edge-a.key'))
        source_tls = ssl.create_default_context(cafile=str(prepared / 'pki/ca.pem'))
        source_tls.load_cert_chain(str(prepared / 'pki/config-service.pem'), str(prepared / 'pki/config-service.key'))
        ct = wait('cloud login', lambda: request(cloud, '/api/sf/v1/login', 'POST', {'login': 'admin', 'password': credentials['password']}))['token']
        secrets_to_hide.append(ct)
        command('formal-node-certificate-and-public-key-enrollment', ['python3', str(bundle / 'scripts/enroll-local-edge.py'), '--state-directory', str(prepared), '--cloud-url', cloud, '--node', 'edge-a'])
        et = wait('original Edge permission synchronization and login', lambda: request(edge, '/api/sf/v1/login', 'POST', {'login': 'admin', 'password': credentials['password']}))['token']
        secrets_to_hide.append(et)
        command('start-real-simulation-and-datatransfer', [*compose, 'up', '-d', '--pull', 'never', 'edge-a-simulator', 'edge-a-gateway'])
        def gateway_ready():
            identifier = container_state('edge-a-network')['Id']
            output = command('gateway-loopback-readiness-' + str(len(report['commands'])), ['docker', 'exec', identifier, 'wget', '-q', '-O-', 'http://127.0.0.1:18082/readyz'], required=False)
            return json.loads(output) if output.strip() else None
        wait('actual gateway readiness through its private loopback listener', gateway_ready)
        check('both-packaged-frontend-portals-serve', all('id="root"' in request(base, '/', context=tls) for base in profile['urls']))
        marker = {'scope': 'finite Linux application fixture: actual generated identities and prepared databases, cloud/Edge --seed startup and formal enrollment; separate native installation is referenced by the final report', 'initialized_at': utc()}
        private_json(prepared / 'installed.json', marker)
        connector_id = 'edge-a/delivery-mqtt'
        source_entity = next(item for item in request(cloud, '/api/sf/v1/entities', token=ct) if item['id'] == 'edge-a')
        source_certificate = ssl.PEM_cert_to_DER_cert((prepared / 'pki/edge-a.pem').read_text())
        check('registered-connector-source-url-and-certificate-match', source_entity['config']['configuration_url'] == 'https://edge-a:18445' and source_entity['config']['certificate_sha256'] == hashlib.sha256(source_certificate).hexdigest(), source_entity['config'])
        wait('original node connector-source TLS endpoint online', lambda: request('https://127.0.0.1:20446', '/health', context=source_tls))
        identities = request(cloud, '/api/sf/v1/workload-identities', token=ct)
        for identity_item in identities:
            if identity_item['id'] in ['platform-edge-a', 'release-edge-a']:
                updated = {key: identity_item[key] for key in ['id', 'node_id', 'program', 'purpose', 'enabled', 'capabilities', 'parameter_ids', 'connector_ids']}
                updated['connector_ids'] = [connector_id]
                request(cloud, '/api/sf/v1/workload-identities', 'POST', {'identity': updated, 'expected_version': identity_item['version']}, ct)
        connector_refs = {}
        for version in [1, 2]:
            secret = 'delivery-secret-' + secrets.token_hex(20)
            secrets_to_hide.append(secret)
            body = {'request_id': 'delivery-connector-' + str(version), 'expected_version': version - 1, 'edge_id': 'edge-a', 'group_id': 'factory', 'protocol': 'mqtt_device',
                    'parameters': {'kind': 'connector', 'connector_id': 'delivery-mqtt', 'connection': {'url': 'tcp://127.0.0.1:18830', 'username': 'delivery-fixture', 'password': secret}, 'polling': {'interval_millis': version * 1000}}}
            request(edge, '/api/sf/v1/connector-configurations', token=et)
            wait('persist connector version ' + str(version) + ' with current startup metadata', lambda: request(edge, '/api/sf/v1/connector-configurations', 'POST', body, et), timeout=20)
        wait('connector public metadata synchronization', lambda: request(cloud, '/api/sf/v1/connector-configurations', token=ct), lambda rows: any(row['configuration']['id'] == connector_id and row['configuration']['version'] == 2 for row in rows))
        for version in [1, 2]:
            connector_refs[version] = request(configuration, '/internal/config/v2/metadata', 'POST', {'references': [{'kind': 'connector', 'id': connector_id, 'version': version, 'digest': ''}]}, context=client_tls)[0]['reference']
        first_parameter = next(item for item in request(cloud, '/api/sf/v1/config', token=ct) if item['id'] == 'heartbeat.interval_ms')
        changed_parameter = copy.deepcopy(first_parameter)
        changed_parameter.update(value=7000, target_node_ids=['edge-a'])
        second_parameter = request(cloud, '/api/sf/v1/config', 'POST', {'parameter': changed_parameter, 'expected_version': first_parameter['version']}, ct)
        source_rule = next(item for item in request(cloud, '/api/sf/v1/definitions', token=ct) if item['id'] == 'climate-average')
        rule_device = copy.deepcopy(next(item for item in request(edge, '/api/sf/v1/entities', token=et) if item['id'] == 'climate-1'))
        rule_device.update(id='delivery-rule-device', name='交付规则隔离设备', version=0, config={})
        request(edge, '/api/sf/v1/entities', 'POST', {'entity': rule_device, 'expected_version': 0}, et)
        wait('isolated rule device synchronized to cloud', lambda: request(cloud, '/api/sf/v1/entities', token=ct), lambda values: any(item['id'] == rule_device['id'] for item in values))
        command('formal-executable-artifact-upload-and-registration', ['python3', str(bundle / 'scripts/register-release-artifact.py'), '--bundle', str(bundle), '--directory', str(prepared), '--build-info', str(build_file)])
        def metadata_checkpoint(name, references, stage):
            observation = {'name': name, 'stage': stage, 'started_at': utc(), 'references': references, 'requests': {}}
            for label, base, path, body, context, certificate in [
                ('cloud-connector-proof', 'https://127.0.0.1:20445', '/internal/authority/connector-metadata', {'references': [ref for ref in references if ref['kind'] == 'connector']}, source_tls, 'config-service'),
                ('config-fixed-metadata', configuration, '/internal/config/v2/metadata', {'references': references}, client_tls, 'config-client-cloud-1'),
            ]:
                first_http_index = len(report['http'])
                detail = {'client_certificate': certificate, 'client_certificate_sha256': sha(prepared / ('pki/' + certificate + '.pem')), 'first_http_index': first_http_index}
                try:
                    detail.update(response=request(base, path, 'POST', body, context=context), status=200)
                except (AssertionError, OSError, ValueError) as error:
                    detail.update(error=redact(str(error)))
                detail['last_http_index'] = len(report['http']) - 1
                observation['requests'][label] = detail
            try:
                observation['connections'] = sql('metadata-connections-' + name + '-' + stage, 'SELECT datname,state,count(*) FROM pg_stat_activity WHERE datname IN (' + ','.join("'" + database + "'" for database in database_names.values()) + ') GROUP BY datname,state ORDER BY datname,state;').splitlines()
            except (RuntimeError, OSError, ValueError) as error:
                observation['connections_error'] = redact(str(error))
            observation['service_states'] = {}
            for service in ['cloud', 'config']:
                try:
                    observation['service_states'][service] = container_state(service)['State']
                except (RuntimeError, OSError, ValueError, KeyError) as error:
                    observation['service_states'][service] = {'capture_error': redact(str(error))}
            observation['completed_at'] = utc()
            report.setdefault('deployment_metadata_checkpoints', []).append(observation)
            save()
        releases = {}
        for version, parameter in [(1, first_parameter), (2, second_parameter)]:
            reference = {'kind': 'parameter', 'id': parameter['id'], 'version': parameter['version'], 'digest': parameter['content_digest']}
            rule = copy.deepcopy(source_rule)
            rule.pop('execution_plan', None)
            rule.update(id='delivery-temperature', name='交付温度计算', version=version, effective_ms=0)
            rule['selector']['device_ids'] = [rule_device['id']]
            if version == 2:
                next(node for node in rule['nodes'] if node['id'] == 'average')['params']['function'] = 'max'
            declaration = {'schema_version': 'smartfactory-release-v1', 'id': 'delivery-release-' + str(version), 'name': '交付包固定发布 ' + str(version), 'program': 'edge', 'components': [
                {'id': 'program', 'kind': 'program', 'version': build['version'], 'sha256': identity['sha256'], 'format': 'smartfactory-executable-v1', 'build': build},
                {'id': 'heartbeat', 'kind': 'configuration', 'version': str(reference['version']), 'sha256': reference['digest'], 'format': 'smartfactory-configuration-v1', 'configuration': reference},
                {'id': 'mqtt', 'kind': 'configuration', 'version': str(version), 'sha256': connector_refs[version]['digest'], 'format': 'smartfactory-configuration-v1', 'configuration': connector_refs[version], 'target_node_ids': ['edge-a'], 'required_capabilities': ['connector:mqtt_device']},
                {'id': rule['id'], 'kind': 'rule', 'version': str(version), 'sha256': hashlib.sha256(canonical(rule)).hexdigest(), 'format': '1.0', 'content': rule}]}
            references = [item['configuration'] for item in declaration['components'] if item.get('configuration')]
            metadata_checkpoint(declaration['id'], references, 'before-validation')
            try:
                validation = request(cloud, '/api/sf/v1/releases/validate', 'POST', declaration, ct)
            finally:
                metadata_checkpoint(declaration['id'], references, 'after-validation')
            check('fixed-release-valid-' + str(version), validation['valid'], validation)
            releases[version] = request(cloud, '/api/sf/v1/releases', 'POST', {'request_id': 'delivery-release-' + str(version), 'manifest': declaration}, ct)
        def deployment(name):
            return request(cloud, '/api/sf/v1/release-deployments/' + name, token=ct)
        def create_deployment(name, version):
            references = [item['configuration'] for item in releases[version]['manifest']['components'] if item.get('configuration')]
            metadata_checkpoint(name, references, 'before')
            try:
                return request(cloud, '/api/sf/v1/release-deployments', 'POST', {'id': name, 'request_id': name, 'release_id': releases[version]['id'], 'group_id': 'factory', 'batches': [['release-edge-a']], 'reason': '正式包入口与固定内容验证'}, ct)
            finally:
                metadata_checkpoint(name, references, 'after')
        def cancel(name):
            def attempt():
                current = deployment(name)
                if current['state'] == 'cancelled':
                    return current
                return request(cloud, '/api/sf/v1/release-deployments/' + name + '/actions', 'POST', {'request_id': 'cancel-' + name + '-' + str(current['version']), 'expected_version': current['version'], 'action': 'cancel', 'reason': '核对准备期间当前分配改变'}, ct)
            return wait('cancel deployment using its current business version ' + name, attempt, timeout=30)
        create_deployment('delivery-prepared-one', 1)
        try:
            command('formal-prepare-first-target', ['python3', str(bundle / 'scripts/bootstrap-runtime.py'), 'prepare-release', '--directory', str(prepared)])
        except RuntimeError:
            failed_workload = next(item for item in request(cloud, '/api/sf/v1/workload-identities', token=ct) if item['id'] == 'release-edge-a')
            failed_token = (prepared / 'release-edge-a.token').read_text().strip()
            failed_assignment = deployment('delivery-prepared-one')
            failed_references = [item['configuration'] for item in releases[1]['manifest']['components'] if item.get('configuration')]
            target = {'deployment_id': failed_assignment['id'], 'generation': failed_assignment['targets'][0]['generation'], 'references': failed_references}
            resolution = {'reference': connector_refs[1], 'credential_ref': connector_id + ':1', 'purpose': 'release-runtime'}
            source_tls = ssl.create_default_context(cafile=str(prepared / 'pki/ca.pem'))
            source_tls.load_cert_chain(str(prepared / 'pki/config-service.pem'), str(prepared / 'pki/config-service.key'))
            for name, call in [
                ('config-target', lambda: request(configuration, '/internal/config/v2/release-target', 'POST', target, failed_token, failed_workload['instance_id'], node_tls)),
                ('config-secret', lambda: request(configuration, '/internal/config/v2/credentials/resolve', 'POST', resolution, failed_token, failed_workload['instance_id'], node_tls)),
                ('node-source-health', lambda: request('https://127.0.0.1:20446', '/health', context=source_tls)),
                ('node-source-secret', lambda: request('https://127.0.0.1:20446', '/internal/config/v2/connector-credentials/resolve', 'POST', {'resolution': resolution, 'release_target': target}, failed_token, failed_workload['instance_id'], source_tls)),
            ]:
                try:
                    call()
                    report.setdefault('prepare_failure_diagnostics', {})[name] = 'passed'
                except (AssertionError, OSError, KeyError) as error:
                    report.setdefault('prepare_failure_diagnostics', {})[name] = redact(str(error))
                save()
            raise
        agent_directory = prepared / 'edge-a-release-agent'
        pending_one = json.loads((agent_directory / 'pending.json').read_text())
        check('prepare-only-leaves-original-service-online', container_state('edge-a')['State']['Running'] and pending_one['runtime']['pid'] == 0 and not (agent_directory / 'active.json').exists())
        cancel('delivery-prepared-one')
        create_deployment('delivery-prepared-two', 2)
        command('formal-prepare-changed-target', ['python3', str(bundle / 'scripts/bootstrap-runtime.py'), 'prepare-release', '--directory', str(prepared)])
        pending_two = json.loads((agent_directory / 'pending.json').read_text())
        cancel('delivery-prepared-two')
        final_assignment = create_deployment('delivery-active-one', 1)
        before_key = sha(prepared / 'edge-a.master')
        command('normal-original-source-service-stop', [*compose, 'stop', 'edge-a'])
        original_state = container_state('edge-a')['State']
        check('original-source-stops-normally', not original_state['Running'] and original_state['ExitCode'] in [0, 143] and not original_state['OOMKilled'], original_state)
        workload = next(item for item in request(cloud, '/api/sf/v1/workload-identities', token=ct) if item['id'] == 'release-edge-a')
        workload_token = (prepared / 'release-edge-a.token').read_text().strip()
        references = [item['configuration'] for item in releases[1]['manifest']['components'] if item.get('configuration')]
        request(configuration, '/internal/config/v2/release-target', 'POST', {'deployment_id': final_assignment['id'], 'generation': final_assignment['targets'][0]['generation'], 'references': references}, workload_token, workload['instance_id'], node_tls)
        request(configuration, '/internal/config/v2/credentials/resolve', 'POST', {'reference': connector_refs[1], 'credential_ref': connector_id + ':1', 'purpose': 'release-runtime'}, workload_token, workload['instance_id'], node_tls, expected=503)
        command('formal-first-service-conversion-with-source-offline', ['python3', str(bundle / 'scripts/bootstrap-runtime.py'), 'enable-release', '--directory', str(prepared)])
        first = wait('first managed release running', lambda: deployment('delivery-active-one'), lambda value: value['state'] == 'completed' and value['targets'][0]['state'] == 'running')
        active_one = json.loads((agent_directory / 'active.json').read_text())
        check('conversion-rechecks-changed-current-assignment', pending_two['release_sha256'] == releases[2]['sha256'] and active_one['release_sha256'] == releases[1]['sha256'] and active_one['generation'] > pending_two['generation'], {'prepared': pending_two, 'active': active_one})
        check('original-database-master-and-service-options-after-conversion', sha(prepared / 'edge-a.master') == before_key and json.loads((prepared / 'release-runtime-edge-a.json').read_text()) == settings)
        service_token = credentials['service_token']
        request(edge, '/internal/releases/runtime', token=service_token, expected=403)
        def running():
            identifier = container_state('edge-a-network')['Id']
            output = command('managed-runtime-loopback-' + str(len(report['commands'])), ['docker', 'exec', identifier, 'wget', '-q', '-O-', '--header', 'Authorization: Bearer ' + service_token, 'http://127.0.0.1:8091/internal/releases/runtime'], required=False)
            return json.loads(output) if output.strip() else None
        initial_runtime = wait('first managed runtime available through the private loopback endpoint', running)
        check('actual-bundle-program-and-fixed-consumer-version-one', initial_runtime['runtime']['program_sha256'] == identity['sha256'] and initial_runtime['runtime']['migration_version'] == 11 and next(item for item in initial_runtime['configurations'] if item['reference']['id'] == connector_id)['reference']['version'] == 1, initial_runtime)
        for version in [1, 2]:
            cache = agent_directory / 'releases' / (releases[version]['sha256'] + '.json')
            check('secret-cache-encrypted-' + str(version), b'ciphertext' in cache.read_bytes() and all(secret.encode() not in cache.read_bytes() for secret in secrets_to_hide if secret.startswith('delivery-secret-')))
        create_deployment('delivery-active-two', 2)
        second = wait('second release running', lambda: deployment('delivery-active-two'), lambda value: value['state'] == 'completed' and value['targets'][0]['state'] == 'running')
        updated_runtime = wait('updated managed runtime with fixed connector version two', running, lambda value: value is not None and any(item['reference']['id'] == connector_id and item['reference']['version'] == 2 for item in value['configurations']))
        check('actual-consumer-and-parameter-version-two', next(item for item in updated_runtime['configurations'] if item['reference']['id'] == connector_id)['reference']['version'] == 2 and next(item for item in updated_runtime['configurations'] if item['reference']['id'] == 'heartbeat.interval_ms')['effective_value'] == 7000, updated_runtime)
        transition = json.loads((agent_directory / 'last-transition.json').read_text())
        check('managed-process-transition-recorded', transition['old_pid'] != transition['new_pid'] and transition['old_exited_ms'] <= transition['new_ready_ms'], transition)
        at = int(time.time() * 1000)
        message = 'delivery-bundle-' + str(at)
        ingress = {'message_id': message, 'source_id': 'edge-a', 'points': [{'device_id': rule_device['id'], 'key': 'temperature', 'value': value, 'observed_ms': at + offset, 'quality': 'GOOD', 'time_source': 'simulation'} for offset, value in enumerate([10, 20])]}
        committed = request(edge, '/api/sf/v1/ingest', 'POST', ingress, service_token)
        repeated = request(edge, '/api/sf/v1/ingest', 'POST', ingress, service_token)
        check('exact-message-commits-once', committed['committed'] and not committed['duplicate'] and repeated['committed'] and repeated['duplicate'])
        def rule_output():
            output = sql('rule-output-' + str(len(report['commands'])), "SELECT data::text FROM observations WHERE definition_id='delivery-temperature' AND observed_ms>=" + str(at) + ' ORDER BY observed_ms DESC LIMIT 20;', database_names['edge-a'])
            return [json.loads(line) for line in output.splitlines() if line.startswith('{')]
        outputs = wait('fixed rule actual result', rule_output, lambda values: any(item['value'] == 20 for item in values))
        check('fixed-rule-output-from-retained-business-database', any(item['value'] == 20 for item in outputs), outputs)
        window_text = sql('complete-isolated-rule-window', "SELECT data FROM observations WHERE device_id='" + rule_device['id'] + "' AND key='temperature' AND definition_id='' AND observed_ms BETWEEN " + str(at - 60000 + 1) + ' AND ' + str(at + 1) + ' ORDER BY observed_ms,id,revision;', database_names['edge-a'])
        window_inputs = [json.loads(line) for line in window_text.splitlines() if line.startswith('{')]
        check('complete-rule-window-contains-only-declared-ingress', len(window_inputs) == 2 and all(point['message_id'] == message and point['quality'] == 'GOOD' for point in window_inputs) and sorted(point['value'] for point in window_inputs) == [10, 20], {'from_ms': at - 60000 + 1, 'to_ms': at + 1, 'inputs': window_inputs})
        before_return = next(item for item in request(edge, '/api/sf/v1/definitions', token=et) if item['id'] == 'delivery-temperature')
        metadata_checkpoint('delivery-return-one', references=[item['configuration'] for item in releases[1]['manifest']['components'] if item.get('configuration')], stage='before')
        current_two = deployment('delivery-active-two')
        returned = request(cloud, '/api/sf/v1/release-deployments/delivery-active-two/rollback', 'POST', {'request_id': 'delivery-return-one', 'expected_version': current_two['version'], 'id': 'delivery-return-one', 'release_id': releases[1]['id'], 'reason': '使用同一业务数据库核对兼容退回与规则重新采用'}, ct)
        metadata_checkpoint('delivery-return-one', references=[item['configuration'] for item in releases[1]['manifest']['components'] if item.get('configuration')], stage='after')
        wait('compatible release rollback completed', lambda: deployment('delivery-return-one'), lambda value: value['state'] == 'completed' and value['targets'][0]['state'] == 'running')
        returned_runtime = wait('returned fixed configuration version one', running, lambda value: value is not None and value['runtime']['release_sha256'] == releases[1]['sha256'])
        returned_rule = next(item for item in request(edge, '/api/sf/v1/definitions', token=et) if item['id'] == 'delivery-temperature')
        check('compatible-rollback-keeps-database-and-reapplies-rule', returned['rollback_of'] == 'delivery-active-two' and returned_runtime['runtime']['migration_version'] == 11 and returned_rule['version'] > before_return['version'] and next(node for node in returned_rule['nodes'] if node['id'] == 'average')['params']['function'] == 'avg' and next(item for item in returned_runtime['configurations'] if item['reference']['id'] == connector_id)['reference']['version'] == 1 and next(item for item in returned_runtime['configurations'] if item['reference']['id'] == 'heartbeat.interval_ms')['effective_value'] == 5000, {'runtime': returned_runtime, 'rule': returned_rule})
        create_deployment('delivery-forward-two', 2)
        wait('release forward after rollback completed', lambda: deployment('delivery-forward-two'), lambda value: value['state'] == 'completed' and value['targets'][0]['state'] == 'running')
        updated_runtime = wait('forward fixed configuration version two', running, lambda value: value is not None and value['runtime']['release_sha256'] == releases[2]['sha256'])
        forward_rule = next(item for item in request(edge, '/api/sf/v1/definitions', token=et) if item['id'] == 'delivery-temperature')
        check('rule-and-configuration-round-trip', forward_rule['version'] > returned_rule['version'] and next(node for node in forward_rule['nodes'] if node['id'] == 'average')['params']['function'] == 'max' and next(item for item in updated_runtime['configurations'] if item['reference']['id'] == connector_id)['reference']['version'] == 2 and next(item for item in updated_runtime['configurations'] if item['reference']['id'] == 'heartbeat.interval_ms')['effective_value'] == 7000, {'runtime': updated_runtime, 'rule': forward_rule})
        command('stop-agent-before-offline-cached-restart', [*compose, '--profile', 'release', 'stop', 'edge-a-release-agent'])
        command('stop-authority-and-configuration-for-cached-restart', [*compose, '--profile', 'release', 'stop', 'cloud', 'config'])
        command('start-agent-from-cache-with-authorities-offline', [*compose, '--profile', 'release', 'up', '-d', '--no-deps', '--pull', 'never', 'edge-a-release-agent'])
        offline = wait('cached application runtime without cloud or config', running, lambda value: value is not None and value['runtime']['release_sha256'] == releases[2]['sha256'] and value['runtime']['healthy'])
        check('offline-cached-program-and-database-preserved', offline['runtime']['program_sha256'] == identity['sha256'] and offline['runtime']['migration_version'] == updated_runtime['runtime']['migration_version'] and not container_state('cloud')['State']['Running'] and not container_state('config')['State']['Running'], offline)
        command('formal-subsequent-start-entrypoint', ['python3', str(bundle / 'scripts/bootstrap-runtime.py'), 'start', '--directory', str(prepared)])
        wait('authority returns after formal start', lambda: request(cloud, '/health'))
        wait('configuration returns after formal start', lambda: request(configuration, '/health', context=client_tls))
        wait('current managed report returns', lambda: deployment('delivery-forward-two'), lambda value: value['targets'][0]['report_fresh'] and value['targets'][0]['runtime']['process_instance_id'] == offline['runtime']['process_instance_id'])
        check('subsequent-start-keeps-original-direct-service-stopped', not container_state('edge-a')['State']['Running'] and json.loads((prepared / 'profile.json').read_text())['release_managed_nodes'] == ['edge-a'])
        repeated_after = request(edge, '/api/sf/v1/ingest', 'POST', ingress, service_token)
        check('duplicate-state-survives-managed-cached-restart', repeated_after['committed'] and repeated_after['duplicate'])
        for base, token in [(cloud, ct), (edge, et)]:
            audit = request(base, '/api/sf/v1/audit/verify', 'POST', {}, token)
            check('audit-valid-' + ('cloud' if base == cloud else 'edge'), audit['valid'] and not audit['issues'], audit)
        database_info = sql('final-private-database-identities', 'SELECT datname,pg_get_userbyid(datdba) FROM pg_database WHERE datname IN (' + ','.join("'" + name + "'" for name in database_names.values()) + ') ORDER BY datname;')
        report['database_identities'] = database_info.splitlines()
        report['finite_workload'] = {'managed_nodes': 1, 'program_artifacts': 1, 'fixed_releases': 2, 'deployments': 6, 'prepare_only_runs': 3, 'compatible_rollbacks': 1, 'rule_configuration_round_trips': 1, 'isolated_rule_device': rule_device['id'],
                                     'new_ingress_messages': 1, 'duplicate_requests': 2, 'connector_source_versions': 2, 'managed_program_restart_with_cloud_and_config_offline': 1,
                                     'real_protocol_simulation': 'five seeded scenes, actual DataTransfer executable and persistent ConfigManager/gRPC consumer'}
        report['status'] = 'passed'
    except Exception as error:
        report['status'] = 'failed'
        report['error'] = redact(str(error))
        report['error_traceback'] = redact(traceback.format_exc())
        raise
    finally:
        stop_samples.set()
        if 'sampler' in locals():
            sampler.join(timeout=30)
        if started:
            command('formal-prepared-stack-stop', ['python3', str(bundle / 'scripts/bootstrap-runtime.py'), 'stop', '--directory', str(prepared)], timeout=180, required=False)
            final_states = {}
            for name in services:
                identifier = command('final-id-' + name, [*compose, '--profile', 'release', '--profile', 'direct-runtime', 'ps', '-a', '-q', name], required=False).strip()
                if not identifier:
                    continue
                info = json.loads(command('final-state-' + name, ['docker', 'inspect', identifier]))[0]
                final_states[name] = info['State']
                command('final-log-' + name, ['docker', 'logs', identifier], required=False)
            report['final_states'] = final_states
            exits_ok = all(not state['Running'] and not state['OOMKilled'] and state['ExitCode'] in [0, 143] for state in final_states.values())
            report['checks'].append({'name': 'all-own-services-stopped-normally-without-oom', 'passed': exits_ok, 'detail': final_states})
            sessions = sql('final-own-database-connections', 'SELECT datname,count(*) FROM pg_stat_activity WHERE datname IN (' + ','.join("'" + name + "'" for name in database_names.values()) + ') GROUP BY datname;').strip()
            report['checks'].append({'name': 'all-own-database-connections-released', 'passed': not sessions, 'detail': sessions})
            command('remove-only-own-prepared-composition', [*compose, '--profile', 'release', '--profile', 'direct-runtime', 'down'], required=False)
        report['status'] = 'passed' if report['status'] == 'passed' and all(item['passed'] for item in report['checks']) else 'failed'
        report['completed_at'] = utc()
        report['all_checks_passed'] = report['status'] == 'passed'
        save()
    print('Verified Linux bundle service conversion: ' + str(work / 'verification.json'))


if __name__ == '__main__':
    main()
