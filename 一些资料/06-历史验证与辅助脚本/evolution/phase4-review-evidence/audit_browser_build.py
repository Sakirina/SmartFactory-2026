#!/usr/bin/env python3
import datetime
import hashlib
import json
import pathlib
import shutil

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
BASE = pathlib.Path('/private/tmp/smartfactory-phase4-independent-browser-20261005')
OUT = ROOT / '.local/evolution/phase4-review-browser'
WS = BASE / 'frontend'
NODE_ROOT = ROOT / '.local/toolchains/node-v24.21.0-darwin-arm64'

def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def ref(path):
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

for name in ['final-inputs.json', 'freeze.json']:
    dest = BASE / ('specialist-' + name)
    if dest.exists():
        raise RuntimeError('Preserved specialist report already exists')
    shutil.copy2(ROOT / '.local/evolution/frontend-phase4' / name, dest)
manifest_path = BASE / 'specialist-final-inputs.json'
specialist = json.loads(manifest_path.read_text())
retained_node = BASE / 'retained-node-runtime'
shutil.copytree(NODE_ROOT, retained_node, symlinks=True, ignore=shutil.ignore_patterns('._*', '.DS_Store'))
copied = json.loads((OUT / 'source-snapshot.json').read_text())['files']
source = {item['relative']: item for item in copied}
checks, omissions, mismatches = [], [], []
for item in specialist['files'] + specialist['outputs']:
    path = pathlib.Path(item['preserved_path'])
    valid = path.exists() and sha(path) == item['sha256']
    if not valid:
        mismatches.append({'path': str(path), 'kind': 'specialist_fixed_input'})
    own = None
    relative = item['path']
    if relative.startswith('workspace/'):
        relative = relative[len('workspace/'):]
        own = WS / relative
        if relative == 'docs/evolution/frontend-phase4.md':
            omissions.append({'relative': relative, 'reason': 'Human handoff documentation; no compiler or test reads this file', 'specialist': ref(path)})
            continue
    elif relative.startswith('static-output/'):
        relative = relative[len('static-output/'):]
        own = WS / 'Frontends/dist' / relative
    elif relative.startswith('dependencies/'):
        relative = relative[len('dependencies/'):]
        own = WS / 'Frontends/node_modules' / relative
    elif relative.startswith('node-runtime/'):
        relative = relative[len('node-runtime/'):]
        own = retained_node / relative
    if own:
        same = own.is_file() and sha(own) == item['sha256']
        checks.append({'specialist': str(path), 'own': str(own), 'sha256': item['sha256'], 'same': same})
        if not same:
            mismatches.append({'path': str(own), 'kind': 'independent_rebuild_input_or_output'})

echarts = WS / 'Frontends/node_modules/echarts'
package = json.loads((echarts / 'package.json').read_text())
lines = echarts / 'lib/chart/lines/LinesSeries.js'
value = lines.read_text()
start = value.index('LinesSeriesModel.prototype.formatTooltip')
body = value[start:value.index('LinesSeriesModel.prototype', start + 20)]
patch_assertions = {'resolved_version_is_6_1_0': package['version'] == '6.1.0', 'lines_formatter_uses_structured_markup': "createTooltipMarkup('nameValue'" in body, 'raw_name_return_removed': 'return name;' not in body and 'return itemName;' not in body}
assert all(patch_assertions.values()), patch_assertions
summary = {'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'specialist_manifest': ref(manifest_path), 'specialist_freeze': ref(BASE / 'specialist-freeze.json'), 'specialist_input_count': len(specialist['files']), 'specialist_output_count': len(specialist['outputs']), 'independent_source': ref(OUT / 'source-snapshot.json'), 'independent_dependencies': ref(OUT / 'installed-dependencies.json'), 'independent_production_assets': ref(OUT / 'production-assets.json'), 'correspondence': checks, 'omissions': omissions, 'mismatches': mismatches, 'echarts': {'package': ref(echarts / 'package.json'), 'formatter': ref(lines), 'assertions': patch_assertions, 'upstream_sources': ref(ROOT / '.local/evolution/phase4-review-a02/echarts-upstream-review.json')}}
(OUT / 'independent-build-correspondence.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'checks': len(checks), 'mismatches': mismatches, 'omitted_non_compiler_documents': len(omissions), 'echarts_assertions': patch_assertions}))
assert not mismatches
