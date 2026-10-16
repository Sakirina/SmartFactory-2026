#!/usr/bin/env python3
import datetime
import difflib
import importlib.util
import json
import pathlib
import shutil
import sys

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
spec = importlib.util.spec_from_file_location('review_runner', ROOT / '.local/evolution/phase4-review-evidence/run_review.py')
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)
r.OUT = ROOT / '.local/evolution/phase4-review-a02'
r.BASE = pathlib.Path('/private/tmp/smartfactory-phase4-independent-a02-20261005')
r.WS = r.BASE / 'workspace'
r.OUT.mkdir(exist_ok=True)
r.BASE.mkdir(exist_ok=True)
AI_FIXED = pathlib.Path('/private/tmp/smartfactory-ai-a02-final-20261005-110928/workspace')

def prepare():
    r.prepare()
    supplement = []
    for relative in ['Frontends/package.json', 'Frontends/package-lock.json', '.github/workflows/build-release.yml']:
        dest = r.WS / relative
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(ROOT / relative, dest)
        supplement.append({'original_path': str(ROOT / relative), 'preserved_path': str(dest), **r.ref(dest)})
    r.write(r.OUT / 'auxiliary-runtime-inputs.json', {'files': supplement})
    source = pathlib.Path('/private/tmp/smartfactory-phase4-independent-20261005/overlays/independent-ai-build-v3/independent_ai_review_test.go')
    original = source.read_text()
    before = 'len(detail.Evidence[0].Arguments) != 0'
    after = 'len(detail.Evidence[0].Arguments) != 0 && string(detail.Evidence[0].Arguments) != "null"'
    assert original.count(before) == 1
    corrected = original.replace(before, after)
    dest = r.BASE / 'overlays/independent-v3-null-corrected/independent_ai_review_test.go'
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(corrected)
    counterpart = AI_FIXED / 'Platform/internal/a02reviewv3corrected/independent_ai_review_test.go'
    assert r.sha(dest) == r.sha(counterpart)
    patch = r.OUT / 'independent-v3-null-correction.patch'
    patch.write_text(''.join(difflib.unified_diff(original.splitlines(True), corrected.splitlines(True), fromfile=str(source), tofile=str(dest))))
    r.write(r.OUT / 'independent-null-correction.json', {'original': r.ref(source), 'corrected': r.ref(dest), 'patch': r.ref(patch), 'equals_specialist_minimal_correction': True, 'reason': 'Required arguments is serialized as JSON null after content is cleared; nil and the JSON null token both represent no parameter content. Non-null prior content still fails the assertion.'})
    r.write(r.OUT / 'independent-v3-overlay.json', {'Replace': {str(r.WS / 'Platform/internal/ai/independent_ai_review_test.go'): str(dest)}})
    r.capture()

