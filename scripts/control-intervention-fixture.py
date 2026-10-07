#!/usr/bin/env python3
"""Run the controlled cloud/edge/DataTransfer browser fixture using actual application code."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def compile_inputs(project, packages, env):
    command = ['go', 'list', '-deps', '-test', '-json', *packages]
    output = subprocess.check_output(command, cwd=ROOT / project, env=env, text=True)
    decoder, position, paths = json.JSONDecoder(), 0, set()
    # go list -test emits a compiled test-package variant whose GoFiles already
    # includes the selected package's tests. Dependency TestGoFiles are metadata
    # for tests that this binary does not compile, so they are excluded here.
    fields = ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles']
    while position < len(output):
        position += len(output[position:]) - len(output[position:].lstrip())
        if position == len(output):
            break
        package, position = decoder.raw_decode(output, position)
        directory = Path(package.get('Dir', ROOT / project))
        for field in fields:
            for name in package.get(field, []):
                path = directory / name
                if path.is_file():
                    paths.add(path.resolve())
        module = package.get('Module', {})
        if module.get('GoMod'):
            paths.add(Path(module['GoMod']).resolve())
    for module in ['Platform', 'DataTransfer']:
        for name in ['go.mod', 'go.sum']:
            paths.add(ROOT / module / name)
    return [{'path': str(path), 'sha256': digest(path), 'bytes': path.stat().st_size} for path in sorted(paths)]

def read_json(path):
    return json.loads(path.read_text())

def start(directory):
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    if any(directory.iterdir()):
        raise RuntimeError('Choose an empty output directory so earlier evidence is preserved')
    binaries = {}
    env = dict(os.environ)
    env.setdefault('GOTOOLCHAIN', 'local')
    inputs = []
    for project, packages in [('DataTransfer', ['./internal/northbound/grpc', './cmd/datatransfer']), ('Platform', ['./internal/app', './cmd/sf-cloud', './cmd/sf-edge'])]:
        inputs.extend(compile_inputs(project, packages, env))
    inputs = sorted({item['path']: item for item in inputs}.values(), key=lambda item: item['path'])
    commands = []
    with (directory / 'build.log').open('w') as log:
        for name, project, package in [('device', 'DataTransfer', './internal/northbound/grpc'), ('platform', 'Platform', './internal/app')]:
            binary = directory / (name + '-control.test')
            command = ['go', 'test', '-c', '-p', '1', '-o', str(binary), package]
            subprocess.run(command, cwd=ROOT / project, env=env, check=True, stdout=log, stderr=subprocess.STDOUT)
            commands.append({'cwd': str(ROOT / project), 'command': command, 'exit_code': 0})
            binaries[name] = binary
        for name, project, package in [('sf-cloud', 'Platform', './cmd/sf-cloud'), ('sf-edge', 'Platform', './cmd/sf-edge'), ('datatransfer', 'DataTransfer', './cmd/datatransfer')]:
            binary = directory / name
            command = ['go', 'build', '-p', '1', '-o', str(binary), package]
            subprocess.run(command, cwd=ROOT / project, env=env, check=True, stdout=log, stderr=subprocess.STDOUT)
            commands.append({'cwd': str(ROOT / project), 'command': command, 'exit_code': 0})
            binaries[name] = binary
    changed = [item['path'] for item in inputs if digest(Path(item['path'])) != item['sha256']]
    if changed:
        raise RuntimeError('Build inputs changed during compilation: ' + ', '.join(changed))
    input_path = directory / 'build-inputs.json'
    input_path.write_text(json.dumps(inputs, indent=2) + '\n')
    manifest = {'created_at_ms': int(time.time() * 1000), 'commands': commands,
                'toolchain': json.loads(subprocess.check_output(['go', 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOEXPERIMENT'], env=env, text=True)),
                'build_inputs': {'path': str(input_path), 'sha256': digest(input_path), 'count': len(inputs), 'verified_unchanged_after_build': True},
                'binaries': {name: {'path': str(binary), 'sha256': digest(binary), 'bytes': binary.stat().st_size} for name, binary in binaries.items()},
                'fixture_script_sha256': digest(Path(__file__))}
    (directory / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    runtime = directory / 'runtime'
    runtime.mkdir(mode=0o700)
    env.update(SF_CONTROL_DEVICE_BINARY=str(binaries['device']), SF_CONTROL_BROWSER_FIXTURE=str(runtime), SF_CONTROL_STATIC_DIR=str(ROOT / 'Frontends' / 'dist'))
    with (directory / 'applications.log').open('w') as log:
        process = subprocess.Popen([str(binaries['platform']), '-test.run=^TestControlBrowserFixture$', '-test.timeout=2h', '-test.v'], cwd=ROOT / 'Platform', env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    (directory / 'process.json').write_text(json.dumps({'pid': process.pid, 'runtime': str(runtime), 'created_at_ms': int(time.time() * 1000)}, indent=2))
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError('Fixture exited; inspect applications.log and runtime/device.log')
        ready = runtime / 'browser-ready.json'
        if ready.exists():
            manifest['runtime'] = {'test_run': '^TestControlBrowserFixture$', 'test_timeout': '2h', 'static_dir': str(ROOT / 'Frontends' / 'dist'), 'pid': process.pid, 'startup': read_json(ready), 'device_rpc': 'actual gRPC', 'cloud_edge_transport': 'mTLS signed exchange', 'database': 'separate SQLite files', 'simulator': 'DataTransfer runtime Modbus TCP counted commands'}
            (directory / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
            print(json.dumps(read_json(ready), ensure_ascii=False, indent=2))
            return
        time.sleep(0.1)
    raise RuntimeError('Fixture startup timed out; inspect applications.log; use stop for cleanup')

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['start', 'status', 'stop'])
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    directory = args.output.expanduser().resolve()
    if args.action == 'start':
        start(directory)
        return
    runtime = directory / 'runtime'
    if args.action == 'status':
        ready = read_json(runtime / 'browser-ready.json')
        counts = runtime / 'physical-actions.json'
        ready['physical_actions'] = read_json(counts) if counts.exists() else {}
        print(json.dumps(ready, ensure_ascii=False, indent=2))
        return
    (runtime / 'browser-stop').write_text('stop requested\n')
    print('Shutdown requested; application workers and the DataTransfer fixture stop together')

if __name__ == '__main__':
    main()
