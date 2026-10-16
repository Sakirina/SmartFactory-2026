import argparse
import datetime
import hashlib
import json
from pathlib import Path
import re


parser = argparse.ArgumentParser()
parser.add_argument('directory', type=Path)
parser.add_argument('--output', required=True, type=Path)
args = parser.parse_args()
root = args.directory.resolve()
digests, findings, records = {}, [], []


def sha(path):
    path = Path(path)
    key = str(path)
    if key not in digests:
        digests[key] = hashlib.sha256(path.read_bytes()).hexdigest()
    return digests[key]


def read_stream(path):
    raw, offset, items = Path(path).read_text(), 0, []
    decoder = json.JSONDecoder()
    while offset < len(raw):
        while offset < len(raw) and raw[offset].isspace():
            offset += 1
        if offset < len(raw):
            value, offset = decoder.raw_decode(raw, offset)
            items.append(value)
    return items


fields = ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles',
          'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles',
          'EmbedFiles', 'TestGoFiles', 'XTestGoFiles', 'TestEmbedFiles',
          'XTestEmbedFiles']
outcome = json.loads((root / 'result.json').read_text())
command_file = Path(outcome.get('commands', root / 'commands.json'))
commands = json.loads(command_file.read_text())
for command in commands:
    record = dict(command)
    record['raw_log_sha256s'] = {}
    for field in ['stdout', 'stderr']:
        if command.get(field):
            record['raw_log_sha256s'][field] = sha(command[field])
    if command.get('stdout'):
        stdout = Path(command['stdout']).read_text()
        stderr = Path(command['stderr']).read_text()
        record['top_pass'] = re.findall(r'^--- PASS: (\S+)', stdout, re.M)
        record['all_pass_count'] = len(re.findall(r'^\s*--- PASS:', stdout, re.M))
        record['failed'] = re.findall(r'^\s*--- FAIL:.*', stdout, re.M)
        record['skipped'] = re.findall(r'^\s*--- SKIP:.*', stdout, re.M)
        record['race_warning'] = 'WARNING: DATA RACE' in stdout + stderr
        if command['exit_code'] == 0 and (record['failed'] or record['race_warning']):
            findings.append({'command': command['name'], 'inconsistent_success': True})
    if command.get('source_before'):
        manifests = {phase: json.loads(Path(command['source_' + phase]).read_text())
                     for phase in ['before', 'after']}
        maps = {phase: {item['path']: item['sha256'] for item in entries}
                for phase, entries in manifests.items()}
        record['verified_before_after_equal'] = maps['before'] == maps['after']
        record['input_count'] = len(maps['before'])
        record['input_manifest_sha256s'] = {phase: sha(command['source_' + phase])
                                           for phase in manifests}
        record['go_list_coverage'] = []
        if not record['verified_before_after_equal']:
            findings.append({'command': command['name'], 'inputs_changed': True})
        for phase, entries in manifests.items():
            lookup = {str(Path(item['path']).resolve()) for item in entries}
            for item in entries:
                archived = Path(item.get('archive', item.get('archived_path', '')))
                if sha(archived) != item['sha256'] or archived.stat().st_size != item['bytes']:
                    findings.append({'archive_mismatch': str(archived)})
                if '/DataTransfer/.cache/' in item['path']:
                    findings.append({'unrelated_cache': item['path']})
            package_file = Path(command['source_' + phase]).parent / (command['name'] + '-' + phase + '.go-list.json')
            packages = read_stream(package_file)
            missing, package_errors = [], []
            for package in packages:
                if package.get('Error') or package.get('DepsErrors'):
                    package_errors.append(package.get('ImportPath'))
                if not package.get('Dir'):
                    continue
                used_fields = fields if 'test' in command.get('argv', []) else [field for field in fields if 'Test' not in field]
                for field in used_fields:
                    for filename in package.get(field, []):
                        path = Path(filename)
                        if not path.is_absolute():
                            path = Path(package['Dir']) / path
                        if str(path.resolve()) not in lookup:
                            missing.append({'package': package['ImportPath'],
                                            'field': field, 'path': str(path)})
            record['go_list_coverage'].append({'phase': phase,
                'path': str(package_file), 'sha256': sha(package_file),
                'packages': len(packages), 'missing': missing,
                'package_errors': package_errors})
            if missing or package_errors:
                findings.append({'command': command['name'], 'phase': phase,
                                 'missing_count': len(missing), 'package_errors': package_errors})
    records.append(record)
binaries = []
binary_file = Path(outcome.get('binaries', root / 'binaries.json'))
if binary_file.exists():
    binaries = json.loads(binary_file.read_text())
    for binary in binaries:
        if sha(binary['path']) != binary['sha256']:
            findings.append({'binary_mismatch': binary['path']})
if outcome['status'] == 'passed' and any(record['exit_code'] != 0 for record in records):
    findings.append({'summary_claims_pass_despite_failed_command': True})
artifacts = {}
for field in ['source_inputs', 'http', 'assertions', 'schema_checks']:
    if not isinstance(outcome.get(field), str):
        continue
    path = Path(outcome[field])
    entries = json.loads(path.read_text())
    artifact = {'path': str(path), 'sha256': sha(path), 'records': len(entries)}
    if field == 'source_inputs':
        for item in entries:
            archived = Path(item.get('archive', item.get('archived_path', '')))
            if sha(archived) != item['sha256'] or archived.stat().st_size != item['bytes']:
                findings.append({'final_input_archive_mismatch': str(archived)})
    elif field == 'assertions':
        artifact['passed'] = sum(item.get('status') == 'passed' for item in entries)
        artifact['process_exits'] = sum('exit_code' in item for item in entries)
        invalid = [item for item in entries if item.get('status') != 'passed' and item.get('exit_code') != 0]
        if outcome['status'] == 'passed' and invalid:
            findings.append({'invalid_assertions': invalid})
    elif field == 'schema_checks':
        if any(item.get('status') != 'passed' for item in entries):
            findings.append({'schema_failure': True})
    elif field == 'http':
        artifact['status_counts'] = {str(status): sum(item.get('status') == status for item in entries)
                                    for status in sorted({item.get('status') for item in entries})}
    artifacts[field] = artifact
report = {'schema_version': 1,
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'status': 'passed' if not findings else 'needs_review',
    'scope': 'Evidence integrity, actual go-list coverage, archived inputs and raw-log recount',
    'test_outcome': outcome['status'], 'source_directory': str(root),
    'result_sha256': sha(root / 'result.json'),
    'commands_sha256': sha(command_file),
    'commands': records, 'binaries': binaries,
    'artifacts': artifacts, 'unique_hashed_files': len(digests), 'findings': findings}
args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'status': report['status'], 'test_outcome': report['test_outcome'],
    'commands': len(records), 'unique_hashed_files': len(digests), 'artifacts': artifacts,
    'checks': [{'name': item['name'], 'exit_code': item['exit_code'],
                'inputs': item.get('input_count'), 'top_pass': len(item.get('top_pass', [])),
                'all_pass': item.get('all_pass_count'), 'failed': item.get('failed', [])}
              for item in records], 'findings': findings}, ensure_ascii=False))
raise SystemExit(0 if not findings else 1)
