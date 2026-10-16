import datetime, hashlib, json, os, pathlib, shutil, subprocess, urllib.parse

repo = pathlib.Path('/path/to/SmartFactory-2026')
evidence = repo / '.local/evolution/query-evidence'
frozen = pathlib.Path((evidence / 'final-snapshot-path.txt').read_text().strip())
def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def now(): return datetime.datetime.now(datetime.timezone.utc).isoformat()

checks = {'checked_at': now()}
manifests = ['final-input-manifest-initial.json', 'final-input-manifest-v2.json']
checks['local_inputs'] = []
for name in manifests:
    path = evidence / name
    manifest = json.loads(path.read_text())
    base = pathlib.Path(manifest['snapshot'])
    mismatches = [label for label, value in manifest['inputs'].items() if value['local'] and sha(base / label) != value['sha256']]
    checks['local_inputs'].append({'manifest': str(path.relative_to(repo)), 'manifest_sha256': sha(path), 'verified': manifest['local_input_count'], 'mismatches': mismatches})
    if mismatches: raise RuntimeError(mismatches)

env = os.environ.copy()
env.update(GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath', GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache', GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOPROXY='off', GOMAXPROCS='2')
goroot = pathlib.Path(subprocess.check_output(['go', 'env', 'GOROOT'], env=env, text=True).strip())
decoder = json.JSONDecoder()
stream = (evidence / 'final-go-list.jsonstream').read_text()
external = {}
while stream.strip():
    pkg, end = decoder.raw_decode(stream.lstrip())
    stream = stream.lstrip()[end:]
    directory = pathlib.Path(pkg.get('Dir', '.'))
    module = pkg.get('Module') or {}
    for field in ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles']:
        for name in pkg.get(field, []):
            path = (directory / name).resolve()
            if not path.is_file() or path.is_relative_to(repo): continue
            if path.is_relative_to(goroot): label = 'toolchain/' + str(path.relative_to(goroot))
            elif module.get('Dir') and path.is_relative_to(pathlib.Path(module['Dir'])): label = 'module/' + module['Path'] + '@' + module.get('Version', 'local') + '/' + str(path.relative_to(pathlib.Path(module['Dir'])))
            else: label = 'generated/' + hashlib.sha256(str(path).encode()).hexdigest() + '/' + path.name
            external[label] = path
manifest = json.loads((frozen / 'input-manifest.json').read_text())
mismatches = [label for label, value in manifest['inputs'].items() if not value['local'] and (label not in external or sha(external[label]) != value['sha256'])]
checks['external_inputs'] = {'verified': sum(not v['local'] for v in manifest['inputs'].values()), 'mismatches': mismatches}
if mismatches: raise RuntimeError(mismatches)

owned = [
    'Platform/pkg/model/query.go', 'Platform/internal/store/migration00007.go',
    'Platform/internal/store/query_projection.go', 'Platform/internal/store/query_read.go',
    'Platform/internal/store/query_projection_test.go', 'Platform/internal/application/queries.go',
    'Platform/internal/application/queries_test.go', 'Platform/internal/api/query_routes.go',
    'Platform/internal/api/query_routes_test.go', 'Platform/internal/store/store.go',
    'Platform/internal/store/transaction.go', 'Platform/internal/store/data.go',
    'Platform/internal/store/batch.go', 'Platform/internal/store/sync.go',
    'Platform/internal/store/control.go', 'Platform/internal/store/retention.go',
    'Platform/internal/store/archive.go', 'Platform/internal/store/migrations.go',
    'Platform/pkg/compatibility/compatibility.go', 'Platform/internal/api/server.go',
    'Platform/internal/api/definition_routes.go', 'Platform/internal/api/contracts.go',
    'Platform/internal/api/business.go', 'Platform/cmd/sf-contracts/main.go',
    'Platform/internal/app/workers.go', 'Platform/internal/control/legacy_test.go',
]
for name, command in [('gofmt', ['gofmt', '-l', *owned]), ('diff_check', ['git', 'diff', '--check', '--', *owned, 'contracts', 'docs/evolution/query-frontend-contract.md'])]:
    result = subprocess.run(command, cwd=repo, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    log = evidence / ('final-' + name + '.log')
    log.write_text(result.stdout)
    checks[name] = {'command': command, 'exit_code': result.returncode, 'log': str(log.relative_to(repo)), 'log_sha256': sha(log), 'output_empty': not result.stdout.strip()}
    if result.returncode or result.stdout.strip(): raise RuntimeError(name + ': ' + result.stdout)
checks['owned_source_files'] = {name: sha(repo / name) for name in owned}

preserved = json.loads((evidence / 'migrations-preserved.json').read_text())
checks['migration_versions_1_through_6'] = {'files': len(preserved), 'all_still_match_accepted_phase3': all(sha(repo / name) == value['sha256'] for name, value in preserved.items())}
if not checks['migration_versions_1_through_6']['all_still_match_accepted_phase3']: raise RuntimeError('historical migrations changed')
config = json.loads((repo / '.local/evolution/pg18-test.json').read_text())
url = urllib.parse.urlparse(config['dsn'])
secret_values = [config['dsn'], urllib.parse.unquote(url.password or '')]
matches = []
for path in evidence.iterdir():
    if path.is_file() and any(len(value) > 4 and value.encode() in path.read_bytes() for value in secret_values): matches.append(path.name)
checks['private_connection_material'] = {'checked_files': sum(path.is_file() for path in evidence.iterdir()), 'matches': matches}
if matches: raise RuntimeError('private material found in evidence')

query = "SELECT nspname FROM pg_namespace WHERE nspname ~ '^sf_(queries|query_application|query_wire)_' ORDER BY nspname"
command = ['docker', 'exec', 'smartfactory-evolution-pg18-20261004', 'psql', '-U', urllib.parse.unquote(url.username), '-d', url.path.lstrip('/'), '-Atc', query]
result = subprocess.run(command, cwd=repo, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
checks['postgres_cleanup'] = {'exit_code': result.returncode, 'remaining_owned_schemas': result.stdout.splitlines() if result.returncode == 0 else None, 'purpose': 'read-only inventory of this specialist query fixture schema prefixes'}
if result.returncode:
    checks['postgres_cleanup']['diagnostic'] = result.stdout
checks['workspace_free_bytes'] = shutil.disk_usage(repo).free
checks['query_contract'] = {'path': 'docs/evolution/query-frontend-contract.md', 'sha256': sha(repo / 'docs/evolution/query-frontend-contract.md'), 'change_after_freeze': 'Clarified page quality counters versus whole-range missing/completeness semantics; production code unchanged by this clarification'}

command = ['/private/tmp/smartfactory-evolution-python/bin/python', 'scripts/check-contracts.py']
result = subprocess.run(command, cwd=repo, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
log = evidence / 'final-workspace-contract-validation.log'
log.write_text(result.stdout)
checks['workspace_contract_validation'] = {'command': command, 'exit_code': result.returncode, 'log': str(log.relative_to(repo)), 'log_sha256': sha(log), 'result': json.loads(result.stdout) if result.returncode == 0 else result.stdout}
if result.returncode: raise RuntimeError(result.stdout)
(evidence / 'final-audit.json').write_text(json.dumps(checks, indent=2, sort_keys=True) + '\n')
print(json.dumps({key: checks[key] for key in ['local_inputs', 'external_inputs', 'migration_versions_1_through_6', 'postgres_cleanup', 'workspace_free_bytes']}, ensure_ascii=False))
