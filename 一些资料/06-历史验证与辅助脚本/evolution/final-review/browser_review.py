#!/usr/bin/env python3
"""Preserve independent frontend inputs and run isolated, recorded verification."""
import datetime
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import time

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
BASE = pathlib.Path('/private/tmp/smartfactory-final-independent-20261007/browser-runtime')
WS = pathlib.Path('/private/tmp/smartfactory-final-independent-20261007/frontend')
OUT = pathlib.Path('/private/tmp/smartfactory-final-independent-20261007/browser-results')
NODE = ROOT / '.local/toolchains/node-v24.21.0-darwin-arm64/bin/node'
NPM = NODE.parent / 'npm'

def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()

def ref(path):
    path = pathlib.Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def write(path, value):
    pathlib.Path(path).write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')

def stamp():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()

def environment():
    result = {key: value for key, value in os.environ.items() if not key.startswith(('SF_', 'DT_', 'OTEL_'))}
    result.update({'PATH': str(NODE.parent) + ':' + os.environ['PATH'], 'CI': '1', 'npm_config_cache': '/private/tmp/smartfactory-phase4-independent-npm-cache', 'PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD': '1'})
    return result

def run(name, command, extra=None, timeout=600):
    OUT.mkdir(parents=True, exist_ok=True)
    metadata_path = OUT / (name + '.run.json')
    if metadata_path.exists():
        raise RuntimeError('Run evidence already exists: ' + name)
    stdout = OUT / (name + '.stdout.log')
    stderr = OUT / (name + '.stderr.log')
    env = environment()
    if extra:
        env.update(extra)
    driver_copy = OUT / 'retained-drivers' / (sha(__file__) + '.py')
    driver_copy.parent.mkdir(exist_ok=True)
    if not driver_copy.exists(): shutil.copy2(__file__, driver_copy)
    selected_inputs = []
    retention = OUT / 'test-inputs'
    retention.mkdir(exist_ok=True)
    selected_test_files = {WS / 'Frontends/tests' / part for part in command if part.endswith('.spec.ts')}
    queue = list(selected_test_files)
    while queue:
        candidate = queue.pop()
        for specifier in re.findall(r'''(?:from\s*|import\s*\()\s*['"](\.[^'"]+)['"]''', candidate.read_text()):
            child = candidate.parent / specifier
            resolved = next((item.resolve() for item in [child, child.with_suffix('.ts'), child.with_suffix('.tsx'), child / 'index.ts'] if item.is_file()), None)
            if resolved and resolved not in selected_test_files:
                selected_test_files.add(resolved); queue.append(resolved)
    for path in sorted((WS / 'Frontends').rglob('*')):
        if not path.is_file() or any(part in {'node_modules', 'dist', 'test-results', 'playwright-report'} for part in path.relative_to(WS / 'Frontends').parts): continue
        if path.relative_to(WS / 'Frontends').parts[0] == 'tests' and path.resolve() not in selected_test_files: continue
        digest = sha(path); stored = retention / digest
        if not stored.exists(): shutil.copy2(path, stored)
        selected_inputs.append(dict(ref(path), retained_copy=str(stored)))
    input_index = OUT / (name + '.inputs.json')
    write(input_index, selected_inputs)
    meta = {'input_index': ref(input_index), 'name': name, 'started_at': stamp(), 'command': command, 'cwd': str(WS / 'Frontends'), 'node': ref(NODE), 'environment': {key: value for key, value in env.items() if key.startswith(('SF_PHASE4_', 'SF_PHASE5_', 'SF_BROWSER_', 'PLAYWRIGHT_')) or key in ('CI', 'npm_config_cache')}, 'driver': ref(driver_copy), 'original_driver_path': str(pathlib.Path(__file__).resolve()), 'stdout_path': str(stdout), 'stderr_path': str(stderr)}
    write(metadata_path, meta)
    start = time.monotonic()
    with stdout.open('w') as out, stderr.open('w') as err:
        try:
            result = subprocess.run(command, cwd=WS / 'Frontends', env=env, stdout=out, stderr=err, timeout=timeout)
            meta['exit_code'] = result.returncode
        except subprocess.TimeoutExpired:
            meta['exit_code'] = -1
            meta['timeout_seconds'] = timeout
    meta.update({'completed_at': stamp(), 'elapsed_seconds': time.monotonic() - start, 'stdout': ref(stdout), 'stderr': ref(stderr)})
    meta['test_inputs_unchanged'] = all(sha(row['path']) == row['sha256'] for row in selected_inputs)
    write(metadata_path, meta)
    print(json.dumps({'name': name, 'exit_code': meta['exit_code'], 'elapsed_seconds': meta['elapsed_seconds'], 'metadata': str(metadata_path)}), flush=True)
    return meta['exit_code']

