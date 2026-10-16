import datetime
import hashlib
import json
import os
import pathlib
import subprocess
import time

repo = pathlib.Path('/path/to/SmartFactory-2026')
evidence = repo / '.local/evolution/business-evidence'
root = pathlib.Path((evidence / 'final-snapshot-path.txt').read_text().strip())
bins = root / 'binaries'
bins.mkdir(exist_ok=True)
report_path = evidence / 'final-v3-verification-runs.json'
inputs = evidence / 'final-v3-actual-inputs.json'
env = dict(os.environ, GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath',
           GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache', GOCACHE='/private/tmp/smartfactory-evolution-go-cache',
           GOPROXY='off', GOMAXPROCS='2')
env.pop('SF_TEST_POSTGRES_DATABASE', None)
report = {'snapshot': str(root), 'input_manifest': str(inputs),
          'input_manifest_sha256': hashlib.sha256(inputs.read_bytes()).hexdigest(),
          'scope': 'P4-B06 explicit request required fields and optional defaults; SQLite application, HTTP, contracts and rebuilt apps',
          'runs': [], 'binaries': {}}


def save():
    report_path.write_text(json.dumps(report, indent=2) + '\n')


def run(name, args):
    log = evidence / (name + '.log')
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    clock = time.monotonic()
    with log.open('w') as output:
        result = subprocess.run(args, cwd=root / 'Platform', env=env, stdout=output, stderr=subprocess.STDOUT)
    item = {'name': name, 'command': args, 'cwd': str(root / 'Platform'), 'started_at': started,
            'completed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'elapsed_seconds': round(time.monotonic() - clock, 3), 'exit_code': result.returncode,
            'log': str(log), 'log_sha256': hashlib.sha256(log.read_bytes()).hexdigest()}
    for arg in args:
        path = pathlib.Path(arg)
        if path.is_file() and path.is_relative_to(bins):
            item['executed_binary_sha256'] = hashlib.sha256(path.read_bytes()).hexdigest()
    report['runs'].append(item)
    save()
    print(name, 'exit', result.returncode, 'elapsed', item['elapsed_seconds'], flush=True)
    if result.returncode:
        print(log.read_text()[-8000:], flush=True)
        raise SystemExit(result.returncode)


def bind(path):
    report['binaries'][str(path)] = {'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
                                  'bytes': path.stat().st_size,
                                  'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}
    save()


run('final-v3-build-tests', ['go', 'test', '-c', '-trimpath', '-p', '1', '-o', str(bins), './internal/application', './internal/api'])
for package, pattern in [('application', '^TestBusiness(Semantic.*|Template.*|HandoverReviewed.*|WorkflowSQLite|Connector.*|WorkOrder.*|ConcurrentAlarmSQLite|ConcurrentHandoverSQLite)$'),
                         ('api', '^TestBusinessHTTP')]:
    binary = bins / (package + '.test')
    bind(binary)
    run('final-v3-' + package, ['go', 'tool', 'test2json', '-t', '-p', 'competition2026/product/platform/internal/' + package,
                               str(binary), '-test.v=test2json', '-test.timeout=180s', '-test.run=' + pattern, '-test.count=1'])
for target in ['sf-cloud', 'sf-edge', 'sf-contracts', 'sf-business-fixture']:
    binary = bins / target
    run('final-v3-build-' + target, ['go', 'build', '-trimpath', '-p', '1', '-o', str(binary), './cmd/' + target])
    bind(binary)
run('final-v3-contract-generation', [str(bins / 'sf-contracts'), '-out', str(root / 'contracts/1.0'), '-examples', str(root / 'examples/factory/definitions.json')])
run('final-v3-contract-validation', ['/private/tmp/smartfactory-evolution-python/bin/python', str(root / 'scripts/check-contracts.py')])
report['all_checks_passed'] = True
save()
print('V3_VERIFICATION_COMPLETE', flush=True)
