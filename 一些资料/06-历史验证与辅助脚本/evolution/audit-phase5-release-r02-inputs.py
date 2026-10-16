"""Verify the final recovery candidate's saved inputs, commands and results."""
import collections
import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BASE = Path('/private/tmp/smartfactory-release-restore-recovery-20261005/candidate-r02-final')
EVIDENCE = BASE / 'evidence'
errors, checked, references = [], {}, []


def sha(path):
    p = Path(path)
    if str(p) not in checked:
        checked[str(p)] = hashlib.sha256(p.read_bytes()).hexdigest()
    return checked[str(p)]


def check(condition, kind, **detail):
    if not condition:
        errors.append({'kind': kind, **detail})


def reference(path):
    p = Path(path)
    result = {'path': str(p), 'sha256': sha(p), 'bytes': p.stat().st_size}
    references.append(result)
    return result


inputs = json.loads((EVIDENCE / 'actual-inputs.json').read_text())
by_path = {row['path']: row['sha256'] for row in inputs}
for row in inputs:
    check(sha(row['retained_copy']) == row['sha256'], 'retained_input', path=row['retained_copy'])
compiled = json.loads((EVIDENCE / 'compiled-source.json').read_text())
compiled_digest = hashlib.sha256(json.dumps(compiled['files'], ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
check(compiled_digest == compiled['source_sha256'], 'compiled_source_identity')
for name, checksum in compiled['files'].items():
    check(sha(BASE / 'source' / name) == checksum, 'fixed_source', path=name)

commands = json.loads((EVIDENCE / 'commands.json').read_text())
tests, go_lists = [], []
fields = ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles']
decoder = json.JSONDecoder()
for command in commands:
    name = command['name']
    check(command['exit_code'] == 0, 'command_exit', name=name)
    reference(command['stdout'])
    reference(command['stderr'])
    if command.get('inputs_before'):
        before = json.loads(Path(command['inputs_before']).read_text())
        after = json.loads(Path(command['inputs_after']).read_text())
        check(before == after == by_path and not command['changed_inputs'], 'guarded_inputs', name=name)
        reference(command['inputs_before'])
        reference(command['inputs_after'])
    if name.startswith('list-test-inputs'):
        raw, position, selected, packages = Path(command['stdout']).read_text(), 0, set(), 0
        while position < len(raw):
            while position < len(raw) and raw[position].isspace():
                position += 1
            if position == len(raw):
                break
            package, position = decoder.raw_decode(raw, position)
            packages += 1
            for field in fields:
                selected.update(str(Path(package['Dir']) / value) for value in package.get(field, []))
        missing = sorted(selected - set(by_path))
        check(not missing, 'go_list_correspondence', name=name, missing=missing)
        go_lists.append({'name': name, 'argv': command['argv'], 'packages': packages, 'selected_paths': len(selected), 'missing': missing})
    if '-json' in command['argv'] and command['argv'][1] == 'test':
        events = [json.loads(line) for line in Path(command['stdout']).read_text().splitlines() if line.startswith('{')]
        states = {(row.get('Package'), row['Test']): row['Action'] for row in events if row.get('Test') and row['Action'] in ('pass', 'fail', 'skip')}
        counts = dict(collections.Counter(states.values()))
        top = dict(collections.Counter(action for (_, test), action in states.items() if '/' not in test))
        skipped = []
        for (package, test), action in states.items():
            if action == 'skip':
                skipped.append({'package': package, 'test': test, 'reason': ''.join(row.get('Output', '') for row in events if row.get('Package') == package and row.get('Test') == test)})
        allowed_skips = {'TestControlledCloudEdgeControlIntervention', 'TestControlBrowserFixture', 'TestReleaseRoundtripPostgres'} if name == 'app-agent-runtime-bundle' else set()
        check(not counts.get('fail') and {row['test'] for row in skipped} == allowed_skips, 'test_outcome', name=name, counts=counts)
        tests.append({'name': name, 'all_named': counts, 'top_level': top, 'conditional_skips': skipped})

race = next(row for row in go_lists if row['name'].endswith('-race'))
check('-race' in race['argv'], 'race_input_conditions')
binaries = json.loads((EVIDENCE / 'binaries.json').read_text())
for binary in binaries:
    check(sha(binary['path']) == binary['sha256'], 'binary_hash', path=binary['path'])
    if 'build_info' in binary:
        check(binary['build_info']['sha256'] == binary['sha256'] and binary['build_info']['build']['source_sha256'] == compiled_digest, 'binary_build_identity', path=binary['path'])
for name in ('actual-inputs.json', 'compiled-source.json', 'binaries.json', 'commands.json'):
    reference(EVIDENCE / name)
result = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'passed': not errors,
    'scope': '最终发布恢复候选的固定输入与专项结果；实际五模板、往返、页面与Linux组合由各自记录确认',
    'candidate': str(BASE), 'source_sha256': compiled_digest,
    'retained_input_count': len(inputs), 'fixed_source_files': len(compiled['files']),
    'physical_files_checked': len(checked), 'commands_checked': len(commands),
    'go_list_correspondence': go_lists, 'tests': tests, 'binaries': binaries,
    'references': references, 'final_runtime_combination_verified': False, 'errors': errors,
}
(ROOT / '.local/evolution/phase5-release-r02-inputs-root-review.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({k: result[k] for k in ('passed', 'retained_input_count', 'fixed_source_files', 'commands_checked', 'tests', 'errors')}, ensure_ascii=False))
raise SystemExit(0 if not errors else 1)