def snapshot(source):
    if WS.exists():
        raise RuntimeError('Independent frontend snapshot already exists')
    OUT.mkdir(parents=True, exist_ok=True)
    WS.mkdir(parents=True)
    rows = []
    for subtree in ['Frontends', 'contracts', 'deploy/toolchains.json']:
        original = source / subtree
        files = sorted(original.rglob('*')) if original.is_dir() else [original]
        for path in files:
            relative = path.relative_to(source)
            if not path.is_file() or any(part in {'node_modules', 'dist', 'test-results', 'playwright-report', '.git', '.DS_Store'} or part.startswith('._') for part in relative.parts):
                continue
            destination = WS / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, destination)
            rows.append({'relative': str(relative), 'original': str(path), 'path': str(destination), 'sha256': sha(path), 'bytes': path.stat().st_size})
    write(OUT / 'source-snapshot.json', {'created_at': stamp(), 'source': str(source), 'workspace': str(WS), 'file_count': len(rows), 'files': rows, 'node': ref(NODE), 'npm': ref(NPM.resolve())})
    print(json.dumps({'snapshot': str(WS), 'file_count': len(rows)}))

def build():
    for name, command in [('npm-ci', [str(NPM), 'ci', '--ignore-scripts', '--no-audit', '--no-fund']), ('unit', [str(NPM), 'run', 'test:unit']), ('build', [str(NPM), 'run', 'build']), ('typecheck-compat', [str(NPM), 'run', 'typecheck:compat']), ('npm-audit', [str(NPM), 'audit', '--json'])]:
        if run(name, command):
            raise SystemExit(1)
    for label, directory in [('installed-dependencies', WS / 'Frontends/node_modules'), ('production-assets', WS / 'Frontends/dist')]:
        rows = []
        for path in sorted(directory.rglob('*')):
            if path.is_file():
                item = ref(path)
                item['relative'] = str(path.relative_to(directory))
                rows.append(item)
        write(OUT / (label + '.json'), {'created_at': stamp(), 'root': str(directory), 'files': rows, 'file_count': len(rows)})

def browser(phase, attempt):
    mode = 'precision' if phase in ['precision', 'surfaces', 'extra'] else 'final'
    report = OUT / ('browser-' + phase + '-' + attempt)
    report.mkdir(parents=True, exist_ok=False)
    evidence = BASE / ('browser-business-final' if phase == 'business' and attempt == 'first' else 'browser-' + phase + '-' + attempt)
    env = {'SF_PHASE4_RUNTIME': str(BASE), 'SF_PHASE4_EVIDENCE': str(evidence), 'SF_PHASE4_CLOUD': 'http://127.0.0.1:' + ('19713' if mode == 'precision' else '19710'), 'SF_PHASE4_EDGE': 'http://127.0.0.1:' + ('19714' if mode == 'precision' else '19711'), 'SF_PHASE4_AI': 'http://127.0.0.1:19712', 'SF_PHASE4_PRECISION_CLOUD': 'http://127.0.0.1:19713', 'SF_PHASE4_PRECISION_EDGE': 'http://127.0.0.1:19714', 'SF_PHASE4_CASES': phase, 'SF_PHASE4_AI_DIR': 'ai-final', 'SF_PHASE4_DATA_SUFFIX': mode, 'SF_PHASE4_REPORT_DIR': str(report), 'SF_PHASE4_SURFACE_USER': 'admin' if mode == 'precision' else 'operator-a'}
    script = {'business': 'phase4.spec.ts', 'details': 'phase4-business-details.spec.ts', 'state': 'phase4-state.spec.ts', 'precision': 'phase4-precision.spec.ts', 'surfaces': 'phase4.spec.ts', 'ai': 'phase4-ai.spec.ts', 'extra': 'phase4-independent-extra.spec.ts'}[phase]
    command = [str(NODE), str(WS / 'Frontends/node_modules/@playwright/test/cli.js'), 'test', '-c', 'playwright.phase4.config.ts', script]
    if os.environ.get('SF_FINAL_GREP'): command += ['--grep', os.environ['SF_FINAL_GREP']]
    status = run('browser-' + phase + '-' + attempt, command, env, timeout=900)
    diagnostic = BASE / 'playwright-results'
    if diagnostic.exists():
        shutil.copytree(diagnostic, BASE / ('playwright-results-' + phase + '-' + attempt))
    raise SystemExit(status)

if __name__ == '__main__':
    if sys.argv[1] == 'snapshot':
        snapshot(pathlib.Path(sys.argv[2]).resolve())
    elif sys.argv[1] == 'build':
        build()
    elif sys.argv[1] == 'browser':
        browser(sys.argv[2], sys.argv[3])
