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
PUBLIC_DIRECTORIES = {'.github', 'DataTransfer', 'Platform', 'Frontends', 'Wiki', 'contracts', 'deploy', 'examples', 'scripts', 'tests'}
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
        if str(relative)=='README.md' or relative.parts[0] in {'Wiki','scripts','contracts','examples','deploy'} or relative.parts[:2]==('tests','fixtures'):
            targets.append(output/relative)
        for target in targets:
            target.parent.mkdir(parents=True,exist_ok=True)
            shutil.copy2(original,target)
    return source_commit


def rewrite_document_links(output):
    output = output.resolve()
    for document in [output/'README.md',*(output/'docs').rglob('*.md'),*(output/'Wiki').rglob('*.md')]:
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


def npm_components(root, output):
    """Keep locked publisher identity separate from npm installation aliases."""
    components=[]
    lock=json.loads((root/'Frontends/package-lock.json').read_text())
    for package,item in lock.get('packages',{}).items():
        if not package or not item.get('version'):continue
        installed_name=package.split('node_modules/')[-1]
        name=item.get('name',installed_name)
        package_id=name+'@'+item['version']
        location_id=hashlib.sha256(package.encode()).hexdigest()[:12]
        entry={'id':package_id+'#'+location_id,'package_id':package_id,'ecosystem':'npm','module':name,'version':item['version'],
               'installation_path':package,'installation_name':installed_name,'license':item.get('license','inspect package'),
               'resolved':item.get('resolved'),'integrity':item.get('integrity'),'development':item.get('dev',False),'license_files':[]}
        if installed_name!=name:entry['alias']=installed_name
        directory=root/'Frontends'/package
        entry['installed_for_build']=directory.is_dir()
        entry['optional']=item.get('optional',False)
        if directory.is_dir():
            metadata=directory/'package.json'
            if metadata.is_file():
                actual=json.loads(metadata.read_text())
                if actual.get('name')!=name or actual.get('version')!=item['version']:
                    raise RuntimeError('Installed npm identity differs from lock: '+package)
            files=[p for p in directory.iterdir() if p.is_file() and p.name.upper().startswith(('LICENSE','LICENCE','NOTICE','COPYING'))]
            if files:
                target=output/'licenses'/('npm__'+package_id.replace('/','__')+'__'+location_id);target.mkdir(parents=True,exist_ok=True)
                for path in files:shutil.copyfile(path,target/path.name);entry['license_files'].append(str((target/path.name).relative_to(output)))
        components.append(entry)
    return components


def go_runtime_license(goroot,version):
    goroot=Path(goroot)
    original=goroot/'LICENSE'
    if original.is_file():return original,{'license_layout':'GOROOT/LICENSE'}
    receipt=goroot.parent/'INSTALL_RECEIPT.json'
    packaged=goroot.parent/'LICENSE'
    if goroot.name=='libexec' and packaged.is_file() and receipt.is_file():
        installed=json.loads(receipt.read_text()).get('source',{}).get('versions',{}).get('stable')
        if installed==version:
            return packaged,{'license_layout':'Homebrew formula root above GOROOT/libexec','distribution':'homebrew',
                             'distribution_version':installed,'distribution_receipt_sha256':sha(receipt)}
    raise RuntimeError('Go runtime license is unavailable for the actual compiler version')


