#!/usr/bin/env python3
"""Independently inspect the final fixed package without starting services."""
import collections
import datetime
import hashlib
import json
import re
import struct
import subprocess
import time
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
BASE = Path('/private/tmp/smartfactory-final-independent-20261007')
BUNDLE = Path('/private/tmp/smartfactory-delivery-final-20261007/bundle')
OUT = BASE / 'package-audit'
OUT.mkdir(exist_ok=False)

def sha(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''): digest.update(block)
    return digest.hexdigest()

def ref(path):
    path = Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def read(path): return json.loads(Path(path).read_text())
def save(path, value): path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')

command = ['python3', str(BUNDLE / 'scripts/verify-release.py'), str(BUNDLE)]
started = time.monotonic()
result = subprocess.run(command, text=True, capture_output=True)
(OUT / 'verify-release.stdout.log').write_text(result.stdout)
(OUT / 'verify-release.stderr.log').write_text(result.stderr)
execution = {'argv': command, 'cwd': str(ROOT), 'exit_code': result.returncode, 'duration_seconds': time.monotonic() - started, 'driver': ref(BUNDLE / 'scripts/verify-release.py'), 'stdout': ref(OUT / 'verify-release.stdout.log'), 'stderr': ref(OUT / 'verify-release.stderr.log')}
assert result.returncode == 0
manifest = read(BUNDLE / 'release-manifest.json')
source = read(BUNDLE / 'source-manifest.json')
build = read(BUNDLE / 'build-inputs.json')
components = read(BUNDLE / 'components.json')['components']
errors = []
sources = []
for relative, expected in source['files'].items():
    actual = ref(BUNDLE / 'source' / relative)
    original = ROOT / relative
    current_sha = sha(original) if original.is_file() else None
    row = {'relative': relative, 'packaged_sha256': actual['sha256'], 'expected_sha256': expected['sha256'], 'current_sha256': current_sha, 'equal': current_sha == actual['sha256'] == expected['sha256']}
    sources.append(row)
    if not row['equal']: errors.append({'type': 'source_difference', **row})
ordinary = read(BASE / 'platform-ordinary/before.inputs.json')
tested = []
for row in ordinary:
    p = Path(row['path'])
    if not p.is_relative_to(ROOT): continue
    relative = str(p.relative_to(ROOT))
    if not relative.startswith(('Platform/', 'DataTransfer/')): continue
    record = source['files'].get(relative)
    matched = bool(record and record['sha256'] == row['sha256'])
    tested.append({'relative': relative, 'tested_sha256': row['sha256'], 'packaged_sha256': record['sha256'] if record else None, 'matched': matched})
    if not matched: errors.append({'type': 'tested_source_difference', **tested[-1]})
frontend = []
for p in sorted((BUNDLE / 'Frontends/dist').rglob('*')):
    if not p.is_file(): continue
    relative = str(p.relative_to(BUNDLE / 'Frontends/dist'))
    tested_path = BASE / 'frontend/Frontends/dist' / relative
    matched = tested_path.is_file() and sha(tested_path) == sha(p)
    frontend.append({'relative': relative, 'packaged': ref(p), 'tested': ref(tested_path) if tested_path.is_file() else None, 'matched': matched})
    if not matched: errors.append({'type': 'frontend_asset_difference', 'relative': relative})
assert len(frontend) == 19
binaries = []
for name, expected in build['binaries'].items():
    p = BUNDLE / 'bin' / name
    header = p.read_bytes()[:64]
    elf_amd64 = header[:6] == b'\x7fELF\x02\x01' and struct.unpack('<H', header[18:20])[0] == 62
    row = {'name': name, 'file': ref(p), 'declared': expected, 'elf_64_little_endian_amd64': elf_amd64, 'matched': sha(p) == expected['sha256'] and p.stat().st_size == expected['bytes']}
    binaries.append(row)
    if not row['matched'] or not elf_amd64: errors.append({'type': 'binary_mismatch', 'name': name})
