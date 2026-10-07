#!/usr/bin/env python3
"""Build frozen inputs and exercise production release processes in isolated directories."""
import argparse
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import sqlite3
import ssl
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BUILDINFO = 'competition2026/product/platform/pkg/buildinfo.'


def digest_bytes(value):
    return hashlib.sha256(value).hexdigest()


def canonical(value):
    # Go's canonical map JSON escapes these characters; examples use ASCII keys.
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':')).replace('<', '\\u003c').replace('>', '\\u003e').replace('&', '\\u0026').encode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--keep-running', action='store_true')
    parser.add_argument('--skip-artifact-fault', action='store_true')
    args = parser.parse_args()
    directory = args.directory.resolve()
    directory.mkdir(parents=True, exist_ok=False)
    directory.chmod(0o700)
    private, evidence, binaries, source = [directory / name for name in ['private', 'evidence', 'bin', 'source']]
    for path in [private, evidence, binaries, source]:
        path.mkdir(mode=0o700)
    commands, http_records, assertions, processes, process_records = [], [], [], {}, []
    compiler_input_paths = {}
    result = {'status': 'running', 'directory': str(directory), 'commands': str(evidence / 'commands.json'), 'http': str(evidence / 'http.json'), 'assertions': str(evidence / 'assertions.json')}

    def save(path, value):
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
        path.chmod(0o600)

    def check(name, condition, detail=None):
        assertions.append({'case': name, 'passed': bool(condition), 'detail': detail})
        save(evidence / 'assertions.json', assertions)
        if not condition:
            raise AssertionError(name)

    def command(name, argv, cwd=None, env=None):
        building = argv[:2] == ['go', 'build']
        before = {key: digest_bytes(path.read_bytes()) for key, path in compiler_input_paths.items()} if building else None
        if before is not None:
            save(evidence / (name + '.inputs-before.json'), before)
        started = time.time()
        completed = subprocess.run(argv, cwd=cwd or source / 'Platform', env=env, capture_output=True, text=True)
        stdout, stderr = evidence / (name + '.stdout.log'), evidence / (name + '.stderr.log')
        stdout.write_text(completed.stdout)
        stderr.write_text(completed.stderr)
        entry = {'name': name, 'argv': argv, 'cwd': str(cwd or source / 'Platform'), 'started_at': started, 'elapsed_seconds': time.time() - started, 'exit_code': completed.returncode, 'stdout': str(stdout), 'stderr': str(stderr)}
        if building:
            after = {key: digest_bytes(path.read_bytes()) for key, path in compiler_input_paths.items()}
            save(evidence / (name + '.inputs-after.json'), after)
            entry.update(inputs_before=str(evidence / (name + '.inputs-before.json')), inputs_after=str(evidence / (name + '.inputs-after.json')), changed_inputs=[key for key in before if before[key] != after[key]])
        commands.append(entry)
        save(evidence / 'commands.json', commands)
        if completed.returncode:
            raise RuntimeError(name + ' failed')
        if entry.get('changed_inputs'):
            raise RuntimeError(name + ' inputs changed while compiling')
        return completed.stdout

    def free_port():
        with socket.socket() as stream:
            stream.bind(('127.0.0.1', 0))
            return stream.getsockname()[1]

    def start(name, argv, env=None):
        out, err = open(evidence / (name + '.stdout.log'), 'a'), open(evidence / (name + '.stderr.log'), 'a')
        process = subprocess.Popen(argv, cwd=directory, env=env, stdout=out, stderr=err, start_new_session=True)
        processes[name] = (process, out, err)
        process_records.append({'name': name, 'pid': process.pid, 'argv': argv, 'started_at': time.time()})
        save(evidence / 'processes.json', process_records)
        return process

    def stop(name, kill=False):
        if name not in processes:
            return
        process, out, err = processes.pop(name)
        if process.poll() is None:
            if kill:
                process.kill()
            else:
                process.terminate()
            try:
                process.wait(timeout=18)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                check('graceful-exit-' + name, False, {'pid': process.pid})
        out.close()
        err.close()
        process_records.append({'name': name, 'pid': process.pid, 'exit_code': process.returncode, 'exited_at': time.time(), 'injected_sigkill': kill})
        save(evidence / 'processes.json', process_records)

    def wait_for(name, call, accept=lambda value: bool(value), timeout=45):
        until, last = time.monotonic() + timeout, None
        while time.monotonic() < until:
            try:
                last = call()
                if accept(last):
                    return last
            except (OSError, urllib.error.URLError, AssertionError, KeyError) as error:
                last = type(error).__name__ + ': ' + str(error)
            time.sleep(0.2)
        raise AssertionError(name + ' timed out: ' + str(last)[:1000])

    tls = None

    def request(base, path, method='GET', value=None, token=None, instance=None, expected=200, context=None, record=True, raw=None):
        body = raw if raw is not None else (None if value is None else json.dumps(value).encode())
        headers = {'Content-Type': 'application/octet-stream' if raw is not None else 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if instance:
            headers['X-SF-Instance-ID'] = instance
        req = urllib.request.Request(base + path, data=body, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, context=context or (tls if base.startswith('https:') else None), timeout=15) as response:
                status, data = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, data = error.code, error.read()
        try:
            response = json.loads(data)
        except (ValueError, UnicodeDecodeError):
            response = data.decode(errors='replace')
        if record:
            visible = '[private]' if path.endswith('/login') or path.endswith('/credentials/resolve') else response
            http_records.append({'at': time.time(), 'method': method, 'base': base, 'path': path, 'status': status, 'response': visible})
            save(evidence / 'http.json', http_records)
        if status != expected:
            raise AssertionError(f'{method} {path}: HTTP {status}, expected {expected}: {str(response)[:1000]}')
        return response

    def sqlite_rows(path, query, parameters=()):
        with sqlite3.connect('file:' + str(path) + '?mode=ro', uri=True, timeout=5) as db:
            return db.execute(query, parameters).fetchall()

    def documents(path, kind):
        return [json.loads(row[0]) for row in sqlite_rows(path, 'SELECT data FROM documents WHERE kind=? ORDER BY id', (kind,))]

    def persisted_runtime(node):
        local = node_directories[node]
        return request(node_urls[node], '/internal/releases/runtime', token=(local / 'runtime-token').read_text().strip(), record=False)

    try:
        # The tested checkout is immutable and independent of concurrent working-tree edits.
        for module in ['Platform', 'DataTransfer']:
            shutil.copytree(ROOT / module, source / module, ignore=shutil.ignore_patterns('.local', '.cache', '.git', '.DS_Store', 'node_modules', '*.db', '*.db-*'))
        shutil.copytree(ROOT / 'contracts', source / 'contracts')
        shutil.copytree(ROOT / 'examples', source / 'examples', ignore=shutil.ignore_patterns('.local'))
        shutil.copytree(ROOT / 'scripts', source / 'scripts', ignore=shutil.ignore_patterns('__pycache__', '.DS_Store'))
        build_env = dict(os.environ, GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath', GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache', GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOPROXY='off', GOMAXPROCS='2')
        command('go-version', ['go', 'version'], env=build_env)
        packages = command('go-list-inputs', ['go', 'list', '-deps', '-json', './cmd/sf-cloud', './cmd/sf-config', './cmd/sf-edge', './cmd/sf-release-agent', './cmd/sf-pki'], env=build_env)
        packages += command('go-list-datatransfer-inputs', ['go', 'list', '-deps', '-json', './cmd/dt-node-configuration-fixture'], cwd=source / 'DataTransfer', env=build_env)
        decoder, offset, production = json.JSONDecoder(), 0, {}
        external_paths = set()
        while offset < len(packages):
            while offset < len(packages) and packages[offset].isspace():
                offset += 1
            if offset >= len(packages):
                break
            package, offset = decoder.raw_decode(packages, offset)
            package_dir = Path(package['Dir'])
            for field in ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles']:
                for name in package.get(field, []):
                    path = package_dir / name
                    if path.is_relative_to(source):
                        production[str(path.relative_to(source))] = digest_bytes(path.read_bytes())
                    else:
                        external_paths.add(path)
            module = package.get('Module', {})
            module = module.get('Replace', module)
            if module.get('GoMod'):
                for path in [Path(module['GoMod']), Path(module['GoMod']).with_name('go.sum')]:
                    if path.is_file():
                        if path.is_relative_to(source):
                            production[str(path.relative_to(source))] = digest_bytes(path.read_bytes())
                        else:
                            external_paths.add(path)
        for path in [source / 'Platform/go.mod', source / 'Platform/go.sum', source / 'DataTransfer/go.mod', source / 'DataTransfer/go.sum', *sorted((source / 'contracts').rglob('*.json'))]:
            production[str(path.relative_to(source))] = digest_bytes(path.read_bytes())
        source_sha = digest_bytes(canonical(production))
        save(evidence / 'compiled-inputs.json', {'sha256': source_sha, 'files': production, 'go_list': str(evidence / 'go-list-inputs.stdout.log')})
        tool_environment = json.loads(command('go-environment', ['go', 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'GOROOT', 'GOTOOLDIR', 'CC', 'CGO_CFLAGS', 'CGO_CPPFLAGS', 'CGO_LDFLAGS'], env=build_env))
        for executable in [shutil.which('go'), shutil.which(tool_environment['CC'].split()[0])]:
            if executable:
                external_paths.add(Path(executable).resolve())
        for name in ['compile', 'link', 'asm', 'cgo', 'pack']:
            path = Path(tool_environment['GOTOOLDIR']) / name
            if path.is_file():
                external_paths.add(path)
        external_root = directory / 'external-inputs'
        external_root.mkdir()
        external_records = []
        for path in sorted(external_paths):
            sha = digest_bytes(path.read_bytes())
            retained = external_root / sha
            if not retained.exists():
                retained.write_bytes(path.read_bytes())
                retained.chmod(0o600)
            external_records.append({'path': str(path), 'sha256': sha, 'bytes': path.stat().st_size, 'retained_copy': str(retained)})
            compiler_input_paths[str(path)] = path
        for relative in production:
            compiler_input_paths[str(source / relative)] = source / relative
        save(evidence / 'external-compiled-inputs.json', external_records)
        save(evidence / 'all-compiled-inputs-before.json', {key: digest_bytes(path.read_bytes()) for key, path in compiler_input_paths.items()})
        result['source'] = str(source)
        result['source_sha256'] = source_sha
        result['local_compiled_input_count'] = len(production)
        result['external_compiled_input_count'] = len(external_records)
        for name in ['sf-cloud', 'sf-config', 'sf-release-agent', 'sf-pki']:
            command('build-' + name, ['go', 'build', '-p', '1', '-trimpath', '-ldflags', f'-X {BUILDINFO}Version=phase5-coordinator -X {BUILDINFO}SourceSHA256={source_sha}', '-o', str(binaries / name), './cmd/' + name], env=build_env)
        for version, maximum in [('v1', '11'), ('v2', '11'), ('v3', '11'), ('legacy', '9')]:
            command('build-edge-' + version, ['go', 'build', '-p', '1', '-trimpath', '-ldflags', f'-X {BUILDINFO}Version=phase5-{version} -X {BUILDINFO}SourceSHA256={source_sha} -X {BUILDINFO}MigrationMinimum=1 -X {BUILDINFO}MigrationMaximum={maximum}', '-o', str(binaries / ('sf-edge-' + version)), './cmd/sf-edge'], env=build_env)
        command('build-datatransfer-fixture', ['go', 'build', '-p', '1', '-trimpath', '-o', str(binaries / 'dt-node-configuration-fixture'), './cmd/dt-node-configuration-fixture'], cwd=source / 'DataTransfer', env=build_env)
        save(evidence / 'all-compiled-inputs-after.json', {key: digest_bytes(path.read_bytes()) for key, path in compiler_input_paths.items()})
        builds = {version: json.loads(command('build-info-' + version, [str(binaries / ('sf-edge-' + version)), '-build-info'])) for version in ['v1', 'v2', 'v3', 'legacy']}
        save(evidence / 'binaries.json', [{'path': str(path), 'sha256': digest_bytes(path.read_bytes()), 'size': path.stat().st_size} for path in sorted(binaries.iterdir())])
        pki = private / 'pki'
        command('pki-init', [str(binaries / 'sf-pki'), '-mode', 'init', '-dir', str(pki)])
        for name, client in [('cloud-1', False), ('config-service', True), ('config-client-cloud-1', True), ('edge-a', True), ('edge-b', True)]:
            argv = [str(binaries / 'sf-pki'), '-dir', str(pki), '-node', name, '-hosts', 'localhost,127.0.0.1']
            if client:
                argv.append('-client')
            command('pki-' + name, argv)
        tls = ssl.create_default_context(cafile=str(pki / 'ca.pem'))
        tls.load_cert_chain(str(pki / 'config-client-cloud-1.pem'), str(pki / 'config-client-cloud-1.key'))
        cloud_port, config_port, authority_port, a_port, b_port, sync_port, source_a_port, source_b_port, dt_a_port, dt_b_port, dt_a_http, dt_b_http = [free_port() for _ in range(12)]
        cloud_url, config_url, authority_url = f'http://127.0.0.1:{cloud_port}', f'https://127.0.0.1:{config_port}', f'https://127.0.0.1:{authority_port}'
        node_urls = {'a': f'http://127.0.0.1:{a_port}', 'b': f'http://127.0.0.1:{b_port}'}
        password = secrets.token_urlsafe(32)
        password_file = private / 'bootstrap-password'
        password_file.write_text(password)
        password_file.chmod(0o600)
        credentials = {node: secrets.token_urlsafe(48) for node in ['a', 'b', 'cloud']}
        capabilities = ['release', 'goos:darwin', 'goarch:arm64', 'config.v2', 'config.read', 'config.report', 'credential.resolve', 'connector:mqtt_device']
        identities = [{'id': 'release-' + node, 'node_id': 'edge-' + node, 'program': 'edge', 'purpose': 'release-runtime', 'enabled': True, 'capabilities': capabilities, 'parameter_ids': ['heartbeat.interval_ms', 'release.private'], 'connector_ids': ['edge-' + node + '/mqtt-' + node]} for node in ['a', 'b']]
        identities.append({'id': 'cloud-parameters', 'node_id': 'cloud-1', 'program': 'cloud', 'purpose': 'coordinator-runtime', 'enabled': True, 'capabilities': ['config.v2', 'config.read', 'config.report'], 'parameter_ids': ['control.start_ttl_ms'], 'connector_ids': []})
        save(private / 'workloads.json', [{'identity': item, 'credential': credentials[node]} for item, node in zip(identities, ['a', 'b', 'cloud'])])
        for node, credential in credentials.items():
            path = private / (node + '.token')
            path.write_text(credential)
            path.chmod(0o600)
        base_env = {key: value for key, value in os.environ.items() if not key.startswith(('SF_', 'OTEL_'))}
        cloud_env = dict(base_env, SF_LISTEN=f'127.0.0.1:{cloud_port}', SF_DATABASE=str(private / 'cloud.db'), SF_NODE_ID='cloud-1', SF_MASTER_KEY_FILE=str(private / 'cloud.key'), SF_BOOTSTRAP_PASSWORD=password, SF_WORKLOAD_BOOTSTRAP_FILE=str(private / 'workloads.json'), SF_AUTHORITY_LISTEN=f'127.0.0.1:{authority_port}', SF_AUTHORITY_TLS_CA=str(pki / 'ca.pem'), SF_AUTHORITY_TLS_CERT=str(pki / 'cloud-1.pem'), SF_AUTHORITY_TLS_KEY=str(pki / 'cloud-1.key'), SF_CONFIG_URL=config_url, SF_CONFIG_TLS_CA=str(pki / 'ca.pem'), SF_CONFIG_TLS_CERT=str(pki / 'config-client-cloud-1.pem'), SF_CONFIG_TLS_KEY=str(pki / 'config-client-cloud-1.key'), SF_CONFIG_TOKEN_FILE=str(private / 'cloud.token'), SF_RELEASE_ARTIFACT_ROOT=str(private / 'artifacts'), SF_STATIC_DIR=str(ROOT / 'Frontends/dist'))
        cloud_env.update(SF_SYNC_LISTEN=f'127.0.0.1:{sync_port}', SF_TLS_CA=str(pki / 'ca.pem'), SF_TLS_CERT=str(pki / 'cloud-1.pem'), SF_TLS_KEY=str(pki / 'cloud-1.key'))
        config_env = dict(base_env, SF_LISTEN=f'127.0.0.1:{config_port}', SF_DATABASE=str(private / 'configuration.db'), SF_NODE_ID='config-1', SF_MASTER_KEY_FILE=str(private / 'configuration.key'), SF_BOOTSTRAP_PASSWORD=password, SF_CONFIG_TLS_CA=str(pki / 'ca.pem'), SF_CONFIG_TLS_CERT=str(pki / 'config-service.pem'), SF_CONFIG_TLS_KEY=str(pki / 'config-service.key'), SF_CONFIG_AUTHORITY_URL=authority_url, SF_STATIC_DIR='')
        save(private / 'service-environments.json', {'cloud': cloud_env, 'config': config_env})
        node_directories = {node: private / ('agent-' + node) for node in ['a', 'b']}
        runtime_directories = {node: private / ('existing-runtime-' + node) for node in ['a', 'b']}
        for path in runtime_directories.values():
            path.mkdir()
        public_keys = {}
        for node, key_path in [('cloud-1', private / 'cloud.key'), ('edge-a', runtime_directories['a'] / 'original.key'), ('edge-b', runtime_directories['b'] / 'original.key')]:
            key_path.write_text(base64.b64encode(os.urandom(32)).decode())
            key_path.chmod(0o600)
            public_keys[node] = json.loads(command('public-keys-' + node, [str(binaries / 'sf-pki'), '--mode', 'public-keys', '--dir', str(pki), '--node', node, '--master-key', str(key_path)]))
        agent_argv, original_argv, original_env = {}, {}, {}
        for node, port in [('a', a_port), ('b', b_port)]:
            source_port = source_a_port if node == 'a' else source_b_port
            runtime_env = {'OTEL_SERVICE_NAME': 'release-fixture-edge-' + node, 'SF_SYNC_URL': f'https://127.0.0.1:{sync_port}', 'SF_CLOUD_SIGNING_KEY': public_keys['cloud-1']['audit_public_key'], 'SF_TLS_CA': str(pki / 'ca.pem'), 'SF_TLS_CERT': str(pki / ('edge-' + node + '.pem')), 'SF_TLS_KEY': str(pki / ('edge-' + node + '.key')), 'SF_CONFIG_AUTHORITY_URL': authority_url, 'SF_CONFIG_TLS_CA': str(pki / 'ca.pem'), 'SF_CONFIG_TLS_CERT': str(pki / ('edge-' + node + '.pem')), 'SF_CONFIG_TLS_KEY': str(pki / ('edge-' + node + '.key')), 'SF_AUTHORITY_LISTEN': f'127.0.0.1:{source_port}', 'SF_AUTHORITY_TLS_CA': str(pki / 'ca.pem'), 'SF_AUTHORITY_TLS_CERT': str(pki / ('edge-' + node + '.pem')), 'SF_AUTHORITY_TLS_KEY': str(pki / ('edge-' + node + '.key'))}
            save(private / (node + '-runtime.json'), {'database': str(runtime_directories[node] / 'original.db'), 'master_key_file': str(runtime_directories[node] / 'original.key'), 'static_directory': str(ROOT / 'Frontends/dist'), 'environment': runtime_env})
            node_directories[node].mkdir(mode=0o700)
            runtime_token = secrets.token_urlsafe(48)
            (node_directories[node] / 'runtime-token').write_text(runtime_token)
            (node_directories[node] / 'runtime-token').chmod(0o600)
            original_argv[node] = [str(binaries / 'sf-edge-v1'), '-node-id', 'edge-' + node, '-listen', '127.0.0.1:' + str(port), '-database', str(runtime_directories[node] / 'original.db'), '-master-key', str(runtime_directories[node] / 'original.key'), '-datatransfer', f'127.0.0.1:{dt_a_port if node == "a" else dt_b_port}', '-seed']
            original_env[node] = dict(base_env, **runtime_env, SF_BOOTSTRAP_PASSWORD=password, SF_SERVICE_TOKEN=runtime_token)
            agent_argv[node] = [str(binaries / 'sf-release-agent'), '--cloud', authority_url, '--config', config_url, '--dir', str(node_directories[node]), '--node-id', 'edge-' + node, '--listen', '127.0.0.1:' + str(port), '--token-file', str(private / (node + '.token')), '--bootstrap-password-file', str(password_file), '--runtime-config', str(private / (node + '-runtime.json')), '--ca', str(pki / 'ca.pem'), '--cert', str(pki / ('edge-' + node + '.pem')), '--key', str(pki / ('edge-' + node + '.key')), '--seed', '--poll', '500ms']
            agent_argv[node].extend(['--datatransfer', f'127.0.0.1:{dt_a_port if node == "a" else dt_b_port}'])
        save(private / 'launch.json', {'cloud_argv': [str(binaries / 'sf-cloud'), '-seed'], 'config_argv': [str(binaries / 'sf-config')], 'agents': agent_argv, 'cloud_url': cloud_url, 'config_url': config_url, 'authority_url': authority_url, 'node_urls': node_urls, 'password_file': str(password_file)})
        start('config', [str(binaries / 'sf-config')], config_env)
        wait_for('config health', lambda: request(config_url, '/health', record=False))
        start('cloud', [str(binaries / 'sf-cloud'), '-seed'], cloud_env)
        wait_for('cloud health', lambda: request(cloud_url, '/health', record=False))
        login = request(cloud_url, '/api/sf/v1/login', 'POST', {'login': 'admin', 'password': password})
        token = login['token']
        (private / 'admin.token').write_text(token)
        (private / 'admin.token').chmod(0o600)
        readonly_password = secrets.token_urlsafe(32)
        readonly_file = private / 'readonly-password'
        readonly_file.write_text(readonly_password)
        readonly_file.chmod(0o600)
        request(cloud_url, '/api/sf/v1/users', 'POST', {'user': {'id': 'release-viewer', 'login': 'release-viewer', 'name': '发布只读验证', 'active': True, 'roles': ['viewer'], 'resources': ['factory']}, 'password': readonly_password, 'expected_version': 0}, token)
        for node, source_port, dt_port, dt_http in [('a', source_a_port, dt_a_port, dt_a_http), ('b', source_b_port, dt_b_port, dt_b_http)]:
            entity = next(entry for entry in request(cloud_url, '/api/sf/v1/entities', token=token) if entry['id'] == 'edge-' + node)
            entity['config'] = dict(public_keys['edge-' + node], configuration_url=f'https://127.0.0.1:{source_port}')
            request(cloud_url, '/api/sf/v1/entities', 'POST', {'entity': entity, 'expected_version': entity['version']}, token)
            start('datatransfer-' + node, [str(binaries / 'dt-node-configuration-fixture'), '--directory', str(private / ('datatransfer-' + node)), '--grpc-listen', f'127.0.0.1:{dt_port}', '--http-listen', f'127.0.0.1:{dt_http}'], base_env)
            wait_for('real DataTransfer consumer ' + node, lambda dt_http=dt_http: request(f'http://127.0.0.1:{dt_http}', '/runtime', record=False))
            start('original-' + node, original_argv[node], original_env[node])
            wait_for('existing Edge service ' + node, lambda node=node: request(node_urls[node], '/health', record=False))
        connector_secrets, connector_refs = [], {1: {}, 2: {}}
        for node in ['a', 'b']:
            edge_token = wait_for('edge business login ' + node, lambda node=node: request(node_urls[node], '/api/sf/v1/login', 'POST', {'login': 'admin', 'password': password}))['token']
            for version in [1, 2]:
                connector_secret = 'connector-probe-' + secrets.token_urlsafe(20)
                connector_secrets.append(connector_secret)
                body = {'request_id': f'connector-{node}-{version}', 'expected_version': version - 1, 'edge_id': 'edge-' + node, 'group_id': 'factory', 'protocol': 'mqtt_device', 'parameters': {'kind': 'connector', 'connector_id': 'mqtt-' + node, 'connection': {'url': 'tcp://127.0.0.1:1883', 'username': 'release-fixture', 'password': connector_secret}, 'polling': {'interval_millis': version * 1000}}}
                request(node_urls[node], '/api/sf/v1/connector-configurations', 'POST', body, edge_token)
            wait_for('connector public versions synchronize ' + node, lambda: request(cloud_url, '/api/sf/v1/connector-configurations', token=token, record=False), lambda entries, node=node: any(c['configuration']['id'] == 'edge-' + node + '/mqtt-' + node and c['configuration']['version'] == 2 for c in entries))
            for version in [1, 2]:
                metadata = request(config_url, '/internal/config/v2/metadata', 'POST', {'references': [{'kind': 'connector', 'id': 'edge-' + node + '/mqtt-' + node, 'version': version, 'digest': ''}]})[0]
                connector_refs[version][node] = metadata['reference']
        config_v1 = next(p for p in request(cloud_url, '/api/sf/v1/config', token=token) if p['id'] == 'heartbeat.interval_ms')
        config_v2 = copy.deepcopy(config_v1)
        config_v2['value'] = 7000
        config_v2['target_node_ids'] = ['edge-a', 'edge-b']
        config_v2 = request(cloud_url, '/api/sf/v1/config', 'POST', {'parameter': config_v2, 'expected_version': config_v1['version']}, token)
        secret_value = 'release-cache-probe-' + secrets.token_urlsafe(18)
        secret = request(cloud_url, '/api/sf/v1/config', 'POST', {'parameter': {'id': 'release.private', 'program': 'edge', 'category': 'release-fixture', 'description': '私有配置缓存校验', 'schema': {'type': 'string'}, 'value': secret_value, 'dynamic': True, 'secret': True, 'target_node_ids': ['edge-a', 'edge-b']}, 'expected_version': 0}, token)
        ref = lambda p: {'kind': 'parameter', 'id': p['id'], 'version': p['version'], 'digest': p['content_digest']}
        cfg_refs = {'v1': ref(config_v1), 'v2': ref(config_v2), 'v3': ref(config_v2), 'legacy': ref(config_v1)}
        defs = request(cloud_url, '/api/sf/v1/definitions', token=token)
        base_rule = next(d for d in defs if d['id'] == 'climate-average')
        manifests = {}
        for version in ['v1', 'v2', 'v3', 'legacy']:
            info = builds[version]
            request(cloud_url, '/api/sf/v1/release-artifacts/' + info['sha256'], 'PUT', token=token, raw=(binaries / ('sf-edge-' + version)).read_bytes())
            request(cloud_url, '/api/sf/v1/release-artifacts', 'POST', {'sha256': info['sha256'], 'build': info['build']}, token)
            rule = copy.deepcopy(base_rule)
            rule.pop('execution_plan', None)
            rule['id'], rule['name'], rule['version'], rule['effective_ms'] = 'release-temperature', '发布温度计算', 1 if version in ['v1', 'legacy'] else 2, 0
            if version in ['v2', 'v3']:
                next(n for n in rule['nodes'] if n['id'] == 'average')['params']['function'] = 'max'
            components = [
                {'id': 'program', 'kind': 'program', 'version': info['build']['version'], 'sha256': info['sha256'], 'format': 'smartfactory-executable-v1', 'build': info['build']},
                {'id': 'heartbeat', 'kind': 'configuration', 'version': str(cfg_refs[version]['version']), 'sha256': cfg_refs[version]['digest'], 'format': 'smartfactory-configuration-v1', 'configuration': cfg_refs[version]},
                {'id': 'private-parameter', 'kind': 'configuration', 'version': str(secret['version']), 'sha256': secret['content_digest'], 'format': 'smartfactory-configuration-v1', 'configuration': ref(secret)},
                {'id': rule['id'], 'kind': 'rule', 'version': str(rule['version']), 'sha256': digest_bytes(canonical(rule)), 'format': '1.0', 'content': rule, 'depends_on': [{'id': 'program', 'version': info['build']['version'], 'sha256': info['sha256']}, {'id': 'heartbeat', 'version': str(cfg_refs[version]['version']), 'sha256': cfg_refs[version]['digest']}]}
            ]
            connector_version = 1 if version in ['v1', 'legacy'] else 2
            for node in ['a', 'b']:
                reference = connector_refs[connector_version][node]
                components.append({'id': 'connector-' + node, 'kind': 'configuration', 'version': str(reference['version']), 'sha256': reference['digest'], 'format': 'smartfactory-configuration-v1', 'configuration': reference, 'target_node_ids': ['edge-' + node], 'required_capabilities': ['connector:mqtt_device']})
            manifest = {'schema_version': 'smartfactory-release-v1', 'id': 'release-' + version, 'name': '实际进程发布 ' + version, 'program': 'edge', 'components': components}
            validation = request(cloud_url, '/api/sf/v1/releases/validate', 'POST', manifest, token)
            check('manifest-valid-' + version, validation['valid'], validation)
            release = request(cloud_url, '/api/sf/v1/releases', 'POST', {'request_id': 'create-' + version, 'manifest': manifest}, token)
            manifests[version] = release
        save(evidence / 'releases.json', manifests)
        # Content checks use the production validator and immutable API identities.
        bad = copy.deepcopy(manifests['v1']['manifest'])
        next(c for c in bad['components'] if c['kind'] == 'rule')['content']['name'] = 'changed without digest'
        check('rule-tamper-rejected', not request(cloud_url, '/api/sf/v1/releases/validate', 'POST', bad, token)['valid'])
        bad = copy.deepcopy(manifests['v1']['manifest'])
        next(c for c in bad['components'] if c['kind'] == 'rule')['depends_on'][0]['id'] = 'missing'
        check('missing-dependency-rejected', not request(cloud_url, '/api/sf/v1/releases/validate', 'POST', bad, token)['valid'])
        bad = copy.deepcopy(manifests['v1']['manifest'])
        bad['name'] = 'identity reuse with changed content'
        request(cloud_url, '/api/sf/v1/releases', 'POST', {'request_id': 'identity-conflict', 'manifest': bad}, token, expected=409)
        deployment = lambda identity: request(cloud_url, '/api/sf/v1/release-deployments/' + identity, token=token, record=False)
        create_deployment = lambda identity, version, batches: request(cloud_url, '/api/sf/v1/release-deployments', 'POST', {'id': identity, 'request_id': 'create-' + identity, 'release_id': manifests[version]['id'], 'group_id': 'factory', 'batches': batches, 'reason': '实际程序批次验证'}, token)
        act = lambda d, action, name: request(cloud_url, '/api/sf/v1/release-deployments/' + d['id'] + '/actions', 'POST', {'request_id': name, 'expected_version': d['version'], 'action': action, 'reason': '批次运行验证 ' + action}, token)
        create_deployment('deploy-cutover-v1', 'v1', [['release-a']])
        command('prepare-original-a-v1', agent_argv['a'] + ['--prepare-only'], env=base_env)
        check('prepare-only-retains-original-process', processes['original-a'][0].poll() is None and not (node_directories['a'] / 'active.json').exists() and json.loads((node_directories['a'] / 'pending.json').read_text())['runtime']['pid'] == 0)
        act(deployment('deploy-cutover-v1'), 'cancel', 'cancel-cutover-v1-before-target-change')
        create_deployment('deploy-cutover-v2', 'v2', [['release-a']])
        command('prepare-original-a-v2', agent_argv['a'] + ['--prepare-only'], env=base_env)
        prepared_prior = json.loads((node_directories['a'] / 'pending.json').read_text())
        check('prepare-only-fixed-secret-cache', all(secret.encode() not in (node_directories['a'] / 'releases' / (manifests['v2']['sha256'] + '.json')).read_bytes() for secret in connector_secrets))
        act(deployment('deploy-cutover-v2'), 'cancel', 'cancel-cutover-v2-before-final-target')
        create_deployment('deploy-v1', 'v1', [['release-a'], ['release-b']])
        stop('original-a')
        preparing_identity = next(item for item in request(cloud_url, '/api/sf/v1/workload-identities', token=token) if item['id'] == 'release-a')
        current_assignment = deployment('deploy-v1')
        current_refs = [component['configuration'] for component in manifests['v1']['manifest']['components'] if component.get('configuration') and (not component.get('target_node_ids') or 'edge-a' in component['target_node_ids'])]
        request(config_url, '/internal/config/v2/release-target', 'POST', {'deployment_id': 'deploy-v1', 'generation': current_assignment['targets'][0]['generation'], 'references': current_refs}, credentials['a'], preparing_identity['instance_id'])
        request(config_url, '/internal/config/v2/credentials/resolve', 'POST', {'reference': connector_refs[1]['a'], 'credential_ref': 'edge-a/mqtt-a:1', 'purpose': 'release-runtime'}, credentials['a'], preparing_identity['instance_id'], expected=503)
        start('agent-a', agent_argv['a'], base_env)
        first = wait_for('first batch with offline second node', lambda: deployment('deploy-v1'), lambda d: d['current_batch'] == 1 and d['targets'][0]['state'] == 'running')
        check('offline-waits-without-advancing', first['state'] == 'active' and first['targets'][1]['state'] in ['waiting', 'offline'], first)
        cutover_a = json.loads((node_directories['a'] / 'active.json').read_text())
        check('first-cutover-rechecks-changed-assignment', prepared_prior['release_sha256'] == manifests['v2']['sha256'] and cutover_a['release_sha256'] == manifests['v1']['sha256'] and cutover_a['generation'] > prepared_prior['generation'] and cutover_a['agent_instance_epoch'] > prepared_prior['agent_instance_epoch'], {'prepared': prepared_prior, 'active': cutover_a})
        reviewed = copy.deepcopy(first)
        time.sleep(2.2)
        latest = deployment('deploy-v1')
        check('heartbeats-preserve-business-version', latest['version'] == reviewed['version'] and latest['targets'][0]['last_sequence'] > reviewed['targets'][0]['last_sequence'], {'reviewed_version': reviewed['version'], 'current_version': latest['version']})
        paused = act(reviewed, 'pause', 'pause-after-heartbeat')
        request(cloud_url, '/api/sf/v1/release-deployments/deploy-v1/actions', 'POST', {'request_id': 'stale-resume', 'expected_version': reviewed['version'], 'action': 'resume', 'reason': '旧业务版本'}, token, expected=409)
        start('agent-b', agent_argv['b'], base_env)
        time.sleep(1)
        check('paused-target-not-applied', not (node_directories['b'] / 'active.json').exists())
        stop('agent-b')
        act(paused, 'resume', 'resume-reviewed-pause')
        command('prepare-original-b-v1', agent_argv['b'] + ['--prepare-only'], env=base_env)
        stop('original-b')
        start('agent-b', agent_argv['b'], base_env)
        first = wait_for('first deployment completed', lambda: deployment('deploy-v1'), lambda d: d['state'] == 'completed')
        check('two-real-processes', first['targets'][0]['runtime']['pid'] != first['targets'][1]['runtime']['pid'], first)
        for node in ['a', 'b']:
            run = persisted_runtime(node)
            check('v1-runtime-' + node, run['runtime']['program_sha256'] == builds['v1']['sha256'] and next(c for c in run['configurations'] if c['reference']['id'] == 'heartbeat.interval_ms')['effective_value'] == 5000, run)
            encrypted = (node_directories[node] / 'releases' / (manifests['v1']['sha256'] + '.json')).read_bytes()
            check('cache-encryption-' + node, secret_value.encode() not in encrypted and b'ciphertext' in encrypted)
            check('explicit-existing-database-' + node, (runtime_directories[node] / 'original.db').exists() and not (node_directories[node] / 'node.db').exists())
        # Rule output is read from the ordinary persisted observation consumer.
        def ingest_and_output(node, suffix, values, expected):
            local_token = (node_directories[node] / 'runtime-token').read_text().strip()
            at = int(time.time() * 1000)
            points = [{'id': suffix + '-' + str(i), 'message_id': suffix, 'source_id': 'edge-' + node, 'device_id': 'climate-1', 'key': 'temperature', 'value': value, 'observed_ms': at + i, 'quality': 'GOOD', 'time_source': 'device', 'entity_revision': 1, 'asset_version': 1} for i, value in enumerate(values)]
            request(node_urls[node], '/api/sf/v1/ingest', 'POST', {'message_id': suffix, 'source_id': 'edge-' + node, 'points': points}, local_token)
            def find_output():
                rows = sqlite_rows(runtime_directories[node] / 'original.db', 'SELECT data FROM observations WHERE definition_id=? AND observed_ms>=? ORDER BY observed_ms DESC', ('release-temperature', at))
                return [json.loads(row[0]) for row in rows]
            outputs = wait_for('rule effect ' + suffix, find_output, lambda rows: any(row['value'] == expected for row in rows))
            check('rule-effect-' + suffix, any(row['value'] == expected for row in outputs), outputs)
            return points
        before_points = ingest_and_output('a', 'v1-average', [10, 20], 15)
        def connector_state(node, interval):
            base = f'http://127.0.0.1:{dt_a_http if node == "a" else dt_b_http}'
            actual = wait_for('connector consumer interval ' + node + ':' + str(interval), lambda: request(base, '/runtime', record=False), lambda actual: len(actual['connectors']) == 1 and actual['connectors'][0]['polling_interval_millis'] == interval and actual['connectors'][0]['credential_consumed'])
            return actual['connectors'][0]

        connector_v1_state = {node: connector_state(node, 1000) for node in ['a', 'b']}
        check('combined-release-pins-actual-connectors-v1', all(state['state']['found'] for state in connector_v1_state.values()), connector_v1_state)
        # A corrupted retained key causes the real replacement executable to exit.
        old_b_runtime = persisted_runtime('b')['runtime']
        key_path = runtime_directories['b'] / 'original.key'
        original_key = key_path.read_bytes()
        key_path.write_text('injected-invalid-master-key')
        create_deployment('deploy-v2', 'v2', [['release-b'], ['release-a']])
        failed = wait_for('real executable start failure', lambda: deployment('deploy-v2'), lambda d: d['state'] == 'failed')
        check('failure-stops-following-batch', failed['current_batch'] == 0 and failed['targets'][0]['failure_component'] == 'program' and failed['targets'][1]['state'] == 'pending', failed)
        transition = json.loads((node_directories['b'] / 'last-transition.json').read_text())
        check('old-process-exited-before-new-start', transition['old_pid'] == old_b_runtime['pid'] and transition['old_exited_ms'] > 0 and transition['new_ready_ms'] == 0, transition)
        key_path.write_bytes(original_key)
        act(failed, 'retry', 'retry-valid-key')
        second = wait_for('replacement retry completed', lambda: deployment('deploy-v2'), lambda d: d['state'] == 'completed')
        check('retry-increments-assignment', second['targets'][0]['generation'] > failed['targets'][0]['generation'], second)
        for node in ['a', 'b']:
            transition = json.loads((node_directories[node] / 'last-transition.json').read_text())
            check('exit-before-readiness-' + node, 0 < transition['old_exited_ms'] <= transition['new_ready_ms'], transition)
        ingest_and_output('a', 'v2-maximum', [30, 40], 40)
        connector_v2_state = {node: connector_state(node, 2000) for node in ['a', 'b']}
        check('combined-upgrade-changes-actual-connectors', all(connector_v2_state[node]['state']['applied_entity_revision'] > connector_v1_state[node]['state']['applied_entity_revision'] for node in ['a', 'b']), connector_v2_state)
        check('retained-observations-after-upgrade', len(sqlite_rows(runtime_directories['a'] / 'original.db', 'SELECT id FROM observations WHERE message_id=? AND definition_id=?', ('v1-average',''))) == 2)
        # Compatibility rejection is driven by the retained current migration.
        refusal = request(cloud_url, '/api/sf/v1/release-deployments/deploy-v2/rollback', 'POST', {'request_id': 'old-database-refusal', 'expected_version': second['version'], 'id': 'deploy-legacy', 'release_id': manifests['legacy']['id'], 'reason': '检查旧程序数据库兼容要求'}, token, expected=400)
        check('incompatible-rollback-provides-recovery', 'restore' in json.dumps(refusal) and 'backup' in json.dumps(refusal), refusal)
        rollback = request(cloud_url, '/api/sf/v1/release-deployments/deploy-v2/rollback', 'POST', {'request_id': 'rollback-v1', 'expected_version': second['version'], 'id': 'deploy-rollback', 'release_id': manifests['v1']['id'], 'reason': '退回固定旧内容并保留数据库'}, token)
        rollback = wait_for('supported rollback', lambda: deployment('deploy-rollback'), lambda d: d['state'] == 'completed')
        check('supported-old-release-running', all(t['runtime']['program_sha256'] == builds['v1']['sha256'] and t['runtime']['migration_version'] == 11 for t in rollback['targets']), rollback)
        connector_rollback_state = {node: connector_state(node, 1000) for node in ['a', 'b']}
        check('combined-rollback-restores-actual-connector-content', all(connector_rollback_state[node]['state']['applied_entity_revision'] > connector_v2_state[node]['state']['applied_entity_revision'] for node in ['a', 'b']), connector_rollback_state)
        for node in ['a', 'b']:
            history = [d for d in documents(runtime_directories[node] / 'original.db', 'definition') if d['id'] == 'release-temperature'][0]
            versions = sqlite_rows(runtime_directories[node] / 'original.db', 'SELECT version FROM document_versions WHERE kind=? AND id=?', ('definition', 'release-temperature'))
            check('rollback-retains-rule-history-' + node, history['version'] >= 3 and len(versions) >= 3, {'active_version': history['version'], 'versions': versions})
        # Replacing a supervisor adopts the orphan and refreshes configuration epoch.
        prior = json.loads((node_directories['a'] / 'active.json').read_text())
        prior_identity = next(i for i in request(cloud_url, '/api/sf/v1/workload-identities', token=token) if i['id'] == 'release-a')
        stop('agent-a', kill=True)
        check('orphan-continues-running', persisted_runtime('a')['runtime']['pid'] == prior['runtime']['pid'])
        start('agent-a', agent_argv['a'], base_env)
        adopted = wait_for('new supervisor reports same process', lambda: deployment('deploy-rollback'), lambda d: next(t for t in d['targets'] if t['identity_id'] == 'release-a')['agent_instance_epoch'] > prior_identity['instance_epoch'] and next(t for t in d['targets'] if t['identity_id'] == 'release-a')['state'] == 'running')
        adopted_a = next(t for t in adopted['targets'] if t['identity_id'] == 'release-a')
        check('agent-restart-adopts-without-process-replacement', adopted_a['runtime']['pid'] == prior['runtime']['pid'] and adopted_a['runtime']['process_instance_id'] == prior['runtime']['process_instance_id'], adopted_a)
        request(authority_url, '/internal/releases/desired', token=credentials['a'], instance=prior_identity['instance_id'], expected=401)
        target = documents(private / 'configuration.db', 'configuration_target')
        check('configuration-target-new-epoch', all(t['instance_epoch'] == adopted_a['agent_instance_epoch'] for t in target if t.get('identity_id') == 'release-a'), [t for t in target if t.get('identity_id') == 'release-a'])
        # Both services can restart; cached child startup precedes online activation.
        stop('cloud')
        stop('config')
        stop('agent-a')
        start('agent-a', agent_argv['a'], base_env)
        offline = wait_for('offline encrypted cache startup', lambda: persisted_runtime('a'))
        check('offline-cache-is-actual-process', offline['runtime']['program_sha256'] == builds['v1']['sha256'] and offline['runtime']['pid'] != prior['runtime']['pid'], offline)
        start('config', [str(binaries / 'sf-config')], config_env)
        wait_for('restarted config', lambda: request(config_url, '/health', record=False))
        start('cloud', [str(binaries / 'sf-cloud'), '-seed'], cloud_env)
        wait_for('restarted cloud', lambda: request(cloud_url, '/health', record=False))
        restarted = wait_for('post-restart persisted deployment', lambda: deployment('deploy-rollback'), lambda d: next(t for t in d['targets'] if t['identity_id'] == 'release-a')['runtime']['pid'] == offline['runtime']['pid'])
        check('coordinator-restart-preserves-generation', next(t for t in restarted['targets'] if t['identity_id'] == 'release-a')['generation'] == adopted_a['generation'], restarted)
        # Kill the supervisor after it persists the replacement child PID, while
        # the real new executable is stopped before readiness is recorded.
        before_switch = persisted_runtime('a')['runtime']
        history_before = len(sqlite_rows(runtime_directories['a'] / 'original.db', 'SELECT version FROM document_versions WHERE kind=? AND id=?', ('definition', 'release-temperature')))
        create_deployment('deploy-interrupted-switch', 'v2', [['release-a'], ['release-b']])
        pending_path = node_directories['a'] / 'pending.json'
        pending, until = None, time.monotonic() + 30
        while time.monotonic() < until:
            try:
                candidate = json.loads(pending_path.read_text())
                if candidate['deployment_id'] == 'deploy-interrupted-switch' and candidate.get('runtime', {}).get('pid', 0) > 0:
                    os.kill(candidate['runtime']['pid'], signal.SIGSTOP)
                    pending = candidate
                    break
            except (OSError, ValueError, KeyError):
                pass
            time.sleep(0.001)
        check('switch-interruption-has-persisted-new-child', pending is not None, pending)
        stop('agent-a', kill=True)
        interrupted_state = json.loads((node_directories['a'] / 'active.json').read_text())
        check('switch-interruption-precedes-active-commit', interrupted_state['release_sha256'] == manifests['v1']['sha256'] and pending['runtime']['pid'] != before_switch['pid'], {'active': interrupted_state, 'pending': pending})
        os.kill(pending['runtime']['pid'], signal.SIGCONT)
        ready_before_supervisor = wait_for('replacement becomes ready before active state is committed', lambda: persisted_runtime('a'), lambda run: run['runtime']['pid'] == pending['runtime']['pid'])
        check('interrupted-ready-process-precedes-active-commit', pending_path.exists() and json.loads((node_directories['a'] / 'active.json').read_text())['release_sha256'] == manifests['v1']['sha256'], ready_before_supervisor)
        start('agent-a', agent_argv['a'], base_env)
        switched = wait_for('interrupted switch resumes real child', lambda: deployment('deploy-interrupted-switch'), lambda d: d['state'] == 'completed')
        switched_a = next(t for t in switched['targets'] if t['identity_id'] == 'release-a')
        check('interrupted-switch-adopts-same-new-process', switched_a['runtime']['pid'] == pending['runtime']['pid'] and switched_a['runtime']['program_sha256'] == builds['v2']['sha256'], switched_a)
        history_after = len(sqlite_rows(runtime_directories['a'] / 'original.db', 'SELECT version FROM document_versions WHERE kind=? AND id=?', ('definition', 'release-temperature')))
        check('interrupted-switch-activates-rules-once', history_after == history_before + 1, {'before': history_before, 'after': history_after})
        check('interrupted-switch-pending-removed', not pending_path.exists())
        # Desired pulls keep the agent online, while actual runtime reports age.
        failed_key_path = runtime_directories['a'] / 'original.key'
        retained_key = failed_key_path.read_bytes()
        failed_key_path.write_text('injected-invalid-key-after-completion')
        try:
            os.kill(switched_a['runtime']['pid'], signal.SIGTERM)
            aged = wait_for('running receipt expires while supervisor stays online', lambda: deployment('deploy-interrupted-switch'), lambda d: next(t for t in d['targets'] if t['identity_id'] == 'release-a')['report_age_ms'] > 15000 and not next(t for t in d['targets'] if t['identity_id'] == 'release-a')['report_fresh'])
            aged_a = next(t for t in aged['targets'] if t['identity_id'] == 'release-a')
            check('completed-history-retains-current-runtime-failure', aged['state'] == 'completed' and aged_a['state'] == 'failed' and aged_a['last_seen_ms'] > aged_a['last_report_ms'] + 10000, aged)
        finally:
            failed_key_path.write_bytes(retained_key)
        recovered = wait_for('child automatically restarts after retained key recovery', lambda: deployment('deploy-interrupted-switch'), lambda d: next(t for t in d['targets'] if t['identity_id'] == 'release-a')['state'] == 'running' and next(t for t in d['targets'] if t['identity_id'] == 'release-a')['report_fresh'])
        recovered_a = next(t for t in recovered['targets'] if t['identity_id'] == 'release-a')
        check('runtime-recovery-preserves-assignment-and-clears-failure', recovered_a['generation'] == switched_a['generation'] and recovered_a['runtime']['pid'] != switched_a['runtime']['pid'] and not recovered_a.get('failure_component') and not recovered_a.get('reason'), recovered_a)
        if not args.skip_artifact_fault:
            stop('agent-b')
            create_deployment('deploy-v3-integrity', 'v3', [['release-b'], ['release-a']])
            artifact_path = private / 'artifacts' / builds['v3']['sha256']
            original_artifact = artifact_path.read_bytes()
            artifact_path.chmod(0o600)
            artifact_path.write_bytes(b'corrupt executable after release validation')
            start('agent-b', agent_argv['b'], base_env)
            integrity = wait_for('artifact preparation failure report', lambda: deployment('deploy-v3-integrity'), lambda d: d['state'] == 'failed')
            check('preparation-failure-stops-batch', integrity['targets'][0]['failure_component'] == 'program' and integrity['targets'][1]['state'] == 'pending', integrity)
            check('old-process-survives-preparation-failure', persisted_runtime('b')['runtime']['program_sha256'] == builds['v2']['sha256'])
            artifact_path.write_bytes(original_artifact)
            artifact_path.chmod(0o500)
            act(integrity, 'retry', 'retry-restored-artifact')
            wait_for('restored artifact completes deployment', lambda: deployment('deploy-v3-integrity'), lambda d: d['state'] == 'completed')
        device_id = 'template-goods-a'
        configured_device = request(cloud_url, '/api/sf/v1/device-configurations/validate', 'POST', {'protocol': 'mqtt_device', 'device_id': device_id, 'parameters': {'kind': 'device', 'connector_id': 'mqtt-a', 'device_id': device_id, 'device_name': '模板发布计数设备', 'datapoints': [{'key': 'pulse', 'source': 'pulse'}]}}, token)
        check('template-device-configuration-valid', configured_device['valid'], configured_device)
        edge_user_token = request(node_urls['a'], '/api/sf/v1/login', 'POST', {'login': 'admin', 'password': password})['token']
        device = request(node_urls['a'], '/api/sf/v1/entities', 'POST', {'entity': {'id': device_id, 'kind': 'device', 'name': '模板发布计数设备', 'parent_id': 'factory', 'edge_id': 'edge-a', 'status': 'approved', 'protocol': 'mqtt_device', 'version': 1, 'config': configured_device['config']}, 'expected_version': 0}, edge_user_token)
        wait_for('edge-approved device synchronizes to cloud', lambda: request(cloud_url, '/api/sf/v1/entities', token=token, record=False), lambda rows: any(row['id'] == device_id and row['status'] == 'approved' and row['version'] == device['version'] for row in rows))
        instance = {'id': 'release-goods-scene', 'name': '模板发布计数场景', 'template_id': 'goods-counting', 'template_version': 1, 'device_id': device_id, 'device_version': device['version'], 'configuration_id': 'edge-a/mqtt-a', 'configuration_version': 2, 'parameters': {'mode': 'delta'}}
        batch = request(cloud_url, '/api/sf/v1/template-batches', 'POST', {'id': 'release-goods-batch', 'request_id': 'release-goods-batch', 'group_id': 'factory', 'instances': [instance]}, token)
        evolved = request(cloud_url, '/api/sf/v1/template-batches/release-goods-batch/evolve', 'POST', {'id': 'release-goods-evolution', 'request_id': 'release-goods-evolution', 'expected_version': batch['version'], 'release_id': 'release-goods-v2', 'name': '货物计数模板第二版发布', 'program_sha256': builds['v3']['sha256'], 'targets': [{'instance_id': instance['id'], 'template_version': 2, 'parameters': {'count_window_ms': 15000}}], 'configurations': [cfg_refs['v2']]}, token)
        check('template-evolution-preserves-existing-bindings', evolved['instances'][0]['id'] == instance['id'] and evolved['instances'][0]['device_id'] == device_id and evolved['bindings'] == batch['bindings'] and evolved['instances'][0]['template_version'] == 2, evolved)
        request(cloud_url, '/api/sf/v1/release-deployments', 'POST', {'id': 'deploy-template-evolution', 'request_id': 'deploy-template-evolution', 'release_id': evolved['release']['id'], 'group_id': 'factory', 'batches': [['release-a']], 'reason': '保留设备与绑定并通过统一发布运行模板新版本'}, token)
        template_deployed = wait_for('template evolves through actual release process', lambda: deployment('deploy-template-evolution'), lambda d: d['state'] == 'completed')
        template_runtime = persisted_runtime('a')
        check('template-release-actual-program-and-component-content', template_runtime['runtime']['release_sha256'] == evolved['release']['sha256'] and template_runtime['runtime']['program_sha256'] == builds['v3']['sha256'] and {entry['reference']['id'] for entry in template_runtime['configurations']} == {'heartbeat.interval_ms', 'edge-a/mqtt-a'}, template_runtime)
        rule_id = evolved['definition_ids'][0]
        active_definition = next(row for row in documents(runtime_directories['a'] / 'original.db', 'definition') if row['id'] == rule_id)
        old_definition = next(row for row in documents(runtime_directories['a'] / 'original.db', 'definition') if row['id'] == 'release-temperature')
        check('template-runtime-new-window-and-retired-prior-rule', active_definition['selector']['window_ms'] == 15000 and old_definition['status'] == 'disabled', {'active': active_definition, 'prior': old_definition})
        wait_for('template bound device reaches owning node', lambda: documents(runtime_directories['a'] / 'original.db', 'entity'), lambda rows: any(row['id'] == device_id for row in rows))
        count_at = int(time.time() * 1000)
        request(node_urls['a'], '/api/sf/v1/ingest', 'POST', {'message_id': 'template-release-count', 'source_id': 'edge-a', 'points': [{'id': 'template-release-point', 'message_id': 'template-release-count', 'source_id': 'edge-a', 'device_id': device_id, 'key': 'pulse', 'value': 5, 'observed_ms': count_at, 'quality': 'GOOD', 'time_source': 'device', 'entity_revision': device['version'], 'asset_version': device['version']}]}, (node_directories['a'] / 'runtime-token').read_text().strip())
        counted = wait_for('template rule produces actual stored output', lambda: [json.loads(row[0]) for row in sqlite_rows(runtime_directories['a'] / 'original.db', 'SELECT data FROM observations WHERE definition_id=? AND observed_ms>=?', (rule_id, count_at))], lambda rows: any(row['value'] == 5 for row in rows))
        check('template-rule-actual-integer-count', any(row['value'] == 5 for row in counted), counted)
        original_batch = request(cloud_url, '/api/sf/v1/template-batches/release-goods-batch', token=token)
        check('template-source-batch-preserved', original_batch['version'] == batch['version'] and original_batch['input']['instances'][0]['template_version'] == 1, original_batch)
        readonly_token = request(cloud_url, '/api/sf/v1/login', 'POST', {'login': 'release-viewer', 'password': readonly_password})['token']
        readonly_detail = request(cloud_url, '/api/sf/v1/release-deployments/deploy-template-evolution', token=readonly_token)
        check('readonly-release-actions-unavailable', all(not action['allowed'] for action in readonly_detail['allowed_actions']), readonly_detail['allowed_actions'])
        request(cloud_url, '/api/sf/v1/release-deployments/deploy-template-evolution/actions', 'POST', {'request_id': 'readonly-pause', 'expected_version': readonly_detail['version'], 'action': 'pause', 'reason': '只读身份权限校验'}, readonly_token, expected=403)
        report = request(cloud_url, '/api/sf/v1/release-deployments/deploy-rollback/reports', token=token)
        check('authenticated-runtime-reports', any(r['state'] == 'running' and r.get('runtime', {}).get('pid', 0) > 0 for r in report))
        kinds = sqlite_rows(private / 'configuration.db', 'SELECT kind,count(*) FROM documents GROUP BY kind')
        check('independent-center-has-no-release-or-rules', not any(kind in ['release', 'release_deployment', 'definition', 'draft'] for kind, _ in kinds), kinds)
        check('independent-databases-and-master-keys', (private / 'cloud.key').read_bytes() != (private / 'configuration.key').read_bytes())
        audit = [json.loads(row[0]) for row in sqlite_rows(private / 'cloud.db', 'SELECT data FROM audit ORDER BY sequence')]
        save(evidence / 'cloud-audit.json', audit)
        save(evidence / 'configuration-reports.json', documents(private / 'configuration.db', 'configuration_report'))
        save(evidence / 'final-deployments.json', request(cloud_url, '/api/sf/v1/release-deployments', token=token))
        for path in [*evidence.rglob('*'), *[p for node in ['a', 'b'] for p in node_directories[node].rglob('*.json')], private / 'configuration.db', private / 'cloud.db']:
            if path.is_file():
                check('private-value-absent-' + str(path.relative_to(directory)), all(value.encode() not in path.read_bytes() for value in [secret_value, *connector_secrets]))
        result.update(status='passed', cloud_url=cloud_url, config_url=config_url, authority_url=authority_url, node_urls=node_urls, tested_at=time.time(), kept_running=args.keep_running, launch=str(private / 'launch.json'))
        if args.keep_running:
            save(directory / 'frontend-fixture.json', {'cloud_url': cloud_url, 'node_urls': node_urls, 'credentials_file': str(password_file), 'login': 'admin', 'readonly_login': 'release-viewer', 'readonly_credentials_file': str(readonly_file), 'launch': str(private / 'launch.json'), 'binaries': str(evidence / 'binaries.json'), 'source_sha256': source_sha})
    except BaseException as error:
        result.update(status='failed', error=type(error).__name__ + ': ' + str(error), failed_at=time.time())
        raise
    finally:
        if result['status'] != 'passed' or not args.keep_running:
            for name in list(processes):
                try:
                    stop(name)
                except BaseException as error:
                    result.setdefault('cleanup_errors', []).append(type(error).__name__ + ': ' + str(error))
            if 'node_urls' in locals():
                closed = {}
                for node, url in node_urls.items():
                    with socket.socket() as stream:
                        closed[node] = stream.connect_ex(('127.0.0.1', urllib.parse.urlparse(url).port)) != 0
                result['node_ports_closed'] = closed
        save(directory / 'result.json', result)
        print(json.dumps({'status': result['status'], 'result': str(directory / 'result.json'), 'error': result.get('error')}, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
