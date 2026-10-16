#!/usr/bin/env python3
"""Independently rebuild and verify the isolated P4-F05 frontend revision."""
import difflib
import json
import pathlib
import shutil
import sys
import browser_review as b

ORIGINAL_BASE = b.BASE
ORIGINAL_OUT = b.OUT
b.BASE = ORIGINAL_BASE / 'r1-runtime'
b.WS = ORIGINAL_BASE / 'frontend-r1'
b.OUT = ORIGINAL_OUT / 'r1'
SOURCE = pathlib.Path('/private/tmp/smartfactory-frontend-phase4-20261005/clean-build-r1')
MANIFEST = b.ROOT / '.local/evolution/frontend-phase4/final-inputs-r1.json'

def prepare():
    original_manifest = json.loads((ORIGINAL_BASE / 'specialist-final-inputs.json').read_text())
    revised_manifest = json.loads(MANIFEST.read_text())
    before = {row['path']: row for row in original_manifest['files']}
    after = {row['path']: row for row in revised_manifest['files']}
    assert before.keys() == after.keys()
    changed = [key for key in before if before[key]['sha256'] != after[key]['sha256']]
    assert changed == ['workspace/Frontends/src/style.css'], changed
    b.snapshot(SOURCE)
    shutil.copy2(MANIFEST, b.WS / 'specialist-final-inputs-r1.json')
    shutil.copy2(MANIFEST.with_name('freeze-r1.json'), b.WS / 'specialist-freeze-r1.json')
    shutil.copytree(ORIGINAL_BASE / 'frontend/Frontends/node_modules', b.WS / 'Frontends/node_modules', symlinks=True)
    style_before = pathlib.Path(before[changed[0]]['preserved_path'])
    style_after = pathlib.Path(after[changed[0]]['preserved_path'])
    difference = ''.join(difflib.unified_diff(style_before.read_text().splitlines(True), style_after.read_text().splitlines(True), fromfile=str(style_before), tofile=str(style_after)))
    (b.OUT / 'f05-style-only.patch').write_text(difference)
    b.write(b.OUT / 'revision-scope.json', {'created_at': b.stamp(), 'predecessor_manifest': b.ref(ORIGINAL_BASE / 'specialist-final-inputs.json'), 'revised_manifest': b.ref(b.WS / 'specialist-final-inputs-r1.json'), 'actual_changed_inputs': changed, 'changed_before': b.ref(style_before), 'changed_after': b.ref(style_after), 'difference': b.ref(b.OUT / 'f05-style-only.patch'), 'dependencies': 'Local copy of independently installed, byte-verified locked dependencies from previous build; no new network transfer', 'driver': b.ref(__file__)})
    for name, command in [('unit', [str(b.NPM), 'run', 'test:unit']), ('build', [str(b.NPM), 'run', 'build']), ('typecheck-compat', [str(b.NPM), 'run', 'typecheck:compat'])]:
        if b.run(name, command):
            raise SystemExit(1)
    checks, omitted, mismatches = [], [], []
    for item in revised_manifest['files'] + revised_manifest['outputs']:
        original = pathlib.Path(item['preserved_path'])
        if not original.is_file() or b.sha(original) != item['sha256']:
            mismatches.append({'path': str(original), 'kind': 'specialist_input_or_output'})
        logical = item['path']
        own = None
        if logical.startswith('workspace/'):
            relative = logical[len('workspace/'):]
            if relative == 'docs/evolution/frontend-phase4.md':
                omitted.append({'relative': relative, 'reason': 'Human handoff documentation; no compiler or test reads this file', 'specialist': b.ref(original)})
                continue
            own = b.WS / relative
        elif logical.startswith('dependencies/'):
            own = b.WS / 'Frontends/node_modules' / logical[len('dependencies/'):]
        elif logical.startswith('node-runtime/'):
            own = ORIGINAL_BASE / 'retained-node-runtime' / logical[len('node-runtime/'):]
        elif logical.startswith('static-output/'):
            own = b.WS / 'Frontends/dist' / logical[len('static-output/'):]
        if own:
            same = own.is_file() and b.sha(own) == item['sha256']
            checks.append({'specialist': str(original), 'own': str(own), 'sha256': item['sha256'], 'same': same})
            if not same:
                mismatches.append({'path': str(own), 'kind': 'independent_input_or_output'})
    outputs = [dict(b.ref(p), relative=str(p.relative_to(b.WS / 'Frontends/dist'))) for p in sorted((b.WS / 'Frontends/dist').rglob('*')) if p.is_file()]
    b.write(b.OUT / 'production-assets.json', {'created_at': b.stamp(), 'file_count': len(outputs), 'files': outputs})
    b.write(b.OUT / 'independent-build-correspondence.json', {'created_at': b.stamp(), 'manifest': b.ref(b.WS / 'specialist-final-inputs-r1.json'), 'source_snapshot': b.ref(b.OUT / 'source-snapshot.json'), 'checks': checks, 'omissions': omitted, 'mismatches': mismatches, 'assets': b.ref(b.OUT / 'production-assets.json')})
    print(json.dumps({'correspondence_count': len(checks), 'mismatches': mismatches, 'output_count': len(outputs)}), flush=True)
    assert not mismatches
    b.BASE.mkdir(parents=True, exist_ok=True)
    for name in ['phase4-independent-extra.spec.ts', 'phase4-independent-mobile.spec.ts']:
        shutil.copy2(pathlib.Path(__file__).parent / name, b.WS / 'Frontends/tests' / name)
    b.write(b.OUT / 'supplemental-tests.json', {'driver': b.ref(__file__), 'files': [b.ref(b.WS / 'Frontends/tests' / name) for name in ['phase4-independent-extra.spec.ts', 'phase4-independent-mobile.spec.ts']]})

