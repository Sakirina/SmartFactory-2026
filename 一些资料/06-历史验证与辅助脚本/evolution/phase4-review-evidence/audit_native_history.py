#!/usr/bin/env python3
"""Keep specialist CE evidence and independent current regression scope explicit."""
import datetime
import difflib
import hashlib
import json
import pathlib

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
OUT = ROOT / '.local/evolution/phase4-review-a02'
OLD = pathlib.Path('/private/tmp/smartfactory-business-native-20261005-retry/compiled-source')
CURRENT = pathlib.Path('/private/tmp/smartfactory-phase4-independent-a02-20261005/workspace')

def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def ref(path):
    return {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}

report_path = ROOT / '.local/evolution/business-workflows-verified.json'
native = json.loads(report_path.read_text())['native_validation']
inputs_path = pathlib.Path(native['actual_inputs']['path'])
inputs = json.loads(inputs_path.read_text())['inputs']
checked, changed, missing = [], [], []
for relative, item in inputs.items():
    if not item.get('local'):
        continue
    old, current = OLD / relative, CURRENT / relative
    if not old.exists():
        missing.append(relative)
        continue
    actual = sha(old)
    if actual != item['sha256']:
        raise RuntimeError('Retained actual native build input mismatch: ' + relative)
    row = {'relative': relative, 'original_input_sha256': item['sha256'], 'retained': ref(old), 'current': ref(current) if current.exists() else None, 'equal': current.exists() and actual == sha(current)}
    checked.append(row)
    if not row['equal']:
        changed.append(relative)

paths = ['Platform/internal/api/business_native_test.go', 'Platform/internal/thingsboard/alarms.go', 'Platform/internal/thingsboard/adapter.go', 'Platform/internal/thingsboard/alarm_states.go', 'Platform/internal/application/native_alarm.go', 'Platform/internal/app/business_workers.go', 'Platform/pkg/model/native_alarm.go']
diff = ''.join(''.join(difflib.unified_diff((OLD / path).read_text().splitlines(True), (CURRENT / path).read_text().splitlines(True), fromfile='specialist-native/' + path, tofile='independent-current/' + path)) for path in paths)
diff_path = OUT / 'native-history-to-current.diff'
diff_path.write_text(diff)
binary, log = pathlib.Path(native['binary']['path']), pathlib.Path(native['test_log']['path'])
assert sha(binary) == native['binary']['sha256']
assert sha(log) == native['test_log']['sha256']
value = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'specialist_report': ref(report_path), 'specialist_native_binary': ref(binary), 'specialist_native_log': ref(log), 'actual_inputs': ref(inputs_path), 'retained_source': str(OLD), 'current_independent_source': str(CURRENT), 'local_input_count': len(checked), 'missing': missing, 'changed_paths': changed, 'input_correspondence': checked, 'native_scope_diff': ref(diff_path), 'interpretation': {'actual_native_ce_rerun_by_this_reviewer': False, 'actual_native_ce_specialist_pass_retained': True, 'unchanged': ['native CE lifecycle integration test', 'native alarm projection and lifecycle payload decoder', 'formal worker immediate and periodic scheduling'], 'changes_since_actual_ce': ['P4-B05 serializes polling, imposes a ten-second batch deadline, continues after failed mappings, preserves per-item failure state, and advances a persisted cursor over up to 64 mappings', 'native sync detail exposes batch and timeout values while hiding its internal cursor', 'business request tags and later AI registration evolved after this historical build'], 'independent_current_verification': ['ordinary and race tests for fair native mapping rotation, 404 and malformed mappings, deadline recovery, immediate worker invocation and periodic retries', 'current business/API/store/query transactions, source versions, manual alarm action versions, projection revisions and cloud/edge synchronization'], 'counting_rule': 'Historical actual CE test is cited as specialist evidence, never counted as an independent current CE rerun'}}
(OUT / 'native-history-audit.json').write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'local_inputs': len(checked), 'changed_paths': len(changed), 'missing': missing, 'native_test_log': log.read_text()}))
