#!/usr/bin/env python3
"""Stop only verified current and coordinator-authorized historical test services."""
import datetime
import hashlib
import json
import os
import re
import signal
import subprocess
import sys
import time
from pathlib import Path

BASE = Path('/private/tmp/smartfactory-final-independent-20261007')
OUT = BASE / 'cleanup'
OUT.mkdir(exist_ok=True)
PAUSE = Path('/private/tmp/smartfactory-release-interface-20261005/pause-handoff/services-and-restoration.json')
HISTORICAL = [
    '/private/tmp/smartfactory-phase5-release-process-tenth-20261005/',
    '/private/tmp/smartfactory-release-restore-recovery-20261005/',
    '/private/tmp/smartfactory-phase5-node-frontend-live-20261005/',
    '/private/tmp/smartfactory-phase5-release-a01-20261005/',
]

def ref(path):
    path = Path(path)
    return {'path': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size}

def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')

def current():
    raw = subprocess.check_output(['ps', '-axo', 'pid=,ppid=,stat=,command='], text=True)
    result = []
    for line in raw.splitlines():
        match = re.match(r'\s*(\d+)\s+(\d+)\s+(\S+)\s+(.*)', line)
        if not match:
            continue
        pid, ppid, state, command = match.groups()
        executable = command.split(' ', 1)[0]
        own = executable.startswith(str(BASE) + '/')
        legacy = any(executable.startswith(prefix) or (' --directory ' + prefix) in command for prefix in HISTORICAL)
        if not own and not legacy:
            continue
        # Only real Go test/runtime binaries are eligible; exclude drivers, browsers and shells.
        name = Path(executable).name
        eligible = name.startswith(('sf-', 'dt-', 'datatransfer', 'platform-control')) or bool(re.fullmatch('[0-9a-f]{64}', name))
        if not eligible:
            continue
        result.append({'pid': int(pid), 'ppid': int(ppid), 'state': state, 'command': command, 'executable': executable, 'scope': 'current_independent' if own else 'coordinator_authorized_oct5_handoff'})
    return result

def listeners(pids):
    call = subprocess.run(['lsof', '-n', '-P', '-iTCP', '-sTCP:LISTEN', '-Fpcn'], text=True, capture_output=True)
    result, pid, command = [], None, ''
    for line in call.stdout.splitlines():
        if line.startswith('p'): pid = int(line[1:])
        elif line.startswith('c'): command = line[1:]
        elif line.startswith('n') and pid in pids: result.append({'pid': pid, 'name': command, 'listen': line[1:]})
    return result

def alive(row):
    call = subprocess.run(['ps', '-p', str(row['pid']), '-o', 'stat=,command='], text=True, capture_output=True)
    parts = call.stdout.strip().split(None, 1)
    return bool(len(parts) == 2 and not parts[0].startswith('Z') and parts[1] == row['command'])

before = current()
snapshot = {'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'authorization': 'REDACTED', 'legacy_handoff': ref(PAUSE), 'processes': before, 'listeners': listeners({row['pid'] for row in before}), 'excluded': 'Existing user Docker projects and the current PostgreSQL validation container are not selected by the executable/path allowlist; all persistent data, credentials, database files and original failure artifacts are retained'}
mode = sys.argv[1]
if mode == 'inspect':
    save(OUT / 'before.json', snapshot)
    print(json.dumps({'selected': len(before), 'by_scope': {scope: sum(row['scope'] == scope for row in before) for scope in ['current_independent', 'coordinator_authorized_oct5_handoff']}, 'listeners': len(snapshot['listeners'])}))
    raise SystemExit(0)
if mode != 'stop': raise SystemExit('Expected inspect or stop')
if (OUT / 'shutdown.json').exists(): raise SystemExit('Shutdown record already exists')
save(OUT / 'immediate-before-stop.json', snapshot)
actions = []
# Stop release agents first so they cannot create a replacement child during shutdown.
ordered = sorted(before, key=lambda row: ('sf-release-agent' not in row['executable'], row['pid']))
for row in ordered:
    if not alive(row):
        actions.append({**row, 'action': 'already_exited_or_pid_changed'})
        continue
    if row['state'].startswith('T'):
        os.kill(row['pid'], signal.SIGCONT)
    os.kill(row['pid'], signal.SIGTERM)
    actions.append({**row, 'action': 'SIGTERM', 'sent_at': datetime.datetime.now(datetime.timezone.utc).isoformat()})
deadline = time.monotonic() + 25
while time.monotonic() < deadline:
    remaining = [row for row in current() if not row['state'].startswith('Z')]
    if not remaining: break
    time.sleep(0.25)
after = current()
live = [row for row in after if not row['state'].startswith('Z')]
report = {'status': 'passed' if not live else 'services_still_running', 'completed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'actions': actions, 'remaining_selected_processes': after, 'remaining_live_services': live, 'remaining_listeners': listeners({row['pid'] for row in after}), 'before': ref(OUT / 'immediate-before-stop.json'), 'retained': 'All database files, master keys, tokens, build evidence, snapshots, private directories and historical failed-run artifacts', 'docker_actions': [], 'test_pg_container': 'Retained for the delivery engineer; no Docker commands were run by this cleanup', 'driver': ref(__file__)}
save(OUT / 'shutdown.json', report)
print(json.dumps({'status': report['status'], 'term_signals': sum(row['action'] == 'SIGTERM' for row in actions), 'remaining_live': len(live), 'remaining_listeners': len(report['remaining_listeners'])}))
