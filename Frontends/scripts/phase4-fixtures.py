#!/usr/bin/env python3
"""Prepare independent browser precision fixtures using official entrypoints and HTTP."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import time
import urllib.request


def private_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def serve(args):
    directory = Path(args.dir).resolve()
    directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    binary = Path(args.binary).resolve(strict=True)
    static = Path(args.static).resolve(strict=True)
    password = secrets.token_urlsafe(32)
    private_json(directory / "private-credentials.json", {"login": "admin", "password": password})
    command = [str(binary), "-listen", "127.0.0.1:" + str(args.port), "-node-id", "precision-" + args.kind,
               "-database", str(directory / "app.db"), "-master-key", str(directory / "master.key"),
               "-static", str(static), "-seed"]
    private_json(directory / "manifest.json", {"kind": args.kind, "pid": os.getpid(), "url": "http://127.0.0.1:" + str(args.port),
                 "created_ms": int(time.time() * 1000), "binary": str(binary), "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                 "command": command, "credentials": "private-credentials.json", "isolation": "new SQLite database; normal production entrypoint"})
    environment = dict(os.environ)
    environment["SF_BOOTSTRAP_PASSWORD"] = password
    os.execve(str(binary), command, environment)


def request(url, token=None, value=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    body = None if value is None else json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode()
    response = urllib.request.urlopen(urllib.request.Request(url, data=body, headers=headers), timeout=30)
    return response.status, json.loads(response.read())


def seed(args):
    directory = Path(args.dir).resolve(strict=True)
    credential = json.loads((directory / "private-credentials.json").read_text())
    manifest = json.loads((directory / "manifest.json").read_text())
    base = (args.url or manifest["url"]).rstrip("/") + "/api/sf/v1"
    _, auth = request(base + "/login", value={"login": "admin", "password": credential["password"]})
    _, entities = request(base + "/entities", auth["token"])
    assert any(entity["id"] == "agv-1" for entity in entities), "Seeded AGV device is absent"
    at = int(time.time() * 1000) - 1000
    message = manifest["kind"] + "-precision-" + str(at)
    values = [("precise_signed", -9007199254740993), ("precise_unsigned", 18446744073709551615),
              ("precise_integer", 9007199254740993), ("precise_float", 12.345678901234567),
              ("precise_string", "9007199254740993"), ("precise_boolean", True),
              ("precise_aggregate", {"average": 9007199254740993, "count": 2, "minimum": 9007199254740992, "maximum": 9007199254740994}),
              ("precise_bad", 99)]
    points = [{"id": message + "-" + key, "message_id": message, "source_id": "precision-source", "source_sequence": at + i,
               "device_id": "agv-1", "key": key, "unit": "count", "value": value, "observed_ms": at + i,
               "received_ms": at + i, "revision": 1, "quality": "BAD" if key == "precise_bad" else "GOOD", "time_source": "device"}
              for i, (key, value) in enumerate(values)]
    body = {"message_id": message, "source_id": "precision-source", "payload_hash": "", "critical": False, "points": points}
    status, result = request(base + "/ingest", auth["token"], body)
    assert status == 200
    raw = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode()
    (directory / "precision-ingest.json").write_bytes(raw)
    private_json(directory / "precision-manifest.json", {"device": "agv-1", "kind": manifest["kind"], "points": points,
                 "http_status": status, "result": result, "payload_sha256": hashlib.sha256(raw).hexdigest(), "normal_ingest": True})
    print(json.dumps({"kind": manifest["kind"], "http_status": status, "point_count": len(points), "directory": str(directory)}, ensure_ascii=False))


parser = argparse.ArgumentParser()
commands = parser.add_subparsers(dest="action", required=True)
command = commands.add_parser("serve-precision")
command.add_argument("--dir", required=True)
command.add_argument("--kind", choices=["cloud", "edge"], required=True)
command.add_argument("--binary", required=True)
command.add_argument("--static", required=True)
command.add_argument("--port", type=int, required=True)
command.set_defaults(run=serve)
command = commands.add_parser("seed-precision")
command.add_argument("--dir", required=True)
command.add_argument("--url")
command.set_defaults(run=seed)
arguments = parser.parse_args()
arguments.run(arguments)
