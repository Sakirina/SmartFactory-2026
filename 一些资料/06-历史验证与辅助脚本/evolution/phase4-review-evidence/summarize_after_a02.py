#!/usr/bin/env python3
import datetime, hashlib, json, pathlib

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
OUT = ROOT / '.local/evolution/phase4-review-a02'

def sha(path):
    digest = hashlib.sha256()
    with pathlib.Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()

def ref(path):
    path = pathlib.Path(path)
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

rows = [json.loads(path.read_text()) for path in sorted(OUT.glob('*.metadata.json'))]
groups = {}
for name, prefix in [('ordinary', 'a02-ordinary-'), ('postgres', 'a02-postgres-'), ('limited_race', 'a02-race-')]:
    selected = [row for row in rows if row['label'].startswith(prefix)]
    groups[name] = {'runs': [row['label'] for row in selected], 'top_level': {status: sum(row['counts']['top_level'][status] for row in selected) for status in ['pass', 'fail', 'skip']}, 'races': sum(row['counts']['races'] for row in selected)}
references = []
for row in rows:
    for key in ['stdout', 'stderr', 'build_inputs', 'binary', 'overlay']:
        item = row.get(key)
        if item:
            references.append({'run': row['label'], 'kind': key, 'path': item['path'], 'expected_sha256': item['sha256'], 'matched': sha(item['path']) == item['sha256']})
    for item in row.get('overlay_inputs', []):
        path = item.get('preserved_path', item['path'])
        references.append({'run': row['label'], 'kind': 'overlay_input', 'path': path, 'expected_sha256': item['sha256'], 'matched': sha(path) == item['sha256']})
for name in ['snapshot.json', 'build-inputs.json', 'auxiliary-runtime-inputs.json']:
    for item in json.loads((OUT / name).read_text())['files']:
        path = item.get('preserved_path', item['path'])
        references.append({'manifest': name, 'path': path, 'expected_sha256': item['sha256'], 'matched': sha(path) == item['sha256']})
integrity = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'checked_count': len(references), 'checks': references, 'mismatches': [row for row in references if not row['matched']]}
(OUT / 'artifact-integrity.json').write_text(json.dumps(integrity, ensure_ascii=False, indent=2) + '\n')
summary = {
    'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'scope': 'A02 corrected backend and synchronized frontend dependency declarations; frontend browser acceptance remains pending its frozen delivery.',
    'previous_checkpoint': ref(ROOT / '.local/evolution/phase4-independent-review-checkpoint.json'),
    'snapshot': ref(OUT / 'snapshot.json'),
    'build_inputs': ref(OUT / 'build-inputs.json'),
    'source_and_specialist_audit': ref(OUT / 'source-and-specialist-audit.json'),
    'null_assertion_correction': ref(OUT / 'independent-null-correction.json'),
    'independent_reproduction': ref(OUT / 'a02-independent-reproduction.metadata.json'),
    'reproduction_result': {'evidence_http': 404, 'visible_content_bytes': 0, 'investigation_http': 200, 'answer_bytes': 0, 'messages': 0, 'resources': 0, 'arguments_json': None, 'reference_status': 'deleted'},
    'test_groups': groups,
    'release_deployment': {'tests': 17, 'pass': 17, 'fail': 0, 'run': ref(OUT / 'a02-release-deployment-tests.metadata.json')},
    'run_metadata': [ref(OUT / (row['label'] + '.metadata.json')) for row in rows],
    'artifact_integrity': ref(OUT / 'artifact-integrity.json'),
    'frontend_findings_pending_independent_verification': ['P4-F01', 'P4-F02', 'P4-F03', 'P4-F04'],
    'final_acceptance': False,
}
(OUT / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
assert not integrity['mismatches']
assert groups['ordinary']['top_level'] == {'pass': 119, 'fail': 0, 'skip': 16}
assert groups['postgres']['top_level'] == {'pass': 17, 'fail': 0, 'skip': 0}
assert groups['limited_race']['top_level'] == {'pass': 20, 'fail': 0, 'skip': 0}
print(json.dumps({'summary': str(OUT / 'summary.json'), 'groups': groups, 'runs': len(rows), 'reference_checks': len(references), 'mismatches': len(integrity['mismatches'])}, ensure_ascii=False))
