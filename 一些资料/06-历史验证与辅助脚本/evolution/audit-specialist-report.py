import argparse
import datetime
import hashlib
import json
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument('report', type=Path)
parser.add_argument('--output', required=True, type=Path)
args = parser.parse_args()
source = args.report.resolve()
payload = json.loads(source.read_text())
references, findings, cache = [], [], {}


def digest(path):
    key = str(path)
    if key not in cache:
        content = path.read_bytes()
        cache[key] = (hashlib.sha256(content).hexdigest(), len(content))
    return cache[key]


def walk(value, pointer='$'):
    if isinstance(value, dict):
        if (isinstance(value.get('path'), str)
                and isinstance(value.get('sha256'), str)
                and len(value['sha256']) == 64):
            path = Path(value['path']).resolve()
            record = {'pointer': pointer, 'path': str(path),
                      'expected_sha256': value['sha256']}
            if not path.is_file():
                findings.append({'pointer': pointer, 'path': str(path), 'error': 'missing'})
            else:
                actual, size = digest(path)
                record.update(sha256=actual, bytes=size)
                if actual != value['sha256']:
                    findings.append({'pointer': pointer, 'path': str(path),
                                     'error': 'sha256', 'expected': value['sha256'],
                                     'actual': actual})
                for field in ['bytes', 'size']:
                    if isinstance(value.get(field), int) and value[field] != size:
                        findings.append({'pointer': pointer, 'path': str(path),
                                         'error': field, 'expected': value[field],
                                         'actual': size})
            references.append(record)
        for key, child in value.items():
            walk(child, pointer + '.' + key)
    elif isinstance(value, list):
        for index, child in enumerate(value):
            walk(child, pointer + '[' + str(index) + ']')


walk(payload)
result = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'scope': 'Every structured path/sha256 reference in the specialist report; raw execution and actual-input coverage are separate coordinator records',
    'report': str(source),
    'report_sha256': digest(source)[0],
    'reported_status': payload.get('status'),
    'status': 'passed' if not findings else 'needs_review',
    'reference_count': len(references),
    'unique_files': len(cache),
    'references': references,
    'findings': findings,
}
args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: result[key] for key in ['status', 'reported_status',
      'reference_count', 'unique_files', 'findings']}, ensure_ascii=False))
raise SystemExit(0 if not findings else 1)
