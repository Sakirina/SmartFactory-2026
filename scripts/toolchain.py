"""Read and verify SmartFactory's selected build toolchain without installing tools."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def load_manifest(root=ROOT):
    return json.loads((root / 'deploy/toolchains.json').read_text())


def configuration_errors(root=ROOT):
    manifest = load_manifest(root)
    errors = []
    if manifest.get('schema_version') != 1:
        return ['unsupported toolchain manifest schema']
    go = manifest['tools']['go']
    for name in manifest['go_modules']:
        source = (root / name).read_text()
        for field, expected in [('go', go['minimum']), ('toolchain', go['toolchain'])]:
            match = re.search(r'^' + field + r'\s+(\S+)\s*$', source, re.MULTILINE)
            if not match or match.group(1) != expected:
                errors.append(f'{name}: {field} must be {expected}')
    for tool in ['go', 'node']:
        item = manifest['tools'][tool]
        if (root / item['version_file']).read_text().strip() != item['locked']:
            errors.append(f'{item["version_file"]}: differs from selected {tool}')
    dependencies = json.loads((root / 'deploy/dependencies.json').read_text())
    lock = json.loads((root / 'deploy/images.lock.json').read_text())
    if lock.get('errors'):
        errors.append('deploy/images.lock.json: registry resolution has errors')
    if set(dependencies['images']) != set(lock['images']):
        errors.append('image lock and dependencies select different components')
    unique = {}
    for name, reference in dependencies['images'].items():
        image = lock['images'].get(name, {})
        if image.get('reference') != reference:
            errors.append(f'{name}: image lock reference differs from dependencies')
        if manifest['runtime'].get(name, {}).get('locked') != reference:
            errors.append(f'{name}: locked reference differs from toolchain manifest')
        if image.get('platform') != dependencies['platform']:
            errors.append(f'{name}: platform differs from selected deployment platform')
        if not re.fullmatch(r'[^@]+@sha256:[0-9a-f]{64}', image.get('pinned', '')):
            errors.append(f'{name}: image is not pinned to a SHA-256 manifest')
        layers = image.get('layers', [])
        if sum(item.get('bytes', 0) for item in layers) != image.get('compressed_bytes'):
            errors.append(f'{name}: layer sizes differ from compressed_bytes')
        for layer in layers:
            previous = unique.get(layer['digest'])
            if previous is not None and previous != layer['bytes']:
                errors.append(f'{name}: repeated layer has conflicting byte counts')
            unique[layer['digest']] = layer['bytes']
    if sum(unique.values()) != lock.get('unique_compressed_bytes'):
        errors.append('image lock unique_compressed_bytes is inconsistent')
    for builder, tool in [('go_builder', 'go'), ('node_builder', 'node')]:
        prefix = 'golang:' if tool == 'go' else 'node:'
        if not dependencies['images'].get(builder, '').startswith(prefix + manifest['tools'][tool]['locked'] + '-'):
            errors.append(f'{builder}: builder does not select the same {tool} patch version')
    allowed_images = {item['pinned'] for item in lock['images'].values()}
    for compose in ['deploy/compose.native.json', 'deploy/compose.site.json']:
        content = json.loads((root / compose).read_text())
        for name, service in content.get('services', {}).items():
            if service.get('image') and service['image'] not in allowed_images:
                errors.append(f'{compose} {name}: image is absent from current lock')
    frontend = json.loads((root / 'Frontends/package.json').read_text())
    selected = {**frontend.get('dependencies', {}), **frontend.get('devDependencies', {})}
    if selected != manifest['frontend_dependencies']:
        errors.append('frontend dependencies differ from centralized locked versions')
    npm_lock = json.loads((root / 'Frontends/package-lock.json').read_text())
    for name, version in selected.items():
        entry = npm_lock.get('packages', {}).get('node_modules/' + name, {})
        alias = re.fullmatch(r'npm:(.+)@(\d+\.\d+\.\d+)', version)
        expected_version = alias.group(2) if alias else version
        if alias and entry.get('name') != alias.group(1):
            errors.append(f'frontend alias target differs for {name}')
        if entry.get('version') != expected_version:
            errors.append(f'frontend lock differs for {name}')
    declarations = npm_lock.get('packages', {}).get('', {})
    if {**declarations.get('dependencies', {}), **declarations.get('devDependencies', {})} != selected:
        errors.append('frontend lock declarations differ from selected dependencies')
    workflow = (root / '.github/workflows/build-release.yml').read_text()
    for tool in ['go', 'node']:
        field = 'go-version-file' if tool == 'go' else 'node-version-file'
        if f'{field}: {manifest["tools"][tool]["version_file"]}' not in workflow:
            errors.append(f'CI {tool} version must use the centralized version file')
    return errors


def environment(root=ROOT, base=None):
    """Select a downloaded workspace Node runtime and prohibit implicit Go toolchain downloads."""
    result = dict(os.environ if base is None else base)
    result['GOTOOLCHAIN'] = 'local'
    version = load_manifest(root)['tools']['node']['locked']
    system = {'Darwin': 'darwin', 'Linux': 'linux', 'Windows': 'win'}.get(platform.system())
    machine = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'x64', 'AMD64': 'x64'}.get(platform.machine())
    if system and machine:
        runtime = root / '.local/toolchains' / f'node-v{version}-{system}-{machine}'
        binary = runtime if system == 'win' else runtime / 'bin'
        if binary.is_dir():
            result['PATH'] = str(binary) + os.pathsep + result.get('PATH', '')
    return result


def _version(command, env):
    result = subprocess.run(command, env=env, capture_output=True, text=True, check=True)
    return result.stdout.strip()


def verify_tools(root=ROOT, env=None):
    selected = load_manifest(root)['tools']
    env = environment(root, env)
    observed = {}
    errors = []
    for tool, command, pattern in [
        ('go', ['go', 'version'], r'\bgo(\d+\.\d+\.\d+)\b'),
        ('node', ['node', '--version'], r'^v(\d+\.\d+\.\d+)$'),
    ]:
        try:
            value = _version(command, env)
            match = re.search(pattern, value)
            observed[tool] = match.group(1) if match else value
            if observed[tool] != selected[tool]['locked']:
                errors.append(f'{tool}: installed {observed[tool]}, selected {selected[tool]["locked"]}')
        except (OSError, subprocess.CalledProcessError) as exc:
            errors.append(f'{tool}: unavailable ({exc})')
    for tool, command in [('npm', ['npm', '--version']), ('python', [shutil.which('python3') or 'python3', '--version'])]:
        try:
            observed[tool] = _version(command, env)
        except (OSError, subprocess.CalledProcessError) as exc:
            errors.append(f'{tool}: unavailable ({exc})')
    if 'python' in observed:
        match = re.search(r'(\d+)\.(\d+)', observed['python'])
        minimum = tuple(map(int, selected['python']['minimum'].split('.')))
        if not match or tuple(map(int, match.groups())) < minimum:
            errors.append(f'python: requires >= {selected["python"]["minimum"]}')
    return observed, errors


def preflight(root=ROOT, env=None):
    """Return build evidence or raise before producing a release with mismatched tools."""
    errors = configuration_errors(root)
    observed, tool_errors = verify_tools(root, env)
    errors.extend(tool_errors)
    if errors:
        raise RuntimeError('\n'.join(errors))
    return {'schema_version': 1, 'selected': load_manifest(root)['tools'], 'observed': observed}