def build_inputs(source, modules, environment):
    """Index all selected Go, assembly and embedded inputs without personal paths."""
    source=source.resolve()
    entries={};observed={};decoder=json.JSONDecoder()
    for module in modules:
        patterns=['./cmd/'+name for name in modules[module]]+(['./examples/http-plugin'] if module=='DataTransfer' else [])
        raw=subprocess.check_output(['go','list','-deps','-json',*patterns],cwd=source/module,text=True,env=environment)
        while raw.strip():
            package,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:]
            directory=Path(package.get('Dir','/nonexistent')).resolve();declared=package.get('Module',{});item=declared.get('Replace',declared)
            for field in ['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','FFiles','SFiles','SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles','TestGoFiles','XTestGoFiles','TestEmbedFiles','XTestEmbedFiles']:
                for name in package.get(field,[]):
                    path=directory/name
                    if not path.is_file():continue
                    if path.resolve().is_relative_to(source):identifier='source/'+str(path.relative_to(source))
                    elif item.get('Version') and item.get('Dir'):
                        identifier='module/'+item['Path']+'@'+item['Version']+'/'+str(path.relative_to(Path(item['Dir']).resolve()))
                    else:identifier='stdlib/'+package.get('ImportPath','unknown')+'/'+name
                    value={'bytes':path.stat().st_size,'sha256':sha(path),'input_kinds':[field]}
                    if identifier in entries:
                        previous=entries[identifier]
                        if previous['sha256']!=value['sha256'] or previous['bytes']!=value['bytes']:raise RuntimeError('Build input identity collision: '+identifier)
                        value['input_kinds']=sorted(set(previous['input_kinds']+[field]))
                    entries[identifier]=value;observed[str(path)]=value['sha256']
            for module_info in [declared,declared.get('Replace',{})]:
                if not module_info.get('GoMod'):continue
                path=Path(module_info['GoMod']).resolve()
                if not path.is_file():raise RuntimeError('Selected Go module manifest is unavailable')
                if path.is_relative_to(source):identifier='source/'+str(path.relative_to(source))
                elif module_info.get('Version'):identifier='module/'+module_info['Path']+'@'+module_info['Version']+'/go.mod'
                else:raise RuntimeError('External unversioned Go module has no portable input identity')
                value={'bytes':path.stat().st_size,'sha256':sha(path),'input_kinds':['module_lock']}
                if identifier in entries and (entries[identifier]['sha256']!=value['sha256'] or entries[identifier]['bytes']!=value['bytes']):raise RuntimeError('Go module manifest identity collision: '+identifier)
                entries[identifier]=value;observed[str(path)]=value['sha256']
    for module in modules:
        for name in ['go.mod','go.sum']:
            path=source/module/name
            if path.is_file():
                entries['source/'+module+'/'+name]={'bytes':path.stat().st_size,'sha256':sha(path),'input_kinds':['module_lock']}
                observed[str(path)]=sha(path)
    return entries,observed


