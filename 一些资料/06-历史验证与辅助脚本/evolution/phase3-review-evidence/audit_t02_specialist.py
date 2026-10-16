import datetime
import json
import shutil
from pathlib import Path

import run_review as R

root, out = R.ROOT, R.OUT
original = root / '.local/evolution/tasks-retry-t02-verified.json'
frozen = out / 'initial-specialist-reports/tasks-retry-t02-verified.json'
if not frozen.exists():
    shutil.copyfile(original, frozen)
report = json.loads(frozen.read_text())


def path(value):
    p = Path(value)
    return p if p.is_absolute() else root / p


def ref(value):
    p = path(value)
    return {'path': str(value), 'sha256': R.sha(p), 'bytes': p.stat().st_size}


refs = []


def walk(value, where='$'):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            p = path(value['path'])
            refs.append({'at': where, **value, 'exists': p.is_file(),
                         'matches': p.is_file() and R.sha(p) == value['sha256']})
        for k, v in value.items():
            walk(v, where + '.' + k)
    elif isinstance(value, list):
        for i, v in enumerate(value):
            walk(v, where + '[' + str(i) + ']')


walk(report)
source = json.loads(path(report['verification']['final_source']['path']).read_text())['sha256']
source_rows = [{'path': p, 'expected_sha256': v,
                'matches': path(p).is_file() and R.sha(path(p)) == v}
               for p, v in source.items()]
protected = [{'path': v['path'],
              'matches': R.sha(path(v['path'])) == v['before_sha256'] == v['after_sha256']}
             for v in report['provenance']['protected_migrations_compatibility_and_previous_reports']]
counts = []
decoded = {}
for c in report['verification']['checks']:
    if not c.get('counts'):
        continue
    raw = path(c['evidence']['path']).read_text()
    actual = R.counts(raw)
    events = []
    for line in raw.splitlines():
        try:
            events.append(json.loads(line))
        except ValueError:
            pass
    decoded[c['name']] = events
    actual_equivalent = {
        'top_level_tests': actual['top_level'],
        'leaf_tests': actual['leaf_cases'],
        'all_named_tests': actual['all_named_cases'],
        'packages': actual['packages'],
        'package_elapsed_seconds': round(sum(e.get('Elapsed', 0) for e in events
                                            if not e.get('Test') and e.get('Action') in ('pass', 'fail')), 3),
        'data_race_reports': actual['data_race_occurrences'],
    }
    comparison = {k: actual_equivalent[k] == v for k, v in c['counts'].items()}
    counts.append({'name': c['name'], 'exit_code': c['exit_code'],
                   'actual_counts': actual_equivalent, 'reported_counts': c['counts'],
                   'comparisons': comparison, 'matches': all(comparison.values()),
                   'source': ref(c['evidence']['path'])})
observations = []
for value in report['verification']['observed_measurements']:
    text = ''.join(e.get('Output', '') for e in decoded[value['run']]
                   if e.get('Test') == value['test'])
    observations.append({**value, 'found_in_decoded_raw_log': value['observed'] in text})
failure_observations = []
for value in report['reproduction_and_first_failures']['failures']:
    text = ''.join(e.get('Output', '') for e in decoded[value['run']]
                   if e.get('Test') == value['test'])
    failure_observations.append({**value, 'found_in_decoded_raw_log': value['observed'] in text})
result = {
    'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'specialist_report': ref(str(frozen.relative_to(root))),
    'typed_refs': refs, 'typed_ref_count': len(refs),
    'reference_mismatches': [r for r in refs if not r['matches']],
    'source_count': len(source_rows), 'source_checks': source_rows,
    'source_mismatches': [r for r in source_rows if not r['matches']],
    'protected': protected, 'counts': counts, 'observations': observations,
    'original_failure_observations': failure_observations,
    'current_binary_independent_audit': ref(str((out / 't02-binary-independent-audit.json').relative_to(root))),
    'scope': 'All specialist report references, current final source, protected files, raw test counts and measurements independently checked. Original expected failures remain historical. Browser replay of the original cancelled River24 data is recorded separately by this reviewer.',
}
R.json_write(out / 't02-specialist-independent-audit.json', result)
summary = {
    'typed_refs': len(refs), 'reference_mismatches': len(result['reference_mismatches']),
    'source': len(source_rows), 'source_mismatches': len(result['source_mismatches']),
    'protected': len(protected), 'protected_match': all(x['matches'] for x in protected),
    'counted_commands': len(counts), 'counts_match': all(c['matches'] for c in counts),
    'observations': len(observations),
    'observations_match': all(v['found_in_decoded_raw_log'] for v in observations),
    'failure_observations': len(failure_observations),
    'failure_observations_match': all(v['found_in_decoded_raw_log'] for v in failure_observations),
}
print(json.dumps(summary, ensure_ascii=False))
if not (not result['reference_mismatches'] and not result['source_mismatches'] and
        summary['protected_match'] and summary['counts_match'] and summary['observations_match'] and
        summary['failure_observations_match']):
    raise SystemExit(1)
