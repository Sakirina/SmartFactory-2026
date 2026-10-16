#!/usr/bin/env python3
"""Verify final frontend report against retained files and actual response bodies."""
import datetime
import json
import re
import shutil
from pathlib import Path
import audit_specialists as A

R, ROOT, OUT = A.R, A.ROOT, A.OUT
BASE = ROOT / '.local/evolution/frontend-phase3'


def read(name):
    return json.loads((BASE / name).read_text())


def entries(mapping, base=ROOT):
    return [{'path': str(base / name), 'sha256': digest} for name, digest in mapping.items()]


def body(value):
    return json.loads(value) if isinstance(value, str) else value


report_file = ROOT / '.local/evolution/frontend-phase3-verified.json'
snapshot = OUT / 'initial-specialist-reports/frontend-phase3-verified.json'
if not snapshot.exists():
    shutil.copyfile(report_file, snapshot)
report = json.loads(snapshot.read_text())
result = {
    'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'report': {'path': str(snapshot.relative_to(ROOT)), 'sha256': R.sha(snapshot)},
    'current_report_identical': R.sha(report_file) == R.sha(snapshot),
    'typed_references': A.compare(A.references(report)),
    'evidence_index': A.compare(read('evidence-index.json')['files']),
    'current_source': A.compare(entries(read('source-manifest.json')['source_files'])),
}
result['builds'] = {}
for generation in ['first', 'second', 'third']:
    manifest = read(f'builds/{generation}/manifest.json')
    result['builds'][generation] = {
        'retained_dist': A.compare(entries(manifest['artifacts'], BASE / 'builds' / generation / 'dist')),
        'source_tree_sha256': manifest['source_tree_sha256'],
    }
third = read('builds/third/manifest.json')
result['current_dist'] = A.compare(entries(third['artifacts'], ROOT / 'Frontends/dist'))
result['independent_browser_dist'] = A.compare(entries(third['artifacts'], R.TMP / 'browser-static-v2'))
result['commands'] = {}
for generation in ['first', 'second', 'third']:
    commands = read(f'commands-{generation}.json')
    raw = (ROOT / next(command['log'] for command in commands if command['name'] == 'unit')).read_text()
    result['commands'][generation] = {
        'node_tests': {name: int(re.search(r'^.*? ' + name + r' (\d+)$', raw, re.M).group(1))
                       for name in ['tests', 'pass', 'fail', 'skipped']},
        'command_record': commands,
    }

actual = read('control-c05-final.json')
cloud, edge = (body(actual[mode]['body']) for mode in ['cloud', 'edge'])
evidence = cloud['evidence'][0]
physical = body(actual['driver']['body'])['physical_actions']
control_assertions = {
    'both_completed': all(item['execution']['status'] == 'completed' for item in [cloud, edge]),
    'first_once': physical.get('recover-browser:first') == 1,
    'remaining_once': physical.get('recover-browser:remaining') == 1,
    'cloud_edge_evidence_identical': cloud['evidence'] == edge['evidence'],
    'one_evidence': len(cloud['evidence']) == 1,
    'trusted_original_success': evidence['trusted'] and evidence['status'] == 'SUCCESS'
        and evidence['command_id'] == 'recover-browser:first'
        and evidence['source'] == 'datatransfer.command_journal:edge-a',
    'both_steps_bind_evidence': all(item['execution']['steps'][0]['evidence_id'] == evidence['id'] for item in [cloud, edge]),
    'both_feedback_bind_identical_evidence': all(any(row['kind'] == 'feedback' and row.get('evidence') == evidence
        for row in item['timeline']) for item in [cloud, edge]),
}
result['actual_c05_response_assertions'] = control_assertions
result['actual_c05_evidence_identity'] = evidence['id']

before = read('history-failed-task-before.json')['body']
after = read('history-failed-task-after-retry.json')['body']
cancelled = read('history-waiting-task-final.json')['body']
role = read('browser-role-revoked.json')
resources = read('browser-resource-revoked.json')
identity = read('browser-identity-revoked-session-proof.json')
result['task_and_permissions_assertions'] = {
    'same_failed_task_and_river': all(before[k] == after[k] for k in ['id', 'river_id', 'replay_id', 'business_id', 'business_version']),
    'retry_attempt_8_to_9': before['attempt'] == before['max_attempts'] == 8 and after['attempt'] == after['max_attempts'] == 9,
    'failed_snapshot_still_reports_actual_error': before['error'] == after['error'] and after['state'] == 'discarded',
    'specialist_task_cancelled': cancelled['id'] == 'analysis:shadow:fixture-failure:1:2' and cancelled['river_id'] == 26
        and cancelled['state'] == cancelled['business_state'] == 'cancelled',
    'role_modals_removed': role['visibleModals'] == [],
    'role_mutation_controls_removed': all(name not in role['visibleButtons'] for name in ['停止候选', '重新启用候选', '保存候选']),
    'resources_clear_details': not any(resources[key] for key in ['detailPresent', 'runPresent', 'nodePresent']),
    'identity_clears_real_session': identity['loginPresent'] and not identity['actualSessionPresent'] and not identity['detailPresent'],
}
result['task_cancellation_scope'] = 'Specialist capture shows one successful fast retry; independent current browser separately reproduced four conflicts during ordinary confirmation. See IR-P3-02; this audit does not close that issue.'
initial = read('history-isolation-before.json')['queries']
final = read('history-isolation-final.json')['queries']
result['formal_isolation'] = {
    key: {'identical_query_rows_and_digest': initial[key] == final[key], 'rows': final[key]['rows'], 'sha256': final[key]['sha256']}
    for key in initial
}
export = read('history-results-export.json')
result['exact_export_integers'] = {
    'raw_input': export['items'][0]['point']['value'],
    'integer_token_present': '9007199254740993' in (BASE / 'history-results-export.json').read_text(),
    'computed_integer_token_present': '9007199254740994' in (BASE / 'history-results-export.json').read_text(),
}
assert all(control_assertions.values()), control_assertions
assert all(result['task_and_permissions_assertions'].values()), result['task_and_permissions_assertions']
assert all(item['identical_query_rows_and_digest'] for item in result['formal_isolation'].values())
for key in ['typed_references', 'evidence_index', 'current_source', 'current_dist', 'independent_browser_dist']:
    assert not result[key]['different'], (key, result[key]['different'])
for value in result['builds'].values():
    assert not value['retained_dist']['different']
R.json_write(OUT / 'final-frontend-specialist-audit.json', result)
print(json.dumps({key: {'files': value['files'], 'matched': value['matched']} for key, value in result.items()
                  if isinstance(value, dict) and 'files' in value}, ensure_ascii=False))
