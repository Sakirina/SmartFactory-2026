import datetime
import json
import os
import socket
from pathlib import Path
from urllib.parse import urlparse

import run_review as R

root, out, tmp = R.ROOT, R.OUT, R.TMP
now = datetime.datetime.now(datetime.timezone.utc).isoformat()


def read(name):
    return json.loads((out / name).read_text())


def ref(p):
    p = Path(p)
    absolute = p if p.is_absolute() else root / p
    return {'path': str(p), 'sha256': R.sha(absolute), 'bytes': absolute.stat().st_size}


def own(name):
    return ref((out / name).relative_to(root))


before = read('t02-reopened-original-state.json')
samples = [read('t02-retry-scheduling-sample-' + str(i) + '.json') for i in range(1, 4)]
immediate = read('t02-cancel-immediate.json')
after = read('t02-cancel-after-scheduling.json')
rejected = read('t02-after-rejected-actions.json')
timing = read('t02-single-read-timing.json')
http = read('t02-duplicate-stale-http.json')
identity_fields = ['id', 'task_id', 'snapshot_id', 'captured_snapshot_id', 'snapshot_sha256', 'input_sha256']
get = json.loads(timing['detail_get']['response'])
post_body = json.loads(timing['cancel_post']['body'])
post = json.loads(timing['cancel_post']['response'])
assertions = {
    'reopened_prior_actual_failure': before['river']['id'] == 24 and before['river']['state'] == 'cancelled' and before['run']['status'] == 'waiting_parent' and before['river']['metadata']['sf_operation_revision'] == 3 and 'cancel_attempted_at' in before['river']['metadata'],
    'retry_survived_production_snoozes': all(s['river']['state'] in ['running', 'available', 'scheduled', 'retryable'] and s['run']['status'] == 'waiting_parent' for s in samples) and all(samples[i+1]['river']['metadata']['snoozes'] > samples[i]['river']['metadata']['snoozes'] for i in range(2)),
    'retry_removed_old_cancellation_marker': all('cancel_attempted_at' not in s['river']['metadata'] for s in samples),
    'retry_advanced_operation_revision_once': all(s['river']['metadata']['sf_operation_revision'] == 4 for s in samples),
    'trace_and_unique_nonce_preserved': all(all(s['river']['metadata'][k] == before['river']['metadata'][k] for k in ['sf_trace', 'river:unique_nonce']) for s in samples + [after]),
    'same_original_identity_and_snapshots': all(all(s['run'][k] == before['run'][k] for k in identity_fields) and s['river']['id'] == 24 for s in samples + [immediate, after, rejected]),
    'one_current_detail_read_then_human_confirmation': timing['single_task_get_before_post'] == 1 and timing['confirmation_elapsed_ms'] >= 3000 and not timing['automatic_retry'],
    'original_read_version_submitted': get['version'] == post_body['expected_version'],
    'cancel_returned_200': timing['cancel_post']['status'] == 200 and post['state'] == post['business_state'] == 'cancelled',
    'cancel_final_states_and_revision': after['task']['state'] == after['task']['business_state'] == after['run']['status'] == after['river']['state'] == 'cancelled' and after['river']['metadata']['sf_operation_revision'] == 5,
    'no_further_calculation_after_cancellation': after['recorded_at_ms'] - immediate['recorded_at_ms'] >= 3000 and all(after[k] == immediate[k] for k in ['run', 'river', 'steps']) and after['run']['cursor'] == 0 and after['steps']['items'] == [],
    'one_additional_retry_and_cancel_audit': after['audit'][:3] == before['audit'] and [a['action'] for a in after['audit']] == ['task.cancel', 'task.retry', 'task.retry', 'task.retry', 'task.cancel'] and all(a['request_id'] == before['run']['task_id'] for a in after['audit']),
    'duplicate_and_stale_versions_rejected': all(c['status'] == 409 for c in http['cases']) and len(http['cases']) == 3,
    'rejected_actions_no_business_effect': all(after[k] == rejected[k] for k in ['run', 'river', 'steps', 'audit']),
}
assert all(assertions.values()), assertions
browser = {'recorded_at': now, 'assertions': assertions,
           'task_id': before['run']['task_id'], 'river_id': 24,
           'snapshot_id': before['run']['snapshot_id'],
           'confirmation_elapsed_ms': timing['confirmation_elapsed_ms'],
           'operation_revisions': [3, 4, 5],
           'sample_snoozes': [s['river']['metadata']['snoozes'] for s in samples],
           'audit_actions': [a['action'] for a in after['audit']],
           'sources': [own(n) for n in ['t02-reopened-original-state.json',
               't02-retry-scheduling-sample-1.json', 't02-retry-scheduling-sample-2.json',
               't02-retry-scheduling-sample-3.json', 't02-single-read-timing.json',
               't02-cancel-immediate.json', 't02-cancel-after-scheduling.json',
               't02-duplicate-stale-http.json', 't02-after-rejected-actions.json']],
           'visuals_inspected': ['t02-single-read-confirmation.png', 't02-single-read-cancelled.png'],
           'case_scope': 'An existing real failed waiting-parent task from T01, copied with its original SQLite data, opened by current production sf-cloud; ordinary page buttons and manual refresh. Existing completed-step preservation, role/resource changes and rollback are additionally executed by current SQLite and PostgreSQL API/race tests.'}
