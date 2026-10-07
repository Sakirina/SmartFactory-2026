#!/usr/bin/env python3
"""Upload and register one exact executable through the release administration API."""
import argparse
import getpass
import hashlib
import json
from pathlib import Path
import ssl
import subprocess
from urllib.request import Request, build_opener, HTTPSHandler, ProxyHandler


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle',type=Path,required=True)
    parser.add_argument('--directory',type=Path,required=True,help='prepared private deployment directory')
    parser.add_argument('--cloud-url',help='default is the prepared HTTPS cloud portal')
    parser.add_argument('--artifact',type=Path,help='default is bundle/bin/sf-edge')
    parser.add_argument('--build-info',type=Path,help='saved -build-info output for a different preparation host architecture')
    parser.add_argument('--login',default='admin')
    parser.add_argument('--password-file',type=Path,help='private current password file; bootstrap password is used only for admin when omitted')
    args=parser.parse_args()
    bundle,work=args.bundle.resolve(),args.directory.resolve()
    profile=json.loads((work/'profile.json').read_text())
    artifact=(args.artifact or bundle/'bin/sf-edge').resolve()
    raw=artifact.read_bytes()
    if not raw or len(raw)>256*1024*1024:raise SystemExit('Artifact must contain 1 byte to 256 MiB')
    digest=hashlib.sha256(raw).hexdigest()
    identity=json.loads(args.build_info.read_text() if args.build_info else subprocess.check_output([str(artifact),'-build-info'],text=True))
    if identity.get('sha256')!=digest:raise SystemExit('Executable digest differs from its -build-info identity')
    build=identity.get('build',{})
    if build.get('program')!='edge':raise SystemExit('This deployment entrypoint registers an Edge executable')
    manifest=bundle/'release-manifest.json'
    if artifact==bundle/'bin/sf-edge' and manifest.is_file():
        release=json.loads(manifest.read_text())
        expected=release['files']['bin/sf-edge']
        if expected['sha256']!=digest or build.get('version')!=release['application_version'] or build.get('source_sha256')!=release['source_sha256']:
            raise SystemExit('Executable or build metadata differs from the selected release bundle')
    if args.password_file:password=args.password_file.read_text().strip()
    elif args.login=='admin':password=json.loads((work/'deployment-credentials.json').read_text())['password']
    else:password=getpass.getpass('Current account password: ')
    base=(args.cloud_url or profile['urls'][0]).rstrip('/')
    context=ssl.create_default_context(cafile=str(work/'pki/ca.pem'))
    opener=build_opener(ProxyHandler({}),HTTPSHandler(context=context))
    def request(path,method='GET',value=None,token='',content=None):
        headers={'Content-Type':'application/octet-stream' if content is not None else 'application/json'}
        if token:headers['Authorization']='Bearer '+token
        data=content if content is not None else None if value is None else json.dumps(value).encode()
        with opener.open(Request(base+path,data=data,method=method,headers=headers),timeout=60) as response:return json.load(response)
    token=request('/api/sf/v1/login','POST',{'login':args.login,'password':password})['token']
    uploaded=request('/api/sf/v1/release-artifacts/'+digest,'PUT',token=token,content=raw)
    registered=request('/api/sf/v1/release-artifacts','POST',{'sha256':digest,'build':build},token)
    if uploaded['sha256']!=digest or uploaded['size']!=len(raw) or registered['sha256']!=digest or registered['build']!=build:
        raise SystemExit('Registered artifact differs from the supplied executable')
    print(json.dumps({'sha256':digest,'size':len(raw),'build':build},ensure_ascii=False,indent=2))


if __name__=='__main__':main()
