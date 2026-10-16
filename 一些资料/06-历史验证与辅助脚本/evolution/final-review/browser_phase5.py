#!/usr/bin/env python3
import importlib.util,json,pathlib,sys,hashlib,os
ROOT=pathlib.Path('/path/to/SmartFactory-2026')
BASE=pathlib.Path('/private/tmp/smartfactory-final-independent-20261007')
HERE=pathlib.Path(__file__).resolve()
spec=importlib.util.spec_from_file_location('browser',HERE.with_name('browser_review.py'));B=importlib.util.module_from_spec(spec);spec.loader.exec_module(B)
B.OUT=BASE/'phase5-browser/runs'
phase,attempt=sys.argv[1:3]
ready=json.loads((BASE/'phase5-browser/ready.json').read_text())
report=B.OUT/(phase+'-'+attempt);report.mkdir(parents=True,exist_ok=False)
evidence=BASE/'phase5-browser'/('evidence-'+phase+'-'+attempt)
script={'release':'phase5-release.spec.ts','initial-fixed':'phase5-first-publish.spec.ts','navigation':'phase5-navigation.spec.ts','permissions':'phase5-permissions.spec.ts','permissions-editor':'phase5-permissions.spec.ts','identity-basis':'phase5-identity-basis.spec.ts','pipeline':'phase5-pipeline.spec.ts','node':'phase5-node.spec.ts','credentials':'phase5-credentials.spec.ts','template-basis-fixed':'phase5-template-basis.spec.ts','templates':'phase5-templates.spec.ts','template-adoption':'phase5-template-adoption.spec.ts'}[phase]
env={'SF_PHASE5_CASES':phase,'SF_PHASE5_DIRECTORY':str(BASE/'phase5-browser'),'SF_PHASE5_EVIDENCE':str(evidence),'SF_PHASE5_OUTPUT':str(report/'test-results'),'SF_PHASE5_REPORT':str(report/'results.json'),'SF_PHASE5_RELEASE_CLOUD':ready['release_cloud'],'SF_PHASE5_RELEASE_EDGE':ready['release_edge'],'SF_PHASE5_NODE_CLOUD':ready['node_cloud'],'SF_PHASE5_NODE_EDGE':ready['node_edge'],'SF_PHASE5_RELEASE_FIXTURE':ready['editor_fixture'] if phase=='permissions-editor' else ready['release_fixture'],'SF_PHASE5_NODE_FIXTURE':ready['node_fixture'],'SF_PHASE5_RELEASE_PROCESS_ROOT':str(BASE/'release-runtime'),'SF_PHASE5_NODE_PROCESS_ROOT':str(BASE/'node-runtime-v2')}
(report/'wrapper.py').write_bytes(HERE.read_bytes())
(report/'wrapper-input.json').write_text(json.dumps({'path':str(report/'wrapper.py'),'original':str(HERE),'sha256':hashlib.sha256(HERE.read_bytes()).hexdigest(),'ready':str(BASE/'phase5-browser/ready.json')},indent=2)+'\n')
command=[str(B.NODE),str(B.WS/'Frontends/node_modules/@playwright/test/cli.js'),'test','-c','phase5.playwright.config.ts',script]
if os.environ.get('SF_FINAL_GREP'): command += ['--grep', os.environ['SF_FINAL_GREP']]
raise SystemExit(B.run('phase5-'+phase+'-'+attempt,command,env,timeout=900))
