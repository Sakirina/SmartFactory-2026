import collections
import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
BASE = Path('/private/tmp/smartfactory-delivery-final-20261007')
BUNDLE = BASE / 'bundle'
RUNTIME = Path('/private/tmp/smartfactory-delivery-final-runtime-20261007/verification.json')
cache, references, findings = {}, [], []


def digest(path):
    path = Path(path).resolve()
    if path not in cache:
        content = path.read_bytes()
        cache[path] = (hashlib.sha256(content).hexdigest(), len(content))
    return cache[path]


def verify(path, row, scope):
    path = Path(path).resolve()
    if not path.is_file():
        findings.append({'path': str(path), 'scope': scope, 'error': 'missing'})
        return
    sha, size = digest(path)
    references.append({'path': str(path), 'sha256': sha, 'bytes': size, 'scope': scope})
    if sha != row['sha256'] or any(isinstance(row.get(k), int) and row[k] != size for k in ('bytes', 'size')):
        findings.append({'path': str(path), 'scope': scope, 'error': 'digest_or_size', 'expected': row['sha256'], 'actual': sha})


def walk(obj, scope):
    if isinstance(obj, dict):
        sha = obj.get('sha256')
        if isinstance(sha, str) and len(sha) == 64:
            for field in ('path', 'log', 'response_file'):
                value = obj.get(field)
                if isinstance(value, str) and value.startswith('/') and not value.startswith('/api/'):
                    verify(value, obj, scope)
                    break
        for key, value in obj.items():
            walk(value, scope + '.' + key)
    elif isinstance(obj, list):
        for i, value in enumerate(obj):
            walk(value, scope + '[' + str(i) + ']')


def load(path):
    path = Path(path)
    value = json.loads(path.read_text())
    sha, size = digest(path)
    references.append({'path': str(path), 'sha256': sha, 'bytes': size, 'scope': 'report'})
    return value


static = load(BASE / 'static-verification-license-complete/verification.json')
runtime = load(RUNTIME)
manifest = load(BUNDLE / 'release-manifest.json')
source = load(BUNDLE / 'source-manifest.json')
build = load(BUNDLE / 'build-inputs.json')
retained = load(BASE / 'retained-final-build-inputs.json')
contracts = load(BASE / 'final-contract-export/verification.json')
walk(static, 'static')
walk(runtime, 'runtime')
walk(contracts, 'contracts')
walk(retained, 'retained')
assert static['passed'] and all(v['passed'] for v in static['checks'].values())
assert all(c['exit_code'] == 0 for c in static['commands'])
assert runtime['status'] == 'passed' and runtime['all_checks_passed']
assert len(runtime['checks']) == 55 and all(c['passed'] for c in runtime['checks'])
assert digest(BUNDLE / 'release-manifest.json')[0] == runtime['bundle_manifest_sha256']
assert build['inputs_before'] == build['inputs_after'] and len(build['inputs_before']) == 6094
assert len(retained['files']) == 6094 and len(retained['additional_toolchain_files']) == 7
assert len(manifest['files']) == 1533 and len(source['files']) == 894
for name, row in manifest['files'].items():
    verify(BUNDLE / name, row, 'release-manifest')
for name, row in source['files'].items():
    verify(BUNDLE / 'source' / name, row, 'public-source')
    verify(ROOT / name, row, 'current-public-source')
source_sha = hashlib.sha256(json.dumps(source['files'], sort_keys=True, separators=(',', ':')).encode()).hexdigest()
assert source_sha == source['sha256'] == manifest['source_sha256'] == build['source_sha256'] == retained['source_sha256']
for row in retained['files'] + retained['additional_toolchain_files']:
    verify(row['original_path'], row, 'actual-build-input')
    verify(row['fixed_path'], row, 'retained-build-input')
assert build['binaries'] == retained['binaries'] and len(build['binaries']) == 14
for name, row in build['binaries'].items():
    verify(BUNDLE / 'bin' / name, row, 'compiled-program')
for row in static['checks']['frontend']['frozen_source_rows']:
    verify(row['fixed_input'], row, 'frontend-frozen-source')
assert static['checks']['frontend']['included_source_files'] == 291
assert len(static['checks']['frontend']['excluded_source_files']) == 4
assert static['checks']['frontend']['static_files'] == 19
nonzero = [{'name': c['name'], 'exit_code': c['exit_code']} for c in runtime['commands'] if c['exit_code']]
assert {c['name'] for c in nonzero} == {'gateway-loopback-readiness-16', 'managed-runtime-loopback-160', 'managed-runtime-loopback-163'}
assert all(c['exit_code'] == 1 for c in nonzero)
assert all(not c.get('timed_out') for c in runtime['commands'])
assert len(runtime['final_states']) == 8
assert all(s['Status'] == 'exited' and s['ExitCode'] == 0 and not s['OOMKilled'] for s in runtime['final_states'].values())
metadata_requests = [v for c in runtime['deployment_metadata_checkpoints'] for v in c['requests'].values()]
assert len(metadata_requests) == 32 and all(v['status'] == 200 for v in metadata_requests)
build_info = runtime['checks'][0]['detail']['build']
assert build_info['source_sha256'] == source_sha and build_info['goos'] == 'linux' and build_info['goarch'] == 'amd64'
assert runtime['checks'][0]['detail']['sha256'] == build['binaries']['sf-edge']['sha256']
assert runtime['driver']['sha256'] == digest(BUNDLE / 'scripts/verify-delivery-runtime.py')[0]
http_status = dict(collections.Counter(str(r.get('status', 'network_readiness')) for r in runtime['http']))
report = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'passed' if not findings else 'needs_review',
    'scope': 'Coordinator review of the exact final package, fixed build inputs, specialist raw commands, final Linux runtime and source correspondence; independent behavioral acceptance is a separate report',
    'source_sha256': source_sha,
    'manifest_files': len(manifest['files']), 'public_source_files': len(source['files']),
    'portable_build_inputs': len(retained['files']), 'toolchain_files': len(retained['additional_toolchain_files']),
    'programs': len(build['binaries']), 'frontend_frozen_inputs': 295, 'frontend_public_inputs': 291, 'frontend_static_files': 19,
    'static_commands': len(static['commands']), 'runtime_checks_passed': len(runtime['checks']),
    'runtime_commands': len(runtime['commands']), 'expected_readiness_nonzero': nonzero,
    'runtime_http_status': http_status, 'configuration_metadata_requests_passed': len(metadata_requests),
    'all_eight_services_stopped_without_oom': True,
    'own_database_connections_zero': next(c['passed'] for c in runtime['checks'] if c['name'] == 'all-own-database-connections-released'),
    'reference_count': len(references), 'unique_files': len(cache), 'references': references, 'findings': findings,
}
out = ROOT / '.local/evolution/phase5-final-package-root-review.json'
out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({k: v for k, v in report.items() if k != 'references'}, ensure_ascii=False))
raise SystemExit(0 if not findings else 1)
