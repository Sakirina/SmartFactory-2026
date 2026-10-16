import datetime
import hashlib
import json
import os
import pathlib
import subprocess

repo = pathlib.Path('/path/to/SmartFactory-2026')
evidence = repo / '.local/evolution/business-evidence'
work = pathlib.Path('/private/tmp/smartfactory-business-native-20261005-retry')
source = work / 'compiled-source'
original = work / 'api-native.test'
env = dict(os.environ, GOTOOLCHAIN='local', GOPATH='/private/tmp/smartfactory-evolution-gopath',
           GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache', GOCACHE='/private/tmp/smartfactory-evolution-go-cache',
           GOPROXY='off', GOMAXPROCS='2')
env.pop('GOFLAGS', None)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def info(path):
    return subprocess.check_output(['go', 'version', '-m', str(path)], env=env, text=True).splitlines()[1:]


report = {'original_binary': str(original), 'original_sha256': digest(original),
          'retained_source': str(source), 'original_build': ['go', 'test', '-c', '-o', str(original), './internal/api'],
          'original_cwd': str(repo / 'Platform'), 'runs': []}


def save():
    (evidence / 'native-rebuild-comparison.json').write_text(json.dumps(report, indent=2) + '\n')


def build(label, directory, flags=None):
    output = work / (label + '.test')
    args = ['go', 'test', '-c', '-o', str(output), './internal/api']
    run_env = dict(env)
    if flags:
        run_env['GOFLAGS'] = flags
    log = evidence / (label + '.log')
    item = {'command': args, 'cwd': str(directory), 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'log': str(log)}
    if flags:
        item['GOFLAGS'] = flags
    with log.open('w') as handle:
        result = subprocess.run(args, cwd=directory, env=run_env, stdout=handle, stderr=subprocess.STDOUT)
    item.update(completed_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), exit_code=result.returncode,
                log_sha256=digest(log))
    if result.returncode == 0:
        item.update(binary=str(output), binary_sha256=digest(output), original_bytes_match=output.read_bytes() == original.read_bytes(),
                    go_build_info_matches=info(output) == info(original))
    report['runs'].append(item)
    save()
    print(label, json.dumps({k: v for k, v in item.items() if k in ['exit_code', 'binary_sha256', 'original_bytes_match', 'go_build_info_matches']}), flush=True)
    if result.returncode:
        print(log.read_text()[-4000:], flush=True)
        raise SystemExit(result.returncode)
    return item


first = build('native-rebuild-same-parameters', source / 'Platform')
if not first['original_bytes_match']:
    replacements = {}
    for path in source.rglob('*'):
        if path.is_file() and (path.suffix in ['.go', '.sql'] or path.name in ['go.mod', 'go.sum', 'manifest.json']):
            replacements[str(repo / path.relative_to(source))] = str(path)
    omitted = []
    for module in ['Platform', 'DataTransfer']:
        for path in (repo / module).rglob('*.go'):
            if any(part in ['.cache', '.git', 'node_modules'] for part in path.relative_to(repo).parts):
                continue
            if not (source / path.relative_to(repo)).exists():
                replacements[str(path)] = ''
                omitted.append(str(path.relative_to(repo)))
    overlay = evidence / 'native-original-path-overlay.json'
    overlay.write_text(json.dumps({'Replace': replacements}, indent=2) + '\n')
    report['original_path_overlay'] = {'path': str(overlay), 'sha256': digest(overlay),
                                      'mapped_retained_files': sum(bool(value) for value in replacements.values()),
                                      'excluded_later_go_files': omitted,
                                      'purpose': 'Retain original compiler source paths while reading preserved source bytes; workspace files remain untouched'}
    second = build('native-rebuild-original-paths', repo / 'Platform', '-overlay=' + str(overlay))
    report['original_path_bytes_reproduced'] = second['original_bytes_match']
else:
    report['original_path_bytes_reproduced'] = True
report['completed_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
save()
