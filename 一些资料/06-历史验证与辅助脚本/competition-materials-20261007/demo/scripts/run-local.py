#!/usr/bin/env python3
"""Start one local service with generated, private development credentials."""
import argparse
import json
import os
from pathlib import Path
import secrets

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("service", choices=["cloud", "edge", "config"])
parser.add_argument("--seed", action="store_true")
parser.add_argument("--native", action="store_true", help="use the enrolled ThingsBoard CE/Edge backend")
parser.add_argument("--sync", action="store_true", help="use the enrolled mutual-TLS cloud/edge synchronization")
parser.add_argument("--config-center", action="store_true", help="subscribe to the independent local configuration service")
parser.add_argument("--binary-dir", default=str(root / "bin") if (root / "bin").is_dir() else "/private/tmp/smartfactory-build/bin")
args = parser.parse_args()
state = root / ".local"
state.mkdir(mode=0o700, exist_ok=True)
path = state / "development-credentials.json"
if not path.exists():
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump({"password": secrets.token_urlsafe(24), "service_token": secrets.token_urlsafe(32)}, stream)
credentials = json.loads(path.read_text())
environment = dict(os.environ)
environment.setdefault("SF_BOOTSTRAP_PASSWORD", credentials["password"])
environment.setdefault("SF_SERVICE_TOKEN", credentials["service_token"])
environment.setdefault("SF_MASTER_KEY_FILE", str(state / (args.service + ".key")))
environment.setdefault("SF_DATABASE", str((state / (args.service + ".db")).resolve()))
environment.setdefault("SF_STATIC_DIR", str(root / "Frontends" / "dist"))
if args.config_center and args.service != "config":
    environment.setdefault("SF_CONFIG_URL", "http://127.0.0.1:8092")
if args.service == "edge":
    environment.setdefault("SF_NODE_ID", "edge-a")
if args.sync:
    pki = state / "pki"
    node = "edge-a" if args.service == "edge" else "cloud-1"
    environment.setdefault("SF_TLS_CA", str(pki / "ca.pem"))
    environment.setdefault("SF_TLS_CERT", str(pki / (node + ".pem")))
    environment.setdefault("SF_TLS_KEY", str(pki / (node + ".key")))
    if args.service == "edge":
        environment.setdefault("SF_SYNC_URL", "https://127.0.0.1:18443")
        environment.setdefault("SF_CLOUD_SIGNING_KEY", json.loads((pki / "cloud-1.public.json").read_text())["audit_public_key"])
        if args.native:
            environment.setdefault("SF_NATIVE_RELAY_LISTEN", "127.0.0.1:17071")
            environment.setdefault("SF_NATIVE_RELAY_TARGET", "127.0.0.1:18444")
    elif args.service == "cloud":
        environment.setdefault("SF_SYNC_LISTEN", "127.0.0.1:18443")
        if args.native:
            environment.setdefault("SF_NATIVE_RELAY_LISTEN", "127.0.0.1:18444")
            environment.setdefault("SF_NATIVE_RELAY_TARGET", "127.0.0.1:17070")
if args.native:
    native = json.loads((state / "tb-bootstrap.json").read_text())
    native_port = 18081 if args.service == "edge" else 18080
    service_port = 8091 if args.service == "edge" else 8090
    environment.setdefault("SF_TB_URL", f"http://127.0.0.1:{native_port}")
    environment.setdefault("SF_TB_USERNAME", native["username"])
    environment.setdefault("SF_TB_PASSWORD", native["password"])
    environment.setdefault("SF_TB_CALLBACK_URL", f"http://host.docker.internal:{service_port}")
binary = str(Path(args.binary_dir) / ("sf-" + args.service))
command = [binary] + (["--seed"] if args.seed else [])
os.chdir(root)
os.execve(binary, command, environment)
