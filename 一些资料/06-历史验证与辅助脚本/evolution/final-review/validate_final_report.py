#!/usr/bin/env python3
import collections
import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
BASE = Path('/private/tmp/smartfactory-final-independent-20261007')
REPORT = ROOT / '.local/evolution/final-independent-review.json'
cache = {}


def digest(value):
    p = Path(value)
    k = (str(p), p.stat().st_size, p.stat().st_mtime_ns)
    if k not in cache:
        with p.open('rb') as f:
            cache[k] = hashlib.file_digest(f, 'sha256').hexdigest()
    return cache[k]


def read(p):
    return json.loads(Path(p).read_text())


def refs(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            yield value
        for v in value.values():
            yield from refs(v)
    elif isinstance(value, list):
        for v in value:
            yield from refs(v)


j = read(REPORT)
assert j['status'] == 'passed'
requirements = j['requirements']
assert len(requirements) == 45
assert len({r['id'] for r in requirements}) == 45
assert all(r['final_independent_status'] == 'passed' for r in requirements)
assert len(j['findings']) == 14 and len(j['watchpoints']) == 14
assert all(r['final_independent_status'] == 'passed' for r in j['findings'] + j['watchpoints'])
for r in requirements:
    assert r['current_implementation_at_review'] or r['id'] == 'E5-07'
    assert r['current_evidence'] and r['conclusion']
    assert all(g in j['verification_groups'] for g in r['verification_groups'])
indexes = [BASE / 'final-summary/browser-artifact-index.json',
           BASE / 'final-summary/executed-input-and-binary-audit-compact.json',
           BASE / 'package-audit/archive-audit.json']
all_refs = list(refs(j))
for p in indexes:
    all_refs.extend(refs(read(p)))
checks = []
errors = []
unique = set()
for r in all_refs:
    key = (r['path'], r['sha256'])
    if key in unique:
        continue
    unique.add(key)
    p = Path(r['path'])
    ok = p.is_file() and digest(p) == r['sha256']
    if 'bytes' in r:
        ok = ok and p.stat().st_size == r['bytes']
    row = {'path': str(p), 'expected_sha256': r['sha256'], 'matched': ok}
    checks.append(row)
    if not ok:
        errors.append(row)
assert not errors, errors
input_audit = read(indexes[1])
assert input_audit['status'] == 'passed' and not input_audit['errors']
assert all(c['matched'] for c in input_audit['checks'])
assert read(BASE / 'final-summary/browser-summary.json')['unique_successful_test_titles'] == 39
assert digest(REPORT) == digest(BASE / 'final-summary/final-independent-review.json')
out = {
    'status': 'passed', 'verified_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'report': {'path': str(REPORT), 'sha256': digest(REPORT), 'bytes': REPORT.stat().st_size},
    'criterion_count': len(requirements),
    'phase_criterion_counts': dict(collections.Counter(r['phase'] for r in requirements)),
    'findings': 14, 'watchpoints': 14,
    'structured_reference_observations': len(all_refs), 'unique_references_checked': len(checks),
    'checks': checks, 'errors': errors,
    'executed_input_audit_distinct_checks': input_audit['check_count'],
    'executed_input_audit_original_observations': input_audit['original_check_observations'],
    'executed_input_audit_distinct_paths': input_audit['unique_checked_paths'],
    'fixed_report_matches': True,
    'scope': 'Final report completeness and exact referenced file integrity; behavioral results retain their original commands, outcomes and scope in the cited execution reports',
}
destination = BASE / 'final-summary/final-report-validation.json'
destination.write_text(json.dumps(out, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'validation': {'path': str(destination), 'sha256': digest(destination), 'bytes': destination.stat().st_size},
                  'report': out['report'], 'criterion_count': len(requirements),
                  'reference_count': len(checks), 'status': 'passed'}, indent=2))