R.json_write(out / 't02-browser-independent-audit.json', browser)

current = read('final-reviewed-source.json')['files']
old = read('reviewed-source-through-c05.json')['files']
changes = [{'path': p, 'before_sha256': old.get(p), 'current_sha256': current.get(p)}
           for p in sorted(old.keys() | current.keys()) if old.get(p) != current.get(p)]
compiled = read('final-compiled-inputs.json')['inputs']
compiled_mismatches = [p['path'] for p in compiled if R.sha(p['path']) != p['sha256']]
source_mismatches = [p for p, h in current.items() if R.sha(root / p) != h]
dt = read('datatransfer-full.metadata.json')['source_after']
front = read('c05-frontend-production-build.metadata.json')['source_after']
assert not compiled_mismatches and not source_mismatches
dt_mismatches = [p for p, h in dt.items() if p.startswith('DataTransfer/') and current.get(p) != h]
front_mismatches = [p for p, h in front.items() if p.startswith('Frontends/') and current.get(p) != h]
assert not dt_mismatches and not front_mismatches
source_audit = {'recorded_at': now, 'changes_since_c05_review': changes,
                'current_source_files': len(current), 'current_source_mismatches': source_mismatches,
                'current_compiled_inputs': len(compiled), 'compiled_input_mismatches': compiled_mismatches,
                'datatransfer_changes_since_independent_test': dt_mismatches,
                'frontend_changes_since_independent_build': front_mismatches,
                'contract_comparison': own('t02-contract-byte-comparison.json'),
                'reuse_scope': 'The final Platform regression and actual PostgreSQL integration run use T02 source. Earlier current-control dual-app, NATS, browser, OTLP overlay, DataTransfer and frontend results retain their own binary/source snapshots. T01/T02 modify task operation versions, history task cancellation/retry, and their tests; control/coordination/DataTransfer/frontend implementation and generated HTTP contracts are unchanged. Dedicated current task checks and full Platform regression cover their integration.'}
R.json_write(out / 'final-source-reuse-audit.json', source_audit)

ports = []
for port in [59107, 59108, 59114, 19060, 51302, 51303, 51313, 19061, 19062, 19063]:
    with socket.socket() as sock:
        sock.settimeout(.2)
        ports.append({'port': port, 'closed': sock.connect_ex(('127.0.0.1', port)) != 0})
assert all(p['closed'] for p in ports)
runtime = read('browser-process-t02.json')
original_checks = [{**v, 'unchanged': R.sha(v['path']) == v['sha256']} for v in runtime['original_data_files']]
assert all(v['unchanged'] for v in original_checks)
master_key_preserved = (tmp/'history-browser-t01/master.key').read_bytes() == (tmp/'history-browser-t02-from-t01/master.key').read_bytes()
assert master_key_preserved
assert "Browser 'sf-phase3-independent-t02-history' closed" in (out/'browser-cli-t02.log').read_text()
assert 'River client stopped' in (out/'history-browser-t02.log').read_text()
cleanup = {'recorded_at': now, 'owned_pid': 75757,
           'stop_method': 'SIGTERM after exact ps command comparison; production River stopped normally',
           'ports': ports, 'browser_session': 'sf-phase3-independent-t02-history', 'browser_closed': True,
           'previous_cleanup': [own('cleanup-waiting-t01.json'), own('t01-cleanup.json')],
           'current_logs': [own('history-browser-t02.log'), own('browser-cli-t02.log')],
           'original_data_checks': original_checks, 'retained_t02_copy': str(tmp/'history-browser-t02-from-t01'),
           'original_and_copied_master_key_identical': master_key_preserved,
           'retained_copy_database': ref(tmp/'history-browser-t02-from-t01/history.db'),
           'other_services_touched': False, 'user_containers_touched': False,
           'work_volume_free_bytes': os.statvfs(root).f_bavail * os.statvfs(root).f_frsize}
R.json_write(out/'final-cleanup.json', cleanup)

previous = []
for name in ['evidence-index-waiting-t01.json', 'evidence-index-through-t01.json']:
    index = read(name)
    mismatches = [v['path'] for v in index['files'] if R.sha(root/v['path']) != v['sha256']]
    previous.append({'index': own(name), 'files_checked': len(index['files']), 'mismatches': mismatches})
assert all(not v['mismatches'] for v in previous)
R.json_write(out/'final-previous-evidence-audit.json', {'recorded_at': now, 'indices': previous})
print(json.dumps({'browser_assertions': len(assertions), 'all_browser_assertions_passed': True,
                  'source_changes_since_c05': [v['path'] for v in changes],
                  'current_compiled_inputs': len(compiled), 'current_source_files': len(current),
                  'closed_ports': len(ports), 'original_data_preserved': True,
                  'previous_indices_unchanged': [v['files_checked'] for v in previous]}))
