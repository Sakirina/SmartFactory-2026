#!/usr/bin/env python3
"""Coordinator read-only verification of saved query test/build evidence."""
import collections
import datetime
import hashlib
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
EVIDENCE = ROOT / '.local/evolution/query-evidence'


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for part in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(part)
    return value.hexdigest()


def read(path):
    return json.loads(path.read_text())


def reference(path):
    return {'path': str(path), 'sha256': digest(path)}


def main():
    errors = []
    goroot = Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip()).resolve()
    raw = (EVIDENCE / 'final-go-list.jsonstream').read_text()
    decoder = json.JSONDecoder()
    external = {}
    packages = 0
    fields = ('GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles')
    while raw.strip():
        pkg, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        packages += 1
        directory = Path(pkg.get('Dir', '.'))
        module = pkg.get('Module') or {}
        for field in fields:
            for name in pkg.get(field, []):
                path = (directory / name).resolve()
                if not path.is_file() or path.is_relative_to(ROOT):
                    continue
                if path.is_relative_to(goroot):
                    label = 'toolchain/' + str(path.relative_to(goroot))
                elif module.get('Dir') and path.is_relative_to(Path(module['Dir'])):
                    label = 'module/' + module['Path'] + '@' + module.get('Version', 'local') + '/' + str(path.relative_to(Path(module['Dir'])))
                else:
                    label = 'generated/' + hashlib.sha256(str(path).encode()).hexdigest() + '/' + path.name
                external[label] = path
    manifests = []
    expected_manifests = set()
    for name in ('final-input-manifest-initial.json', 'final-input-manifest-v2.json'):
        path = EVIDENCE / name
        doc = read(path)
        expected_manifests.add(digest(path))
        checked = collections.Counter()
        different = []
        for label, item in doc['inputs'].items():
            source = Path(doc['snapshot']) / label if item['local'] else external.get(label)
            group = 'local' if item['local'] else label.split('/')[0]
            checked[group] += 1
            actual = digest(source) if source and source.is_file() else None
            if actual != item['sha256']:
                different.append({'label': label, 'expected': item['sha256'], 'actual': actual})
        manifests.append({'manifest': reference(path), 'snapshot': doc['snapshot'], 'checked': dict(checked), 'different': different})
        errors.extend(different)
        if doc['package_count'] != packages or doc['input_count'] != sum(checked.values()):
            errors.append({'kind': 'manifest_count', 'manifest': name})
    runs_path = EVIDENCE / 'final-verification-runs.json'
    runs_doc = read(runs_path)
    binaries = []
    for path, expected in runs_doc['binaries'].items():
        actual = digest(Path(path)) if Path(path).is_file() else None
        same = actual == expected['sha256']
        binaries.append({'path': path, 'sha256': actual, 'identical': same})
        if not same:
            errors.append({'kind': 'binary_digest', 'path': path})
    runs = []
    for item in runs_doc['runs']:
        log = ROOT / item['log']
        same = log.is_file() and digest(log) == item['log_sha256']
        if not same or item['input_manifest_sha256'] not in expected_manifests:
            errors.append({'kind': 'run_reference', 'name': item['name']})
        counts = collections.Counter()
        failures = []
        if log.suffix == '.jsonl':
            for line in log.read_text().splitlines():
                event = json.loads(line)
                if event.get('Action') in ('pass', 'fail', 'skip'):
                    kind = 'package' if 'Test' not in event else 'subtest' if '/' in event['Test'] else 'top_level'
                    counts[kind + '_' + event['Action']] += 1
                    if event['Action'] == 'fail':
                        failures.append(event.get('Test', event.get('Package')))
        historical_failure = item['name'] == 'final-control'
        if item['exit_code'] != (1 if historical_failure else 0):
            errors.append({'kind': 'unexpected_exit', 'name': item['name']})
        if item.get('executed_binary_sha256') and not any(b['sha256'] == item['executed_binary_sha256'] for b in binaries):
            errors.append({'kind': 'unmatched_executed_binary', 'name': item['name']})
        runs.append({'name': item['name'], 'exit_code': item['exit_code'], 'log': reference(log), 'digest_identical': same, 'counts': dict(counts), 'failures': failures, 'historical_failure': historical_failure})
    result = {'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'verified' if not errors else 'findings', 'scope': 'Fixed query snapshots and actual recorded build/test inputs; later business changes are evaluated in subsequent combined verification', 'go_list': reference(EVIDENCE / 'final-go-list.jsonstream'), 'package_count': packages, 'manifests': manifests, 'run_manifest': reference(runs_path), 'binaries': binaries, 'runs': runs, 'errors': errors}
    output = ROOT / '.local/evolution/phase4-query-inputs-root-review.json'
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({'status': result['status'], 'packages': packages, 'snapshots': [{k: m[k] for k in ('snapshot', 'checked', 'different')} for m in manifests], 'binaries': len(binaries), 'runs': [{'name': r['name'], 'exit_code': r['exit_code'], 'counts': r['counts']} for r in runs], 'errors': errors}, ensure_ascii=False))
    return bool(errors)


if __name__ == '__main__':
    raise SystemExit(main())
