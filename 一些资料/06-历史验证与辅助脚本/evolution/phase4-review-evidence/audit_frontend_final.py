#!/usr/bin/env python3
import datetime
import hashlib
import json
import pathlib
import shutil

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
BASE = pathlib.Path('/private/tmp/smartfactory-phase4-independent-browser-20261005')
OUT = ROOT / '.local/evolution/phase4-review-browser'

def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()

def read(path):
    return json.loads(pathlib.Path(path).read_text())

def ref(path):
    path = pathlib.Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

def references(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str) and len(value['sha256']) == 64:
            yield value
        for child in value.values():
            yield from references(child)
    elif isinstance(value, list):
        for child in value:
            yield from references(child)

source_report = ROOT / '.local/evolution/frontend-phase4-verified.json'
saved_report = BASE / 'specialist-final-report.json'
assert sha(source_report) == '991e16581c4033e778bd1c0753a7337e7bf2e90cb515407afb8f9323f5107524'
if saved_report.exists():
    assert sha(saved_report) == sha(source_report)
else:
    shutil.copy2(source_report, saved_report)
report = read(saved_report)
checks, mismatches = [], []
groups = {'report': list(references(report)), 'evidence_index': list(references(read(report['retained_evidence_index']['path']))), 'business_actual_inputs': list(references(read(report['backend_combination']['business_actual_build_inputs']['path'])))}
for name, rows in groups.items():
    for row in rows:
        path = pathlib.Path(row.get('preserved_path', row['path']))
        matched = path.is_file() and sha(path) == row['sha256']
        checks.append({'group': name, 'path': str(path), 'sha256': row['sha256'], 'matched': matched})
        if not matched:
            mismatches.append(checks[-1])

r1 = read(BASE / 'frontend-r1/specialist-final-inputs-r1.json')
current = []
for row in r1['files'] + r1['outputs']:
    if row['path'].startswith('workspace/'):
        path = ROOT / row['path'][len('workspace/'):]
    elif row['path'].startswith('static-output/'):
        path = ROOT / 'Frontends/dist' / row['path'][len('static-output/'):]
    else:
        continue
    matched = path.is_file() and sha(path) == row['sha256']
    current.append({'path': str(path), 'sha256': row['sha256'], 'matched': matched})
    if not matched:
        mismatches.append(current[-1])

a02 = read(ROOT / '.local/evolution/phase4-review-a02/snapshot.json')
backend = []
for row in a02['files']:
    relative = row['path']
    if pathlib.Path(relative).suffix == '.go' or pathlib.Path(relative).name in ['go.mod', 'go.sum'] or relative.startswith('contracts/'):
        path = ROOT / relative
        matched = path.is_file() and sha(path) == row['sha256']
        backend.append({'path': str(path), 'sha256': row['sha256'], 'matched': matched})
        if not matched:
            mismatches.append(backend[-1])

result = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'specialist_report': ref(saved_report), 'source_report': ref(source_report), 'reference_counts': {name: len(rows) for name, rows in groups.items()}, 'checks': checks, 'current_frontend_source_and_assets': current, 'current_backend_and_contracts': backend, 'mismatches': mismatches, 'specialist_summaries_checked': {'browser_pass': report['browser']['unified']['passed'], 'unit_pass': report['checks']['unit']['passed'], 'persistence': report['persistence'], 'audit': report['audit'], 'cleanup': report['cleanup']}, 'history_note': 'The preliminary inspection encountered the earlier transition input; the first complete independent 30,878-item freeze audit already used bb13. The retained transition-manifest-retention.json records the exact distinct digests.'}
(OUT / 'specialist-final-audit.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'reference_counts': result['reference_counts'], 'current_frontend': len(current), 'current_backend_and_contracts': len(backend), 'mismatches': mismatches, 'report': str(OUT / 'specialist-final-audit.json')}))
assert not mismatches
