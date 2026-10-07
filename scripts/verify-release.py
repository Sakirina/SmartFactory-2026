#!/usr/bin/env python3
"""Verify every file in a SmartFactory release before installation."""
import argparse
import hashlib
import json
from pathlib import Path

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('directory',type=Path)
args=parser.parse_args()
root=args.directory.resolve()
manifest=json.loads((root/'release-manifest.json').read_text())
if manifest.get('format')!='smartfactory-release-v1':raise SystemExit('Unsupported release manifest')
actual=set()
for path in root.rglob('*'):
    if path.is_symlink():raise SystemExit('Unsafe release path: '+str(path.relative_to(root)))
    if path.is_file() and path!=root/'release-manifest.json':actual.add(str(path.relative_to(root)))
unlisted=actual-set(manifest['files'])
if unlisted:raise SystemExit('Unlisted release files: '+', '.join(sorted(unlisted)))
for relative,expected in manifest['files'].items():
    path=root/relative
    if path.is_symlink() or not path.resolve().is_relative_to(root):raise SystemExit('Unsafe release path: '+relative)
    digest=hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda:source.read(1024*1024),b''):digest.update(block)
    if path.stat().st_size!=expected['bytes'] or digest.hexdigest()!=expected['sha256']:raise SystemExit('Release mismatch: '+relative)
source_manifest=root/'source-manifest.json'
if source_manifest.is_file():
    source=json.loads(source_manifest.read_text())
    canonical=hashlib.sha256(json.dumps(source['files'],sort_keys=True,separators=(',',':')).encode()).hexdigest()
    if canonical!=source.get('sha256') or canonical!=manifest.get('source_sha256'):raise SystemExit('Release source identity mismatch')
    for relative,item in source['files'].items():
        if manifest['files'].get('source/'+relative)!=item:raise SystemExit('Release source mismatch: '+relative)
build_inputs=root/'build-inputs.json'
if build_inputs.is_file():
    inputs=json.loads(build_inputs.read_text())
    if inputs.get('inputs_before')!=inputs.get('inputs_after'):raise SystemExit('Release build inputs changed')
    if inputs.get('source_sha256')!=manifest.get('source_sha256'):raise SystemExit('Release build source mismatch')
    for name,item in inputs.get('binaries',{}).items():
        if manifest['files'].get('bin/'+name)!=item:raise SystemExit('Release binary provenance mismatch: '+name)
    for execution in inputs.get('executions',[]):
        log=manifest['files'].get(execution['log'])
        if execution.get('exit_code')!=0 or log is None or log['sha256']!=execution.get('log_sha256'):raise SystemExit('Release build command evidence mismatch: '+execution['name'])
print('Verified '+str(len(manifest['files']))+' files for '+manifest['target'])