assert len(binaries) == 14
license_records, optional_absent, source_first_party, containers = [], [], [], []
for component in components:
    files = component.get('license_files', [])
    if not files:
        if component.get('first_party'): source_first_party.append(component['id'])
        elif component.get('optional') and component.get('installed_for_build') is False: optional_absent.append(component['id'])
        else: errors.append({'type': 'missing_installed_license', 'id': component['id']})
    for filename in files:
        p = BUNDLE / filename
        if not p.is_file() or filename not in manifest['files']:
            errors.append({'type': 'license_file_missing', 'id': component['id'], 'file': filename})
        else: license_records.append({'component': component['id'], 'file': ref(p)})
    if component.get('ecosystem') == 'container':
        row = {'id': component['id'], 'image': component['image'], 'source': component.get('source'), 'license_scope': component.get('license_scope'), 'license_files': files, 'provenance': []}
        if not row['source'] or not files: errors.append({'type': 'missing_container_source_or_license', 'id': row['id']})
        for name, provenance in zip(files, component.get('license_provenance', []), strict=True):
            matched = sha(BUNDLE / name) == provenance['sha256']
            row['provenance'].append({'file': name, 'source_url': provenance.get('source_url'), 'source_commit': provenance.get('source_commit'), 'matched': matched})
            if not matched: errors.append({'type': 'container_license_provenance_mismatch', 'id': row['id'], 'file': name})
        containers.append(row)
aliases = [row for row in components if row.get('alias')]
privacy_files, privacy_matches = 0, []
for p in BUNDLE.rglob('*'):
    if not p.is_file() or p.suffix.lower() not in ['.md', '.json', '.yaml', '.yml', '.toml', '.py', '.html']: continue
    relative = str(p.relative_to(BUNDLE))
    if relative.startswith('licenses/'): continue
    privacy_files += 1
    text = p.read_text(errors='replace')
    patterns = ['/home/developer', '/path/to/competition-workspace', '/private/tmp/smartfactory-', 'developer@']
    found = [pattern for pattern in patterns if pattern in text]
    if found: privacy_matches.append({'file': relative, 'patterns': found})
if privacy_matches: errors.append({'type': 'private_workspace_paths', 'matches': privacy_matches})
profile = BUNDLE / 'scripts/bootstrap-runtime.py'
backup = BUNDLE / 'Wiki/operations/backup-restore.md'
profile_correction = "compose('--profile','release','stop',node)" in profile.read_text()
backup_profile_commands = [line.strip() for line in backup.read_text().splitlines() if 'docker compose' in line and (' start ' in line or ' ps ' in line)]
assert profile_correction and all('--profile release' in line for line in backup_profile_commands)
report = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'passed' if not errors else 'failed', 'package_manifest': ref(BUNDLE / 'release-manifest.json'), 'source_manifest': ref(BUNDLE / 'source-manifest.json'), 'build_inputs': ref(BUNDLE / 'build-inputs.json'), 'component_manifest': ref(BUNDLE / 'components.json'), 'source_sha256': manifest['source_sha256'], 'file_count': len(manifest['files']), 'listed_file_bytes': sum(row['bytes'] for row in manifest['files'].values()), 'verify_release': execution, 'source_comparisons': sources, 'test_source_correspondence': tested, 'frontend_assets': frontend, 'binaries': binaries, 'build_inputs_equal_before_after': build['inputs_before'] == build['inputs_after'], 'build_declared_input_count': len(build['inputs_before']), 'build_input_scope': build['input_scope'], 'component_counts': dict(collections.Counter(row['ecosystem'] for row in components)), 'container_provenance': containers, 'license_files': license_records, 'uninstalled_optional_dependency_records': optional_absent, 'first_party_module_records': source_first_party, 'npm_alias_records': aliases, 'public_path_scan': {'files': privacy_files, 'matches': privacy_matches}, 'b01_packaged_profile_correction': {'bootstrap_runtime': ref(profile), 'stop_uses_release_profile': profile_correction, 'backup_guide': ref(backup), 'database_commands': backup_profile_commands}, 'errors': errors, 'runtime_scope': 'Read-only static verification of the fixed fourteen-program package; actual Linux startup, consumer application, stop and preserved-data restart are supplied by the separately coordinated final packaged runtime execution', 'driver': ref(__file__)}
save(OUT / 'verification.json', report)
print(json.dumps({'status': report['status'], 'files': report['file_count'], 'sources': len(sources), 'current_test_source_matches': sum(row['matched'] for row in tested), 'binaries': len(binaries), 'assets': len(frontend), 'container_components': len(containers), 'license_references': len(license_records), 'privacy_matches': privacy_matches, 'errors': errors[:5]}, ensure_ascii=False))
