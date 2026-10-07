#!/usr/bin/env python3
"""Run finite external protocol and durable-process release regressions."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]


def utc():
    return datetime.now(timezone.utc).isoformat()


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--python', type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    work = args.directory.resolve()
    work.mkdir(parents=True, exist_ok=False)
    binaries = work / 'bin'
    binaries.mkdir()
    env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOMAXPROCS='2',
               GOPATH='/private/tmp/smartfactory-evolution-gopath',
               GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
               GOCACHE='/private/tmp/smartfactory-evolution-go-cache')
    report = {'schema_version': 1, 'started_at': utc(), 'directory': str(work),
              'scope': 'finite external OPC UA, encrypted certificate validation, real HTTP plugin, external MQTT and A06 100000 durable messages',
              'commands': [], 'processes': [], 'checks': {}}
    live = []

    def save():
        (work / 'result.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')

    def run(name, argv, cwd=ROOT / 'DataTransfer', extra=None, timeout=300, trace=False):
        started = time.monotonic()
        started_at = utc()
        log = work / (name + '.log')
        with log.open('w') as output:
            process = subprocess.Popen(argv, cwd=cwd, env=dict(env, **(extra or {})), stdout=output, stderr=subprocess.STDOUT)
            observed = {}
            try:
                while process.poll() is None:
                    if trace:
                        listing = subprocess.check_output(['ps', '-axo', 'pid=,ppid=,command='], text=True)
                        for line in listing.splitlines():
                            fields = line.strip().split(None, 2)
                            if len(fields) == 3 and fields[2].startswith(str(binaries / 'durable.test')):
                                observed.setdefault(fields[0], {'pid': int(fields[0]), 'parent_pid': int(fields[1]), 'argv': fields[2], 'first_observed': utc()})
                    if time.monotonic() - started > timeout:
                        raise TimeoutError(name)
                    time.sleep(0.2 if trace else 0.1)
            except Exception:
                process.kill()
                process.wait()
                raise
        entry = {'name': name, 'argv': argv, 'cwd': str(cwd), 'started_at': started_at,
                 'exit_code': process.returncode, 'duration_seconds': time.monotonic() - started,
                 'log': str(log), 'log_sha256': digest(log)}
        if observed:
            entry['observed_processes'] = list(observed.values())
        report['commands'].append(entry)
        save()
        if process.returncode:
            raise RuntimeError(name + ' failed; inspect ' + str(log))
        return log.read_text()

    def start_peer(name, argv, port):
        log = work / (name + '.log')
        output = log.open('w')
        process = subprocess.Popen(argv, cwd=work, env=env, stdout=output, stderr=subprocess.STDOUT)
        entry = {'name': name, 'argv': argv, 'pid': process.pid, 'log': str(log), 'started_at': utc()}
        report['processes'].append(entry)
        live.append((process, output, entry))
        until = time.monotonic() + 20
        while time.monotonic() < until and process.poll() is None:
            try:
                with socket.create_connection(('127.0.0.1', port), timeout=0.1):
                    entry['ready_at'] = utc()
                    save()
                    return
            except OSError:
                time.sleep(0.1)
        raise RuntimeError(name + ' did not listen; inspect ' + str(log))

    try:
        packages = ['internal/connector/opcua', 'internal/connector/sidecar', 'internal/northbound/mqtt', 'tests/acceptance']
        raw = run('go-list-inputs', ['go', 'list', '-deps', '-json', '-test', *['./' + p for p in packages], './examples/http-plugin'])
        decoder = json.JSONDecoder()
        inputs = {}
        while raw.strip():
            package, end = decoder.raw_decode(raw.lstrip())
            raw = raw.lstrip()[end:]
            directory = Path(package.get('Dir', '/nonexistent'))
            for field in ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'TestGoFiles', 'XTestGoFiles', 'EmbedFiles', 'TestEmbedFiles', 'XTestEmbedFiles']:
                for name in package.get(field, []):
                    path = directory / name
                    if path.is_file():
                        inputs[str(path)] = {'sha256': digest(path), 'bytes': path.stat().st_size,
                                             'first_party': path.resolve().is_relative_to(ROOT)}
        for name in ['DataTransfer/go.mod', 'DataTransfer/go.sum', 'tests/fixtures/opcua_secure.py', 'tests/fixtures/requirements-opcua.txt']:
            path = ROOT / name
            inputs[str(path)] = {'sha256': digest(path), 'bytes': path.stat().st_size, 'first_party': True}
        (work / 'compile-inputs.json').write_text(json.dumps(inputs, indent=2) + '\n')
        report['compile_inputs'] = {'path': str(work / 'compile-inputs.json'), 'sha256': digest(work / 'compile-inputs.json'), 'count': len(inputs)}
        report['toolchain'] = {'go': run('go-version', ['go', 'version']).strip(),
                               'python_packages': run('python-packages', [str(args.python), '-m', 'pip', 'freeze'], cwd=work).splitlines()}
        for package, name in zip(packages, ['opcua', 'sidecar', 'mqtt', 'durable']):
            run('build-' + name, ['go', 'test', '-c', '-p', '1', '-o', str(binaries / (name + '.test')), './' + package])
        run('build-http-plugin', ['go', 'build', '-p', '1', '-trimpath', '-o', str(binaries / 'http-plugin'), './examples/http-plugin'])
        plain = work / 'opcua_plain.py'
        plain.write_text('''import asyncio, sys\nfrom asyncua import Server, ua\nasync def main():\n    server = Server()\n    await server.init()\n    server.set_endpoint("opc.tcp://127.0.0.1:" + sys.argv[1])\n    server.set_security_policy([ua.SecurityPolicyType.NoSecurity])\n    async with server:\n        print("READY", flush=True)\n        await asyncio.Event().wait()\nasyncio.run(main())\n''')
        mqtt_source = work / 'mqtt_peer.go'
        mqtt_source.write_text('''package main\nimport("io";"log/slog";"os";"os/signal";"syscall"; mqtt "github.com/mochi-mqtt/server/v2";"github.com/mochi-mqtt/server/v2/hooks/auth";"github.com/mochi-mqtt/server/v2/listeners")\nfunc main(){s:=mqtt.New(&mqtt.Options{Logger:slog.New(slog.NewTextHandler(io.Discard,nil))});if e:=s.AddHook(new(auth.AllowHook),nil);e!=nil{panic(e)};if e:=s.AddListener(listeners.NewTCP(listeners.Config{ID:"external",Address:"127.0.0.1:"+os.Args[1]}));e!=nil{panic(e)};go func(){if e:=s.Serve();e!=nil{panic(e)}}();ch:=make(chan os.Signal,1);signal.Notify(ch,syscall.SIGTERM,syscall.SIGINT);<-ch;s.Close()}\n''')
        run('build-mqtt-peer', ['go', 'build', '-p', '1', '-o', str(binaries / 'mqtt-peer'), str(mqtt_source)])
        opcua_port, mqtt_port = free_port(), free_port()
        start_peer('external-opcua', [str(args.python), str(plain), str(opcua_port)], opcua_port)
        start_peer('external-mqtt', [str(binaries / 'mqtt-peer'), str(mqtt_port)], mqtt_port)
        log = run('opcua-external-and-security', [str(binaries / 'opcua.test'), '-test.v', '-test.timeout=2m'],
                  cwd=ROOT / 'DataTransfer/internal/connector/opcua',
                  extra={'DT_OPCUA_SMOKE_ENDPOINT': 'opc.tcp://127.0.0.1:' + str(opcua_port), 'SF_OPCUA_PYTHON': str(args.python)})
        for name in ['TestNativeClientExternalSmoke', 'TestNativeSignedEncryptedMethodAndCertificateValidation']:
            report['checks'][name] = '--- PASS: ' + name in log
        log = run('http-plugin-process', [str(binaries / 'sidecar.test'), '-test.v', '-test.run=^TestHTTPExampleProcessCollectsAndControlsSimulator$', '-test.timeout=2m'],
                  cwd=ROOT / 'DataTransfer/internal/connector/sidecar', extra={'SF_HTTP_PLUGIN_BINARY': str(binaries / 'http-plugin')})
        report['checks']['TestHTTPExampleProcessCollectsAndControlsSimulator'] = '--- PASS: TestHTTPExampleProcessCollectsAndControlsSimulator' in log
        log = run('mqtt-external', [str(binaries / 'mqtt.test'), '-test.v', '-test.timeout=2m'],
                  extra={'DT_MQTT_SMOKE_BROKER': 'tcp://127.0.0.1:' + str(mqtt_port)})
        report['checks']['TestExternalBrokerSmoke'] = '--- PASS: TestExternalBrokerSmoke' in log
        log = run('durable-100000', [str(binaries / 'durable.test'), '-test.v', '-test.run=^TestDurableMessagesFaultInjection$', '-test.timeout=20m'],
                  extra={'SF_ACCEPTANCE_COUNT': '100000', 'SF_A06_EVIDENCE': str(work / 'durable-a06.json')}, timeout=1210, trace=True)
        report['checks']['TestDurableMessagesFaultInjection'] = '--- PASS: TestDurableMessagesFaultInjection' in log
        report['durable'] = json.loads((work / 'durable-a06.json').read_text())
        children = [p for p in report['commands'][-1].get('observed_processes', []) if '-test.run=^TestDurableProcessFixture$' in p['argv']]
        report['checks']['durable_actual_child_and_three_restarts_observed'] = len(children) == 4
        report['durable_processes'] = children
        changed = [p for p, expected in inputs.items() if digest(Path(p)) != expected['sha256']]
        report['checks']['compile_inputs_unchanged'] = not changed
        report['changed_compile_inputs'] = changed
        report['binaries'] = {p.name: {'path': str(p), 'sha256': digest(p), 'bytes': p.stat().st_size} for p in binaries.iterdir()}
        report['fixtures'] = {p.name: {'path': str(p), 'sha256': digest(p)} for p in [plain, mqtt_source]}
        report['all_checks_passed'] = all(report['checks'].values())
        if not report['all_checks_passed']:
            raise RuntimeError('a required check lacks passing evidence')
    except Exception as error:
        report['all_checks_passed'] = False
        report['error'] = str(error)
        raise
    finally:
        for process, output, entry in reversed(live):
            started = time.monotonic()
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                entry['forced_kill'] = True
            output.close()
            entry.update(exit_code=process.returncode, stopped_at=utc(), stop_seconds=time.monotonic() - started,
                         log_sha256=digest(Path(entry['log'])))
        report['completed_at'] = utc()
        save()
    print('Verified finite protocol and durable regressions: ' + str(work / 'result.json'))


if __name__ == '__main__':
    main()
