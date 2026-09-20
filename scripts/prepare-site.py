#!/usr/bin/env python3
"""Prepare three persistent, mutual-TLS NATS servers without downloading images."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
from datetime import datetime, timezone

parser = argparse.ArgumentParser()
parser.add_argument("--binary-dir", default="/private/tmp/smartfactory-build/bin")
parser.add_argument("--memory", action="store_true", help="prepare separate, disposable NATS stores in bounded tmpfs")
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
site = root / (".local/site-memory" if args.memory else ".local/site")
project = "smartfactory-site-memory" if args.memory else "smartfactory-site"
client_port = 15221 if args.memory else 14221
monitor_port = 19221 if args.memory else 18221
pki = site / "pki"
pki.mkdir(parents=True, mode=0o700, exist_ok=True)
binary = str(Path(args.binary_dir) / "sf-pki")
subprocess.run([binary, "--mode", "init", "--dir", str(pki)], check=True)

def private(path, value):
    encoded = (value if isinstance(value, str) else json.dumps(value, indent=2) + "\n").encode()
    if path.exists() and path.read_bytes() != encoded:
        backup = site / "backups" / datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
        backup.mkdir(parents=True, exist_ok=True)
        shutil.copy2(path, backup / path.name)
    with os.fdopen(os.open(path, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600), "wb") as stream:
        stream.write(encoded)

credentials = site / "credentials.json"
if not credentials.exists():
    private(credentials, {"token": secrets.token_urlsafe(32), "route_password": secrets.token_urlsafe(32)})
auth = json.loads(credentials.read_text())
def nats_config(value, level=0):
    lines = []
    for name, content in value.items():
        if isinstance(content, dict):
            lines.extend(["  " * level + name + " {", nats_config(content, level+1).rstrip(), "  " * level + "}"])
        else:
            lines.append("  " * level + name + ": " + json.dumps(content))
    return "\n".join(lines) + "\n"
services = {}
images = json.loads((root / "deploy/images.lock.json").read_text())["images"]
for index in range(1, 4):
    name = f"nats-{index}"
    subprocess.run([binary, "--dir", str(pki), "--node", name, "--client", "--hosts", f"localhost,127.0.0.1,{name}"], check=True)
    tls = {"cert_file": f"/certs/{name}.pem", "key_file": f"/certs/{name}.key", "ca_file": "/certs/ca.pem", "verify": True, "timeout": 3}
    config = {
        "server_name": name, "listen": "0.0.0.0:4222", "http": "0.0.0.0:8222",
        "client_advertise": f"127.0.0.1:{client_port+index}", "tls": tls,
        "authorization": {"token": auth["token"]},
        "jetstream": {"store_dir": "/data", "max_memory_store": 64 * 1024**2, "max_file_store": 2 * 1024**3},
        "cluster": {"name": project, "listen": "0.0.0.0:6222", "advertise": name+":6222", "tls": tls,
                    "authorization": {"user": "site-router", "password": auth["route_password"]},
                    "routes": [f"nats-route://site-router:{auth['route_password']}@nats-{peer}:6222" for peer in range(1, 4) if peer != index]},
    }
    private(site / (name + ".conf"), nats_config(config))
    services[name] = {
        "image": images["nats"]["pinned"], "platform": "linux/amd64", "pull_policy": "never",
        "restart": "unless-stopped", "command": ["-c", "/config/nats.conf"],
        "ports": [f"127.0.0.1:{client_port+index}:4222", f"127.0.0.1:{monitor_port+index}:8222"],
        "volumes": [f"{site}/{name}.conf:/config/nats.conf:ro", f"{pki}/{name}.pem:/certs/{name}.pem:ro", f"{pki}/{name}.key:/certs/{name}.key:ro", f"{pki}/ca.pem:/certs/ca.pem:ro"],
        "logging": {"driver": "json-file", "options": {"max-size": "10m", "max-file": "3"}},
    }
    if args.memory:
        services[name].update({"tmpfs": ["/data:rw,size=128m"], "mem_limit": "192m", "memswap_limit": "192m", "logging": {"driver": "none"}, "restart": "no"})
    else:
        services[name]["volumes"].append(f"{name}:/data")
for node in ["edge-a", "edge-b", "edge-c"]:
    subprocess.run([binary, "--dir", str(pki), "--node", node, "--client"], check=True)
compose = site / "compose.json" if args.memory else root / "deploy/compose.site.json"
content = {"name": project, "services": services}
if not args.memory:
    content["volumes"] = {f"nats-{index}": {} for index in range(1, 4)}
private(compose, content)
print("Prepared three independent JetStream stores using " + ("bounded tmpfs" if args.memory else "persistent volumes") + ", mutual TLS, and private site credentials.")
