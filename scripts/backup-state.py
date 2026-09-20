#!/usr/bin/env python3
"""Create, verify and restore explicit SmartFactory state snapshots."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for part in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(part)
    return value.hexdigest()


def inventory(path):
    with sqlite3.connect(path.as_uri() + '?mode=ro', uri=True) as db:
        if db.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
            raise ValueError('SQLite consistency check failed')
        tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
        counts = {name: db.execute('SELECT count(*) FROM "' + name.replace('"', '""') + '"').fetchone()[0] for name in tables}
        versions = {}
        if 'documents' in tables:
            for kind, identifier, version in db.execute('SELECT kind,id,version FROM documents ORDER BY kind,id'):
                versions[kind + ':' + identifier] = version
        return {'table_counts': counts, 'document_versions': versions}


def named(values):
    result = []
    for value in values:
        name, separator, source = value.partition('=')
        if not separator or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,79}', name):
            raise ValueError('Use a simple name=path for each input')
        result.append((name, Path(source).resolve()))
    return result


def verify(directory):
    manifest = json.loads((directory / 'manifest.json').read_text())
    if manifest.get('schema_version') != 1 or not isinstance(manifest.get('files'), list):
        raise ValueError('Unsupported snapshot manifest')
    for item in manifest['files']:
        name = item['name']
        if Path(name).name != name or name in ('.', '..') or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,79}', name):
            raise ValueError('Unsafe snapshot filename')
        path = directory / name
        if path.is_symlink() or path.stat().st_size != item['bytes'] or digest(path) != item['sha256']:
            raise ValueError('Snapshot hash or size mismatch: ' + name)
        if item['kind'] == 'sqlite' and inventory(path) != item['inventory']:
            raise ValueError('Database inventory mismatch: ' + name)
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='action', required=True)
    create = commands.add_parser('create')
    create.add_argument('--output', type=Path, required=True)
    create.add_argument('--sqlite', action='append', default=[], metavar='NAME=PATH')
    create.add_argument('--file', action='append', default=[], metavar='NAME=PATH')
    create.add_argument('--postgres', action='append', default=[], metavar='NAME=CONTAINER,DATABASE,USER')
    create.add_argument('--quiesced', action='store_true', required=True, help='confirm application writers have stopped for this snapshot')
    check = commands.add_parser('verify')
    check.add_argument('snapshot', type=Path)
    restore = commands.add_parser('restore')
    restore.add_argument('snapshot', type=Path)
    restore.add_argument('--output', type=Path, required=True, help='new directory only')
    pg = commands.add_parser('restore-postgres')
    pg.add_argument('snapshot', type=Path)
    pg.add_argument('--name', required=True)
    pg.add_argument('--container', required=True)
    pg.add_argument('--database', required=True)
    pg.add_argument('--user', default='postgres')
    args = parser.parse_args()
    os.umask(0o077)
    if args.action == 'create':
        inputs = [('sqlite', n, p) for n, p in named(args.sqlite)] + [('file', n, p) for n, p in named(args.file)]
        names = [n for _, n, _ in inputs]
        pginputs = []
        for spec in args.postgres:
            name, separator, value = spec.partition('=')
            if not separator or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,79}', name) or len(value.split(',')) != 3:
                raise ValueError('PostgreSQL input requires NAME=CONTAINER,DATABASE,USER')
            names.append(name)
            pginputs.append((name, value.split(',')))
        if not names or len(set(names)) != len(names) or 'manifest.json' in names:
            raise ValueError('Provide unique snapshot names')
        for _, _, source in inputs:
            if not source.is_file():
                raise ValueError('Input must be an existing file: ' + str(source))
        out = args.output.resolve()
        out.mkdir(mode=0o700, parents=True, exist_ok=False)
        manifest = {'schema_version': 1, 'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'quiesced': True, 'files': []}
        for kind, name, source in inputs:
            target = out / name
            if kind == 'sqlite':
                with sqlite3.connect(source.as_uri() + '?mode=ro', uri=True) as src, sqlite3.connect(target) as dst:
                    src.backup(dst, pages=256)
            else:
                shutil.copyfile(source, target)
            target.chmod(0o600)
            item = {'name': name, 'kind': kind, 'bytes': target.stat().st_size, 'sha256': digest(target)}
            if kind == 'sqlite':
                item['inventory'] = inventory(target)
            manifest['files'].append(item)
        for name, (container, database, user) in pginputs:
            target = out / name
            with target.open('xb') as stream:
                subprocess.run(['docker', 'exec', container, 'pg_dump', '--username', user, '--dbname', database, '--format=custom', '--compress=6', '--no-owner', '--no-acl'], stdout=stream, check=True)
            manifest['files'].append({'name': name, 'kind': 'postgres', 'bytes': target.stat().st_size, 'sha256': digest(target), 'database': database})
        (out / 'manifest.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')
        verify(out)
        print('Snapshot verified: ' + str(out))
        return
    source = args.snapshot.resolve()
    manifest = verify(source)
    if args.action == 'verify':
        print('Verified ' + str(len(manifest['files'])) + ' state files')
    elif args.action == 'restore':
        out = args.output.resolve()
        out.mkdir(mode=0o700, parents=True, exist_ok=False)
        for item in manifest['files']:
            shutil.copyfile(source / item['name'], out / item['name'])
            (out / item['name']).chmod(0o600)
        shutil.copyfile(source / 'manifest.json', out / 'manifest.json')
        verify(out)
        print('Restored and verified state in ' + str(out))
    else:
        item = next((x for x in manifest['files'] if x['name'] == args.name and x['kind'] == 'postgres'), None)
        if not item:
            raise ValueError('PostgreSQL dump not found')
        base = ['docker', 'exec', args.container, 'psql', '--username', args.user, '--dbname', args.database, '-At', '-c']
        count = subprocess.check_output(base + ["SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND c.relkind IN ('r','p','v','m','S')"], text=True).strip()
        if count != '0':
            raise ValueError('Restore requires an empty target database')
        with (source / args.name).open('rb') as stream:
            subprocess.run(['docker', 'exec', '-i', args.container, 'pg_restore', '--username', args.user, '--dbname', args.database, '--single-transaction', '--exit-on-error', '--no-owner', '--no-acl'], stdin=stream, check=True)
        print('PostgreSQL snapshot restored into empty database ' + args.database)


if __name__ == '__main__':
    main()
