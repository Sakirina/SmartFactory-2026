#!/usr/bin/env python3
"""Build a portable release without including local credentials or test databases."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]
PUBLIC_DIRECTORIES = {'.github', 'DataTransfer', 'Platform', 'Frontends', 'contracts', 'deploy', 'examples', 'scripts', 'tests'}
PUBLIC_ROOT_FILES = {'.gitattributes', '.gitignore', 'README.md', 'LICENSE', 'LICENSE.md', 'LICENSE.txt'}
EXCLUDED_DIRECTORIES = {'.local', '.git', '.agents', '.codex', '.cache', '.tools', '.venv', 'node_modules', '__pycache__', '.pytest_cache', 'dist', 'bin', 'coverage', 'test-results', 'playwright-report'}
INTERNAL_SCRIPTS = {'create-product-document.py', 'create-product-presentation.mjs', 'record-demo.mjs'}


def public_source_path(relative):
    if relative.parts[0] not in PUBLIC_DIRECTORIES and str(relative) not in PUBLIC_ROOT_FILES:return False
    if relative.parts[:2] == ('DataTransfer', 'docs'):return False
    if any(part in EXCLUDED_DIRECTORIES or part.startswith('.chart-data-') for part in relative.parts):return False
    name=relative.name
    if name in {'.DS_Store', 'AGENTS.md'} or name in INTERNAL_SCRIPTS:return False
    if name == '.env' or (name.startswith('.env.') and name not in {'.env.example', '.env.sample'}):return False
    if name.endswith(('-credentials.json', '.tsbuildinfo')):return False
    return relative.suffix.lower() not in {'.db', '.sqlite', '.sqlite3', '.log', '.pyc', '.pyo', '.key', '.pem', '.p12', '.pfx', '.zip', '.tar', '.gz', '.tgz', '.7z', '.docx', '.pptx', '.xlsx', '.pdf', '.mp4', '.srt'} and not name.endswith(('.db-shm', '.db-wal'))


def sha(path):
    h=hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda:source.read(1024*1024),b''):h.update(block)
    return h.hexdigest()


def source_snapshot(output):
    probe=subprocess.run(['git','rev-parse','--show-toplevel'],cwd=ROOT,capture_output=True,text=True)
    if probe.returncode==0 and Path(probe.stdout.strip()).resolve()==ROOT:
        paths=[ROOT/Path(os.fsdecode(p)) for p in subprocess.check_output(['git','ls-files','--cached','--others','--exclude-standard','-z'],cwd=ROOT).split(b'\0') if p]
        commit=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
    else:
        paths=ROOT.rglob('*')
        previous=ROOT.parent/'release-manifest.json'
        commit=json.loads(previous.read_text()).get('source_commit','unversioned-source') if previous.is_file() else 'unversioned-source'
    selected=[]
    for original in paths:
        relative=original.relative_to(ROOT)
        if original.is_relative_to(output) or not public_source_path(relative):continue
        if original.is_file() and not original.is_symlink() and original.resolve().is_relative_to(ROOT):selected.append((original,relative))
    return selected,commit


def copy_release_sources(output):
    paths,source_commit=source_snapshot(output)
    for original,relative in paths:
        targets=[output/'source'/relative]
        if str(relative)=='README.md' or relative.parts[0] in {'scripts','contracts','examples','deploy'} or relative.parts[:2]==('tests','fixtures'):
            targets.append(output/relative)
        for target in targets:
            target.parent.mkdir(parents=True,exist_ok=True)
            shutil.copy2(original,target)
    return source_commit


def rewrite_document_links(output):
    for document in [output/'README.md',*(output/'docs').rglob('*.md')]:
        def replace(match):
            target=match.group(1)
            if re.match(r'[a-zA-Z]+:',target) or target.startswith('#'):return match.group(0)
            name,separator,fragment=target.partition('#')
            existing=(document.parent/name).resolve()
            if existing.exists() or not existing.is_relative_to(output):return match.group(0)
            source=output/'source'/existing.relative_to(output)
            if not source.exists():return match.group(0)
            return ']('+os.path.relpath(source,document.parent)+separator+fragment+')'
        document.write_text(re.sub(r'\]\(([^)]+)\)',replace,document.read_text()))


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--os',default='linux',choices=['linux','darwin'])
    parser.add_argument('--arch',default='amd64',choices=['amd64','arm64'])
    parser.add_argument('--skip-frontend-build',action='store_true',help='use the already built, verified dist directory')
    parser.add_argument('--resume',action='store_true',help='finish this builder\'s previously interrupted output')
    args=parser.parse_args()
    output=args.output.resolve()
    if args.resume and not (output/'source/Platform/go.mod').is_file():raise SystemExit('Resume requires an existing release source snapshot')
    output.mkdir(parents=True,exist_ok=args.resume)
    (output/'bin').mkdir(exist_ok=args.resume)
    environment=dict(os.environ,GOOS=args.os,GOARCH=args.arch,CGO_ENABLED='0')
    modules={'Platform':['sf-cloud','sf-edge','sf-config','sf-pki','sf-contracts','sf-model-mock','sf-hr-plugin','sf-api-benchmark','sf-site-fixture'],
             'DataTransfer':['datatransfer','dt-simulator','dt-benchmark']}
    for module,names in modules.items():
        subprocess.run(['go','build','-trimpath','-ldflags=-s -w','-o',str(output/'bin')+'/',*[f'./cmd/{name}' for name in names]],cwd=ROOT/module,env=environment,check=True)
    subprocess.run(['go','build','-trimpath','-ldflags=-s -w','-o',str(output/'bin/http-plugin'),'./examples/http-plugin'],cwd=ROOT/'DataTransfer',env=environment,check=True)
    if not args.skip_frontend_build:
        subprocess.run(['npm','run','build'],cwd=ROOT/'Frontends',check=True)
    shutil.copytree(ROOT/'Frontends/dist',output/'Frontends/dist',dirs_exist_ok=args.resume)
    # Apply the same public-file selection to Git checkouts and unpacked source snapshots.
    source_commit=copy_release_sources(output)
    rewrite_document_links(output)
    components=[]
    licenses=output/'licenses';licenses.mkdir(exist_ok=args.resume)
    decoder=json.JSONDecoder()
    for module in modules:
        patterns=['./cmd/...']+(['./examples/http-plugin'] if module=='DataTransfer' else [])
        raw=subprocess.check_output(['go','list','-deps','-json',*patterns],cwd=ROOT/module,text=True,env=environment)
        while raw.strip():
            package,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:]
            item=package.get('Module',{})
            if not item.get('Version'):continue
            identifier=item['Path']+'@'+item['Version']
            if any(c['id']==identifier for c in components):continue
            directory=Path(item.get('Dir','/nonexistent'))
            entry={'id':identifier,'ecosystem':'go','module':item['Path'],'version':item['Version'],'first_party':directory.resolve().is_relative_to(ROOT),'license_files':[]}
            target=licenses/identifier.replace('/','__');target.mkdir(exist_ok=True)
            for name in ['LICENSE','LICENSE.txt','LICENSE.md','COPYING','NOTICE']:
                path=directory/name
                if path.is_file():shutil.copyfile(path,target/name);entry['license_files'].append(str((target/name).relative_to(output)))
            components.append(entry)
    lock=json.loads((ROOT/'Frontends/package-lock.json').read_text())
    for package,item in lock.get('packages',{}).items():
        if not package or not item.get('version'):continue
        name=package.split('node_modules/')[-1]
        entry={'id':name+'@'+item['version'],'ecosystem':'npm','module':name,'version':item['version'],'license':item.get('license','inspect package'),'development':item.get('dev',False),'license_files':[]}
        directory=ROOT/'Frontends'/package
        entry['installed_for_build']=directory.is_dir()
        entry['optional']=item.get('optional',False)
        if directory.is_dir():
            files=[p for p in directory.iterdir() if p.is_file() and p.name.upper().startswith(('LICENSE','LICENCE','NOTICE','COPYING'))]
            if files:
                target=licenses/('npm__'+entry['id'].replace('/','__'));target.mkdir(exist_ok=True)
                for path in files:shutil.copyfile(path,target/path.name);entry['license_files'].append(str((target/path.name).relative_to(output)))
        components.append(entry)
    sources=json.loads((ROOT/'deploy/dependencies.json').read_text())['sources']
    components.extend({'id':key,'ecosystem':'container','image':item['pinned'],'source':sources.get(key),'license_files':[]} for key,item in json.loads((ROOT/'deploy/images.lock.json').read_text())['images'].items())
    for notice in json.loads((ROOT/'deploy/licenses/provenance.json').read_text()):
        for entry in components:
            if entry.get('module',entry['id'])!=notice['component']:continue
            target=licenses/('upstream__'+notice['component'].replace('/','__'))/Path(notice['included_file']).name
            target.parent.mkdir(parents=True,exist_ok=True)
            shutil.copyfile(ROOT/notice['included_file'],target)
            entry['license_files'].append(str(target.relative_to(output)))
    (output/'components.json').write_text(json.dumps({'format':'smartfactory-components-v1','components':components},ensure_ascii=False,indent=2)+'\n')
    manifest={'format':'smartfactory-release-v1','created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'target':args.os+'/'+args.arch,
              'source_commit':source_commit,
              'working_tree_included':True,'go_version':subprocess.check_output(['go','version'],text=True).strip(),'files':{}}
    for path in sorted(output.rglob('*')):
        if path.is_file() and path.name!='release-manifest.json':manifest['files'][str(path.relative_to(output))]={'bytes':path.stat().st_size,'sha256':sha(path)}
    (output/'release-manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
    print(f"Release created: {output}; {len(manifest['files'])} files, {sum(v['bytes'] for v in manifest['files'].values())} bytes")


if __name__=='__main__':main()
