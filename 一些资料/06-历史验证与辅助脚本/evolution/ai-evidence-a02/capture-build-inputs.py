#!/usr/bin/env python3
import datetime, hashlib, json, os, pathlib, shutil, subprocess

root = pathlib.Path('/path/to/SmartFactory-2026')
snapshot = json.loads((root/'.local/evolution/ai-evidence-a02/final-snapshot.json').read_text())
workspace = pathlib.Path(snapshot['workspace'])
base = workspace.parent
out = root/'.local/evolution/ai-evidence-a02'
tests = ['./internal/ai', './internal/application', './internal/a02reviewv2', './internal/a02reviewv3', './internal/a02reviewv3corrected']
commands = ['./cmd/sf-cloud', './cmd/sf-edge', './cmd/sf-contracts', './cmd/sf-ai-evidence-fixture']
specs = [('ordinary-tests', ['go','list','-p','1','-deps','-test','-json',*tests]),
         ('application-commands', ['go','list','-p','1','-deps','-json',*commands]),
         ('limited-race-tests', ['go','list','-p','1','-race','-deps','-test','-json','./internal/ai','./internal/application'])]
decoder = json.JSONDecoder()
inputs = {}
generated = []
manifests = []
for label, command in specs:
    process = subprocess.run(command, cwd=workspace/'Platform', env=os.environ, capture_output=True, text=True)
    log = base/(label+'-go-list.json')
    log.write_text(process.stdout)
    (base/(label+'-go-list.stderr.log')).write_text(process.stderr)
    if process.returncode:
        raise RuntimeError(label+': '+process.stderr)
    packages = []; pos = 0
    while pos < len(process.stdout):
        while pos < len(process.stdout) and process.stdout[pos].isspace(): pos += 1
        if pos == len(process.stdout): break
        package, pos = decoder.raw_decode(process.stdout, pos)
        if package.get('Error') or package.get('DepsErrors'): raise RuntimeError(str(package))
        packages.append(package)
        directory = pathlib.Path(package.get('Dir', workspace/'Platform'))
        for field in ['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','SFiles','SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles']:
            for name in package.get(field,[]):
                path = pathlib.Path(name)
                if not path.is_absolute(): path = directory/path
                if not path.is_file():
                    generated.append({'spec':label,'package':package['ImportPath'],'field':field,'path':str(path),'reason':'toolchain-generated test main or build intermediary'})
                    continue
                item = inputs.setdefault(str(path), {'path':str(path),'fields':set(),'packages':set(),'specs':set()})
                item['fields'].add(field); item['packages'].add(package['ImportPath']); item['specs'].add(label)
        module = package.get('Module') or {}
        for candidate in [module.get('GoMod'), (module.get('Replace') or {}).get('GoMod')]:
            if not candidate: continue
            path = pathlib.Path(candidate)
            if path.is_file():
                item = inputs.setdefault(str(path), {'path':str(path),'fields':set(),'packages':set(),'specs':set()})
                item['fields'].add('GoMod'); item['packages'].add(package['ImportPath']); item['specs'].add(label)
    manifests.append({'label':label,'command':command,'packages':len(packages),'go_list_path':str(log),'go_list_sha256':hashlib.sha256(log.read_bytes()).hexdigest()})

entries = []
for key, item in sorted(inputs.items()):
    path = pathlib.Path(key)
    raw = path.read_bytes()
    item.update({'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)})
    for field in ['fields','packages','specs']: item[field] = sorted(item[field])
    if path.is_relative_to(workspace):
        item['source_class'] = 'frozen_workspace'
        item['relative_path'] = str(path.relative_to(workspace))
        item['preserved_path'] = str(path)
    else:
        item['source_class'] = 'toolchain' if '/src/' in str(path) and '/go1.' in str(path) else 'external_dependency'
        destination = base/'external-build-inputs'/str(path).lstrip('/')
        destination.parent.mkdir(parents=True,exist_ok=True)
        destination.write_bytes(raw)
        item['preserved_path'] = str(destination)
    entries.append(item)

go_binary = pathlib.Path(shutil.which('go')).resolve()
manifest = {'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'workspace':str(workspace),'snapshot_manifest_sha256':hashlib.sha256((out/'final-snapshot.json').read_bytes()).hexdigest(),
            'scope':'Files reported as active Go/C/assembly/embed/module inputs by go list for the exact normal, command and race targets; testdata/configuration files are additionally preserved in the workspace snapshot',
            'go_binary':str(go_binary),'go_binary_sha256':hashlib.sha256(go_binary.read_bytes()).hexdigest(),
            'go_version':subprocess.check_output(['go','version'],text=True).strip(),
            'specifications':manifests,'files':entries,'generated_inputs':generated}
path = out/'final-build-inputs.json'
path.write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
changes = [item['path'] for item in entries if hashlib.sha256(pathlib.Path(item['path']).read_bytes()).hexdigest()!=item['sha256']]
assert not changes, changes
print(json.dumps({'files':len(entries),'bytes':sum(item['bytes'] for item in entries),'workspace_files':sum(item['source_class']=='frozen_workspace' for item in entries),'generated_intermediaries':len(generated),'input_changes':changes,'manifest':str(path),'sha256':hashlib.sha256(path.read_bytes()).hexdigest()},ensure_ascii=False))
