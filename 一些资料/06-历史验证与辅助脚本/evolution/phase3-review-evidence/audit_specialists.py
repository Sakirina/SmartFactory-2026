#!/usr/bin/env python3
"""Read specialist evidence and compare the retained facts with current files."""
import datetime
import importlib.util
import json
from pathlib import Path

OUT = Path(__file__).resolve().parent
ROOT = OUT.parents[2]
spec = importlib.util.spec_from_file_location('review_runner', OUT / 'run_review.py')
R = importlib.util.module_from_spec(spec)
spec.loader.exec_module(R)


def resolve(path):
    path = Path(path)
    return path if path.is_absolute() else ROOT / path


def read(path):
    return json.loads(resolve(path).read_text())


def compare(entries):
    results = []
    for entry in entries:
        path = entry.get('path', entry.get('binary_path'))
        target = resolve(path)
        actual = R.sha(target) if target.is_file() else None
        results.append({'path': path, 'expected': entry['sha256'], 'actual': actual,
                        'identical': actual == entry['sha256']})
    return {'files': len(results), 'matched': sum(item['identical'] for item in results),
            'different': [item for item in results if not item['identical']]}


def references(value):
    found = []
    def walk(item):
        if isinstance(item, dict):
            if ('path' in item or 'binary_path' in item) and 'sha256' in item:
                found.append(item)
            for child in item.values():
                walk(child)
        elif isinstance(item, list):
            for child in item:
                walk(child)
    walk(value)
    return found


def command_audit(command):
    raw = resolve(command['log']['path']).read_text()
    counts = R.counts(raw)
    passed = []
    for line in raw.splitlines():
        try:
            item = json.loads(line)
        except ValueError:
            continue
        if item.get('Action') == 'pass' and item.get('Test'):
            passed.append(item['Test'])
    result = {'log': command['log'], 'counts': counts}
    if 'passed_tests_and_subtests' in command:
        result['reported_passed_names_match'] = sorted(passed) == sorted(command['passed_tests_and_subtests'])
    if 'top_level' in command:
        result['reported_top_level_match'] = counts['top_level'] == command['top_level']
    return result


def main():
    snapshots = OUT / 'initial-specialist-reports'
    snapshots.mkdir(exist_ok=True)
    reports = {}
    result = {'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'reports': {}}
    for name in ['control-execution-verified.json', 'history-analysis-verified.json', 'phase3-coordinator-findings.json']:
        original = ROOT / '.local/evolution' / name
        saved = snapshots / name
        if not saved.exists():
            saved.write_bytes(original.read_bytes())
        value = json.loads(saved.read_text())
        reports[name] = value
        result['reports'][name] = {'snapshot': str(saved.relative_to(ROOT)), 'sha256': R.sha(saved),
                                   'references': compare(references(value))}
    control = reports['control-execution-verified.json']
    history = reports['history-analysis-verified.json']
    result['control_source'] = compare(read(control['source']['final_test_and_source_manifest']['path']))
    result['control_compiled_inputs'] = compare(read(control['source']['compiled_inputs']['path']))
    result['control_binaries'] = compare(list(control['source']['binaries'].values()))
    hm = read(history['source_manifest']['path'])
    result['history_source'] = {key: compare(hm[key]) for key in ['production_inputs','test_and_fixture_inputs','module_lock_inputs','generated_contract_inputs','documentation_inputs']}
    result['history_binaries'] = compare([history['fixture_binary'], *history['tested_binaries']])
    result['control_commands'] = [command_audit(item) for item in control['commands']]
    result['history_commands'] = [command_audit(item) for item in history['checks']]
    R.json_write(OUT / 'initial-specialist-audit.json', result)
    print(json.dumps({key: value if key == 'recorded_at' else
        {k: {'files': v['references']['files'], 'different': len(v['references']['different'])} for k,v in value.items()}
        if key == 'reports' else
        {'files': value['files'], 'different': len(value['different'])} if isinstance(value,dict) and 'files' in value else
        {k: {'files': v['files'], 'different': len(v['different'])} for k,v in value.items()} if key=='history_source' else
        [{'log':item['log']['path'], 'top_level':item['counts']['top_level'], 'report_count_matches':item.get('reported_passed_names_match',item.get('reported_top_level_match'))} for item in value]
        for key,value in result.items()}, ensure_ascii=False,indent=2))


if __name__ == '__main__':
    main()
