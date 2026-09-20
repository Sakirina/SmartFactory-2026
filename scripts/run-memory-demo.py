#!/usr/bin/env python3
"""Run the five-scene business demo with bounded lifetime and in-memory databases."""
import argparse
import base64
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import time
from urllib.error import URLError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary-dir', type=Path, default=ROOT / 'bin' if (ROOT / 'bin').is_dir() else Path('/private/tmp/smartfactory-build/bin'))
    parser.add_argument('--duration', type=int, default=900, help='maximum lifetime in seconds, 1..3600')
    args = parser.parse_args()
    if not 1 <= args.duration <= 3600:
        parser.error('duration must be 1..3600 seconds')
    names = ['sf-cloud', 'sf-edge', 'sf-config', 'sf-pki', 'sf-model-mock', 'datatransfer', 'dt-simulator']
    binaries = args.binary_dir.resolve()
    for name in names:
        if not (binaries / name).is_file():
            raise SystemExit('Build the missing binary: ' + name)
    for port in [8090, 8091, 8092, 8099, 1502, 18830, 48400, 18083, 18082, 50051, 18443]:
        with socket.socket() as probe:
            probe.settimeout(0.1)
            if probe.connect_ex(('127.0.0.1', port)) == 0:
                raise SystemExit('Demo port already in use: ' + str(port))
    os.umask(0o077)
    state = ROOT / '.local'
    private = state / 'memory-demo'
    private.mkdir(mode=0o700, parents=True, exist_ok=True)
    pki = state / 'pki'
    subprocess.run([str(binaries / 'sf-pki'), '--mode', 'init', '--dir', str(pki)], check=True)
    for service, node in [('cloud', 'cloud-1'), ('edge', 'edge-a')]:
        key = state / (service + '.key')
        if not key.exists():
            key.write_bytes(base64.b64encode(os.urandom(32)))
        command = [str(binaries / 'sf-pki'), '--dir', str(pki), '--node', node]
        if service == 'edge':
            command.append('--client')
        subprocess.run(command, check=True)
        data = subprocess.check_output([str(binaries / 'sf-pki'), '--mode', 'public-keys', '--dir', str(pki), '--node', node, '--master-key', str(key)])
        (pki / (node + '.public.json')).write_bytes(data)
    processes = []
    stopped = False
    def stop(*_):
        nonlocal stopped
        stopped = True
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    def start(name, command, variables=None):
        environment = dict(os.environ)
        environment.update(variables or {})
        log = (private / (name + '.log')).open('wb')
        try:
            child = subprocess.Popen(command, cwd=ROOT, env=environment, stdout=log, stderr=subprocess.STDOUT)
        finally:
            log.close()
        processes.append((name, child))
        return child
    def wait_port(port):
        until = time.monotonic() + 25
        while time.monotonic() < until:
            if stopped or any(p.poll() is not None for _, p in processes):
                raise RuntimeError('A demo process stopped; inspect .local/memory-demo logs')
            with socket.socket() as probe:
                probe.settimeout(0.2)
                if probe.connect_ex(('127.0.0.1', port)) == 0:
                    return
            time.sleep(0.1)
        raise RuntimeError('Port did not become ready: ' + str(port))
    def service(name, *extra, variables=None):
        values = {'SF_DATABASE': ':memory:'}
        values.update(variables or {})
        start(name, ['python3', str(ROOT / 'scripts/run-local.py'), name, '--binary-dir', str(binaries), *extra], values)
    started = time.monotonic()
    try:
        start('model', [str(binaries / 'sf-model-mock')])
        service('config')
        wait_port(8092)
        service('cloud', '--seed', '--sync', '--config-center', variables={'SF_MODEL_ENDPOINT': 'http://127.0.0.1:8099/v1', 'SF_MODEL_NAME': 'sf-acceptance-simulator'})
        wait_port(8090)
        subprocess.run(['python3', str(ROOT / 'scripts/enroll-local-edge.py')], cwd=ROOT, check=True)
        config = private / 'datatransfer.yaml'
        start('simulator', [str(binaries / 'dt-simulator'), '--state', ':memory:', '--config-out', str(config)])
        wait_port(18083)
        start('datatransfer', [str(binaries / 'datatransfer'), '--config', str(config)], {'DT_RUNTIME_STATE_PATH': ':memory:', 'DT_BUFFER_ENABLED': 'false'})
        wait_port(50051)
        service('edge', '--seed', '--sync', '--config-center', variables={'SF_DATATRANSFER_ADDRESS': '127.0.0.1:50051'})
        wait_port(8091)
        manifest = {'storage': 'memory', 'thingsboard_native': False, 'duration_limit_seconds': args.duration, 'processes': {name: p.pid for name, p in processes}, 'urls': {'cloud': 'http://127.0.0.1:8090', 'edge': 'http://127.0.0.1:8091/edge', 'screen': 'http://127.0.0.1:8090/screen'}}
        (private / 'runtime.json').write_text(json.dumps(manifest, indent=2) + '\n')
        print('Memory demo ready: cloud 8090, edge 8091, simulator 18083; stopping after ' + str(args.duration) + ' seconds', flush=True)
        while not stopped and time.monotonic() - started < args.duration:
            ended = [(name, p.returncode) for name, p in processes if p.poll() is not None]
            if ended:
                raise RuntimeError('Demo process exited: ' + str(ended))
            time.sleep(0.5)
    finally:
        for _, child in reversed(processes):
            if child.poll() is None:
                child.send_signal(signal.SIGINT)
        until = time.monotonic() + 8
        for _, child in reversed(processes):
            try:
                child.wait(timeout=max(0.1, until - time.monotonic()))
            except subprocess.TimeoutExpired:
                child.terminate()
                child.wait(timeout=5)
        print('Memory demo stopped; in-memory observations discarded, configuration and evidence files retained', flush=True)


if __name__ == '__main__':
    main()
