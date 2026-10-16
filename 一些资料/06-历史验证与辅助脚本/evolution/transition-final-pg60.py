import datetime
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time

root = Path('/path/to/SmartFactory-2026')
descriptor = root / '.local/evolution/pg18-test.json'
old = json.loads(descriptor.read_text())
assert old['container'] == 'smartfactory-evolution-pg18-final-20261005'
base = Path('/private/tmp/smartfactory-evolution-pg18-final-20261005')
proof = base / 'pg60-transition'
proof.mkdir(exist_ok=True)
proof.chmod(0o700)
(proof / 'before-descriptor.json').write_bytes(descriptor.read_bytes())
(proof / 'before-descriptor.json').chmod(0o600)
record = {'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'old_container': old['container'], 'status': 'in_progress'}


def run(args):
    return subprocess.run(args, check=True, capture_output=True)


def inspect(name):
    return json.loads(run(['docker', 'inspect', name]).stdout)[0]


original = inspect(old['container'])
env = dict(value.split('=', 1) for value in original['Config']['Env'] if '=' in value)
user, database = env['POSTGRES_USER'], env['POSTGRES_DB']


def query(name, sql):
    return run(['docker', 'exec', name, 'psql', '-U', user, '-d', database, '-X', '-At', '-c', sql]).stdout.decode().strip()


def snapshot(name, stage, databases):
    result = {}
    for db in databases:
        raw = run(['docker', 'exec', name, 'pg_dump', '-U', user, '-d', db, '--format=plain', '--no-owner', '--no-privileges']).stdout
        path = proof / (stage + '-' + db + '.sql')
        path.write_bytes(raw)
        path.chmod(0o600)
        # PG18 emits a random psql restrict key; retain raw dumps and compare
        # the full logical dump after removing only these control lines.
        normalized = b'\n'.join(line for line in raw.splitlines() if not re.match(rb'^\\(?:un)?restrict\s', line))
        result[db] = {'path': str(path), 'bytes': len(raw), 'sha256': hashlib.sha256(raw).hexdigest(), 'normalized_sha256': hashlib.sha256(normalized).hexdigest()}
    return result


try:
    active = query(old['container'], "SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()")
    assert active == '0', 'Another application connection still exists'
    names = query(old['container'], "SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY datname").splitlines()
    record['before_system_identifier'] = query(old['container'], 'SELECT system_identifier::text FROM pg_control_system()')
    record['before_max_connections'] = query(old['container'], 'SHOW max_connections')
    record['before_databases'] = snapshot(old['container'], 'before', names)
    run(['docker', 'stop', '--time', '30', old['container']])
    record['old_container_final_state'] = inspect(old['container'])['State']
    assert record['old_container_final_state']['ExitCode'] == 0 and not record['old_container_final_state']['OOMKilled']
    name = 'smartfactory-evolution-pg18-final60-20261005'
    port = original['NetworkSettings']['Ports']['5432/tcp'][0]['HostPort']
    args = ['docker', 'run', '-d', '--name', name, '--memory', '1g', '--cpus', '2', '--mount', 'type=bind,source=' + str(base / 'data') + ',target=/var/lib/postgresql', '-p', '127.0.0.1:' + port + ':5432', '--env-file', str(base / 'postgres.env'), old['image'], '-c', 'shared_buffers=64MB', '-c', 'max_connections=60']
    record['new_start_command'] = args
    container_id = run(args).stdout.decode().strip()
    deadline = time.monotonic() + 40
    while True:
        try:
            record['after_max_connections'] = query(name, 'SHOW max_connections')
            break
        except subprocess.CalledProcessError:
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.25)
    assert record['after_max_connections'] == '60'
    record['after_system_identifier'] = query(name, 'SELECT system_identifier::text FROM pg_control_system()')
    assert record['before_system_identifier'] == record['after_system_identifier']
    record['after_databases'] = snapshot(name, 'after', names)
    record['logical_data_unchanged'] = all(record['before_databases'][db]['normalized_sha256'] == record['after_databases'][db]['normalized_sha256'] for db in names)
    assert record['logical_data_unchanged'], 'Logical dump differs after restart'
    record['new_container_state'] = inspect(name)['State']
    current = {**old, 'container': name, 'container_id': container_id, 'max_connections': 60, 'started_at': record['new_container_state']['StartedAt'], 'readiness': 'ready', 'previous_persistent_container': old['container'], 'transition_record': str(proof / 'verification.json')}
    descriptor.write_text(json.dumps(current, ensure_ascii=False, indent=2) + '\n')
    descriptor.chmod(0o600)
    record['status'] = 'passed'
except Exception as error:
    record['status'] = 'failed'
    record['error'] = str(error)
    raise
finally:
    record['completed_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    (proof / 'verification.json').write_text(json.dumps(record, ensure_ascii=False, indent=2) + '\n')
    (proof / 'verification.json').chmod(0o600)
    print(json.dumps({key: record[key] for key in ['status', 'before_max_connections', 'after_max_connections', 'logical_data_unchanged', 'error'] if key in record}))
