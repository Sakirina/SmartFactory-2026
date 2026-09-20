#!/usr/bin/env python3
"""Resolve Docker Hub manifest metadata without downloading image layers."""
import argparse
import concurrent.futures
import datetime
import hashlib
import json
from pathlib import Path
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
ACCEPT = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])

def fetch(url, headers=None):
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers or {}), timeout=30) as response:
        data = response.read(2_000_001)
        if len(data) > 2_000_000:
            raise ValueError("unexpectedly large registry metadata")
        return json.loads(data), response.headers, data

def resolve(item, platform):
    name, ref = item
    repository, tag = ref.rsplit(":", 1)
    if "/" not in repository:
        repository = "library/" + repository
    query = urllib.parse.urlencode({"service": "registry.docker.io", "scope": f"repository:{repository}:pull"})
    auth, _, _ = fetch("https://auth.docker.io/token?" + query)
    headers = {"Accept": ACCEPT, "Authorization": "Bearer " + auth["token"]}
    url = f"https://registry-1.docker.io/v2/{repository}/manifests/"
    manifest, meta, raw = fetch(url + tag, headers)
    digest = meta.get("Docker-Content-Digest") or "sha256:" + hashlib.sha256(raw).hexdigest()
    os_name, architecture = platform.split("/")
    if "manifests" in manifest:
        matches = [m for m in manifest["manifests"] if m.get("platform", {}).get("os") == os_name and m["platform"].get("architecture") == architecture]
        if not matches:
            raise ValueError(f"{ref} has no {platform} manifest")
        digest = matches[0]["digest"]
        manifest, _, _ = fetch(url + digest, headers)
    layers = [{"digest": layer["digest"], "bytes": layer["size"]} for layer in manifest["layers"]]
    return name, {"reference": ref, "pinned": repository + "@" + digest, "platform": platform, "compressed_bytes": sum(layer["bytes"] for layer in layers), "layers": layers}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=ROOT / "deploy/images.lock.json")
    args = parser.parse_args()
    requirements = json.loads((ROOT / "deploy/dependencies.json").read_text())
    images, errors = {}, {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        futures = {pool.submit(resolve, item, requirements["platform"]): item[0] for item in requirements["images"].items()}
        for future in concurrent.futures.as_completed(futures):
            try:
                name, data = future.result()
                images[name] = data
            except Exception as exc:
                errors[futures[future]] = str(exc)
    unique = {layer["digest"]: layer["bytes"] for image in images.values() for layer in image["layers"]}
    result = {"schema_version": 1, "resolved_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "unique_compressed_bytes": sum(unique.values()), "images": images, "errors": errors}
    args.output.write_text(json.dumps(result, indent=2, ensure_ascii=False) + "\n")
    for name, image in sorted(images.items()):
        print(f"{name}: {image['reference']} {image['compressed_bytes']/1024/1024:.1f} MiB")
    print(f"Unique compressed layers: {sum(unique.values())/1024/1024:.1f} MiB")
    if errors:
        print(json.dumps(errors, ensure_ascii=False))
        raise SystemExit(1)

if __name__ == "__main__":
    main()
