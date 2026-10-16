#!/usr/bin/env python3
import hashlib,json,pathlib,subprocess
root=pathlib.Path('/path/to/SmartFactory-2026')
snapshot=json.loads((root/'.local/evolution/ai-evidence-a02/final-snapshot.json').read_text())
workspace=pathlib.Path(snapshot['workspace']);base=workspace.parent
binary_dir=base/'bin';binary_dir.mkdir(exist_ok=True)
runner=root/'.local/evolution/ai-evidence-a02/run-verification.py'
records=[]
def build(label,command,binary,kind):
    subprocess.run(['python3',str(runner),'--cwd',str(workspace/'Platform'),label,'--',*command],check=True)
    metadata=json.loads((runner.parent/(label+'.metadata.json')).read_text())
    records.append({'label':label,'kind':kind,'path':str(binary),'sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'bytes':binary.stat().st_size,'command':command,'metadata':str(runner.parent/(label+'.metadata.json')),'compile_inputs_manifest_sha256':metadata['compile_inputs_manifest_sha256']})
    (runner.parent/'final-binaries.json').write_text(json.dumps(records,ensure_ascii=False,indent=2)+'\n')
for name in ['ai','application','a02reviewv2','a02reviewv3','a02reviewv3corrected']:
    binary=binary_dir/(name+'.test')
    build('final-test-build-'+name,['go','test','-p','1','-c','-o',str(binary),'./internal/'+name],binary,'ordinary-test')
for name in ['sf-cloud','sf-edge','sf-contracts','sf-ai-evidence-fixture']:
    binary=binary_dir/name
    build('final-command-build-'+name,['go','build','-p','1','-o',str(binary),'./cmd/'+name],binary,'application-command')
for name in ['ai','application']:
    binary=binary_dir/(name+'.race.test')
    build('final-race-build-'+name,['go','test','-p','1','-race','-c','-o',str(binary),'./internal/'+name],binary,'race-test')
print(json.dumps({'binaries':len(records),'manifest':str(runner.parent/'final-binaries.json'),'sha256':hashlib.sha256((runner.parent/'final-binaries.json').read_bytes()).hexdigest()}))
