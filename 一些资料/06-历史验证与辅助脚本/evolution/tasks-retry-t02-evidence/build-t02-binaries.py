import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile

root = Path('/path/to/SmartFactory-2026')
output = Path('/private/tmp/smartfactory-t02-current')
output.mkdir(exist_ok=True)
evidence = root / '.local/evolution/tasks-retry-t02-evidence'
env = os.environ.copy()
env.update(GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath',
           GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
           GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOPROXY='off')

def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def capture(argv):
    return subprocess.check_output(argv, env=env, cwd=root / 'Platform', text=True)

def decode_stream(raw):
    decoder = json.JSONDecoder()
    rows, offset = [], 0
    while offset < len(raw):
        while offset < len(raw) and raw[offset].isspace():
            offset += 1
        if offset == len(raw):
            break
        row, offset = decoder.raw_decode(raw, offset)
        rows.append(row)
    return rows

manifests, all_inputs = {}, set()
for name in ('sf-history-fixture', 'sf-cloud'):
    package_path = './cmd/' + name
    raw = capture(['go', 'list', '-deps', '-json', package_path])
    list_log = output / (name + '-go-list-deps.json')
    list_log.write_text(raw)
    inputs, modules, local_packages = set(), {}, []
    for package in decode_stream(raw):
        module = package.get('Module')
        if module:
            modules[module['Path']] = {key: module[key] for key in ('Version', 'Sum', 'GoModSum', 'Replace') if key in module}
        directory = Path(package.get('Dir', '/'))
        if not directory.is_relative_to(root):
            continue
        local_packages.append(package['ImportPath'])
        for group in ('GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'HFiles', 'SFiles', 'EmbedFiles'):
            inputs.update(directory / path for path in package.get(group, []))
    inputs.update(root / name for name in ('Platform/go.mod', 'Platform/go.sum', 'DataTransfer/go.mod', 'DataTransfer/go.sum'))
    all_inputs |= inputs
    manifests[name] = {'command': ['go', 'build', '-o', str(output / name), package_path],
                      'build_inputs': {str(path.relative_to(root)): {'sha256': sha(path), 'bytes': path.stat().st_size} for path in sorted(inputs)},
                      'local_packages': local_packages, 'dependency_modules': modules,
                      'go_list_log': {'path': str(list_log), 'sha256': sha(list_log)}}
archive = output / 'source.tar.gz'
with tarfile.open(archive, 'w:gz') as tar:
    for path in sorted(all_inputs):
        tar.add(path, arcname=str(path.relative_to(root)))
for name, value in manifests.items():
    with open('/private/tmp/storage-t02-build-' + name + '.log', 'w') as log:
        result = subprocess.run(value['command'], env=env, cwd=root / 'Platform', stdout=log, stderr=subprocess.STDOUT)
    if result.returncode:
        raise SystemExit(result.returncode)
    value['exit_code'] = result.returncode
    value['changed_during_build'] = [name for name, record in value['build_inputs'].items() if sha(root / name) != record['sha256']]
    assert not value['changed_during_build'], value['changed_during_build']
    binary = output / name
    value['binary'] = {'path': str(binary), 'sha256': sha(binary), 'bytes': binary.stat().st_size}
    value['build_info'] = capture(['go', 'version', '-m', str(binary)])
    value['source_archive'] = {'path': str(archive), 'sha256': sha(archive), 'bytes': archive.stat().st_size}
    help_result = subprocess.run([str(binary), '-help'], env=env, text=True, capture_output=True)
    help_path = output / (name + '-help.txt')
    help_path.write_text(help_result.stdout + help_result.stderr)
    value['help'] = {'exit_code': help_result.returncode, 'path': str(help_path), 'sha256': sha(help_path)}
    assert help_result.returncode == 0
manifest = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'cwd': str(root / 'Platform'),
            'environment': {key: env[key] for key in ('GOTOOLCHAIN', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOPROXY')},
            'go_version': capture(['go', 'version']).strip(),
            'go_environment': json.loads(capture(['go', 'env', '-json', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOTOOLCHAIN'])),
            'combined_input_count': len(all_inputs), 'binaries': manifests,
            'restore_launch': {'argv_template': [str(output / 'sf-cloud'), '-database', '<isolated-copy>/history.db', '-master-key', '<isolated-copy>/master.key', '-listen', '127.0.0.1:<QA-port>', '-node-id', 'history-fixture', '-static', str(root / 'Frontends/dist')],
                               'source': 'Platform/internal/app/app.go:338 and Platform/cmd/sf-history-fixture/main.go:64',
                               'notes': 'Copy the stopped SQLite fixture including any WAL/SHM files and its master.key into an isolated directory; preserve the original. Keep node-id history-fixture for this fixture. Existing private-credentials.json authenticates the retained admin. The fixture generator requires a new directory; sf-cloud opens an existing database. No production or fixture entrypoint changes.'}}
(output / 'build-inputs.json').write_text(json.dumps(manifest, indent=2) + '\n')
(evidence / 'current-binaries-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
print(json.dumps({'manifest': str(output / 'build-inputs.json'), 'combined_input_count': len(all_inputs), 'binaries': {name: {'binary': value['binary'], 'input_count': len(value['build_inputs']), 'source_archive': value['source_archive']} for name, value in manifests.items()}}, indent=2))
