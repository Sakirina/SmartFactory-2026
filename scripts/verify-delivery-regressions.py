#!/usr/bin/env python3
"""Record complete Go test inputs and run final serial application regressions."""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import traceback
from urllib.parse import urlparse, unquote, urlunparse

ROOT = Path(__file__).resolve().parents[1]
INPUT_FIELDS = ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles',
                'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles', 'TestGoFiles', 'XTestGoFiles', 'TestEmbedFiles', 'XTestEmbedFiles']


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def utc():
    return datetime.now(timezone.utc).isoformat()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--postgres-description', type=Path, default=ROOT / '.local/evolution/pg18-test.json')
    parser.add_argument('--retained-inputs', type=Path, action='append', default=[])
    args = parser.parse_args()
    os.umask(0o077)
    work = args.directory.resolve()
    work.mkdir(parents=True, exist_ok=False)
    fixed = work / 'fixed-inputs'
    fixed.mkdir()
    pg = json.loads(args.postgres_description.read_text())
    parsed = urlparse(pg['dsn'])
    user, password = unquote(parsed.username), unquote(parsed.password)
    database_name = 'sf_delivery_regression_' + hashlib.sha256(str(work).encode()).hexdigest()[:10]
    dsn = urlunparse(parsed._replace(path='/' + database_name))
    env = {key: value for key, value in os.environ.items() if not key.startswith(('SF_', 'DT_', 'OTEL_'))}
    env.update(GOTOOLCHAIN='local', GOPROXY='off', GOMAXPROCS='2', CGO_ENABLED='0',
               GOPATH='/private/tmp/smartfactory-evolution-gopath', GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
               GOCACHE='/private/tmp/smartfactory-evolution-go-cache', SF_TEST_POSTGRES_DATABASE=dsn, SF_TEST_NODE_CONFIGURATION_PG='1')
    report = {'schema_version': 1, 'started_at': utc(), 'status': 'running', 'commands': [], 'input_sets': [], 'test_results': {},
              'scope': 'darwin/arm64 CGO=0 final serial Go regressions with isolated PostgreSQL18 database and a real ConfigManager/gRPC consumer; Linux executables verified by the separate delivery runtime',
              'database': database_name, 'postgres_container': pg['container'], 'input_fields': INPUT_FIELDS,
              'retained_input_indexes': [str(path.resolve()) for path in args.retained_inputs], 'reused_inputs': 0, 'new_retained_inputs': 0}
    consumer = None
    streams = []
    created = False
    known = {}
    for index in args.retained_inputs:
        data = json.loads(index.read_text())
        entries = data.get('inputs', data) if isinstance(data, dict) else data
        values = entries.values() if isinstance(entries, dict) else entries
        for entry in values:
            if not isinstance(entry, dict):
                continue
            retained = entry.get('retained_copy') or entry.get('retained_path') or entry.get('retained') or entry.get('archive')
            if retained and entry.get('sha256'):
                path = Path(retained)
                if path.is_file() and sha(path) == entry['sha256']:
                    known[entry['sha256']] = path
    reused, new = set(), set()

    def save():
        report['reused_inputs'], report['new_retained_inputs'] = len(reused), len(new)
        (work / 'verification.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')

    def command(name, argv, cwd=ROOT, command_env=None, input=None, required=True):
        start = time.monotonic()
        result = subprocess.run(argv, cwd=cwd, env=command_env or env, input=input, capture_output=True, text=True)
        output = work / (name + '.stdout.log')
        error = work / (name + '.stderr.log')
        for path, text in [(output, result.stdout), (error, result.stderr)]:
            path.write_text(text.replace(dsn, '<private-dsn>').replace(password, '<private-password>'))
        report['commands'].append({'name': name, 'argv': argv, 'cwd': str(cwd), 'exit_code': result.returncode,
                                   'duration_seconds': time.monotonic() - start, 'stdout': str(output), 'stdout_sha256': sha(output),
                                   'stderr': str(error), 'stderr_sha256': sha(error)})
        save()
        if required and result.returncode:
            raise RuntimeError(name + ' failed; inspect its saved logs')
        return result

    def sql(name, query, database='postgres'):
        return command(name, ['docker', 'exec', '-i', pg['container'], 'psql', '-U', user, '-d', database, '-v', 'ON_ERROR_STOP=1', '-At'], input=query).stdout.strip()

    def capture(module, name):
        result = command(name + '-go-list', ['go', 'list', '-deps', '-test', '-json', './...'], ROOT / module)
        remaining, decoder, entries = result.stdout.lstrip(), json.JSONDecoder(), {}
        packages = []
        replacements = []
        while remaining:
            package, end = decoder.raw_decode(remaining)
            remaining = remaining[end:].lstrip()
            packages.append(package.get('ImportPath'))
            directory = Path(package.get('Dir', ROOT / module)).resolve()
            for field in INPUT_FIELDS:
                for filename in package.get(field, []):
                    path = (directory / filename).resolve()
                    if not path.is_file():
                        raise FileNotFoundError(str(path))
                    entries.setdefault(path, set()).add(field)
            info = package.get('Module', {})
            if info.get('Replace'):
                replacements.append({'module': info.get('Path'), 'replacement': info['Replace']})
            for mod in [info, info.get('Replace', {})]:
                if mod.get('GoMod'):
                    entries.setdefault(Path(mod['GoMod']).resolve(), set()).add('module_lock')
        for folder in ['Platform', 'DataTransfer']:
            for filename in ['go.mod', 'go.sum']:
                entries.setdefault(ROOT / folder / filename, set()).add('module_lock')
        records = []
        for path, kinds in sorted(entries.items()):
            digest = sha(path)
            retained = known.get(digest)
            if retained:
                reused.add(digest)
            else:
                retained = fixed / digest
                if not retained.exists():
                    shutil.copyfile(path, retained)
                known[digest] = retained
                new.add(digest)
            records.append({'path': str(path), 'sha256': digest, 'bytes': path.stat().st_size, 'input_kinds': sorted(kinds), 'retained_copy': str(retained)})
        path = work / (name + '.inputs.json')
        path.write_text(json.dumps(records, ensure_ascii=False, indent=2) + '\n')
        report['input_sets'].append({'name': name, 'module': module, 'path': str(path), 'sha256': sha(path), 'input_count': len(records),
                                     'package_count': len(packages), 'module_replacements': replacements, 'overlays': env.get('GOFLAGS', '')})
        save()
        return {entry['path']: entry['sha256'] for entry in records}

    try:
        shutil.copy2(Path(__file__).resolve(), work / 'driver.py')
        report['driver_sha256'] = sha(work / 'driver.py')
        toolchain = command('actual-go-toolchain', ['go', 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOTOOLCHAIN', 'GOROOT', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOPROXY', 'GOFLAGS', 'GOWORK', 'GOTOOLDIR']).stdout
        report['toolchain_environment'] = json.loads(toolchain)
        tool_directory = Path(report['toolchain_environment']['GOTOOLDIR'])
        report['toolchain_executables'] = {name: {'path': str(path), 'sha256': sha(path)} for name, path in [('go', Path(shutil.which('go'))), *[(name, tool_directory / name) for name in ['compile', 'link', 'asm', 'vet']]]}
        sql('create-only-own-regression-database', 'CREATE DATABASE ' + database_name + ';')
        created = True
        report['initial_database_connections'] = sql('initial-database-connections', "SELECT count(*) FROM pg_stat_activity WHERE datname='" + database_name + "';")
        consumer_binary = work / 'dt-node-configuration-fixture'
        before = capture('DataTransfer', 'consumer-build-before')
        command('build-current-configuration-consumer', ['go', 'build', '-p', '1', '-trimpath', '-o', str(consumer_binary), './cmd/dt-node-configuration-fixture'], ROOT / 'DataTransfer')
        after = capture('DataTransfer', 'consumer-build-after')
        if before != after:
            raise RuntimeError('consumer build inputs changed')
        report['consumer_binary'] = {'path': str(consumer_binary), 'bytes': consumer_binary.stat().st_size, 'sha256': sha(consumer_binary)}
        consumer_state = work / 'consumer-state'
        streams = [(work / 'consumer.stdout.log').open('w'), (work / 'consumer.stderr.log').open('w')]
        argv = [str(consumer_binary), '--directory', str(consumer_state), '--grpc-listen', '127.0.0.1:0', '--http-listen', '127.0.0.1:0']
        consumer = subprocess.Popen(argv, cwd=ROOT, env=env, stdout=streams[0], stderr=streams[1])
        report['consumer_process'] = {'pid': consumer.pid, 'argv': argv, 'started_at': utc()}
        until = time.monotonic() + 30
        while not (consumer_state / 'ready.json').is_file() and time.monotonic() < until:
            if consumer.poll() is not None:
                raise RuntimeError('configuration consumer exited before readiness')
            time.sleep(0.1)
        ready = json.loads((consumer_state / 'ready.json').read_text())
        env['SF_TEST_DATATRANSFER_CONNECTOR_ADDRESS'] = ready['grpc_address']
        for module in ['Platform', 'DataTransfer']:
            before = capture(module, module.lower() + '-tests-before')
            result = command(module.lower() + '-tests', ['go', 'test', '-count=1', '-p', '1', '-json', './...'], ROOT / module, required=False)
            after = capture(module, module.lower() + '-tests-after')
            counts, skips, failures = Counter(), [], []
            for line in result.stdout.splitlines():
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if event.get('Test') and event.get('Action') in ['pass', 'fail', 'skip']:
                    counts[event['Action']] += 1
                    if event['Action'] in ['skip', 'fail']:
                        (skips if event['Action'] == 'skip' else failures).append({'package': event['Package'], 'test': event['Test'], 'elapsed_seconds': event.get('Elapsed')})
            report['test_results'][module] = {'exit_code': result.returncode, 'inputs_unchanged': before == after, 'counts_including_subtests': dict(counts), 'skips': skips, 'failures': failures}
            save()
            if result.returncode or before != after:
                raise RuntimeError(module + ' final regression failed or its inputs changed')
        report['status'] = 'passed'
    except Exception as error:
        report['status'] = 'failed'
        report['error'] = str(error).replace(password, '<private-password>')
        report['error_traceback'] = traceback.format_exc().replace(password, '<private-password>')
        raise
    finally:
        if consumer is not None:
            consumer.terminate()
            try:
                consumer.wait(timeout=15)
            except subprocess.TimeoutExpired:
                consumer.kill()
                consumer.wait()
                report['consumer_process']['forced_kill'] = True
            report['consumer_process'].update(exit_code=consumer.returncode, stopped_at=utc())
            for stream in streams:
                stream.close()
            report['consumer_logs'] = {name: {'path': str(work / name), 'sha256': sha(work / name)} for name in ['consumer.stdout.log', 'consumer.stderr.log']}
        if created:
            report['final_database_connections'] = sql('final-database-connections', "SELECT count(*) FROM pg_stat_activity WHERE datname='" + database_name + "';")
            report['remaining_schemas'] = sql('final-own-schemas', "SELECT nspname FROM pg_namespace WHERE nspname NOT IN ('public','information_schema') AND nspname NOT LIKE 'pg_%';", database_name)
            if report['status'] == 'passed' and report['final_database_connections'] == '0':
                sql('drop-only-own-regression-database', 'DROP DATABASE ' + database_name + ';')
                report['database_removed'] = sql('confirm-own-database-removed', "SELECT count(*) FROM pg_database WHERE datname='" + database_name + "';") == '0'
            else:
                report['database_retained_for_failed_verification'] = True
        report['completed_at'] = utc()
        report['all_checks_passed'] = report['status'] == 'passed' and report.get('database_removed', False) and not report.get('consumer_process', {}).get('forced_kill', False) and report.get('consumer_process', {}).get('exit_code') in [0, -15]
        save()
    print('Verified final module regressions: ' + str(work / 'verification.json'))


if __name__ == '__main__':
    main()
