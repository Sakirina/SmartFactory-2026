#!/usr/bin/env python3
"""Verify saved business/native compilation-input hashes without executing tests."""
import collections
import datetime
import hashlib
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
MODULE_CACHE = Path('/private/tmp/smartfactory-evolution-gomodcache')
BUILD_CACHE = Path('/private/tmp/smartfactory-evolution-go-cache')


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for part in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(part)
    return value.hexdigest()


def escaped_module_path(value):
    return ''.join('!' + c.lower() if 'A' <= c <= 'Z' else c for c in value)


def main():
    goroot = Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip())
    results = []
    for name in ('native-actual-inputs.json', 'final-actual-inputs.json'):
        path = ROOT / '.local/evolution/business-evidence' / name
        doc = json.loads(path.read_text())
        checked = collections.Counter()
        failures = []
        for label, value in doc['inputs'].items():
            if value['local']:
                kind = 'local'
                source = Path(doc['root']) / label
            elif label.startswith('toolchain/'):
                kind = 'toolchain'
                source = goroot / label.removeprefix('toolchain/')
            elif label.startswith('module/'):
                kind = 'module'
                module, _, rest = label.removeprefix('module/').partition('@')
                version, _, relative = rest.partition('/')
                source = MODULE_CACHE / escaped_module_path(module + '@' + version) / relative
            elif label.startswith('generated/'):
                kind = 'generated'
                filename = Path(label).name
                source = BUILD_CACHE / filename[:2] / filename
            else:
                failures.append({'label': label, 'reason': 'unknown provenance label'})
                continue
            actual = digest(source) if source.is_file() else None
            checked[kind] += 1
            if actual != value['sha256']:
                failures.append({'label': label, 'resolved_path': str(source), 'expected': value['sha256'], 'actual': actual})
        if sum(checked.values()) != doc['input_count'] or checked['local'] != doc['local_input_count']:
            failures.append({'reason': 'manifest input count mismatch'})
        results.append({'manifest': {'path': str(path), 'sha256': digest(path)}, 'snapshot': doc['root'], 'input_selection': doc['commands'], 'checked': dict(checked), 'reported_package_count': doc['package_count'], 'errors': failures})
    errors = sum(len(result['errors']) for result in results)
    result = {'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'verified' if not errors else 'findings', 'scope': 'Saved native and final business/query specialist source inputs; AI files intentionally excluded from final business snapshot as recorded by coordinator', 'results': results}
    output = ROOT / '.local/evolution/phase4-business-inputs-root-review.json'
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(result, ensure_ascii=False))
    return bool(errors)


if __name__ == '__main__':
    raise SystemExit(main())
