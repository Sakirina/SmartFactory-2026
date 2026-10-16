import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile

root = Path('/path/to/SmartFactory-2026')
output = Path('/private/tmp/smartfactory-t01-current')
output.mkdir(exist_ok=True)
env = os.environ.copy()
env.update(GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath',
           GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
           GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOPROXY='off')


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(argv):
    return subprocess.check_output(argv, env=env, cwd=root / 'Platform', text=True)


raw = run(['go', 'list', '-deps', '-json', './cmd/sf-history-fixture'])
(output / 'go-list-deps.json').write_text(raw)
decoder = json.JSONDecoder()
packages = []
offset = 0
while offset < len(raw):
    while offset < len(raw) and raw[offset].isspace():
        offset += 1
    if offset == len(raw):
        break
    value, offset = decoder.raw_decode(raw, offset)
    packages.append(value)
inputs = set()
modules = {}
local_packages = []
for package in packages:
    module = package.get('Module')
    if module:
        modules[module['Path']] = {key: module[key] for key in ('Version', 'Sum', 'GoModSum', 'Replace') if key in module}
    directory = Path(package.get('Dir', '/'))
    if not directory.is_relative_to(root):
        continue
    local_packages.append(package['ImportPath'])
    for group in ('GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'HFiles', 'SFiles', 'EmbedFiles'):
        inputs.update(directory / name for name in package.get(group, []))
for name in ['Platform/go.mod', 'Platform/go.sum', 'DataTransfer/go.mod', 'DataTransfer/go.sum']:
    inputs.add(root / name)
before = {str(path.relative_to(root)): {'sha256': sha(path), 'bytes': path.stat().st_size} for path in sorted(inputs)}
archive = output / 'source.tar.gz'
with tarfile.open(archive, 'w:gz') as tar:
    for path in sorted(inputs):
        tar.add(path, arcname=str(path.relative_to(root)))
binary = output / 'sf-history-fixture'
command = ['go', 'build', '-o', str(binary), './cmd/sf-history-fixture']
with open('/private/tmp/storage-t01-current-build.log', 'w') as log:
    result = subprocess.run(command, cwd=root / 'Platform', env=env, stdout=log, stderr=subprocess.STDOUT)
if result.returncode:
    raise SystemExit(result.returncode)
changed = [name for name, info in before.items() if sha(root / name) != info['sha256']]
assert not changed, changed
manifest = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'command': command, 'cwd': str(root / 'Platform'),
            'environment': {key: env[key] for key in ['GOTOOLCHAIN', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOPROXY']},
            'exit_code': result.returncode, 'go_version': run(['go', 'version']).strip(),
            'go_environment': json.loads(run(['go', 'env', '-json', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOTOOLCHAIN'])),
            'binary': {'path': str(binary), 'sha256': sha(binary), 'bytes': binary.stat().st_size},
            'build_info': run(['go', 'version', '-m', str(binary)]),
            'source_archive': {'path': str(archive), 'sha256': sha(archive), 'bytes': archive.stat().st_size},
            'build_inputs': before, 'local_packages': local_packages, 'dependency_modules': modules,
            'go_list_log': {'path': str(output / 'go-list-deps.json'), 'sha256': sha(output / 'go-list-deps.json')},
            'changed_during_build': changed}
(output / 'build-inputs.json').write_text(json.dumps(manifest, indent=2) + '\n')
(root / '.local/evolution/tasks-cancel-t01-evidence/current-binary-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
print(json.dumps({'binary': manifest['binary'], 'source_archive': manifest['source_archive'],
                  'input_count': len(before), 'local_package_count': len(local_packages),
                  'manifest': str(output / 'build-inputs.json'), 'changed_during_build': changed}, indent=2))
