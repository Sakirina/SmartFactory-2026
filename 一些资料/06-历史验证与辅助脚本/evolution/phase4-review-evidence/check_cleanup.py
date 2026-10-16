#!/usr/bin/env python3
import datetime, json, pathlib, shutil, subprocess, urllib.parse

ROOT = pathlib.Path('/path/to/SmartFactory-2026')
OUT = ROOT / '.local/evolution/phase4-review-evidence'
uri = urllib.parse.urlparse(json.loads((ROOT / '.local/evolution/pg18-test.json').read_text())['dsn'])
query = "select json_build_object('fixture_schemas',coalesce((select json_agg(nspname order by nspname) from pg_namespace where nspname like 'sf_%'),'[]'::json),'other_sessions',(select count(*) from pg_stat_activity where datname=current_database() and pid<>pg_backend_pid()),'server_version',current_setting('server_version'))::text"
command = ['docker', 'exec', 'smartfactory-evolution-pg18-20261004', 'psql', '-U', uri.username, '-d', uri.path.lstrip('/'), '-At', '-c', query]
process = subprocess.run(command, capture_output=True, text=True, timeout=30)
result = {'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'container': 'smartfactory-evolution-pg18-20261004', 'query': query, 'exit_code': process.returncode}
if process.returncode == 0:
    result['database'] = json.loads(process.stdout)
else:
    result['error'] = process.stderr.replace(uri.password or '<no-password>', '<redacted>')
try:
    rows = subprocess.check_output(['ps', '-axo', 'pid=,comm=,args='], text=True).splitlines()
    own = []
    for line in rows:
        fields = line.strip().split(None, 2)
        if len(fields) == 3 and fields[1].startswith('/private/tmp/smartfactory-phase4-independent-20261005/bin/'):
            own.append({'pid': int(fields[0]), 'executable': fields[1]})
    result['own_binary_processes'] = own
except (PermissionError, subprocess.CalledProcessError) as error:
    result['process_check_error'] = str(error)
result['persistent_services_created'] = []
result['retained_prior_stage_schemas'] = ['sf_pilot_20261004']
result['retention_reference'] = {'path': str(ROOT / 'docs/evolution/handoff-storage.md'), 'line': 27}
result['remaining_independent_schemas'] = [name for name in result.get('database', {}).get('fixture_schemas', []) if name not in result['retained_prior_stage_schemas']]
result['resources_released'] = process.returncode == 0 and not result['remaining_independent_schemas'] and result.get('own_binary_processes') == []
result['disk_free_bytes'] = {p: shutil.disk_usage(p).free for p in ['/Volumes/Work', '/private/tmp']}
(OUT / 'resource-cleanup.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(result, ensure_ascii=False))
