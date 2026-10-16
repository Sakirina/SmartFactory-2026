#!/usr/bin/env python3
"""Coordinator review of the specialist's task cancellation evidence."""
from collections import Counter
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import socket

ROOT = Path(__file__).resolve().parents[2]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def resolve(path):
    item = Path(path)
    return item if item.is_absolute() else ROOT / item


def read(path):
    return json.loads(resolve(path).read_text())


def count_log(path):
    events = []
    for line in resolve(path).read_text().splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    terminal = [e for e in events if e.get('Action') in ('pass', 'fail', 'skip')]
    named = [e for e in terminal if e.get('Test')]
    names = {(e['Package'], e['Test']) for e in named}
    groups = {
        'top_level': [e for e in named if '/' not in e['Test']],
        'leaf_cases': [e for e in named if not any(
            p == e['Package'] and t.startswith(e['Test'] + '/') for p, t in names)],
        'all_named_cases': named,
        'packages': [e for e in terminal if not e.get('Test')],
    }
    counts = {}
    for key, values in groups.items():
        c = Counter(e['Action'] for e in values)
        counts[key] = {a: c[a] for a in ('pass', 'fail', 'skip')}
    return counts, events


def main():
    path = ROOT / '.local/evolution/tasks-cancel-t01-verified.json'
    report = read(path)
    spec = importlib.util.spec_from_file_location(
        'evidence_audit', ROOT / '.local/evolution/phase2-review-evidence/audit_evidence.py')
    audit = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(audit)
    references = audit.audit_report(path.name)
    commands = []
    raw_by_name = {}
    for check in report['verification']['checks']:
        entry = {'name': check['name'], 'exit_code': check['exit_code']}
        raw = resolve(check['evidence']['path']).read_text()
        entry['raw_sha256_matches'] = digest(resolve(check['evidence']['path'])) == check['evidence']['sha256']
        entry['data_race_report'] = 'WARNING: DATA RACE' in raw
        raw_by_name[check['name']] = raw
        if 'counts' in check:
            counts, events = count_log(check['evidence']['path'])
            raw_by_name[check['name']] = ''.join(e.get('Output', '') for e in events)
            entry['actual_counts'] = counts
            entry['reported_counts_match'] = counts == check['counts']
        commands.append(entry)
    observations = report['verification']['observed_confirmation_and_progress']
    unmatched_observations = [v['test'] for v in observations
                              if v['observed'] not in raw_by_name[v['run']]]
    source_record = read(report['verification']['source_before_commands']['path'])
    source_differences = [p for p, sha in source_record['source_sha256'].items()
                          if digest(resolve(p)) != sha]
    protected = report['provenance']['preserved_migration_compatibility_and_original_reports']
    changed_protected = [v['path'] for v in protected if not (
        v['before_sha256'] == v['after_sha256'] == digest(resolve(v['path'])))]
    contracts = report['provenance']['contract_integrity']
    changed_contracts = [v['path'] for v in contracts if digest(resolve(v['path'])) != v['sha256']]
    manifest = read(report['binary_handoff']['manifest']['path'])
    build_differences = [p for p, v in manifest['build_inputs'].items()
                         if digest(resolve(p)) != v['sha256']]
    with socket.socket() as client:
        client.settimeout(0.5)
        old_port_closed = client.connect_ex(('127.0.0.1', 19071)) != 0
    results = {
        'created_at': datetime.now(timezone.utc).isoformat(),
        'report': {'path': str(path.relative_to(ROOT)), 'sha256': digest(path)},
        'scope': 'Specialist evidence and current source verification; final independent browser acceptance is recorded separately',
        'references': references,
        'commands': commands,
        'quoted_observations': {'checked': len(observations), 'unmatched': unmatched_observations},
        'verification_source': {'count': len(source_record['source_sha256']), 'different': source_differences},
        'protected_files': {'count': len(protected), 'different': changed_protected},
        'contracts': {'count': len(contracts), 'different': changed_contracts},
        'current_binary_compilation_inputs': {'count': len(manifest['build_inputs']), 'different': build_differences},
        'old_fixture_port_19071_closed': old_port_closed,
        'source_scope_review': '.local/evolution/tasks-cancel-t01-source-scope-root-review.json',
        'binary_review': '.local/evolution/tasks-cancel-t01-binary-root-review.json',
    }
    problems = bool(references['different'] or references['missing_references'] or
                    unmatched_observations or source_differences or changed_protected or
                    changed_contracts or build_differences or not old_port_closed or
                    any(c['exit_code'] != 0 or not c['raw_sha256_matches'] or
                        c['data_race_report'] or c.get('reported_counts_match') is False for c in commands))
    results['status'] = 'passed_specialist_evidence_review' if not problems else 'differences_require_review'
    (ROOT / '.local/evolution/tasks-cancel-t01-root-review.json').write_text(
        json.dumps(results, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({k: v for k, v in results.items() if k not in ('commands',)},
                     ensure_ascii=False, indent=2))
    print(json.dumps({'commands': commands}, ensure_ascii=False))
    if problems:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