def audit():
    def refs(value):
        if isinstance(value, dict):
            if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
                yield value['path'], value['sha256']
            for key, item in value.items():
                if isinstance(item, str) and key.endswith('_sha256'):
                    stem = key[:-7]
                    target = value.get(stem) or value.get(stem + '_path')
                    if isinstance(target, str) and '/' in target:
                        yield target, item
            for item in value.values():
                yield from refs(item)
        elif isinstance(value, list):
            for item in value:
                yield from refs(item)
    report = ROOT / '.local/evolution/ai-evidence-a02-verified.json'
    rows = []
    for name, digest in sorted(set(refs(json.loads(report.read_text())))):
        path = pathlib.Path(name)
        if not path.is_absolute():
            path = ROOT / path
        rows.append({'path': str(path), 'expected_sha256': digest, 'matched': path.is_file() and r.sha(path) == digest})
    snapshot = json.loads((r.OUT / 'snapshot.json').read_text())['files']
    comparison, prior_changes, contracts = [], [], []
    prior = pathlib.Path('/private/tmp/smartfactory-phase4-independent-20261005/workspace')
    for item in snapshot:
        relative = pathlib.Path(item['path'])
        if relative.parts[0] in ['Platform', 'DataTransfer'] and (relative.suffix == '.go' or relative.name in ['go.mod', 'go.sum']):
            fixed = AI_FIXED / relative
            comparison.append({'path': str(relative), 'sha256': item['sha256'], 'ai_fixed_sha256': r.sha(fixed) if fixed.is_file() else None, 'identical': fixed.is_file() and r.sha(fixed) == item['sha256']})
        old = prior / relative
        if not old.is_file() or r.sha(old) != item['sha256']:
            prior_changes.append({'path': str(relative), 'before_sha256': r.sha(old) if old.is_file() else None, 'after_sha256': item['sha256']})
        if relative.parts[0] == 'contracts':
            contracts.append({'path': str(relative), 'unchanged': old.is_file() and r.sha(old) == item['sha256']})
    result = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'report': r.ref(report), 'references': rows, 'unmatched': [row for row in rows if not row['matched']], 'backend_vs_AI_fixed': comparison, 'backend_differences': [row for row in comparison if not row['identical']], 'changes_from_prior_independent_snapshot': prior_changes, 'contracts': contracts}
    r.write(r.OUT / 'source-and-specialist-audit.json', result)
    print(json.dumps({'references': len(rows), 'unmatched': len(result['unmatched']), 'backend_files': len(comparison), 'backend_differences': result['backend_differences'], 'changes_from_prior': prior_changes, 'contract_count': len(contracts), 'contract_changes': [x for x in contracts if not x['unchanged']]}, ensure_ascii=False), flush=True)
    assert not result['unmatched'] and not result['backend_differences']

def run_tests(mode):
    packages = json.loads((r.OUT / 'test-packages.json').read_text())
    if mode == 'ordinary':
        for suffix in ['ai', 'application', 'api']:
            package = next(x for x in packages if x['module'] == 'Platform' and x['relative'] == './internal/' + suffix)
            r.test(package, 'a02-ordinary-' + suffix)
    elif mode == 'postgres':
        for suffix in ['ai', 'application', 'api']:
            package = next(x for x in packages if x['module'] == 'Platform' and x['relative'] == './internal/' + suffix)
            r.test(package, 'a02-postgres-' + suffix, r'^Test(.*Postgres.*)$', pg=True)
    elif mode == 'race':
        for suffix in ['ai', 'application']:
            package = next(x for x in packages if x['module'] == 'Platform' and x['relative'] == './internal/' + suffix)
            r.test(package, 'a02-race-' + suffix, r'^TestInvestigation', race=True, pg=True, timeout='300s')
    elif mode == 'reproduction':
        binary = r.BASE / 'bin/independent-v3-null-corrected.test'
        binary.parent.mkdir(exist_ok=True)
        metadata = r.run('a02-independent-reproduction-build', ['go', 'test', '-p', '1', '-overlay', str(r.OUT / 'independent-v3-overlay.json'), '-c', '-o', str(binary), './internal/ai'], r.WS / 'Platform')
        if metadata['exit_code'] == 0:
            r.run('a02-independent-reproduction', ['go', 'tool', 'test2json', '-t', '-p', 'competition2026/product/platform/internal/ai', str(binary), '-test.v=test2json', '-test.parallel=1', '-test.count=1', '-test.timeout=60s', '-test.run=^TestIndependent'], r.WS / 'Platform/internal/ai')
    elif mode == 'auxiliary':
        r.run('a02-release-deployment-tests', ['/private/tmp/smartfactory-evolution-python/bin/python', '-m', 'unittest', 'discover', '-s', 'tests', '-v'], r.WS)
        for name in ['sf-ai-evidence-fixture', 'sf-business-fixture', 'sf-site-fixture']:
            r.run('a02-build-' + name, ['go', 'build', '-p', '1', '-o', str(r.BASE / 'bin' / name), './cmd/' + name], r.WS / 'Platform')
    elif mode == 'precision-binaries':
        for name in ['sf-cloud', 'sf-edge']:
            r.run('a02-build-' + name, ['go', 'build', '-p', '1', '-o', str(r.BASE / 'bin' / name), './cmd/' + name], r.WS / 'Platform')

if __name__ == '__main__':
    mode = sys.argv[1]
    if mode == 'prepare':
        prepare()
        audit()
    elif mode == 'audit':
        audit()
    else:
        run_tests(mode)
