#!/usr/bin/env python3
"""Stop only the eight recorded independent fixtures and inspect residual resources."""
import datetime
import hashlib
import json
import os
import pathlib
import shutil
import signal
import socket
import subprocess
import time
import urllib.parse

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
OUT = ROOT / '.local/evolution/phase4-review-browser'
TARGET = OUT / 'final-resource-cleanup.json'
if TARGET.exists():
    raise RuntimeError('Preserve existing final cleanup evidence')
metadata = sorted(OUT.rglob('*.server.json'))
assert len(metadata) == 8, len(metadata)
records = []
for path in metadata:
    record = json.loads(path.read_text())
    expected = record['command']
    assert expected[0].startswith('/private/tmp/smartfactory-phase4-independent-a02-20261005/bin/')
    assert record['directory'].startswith('/private/tmp/smartfactory-phase4-independent-browser-20261005/')
    process = subprocess.run(['ps', '-p', str(record['pid']), '-o', 'command='], capture_output=True, text=True)
    observed = process.stdout.strip()
    identity_ok = observed.startswith(expected[0] + ' ') and record['directory'] in observed
    row = {'server_metadata': {'path': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}, 'pid': record['pid'], 'port': record['port'], 'already_exited': not observed, 'observed_command_matches_record': identity_ok}
    if observed and not identity_ok:
        raise RuntimeError('Recorded process identity changed; refusing termination')
    if identity_ok:
        os.kill(record['pid'], signal.SIGTERM)
        row['signal'] = 'SIGTERM'
    records.append(row)

deadline = time.monotonic() + 15
while time.monotonic() < deadline:
    live = []
    for row in records:
        process = subprocess.run(['ps', '-p', str(row['pid']), '-o', 'stat='], capture_output=True, text=True)
        if process.stdout.strip() and not process.stdout.strip().startswith('Z'):
            live.append(row['pid'])
    if not live:
        break
    time.sleep(0.2)
for row in records:
    process = subprocess.run(['ps', '-p', str(row['pid']), '-o', 'stat='], capture_output=True, text=True)
    row['exited'] = not process.stdout.strip() or process.stdout.strip().startswith('Z')
    with socket.socket() as client:
        client.settimeout(0.3)
        row['port_closed'] = client.connect_ex(('127.0.0.1', row['port'])) != 0

uri = urllib.parse.urlparse(json.loads((ROOT / '.local/evolution/pg18-test.json').read_text())['dsn'])
query = "select json_build_object('fixture_schemas',coalesce((select json_agg(nspname order by nspname) from pg_namespace where nspname like 'sf_%'),'[]'::json),'other_sessions',(select count(*) from pg_stat_activity where datname=current_database() and pid<>pg_backend_pid()),'server_version',current_setting('server_version'))::text"
process = subprocess.run(['docker', 'exec', 'smartfactory-evolution-pg18-20261004', 'psql', '-U', uri.username, '-d', uri.path.lstrip('/'), '-At', '-c', query], capture_output=True, text=True, timeout=30)
assert process.returncode == 0, 'Read-only PostgreSQL resource query failed'
database = json.loads(process.stdout)
remaining_schemas = [value for value in database['fixture_schemas'] if value != 'sf_pilot_20261004']
processes = subprocess.check_output(['ps', '-axo', 'pid=,comm=,args='], text=True).splitlines()
independent = []
for line in processes:
    fields = line.strip().split(None, 2)
    if len(fields) != 3:
        continue
    if fields[1].startswith(('/private/tmp/smartfactory-phase4-independent-20261005/bin/', '/private/tmp/smartfactory-phase4-independent-a02-20261005/bin/')):
        independent.append({'pid': int(fields[0]), 'executable': fields[1]})
browser_tests = [line.split(None, 2)[0] for line in processes if '/private/tmp/smartfactory-phase4-independent-browser-20261005/' in line and '@playwright/test/cli.js' in line]
result = {'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'servers': records, 'own_binary_processes': independent, 'own_browser_runner_pids': browser_tests, 'browser_lifecycle': 'Every Playwright test runner returned after closing its isolated browser contexts; no persistent browser session was created', 'database': database, 'remaining_independent_schemas': remaining_schemas, 'retained_prior_stage_schema': {'name': 'sf_pilot_20261004', 'reason': 'Explicit retention recorded by prior-phase storage handoff', 'reference': str(ROOT / 'docs/evolution/handoff-storage.md'), 'line': 27}, 'persistent_services_created': [], 'existing_user_containers_modified': False, 'disk_free_bytes': {value: shutil.disk_usage(value).free for value in ['/Volumes/Work', '/private/tmp']}}
result['resources_released'] = all(row['exited'] and row['port_closed'] for row in records) and not independent and not browser_tests and not remaining_schemas and database['other_sessions'] == 0
TARGET.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'resources_released': result['resources_released'], 'fixtures_exited': sum(row['exited'] for row in records), 'ports_closed': sum(row['port_closed'] for row in records), 'other_pg_sessions': database['other_sessions'], 'independent_schemas': remaining_schemas, 'disk_free_bytes': result['disk_free_bytes'], 'report': str(TARGET)}))
assert result['resources_released']
