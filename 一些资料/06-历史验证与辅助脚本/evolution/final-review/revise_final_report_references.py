#!/usr/bin/env python3
import datetime
import hashlib
import json
import shutil
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
BASE = Path('/private/tmp/smartfactory-final-independent-20261007/final-summary')
ORIGINALS = BASE / 'report-correction-originals'
OLD_DELIVERY_SHA = 'd7536702f8e31f5e43850e8525a0e9ef2e7494ca197f793ba6b2bf3a58ab7955'
NEW_DELIVERY_SHA = '12cb89e36ebb8ab1ae5c50bdea04c0d0fd69350bb52e351d5f314605d0af4e7c'
OLD_REVIEW_SHA = '23d1c81e08db8a35cf9f9a6078bf0df668375c33445a35119525726ecbe2961a'
cache = {}


def sha(value):
    p = Path(value)
    key = (str(p), p.stat().st_size, p.stat().st_mtime_ns)
    if key not in cache:
        with p.open('rb') as f:
            cache[key] = hashlib.file_digest(f, 'sha256').hexdigest()
    return cache[key]


def ref(value):
    p = Path(value)
    return {'path': str(p), 'sha256': sha(p), 'bytes': p.stat().st_size}


def read(value):
    return json.loads(Path(value).read_text())


def references(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            yield value
        for v in value.values():
            yield from references(v)
    elif isinstance(value, list):
        for v in value:
            yield from references(v)


delivery_path = ROOT / '.local/evolution/final-delivery-verified.json'
review_path = ROOT / '.local/evolution/final-independent-review.json'
assert sha(delivery_path) == NEW_DELIVERY_SHA
assert sha(review_path) == OLD_REVIEW_SHA
assert sha(ORIGINALS / (OLD_DELIVERY_SHA + '.json')) == OLD_DELIVERY_SHA
assert sha(ORIGINALS / (OLD_REVIEW_SHA + '.json')) == OLD_REVIEW_SHA
changes = read(BASE / 'delivery-report-text-diff.json')['changes']
assert {c['key'] for c in changes} == {
    '$.regressions_and_specialties.current_datatransfer_correspondence',
    '$.reverification_entrypoints.public_operations',
    '$.report_generator.bytes', '$.report_generator.sha256',
}
delivery = read(delivery_path)
assert delivery['reverification_entrypoints']['public_operations'] == '<extracted-package>/Wiki/installation.md 和 Wiki/user-guide/node-releases.md'
assert all(x in delivery['regressions_and_specialties']['current_datatransfer_correspondence'] for x in ['596', '4268', '4318'])
checks = []
for item in references(delivery):
    ok = sha(item['path']) == item['sha256']
    assert ok, item
    checks.append({'path': item['path'], 'sha256': item['sha256'], 'matched': ok})
index_path = ROOT / '.local/evolution/delivery/SmartFactory-1.1.0-dev-linux-amd64-20261007-6c66570a31e7.final-report.json'
assert sha(index_path) == '7fd9453d99926959c1937b24dd78704e67436afd347025872781b8d44c9a0fcd'
for item in references(read(index_path)):
    assert sha(item['path']) == item['sha256'], item
for p in [ROOT / 'Wiki/user-guide/node-releases.md', Path('/private/tmp/smartfactory-delivery-final-20261007/bundle/Wiki/user-guide/node-releases.md')]:
    assert p.is_file(), p
source_document = ROOT / 'Wiki/user-guide/node-releases.md'
packaged_document = Path('/private/tmp/smartfactory-delivery-final-20261007/bundle/Wiki/user-guide/node-releases.md')
assert sha(source_document) == sha('/private/tmp/smartfactory-delivery-final-20261007/bundle/source/Wiki/user-guide/node-releases.md')
normalized_document = packaged_document.read_text().replace('(../../source/Frontends/', '(../../Frontends/').replace('(../../source/Platform/', '(../../Platform/')
assert normalized_document == source_document.read_text()

correction_path = BASE / 'delivery-report-correction-audit.json'
correction = {
    'status': 'passed', 'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'original_delivery_report': ref(ORIGINALS / (OLD_DELIVERY_SHA + '.json')),
    'corrected_delivery_report': ref(delivery_path),
    'delivery_association_index': ref(index_path),
    'specialist_correction': ref('/private/tmp/smartfactory-delivery-final-20261007/report-text-correction/verification.json'),
    'exact_json_diff': ref(BASE / 'delivery-report-text-diff.json'),
    'change_scope': 'Exactly two report text fields and the resulting generator size/hash changed; all source, package, archive, behavior, command, result and cleanup fields are identical',
    'current_reference_observations': len(checks), 'reference_checks': checks,
    'actual_document': ref(ROOT / 'Wiki/user-guide/node-releases.md'),
    'packaged_document': ref('/private/tmp/smartfactory-delivery-final-20261007/bundle/Wiki/user-guide/node-releases.md'),
    'document_source_correspondence': ref('/private/tmp/smartfactory-delivery-final-20261007/bundle/source/Wiki/user-guide/node-releases.md'),
    'packaged_link_rewrite': 'The public Wiki page rewrites five frontend/backend source links through ../../source; its packaged source copy matches the workspace byte for byte, and reversing only those link prefixes reproduces the original page',
    'original_preservation': ref(ORIGINALS / 'preservation.json'),
    'original_generator': ref(ORIGINALS / '35a8e4bd0abf7c52fbfab5bb92b91a9b9ce1ad7a3bdd7a29eb250c6a245140da.py'),
    'original_generator_preservation': ref(ORIGINALS / 'generator-preservation.json'),
    'driver': ref(Path(__file__)),
}
correction_path.write_text(json.dumps(correction, ensure_ascii=False, indent=2) + '\n')
report = read(review_path)
count = 0
for item in references(report):
    if item['path'] == str(delivery_path):
        assert item['sha256'] == OLD_DELIVERY_SHA
        item.update(ref(delivery_path))
        count += 1
assert count > 0
report['delivery_reference_correction'] = {
    'updated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'updated_reference_count': count,
    'original_independent_report': ref(ORIGINALS / (OLD_REVIEW_SHA + '.json')),
    'original_integrity_verification': ref(ORIGINALS / 'c4f9cca3a7895bf043a3ed9677f1feb18396f116d7895ba9f19db0eacb75bdd4.json'),
    'audit': ref(correction_path),
    'acceptance_conclusions': 'All 45 conclusions, 14 finding results, 14 watchpoint results and original execution outcomes are unchanged; only the corrected delivery report references and this provenance record were updated',
}
review_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
shutil.copyfile(review_path, BASE / 'final-independent-review.json')
print(json.dumps({'status': 'passed', 'report': ref(review_path),
                  'reference_updates': count, 'correction_audit': ref(correction_path)}, indent=2))
