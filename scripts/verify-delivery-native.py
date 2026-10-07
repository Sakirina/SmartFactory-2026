#!/usr/bin/env python3
"""Verify PID1 Edge shutdown, retained data and the formal Kafka resource budget."""
import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import threading
import time
import traceback
from urllib.parse import urlparse, unquote
from urllib.request import Request, build_opener, ProxyHandler

ROOT = Path(__file__).resolve().parents[1]


def utc():
    return datetime.now(timezone.utc).isoformat()


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--source', type=Path, required=True, help='retained phase-one native state')
    parser.add_argument('--initialize-native', action='store_true', help='initialize new native databases when the retained validation server is unavailable')
    parser.add_argument('--reuse-native-databases', action='store_true', help='continue this verifier\'s retained isolated native databases and data')
    args = parser.parse_args()
    os.umask(0o077)
    work, source = args.directory.resolve(), args.source.resolve()
    work.mkdir(parents=True, exist_ok=False)
    binaries = work / 'bin'
    binaries.mkdir()
    pg = json.loads((ROOT / '.local/evolution/pg18-test.json').read_text())
    url = urlparse(pg['dsn'])
    user, password = unquote(url.username), unquote(url.password)
    pg_container = pg['container']
    database_names = {'cloud': 'sf_delivery_tbce_20261005', 'edge': 'sf_delivery_tbedge_20261005'}
    credentials = json.loads((source / 'development-credentials.json').read_text())
    native = json.loads((source / 'tb-bootstrap.json').read_text())
    env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOMAXPROCS='2', CGO_ENABLED='0',
               GOPATH='/private/tmp/smartfactory-evolution-gopath',
               GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
               GOCACHE='/private/tmp/smartfactory-evolution-go-cache')
    report = {'schema_version': 1, 'started_at': utc(), 'scope': 'formal native Edge exec/PID1 stop and restart with retained original/derived telemetry; Kafka 512MiB heap in 1GiB container',
              'commands': [], 'checks': {}, 'processes': [], 'samples': [],
              'budget': {'ce_container_bytes': 3 * 1024**3, 'edge_container_bytes': 2 * 1024**3,
                         'kafka_container_bytes': 1024**3, 'existing_postgres_container_bytes': 1024**3,
                         'combined_container_limit_bytes': 7 * 1024**3, 'ce_and_edge_connection_pools': {'maximum': 4, 'minimum_idle': 1}}}
    processes = []
    started_containers = False
    stop_samples = threading.Event()
    opener = build_opener(ProxyHandler({}))

    def save():
        (work / 'verification.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')

    driver_inputs = work / 'driver-inputs'
    driver_inputs.mkdir()
    report['driver_inputs'] = []
    for relative in ['scripts/verify-delivery-native.py', 'scripts/build-release.py',
                     'scripts/prepare-runtime.py', 'scripts/bootstrap-runtime.py',
                     'scripts/bootstrap-thingsboard.py', 'scripts/check-native.py',
                     'deploy/images.lock.json']:
        original = ROOT / relative
        retained = driver_inputs / relative
        retained.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(original, retained)
        report['driver_inputs'].append({'source': relative, 'retained': str(retained), 'sha256': sha(retained)})
    report['invocation'] = {'argv': [str(ROOT / 'scripts/verify-delivery-native.py'), '--directory', str(work), '--source', str(source)]
                            + (['--initialize-native'] if args.initialize_native else [])
                            + (['--reuse-native-databases'] if args.reuse_native_databases else []),
                            'cwd': str(ROOT), 'toolchain_environment': {key: env[key] for key in ['GOTOOLCHAIN', 'GOPROXY', 'GOMAXPROCS', 'CGO_ENABLED', 'GOPATH', 'GOMODCACHE', 'GOCACHE']}}
    save()

    def run(name, argv, *, input=None, cwd=ROOT, environment=None, timeout=180, required=True):
        started = time.monotonic()
        result = subprocess.run(argv, cwd=cwd, env=environment or env, input=input, capture_output=True, text=True, timeout=timeout)
        log = work / (name + '.log')
        logged_stdout = result.stdout
        if argv[:2] == ['docker', 'inspect'] and result.returncode == 0:
            inspected = json.loads(logged_stdout)
            for value in inspected:
                configuration = value.get('Config', {})
                configuration['Env'] = [item.split('=', 1)[0] + '=<private>' for item in configuration.get('Env', [])]
            logged_stdout = json.dumps(inspected, indent=2)
        log.write_text(logged_stdout + '\n--- stderr ---\n' + result.stderr)
        report['commands'].append({'name': name, 'argv': argv, 'exit_code': result.returncode, 'duration_seconds': time.monotonic() - started,
                                   'log': str(log), 'sha256': sha(log)})
        save()
        if required and result.returncode:
            raise RuntimeError(name + ' failed; inspect ' + str(log))
        return result.stdout

    def sql(name, query):
        return run(name, ['docker', 'exec', '-i', pg_container, 'psql', '-U', user, '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-At'], input=query)

    def request(base, path, value=None, token='', tb=False, timeout=20):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['X-Authorization' if tb else 'Authorization'] = 'Bearer ' + token
        attempt = {'at': utc(), 'base': base, 'path': path, 'method': 'GET' if value is None else 'POST', 'timeout_seconds': timeout}
        started = time.monotonic()
        try:
            with opener.open(Request(base + path, data=None if value is None else json.dumps(value).encode(), headers=headers), timeout=timeout) as response:
                data = response.read()
                attempt.update(status=response.status, response_bytes=len(data))
                return json.loads(data) if data else None
        except Exception as error:
            attempt.update(error_type=type(error).__name__, status=getattr(error, 'code', None))
            raise
        finally:
            attempt['duration_seconds'] = time.monotonic() - started
            report.setdefault('http_attempts', []).append(attempt)
            save()

    def wait(check, label, timeout=300, container=None):
        until = time.monotonic() + timeout
        errors = []
        attempts = 0
        while time.monotonic() < until:
            attempts += 1
            try:
                value = check()
                if value:
                    report.setdefault('readiness', []).append({'label': label, 'ready_at': utc(), 'attempts': attempts,
                                                               'preceding_attempts': attempts - 1, 'preceding_errors': len(errors), 'last_error_types': errors[-10:]})
                    save()
                    return value
            except Exception as error:
                errors.append(type(error).__name__)
            if container:
                state = json.loads(subprocess.check_output(['docker', 'inspect', '--format', '{{json .State}}', container], text=True))
                if not state['Running']:
                    report['readiness_failure'] = {'label': label, 'container': container, 'state': state}
                    run('readiness-failed-' + label, ['docker', 'logs', container], required=False)
                    raise RuntimeError(label + ' exited before readiness')
            time.sleep(1)
        raise TimeoutError(label + '; attempts=' + str(len(errors)))

    def start_app(mode):
        port, tb_port = (19990, 19080) if mode == 'cloud' else (19991, 19081)
        runtime = dict(env, SF_BOOTSTRAP_PASSWORD=credentials['password'], SF_SERVICE_TOKEN=credentials['service_token'],
                       SF_MASTER_KEY_FILE=str(work / (mode + '.master')), SF_DATABASE=str(work / (mode + '.db')),
                       SF_STATIC_DIR=str(ROOT / 'Frontends/dist'), SF_LISTEN='0.0.0.0:' + str(port),
                       SF_NODE_ID='cloud-native-test' if mode == 'cloud' else 'edge-a', SF_TB_URL='http://127.0.0.1:' + str(tb_port),
                       SF_TB_USERNAME=native['username'], SF_TB_PASSWORD=native['password'], SF_TB_CALLBACK_URL='http://host.docker.internal:' + str(port))
        log = work / ('business-' + mode + '.log')
        output = log.open('a')
        process = subprocess.Popen([str(binaries / ('sf-' + mode)), '--seed'], cwd=ROOT, env=runtime, stdout=output, stderr=subprocess.STDOUT)
        entry = {'name': 'sf-' + mode, 'pid': process.pid, 'binary': str(binaries / ('sf-' + mode)), 'log': str(log), 'started_at': utc()}
        report['processes'].append(entry)
        processes.append((process, output, entry))
        return wait(lambda: request('http://127.0.0.1:' + str(port), '/api/sf/v1/login', {'login': 'admin', 'password': credentials['password']}).get('token'), 'business-' + mode)

    try:
        spec = importlib.util.spec_from_file_location('builder', ROOT / 'scripts/build-release.py')
        builder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(builder)
        inputs, observed = builder.build_inputs(ROOT, {'Platform': ['sf-cloud', 'sf-edge', 'sf-pki']}, dict(env, GOOS='darwin', GOARCH='arm64'))
        (work / 'compile-inputs-before.json').write_text(json.dumps(inputs, indent=2) + '\n')
        source_digest = hashlib.sha256(json.dumps(inputs, sort_keys=True).encode()).hexdigest()
        flags = '-s -w -X competition2026/product/platform/pkg/buildinfo.Version=delivery-native -X competition2026/product/platform/pkg/buildinfo.SourceSHA256=' + source_digest
        run('build-apps', ['go', 'build', '-p', '1', '-trimpath', '-ldflags', flags, '-o', str(binaries) + '/', './cmd/sf-cloud', './cmd/sf-edge', './cmd/sf-pki'], cwd=ROOT / 'Platform')
        report['compile_inputs_before'] = {'path': str(work / 'compile-inputs-before.json'), 'sha256': sha(work / 'compile-inputs-before.json'), 'count': len(inputs)}
        report['binaries'] = {p.name: {'path': str(p), 'sha256': sha(p), 'bytes': p.stat().st_size} for p in binaries.iterdir()}
        bundle = work / 'preparation-bundle'
        (bundle / 'bin').mkdir(parents=True)
        (bundle / 'deploy').mkdir()
        shutil.copy2(binaries / 'sf-edge', bundle / 'bin/sf-edge')
        shutil.copy2(ROOT / 'deploy/images.lock.json', bundle / 'deploy/images.lock.json')
        prepared = work / 'prepared'
        run('prepare-formal-native-entrypoint', ['python3', str(ROOT / 'scripts/prepare-runtime.py'), '--bundle', str(bundle), '--directory', str(prepared), '--profile', 'capacity', '--hosting', 'hosted', '--pki-binary', str(binaries / 'sf-pki')])
        config = json.loads((prepared / 'compose.json').read_text())
        edge = config['services']['edge-a-native']
        ce = config['services']['thingsboard']
        kafka = config['services']['kafka']
        initializer = config['services']['kafka-storage-init']
        tuning = {'SPRING_DATASOURCE_USERNAME': user, 'SPRING_DATASOURCE_PASSWORD': password,
                  'SPRING_DATASOURCE_MAXIMUM_POOL_SIZE': '4', 'SPRING_DATASOURCE_MINIMUM_IDLE': '1',
                  'SPRING_DATASOURCE_HIKARI_MAXIMUM_POOL_SIZE': '4', 'SPRING_DATASOURCE_HIKARI_MINIMUM_IDLE': '1',
                  'MALLOC_ARENA_MAX': '2', 'LOCAL_JS_THREAD_POOL_SIZE': '2', 'LOCAL_JS_SANDBOX_MONITOR_THREAD_POOL_SIZE': '1',
                  'TBEL_THREAD_POOL_SIZE': '2', 'ACTORS_RULE_EXTERNAL_CALL_THREAD_POOL_SIZE': '4'}
        ce['environment'].update(tuning, SPRING_DATASOURCE_URL=f'jdbc:postgresql://host.docker.internal:{url.port}/' + database_names['cloud'],
                                 JAVA_OPTS='-Xms128m -Xmx512m -XX:MaxMetaspaceSize=256m -XX:MaxDirectMemorySize=64m -XX:ReservedCodeCacheSize=64m -XX:ActiveProcessorCount=2 -Xss512k')
        ce.update(mem_limit='3g', memswap_limit='3g', stop_grace_period='60s', ports=['127.0.0.1:19080:8080'], depends_on={'kafka': {'condition': 'service_healthy'}}, extra_hosts=['host.docker.internal:host-gateway'], restart='no')
        edge['environment'].update(tuning, SPRING_DATASOURCE_URL=f'jdbc:postgresql://host.docker.internal:{url.port}/' + database_names['edge'],
                                   CLOUD_RPC_HOST='thingsboard', CLOUD_RPC_PORT='7070',
                                   JAVA_OPTS='-Xms128m -Xmx512m -XX:MaxMetaspaceSize=256m -XX:MaxDirectMemorySize=96m -XX:ReservedCodeCacheSize=96m -XX:ActiveProcessorCount=2 -Xss512k')
        edge.pop('network_mode')
        native_data = work / 'native-edge-data'
        if args.reuse_native_databases:shutil.copytree(source / 'native-edge-data', native_data)
        else:native_data.mkdir(mode=0o777)
        native_data.chmod(0o777)
        if not args.initialize_native:(native_data / '.firstlaunch').touch()
        edge.update(ports=['127.0.0.1:19081:8080'], volumes=[str(native_data) + ':/data'], depends_on={'thingsboard': {'condition': 'service_started'}}, extra_hosts=['host.docker.internal:host-gateway'], restart='no')
        source_edge_env = source / 'tb-edge.env'
        if not source_edge_env.is_file():
            source_edge_env = source / 'prepared/tb-edge-edge-a.env'
        shutil.copy2(source_edge_env, prepared / 'tb-edge-edge-a.env')
        report['retained_edge_environment'] = {'path': str(source_edge_env), 'sha256': sha(source_edge_env)}
        kafka.update(memswap_limit='1g', restart='no')
        fixture = {'name': 'sf-delivery-native-20261005', 'services': {'kafka-storage-init': initializer, 'kafka': kafka, 'thingsboard': ce, 'edge-a-native': edge}, 'volumes': {'kafka': {}}}
        if args.initialize_native:
            install = json.loads(json.dumps(ce))
            install.pop('ports')
            install.update(profiles=['install'], restart='no')
            install['environment'].update(INSTALL_TB='true', LOAD_DEMO='false')
            install['environment']['JAVA_OPTS']='-Xms256m -Xmx768m -XX:MaxMetaspaceSize=384m -XX:MaxDirectMemorySize=128m -XX:ReservedCodeCacheSize=96m -XX:ActiveProcessorCount=2 -Xss512k'
            fixture['services']['tb-install'] = install
        for value in fixture['services'].values():
            value['labels'] = {'sf.evolution.fixture': fixture['name']}
        (prepared / 'compose.json').write_text(json.dumps(fixture, indent=2) + '\n')
        report['prepared_entrypoint'] = {'argv': edge['entrypoint'], 'stop_grace_period': edge['stop_grace_period'], 'prepare_source_sha256': sha(ROOT / 'scripts/prepare-runtime.py'),
                                         'fixture_adjustments': 'private cloned databases, isolated ports and data path, 3GiB CE budget and four-connection pools; generated native entrypoint and formal Kafka heap/container settings retained',
                                         'preparation_host_pki': 'darwin/arm64 override; business fixtures use native host binaries; final Linux bundle integration is verified separately'}
        if args.initialize_native:
            credentials = json.loads((prepared / 'development-credentials.json').read_text())
            for mode, node in [('cloud', 'cloud-1'), ('edge', 'edge-a')]:shutil.copy2(prepared / (node + '.master'), work / (mode + '.master'))
            for name in ['deployment-credentials.json', 'development-credentials.json']:shutil.copy2(prepared / name, work / name)
            sql('create-private-native-databases', '\n'.join('CREATE DATABASE "' + name + '";' for name in database_names.values()))
            report['native_state_origin'] = 'fresh isolated CE/Edge databases installed and enrolled with formal scripts after the earlier shared tmpfs server failed; telemetry preservation is verified within this run'
        else:
            for mode in ['cloud', 'edge']:
                original_db = source / (mode + '.db')
                with sqlite3.connect('file:' + str(original_db) + '?mode=ro', uri=True) as original, sqlite3.connect(work / (mode + '.db')) as target:original.backup(target)
                original_key=source / (mode + '.key')
                shutil.copy2(original_key if original_key.exists() else source / (mode + '.master'), work / (mode + '.master'))
            for name in ['tb-bootstrap.json', 'development-credentials.json']:shutil.copy2(source / name, work / name)
            if args.reuse_native_databases:
                report['native_state_origin']='continued the preceding verifier run\'s isolated PostgreSQL18 CE/Edge databases, copied business SQLite/master keys and retained native Edge data'
            else:sql('clone-private-native-databases', '\n'.join('CREATE DATABASE "' + database_names[mode] + '" TEMPLATE "' + ('sf_evolution_tbce_20261004' if mode == 'cloud' else 'sf_evolution_tbedge_20261004') + '";' for mode in ['cloud', 'edge']))
        compose = ['docker', 'compose', '-f', str(prepared / 'compose.json')]
        started_containers = True
        if args.initialize_native:run('install-new-native-ce', [*compose, '--profile', 'install', 'run', '--rm', 'tb-install'], timeout=600)
        run('start-ce-kafka', [*compose, 'up', '-d', '--pull', 'never', 'thingsboard'], timeout=300)
        if args.initialize_native:
            wait(lambda: request('http://127.0.0.1:19080', '/api/auth/login', {'username': 'sysadmin@thingsboard.org', 'password': 'sysadmin'}).get('token'), 'new-native-cloud-before-enrollment', 450, fixture['name'] + '-thingsboard-1')
            run('enroll-new-native-cloud-and-edge', ['python3', str(ROOT / 'scripts/bootstrap-thingsboard.py'), '--url', 'http://127.0.0.1:19080', '--state-directory', str(work), '--edges', 'edge-a'], timeout=600)
            native = json.loads((work / 'tb-bootstrap.json').read_text())
            shutil.copy2(work / 'tb-edge-edge-a.env', prepared / 'tb-edge-edge-a.env')
        ce_token = wait(lambda: request('http://127.0.0.1:19080', '/api/auth/login', {'username': native['username'], 'password': native['password']}).get('token'), 'native-cloud', 450, fixture['name'] + '-thingsboard-1')
        run('start-edge', [*compose, '--profile', 'native-edge', 'up', '-d', '--pull', 'never', 'edge-a-native'], timeout=300)
        edge_token = wait(lambda: request('http://127.0.0.1:19081', '/api/auth/login', {'username': native['username'], 'password': native['password']}).get('token'), 'native-edge', 450, fixture['name'] + '-edge-a-native-1')
        names = {}
        for name in ['thingsboard', 'edge-a-native', 'kafka']:
            identifier = run('container-id-' + name, [*compose, '--profile', 'native-edge', 'ps', '-q', name]).strip()
            info = json.loads(run('inspect-json-' + name, ['docker', 'inspect', identifier]))[0]
            names[name] = info['Name'].lstrip('/')

        def sample():
            while not stop_samples.is_set():
                values = {}
                for name, container in names.items():
                    result = subprocess.run(['docker', 'exec', container, 'sh', '-c', 'cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak'], capture_output=True, text=True)
                    if result.returncode == 0:
                        values[name] = [int(v) for v in result.stdout.split()]
                report['samples'].append({'at': utc(), 'memory_current_and_peak': values})
                stop_samples.wait(2)
        sampler = threading.Thread(target=sample, daemon=True)
        sampler.start()
        for name, container in names.items():
            cmdline = run('pid1-' + name, ['docker', 'exec', container, 'sh', '-c', 'tr "\\000" " " < /proc/1/cmdline'])
            report.setdefault('pid1', {})[name] = cmdline
        report['checks']['edge_java_is_pid1'] = report['pid1']['edge-a-native'].startswith('java ')
        report['checks']['kafka_formal_heap_applied'] = '-Xmx512m' in report['pid1']['kafka']
        tokens = {}
        for mode in ['cloud', 'edge']:
            tokens[mode]=start_app(mode)
            base='http://127.0.0.1:'+('19990' if mode=='cloud' else '19991')
            def definitions_ready(base=base,mode=mode):
                queues=request(base,'/api/sf/v1/runtime',token=tokens[mode])['queues']
                with sqlite3.connect('file:'+str(work/(mode+'.db'))+'?mode=ro',uri=True) as database:
                    states=[{'state':row[0],'count':row[1]} for row in database.execute('SELECT state,count(*) FROM river_job GROUP BY state ORDER BY state')]
                    pending=[{'kind':row[0],'count':row[1],'with_previous_errors':row[2]} for row in database.execute("SELECT kind,count(*),sum(CASE WHEN last_error<>'' THEN 1 ELSE 0 END) FROM outbox WHERE kind IN ('tb_entity','tb_definition') GROUP BY kind ORDER BY kind")]
                report.setdefault('projection_readiness_samples',[]).append({'at':utc(),'mode':mode,'runtime_queues':queues,'river_states':states,'pending':pending})
                save()
                return all(value['count']==0 for value in queues if value['kind'] in ['tb_entity','tb_definition'])
            wait(definitions_ready,'business-native-projections-'+mode,900)
        run('native-current-roundtrip', ['python3', str(ROOT / 'scripts/check-native.py'), '--state-directory', str(work), '--cloud-url', 'http://127.0.0.1:19990', '--edge-url', 'http://127.0.0.1:19991', '--tb-cloud-url', 'http://127.0.0.1:19080', '--tb-edge-url', 'http://127.0.0.1:19081', '--output', str(work / 'roundtrip.json')])
        roundtrip = json.loads((work / 'roundtrip.json').read_text())
        report['checks']['cloud_and_edge_current_original_derived_duplicate_and_native_callback'] = all(v['passed'] for v in roundtrip['profiles'])
        before_transactions = sql('native-database-connections-before-stop', "SELECT datname,state,count(*) FROM pg_stat_activity WHERE datname IN ('" + "','".join(database_names.values()) + "') GROUP BY datname,state ORDER BY datname,state;")
        report['database_connections_before_stop'] = before_transactions
        started = time.monotonic()
        run('formal-edge-stop', [*compose, '--profile', 'native-edge', 'stop', 'edge-a-native'], timeout=80)
        report['edge_stop_seconds'] = time.monotonic() - started
        state = json.loads(run('edge-stopped-state', ['docker', 'inspect', names['edge-a-native']]))[0]['State']
        report['edge_stopped_state'] = state
        report['checks']['edge_exits_before_stop_limit_without_oom'] = state['ExitCode'] in [0, 143] and not state['OOMKilled'] and report['edge_stop_seconds'] < 60
        sessions = sql('edge-database-transactions-ended', "SELECT count(*) FROM pg_stat_activity WHERE datname='" + database_names['edge'] + "' AND pid<>pg_backend_pid();").strip()
        report['checks']['edge_database_connections_and_transactions_released'] = sessions == '0'
        run('edge-shutdown-log', ['docker', 'logs', names['edge-a-native']])
        log = (work / 'edge-shutdown-log.log').read_text()
        report['checks']['edge_application_shutdown_logged'] = 'Shutdown' in log or 'shutdown' in log or 'Closing' in log or 'shutdownHook' in log
        run('restart-retained-edge', [*compose, '--profile', 'native-edge', 'start', 'edge-a-native'])
        edge_token = wait(lambda: request('http://127.0.0.1:19081', '/api/auth/login', {'username': native['username'], 'password': native['password']}).get('token'), 'retained-edge-restart', 450)
        edge_profile = next(v for v in roundtrip['profiles'] if v['name'] == 'edge')
        timestamp = int(edge_profile['message_id'].rsplit('-', 1)[1])
        device = wait(lambda: next((v for v in request('http://127.0.0.1:19991', '/api/sf/v1/entities', token=tokens['edge']) if v['id'] == 'climate-1'), None),
                      'retained-business-device-readable', 450)
        def retained_data():
            values = request('http://127.0.0.1:19081', f"/api/plugins/telemetry/DEVICE/{device['tb_id']}/values/timeseries?keys=temperature,climate-average.temperature&startTs={timestamp-1}&endTs={timestamp+1}&agg=NONE&limit=100", token=edge_token, tb=True, timeout=30)
            report.setdefault('retained_timeseries_attempts', []).append({'at': utc(), 'values': values})
            save()
            return values if all(any(int(point['ts']) == timestamp and float(point['value']) == 22.5 for point in values.get(key, []))
                                 for key in ['temperature', 'climate-average.temperature']) else None
        timeseries = wait(retained_data, 'retained-native-original-and-derived-data-readable', 450)
        report['retained_timeseries'] = timeseries
        report['checks']['native_original_and_derived_data_survive_restart'] = bool(timeseries.get('temperature') and timeseries.get('climate-average.temperature'))
        repeat = request('http://127.0.0.1:19991', '/api/sf/v1/ingest', {'message_id': edge_profile['message_id'], 'source_id': 'edge-a', 'critical': True, 'event': {'kind': 'native_verification'}, 'points': [{'device_id': 'climate-1', 'key': 'temperature', 'value': 22.5, 'observed_ms': timestamp, 'quality': 'GOOD', 'time_source': 'simulation'}]}, credentials['service_token'])
        report['checks']['repeat_after_native_restart_is_duplicate'] = repeat['duplicate'] and repeat['committed']
        for mode in ['cloud', 'edge']:
            audit = request('http://127.0.0.1:' + ('19990' if mode == 'cloud' else '19991'), '/api/sf/v1/audit/verify', {}, tokens[mode])
            report['checks'][mode + '_audit_chain_valid'] = audit['valid'] and not audit['issues']
        for _ in range(15):
            request('http://127.0.0.1:19080', '/api/auth/user', token=ce_token, tb=True)
            request('http://127.0.0.1:19081', '/api/auth/user', token=edge_token, tb=True)
            time.sleep(1)
        report['finite_workload'] = {'new_ingress_messages': 2, 'same_message_duplicate_requests': 3, 'authenticated_native_reads_per_service': 15,
                                     'native_edge_restart_count': 1, 'source_fixture_copied_databases_and_master_keys': not args.initialize_native}
        report['all_checks_passed'] = all(report['checks'].values())
        if not report['all_checks_passed']:
            raise RuntimeError('a required native check lacks passing evidence')
    except Exception as error:
        report['all_checks_passed'] = False
        report['error'] = str(error)
        report['error_traceback'] = traceback.format_exc()
        raise
    finally:
        stop_samples.set()
        if 'sampler' in locals():sampler.join(timeout=15)
        for process, output, entry in reversed(processes):
            started = time.monotonic()
            process.terminate()
            try:process.wait(timeout=30)
            except subprocess.TimeoutExpired:process.kill();process.wait();entry['forced_kill']=True
            output.close()
            entry.update(exit_code=process.returncode, stopped_at=utc(), stop_seconds=time.monotonic()-started, log_sha256=sha(Path(entry['log'])))
        if started_containers:
            run('formal-prepared-stack-stop', ['python3', str(ROOT / 'scripts/bootstrap-runtime.py'), 'stop', '--directory', str(prepared)], timeout=100, required=False)
            states = {}
            for name in ['thingsboard', 'edge-a-native', 'kafka']:
                identifier = run('final-id-' + name, [*compose, '--profile', 'native-edge', 'ps', '-a', '-q', name], required=False).strip()
                if not identifier:continue
                info = json.loads(run('final-state-' + name, ['docker', 'inspect', identifier]))[0]
                states[name] = info['State']
                run('final-log-' + name, ['docker', 'logs', identifier], required=False)
            report['final_container_states'] = states
            report['checks']['stack_stopped_without_oom_or_forced_container_kill'] = len(states) == 3 and all(value['ExitCode'] in [0, 143] and not value['OOMKilled'] for value in states.values())
            run('remove-own-native-project', [*compose, '--profile', 'native-edge', 'down', '-v'], required=False)
        if 'observed' in locals():
            after = {name: sha(Path(name)) for name in observed}
            (work / 'compile-inputs-after.json').write_text(json.dumps(after, indent=2)+'\n')
            report['compile_inputs_after']={'path':str(work/'compile-inputs-after.json'),'sha256':sha(work/'compile-inputs-after.json'),'count':len(after)}
            report['checks']['compile_inputs_unchanged']=after==observed
        report['checks']['business_processes_stopped_without_forced_kill'] = all(not entry.get('forced_kill') and entry['exit_code'] in [0, -15] for _, _, entry in processes)
        report['all_checks_passed'] = report.get('all_checks_passed', False) and all(report['checks'].values())
        report['completed_at'] = utc()
        save()
    print('Verified native stop, retained data and formal Kafka budget: ' + str(work / 'verification.json'))


if __name__ == '__main__':
    main()
