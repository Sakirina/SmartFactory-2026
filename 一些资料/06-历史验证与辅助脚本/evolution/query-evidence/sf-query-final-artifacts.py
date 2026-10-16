import datetime, hashlib, json, pathlib, shutil, subprocess, time

repo = pathlib.Path('/path/to/SmartFactory-2026')
evidence = repo / '.local/evolution/query-evidence'
frozen = pathlib.Path((evidence / 'final-snapshot-path.txt').read_text().strip())
manifest = json.loads((frozen / 'input-manifest.json').read_text())
def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def now(): return datetime.datetime.now(datetime.timezone.utc).isoformat()

repeat = pathlib.Path('/private/tmp/smartfactory-query-contracts-repeat-20261005')
repeat.mkdir(exist_ok=True)
binary = frozen / 'binaries/sf-contracts'
command = [str(binary), '-out', str(repeat / 'contracts/1.0'), '-examples', str(repeat / 'examples/factory/definitions.json')]
started = now()
clock = time.monotonic()
result = subprocess.run(command, cwd=frozen / 'Platform', text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
log = evidence / 'contracts-determinism.log'
log.write_text(result.stdout)
if result.returncode: raise SystemExit(result.returncode)

generated = sorted(path.relative_to(frozen) for path in (frozen / 'contracts').rglob('*') if path.is_file())
generated.append(pathlib.Path('examples/factory/definitions.json'))
artifacts = {}
for path in generated:
    original, duplicate = frozen / path, repeat / path
    if not duplicate.is_file() or sha(original) != sha(duplicate): raise RuntimeError(f'non-deterministic contract: {path}')
    target = repo / path
    before = sha(target) if target.exists() else None
    target.parent.mkdir(parents=True, exist_ok=True)
    if before != sha(original): shutil.copyfile(original, target)
    artifacts[str(path)] = {'sha256': sha(original), 'bytes': original.stat().st_size, 'workspace_before_sha256': before, 'workspace_matches_frozen': sha(target) == sha(original)}
record = {
    'started_at': started, 'completed_at': now(), 'elapsed_seconds': round(time.monotonic() - clock, 3),
    'command': command, 'cwd': str(frozen / 'Platform'), 'exit_code': result.returncode,
    'input_manifest_sha256': sha(frozen / 'input-manifest.json'), 'binary_sha256': sha(binary),
    'log': str(log.relative_to(repo)), 'log_sha256': sha(log),
    'generation_repeated_byte_for_byte': True, 'generated_count': len(generated), 'artifacts': artifacts,
}
(evidence / 'generated-artifacts.json').write_text(json.dumps(record, indent=2, sort_keys=True) + '\n')

drift = []
for label, value in manifest['inputs'].items():
    if not value['local']: continue
    if sha(frozen / label) != value['sha256']: raise RuntimeError(f'frozen input changed: {label}')
    current = repo / label
    digest = sha(current) if current.exists() else None
    if digest != value['sha256']:
        drift.append({'path': label, 'frozen_sha256': value['sha256'], 'workspace_sha256': digest})
(evidence / 'workspace-drift-after-freeze.json').write_text(json.dumps({'checked_at': now(), 'input_manifest_sha256': sha(frozen / 'input-manifest.json'), 'changes': drift}, indent=2) + '\n')
print(json.dumps({'generated_artifacts': len(generated), 'repeated_generation_identical': True, 'workspace_drift': [item['path'] for item in drift]}, ensure_ascii=False))