def browser(group):
    report = b.OUT / ('browser-' + group)
    report.mkdir(parents=True, exist_ok=False)
    evidence = b.BASE / ('browser-' + group)
    phase = {'empty': 'extra', 'mobile': 'extra', 'surfaces': 'surfaces', 'precision': 'precision'}[group]
    env = {'SF_PHASE4_RUNTIME': str(b.BASE), 'SF_PHASE4_EVIDENCE': str(evidence), 'SF_PHASE4_CLOUD': 'http://127.0.0.1:19523', 'SF_PHASE4_EDGE': 'http://127.0.0.1:19524', 'SF_PHASE4_PRECISION_CLOUD': 'http://127.0.0.1:19523', 'SF_PHASE4_PRECISION_EDGE': 'http://127.0.0.1:19524', 'SF_PHASE4_CASES': phase, 'SF_PHASE4_DATA_SUFFIX': 'precision', 'SF_PHASE4_REPORT_DIR': str(report), 'SF_PHASE4_SURFACE_USER': 'admin'}
    script = {'empty': 'phase4-independent-extra.spec.ts', 'mobile': 'phase4-independent-mobile.spec.ts', 'surfaces': 'phase4.spec.ts', 'precision': 'phase4-precision.spec.ts'}[group]
    command = [str(b.NODE), str(b.WS / 'Frontends/node_modules/@playwright/test/cli.js'), 'test', '-c', 'playwright.phase4.config.ts', script]
    if group == 'empty':
        command += ['--grep', 'independent empty queries']
    if group == 'precision':
        command += ['--grep', 'preserve']
    status = b.run('browser-' + group, command, env, timeout=900)
    diagnostic = b.BASE / 'playwright-results'
    if diagnostic.exists():
        shutil.copytree(diagnostic, b.BASE / ('playwright-results-' + group))
    raise SystemExit(status)

if __name__ == '__main__':
    if sys.argv[1] == 'prepare':
        prepare()
    else:
        browser(sys.argv[1])
