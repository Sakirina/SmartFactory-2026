from pathlib import Path
import json, hashlib, datetime, collections

repo = Path('/path/to/SmartFactory-2026')
base = Path('/private/tmp/smartfactory-release-restore-recovery-20261005')
report = repo / '.local/evolution/release-restore-recovery-verified.json'
r = json.loads(report.read_text())
errors, hashes = [], {}

def sha(p):
    p = Path(p)
    if str(p) not in hashes:
        hashes[str(p)] = hashlib.sha256(p.read_bytes()).hexdigest()
    return hashes[str(p)]

for item in r['references']:
    p = Path(item['path'])
    p = p if p.is_absolute() else repo / p
    if not p.is_file():
        errors.append({'missing': str(p)})
        continue
    if sha(p) != item['sha256']:
        errors.append({'sha256': str(p)})
    if 'bytes' in item and p.stat().st_size != item['bytes']:
        errors.append({'bytes': str(p)})

counts = []
for name, expected_pass, expected_fail in [
    ('process-final-fourth', 60, ['original-master-key-cache-artifacts-preserved']),
    ('process-final-sixth', 24, []),
    ('process-cached-failure-restart', 12, []),
]:
    checks = json.loads((base / name / 'evidence/checks.json').read_text())
    passed = sum(i['passed'] is True for i in checks)
    failed = [i['name'] for i in checks if i['passed'] is not True]
    if passed != expected_pass or failed != expected_fail:
        errors.append({'checks': name, 'passed': passed, 'failed': failed})
    counts.append({'directory': str(base / name), 'passed': passed, 'retained_failed_checks': failed})

backup = json.loads((base / 'evidence/original-backup-integrity-final.json').read_text())
for item in backup['files']:
    if sha(item['path']) != item['expected_sha256']:
        errors.append({'original_backup': item['path']})

f = json.loads((base / 'evidence/frontend-fixture-final.json').read_text())
e = base / 'process-final-sixth/evidence'
runtime_snapshot = json.loads((e / 'final-frontend-confirmed-runtime.json').read_text())
rt = runtime_snapshot['runtime']
consumer = json.loads((e / 'final-frontend-confirmed-consumer.json').read_text())
deployment = json.loads((e / 'recovery-r02-final-frontend-confirm-second.json').read_text())
reports = json.loads((e / 'recovery-r02-final-frontend-confirm-second-reports.json').read_text())
components = collections.Counter(i['kind'] for i in rt['components'])
target = deployment['targets'][0]
if components != {'program': 1, 'rule': 25, 'configuration': 3}:
    errors.append({'runtime_components': dict(components)})
if deployment['state'] != 'completed' or target['state'] != 'running':
    errors.append('deployment incomplete')
for k in ['desired_sha256', 'prepared_sha256', 'applied_sha256', 'running_sha256']:
    if target[k] != rt['release_sha256']:
        errors.append({'target_sha': k})
running = [i for i in reports if i['state'] == 'running']
if not running or not any(i['runtime'] == rt for i in running):
    errors.append('actual runtime has no matching running report')
if rt['program_sha256'] != r['frontend_handoff']['program_sha256'] or sha(f['final_program_binary']) != rt['program_sha256']:
    errors.append('program mismatch')
if rt['build']['source_sha256'] != r['source']['source_sha256']:
    errors.append('build source mismatch')
protocols = sorted(i['protocol'] for i in consumer['connectors'])
if protocols != ['modbus_tcp', 'mqtt_device', 'opcua'] or any(not i['state']['found'] for i in consumer['connectors']):
    errors.append('three actual consumer protocols mismatch')
cache = json.loads((base / 'evidence/original-cache-reencryption-proof.json').read_text())
if not cache['passed'] or not cache['plaintext_bytes_identical']:
    errors.append('cache full payload not verified')
if sha(cache['original']['path']) != cache['original']['ciphertext_sha256']:
    errors.append('historical cache bytes changed')
input_audit = json.loads((repo / '.local/evolution/phase5-release-r02-inputs-root-review.json').read_text())
db_audit = json.loads((repo / '.local/evolution/phase5-release-recovered-database-root-review.json').read_text())
if not input_audit['passed'] or not db_audit['passed']:
    errors.append('prior coordinator audit failed')
out = {
    'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'passed': not errors,
    'report': {'path': str(report), 'sha256': sha(report)},
    'reference_count': len(r['references']),
    'physical_files_checked': len(hashes),
    'fixed_source_sha256': r['source']['source_sha256'],
    'retained_input_count': input_audit['retained_input_count'],
    'retained_inputs_verified_by': '.local/evolution/phase5-release-r02-inputs-root-review.json',
    'checks': counts,
    'original_backup_files': len(backup['files']),
    'runtime_components': dict(components),
    'actual_consumer_protocols': protocols,
    'actual_runtime_pid': rt['pid'],
    'actual_runtime_process_instance': rt['process_instance_id'],
    'final_assignment_generation': target['generation'],
    'final_instance_epoch': target['agent_instance_epoch'],
    'original_fixed_authority_full_row_equal': db_audit['authority_fixed_heartbeat']['full_rows_equal'],
    'retained_failures': len(r['retained_failed_attempts']),
    'auditor_correction': 'The initial coordinator script accessed components at the response root and stopped with KeyError before producing a report; this version reads the documented runtime child object.',
    'scope': 'Cross-check of sealed report, original logs and process state; final frontend and independent review remain pending',
    'errors': errors,
    'references': r['references'],
}
(repo / '.local/evolution/phase5-release-recovery-root-review.json').write_text(json.dumps(out, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({k: v for k, v in out.items() if k != 'references'}, ensure_ascii=False, indent=2))
assert not errors
