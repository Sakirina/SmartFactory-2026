#!/usr/bin/env python3
"""Register the local simulation edge certificate and public keys through the business API."""
import json
import argparse
from pathlib import Path
from urllib.request import Request, urlopen

root = Path(__file__).resolve().parents[1]
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--state-directory',type=Path,default=root/'.local')
parser.add_argument('--cloud-url',default='http://127.0.0.1:8090')
parser.add_argument('--node',default='edge-a')
args=parser.parse_args()
if not args.node.replace('-','').replace('_','').isalnum():raise SystemExit('Invalid node identifier')
credentials = json.loads((args.state_directory / "development-credentials.json").read_text())
registration = json.loads((args.state_directory / ('pki/'+args.node+'.public.json')).read_text())
base = args.cloud_url.rstrip('/')

def call(method, path, value=None, token=""):
    data = None if value is None else json.dumps(value).encode()
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + token}
    with urlopen(Request(base + path, data=data, headers=headers, method=method), timeout=15) as response:
        return json.load(response)

token = call("POST", "/api/sf/v1/login", {"login": "admin", "password": credentials["password"]})["token"]
entity = next(item for item in call("GET", "/api/sf/v1/entities", token=token) if item["id"] == args.node)
if entity.get("config") != registration or entity["status"] != "active":
    entity["config"] = registration
    entity["status"] = "active"
    entity = call("POST", "/api/sf/v1/entities", {"entity": entity, "expected_version": entity["version"]}, token)
print(args.node+" certificate, audit key and credential encryption key enrolled; version", entity["version"])
