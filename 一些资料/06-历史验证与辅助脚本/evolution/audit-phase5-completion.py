#!/usr/bin/env python3
"""Verify final requirement records, accepted source copies and delivery identity."""
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
ERRORS = []
REFERENCES = []


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def ref(path):
    path = ROOT / path
    result = {'path': str(path), 'sha256': digest(path), 'bytes': path.stat().st_size}
    REFERENCES.append(result)
    return result


def read(path):
    ref(path)
    return json.loads((ROOT / path).read_text())


def check(condition, detail):
    if not condition:
        ERRORS.append(detail)


def structured_refs(value):
    if isinstance(value, dict):
        if isinstance(value.get('path'), str) and isinstance(value.get('sha256'), str):
            path = ROOT / value['path']
            check(path.is_file(), 'Missing reference: ' + str(path))
            if path.is_file():
                check(digest(path) == value['sha256'], 'Reference changed: ' + str(path))
        for child in value.values():
            structured_refs(child)
    elif isinstance(value, list):
        for child in value:
            structured_refs(child)


def main():
    acceptance = read('docs/evolution/acceptance.json')
    team = read('docs/evolution/team.json')
    findings = read('.local/evolution/phase5-coordinator-findings.json')
    watches = read('.local/evolution/phase5-acceptance-watchpoints.json')
    coordinator = read('.local/evolution/phase5-final-root-review.json')
    independent = read('.local/evolution/final-independent-review.json')
    delivery = read('.local/evolution/final-delivery-verified.json')
    audit = read('.local/evolution/acceptance-record-audit.json')
    pointer = read('.local/evolution/phase5-accepted-source.json')
    manifest = read(pointer['source_manifest']['path'])
    preserved = read(pointer['preserved_paths'])
    check(acceptance['overall_status'] == team['overall_status'] == 'complete', 'Overall record status')
    check(len(acceptance['requirements']) == 45, 'Requirement count')
    check(all(x['verification_status'] == 'passed' and x['implementation_status'] == 'complete' for x in acceptance['requirements']), 'Unfinished requirement')
    check(all(x['status'] == 'accepted' for x in team['phases']), 'Unaccepted phase')
    check(all(len(x['created']) <= team['limits']['created_per_phase'] for x in team['phases']), 'Created-role limit')
    check(len(findings['items']) == 14 and findings['all_findings_resolved'] and all(x['status'] == 'resolved' for x in findings['items']), 'Unresolved finding')
    check(len(watches['watchpoints']) == 14 and watches['all_verified'] and all(x['status'] == 'verified' for x in watches['watchpoints']), 'Unverified watchpoint')
    check(coordinator['status'] == independent['status'] == delivery['status'] == 'passed', 'Final report status')
    source_sha = delivery['deliverable']['source_sha256']
    check(source_sha == independent['source_sha256'] == coordinator['source_sha256'] == acceptance['final_review']['source_sha256'] == pointer['public_delivery_source_sha256'], 'Source identity differs')
    check(audit['record_consistency_passed'] and audit['all_requirements_passed'] and audit['source_sha256'] == digest(ROOT / 'docs/evolution/acceptance.json'), 'Acceptance audit no longer current')
    check(pointer['status'] == 'accepted_and_preserved' and manifest['status'] == 'verified', 'Snapshot status')
    check(manifest['count'] == pointer['source_files'] == 909, 'Snapshot source count')
    check(len(manifest['phase_records']) == pointer['phase_records'] == 52, 'Phase-record count')
    check(preserved['source_count'] == 909 and preserved['frontend_dist_count'] == pointer['frontend_dist_files'] == 19, 'Preserved-path count')
    for name, item in manifest['files'].items():
        check(digest(ROOT / name) == item['sha256'], 'Current source changed: ' + name)
    for name, item in manifest['phase_records'].items():
        check(digest(ROOT / name) == item['sha256'] == digest(Path(item['path'])), 'Phase record differs: ' + name)
    for item in preserved['files']:
        check(digest(Path(item['original'])) == item['sha256'] == digest(Path(item['preserved'])), 'Preserved source or frontend differs: ' + item['original'])
    for value in [acceptance['final_review'], coordinator, pointer]:
        structured_refs(value)
    check(acceptance['excluded_from_this_goal'] == independent['excluded_from_this_goal'] == coordinator['excluded_from_this_goal'], 'Scope exclusions differ')
    links = []
    for name in ['docs/evolution/status.md', 'docs/evolution/plan.md', 'docs/evolution/pause-handoff.md']:
        path = ROOT / name
        for target in re.findall(r'\[[^\]]*\]\(([^)]+)\)', path.read_text()):
            if '://' in target or target.startswith('#'):
                continue
            actual = (path.parent / target.split('#')[0]).resolve()
            links.append({'source': name, 'target': target, 'exists': actual.exists()})
            check(actual.exists(), 'Document link missing: ' + name + ' -> ' + target)
    diff = subprocess.run(['git', 'diff', '--check'], cwd=ROOT, capture_output=True, text=True)
    check(diff.returncode == 0, 'Git diff whitespace check failed')
    report = {
        'checked_at': datetime.now(timezone.utc).isoformat(),
        'status': 'passed' if not ERRORS else 'failed',
        'scope': 'Final record consistency, source preservation and delivery identity after formal acceptance',
        'requirements': dict(Counter(x['verification_status'] for x in acceptance['requirements'])),
        'phases': {str(x['phase']): x['status'] for x in team['phases']},
        'phase5_findings_resolved': 14, 'phase5_watchpoints_verified': 14,
        'source_files_verified': manifest['count'], 'phase_records_verified': len(manifest['phase_records']),
        'frontend_files_verified': pointer['frontend_dist_files'],
        'public_delivery_source_sha256': source_sha, 'archive': delivery['deliverable']['archive'],
        'requirement_references_verified': audit['reference_count'],
        'document_links': links,
        'git_diff_check': {'exit_code': diff.returncode, 'stdout': diff.stdout, 'stderr': diff.stderr},
        'references': REFERENCES, 'errors': ERRORS,
    }
    (ROOT / '.local/evolution/phase5-completion-audit.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({key: report[key] for key in ['status', 'requirements', 'phases', 'source_files_verified', 'phase_records_verified', 'frontend_files_verified', 'requirement_references_verified', 'errors']}, ensure_ascii=False))
    return 0 if not ERRORS else 1


if __name__ == '__main__':
    raise SystemExit(main())
