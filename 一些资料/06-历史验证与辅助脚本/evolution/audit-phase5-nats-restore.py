import datetime
import difflib
import hashlib
import json
from pathlib import Path

root = Path('/path/to/SmartFactory-2026')
directory = Path('/private/tmp/smartfactory-delivery-nats-cold-restore-final-20261005')
report = json.loads((directory / 'verification.json').read_text())
archive = Path('/private/tmp/smartfactory-phase5-nats-coordinator-evidence-20261005')
archive.mkdir(exist_ok=True)
(archive / 'inputs').mkdir(exist_ok=True)
findings = []
refs = []


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def check(condition, message):
    if not condition:
        findings.append(message)


def ref(path):
    path = Path(path)
    result = {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}
    refs.append(result)
    return result


before = json.loads((directory / 'compile-inputs-before.json').read_text())
after = json.loads((directory / 'compile-inputs-after.json').read_text())
check(before == after, 'build/run input manifests differ')
for field in ['compile_inputs_before', 'compile_inputs_after', 'tested_binary']:
    item = report[field]
    check(sha(item['path']) == item['sha256'], 'reported digest differs: ' + field)
    ref(item['path'])
build = report['build']
check(build['argv'] == ['go', 'test', '-overlay', str(directory / 'overlay.json'), '-c', '-p', '1', '-o', str(directory / 'coordination.test'), './internal/coordination'], 'recorded build invocation differs')
check(build['cwd'] == str(root / 'Platform'), 'recorded build working directory differs')
check(sha(build['log']) == build['log_sha256'], 'build log digest differs')
check(sha(build['original_verification']) == build['original_verification_sha256'], 'prior verification record differs')
ref(build['log'])
ref(build['original_verification'])

previous = json.loads(Path('/private/tmp/smartfactory-phase5-protocol-coordinator-evidence-20261005/retained-inputs.json').read_text())
preserved = {(item['path'], item['sha256']): item['retained_copy'] for item in previous}
retained, reused, created = [], 0, 0
for logical, item in before.items():
    actual = Path(item['path'])
    check(sha(actual) == item['sha256'], 'current effective input changed: ' + str(actual))
    candidate = preserved.get((str(actual), item['sha256']))
    if candidate and Path(candidate).is_file() and sha(candidate) == item['sha256']:
        copy = Path(candidate)
        reused += 1
    else:
        copy = archive / 'inputs' / item['sha256']
        if not copy.exists():
            copy.write_bytes(actual.read_bytes())
        check(sha(copy) == item['sha256'], 'preserved input differs: ' + str(actual))
        created += 1
    retained.append({'logical_path': logical, **item, 'retained_copy': str(copy)})
(archive / 'retained-inputs.json').write_text(json.dumps(retained, ensure_ascii=False, indent=2) + '\n')

decoder, packages, position = json.JSONDecoder(), [], 0
raw = (directory / 'go-list-inputs.jsonl').read_text()
while position < len(raw):
    while position < len(raw) and raw[position].isspace():
        position += 1
    if position >= len(raw):
        break
    value, position = decoder.raw_decode(raw, position)
    packages.append(value)
fields = ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles', 'TestGoFiles', 'XTestGoFiles', 'TestEmbedFiles', 'XTestEmbedFiles']
missing = []
for package in packages:
    check(not package.get('Error') and not package.get('DepsErrors'), 'go list error: ' + package.get('ImportPath', 'unknown'))
    base = Path(package.get('Dir', ''))
    for field in fields:
        for name in package.get(field, []):
            path = str(base / name)
            if path not in before:
                missing.append({'package': package.get('ImportPath'), 'field': field, 'path': path})
check(not missing, 'go list input coverage incomplete')
ref(directory / 'go-list-inputs.jsonl')

adaptation = report['test_input_adaptation']
phase4 = json.loads((root / '.local/evolution/phase4-preserved-paths.json').read_text())
old = next(item for item in phase4['files'] if item['original'] == adaptation['original'])
check(sha(old['preserved']) == adaptation['original_sha256'], 'pre-adaptation source differs')
old_text = Path(old['preserved']).read_text()
new_text = Path(adaptation['effective']).read_text()
check(new_text == old_text.replace('\t\texpected.Params["migration"] = "upgraded-server"\n', '', 1), 'test adaptation differs from authorized single-line removal')
check(sha(adaptation['original']) == adaptation['effective_sha256'], 'working source does not match effective fixture')
overlay = json.loads(Path(adaptation['overlay']).read_text())
check(overlay['Replace'] == {adaptation['original']: adaptation['effective']}, 'unexpected overlay replacement')
diff = ''.join(difflib.unified_diff(old_text.splitlines(True), new_text.splitlines(True), fromfile='previous-fixture', tofile='current-fixture'))
(archive / 'fixture-change.diff').write_text(diff)
ref(adaptation['overlay'])
ref(adaptation['effective'])
ref(old['preserved'])

