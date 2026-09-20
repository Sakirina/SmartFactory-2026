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
for relative,expected in manifest['files'].items():
    path=root/relative
    if path.is_symlink() or not path.resolve().is_relative_to(root):raise SystemExit('Unsafe release path: '+relative)
    digest=hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda:source.read(1024*1024),b''):digest.update(block)
    if path.stat().st_size!=expected['bytes'] or digest.hexdigest()!=expected['sha256']:raise SystemExit('Release mismatch: '+relative)
print('Verified '+str(len(manifest['files']))+' files for '+manifest['target'])
