#!/usr/bin/env python3
"""Record fixed Go inputs and serial node-configuration consumer, PG and race checks."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--postgres-file', type=Path, required=True)
    parser.add_argument('--target-removal-only', action='store_true', help='repeat only the changed target-removal test under PG and race')
    parser.add_argument('--configuration-test-pattern', help='select the changed configuration test for a narrow repeat')
    args = parser.parse_args()
    os.umask(0o077)
    directory = args.directory.resolve()
    directory.mkdir(parents=True, exist_ok=False)
    archive = directory / 'fixed-inputs'
    archive.mkdir()
    records = []
    env = dict(os.environ, GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath',
               GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
               GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOPROXY='off', GOMAXPROCS='2')

    def save(name, value):
        path = directory / name
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
        path.chmod(0o600)
        return str(path)

    def capture(name, cwd, packages, test=False):
        argv = ['go', 'list', '-deps'] + (['-test'] if test else []) + ['-json'] + packages
        result = subprocess.run(argv, cwd=cwd, env=env, capture_output=True, text=True)
        (directory / (name + '.go-list.json')).write_text(result.stdout)
        (directory / (name + '.go-list.stderr.log')).write_text(result.stderr)
        if result.returncode:
            raise RuntimeError(name + ' go list failed')
        source_paths = set()
        decoder = json.JSONDecoder()
        remaining = result.stdout.lstrip()
        while remaining:
            package, consumed = decoder.raw_decode(remaining)
            remaining = remaining[consumed:].lstrip()
            folder = Path(package.get('Dir', cwd))
            for field in ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles',
                          'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles',
                          'EmbedFiles', 'TestGoFiles', 'XTestGoFiles', 'TestEmbedFiles', 'XTestEmbedFiles']:
                for filename in package.get(field, []):
                    path = (folder / filename).resolve()
                    if path.is_file():
                        source_paths.add(path)
            module = package.get('Module', {})
            for info in [module, module.get('Replace', {})]:
                if info.get('GoMod'):
                    source_paths.add(Path(info['GoMod']).resolve())
        for module in [ROOT / 'Platform', ROOT / 'DataTransfer']:
            for filename in ['go.mod', 'go.sum']:
                source_paths.add(module / filename)
        rows = []
        for path in sorted(source_paths):
            raw = path.read_bytes()
            digest = hashlib.sha256(raw).hexdigest()
            target = archive / digest
            if not target.exists():
                target.write_bytes(raw)
            rows.append({'path': str(path), 'sha256': digest, 'bytes': len(raw), 'archive': str(target)})
        save(name + '.inputs.json', rows)
        return rows

    def command(name, argv, cwd, command_env=None, packages=None, test=False):
        before = capture(name + '-before', cwd, packages, test) if packages else None
        started = time.time()
        result = subprocess.run(argv, cwd=cwd, env=command_env or env, capture_output=True, text=True)
        stdout = directory / (name + '.stdout.log')
        stderr = directory / (name + '.stderr.log')
        stdout.write_text(result.stdout)
        stderr.write_text(result.stderr)
        after = capture(name + '-after', cwd, packages, test) if packages else None
        record = {'name': name, 'argv': argv, 'cwd': str(cwd), 'exit_code': result.returncode,
                  'started_unix': started, 'elapsed_seconds': time.time() - started,
                  'stdout': str(stdout), 'stderr': str(stderr),
                  'source_before': str(directory / (name + '-before.inputs.json')) if before else None,
                  'source_after': str(directory / (name + '-after.inputs.json')) if after else None,
                  'input_unchanged': before == after if before else None}
        records.append(record)
        save('commands.json', records)
        if result.returncode or before != after:
            raise RuntimeError(name + (' failed' if result.returncode else ' inputs changed'))

    fixture = directory / 'dt-node-configuration-fixture'
    fixture_process = None
    fixture_logs = []

    def start_consumer(name):
        nonlocal fixture_process, fixture_logs
        state = directory / (name + '-consumer')
        out = open(directory / (name + '-consumer.stdout.log'), 'w')
        err = open(directory / (name + '-consumer.stderr.log'), 'w')
        fixture_logs = [out, err]
        argv = [str(fixture), '--directory', str(state), '--grpc-listen', '127.0.0.1:0', '--http-listen', '127.0.0.1:0']
        fixture_process = subprocess.Popen(argv, cwd=ROOT, env=env, stdout=out, stderr=err)
        for _ in range(100):
            if (state / 'ready.json').exists():
                ready = json.loads((state / 'ready.json').read_text())
                save(name + '-consumer.json', dict(ready, argv=argv))
                return ready['grpc_address']
            if fixture_process.poll() is not None:
                break
            time.sleep(0.1)
        raise RuntimeError('actual consumer did not start')

    def stop_consumer():
        nonlocal fixture_process
        if fixture_process is not None:
            if fixture_process.poll() is None:
                fixture_process.terminate()
                try:
                    fixture_process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    fixture_process.kill()
                    fixture_process.wait()
            records.append({'name': 'consumer-exit', 'pid': fixture_process.pid, 'exit_code': fixture_process.returncode})
            save('commands.json', records)
            fixture_process = None
            for log in fixture_logs:
                log.close()

    status, error = 'passed', None
    try:
        command('go-version', ['go', 'version'], ROOT / 'Platform')
        if not args.target_removal_only:
            command('build-consumer', ['go', 'build', '-o', str(fixture), './cmd/dt-node-configuration-fixture'],
                    ROOT / 'DataTransfer', packages=['./cmd/dt-node-configuration-fixture'])
            save('binaries.json', [{'path': str(fixture), 'bytes': fixture.stat().st_size,
                                    'sha256': hashlib.sha256(fixture.read_bytes()).hexdigest()}])
            address = start_consumer('sqlite')
            command('sqlite-actual-connector', ['go', 'test', '-count=1', '-p', '1', './internal/datatransfer',
                    '-run', 'TestControlledFixedConnector', '-v'], ROOT / 'Platform',
                    dict(env, SF_TEST_DATATRANSFER_CONNECTOR_ADDRESS=address), ['./internal/datatransfer'], True)
            stop_consumer()
        # The DSN is read directly into the environment and is never included in records.
        pg = json.loads(args.postgres_file.read_text())
        dsn = pg.get('dsn') or pg.get('database') or pg.get('DSN')
        if not dsn:
            raise RuntimeError('private PostgreSQL DSN is missing')
        configuration_pattern = args.configuration_test_pattern or ('TestFixedReleaseConfiguration' if args.target_removal_only else 'TestWorkload|TestFixedReleaseConfiguration')
        command('postgres-workloads', ['go', 'test', '-count=1', '-p', '1', './internal/configcenter', '-run',
                configuration_pattern, '-v'], ROOT / 'Platform',
                dict(env, SF_TEST_NODE_CONFIGURATION_PG='1', SF_TEST_POSTGRES_DATABASE=dsn), ['./internal/configcenter'], True)
        packages = ['./internal/configcenter'] if args.target_removal_only else ['./internal/configcenter', './internal/nodeidentity', './internal/datatransfer']
        if not args.target_removal_only:
            address = start_consumer('race')
        command('race-node-configuration', ['go', 'test', '-race', '-count=1', '-p', '1'] + packages + ['-run',
                configuration_pattern if args.target_removal_only else configuration_pattern+'|TestRemoteAuthority|TestControlledFixedConnector', '-v'],
                ROOT / 'Platform', env if args.target_removal_only else dict(env, SF_TEST_DATATRANSFER_CONNECTOR_ADDRESS=address),
                packages, True)
    except Exception as exc:
        status, error = 'failed', str(exc)
    finally:
        stop_consumer()
        result = {'status': status, 'error': error, 'commands': str(directory / 'commands.json'),
                  'postgres_pool': {'applications': {'max_open': 3, 'max_idle': 1}, 'administrative_max_open': 1},
                  'go_environment': {key: env[key] for key in ['GOTOOLCHAIN', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOPROXY', 'GOMAXPROCS']}}
        save('result.json', result)
        print(json.dumps(result, ensure_ascii=False))
    return 0 if status == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