source = Path(report['source_directory'])
cold = source / 'source-cold-backup'
cold_manifest = json.loads((cold / 'manifest.json').read_text())
check(sha(cold / 'manifest.json') == report['cold_manifest_sha256'], 'cold manifest digest differs')
check(cold_manifest == report['restored_files_before_start'], 'restored files differ from cold manifest')
for item in cold_manifest:
    check(sha(cold / item['path']) == item['sha256'], 'cold backup file differs: ' + item['path'])
ref(cold / 'manifest.json')

initial = json.loads((directory / 'restored_old_backup-checkpoint.json').read_text())
advanced = json.loads((directory / 'restored_takeover-checkpoint.json').read_text())
original = json.loads((source / 'source-cold-backup_restart-checkpoint.json').read_text())
check(initial['execution'] == original['execution'], 'restored initial execution differs')
check(initial['checkpoint_revision'] == original['checkpoint_revision'] == 1, 'initial checkpoint revision differs')
check(initial['checkpoint_sha256'] == original['checkpoint_sha256'], 'initial checkpoint signature digest differs')
check(advanced['execution']['fence'] > initial['execution']['fence'], 'execution fence did not advance')
check(advanced['checkpoint_revision'] == 2 and len(advanced['execution']['steps']) == 2, 'takeover did not add expected step/revision')
check(initial['execution']['params'] == advanced['execution']['params'], 'immutable request parameters changed')
for record in [initial, advanced]:
    check(all(record['checks'].values()), 'checkpoint authentication or replica check failed')
    for stream in record['streams']:
        check(stream['config']['num_replicas'] == 3 and stream['config']['storage'] == 'file', 'invalid stream persistence/replication')
        check(len(stream['cluster']['replicas']) == 2 and all(item['current'] for item in stream['cluster']['replicas']), 'stream follower not current')
for before_stream, original_stream in zip(initial['streams'], original['streams']):
    check(before_stream['config'] == original_stream['config'], 'restored stream configuration differs')
    for field in ['messages', 'bytes', 'first_seq', 'last_seq']:
        check(before_stream['state'][field] == original_stream['state'][field], 'restored stream state differs: ' + field)
for name in ['restored_old_backup-checkpoint.json', 'restored_takeover-checkpoint.json', 'restored_old_backup-members.json']:
    ref(directory / name)

commands = []
for command in report['commands']:
    output = directory / command['output']
    text = output.read_text()
    if command['exit_code']:
        check(command['output'] == 'command-002.txt' and 'context deadline exceeded' in text, 'unexplained failed command')
    if command['output'] in ['command-003.txt', 'command-007.txt']:
        check('--- PASS: TestPersistentThreeReplicaUpgrade' in text and '\nPASS\n' in text, 'successful test missing PASS log')
    commands.append({'exit_code': command['exit_code'], 'output': ref(output)})
for name, state in report['final_container_states'].items():
    check(state['ExitCode'] == 0 and not state['OOMKilled'] and not state['Running'], 'container did not exit normally: ' + name)
for relative, value in report['retained_identity_material'].items():
    check(value['sha256'] == value['source_sha256'], 'identity material changed: ' + relative)
check(all(report['checks'].values()) and report['temporary_project_removed'], 'specialist checks or cleanup incomplete')

result = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'passed' if not findings else 'needs_review',
    'report': ref(directory / 'verification.json'),
    'input_count': len(before), 'actual_packages': len(packages),
    'build_provenance': build,
    'before_after_equal': before == after, 'missing_inputs': missing,
    'retained_inputs': ref(archive / 'retained-inputs.json'),
    'reused_retained_inputs': reused, 'new_retained_inputs': created,
    'test_adaptation': ref(archive / 'fixture-change.diff'),
    'backup_files': len(cold_manifest),
    'old_checkpoint': {'revision': initial['checkpoint_revision'], 'fence': initial['execution']['fence']},
    'new_checkpoint': {'revision': advanced['checkpoint_revision'], 'fence': advanced['execution']['fence']},
    'commands': commands, 'final_container_states': report['final_container_states'],
    'fixture_resources': report['fixture_resources'], 'backup_cutoff': report['backup_cutoff'],
    'references': refs, 'findings': findings,
}
target = root / '.local/evolution/phase5-nats-cold-restore-root-review.json'
target.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: result[key] for key in ['status', 'input_count', 'actual_packages', 'before_after_equal', 'reused_retained_inputs', 'new_retained_inputs', 'backup_files', 'old_checkpoint', 'new_checkpoint', 'findings']}, ensure_ascii=False))
raise SystemExit(0 if not findings else 1)