def main():
    from toolchain import environment as toolchain_environment, preflight

    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--os',default='linux',choices=['linux','darwin'])
    parser.add_argument('--arch',default='amd64',choices=['amd64','arm64'])
    parser.add_argument('--version',default='1.1.0-dev',help='immutable application version reported by release programs')
    parser.add_argument('--skip-frontend-build',action='store_true',help='use the already built, verified dist directory')
    parser.add_argument('--resume',action='store_true',help='finish this builder\'s previously interrupted output')
    args=parser.parse_args()
    output=args.output.resolve()
    if args.resume and not (output/'source/Platform/go.mod').is_file():raise SystemExit('Resume requires an existing release source snapshot')
    environment=dict(toolchain_environment(ROOT),GOOS=args.os,GOARCH=args.arch,CGO_ENABLED='0',GOTOOLCHAIN='local')
    try:
        toolchain_evidence=preflight(ROOT,environment)
    except RuntimeError as error:
        raise SystemExit('Build toolchain verification failed:\n'+str(error)) from error
    output.mkdir(parents=True,exist_ok=args.resume)
    (output/'bin').mkdir(exist_ok=args.resume)
    source_commit=copy_release_sources(output)
    source=output/'source'
    source_files={str(p.relative_to(source)):{'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted(source.rglob('*')) if p.is_file()}
    source_sha256=hashlib.sha256(json.dumps(source_files,sort_keys=True,separators=(',',':')).encode()).hexdigest()
    (output/'source-manifest.json').write_text(json.dumps({'format':'smartfactory-source-v1','sha256':source_sha256,'files':source_files},ensure_ascii=False,indent=2)+'\n')
    modules={'Platform':['sf-cloud','sf-edge','sf-config','sf-release-agent','sf-pki','sf-contracts','sf-model-mock','sf-hr-plugin','sf-api-benchmark','sf-site-fixture'],
             'DataTransfer':['datatransfer','dt-simulator','dt-benchmark']}
    inputs,observed=build_inputs(source,modules,environment)
    build_started=datetime.datetime.now(datetime.timezone.utc).isoformat()
    build_flags='-s -w -X competition2026/product/platform/pkg/buildinfo.Version='+args.version+' -X competition2026/product/platform/pkg/buildinfo.SourceSHA256='+source_sha256
    executions=[]
    logs=output/'build-logs';logs.mkdir(exist_ok=args.resume)
    def execute_build(name,argv,directory):
        started=datetime.datetime.now(datetime.timezone.utc).isoformat()
        log=logs/(name+'.log')
        with log.open('w') as handle:
            result=subprocess.run(argv,cwd=directory,env=environment,stdout=handle,stderr=subprocess.STDOUT)
        executions.append({'name':name,'argv':[str(value).replace(str(output),'<bundle>') for value in argv],
                           'working_directory':str(directory.relative_to(output)),'started_at':started,
                           'finished_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'exit_code':result.returncode,
                           'log':str(log.relative_to(output)),'log_sha256':sha(log)})
        (output/'build-commands.json').write_text(json.dumps({'format':'smartfactory-build-commands-v1','executions':executions},indent=2)+'\n')
        if result.returncode:raise SystemExit('Build failed: '+name+'; see '+str(log))
    for module,names in modules.items():
        execute_build(module,['go','build','-p','1','-trimpath','-ldflags',build_flags if module=='Platform' else '-s -w','-o',str(output/'bin')+'/',*[f'./cmd/{name}' for name in names]],source/module)
    execute_build('http-plugin',['go','build','-p','1','-trimpath','-ldflags=-s -w','-o',str(output/'bin/http-plugin'),'./examples/http-plugin'],source/'DataTransfer')
    if not args.skip_frontend_build:
        subprocess.run(['npm','run','build'],cwd=ROOT/'Frontends',env=environment,check=True)
    shutil.copytree(ROOT/'Frontends/dist',output/'Frontends/dist',dirs_exist_ok=args.resume)
    changed=[path for path,expected in observed.items() if sha(Path(path))!=expected]
    after_inputs,_=build_inputs(source,modules,environment)
    current,_=source_snapshot(output)
    current_files={str(relative):{'bytes':original.stat().st_size,'sha256':sha(original)} for original,relative in current}
    if changed or inputs!=after_inputs or current_files!=source_files:raise SystemExit('Build inputs changed while creating the release')
    build_finished=datetime.datetime.now(datetime.timezone.utc).isoformat()
    (output/'build-inputs.json').write_text(json.dumps({'format':'smartfactory-build-inputs-v1','started_at':build_started,'finished_at':build_finished,'source_sha256':source_sha256,
            'input_scope':'All file fields declared by go list for selected production packages and dependencies, including their declared test resources; production commands compile the listed commands without test variants',
            'environment':{key:environment[key] for key in ['GOOS','GOARCH','CGO_ENABLED','GOTOOLCHAIN','GOMAXPROCS','GOPROXY'] if key in environment},'inputs_before':inputs,'inputs_after':after_inputs,
            'commands':{module:{'working_directory':'source/'+module,'packages':[f'./cmd/{name}' for name in names],'ldflags':build_flags if module=='Platform' else '-s -w'} for module,names in modules.items()},
            'executions':executions,
            'binaries':{p.name:{'bytes':p.stat().st_size,'sha256':sha(p)} for p in sorted((output/'bin').iterdir())}},ensure_ascii=False,indent=2)+'\n')
    rewrite_document_links(output)
    components=[]
    licenses=output/'licenses';licenses.mkdir(exist_ok=args.resume)
    goroot=subprocess.check_output(['go','env','GOROOT'],text=True,env=environment).strip()
    if goroot:
        runtime_version=(Path(goroot)/'VERSION').read_text().splitlines()[0]
        if runtime_version!='go'+toolchain_evidence['observed']['go']:raise SystemExit('Go runtime source differs from actual compiler')
        runtime_license,runtime_distribution=go_runtime_license(goroot,toolchain_evidence['observed']['go'])
        target=licenses/('go__'+runtime_version);target.mkdir(exist_ok=True)
        shutil.copyfile(runtime_license,target/'LICENSE')
        components.append({'id':'go@'+toolchain_evidence['observed']['go'],'ecosystem':'toolchain','module':'go','version':toolchain_evidence['observed']['go'],
                           'standard_library_and_runtime':True,'source':'https://go.googlesource.com/go/+/refs/tags/'+runtime_version,
                           'source_version':runtime_version,'license':'BSD-3-Clause','license_files':[str((target/'LICENSE').relative_to(output))],
                           **runtime_distribution,
                           'compiler_sha256':sha(Path(shutil.which('go',path=environment.get('PATH'))))})
    decoder=json.JSONDecoder()
    for module in modules:
        patterns=['./cmd/...']+(['./examples/http-plugin'] if module=='DataTransfer' else [])
        raw=subprocess.check_output(['go','list','-deps','-json',*patterns],cwd=source/module,text=True,env=environment)
        while raw.strip():
            package,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:]
            item=package.get('Module',{})
            if not item.get('Version'):continue
            identifier=item['Path']+'@'+item['Version']
            if any(c['id']==identifier for c in components):continue
            directory=Path(item.get('Dir','/nonexistent'))
            entry={'id':identifier,'ecosystem':'go','module':item['Path'],'version':item['Version'],'first_party':directory.resolve().is_relative_to(source),'license_files':[]}
            target=licenses/identifier.replace('/','__');target.mkdir(exist_ok=True)
            for name in ['LICENSE','LICENSE.txt','LICENSE.md','COPYING','NOTICE']:
                path=directory/name
                if path.is_file():shutil.copyfile(path,target/name);entry['license_files'].append(str((target/name).relative_to(output)))
            components.append(entry)
    components.extend(npm_components(ROOT,output))
    sources=json.loads((ROOT/'deploy/dependencies.json').read_text())['sources']
    components.extend({'id':key,'ecosystem':'container','image':item['pinned'],'source':sources.get(key),
                       'license_scope':sources.get(key,{}).get('license_scope','Primary upstream program; operating-system and other image packages retain their individual publisher licenses'),
                       'license_files':[]} for key,item in json.loads((ROOT/'deploy/images.lock.json').read_text())['images'].items())
    for notice in json.loads((ROOT/'deploy/licenses/provenance.json').read_text()):
        notice_file=ROOT/notice['included_file']
        if sha(notice_file)!=notice['sha256']:raise SystemExit('Upstream license provenance differs: '+notice['included_file'])
        for entry in components:
            if entry.get('module',entry['id'])!=notice['component']:continue
            if notice.get('version') and entry.get('version')!=notice['version']:continue
            target=licenses/('upstream__'+notice['component'].replace('/','__'))/Path(notice['included_file']).name
            target.parent.mkdir(parents=True,exist_ok=True)
            shutil.copyfile(notice_file,target)
            entry['license_files'].append(str(target.relative_to(output)))
            entry.setdefault('license_provenance',[]).append({key:notice[key] for key in ['source_commit','source_ref','source_path','source_url','source_kind','sha256','publisher_manifest_url','publisher_integrity','license_material'] if key in notice})
    (output/'components.json').write_text(json.dumps({'format':'smartfactory-components-v1','components':components},ensure_ascii=False,indent=2)+'\n')
    manifest={'format':'smartfactory-release-v1','created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'target':args.os+'/'+args.arch,
              'source_commit':source_commit,'source_sha256':source_sha256,'application_version':args.version,
              'working_tree_included':True,'go_version':subprocess.check_output(['go','version'],text=True,env=environment).strip(),
              'toolchains':toolchain_evidence,'files':{}}
    for path in sorted(output.rglob('*')):
        if path.is_file() and path!=output/'release-manifest.json':manifest['files'][str(path.relative_to(output))]={'bytes':path.stat().st_size,'sha256':sha(path)}
    (output/'release-manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
    print(f"Release created: {output}; {len(manifest['files'])} files, {sum(v['bytes'] for v in manifest['files'].values())} bytes")


if __name__=='__main__':main()
