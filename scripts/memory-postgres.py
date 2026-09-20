#!/usr/bin/env python3
"""Start or stop the disposable PostgreSQL fixture stored in a bounded tmpfs."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import time
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[1]
NAME = "smartfactory-memory-postgres"
PRIVATE = ROOT / ".local" / "memory-postgres.json"
LABEL = "smartfactory.fixture=memory-postgres"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["start", "stop", "status"])
    args = parser.parse_args()
    found = subprocess.run(["docker", "inspect", "--format", '{{ index .Config.Labels "smartfactory.fixture" }}', NAME], text=True, capture_output=True)
    if found.returncode == 0 and found.stdout.strip() != "memory-postgres":
        raise SystemExit("Existing container has a different owner label")
    if args.action == "status":
        print("fixture exists" if found.returncode == 0 else "fixture is stopped")
        return
    if args.action == "stop":
        if found.returncode == 0:
            subprocess.run(["docker", "stop", "--timeout", "20", NAME], check=True, stdout=subprocess.DEVNULL)
        print("Memory PostgreSQL stopped; its temporary dataset is discarded")
        return
    if found.returncode == 0:
        print("Memory PostgreSQL already exists on loopback port 54322")
        return
    compose = json.loads((ROOT / "deploy" / "compose.native.json").read_text())
    image = compose["services"]["cloud-db"]["image"]
    password = secrets.token_urlsafe(32)
    environment = os.environ.copy()
    environment["POSTGRES_PASSWORD"] = password
    subprocess.run(["docker", "run", "--detach", "--rm", "--pull", "never", "--name", NAME,
                    "--label", LABEL, "--memory", "1g", "--memory-swap", "1g", "--shm-size", "128m",
                    "--tmpfs", "/var/lib/postgresql/data:rw,size=512m", "--tmpfs", "/tmp:rw,size=64m",
                    "--publish", "127.0.0.1:54322:5432", "--env", "POSTGRES_PASSWORD",
                    "--env", "POSTGRES_USER=smartfactory", "--env", "POSTGRES_DB=fixture",
                    "--log-driver", "none", image, "postgres", "-c", "shared_buffers=64MB",
                    "-c", "max_wal_size=128MB", "-c", "min_wal_size=32MB"], env=environment, check=True, stdout=subprocess.DEVNULL)
    until = time.monotonic() + 45
    while time.monotonic() < until:
        ready = subprocess.run(["docker", "exec", NAME, "pg_isready", "-h", "127.0.0.1", "-U", "smartfactory", "-d", "fixture"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if ready.returncode == 0:
            break
        time.sleep(0.5)
    else:
        raise SystemExit("Memory PostgreSQL did not become ready")
    value = {"dsn": "postgres://smartfactory:" + quote(password) + "@127.0.0.1:54322/fixture?sslmode=disable",
             "container": NAME, "data_limit_bytes": 512 * 1024**2, "container_memory_limit_bytes": 1024**3,
             "storage": "tmpfs", "image": image}
    PRIVATE.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(PRIVATE, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as out:
        json.dump(value, out, indent=2)
    print("Memory PostgreSQL ready on loopback port 54322; data limit 512 MiB, container limit 1 GiB")


if __name__ == "__main__":
    main()
