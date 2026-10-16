#!/usr/bin/env python3
"""Read-only Go source selection after coordinator authorization."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path('/path/to/SmartFactory-2026')
OLD = Path('/private/tmp/smartfactory-delivery-regressions-current-20261005')
OUT = Path('/private/tmp/smartfactory-final-independent-20261007/go-selection')
OUT.mkdir(parents=True, exist_ok=True)
REPORT = json.loads((OLD / 'verification.json').read_text())
GO = '/opt/homebrew/Cellar/go/1.27.1/libexec/bin/go'

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def ref(path):
    path = Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def objects(path):
    data = Path(path).read_text()
    decoder = json.JSONDecoder()
    offset = 0
    result = []
    while offset < len(data):
        if data[offset].isspace():
            offset += 1
            continue
        item, offset = decoder.raw_decode(data, offset)
        result.append(item)
    return result

def selection(packages):
    paths = set()
    errors = []
    for package in packages:
        if package.get('Error') or package.get('DepsErrors'):
            errors.append({k: package[k] for k in ['ImportPath', 'Error', 'DepsErrors'] if k in package})
        directory = Path(package.get('Dir', ''))
        for field in REPORT['input_fields']:
            for name in package.get(field, []):
                paths.add(str(directory / name))
    return paths, errors

env = {k: v for k, v in os.environ.items() if not k.startswith(('SF_', 'DT_', 'OTEL_'))}
for key in ['CGO_ENABLED', 'GOARCH', 'GOCACHE', 'GOFLAGS', 'GOMODCACHE', 'GOOS', 'GOPATH', 'GOPROXY', 'GOROOT', 'GOTOOLCHAIN', 'GOWORK']:
    env[key] = REPORT['toolchain_environment'][key]
env['GOMAXPROCS'] = '2'
results = []
for module in ['DataTransfer', 'Platform']:
    label = module.lower()
    stdout = OUT / (label + '.go-list.json-stream')
    stderr = OUT / (label + '.go-list.stderr.log')
    metadata = OUT / (label + '.metadata.json')
    if metadata.exists():
        raise RuntimeError('Enumeration evidence already exists: ' + str(metadata))
    command = [GO, 'list', '-deps', '-test', '-json', './...']
    start = time.monotonic()
    with stdout.open('w') as out, stderr.open('w') as err:
        run = subprocess.run(command, cwd=ROOT / module, env=env, stdout=out, stderr=err)
    current = objects(stdout) if run.returncode == 0 else []
    old_command = next(x for x in REPORT['commands'] if x['name'] == label + '-tests-before-go-list')
    previous = objects(old_command['stdout'])
    selected, selection_errors = selection(current)
    old_selected, old_errors = selection(previous)
    manifest = OLD / (label + '-tests-before.inputs.json')
    expected = {row['path']: row for row in json.loads(manifest.read_text())}
    selected_rows = []
    for path in sorted(selected):
        exists = Path(path).is_file()
        selected_rows.append({'path': path, 'exists': exists, 'sha256': sha(path) if exists else None,
            'previous_manifest_sha256': expected.get(path, {}).get('sha256')})
    result = {
        'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
        'module': module, 'argv': command, 'cwd': str(ROOT / module),
        'exit_code': run.returncode, 'duration_seconds': time.monotonic() - start,
        'environment': {k: env[k] for k in ['CGO_ENABLED', 'GOARCH', 'GOCACHE', 'GOFLAGS', 'GOMODCACHE', 'GOOS', 'GOPATH', 'GOPROXY', 'GOROOT', 'GOTOOLCHAIN', 'GOWORK', 'GOMAXPROCS']},
        'toolchain': ref(GO), 'driver': ref(__file__), 'stdout': ref(stdout), 'stderr': ref(stderr),
        'previous_go_list': ref(old_command['stdout']), 'previous_manifest': ref(manifest),
        'current_package_count': len(current), 'previous_package_count': len(previous),
        'current_selected_count': len(selected), 'previous_selected_count': len(old_selected),
        'selected_path_sets_equal': selected == old_selected,
        'added_paths': sorted(selected - old_selected), 'removed_paths': sorted(old_selected - selected),
        'current_selection_errors': selection_errors, 'previous_selection_errors': old_errors,
        'selected_inputs': selected_rows,
        'inputs_not_in_previous_manifest': [r for r in selected_rows if r['previous_manifest_sha256'] is None],
        'changed_inputs': [r for r in selected_rows if r['sha256'] != r['previous_manifest_sha256']],
        'missing_current_inputs': [r for r in selected_rows if not r['exists']],
        'product_tests_builds_services_or_browser_executed': False,
    }
    metadata.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    results.append(ref(metadata))
    print(json.dumps({k: result[k] for k in ['module', 'exit_code', 'current_package_count', 'previous_package_count', 'current_selected_count', 'previous_selected_count', 'selected_path_sets_equal', 'added_paths', 'removed_paths', 'changed_inputs', 'current_selection_errors']}, ensure_ascii=False), flush=True)
    if run.returncode or selection_errors or result['missing_current_inputs']:
        raise SystemExit(1)
(OUT / 'summary.json').write_text(json.dumps({'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'results': results}, indent=2) + '\n')
