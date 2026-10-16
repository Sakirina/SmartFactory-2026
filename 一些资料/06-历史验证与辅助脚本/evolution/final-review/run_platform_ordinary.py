#!/usr/bin/env python3
"""Final ordinary Platform tests, with no external service variables inherited."""
from collections import Counter
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import traceback

ROOT = Path('/path/to/SmartFactory-2026')
OLD = Path('/private/tmp/smartfactory-delivery-regressions-current-20261005')
OUT = Path('/private/tmp/smartfactory-final-independent-20261007/platform-ordinary')
GO = '/opt/homebrew/Cellar/go/1.27.1/libexec/bin/go'
FIELDS = ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles', 'TestGoFiles', 'XTestGoFiles', 'TestEmbedFiles', 'XTestEmbedFiles']
OUT.mkdir(parents=True, exist_ok=False)
(OUT / 'fixed-inputs').mkdir()
env = {k: v for k, v in os.environ.items() if not k.startswith(('SF_', 'DT_', 'OTEL_'))}
env.update(GOTOOLCHAIN='local', GOPROXY='off', GOMAXPROCS='2', CGO_ENABLED='0', GOOS='darwin', GOARCH='arm64',
           GOPATH='/private/tmp/smartfactory-evolution-gopath', GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
           GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOFLAGS='', GOWORK='')

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def ref(path):
    path = Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()

report = {'started_at': now(), 'status': 'running', 'scope': 'Final complete Platform ordinary regression; PostgreSQL and external service prerequisites are deliberately absent in this coordinated window',
    'environment': {k: env[k] for k in ['GOTOOLCHAIN', 'GOPROXY', 'GOMAXPROCS', 'CGO_ENABLED', 'GOOS', 'GOARCH', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOFLAGS', 'GOWORK']},
    'external_test_variables': {k: v for k, v in env.items() if k.startswith(('SF_', 'DT_', 'OTEL_'))},
    'driver': ref(__file__), 'commands': [], 'input_sets': []}
shutil.copy2(__file__, OUT / 'driver.py')

def save():
    (OUT / 'verification.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')

def command(name, argv, required=True):
    start = time.monotonic()
    stdout, stderr = OUT / (name + '.stdout.log'), OUT / (name + '.stderr.log')
    with stdout.open('w') as out, stderr.open('w') as err:
        result = subprocess.run(argv, cwd=ROOT / 'Platform', env=env, stdout=out, stderr=err)
    item = {'name': name, 'argv': argv, 'cwd': str(ROOT / 'Platform'), 'exit_code': result.returncode,
        'duration_seconds': time.monotonic() - start, 'stdout': ref(stdout), 'stderr': ref(stderr)}
    report['commands'].append(item)
    save()
    if required and result.returncode:
        raise RuntimeError(name + ' failed; see retained logs')
    return item

def objects(path):
    raw = Path(path).read_text()
    decoder, offset, result = json.JSONDecoder(), 0, []
    while offset < len(raw):
        if raw[offset].isspace():
            offset += 1
            continue
        value, offset = decoder.raw_decode(raw, offset)
        result.append(value)
    return result

known = {}
for path in [OLD / 'platform-tests-before.inputs.json', OLD / 'datatransfer-tests-before.inputs.json',
             Path('/private/tmp/smartfactory-phase5-node-deadlock-verified-20261005/race-actual.inputs.json')]:
    for row in json.loads(path.read_text()):
        stored = row.get('retained_copy') or row.get('archive')
        if stored:
            known[row['sha256']] = Path(stored)
verified_retained = set()

def capture(label):
    listed = command(label + '-go-list', [GO, 'list', '-deps', '-test', '-json', './...'])
    packages = objects(listed['stdout']['path'])
    selected = {}
    for package in packages:
        if package.get('Error') or package.get('DepsErrors'):
            raise RuntimeError('Incomplete Go package enumeration')
        for field in FIELDS:
            for name in package.get(field, []):
                selected.setdefault((Path(package['Dir']) / name).resolve(), set()).add(field)
        module = package.get('Module', {})
        for item in [module, module.get('Replace', {})]:
            if item.get('GoMod'):
                selected.setdefault(Path(item['GoMod']).resolve(), set()).add('module_lock')
    for module in ['Platform', 'DataTransfer']:
        for name in ['go.mod', 'go.sum']:
            selected.setdefault(ROOT / module / name, set()).add('module_lock')
    rows = []
    for path, kinds in sorted(selected.items()):
        digest = sha(path)
        stored = known.get(digest)
        if stored and digest not in verified_retained:
            if not stored.is_file() or sha(stored) != digest:
                raise RuntimeError('Retained input changed: ' + str(stored))
            verified_retained.add(digest)
        if stored is None:
            stored = OUT / 'fixed-inputs' / digest
            shutil.copy2(path, stored)
            known[digest] = stored
        rows.append({'path': str(path), 'sha256': digest, 'bytes': path.stat().st_size, 'input_kinds': sorted(kinds), 'retained_copy': str(stored)})
    path = OUT / (label + '.inputs.json')
    path.write_text(json.dumps(rows, ensure_ascii=False, indent=2) + '\n')
    report['input_sets'].append({'name': label, 'package_count': len(packages), 'input_count': len(rows), 'index': ref(path)})
    save()
    return {r['path']: r['sha256'] for r in rows}

try:
    toolchain = command('toolchain', [GO, 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOTOOLCHAIN', 'GOROOT', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOPROXY', 'GOFLAGS', 'GOWORK', 'GOTOOLDIR'])
    report['toolchain_environment'] = json.loads(Path(toolchain['stdout']['path']).read_text())
    tool_directory = Path(report['toolchain_environment']['GOTOOLDIR'])
    report['compiler_tools'] = [ref(GO)] + [ref(tool_directory / name) for name in ['compile', 'link', 'asm', 'vet']]
    before = capture('before')
    test = command('platform-tests', [GO, 'test', '-count=1', '-p', '1', '-json', './...'], required=False)
    after = capture('after')
    counts, terminal = Counter(), []
    for line in Path(test['stdout']['path']).read_text().splitlines():
        event = json.loads(line)
        if event.get('Test') and event.get('Action') in ['pass', 'fail', 'skip']:
            counts[event['Action']] += 1
            terminal.append({k: event[k] for k in ['Package', 'Test', 'Action', 'Elapsed'] if k in event})
    report.update(counts_including_subtests=dict(counts), terminal_test_events=terminal,
        skipped_tests=[e for e in terminal if e['Action'] == 'skip'], failed_tests=[e for e in terminal if e['Action'] == 'fail'],
        inputs_unchanged=before == after, status='passed' if test['exit_code'] == 0 and before == after else 'failed')
except Exception as error:
    report.update(status='failed', error=str(error), traceback=traceback.format_exc())
finally:
    report['completed_at'] = now()
    save()
print(json.dumps({k: report[k] for k in ['status', 'counts_including_subtests', 'inputs_unchanged', 'error'] if k in report}, ensure_ascii=False), flush=True)
raise SystemExit(0 if report['status'] == 'passed' else 1)
