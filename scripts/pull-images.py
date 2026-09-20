#!/usr/bin/env python3
"""Pull selected pinned images; default behavior only prints the transfer inventory."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--execute", action="store_true", help="perform the previously reviewed transfer")
parser.add_argument("--only", nargs="*")
args = parser.parse_args()
lock = json.loads((ROOT / "deploy/images.lock.json").read_text())
names = args.only or sorted(lock["images"])
unknown = set(names) - lock["images"].keys()
if unknown:
    raise SystemExit("Unresolved images: " + ", ".join(sorted(unknown)))
layers = {layer["digest"]: layer["bytes"] for name in names for layer in lock["images"][name]["layers"]}
print(f"Selected compressed download (before local cache): {sum(layers.values())/1024/1024:.1f} MiB")
for name in names:
    item = lock["images"][name]
    command = ["docker", "pull", "--platform", item["platform"], item["pinned"]]
    print(" ".join(command), flush=True)
    if args.execute:
        subprocess.run(command, check=True)
