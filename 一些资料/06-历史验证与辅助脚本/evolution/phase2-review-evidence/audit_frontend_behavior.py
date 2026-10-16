#!/usr/bin/env python3
import hashlib
import json
from pathlib import Path

OUT = Path(__file__).resolve().parent
ROOT = OUT.parents[2]
FRONT = ROOT / '.local/evolution/frontend-phase2'
used = {}


def read(name):
    path = FRONT / name
    used[str(path.relative_to(ROOT))] = hashlib.sha256(path.read_bytes()).hexdigest()
    return json.loads(path.read_text())


capture = read('browser-task-requests-final.json')
assert not capture['errors']
path = '/api/sf/v1/tasks/recompute%3Afrontend%3Aretry%2Fwith-identity%3A1/retry'
posts = [r for r in capture['requests'] if r['method'] == 'POST' and r['path'] == path]
assert [r['status'] for r in posts] == [409, 200, 200]
assert all(isinstance(json.loads(r['body'])['expected_version'], str) for r in posts)
for response in map(lambda r: json.loads(r['response']), posts[1:]):
    assert response['id'] == 'recompute:frontend:retry/with-identity:1'
    assert response['river_id'] == 361 and response['business_id'] == 'frontend:retry/with-identity'
before = read('task-job-before-final-retry.json')
after = read('task-job-completed.json')
for key in ['task_id', 'replay_id', 'replay_start_ms', 'replay_end_ms', 'id', 'from_ms', 'to_ms', 'cursor_ms']:
    assert before[key] == after[key]
assert before['replay_phase'] == 'rollups' and before['status'] == 'failed'
assert after['replay_phase'] == 'completed' and after['status'] == 'completed'
db_before = read('database-before-rollup-retry.json')
db_after = read('database-after-rollup-retry.json')
assert db_before['observations'] == db_after['observations']
assert db_before['latest'] == db_after['latest']
assert db_before['rollups']['rows'] == 0 and db_after['rollups']['rows'] == 6
cancel = read('task-cancel-proof.json')
assert cancel['status'] == 200 and cancel['outbox_preserved_rows'] == 1
assert cancel['before']['state'] == 'available' and cancel['after']['state'] == 'cancelled'
for key in ['id', 'business_id', 'river_id', 'outbox_id']:
    assert cancel['before'][key] == cancel['after'][key]
viewer = read('viewer-api.json')
assert viewer['read_status'] == 200 and viewer['write_status'] == 403
assert viewer['allowed_actions'] == [] and not viewer['archive_visible']
viewer_ui = read('browser-task-readonly.json')
assert viewer_ui['actionButtons'] == []
assert json.loads(viewer_ui['requests'][-1]['response'])['allowed_actions'] == []
denied = read('browser-resource-denied.json')
assert denied['requests'][-1]['status'] == 403
assert not denied['taskDetailPresent'] and denied['actionButtons'] == []
revoked = read('browser-viewer-revoked.json')
assert revoked['requests'][-1]['status'] == 401 and revoked['expiredEvents'] == 1
assert not any(revoked[key] for key in ['taskDetailPresent', 'drawerPresent', 'sessionPresent'])
assert revoked['loginPresent']
result = {
    'passed': True,
    'original_browser_capture_requests': len(capture['requests']),
    'fixed_task_retry_statuses': [409, 200, 200], 'task_river_business_identity_retained': True,
    'replay_window_and_cursor_retained': True, 'rollup_recovery_raw_and_latest_unchanged': True,
    'rollups_before_rows': 0, 'rollups_after_rows': 6,
    'available_task_cancel_keeps_outbox_and_identities': True,
    'read_only_forced_write_denied': True, 'insufficient_resource_hides_stale_detail': True,
    'revoked_identity_expires_existing_browser_session': True,
    'scope': 'Independent assertions against retained specialist HTTP/browser/database artifacts; current production HTTP semantics also pass the independent Go and PostgreSQL tests',
    'evidence_sha256': used,
}
(OUT / 'frontend-original-behavior-audit.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({key: value for key, value in result.items() if key != 'evidence_sha256'}, ensure_ascii=False))
