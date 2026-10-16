#!/usr/bin/env python3
"""Independent phase-three checks, preserving each attempt and source snapshot."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[3]
OUT = Path(__file__).resolve().parent
TMP = Path('/private/tmp/smartfactory-phase3-independent')
TMP.mkdir(exist_ok=True)
ENV = os.environ.copy()
ENV.update(GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath',
           GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
           GOCACHE='/private/tmp/smartfactory-evolution-go-cache', GOPROXY='off', GOMAXPROCS='2')
ENV['PATH'] = str(ROOT / '.local/toolchains/node-v24.21.0-darwin-arm64/bin') + os.pathsep + ENV['PATH']
for key in ['SF_TEST_POSTGRES_DATABASE', 'SF_HR_PLUGIN_BINARY']:
    ENV.pop(key, None)

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def snapshot(scope):
    files = []
    for directory in scope:
        base = ROOT / directory
        if base.is_file():
            files.append(base)
        elif base.exists():
            files.extend(p for p in base.rglob('*') if p.is_file()
                         and not any(s in p.parts for s in ['node_modules', '.git', 'dist', '__pycache__', '.local', '.cache', '.tools', '.pytest_cache'])
                         and p.name != '.DS_Store')
    return {str(p.relative_to(ROOT)): sha(p) for p in sorted(set(files))}

def json_write(path, value):
    Path(path).write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')

def counts(raw):
    results = {}
    output = {}
    packages = {}
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except (ValueError, TypeError):
            continue
        package, test, action = event.get('Package', ''), event.get('Test'), event.get('Action')
        if test:
            key = (package, test)
            if action == 'output':
                output.setdefault(key, []).append(event.get('Output', ''))
            if action in ['pass', 'fail', 'skip']:
                results[key] = action
        elif action in ['pass', 'fail', 'skip']:
            packages[package] = action
    def total(items):
        items = list(items)
        return {a: sum(v == a for v in items) for a in ['pass', 'fail', 'skip']}
    return dict(top_level=total(v for (_, t), v in results.items() if '/' not in t),
                leaf_cases=total(v for (p, t), v in results.items()
                    if not any(p == p2 and t2.startswith(t + '/') for p2, t2 in results)),
                all_named_cases=total(results.values()), packages=total(packages.values()),
                conditional_skips=[dict(package=p, test=t, reason=''.join(output.get((p, t), [])))
                    for (p, t), v in results.items() if v == 'skip'],
                failures=[dict(package=p, test=t, output=''.join(output.get((p, t), [])))
                    for (p, t), v in results.items() if v == 'fail'],
                data_race_occurrences=raw.count('WARNING: DATA RACE'))

def run(label, argv, cwd, scope, extra_env=None, timeout=1200):
    start = datetime.datetime.now(datetime.timezone.utc).isoformat()
    before = snapshot(scope)
    env = ENV.copy()
    env.update(extra_env or {})
    secrets = []
    if env.get('SF_TEST_POSTGRES_DATABASE'):
        from urllib.parse import urlparse
        secrets = [env['SF_TEST_POSTGRES_DATABASE']]
        password = urlparse(secrets[0]).password
        if password:
            secrets.append(password)
    then = time.monotonic()
    try:
        process = subprocess.run(argv, cwd=ROOT / cwd, env=env, capture_output=True,
                                 timeout=timeout, text=True, errors='replace')
        code, stdout, stderr = process.returncode, process.stdout, process.stderr
    except subprocess.TimeoutExpired as error:
        code = 124
        stdout = error.stdout or ''
        stderr = error.stderr or ''
        if isinstance(stdout, bytes): stdout = stdout.decode(errors='replace')
        if isinstance(stderr, bytes): stderr = stderr.decode(errors='replace')
    for secret in secrets:
        stdout, stderr = stdout.replace(secret, '<redacted>'), stderr.replace(secret, '<redacted>')
    after = snapshot(scope)
    stdout_path, stderr_path = OUT / (label + '.stdout.log'), OUT / (label + '.stderr.log')
    stdout_path.write_text(stdout)
    stderr_path.write_text(stderr)
    meta = dict(label=label, argv=argv, cwd=str(ROOT / cwd), started_at=start,
        completed_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
        elapsed_seconds=round(time.monotonic() - then, 3), exit_code=code,
        environment={k: env[k] for k in ['GOTOOLCHAIN','GOPATH','GOMODCACHE','GOCACHE','GOPROXY','GOMAXPROCS']},
        postgres_fixture=bool(env.get('SF_TEST_POSTGRES_DATABASE')),
        source_before=before, source_after=after,
        changed_sources=[p for p in set(before) | set(after) if before.get(p) != after.get(p)],
        stdout=dict(path=str(stdout_path.relative_to(ROOT)), sha256=sha(stdout_path)),
        stderr=dict(path=str(stderr_path.relative_to(ROOT)), sha256=sha(stderr_path)),
        counts=counts(stdout) if '-json' in argv else None)
    json_write(OUT / (label + '.metadata.json'), meta)
    print(json.dumps({k:meta[k] for k in ['label','exit_code','elapsed_seconds','changed_sources']}, ensure_ascii=False), flush=True)
    return meta

if __name__ == '__main__':
    mode = sys.argv[1]
    backend = ['Platform', 'DataTransfer']
    opts = ['go','test','-json','-count=1','-p=1','-parallel=1','-timeout=180s']
    if mode == 'ordinary':
        run('datatransfer-full', opts+['./...'], 'DataTransfer', ['DataTransfer'], timeout=900)
        run('platform-full', opts+['./...'], 'Platform', backend, timeout=900)
        plugin = str(TMP / 'sf-hr-plugin')
        built = run('plugin-build', ['go','build','-o',plugin,'./cmd/sf-hr-plugin'], 'Platform', backend)
        if built['exit_code'] == 0:
            json_write(OUT/'plugin-binary.json', {'path':plugin, 'sha256':sha(plugin), 'build_info':subprocess.check_output(['go','version','-m',plugin],env=ENV,text=True)})
            run('plugin-actual-process', opts+['./internal/plugins','-run','^TestActualOrganizationProcessPullAndCrashIsolation$'], 'Platform', backend, {'SF_HR_PLUGIN_BINARY':plugin})
    elif mode == 'postgres':
        dsn = json.loads((ROOT / '.local/evolution/pg18-test.json').read_text())['dsn']
        run('postgres-specialized', opts+['./internal/store','./internal/tasks','./internal/api','./internal/application','./internal/compiledplan','./internal/control','-run','^TestPostgres'], 'Platform', backend, {'SF_TEST_POSTGRES_DATABASE':dsn},timeout=900)
    elif mode == 'race':
        options = opts+['-race']
        run('control-race',options+['./internal/control','./internal/cloudsync','./internal/coordination','./internal/datatransfer'],'Platform',backend,timeout=900)
        run('history-api-race',options+['./internal/application','./internal/api','-run','^Test(HistoryReplay|HistoryCompare|HistoryCancellation|HistoryAuthorization|HistoryRiver|ShadowProduction|ShadowCalculation|ShadowCandidateStop|ShadowDeliveryInterruption|HistoryHTTP|ControlHTTP)'],'Platform',backend,timeout=900)
        run('datatransfer-command-race',options+['./internal/command','./internal/state','./internal/northbound/grpc'],'DataTransfer',['DataTransfer'],timeout=900)
    elif mode == 'contracts':
        directory=TMP/'contracts/1.0'
        examples=TMP/'examples.json'
        generated=run('go-contract-generation',['go','run','./cmd/sf-contracts','-out',str(directory),'-examples',str(examples)],'Platform',backend+['contracts','examples/factory/definitions.json'])
        if generated['exit_code']==0:
            pairs=[(p,ROOT/'contracts/1.0'/p.name) for p in sorted(directory.glob('*'))]
            pairs += [(directory.parent/'compatibility.json',ROOT/'contracts/compatibility.json'),(examples,ROOT/'examples/factory/definitions.json')]
            rows=[{'path':str(target.relative_to(ROOT)),'generated_sha256':sha(p),'current_sha256':sha(target) if target.exists() else None,'identical':target.exists() and sha(p)==sha(target)} for p,target in pairs]
            json_write(OUT/'contract-byte-comparison.json',{'files':rows,'mismatches':[v['path'] for v in rows if not v['identical']]})
            print(json.dumps({'label':'contract-byte-comparison','files':len(rows),'mismatches':[v['path'] for v in rows if not v['identical']]}),flush=True)
        run('contract-schema-validation',['/private/tmp/smartfactory-evolution-python/bin/python','scripts/check-contracts.py'],'',['contracts','examples/factory/definitions.json','scripts/check-contracts.py'])
