#!/usr/bin/env python3
"""Independent evidence/input inspection; does not execute product processes."""
from __future__ import annotations

import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
OUT = Path('/private/tmp/smartfactory-final-independent-20261007/readonly')
OUT.mkdir(parents=True, exist_ok=True)

def sha(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for part in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(part)
    return h.hexdigest()

def read(path):
    return json.loads(Path(path).read_text())

def ref(path):
    path = Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def save(name, value):
    path = OUT / name
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    return ref(path)

def record(path, expected=None):
    path = Path(path)
    row = {'path': str(path), 'exists': path.is_file()}
    if row['exists']:
        row.update(ref(path))
    if expected is not None:
        row.update(expected_sha256=expected, matched=row.get('sha256') == expected)
    return row

created_at = datetime.datetime.now(datetime.timezone.utc).isoformat()
acceptance_path = ROOT / 'docs/evolution/acceptance.json'
acceptance = read(acceptance_path)
acceptance_copy = save('acceptance-at-inspection.json', acceptance)
evidence_paths = sorted({p for requirement in acceptance['requirements'] for p in requirement['evidence']})
evidence = [record(ROOT / p) for p in evidence_paths]
requirements = []
for requirement in acceptance['requirements']:
    implementation = []
    for relative in requirement.get('implementation', []):
        path = ROOT / relative
        if path.is_dir():
            rows = [ref(p) for p in sorted(path.rglob('*')) if p.is_file()]
            implementation.append({'path': str(path), 'kind': 'directory', 'files': rows})
        else:
            implementation.append(record(path))
    requirements.append({
        'id': requirement['id'], 'title': requirement['title'],
        'requirement': requirement['requirement'],
        'verification_criteria': requirement['verification_criteria'],
        'recorded_verification_status': requirement['verification_status'],
        'recorded_evidence': requirement['evidence'],
        'current_implementation': implementation,
        'independent_final_status': 'pending_final_combination_review',
    })
inventory_ref = save('requirement-inventory.json', {
    'created_at': created_at, 'acceptance': ref(acceptance_path),
    'retained_acceptance': acceptance_copy,
    'requirements': requirements, 'direct_evidence': evidence,
    'missing_evidence': [row for row in evidence if not row['exists']],
    'scope': '45 criteria and current implementation/evidence inventory; no final acceptance is assigned by inventory',
})

frontend_path = ROOT / '.local/evolution/release-interface/final-inputs.json'
frontend = read(frontend_path)
frontend_rows = []
frontend_current = []
for row in frontend['files']:
    item = record(row['preserved_path'], row['sha256'])
    item['manifest_path'] = row['path']
    frontend_rows.append(item)
    if row['path'].startswith('workspace/'):
        current = record(ROOT / row['path'][len('workspace/'):], row['sha256'])
        current['manifest_path'] = row['path']
        frontend_current.append(current)
frontend_outputs = []
for row in frontend['outputs']:
    for label, path in [('preserved', Path(row['preserved_path'])), ('tested', Path(frontend['tested_static']) / row['path']), ('workspace', ROOT / 'Frontends/dist' / row['path'])]:
        item = record(path, row['sha256'])
        item.update(kind=label, manifest_path=row['path'])
        frontend_outputs.append(item)
frontend_ref = save('frontend-fixed-input-audit.json', {
    'created_at': created_at, 'manifest': ref(frontend_path),
    'declared_inputs': frontend['source_file_count'] + frontend['installed_dependency_file_count'] + frontend['node_runtime_file_count'],
    'retained_input_count': len(frontend_rows),
    'retained_inputs': frontend_rows, 'current_source': frontend_current,
    'outputs': frontend_outputs,
    'mismatches': [r for r in frontend_rows + frontend_current + frontend_outputs if not r['matched']],
})

historical_path = ROOT / '.local/evolution/phase4-preserved-paths.json'
historical = read(historical_path)
comparisons = []
historical_originals = set()
for row in historical['files']:
    original = Path(row['original'])
    historical_originals.add(original)
    current = record(original)
    retained = record(row['preserved'], row['sha256'])
    comparisons.append({
        'relative_path': str(original.relative_to(ROOT)),
        'class': row['class'], 'retained': retained, 'current': current,
        'current_equal_historical': current.get('sha256') == row['sha256'],
    })
new_sources = []
for folder in ['Platform', 'DataTransfer', 'Frontends/src', 'Frontends/tests', 'contracts']:
    for path in sorted((ROOT / folder).rglob('*')):
        if path.is_file() and path not in historical_originals and not any(part.startswith('.') or part in ['node_modules', 'vendor'] for part in path.relative_to(ROOT).parts) and path.suffix in ['.go', '.ts', '.tsx', '.mjs', '.json', '.proto', '.css']:
            new_sources.append(ref(path))
delta_ref = save('phase4-current-source-comparison-revised.json', {
    'created_at': created_at, 'historical_map': ref(historical_path),
    'comparisons': comparisons, 'new_source_files': new_sources,
    'retained_errors': [r for r in comparisons if not r['retained']['matched']],
    'changed_existing_source_paths': [r['relative_path'] for r in comparisons if r['class'] == 'source' and not r['current_equal_historical']],
})

summary = {
    'created_at': created_at, 'scope': 'read_only_input_and_requirement_inventory',
    'criteria': len(requirements), 'direct_evidence_files': len(evidence),
    'missing_evidence_files': sum(not r['exists'] for r in evidence),
    'frontend_retained_inputs': len(frontend_rows), 'frontend_current_sources': len(frontend_current),
    'frontend_output_comparisons': len(frontend_outputs),
    'frontend_mismatches': sum(not r['matched'] for r in frontend_rows + frontend_current + frontend_outputs),
    'historical_preserved_files': len(comparisons),
    'historical_preservation_errors': sum(not r['retained']['matched'] for r in comparisons),
    'changed_existing_source_files': sum(r['class'] == 'source' and not r['current_equal_historical'] for r in comparisons),
    'new_source_files': len(new_sources),
    'reports': [inventory_ref, frontend_ref, delta_ref],
    'product_tests_builds_services_or_browser_executed': False,
}
save('summary-revised.json', summary)
print(json.dumps(summary, ensure_ascii=False, indent=2))
